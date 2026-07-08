// Package dra detects, at runtime, whether the cluster can do claim-based
// model placement: Kubernetes DRA (resource.k8s.io/v1, GA in 1.34) plus the
// llmfit.ai ModelClaim CRD served by llmfit-dra. Detection is runtime-only —
// never compile-time — so sympozium keeps working unchanged on clusters
// without either (docs/positioning.md: models are claimed, not placed).
package dra

import (
	"sync"
	"time"

	"k8s.io/client-go/discovery"
)

const recheckInterval = 5 * time.Minute

// Detector caches whether claim-based placement is available. A negative
// answer is re-checked on an interval (the driver may be installed after
// sympozium); a positive answer is sticky for the process lifetime — CRDs
// being deleted mid-flight is a cluster teardown, not a mode change.
type Detector struct {
	disco discovery.DiscoveryInterface

	mu        sync.Mutex
	available bool
	checkedAt time.Time
}

func NewDetector(disco discovery.DiscoveryInterface) *Detector {
	return &Detector{disco: disco}
}

// Available reports whether ModelClaim placement can be used right now.
func (d *Detector) Available() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.available {
		return true
	}
	if time.Since(d.checkedAt) < recheckInterval {
		return false
	}
	d.checkedAt = time.Now()
	d.available = d.probe()
	return d.available
}

func (d *Detector) probe() bool {
	if d.disco == nil {
		return false
	}
	if !d.serves("resource.k8s.io/v1", "resourceclaimtemplates") {
		return false
	}
	return d.serves("llmfit.ai/v1alpha1", "modelclaims")
}

func (d *Detector) serves(groupVersion, resource string) bool {
	list, err := d.disco.ServerResourcesForGroupVersion(groupVersion)
	if err != nil || list == nil {
		return false
	}
	for _, r := range list.APIResources {
		if r.Name == resource {
			return true
		}
	}
	return false
}

// Static is a fixed-answer Detector for tests.
func Static(available bool) *Detector {
	d := &Detector{available: available, checkedAt: time.Now().Add(24 * time.Hour)}
	return d
}
