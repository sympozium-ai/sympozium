package webhook

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// SkillPackValidator is a validating webhook that checks native sidecar tool
// declarations at SkillPack create/update time, so authors get immediate
// feedback at `kubectl apply` instead of discovering errors later via some
// AgentRun. Cross-pack uniqueness is intentionally NOT checked here — it depends
// on which SkillPacks are attached together and is enforced by the AgentRun
// policy webhook.
type SkillPackValidator struct {
	Log     logr.Logger
	Decoder admission.Decoder
}

// Handle validates a SkillPack's sidecar tools.
func (sv *SkillPackValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	sp := &sympoziumv1alpha1.SkillPack{}
	if err := sv.Decoder.Decode(req, sp); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	if sp.Spec.Sidecar == nil || len(sp.Spec.Sidecar.Tools) == 0 {
		return admission.Allowed("no sidecar tools to validate")
	}

	seen := make(map[string]struct{}, len(sp.Spec.Sidecar.Tools))
	for _, tool := range sp.Spec.Sidecar.Tools {
		if err := validateSidecarToolStructural(sp.Name, tool); err != nil {
			return admission.Denied(err.Error())
		}
		// Within-pack uniqueness (cross-pack is checked at AgentRun admission).
		if _, dup := seen[tool.Name]; dup {
			return admission.Denied(fmt.Sprintf("SkillPack %q declares sidecar tool %q more than once", sp.Name, tool.Name))
		}
		seen[tool.Name] = struct{}{}
	}

	return admission.Allowed("sidecar tools validated")
}
