package modelkey

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

const ns = "team"

func agent(name, secret string) *api.Agent {
	a := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	if secret != "" {
		a.Spec.AuthRefs = []api.SecretRef{{Provider: "openai", Secret: secret}}
	}
	return a
}

func member(name, ensemble, secret string) *api.Agent {
	a := agent(name, secret)
	a.Labels = map[string]string{ensembleLabel: ensemble}
	a.OwnerReferences = []metav1.OwnerReference{{APIVersion: "sympozium.ai/v1alpha1", Kind: "Ensemble", Name: ensemble, UID: "e", Controller: ptr.To(true)}}
	return a
}

func secret(name, owner string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: map[string][]byte{"OPENAI_API_KEY": []byte("k")}}
	if owner != "" {
		s.Annotations = map[string]string{OwnerAnnotation: owner}
	}
	return s
}

func store(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func ownerOf(t *testing.T, c client.Client, name string) string {
	t.Helper()
	var s corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	return s.Annotations[OwnerAnnotation]
}

func TestFirstGrantingAgentClaimsTheKey(t *testing.T) {
	a := agent("hermes-a", "key-a")
	c := store(t, a, secret("key-a", ""))
	if err := Authorize(context.Background(), c, a, "openai", "key-a"); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, c, "key-a"); got != "Agent/hermes-a" {
		t.Fatalf("owner = %q", got)
	}
	// Its later runs (and its spawned sub-agents, which run as the same Agent) pass.
	if err := Authorize(context.Background(), c, a, "openai", "key-a"); err != nil {
		t.Fatal(err)
	}
}

func TestASecondAgentCannotShareAKey(t *testing.T) {
	a, b := agent("hermes-a", "shared"), agent("pi-b", "shared")
	c := store(t, a, b, secret("shared", "Agent/hermes-a"))
	err := Authorize(context.Background(), c, b, "openai", "shared")
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Owner != "Agent/hermes-a" || !IsRefusal(err) {
		t.Fatalf("err = %v, want a conflict naming the owner", err)
	}
	if Check(context.Background(), c, b, "openai", "shared") == nil {
		t.Fatal("admission check missed the conflict")
	}
}

func TestAnUngrantedKeyIsRefusedAndNotClaimed(t *testing.T) {
	a := agent("rogue", "own-key")
	c := store(t, a, secret("someone-elses", ""))
	err := Authorize(context.Background(), c, a, "openai", "someone-elses")
	var notGranted *NotGrantedError
	if !errors.As(err, &notGranted) {
		t.Fatalf("err = %v, want not granted", err)
	}
	if got := ownerOf(t, c, "someone-elses"); got != "" {
		t.Fatalf("an ungranted key was claimed by %q", got)
	}
}

func TestAProviderSpecificGrantDoesNotCoverAnotherProvider(t *testing.T) {
	a := agent("hermes-a", "key-a")
	c := store(t, a, secret("key-a", ""))
	if err := Authorize(context.Background(), c, a, "anthropic", "key-a"); !IsRefusal(err) {
		t.Fatalf("err = %v, want refusal", err)
	}
}

func TestAStaleClaimIsTakenOver(t *testing.T) {
	a := agent("new-owner", "key")
	c := store(t, a, secret("key", "Agent/deleted-agent"))
	if err := Authorize(context.Background(), c, a, "openai", "key"); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, c, "key"); got != "Agent/new-owner" {
		t.Fatalf("owner = %q", got)
	}
}

func TestEnsembleMembersShareTheirTeamKey(t *testing.T) {
	ensemble := &api.Ensemble{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "research"}}
	lead, writer := member("lead", "research", "team-key"), member("writer", "research", "team-key")
	c := store(t, ensemble, lead, writer, secret("team-key", ""))
	if err := Authorize(context.Background(), c, lead, "openai", "team-key"); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, c, "team-key"); got != "Ensemble/research" {
		t.Fatalf("owner = %q", got)
	}
	if err := Authorize(context.Background(), c, writer, "openai", "team-key"); err != nil {
		t.Fatalf("a member could not use the team key: %v", err)
	}
	// A persona delegated to with the lead's key needs no authRefs of its own.
	reviewer := member("reviewer", "research", "")
	if err := Authorize(context.Background(), c, reviewer, "openai", "team-key"); err != nil {
		t.Fatalf("a delegated persona could not inherit: %v", err)
	}
	// An Agent outside the team cannot, even if it lists the Secret.
	outsider := agent("outsider", "team-key")
	if err := Authorize(context.Background(), c, outsider, "openai", "team-key"); !IsRefusal(err) {
		t.Fatalf("err = %v, want conflict", err)
	}
}

func TestALabelAloneDoesNotJoinAnEnsemble(t *testing.T) {
	impostor := agent("impostor", "")
	impostor.Labels = map[string]string{ensembleLabel: "research"}
	if Owner(impostor) != "Agent/impostor" {
		t.Fatalf("owner = %q; membership needs the Ensemble's controller reference", Owner(impostor))
	}
}

func TestNoKeyAndMissingSecretsPass(t *testing.T) {
	a := agent("local", "")
	c := store(t, a)
	if err := Authorize(context.Background(), c, a, "llama-server", ""); err != nil {
		t.Fatal(err)
	}
	if err := Authorize(context.Background(), c, a, "openai", "not-created-yet"); err != nil {
		t.Fatalf("a missing Secret should fail later, not here: %v", err)
	}
}

func TestOwnedBy(t *testing.T) {
	if !OwnedBy(secret("k", "Agent/a"), agent("a", "")) || OwnedBy(secret("k", "Agent/a"), agent("b", "")) || OwnedBy(secret("k", ""), agent("a", "")) {
		t.Fatal("OwnedBy")
	}
}
