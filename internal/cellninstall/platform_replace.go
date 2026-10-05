package cellninstall

import (
	"context"
	"fmt"
	"strings"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Moving a scope to a new package (approved with --celln-fleet-replace-package)
// replaces the scope's cluster catalogue and rebinds the platform wrappers
// every namespace holds. Every live parent was already lost when the nodes
// restarted on the new package, so nothing here waits for runs to drain.

// replaceObjectTimeout bounds how long a replaced object may take to go away.
const replaceObjectTimeout = 60 * time.Second

// replacePlatformObject swaps existing for object. Runtime profiles and
// cluster tools are immutable, so every catalogue object is deleted and
// created again rather than updated.
func replacePlatformObject(ctx context.Context, store client.Client, object, existing client.Object) error {
	if err := store.Delete(ctx, existing, client.Preconditions{UID: ptr(existing.GetUID())}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("replace %s: %w", object.GetName(), err)
	}
	deadline := time.Now().Add(replaceObjectTimeout)
	for {
		object.SetResourceVersion("")
		err := store.Create(ctx, object)
		if err == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(err) || time.Now().After(deadline) {
			return fmt.Errorf("replace %s: %w", object.GetName(), err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func ptr[T any](v T) *T { return &v }

// retirePlatformCatalogue removes what the replaced publication installed and
// the new one no longer carries: runtime profiles and tools of the old
// package, and the old scope's policy when the scope itself changed. Only
// objects annotated with the replaced package are touched.
func retirePlatformCatalogue(ctx context.Context, store client.Client, old FleetPublication, scope string, current []client.Object, policyName string) error {
	keep := map[string]bool{}
	for _, object := range current {
		keep[object.GetName()] = true
	}
	scopes := []string{scope}
	if old.Scope != "" && old.Scope != scope {
		scopes = append(scopes, old.Scope)
	}
	retired := func(name string, annotations map[string]string, prefix func(string) string) bool {
		if keep[name] || annotations[packageAnnotation] == "" || annotations[packageAnnotation] != old.Package {
			return false
		}
		for _, s := range scopes {
			if strings.HasPrefix(name, prefix(s)) {
				return true
			}
		}
		return false
	}
	var profiles api.CellnRuntimeProfileList
	if err := store.List(ctx, &profiles); err != nil {
		return err
	}
	for i := range profiles.Items {
		p := &profiles.Items[i]
		if retired(p.Name, p.Annotations, func(s string) string { return PlatformProfileName(s, cellnplatform.DefaultBackend) }) {
			if err := deleteIgnoringMissing(ctx, store, p); err != nil {
				return err
			}
		}
	}
	var tools api.ClusterCellnToolList
	if err := store.List(ctx, &tools); err != nil {
		return err
	}
	for i := range tools.Items {
		t := &tools.Items[i]
		if retired(t.Name, t.Annotations, func(s string) string { return "celln-" + s + "-" }) {
			if err := deleteIgnoringMissing(ctx, store, t); err != nil {
				return err
			}
		}
	}
	if old.Scope != "" && old.Scope != scope {
		_, oldPolicy, _ := PlatformCatalogueNames(old.Scope)
		if oldPolicy != policyName {
			var policy api.CellnExecutionPolicy
			err := store.Get(ctx, types.NamespacedName{Name: oldPolicy}, &policy)
			if err == nil && policy.Annotations[packageAnnotation] != "" {
				if err := deleteIgnoringMissing(ctx, store, &policy); err != nil {
					return err
				}
			} else if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func deleteIgnoringMissing(ctx context.Context, store client.Client, object client.Object) error {
	if err := store.Delete(ctx, object); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("retire %s: %w", object.GetName(), err)
	}
	return nil
}

// rebindTenantWrappers points every namespace's platform-managed wrappers at
// the new profiles: the runtime wrapper's profile reference and the model
// connection's route and credential profile. Wrappers a namespace prepared
// itself (without the managed-by label) are left alone, as are wrappers for
// a backend the new publication does not carry.
func rebindTenantWrappers(ctx context.Context, store client.Client, profiles map[string]*api.CellnRuntimeProfile, policy *api.CellnExecutionPolicy, mediatedOnly map[string]bool) error {
	var runtimes api.AgentRuntimeList
	if err := store.List(ctx, &runtimes, client.MatchingLabels{cellnplatform.ManagedByLabel: cellnplatform.ManagedByValue}); err != nil {
		return err
	}
	for i := range runtimes.Items {
		runtime := &runtimes.Items[i]
		profile := profiles[runtime.Labels[cellnplatform.BackendLabel]]
		if profile == nil || runtime.Spec.CellnProfileRef == nil {
			continue
		}
		backend := runtime.Labels[cellnplatform.BackendLabel]
		wanted, err := platformWrappers(runtime.Namespace, profile, policy, mediatedOnly[backend])
		if err != nil {
			return err
		}
		for _, object := range wanted {
			switch w := object.(type) {
			case *api.AgentRuntime:
				if *runtime.Spec.CellnProfileRef == *w.Spec.CellnProfileRef {
					continue
				}
				patch := client.MergeFrom(runtime.DeepCopy())
				runtime.Spec.CellnProfileRef = w.Spec.CellnProfileRef
				if err := store.Patch(ctx, runtime, patch); err != nil {
					return fmt.Errorf("rebind %s/%s: %w", runtime.Namespace, runtime.Name, err)
				}
			case *api.ModelConnection:
				var connection api.ModelConnection
				if err := store.Get(ctx, client.ObjectKeyFromObject(w), &connection); apierrors.IsNotFound(err) {
					continue
				} else if err != nil {
					return err
				}
				if connection.Labels[cellnplatform.ManagedByLabel] != cellnplatform.ManagedByValue {
					continue
				}
				patch := client.MergeFrom(connection.DeepCopy())
				connection.Spec = w.Spec
				if err := store.Patch(ctx, &connection, patch); err != nil {
					return fmt.Errorf("rebind %s/%s: %w", connection.Namespace, connection.Name, err)
				}
			}
		}
	}
	return nil
}
