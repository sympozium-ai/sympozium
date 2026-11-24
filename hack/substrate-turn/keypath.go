package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

// The key path (checkpoint 3). The harness gateway is the real one; what it
// reads (AgentRun, Agent, the Agent's key Secret, the run's binding) is held
// in memory here instead of in a cluster, because the spike cluster's
// controller would otherwise start the same runs as Jobs. The provider is a
// stub on loopback that answers only a request carrying the Agent's key, so a
// turn succeeds only if the gateway added it.

const keyPathNamespace = "spike"

type keyPath struct {
	objects client.Client
	gateway http.Handler
	agent   *api.Agent
	keyUID  types.UID
	origin  string
	// realKey never leaves this process except towards the provider stub.
	realKey string

	mu        sync.Mutex
	withKey   int
	otherAuth []string
}

func newKeyPath(actors harnessgateway.ActorVerifier) (*keyPath, error) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := api.AddToScheme(scheme); err != nil {
		return nil, err
	}
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	k := &keyPath{realKey: "sk-spike-" + hex.EncodeToString(raw), keyUID: "key-uid"}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	k.origin = "http://" + listener.Addr().String()
	go func() { _ = http.Serve(listener, http.HandlerFunc(k.provider)) }()

	profiles, err := harnessgateway.ParseSet([]byte(fmt.Sprintf(`
allowedEnvNames: [OPENAI_BASE_URL, OPENAI_API_KEY]
allowedAuthHeaders: [Authorization]
insecureOrigins: [%q]
profiles:
  - name: openai
    providers: [openai]
    upstream: %s
    secretKeys: [OPENAI_API_KEY]
    auth: {type: header, header: Authorization, prefix: "Bearer "}
    paths: [/v1/chat/completions]
    env: {baseURL: [OPENAI_BASE_URL], apiKey: [OPENAI_API_KEY]}
    clientBasePath: /v1
`, k.origin, k.origin)))
	if err != nil {
		return nil, err
	}
	if len(profiles.Rejected) > 0 {
		return nil, fmt.Errorf("profile rejected: %v", profiles.Rejected)
	}

	k.agent = &api.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "spike-agent", Namespace: keyPathNamespace, UID: "agent-uid"},
		Spec:       api.AgentSpec{AuthRefs: []api.SecretRef{{Provider: "openai", Secret: "spike-agent-key"}}},
	}
	key := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "spike-agent-key", Namespace: keyPathNamespace, UID: k.keyUID, Annotations: map[string]string{
			modelkey.OwnerAnnotation:  modelkey.Owner(k.agent),
			modelkey.OriginAnnotation: k.origin,
		}},
		Data: map[string][]byte{"OPENAI_API_KEY": []byte(k.realKey)},
	}
	k.objects = fake.NewClientBuilder().WithScheme(scheme).WithObjects(k.agent, key).WithStatusSubresource(&api.AgentRun{}).Build()
	k.gateway = (&harnessgateway.Gateway{Reader: k.objects, Profiles: staticProfiles{profiles}, Actors: actors}).Handler()
	return k, nil
}

type staticProfiles struct{ set *harnessgateway.Set }

func (s staticProfiles) Load(context.Context) (*harnessgateway.Set, error) { return s.set, nil }

// bind records a Running AgentRun for the Agent and binds it to one actor, as
// the controller will when it starts a run on Substrate.
func (k *keyPath) bind(ctx context.Context, run, actor string) error {
	agentRun := &api.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: run, Namespace: keyPathNamespace, UID: types.UID("uid-" + run)},
		Spec: api.AgentRunSpec{AgentRef: k.agent.Name,
			Model: api.ModelSpec{Provider: "openai", Model: "stub", AuthSecretRef: "spike-agent-key"}},
	}
	if err := k.objects.Create(ctx, agentRun); err != nil {
		return err
	}
	agentRun.Status.Phase = api.AgentRunPhaseRunning
	if err := k.objects.Status().Update(ctx, agentRun); err != nil {
		return err
	}
	return k.objects.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: harnessgateway.RunTokenSecretName(run), Namespace: keyPathNamespace,
		Annotations: map[string]string{
			harnessgateway.AgentUIDAnnotation:     string(k.agent.UID),
			harnessgateway.KeySecretUIDAnnotation: string(k.keyUID),
			harnessgateway.OriginAnnotation:       k.origin,
			harnessgateway.ActorAnnotation:        actor,
		},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "AgentRun", Name: run, UID: agentRun.UID, Controller: ptr.To(true)}},
	}})
}

func (k *keyPath) provider(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	k.mu.Lock()
	if auth != "Bearer "+k.realKey {
		// Recorded by shape only; a JWT arriving here would be a finding.
		k.otherAuth = append(k.otherAuth, fmt.Sprintf("%d bytes, %d dots", len(auth), strings.Count(auth, ".")))
		k.mu.Unlock()
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
		return
	}
	k.withKey++
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "chatcmpl-stub", "object": "chat.completion", "created": time.Now().Unix(), "model": "stub",
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": "the provider saw the Agent's key"}}},
		"usage": map[string]int{"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12},
	})
}

func (k *keyPath) counts() (withKey int, other []string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.withKey, append([]string(nil), k.otherAuth...)
}
