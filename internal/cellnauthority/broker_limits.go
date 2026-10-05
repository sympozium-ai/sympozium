package cellnauthority

import (
	"fmt"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// ArtifactOperations are the run-data operations a brokered tool may declare;
// ArtifactWrites are those that change run data and so need declared effects.
var (
	ArtifactOperations = map[string]bool{"read": true, "write": true, "list": true, "append": true, "search": true, "delete": true}
	ArtifactWrites     = map[string]bool{"write": true, "append": true, "delete": true}
)

// AnyHost, as the only HTTPS host, lets a web tool reach any public HTTPS
// site. Celln's broker still refuses private, loopback, link-local and
// reserved addresses, plain HTTP and redirects to them.
const AnyHost = "*"

// AnyPublicHost reports a host list that is exactly [AnyHost].
func AnyPublicHost(hosts []string) bool {
	return len(hosts) == 1 && hosts[0] == AnyHost
}

func validateBrokerLimits(l api.CellnToolLimits) error {
	if l.Artifacts != nil && l.HTTPS != nil {
		return fmt.Errorf("one broker capability per starter tool required")
	}
	if a := l.Artifacts; a != nil {
		if !ArtifactOperations[a.Operation] || a.MaxOperations < 1 || a.MaxOperations > 64 || a.MaxFiles < 1 || a.MaxFiles > 256 || a.MaxFileBytes < 1 || a.MaxFileBytes > 4096 || a.MaxTotalBytes < a.MaxFileBytes || a.MaxTotalBytes > 1048576 || (ArtifactWrites[a.Operation] && l.Effects != "external-side-effects") {
			return fmt.Errorf("invalid run-artifact broker limits")
		}
	}
	if h := l.HTTPS; h != nil {
		if len(h.AllowHosts) < 1 || len(h.AllowHosts) > 16 || h.MaxRequests < 1 || h.MaxRequests > 16 || h.MaxResponseBytes < 1 || h.MaxResponseBytes > 4096 || h.TimeoutMillis < 1 || h.TimeoutMillis > 30000 || h.TimeoutMillis > l.TimeoutMillis || l.Effects != "external-side-effects" {
			return fmt.Errorf("invalid HTTPS broker limits")
		}
		if AnyPublicHost(h.AllowHosts) {
			return nil
		}
		seen := map[string]bool{}
		for _, host := range h.AllowHosts {
			if host == AnyHost {
				return fmt.Errorf("%q must be the only HTTPS host", AnyHost)
			}
			if len(host) > 253 || !strings.Contains(host, ".") || seen[host] {
				return fmt.Errorf("invalid or duplicate HTTPS host")
			}
			seen[host] = true
			for _, label := range strings.Split(host, ".") {
				if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
					return fmt.Errorf("invalid HTTPS DNS label")
				}
				for _, c := range label {
					if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
						return fmt.Errorf("HTTPS host must be a lowercase DNS name")
					}
				}
			}
		}
	}
	return nil
}

// Each layer must explicitly grant the same operation. Copies prevent the
// intersection from mutating catalogue metadata or another caller's grants.
func intersectBrokerLimits(l *api.CellnToolLimits, grant api.CellnToolLimits) error {
	if (l.Artifacts == nil) != (grant.Artifacts == nil) || (l.HTTPS == nil) != (grant.HTTPS == nil) {
		return fmt.Errorf("broker capability absent or different in grant")
	}
	if a := l.Artifacts; a != nil {
		g := grant.Artifacts
		if a.Operation != g.Operation {
			return fmt.Errorf("artifact operation differs from grant")
		}
		copy := *a
		copy.MaxOperations = min(a.MaxOperations, g.MaxOperations)
		copy.MaxFiles = min(a.MaxFiles, g.MaxFiles)
		copy.MaxFileBytes = min(a.MaxFileBytes, g.MaxFileBytes)
		copy.MaxTotalBytes = min(a.MaxTotalBytes, g.MaxTotalBytes)
		copy.MaxFileBytes = min(copy.MaxFileBytes, copy.MaxTotalBytes)
		l.Artifacts = &copy
	}
	if h := l.HTTPS; h != nil {
		g := grant.HTTPS
		copy := *h
		copy.AllowHosts = nil
		switch {
		case AnyPublicHost(g.AllowHosts):
			// Any public host keeps whatever this layer allows.
			copy.AllowHosts = append(copy.AllowHosts, h.AllowHosts...)
		case AnyPublicHost(h.AllowHosts):
			// An explicit grant narrows any public host to its own list.
			copy.AllowHosts = append(copy.AllowHosts, g.AllowHosts...)
		default:
			for _, host := range h.AllowHosts {
				for _, allowed := range g.AllowHosts {
					if host == allowed {
						copy.AllowHosts = append(copy.AllowHosts, host)
						break
					}
				}
			}
		}
		if len(copy.AllowHosts) == 0 {
			return fmt.Errorf("HTTPS grant intersection has no destinations")
		}
		copy.MaxRequests = min(h.MaxRequests, g.MaxRequests)
		copy.MaxResponseBytes = min(h.MaxResponseBytes, g.MaxResponseBytes)
		copy.TimeoutMillis = min(h.TimeoutMillis, g.TimeoutMillis, l.TimeoutMillis)
		l.HTTPS = &copy
	}
	return nil
}
