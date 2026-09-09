package cellnparent

import (
	"context"
	"os"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func coordinatorStore(t *testing.T, ctx context.Context, run *api.AgentRun) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	selected := os.Getenv("CELLN_INTEROP_KUBE_CONTEXT")
	if selected == "" {
		return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}, &api.AgentRunTurn{}).WithObjects(run).Build()
	}
	// This destructive-cleanup test may only target the explicitly selected
	// local development cluster, never the current context implicitly.
	if selected != "kind-celln-deployed" {
		t.Fatal("live coordinator proof restricted to kind-celln-deployed")
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: selected}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	store, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "celln-parent-interop-"}}
	if err := store.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated proof namespace: %s", ns.Name)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		uid := ns.UID
		if err := store.Delete(cleanup, ns, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
			t.Errorf("proof namespace cleanup %s: %v", ns.Name, err)
		}
	})
	run.Namespace = ns.Name
	run.UID = ""
	run.Spec.AgentID = "primary"
	run.Spec.SessionKey = "parent-interop"
	run.Spec.Model = api.ModelSpec{Provider: "deepseek", Model: "deepseek-chat"}
	if err := store.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
		t.Fatal(err)
	}
	if run.UID == "" {
		t.Fatal("API server did not assign a run UID")
	}
	return store
}
