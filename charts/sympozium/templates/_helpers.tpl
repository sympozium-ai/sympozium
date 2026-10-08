{{/*
Expand the name of the chart.
*/}}
{{- define "sympozium.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "sympozium.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label helper.
*/}}
{{- define "sympozium.labels" -}}
helm.sh/chart: {{ include "sympozium.chart" . }}
{{ include "sympozium.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: sympozium
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "sympozium.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sympozium.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Chart name and version.
*/}}
{{- define "sympozium.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Image tag helper — defaults to Chart.AppVersion.
*/}}
{{- define "sympozium.imageTag" -}}
{{- .Values.image.tag | default (printf "v%s" .Chart.AppVersion) }}
{{- end }}

{{/*
Controller image.
*/}}
{{- define "sympozium.controllerImage" -}}
{{- $repo := .Values.controller.image.repository | default (printf "%s/controller" .Values.image.registry) }}
{{- $tag := .Values.controller.image.tag | default (include "sympozium.imageTag" .) }}
{{- printf "%s:%s" $repo $tag }}
{{- end }}

{{/*
API server image.
*/}}
{{- define "sympozium.apiserverImage" -}}
{{- $repo := .Values.apiserver.image.repository | default (printf "%s/apiserver" .Values.image.registry) }}
{{- $tag := .Values.apiserver.image.tag | default (include "sympozium.imageTag" .) }}
{{- printf "%s:%s" $repo $tag }}
{{- end }}

{{/*
Webhook image.
*/}}
{{- define "sympozium.webhookImage" -}}
{{- $repo := .Values.webhook.image.repository | default (printf "%s/webhook" .Values.image.registry) }}
{{- $tag := .Values.webhook.image.tag | default (include "sympozium.imageTag" .) }}
{{- printf "%s:%s" $repo $tag }}
{{- end }}

{{/*
Web proxy image.
*/}}
{{- define "sympozium.webProxyImage" -}}
{{- $repo := .Values.webProxy.image.repository | default (printf "%s/web-proxy" .Values.image.registry) }}
{{- $tag := .Values.webProxy.image.tag | default (include "sympozium.imageTag" .) }}
{{- printf "%s:%s" $repo $tag }}
{{- end }}

{{/*
Node probe image.
*/}}
{{- define "sympozium.nodeProbeImage" -}}
{{- $repo := .Values.nodeProbe.image.repository | default (printf "%s/node-probe" .Values.image.registry) }}
{{- $tag := .Values.nodeProbe.image.tag | default (include "sympozium.imageTag" .) }}
{{- printf "%s:%s" $repo $tag }}
{{- end }}

{{/*
llmfit daemon image.
*/}}
{{- define "sympozium.llmfitDaemonImage" -}}
{{- $repo := .Values.llmfit.daemonset.image.repository | default (printf "%s/llmfit-daemon" .Values.image.registry) }}
{{- $tag := .Values.llmfit.daemonset.image.tag | default (include "sympozium.imageTag" .) }}
{{- printf "%s:%s" $repo $tag }}
{{- end }}

{{/*
NATS URL — internal or external.
*/}}
{{- define "sympozium.natsUrl" -}}
{{- if .Values.nats.enabled }}
{{- printf "nats://nats.%s.svc:4222" .Values.namespace }}
{{- else }}
{{- .Values.nats.externalUrl }}
{{- end }}
{{- end }}

