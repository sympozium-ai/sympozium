package controller_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	ctrl "sigs.k8s.io/controller-runtime"
)

// The optional external mode starts the actual controller entrypoint using
// only the scoped identity. Provisioning/owner processes remain separate.
func startLiveParentManager(t *testing.T, manager ctrl.Manager, config, operator *rest.Config, namespace, registrations, approvals string) (func(), <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	binary := os.Getenv("CELLN_INTEROP_CONTROLLER_BINARY")
	if binary == "" {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { done <- manager.Start(ctx) }()
		return cancel, done
	}
	if !filepath.IsAbs(binary) || config.BearerToken == "" || os.Getenv("CELLN_INTEROP_SCOPED_CONTROLLER") != "true" {
		t.Fatal("absolute controller binary and scoped token config required")
	}
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "controller-token")
	if err := os.WriteFile(tokenPath, []byte(config.BearerToken), 0600); err != nil {
		t.Fatal(err)
	}
	issuer, err := kubernetes.NewForConfig(operator)
	if err != nil {
		t.Fatal(err)
	}
	kubeconfig := clientcmdapi.Config{Clusters: map[string]*clientcmdapi.Cluster{"proof": {Server: config.Host, CertificateAuthority: config.CAFile, CertificateAuthorityData: config.CAData, TLSServerName: config.ServerName}}, AuthInfos: map[string]*clientcmdapi.AuthInfo{"controller": {TokenFile: tokenPath}}, Contexts: map[string]*clientcmdapi.Context{"proof": {Cluster: "proof", AuthInfo: "controller", Namespace: namespace}}, CurrentContext: "proof"}
	raw, err := clientcmd.Write(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "kubeconfig")
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(directory, "controller.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--celln-parent-only", "--watch-namespace", namespace, "--leader-elect=false", "--metrics-bind-address=0", "--health-probe-bind-address=0")
	command.Env = []string{"PATH=/usr/bin:/bin", "KUBECONFIG=" + configPath, "CELLN_PARENT_CONFIG=" + approvals, "CELLN_PARENT_REGISTRATIONS=" + registrations}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	renewCtx, stopRenewal := context.WithCancel(context.Background())
	renewed := make(chan error, 1)
	go func() {
		renewed <- renewParentToken(renewCtx, tokenPath, func(ctx context.Context) (string, time.Time, error) {
			seconds := int64(600)
			answer, err := issuer.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, "celln-parent-controller", &authnv1.TokenRequest{Spec: authnv1.TokenRequestSpec{ExpirationSeconds: &seconds}}, metav1.CreateOptions{})
			if err != nil {
				return "", time.Time{}, err
			}
			return answer.Status.Token, answer.Status.ExpirationTimestamp.Time, nil
		})
	}()
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	finished := make(chan struct{})
	go func() {
		var err error
		select {
		case err = <-exited:
			stopRenewal()
			if renewalErr := <-renewed; renewalErr != nil && err == nil {
				err = renewalErr
			}
		case err = <-renewed:
			stopRenewal()
			_ = command.Process.Signal(syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(4 * time.Second):
				_ = command.Process.Kill()
				<-exited
			}
		}
		log.Close()
		close(finished)
		if err != nil {
			err = fmt.Errorf("external controller exit: %w; log %s", err, logPath)
		}
		done <- err
	}()
	stop := func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		go func() {
			select {
			case <-finished:
			case <-time.After(4 * time.Second):
				_ = command.Process.Kill()
			}
		}()
	}
	t.Logf("separate parent-only controller process started (PID %d) with scoped token-file kubeconfig", command.Process.Pid)
	return stop, done
}
