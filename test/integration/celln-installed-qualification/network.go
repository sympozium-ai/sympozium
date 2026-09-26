package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	core "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

type observation struct {
	CurlExit int `json:"curlExit"`
	HTTP     struct {
		Code     string `json:"httpCode"`
		RemoteIP string `json:"remoteIP"`
	} `json:"http"`
}

func probeCommand(address string) []string {
	// Plaintext, credential-free request to the TLS listener deliberately returns
	// HTTP 400. We test packet reachability, not authentication or TLS acceptance.
	// remoteIP must remain empty for a denied connect; an HTTP timeout is not proof.
	return []string{"/bin/sh", "-c", fmt.Sprintf(`result=$(curl --disable --noproxy '*' --silent --connect-timeout 2 --max-time 3 --output /dev/null --write-out '{"httpCode":"%%{http_code}","remoteIP":"%%{remote_ip}"}' 'http://%s:8443/'); code=$?; printf '{"curlExit":%%d,"http":%%s}\n' "$code" "$result"`, address)}
}
func (h *harness) network(ctx context.Context) error {
	ns := "celln-review-a-" + h.review
	providers, err := h.admin.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=review-provider-" + h.review})
	if err != nil || len(providers.Items) != 1 {
		return errors.New("provider endpoint not unique")
	}
	provider := providers.Items[0]
	address, err := netip.ParseAddr(provider.Status.PodIP)
	if err != nil || !address.IsPrivate() || !address.Is4() {
		return errors.New("private fixture Pod IP required")
	}
	policies, err := h.admin.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	selected := 0
	for _, policy := range policies.Items {
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
		if err != nil {
			return err
		}
		if !selector.Matches(labels.Set(provider.Labels)) {
			continue
		}
		ingress := false
		for _, kind := range policy.Spec.PolicyTypes {
			ingress = ingress || kind == networking.PolicyTypeIngress
		}
		if !ingress {
			continue
		}
		selected++
		if policy.Name != "review-provider-isolation-"+h.review || len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 1 {
			return errors.New("unexpected ingress policy union")
		}
		from := policy.Spec.Ingress[0].From[0]
		if from.PodSelector == nil || from.PodSelector.MatchLabels["app"] != "celln-review-gateway-"+h.review || from.NamespaceSelector == nil || from.NamespaceSelector.MatchLabels["sympozium.ai/celln-review"] != h.review {
			return errors.New("unexpected provider isolation policy")
		}
	}
	if selected != 1 {
		return errors.New("exact provider ingress isolation policy required")
	}
	system := "celln-review-system-" + h.review
	gateways, err := h.admin.CoreV1().Pods(system).List(ctx, metav1.ListOptions{LabelSelector: "app=celln-review-gateway-" + h.review})
	if err != nil || len(gateways.Items) != 1 {
		return errors.New("gateway endpoint not unique")
	}
	command := probeCommand(address.String())
	control, err := h.execProbe(ctx, system, gateways.Items[0].Name, "gateway", command)
	if err != nil {
		return err
	}
	reachable := func(value observation) bool {
		return value.CurlExit == 0 && value.HTTP.Code == "400" && value.HTTP.RemoteIP == address.String()
	}
	h.record("network/allowed-control-before", reachable(control), "existing gateway pod reaches fixture listener without credentials")
	if !reachable(control) {
		return errors.New("network positive control unavailable")
	}
	zero := int64(65532)
	deadline := int64(20)
	pod, err := h.admin.CoreV1().Pods(ns).Create(ctx, &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "qualification-network-" + h.report.Epoch, Labels: map[string]string{"app": "qualification-network"}}, Spec: core.PodSpec{AutomountServiceAccountToken: boolp(false), RestartPolicy: core.RestartPolicyNever, ActiveDeadlineSeconds: &deadline, SecurityContext: &core.PodSecurityContext{RunAsNonRoot: boolp(true), RunAsUser: &zero, RunAsGroup: &zero, SeccompProfile: &core.SeccompProfile{Type: core.SeccompProfileTypeRuntimeDefault}}, Containers: []core.Container{{Name: "probe", Image: h.image, ImagePullPolicy: core.PullIfNotPresent, Command: command, SecurityContext: &core.SecurityContext{AllowPrivilegeEscalation: boolp(false), ReadOnlyRootFilesystem: boolp(true), Capabilities: &core.Capabilities{Drop: []core.Capability{"ALL"}}}}}}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if h.admin.CoreV1().Pods(ns).Delete(context.Background(), pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}}) != nil {
			h.report.RecoveryRequired = true
		}
	}()
	deadlineAt := time.Now().Add(45 * time.Second)
	for {
		current, err := h.admin.CoreV1().Pods(ns).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.UID != pod.UID {
			return errors.New("network probe identity changed")
		}
		if current.Status.Phase == core.PodSucceeded {
			break
		}
		if current.Status.Phase == core.PodFailed || time.Now().After(deadlineAt) {
			return errors.New("network probe did not complete")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	data, err := h.admin.CoreV1().Pods(ns).GetLogs(pod.Name, &core.PodLogOptions{Container: "probe"}).DoRaw(ctx)
	if err != nil || len(data) > 4096 {
		return errors.New("bounded probe output unavailable")
	}
	var observed observation
	if json.Unmarshal(data, &observed) != nil {
		return errors.New("invalid network observation")
	}
	denied := observed.CurlExit == 28 && observed.HTTP.Code == "000" && observed.HTTP.RemoteIP == ""
	h.record("network/tenant-to-provider-denied", denied, fmt.Sprintf("credential-free tenant pod: curl exit %d, HTTP %s, connected=%t; timeout without a connected peer required", observed.CurlExit, observed.HTTP.Code, observed.HTTP.RemoteIP != ""))
	control, err = h.execProbe(ctx, system, gateways.Items[0].Name, "gateway", command)
	if err != nil {
		return err
	}
	h.record("network/allowed-control-after", reachable(control), "positive control repeated after deny probe")
	return nil
}
func (h *harness) execProbe(ctx context.Context, namespace, pod, container string, command []string) (observation, error) {
	var result observation
	request := h.admin.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(namespace).SubResource("exec").VersionedParams(&core.PodExecOptions{Container: container, Command: command, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(h.config, "POST", request.URL())
	if err != nil {
		return result, err
	}
	var stdout, stderr bytes.Buffer
	if executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}) != nil || stdout.Len() > 4096 {
		return result, errors.New("bounded control probe failed")
	}
	if json.Unmarshal(stdout.Bytes(), &result) != nil {
		return result, errors.New("invalid control probe observation")
	}
	return result, nil
}