{{/* Name of credentials Secret for bundled NATS. */}}
{{- define "sympozium.natsAuthSecret" -}}
{{- if .Values.nats.auth.existingSecret }}
{{- .Values.nats.auth.existingSecret }}
{{- else }}
{{- printf "%s-nats-auth" (include "sympozium.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Namespace helper.
*/}}
{{- define "sympozium.namespace" -}}
{{- .Values.namespace | default "sympozium-system" }}
{{- end }}

{{/*
OTel headers: convert map to comma-separated "key=value" pairs.
*/}}
{{- define "sympozium.otelHeaders" -}}
{{- $pairs := list -}}
{{- range $k, $v := .Values.observability.headers -}}
{{- $pairs = append $pairs (printf "%s=%s" $k $v) -}}
{{- end -}}
{{- join "," $pairs -}}
{{- end }}

{{/*
OTel resource attributes: convert map to comma-separated "key=value" pairs.
*/}}
{{- define "sympozium.otelResourceAttrs" -}}
{{- $pairs := list -}}
{{- range $k, $v := .Values.observability.resourceAttributes -}}
{{- $pairs = append $pairs (printf "%s=%s" $k $v) -}}
{{- end -}}
{{- join "," $pairs -}}
{{- end }}

{{/*
Mediated Celln model access (celln.mediation). Renders "true" when enabled and
refuses any incomplete or contradictory input, so the controller, the fleet
dispatchers and the model gateway are never rendered half-wired. Every
template that wires one of the three includes this.
*/}}
{{- define "sympozium.cellnMediation" -}}
{{- $m := .Values.celln.mediation | default dict -}}
{{- if not (kindIs "bool" (ternary $m.defaultRoutes true (hasKey $m "defaultRoutes"))) -}}
{{- fail "celln.mediation.defaultRoutes must be true or false" -}}
{{- end -}}
{{- if and (not $m.enabled) (or $m.mediateBackends $m.routes) -}}
{{- fail "celln.mediation.routes and celln.mediation.mediateBackends require celln.mediation.enabled: without mediation an Agent's own key is never used, so the declared routes would admit nothing; enable mediation or remove them" -}}
{{- end -}}
{{- if $m.enabled -}}
{{- if not (kindIs "bool" ($m.mediateBackends | default false)) -}}
{{- fail "celln.mediation.mediateBackends must be true or false" -}}
{{- end -}}
{{- $routes := $m.routes | default list -}}
{{- if or (not (kindIs "slice" $routes)) (gt (len $routes) 32) -}}
{{- fail "celln.mediation.routes must be a list of at most 32 routes: one execution policy carries no more" -}}
{{- end -}}
{{- range $i, $route := $routes -}}
{{- if not (kindIs "map" $route) -}}
{{- fail (printf "celln.mediation.routes[%d] must be a route: provider, protocol, models, endpointOrigins" $i) -}}
{{- end -}}
{{- range $key, $_ := $route -}}
{{- if not (has $key (list "provider" "protocol" "models" "endpointOrigins" "auth" "allowInsecure")) -}}
{{- fail (printf "celln.mediation.routes[%d].%s is not a route field (provider, protocol, models, endpointOrigins)" $i $key) -}}
{{- end -}}
{{- end -}}
{{- if not (regexMatch "^[a-zA-Z0-9_-]{1,64}$" (toString ($route.provider | default ""))) -}}
{{- fail (printf "celln.mediation.routes[%d].provider must be 1-64 letters, digits, underscores or hyphens" $i) -}}
{{- end -}}
{{- if not (has ($route.protocol | default "") (list "openai-chat" "anthropic-messages")) -}}
{{- fail (printf "celln.mediation.routes[%d].protocol must be openai-chat or anthropic-messages" $i) -}}
{{- end -}}
{{- $models := $route.models | default list -}}
{{- if or (not (kindIs "slice" $models)) (lt (len $models) 1) (gt (len $models) 32) -}}
{{- fail (printf "celln.mediation.routes[%d].models must list 1-32 exact model names, or exactly [\"*\"] for any model: a route without a model admits nothing" $i) -}}
{{- end -}}
{{- if not (and (eq (len $models) 1) (eq (toString (index $models 0)) "*")) -}}
{{- range $model := $models -}}
{{- if not (regexMatch "^[^*[:space:]]([^*\\r\\n]{0,126}[^*[:space:]])?$" (toString $model)) -}}
{{- fail (printf "celln.mediation.routes[%d].models: %q must be an exact model name of at most 128 bytes; the only pattern is a lone \"*\" (any model), never beside a name or inside one" $i (toString $model)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if ne (len ($models | uniq)) (len $models) -}}
{{- fail (printf "celln.mediation.routes[%d].models must not repeat a model" $i) -}}
{{- end -}}
{{- $origins := $route.endpointOrigins | default list -}}
{{- $auth := $route.auth | default "secret" -}}
{{- if not (has $auth (list "secret" "none")) -}}
{{- fail (printf "celln.mediation.routes[%d].auth must be secret or none" $i) -}}
{{- end -}}
{{- if and (hasKey $route "allowInsecure") (not (kindIs "bool" $route.allowInsecure)) -}}
{{- fail (printf "celln.mediation.routes[%d].allowInsecure must be a boolean" $i) -}}
{{- end -}}
{{- $insecure := $route.allowInsecure | default false -}}
{{- if and $insecure (ne $auth "none") -}}
{{- fail (printf "celln.mediation.routes[%d].allowInsecure requires auth none; a Secret never crosses plain HTTP" $i) -}}
{{- end -}}
{{- if or (not (kindIs "slice" $origins)) (lt (len $origins) 1) (gt (len $origins) 16) -}}
{{- fail (printf "celln.mediation.routes[%d].endpointOrigins must list 1-16 HTTPS origins" $i) -}}
{{- end -}}
{{- range $origin := $origins -}}
{{- $pattern := "^https://[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$" -}}
{{- if $insecure -}}
{{- $pattern = "^https?://([A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?|\\[[0-9A-Fa-f:]+\\])(:[0-9]{1,5})?$" -}}
{{- end -}}
{{- if not (regexMatch $pattern (toString $origin)) -}}
{{- fail (printf "celln.mediation.routes[%d].endpointOrigins: %q must be an https://host origin without port, path or credentials; a Secret never crosses plain HTTP" $i (toString $origin)) -}}
{{- end -}}
{{- if and $insecure (not (has $origin ($.Values.modelGateway.privateOrigins | default list))) -}}
{{- fail (printf "celln.mediation.routes[%d]: an insecure keyless origin must also be listed in modelGateway.privateOrigins" $i) -}}
{{- end -}}
{{- end -}}
{{- if ne (len ($origins | uniq)) (len $origins) -}}
{{- fail (printf "celln.mediation.routes[%d].endpointOrigins must not repeat an origin" $i) -}}
{{- end -}}
{{- end -}}
{{- $fleet := .Values.celln.fleet | default dict -}}
{{- if not (and .Values.celln.enabled $fleet.enabled) -}}
{{- fail "celln.mediation requires celln.enabled and celln.fleet.enabled: the scoped receiver runs in the fleet's celln-node dispatchers" -}}
{{- end -}}
{{- if and .Values.celln.dispatcher .Values.celln.dispatcher.enduring .Values.celln.dispatcher.enduring.enabled -}}
{{- fail "celln.mediation cannot combine with celln.dispatcher.enduring" -}}
{{- end -}}
{{- if not (regexMatch "^[^[:space:]]{1,253}$" ($m.clusterId | default "")) -}}
{{- fail "celln.mediation.clusterId is required: the operator-chosen cluster identity bound into every decision" -}}
{{- end -}}
{{- $issuer := $m.issuer | default dict -}}
{{- if ne ($issuer.name | default "") "sympozium-control-plane" -}}
{{- fail "celln.mediation.issuer.name must stay sympozium-control-plane: the credential contract fixes the issuer" -}}
{{- end -}}
{{- if not (regexMatch "^[^[:space:]]{1,128}$" ($issuer.keyId | default "")) -}}
{{- fail "celln.mediation.issuer.keyId is required: the `kid` of the bootstrapped signing key in the trust JWKS" -}}
{{- end -}}
{{- range $key := list "controllerSecret" "gatewaySecret" "nodeSecret" "trustConfigMap" -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$" (get $m $key | default "")) -}}
{{- fail (printf "celln.mediation.%s must name the operator-provided object created by `sympozium celln-mediation bootstrap`" $key) -}}
{{- end -}}
{{- end -}}
{{- $receiver := $m.receiver | default dict -}}
{{- if and $receiver.url (not (regexMatch "^https://[^/?#@[:space:]]+$" $receiver.url)) -}}
{{- fail "celln.mediation.receiver.url must be an origin-only HTTPS URL (https://host[:port])" -}}
{{- end -}}
{{- $port := int ($receiver.port | default 9443) -}}
{{- if or (lt $port 1024) (gt $port 65535) (eq $port 8787) -}}
{{- fail "celln.mediation.receiver.port must be an unprivileged port other than the dispatcher's 8787" -}}
{{- end -}}
{{- if .Values.modelGateway.configurationClaim -}}
{{- fail "celln.mediation deploys the model gateway from Secret/ConfigMap volumes; unset modelGateway.configurationClaim" -}}
{{- end -}}
{{- $database := .Values.modelGateway.database | default dict -}}
{{- if and $database.secretName (not $database.key) -}}
{{- fail "modelGateway.database.key must name the Secret key holding the PostgreSQL URL" -}}
{{- end -}}
{{- if not $database.secretName -}}
{{- $bundled := $database.bundled | default dict -}}
{{- if not (regexMatch "^.+@sha256:[0-9a-f]{64}$" ($bundled.image | default "")) -}}
{{- fail "modelGateway.database.bundled.image must be pinned by sha256 digest (or set modelGateway.database.secretName to use your own PostgreSQL)" -}}
{{- end -}}
{{- end -}}
true
{{- end -}}
{{- end -}}

{{/*
"true" when celln.mediation runs the chart's own PostgreSQL: mediation is on
and no operator database Secret is named.
*/}}
{{- define "sympozium.modelGatewayBundledDatabase" -}}
{{- if and (include "sympozium.cellnMediation" .) (not ((.Values.modelGateway.database | default dict).secretName)) -}}
true
{{- end -}}
{{- end -}}

{{/* Name shared by the bundled PostgreSQL's StatefulSet, Service and Secret. */}}
{{- define "sympozium.modelGatewayDatabaseName" -}}
{{- printf "%s-model-gateway-db" (include "sympozium.fullname" .) -}}
{{- end -}}

{{/* HTTPS origin of the chart's model gateway Service. */}}
{{- define "sympozium.modelGatewayOrigin" -}}
{{- printf "https://%s-model-gateway.%s.svc:8443" (include "sympozium.fullname" .) (include "sympozium.namespace" .) -}}
{{- end -}}

{{/* HTTPS origin of the one scoped receiver the controller dispatches to. */}}
{{- define "sympozium.cellnScopedReceiverOrigin" -}}
{{- $receiver := (.Values.celln.mediation | default dict).receiver | default dict -}}
{{- $receiver.url | default (printf "https://celln-scoped-receiver.celln-system.svc:%d" (int ($receiver.port | default 9443))) -}}
{{- end -}}

{{/*
Shell lines for the celln-node dispatcher wrapper: pass the enduring parent
request only when the node has one. Celln refuses to start on a missing file,
and without the flag it serves one-shot scoped dispatch only.
*/}}
{{- define "sympozium.cellnScopedParentRequestArg" -}}
if [ -s "$SCOPED_PARENT_REQUEST" ]; then
  set -- "$@" --scoped-parent-request-file "$SCOPED_PARENT_REQUEST"
else
  echo "celln scoped receiver: no parent request at $SCOPED_PARENT_REQUEST; enduring scoped runs stay disabled (one-shot only) until it exists and this pod restarts" >&2
fi
{{- end -}}

{{/*
The mediated routes recorded when celln.mediation is enabled, the operator
declares no routes and celln.mediation.defaultRoutes is true (the default):
any model ("*") of the well-known hosted providers at their exact public API
origins, each with the Agent's own key. Mirrors cellninstall.DefaultMediatedRoutes
(TestDefaultMediatedRoutesMatchChart keeps them equal). Renders a JSON list.
*/}}
{{- define "sympozium.cellnDefaultMediatedRoutes" -}}
{{- toJson (list
  (dict "provider" "openai" "protocol" "openai-chat" "models" (list "*") "endpointOrigins" (list "https://api.openai.com"))
  (dict "provider" "anthropic" "protocol" "anthropic-messages" "models" (list "*") "endpointOrigins" (list "https://api.anthropic.com"))
  (dict "provider" "deepseek" "protocol" "openai-chat" "models" (list "*") "endpointOrigins" (list "https://api.deepseek.com"))
) -}}
{{- end }}

{{/*
sympozium.mediationTrustChecksum: changes whenever mediation trust is rotated
- a new signing key id, or the trust Secrets/ConfigMap recreated (new UIDs) -
so the controller and gateway, which copy their trust once at start, roll.
lookup is empty under `helm template`; the key id still applies.
*/}}
{{- define "sympozium.mediationTrustChecksum" -}}
{{- $m := .Values.celln.mediation | default dict -}}
{{- $ns := include "sympozium.namespace" . -}}
{{- $ids := list ($m.issuer | default dict).keyId -}}
{{- range $secret := list $m.controllerSecret $m.gatewaySecret -}}
{{- if $secret -}}
{{- $ids = append $ids ((lookup "v1" "Secret" $ns $secret).metadata | default dict).uid -}}
{{- end -}}
{{- end -}}
{{- if $m.trustConfigMap -}}
{{- $ids = append $ids ((lookup "v1" "ConfigMap" $ns $m.trustConfigMap).metadata | default dict).uid -}}
{{- end -}}
{{- $ids | toJson | sha256sum -}}
{{- end -}}
