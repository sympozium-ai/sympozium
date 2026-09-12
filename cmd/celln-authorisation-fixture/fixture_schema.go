package main

import (
	"os"
	"path/filepath"
)

func writeSchemas(dir string) error {
	decision := `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://sympozium.ai/schemas/celln-authorisation/decision-v1.json",
  "title": "CellnAuthorisationDecision",
  "type": "object",
  "additionalProperties": false,
  "required": ["apiVersion","kind","clusterId","run","subject","operation","lifecycle","parent","runtime","agent","policy","tools","route","budget","windows","requestDigest"],
  "properties": {
    "apiVersion": {"const":"celln.sympozium.ai/authorisation-decision-v1"},
    "kind": {"const":"CellnAuthorisationDecision"},
    "clusterId": {"$ref":"#/$defs/id"},
    "run": {"$ref":"#/$defs/run"},
    "subject": {"$ref":"#/$defs/subject"},
    "operation": {"enum":["execution.start","execution.turn","execution.read","execution.cleanup"]},
    "lifecycle": {"enum":["one-shot","enduring-initial","enduring-turn"]},
    "parent": {"oneOf":[{"type":"null"},{"$ref":"#/$defs/parent"}]},
    "runtime": {"$ref":"#/$defs/runtime"},
    "agent": {"$ref":"#/$defs/subject"},
    "policy": {"$ref":"#/$defs/policy"},
    "tools": {"type":"array","maxItems":16,"items":{"$ref":"#/$defs/tool"}},
    "route": {"$ref":"#/$defs/route"},
    "budget": {"$ref":"#/$defs/budget"},
    "windows": {"$ref":"#/$defs/windows"},
    "requestDigest": {"$ref":"#/$defs/sha256"}
  },
  "$defs": {
    "id":{"type":"string","minLength":1,"maxLength":253},
    "sha256":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"},
    "blake3":{"type":"string","pattern":"^blake3:[0-9a-f]{64}$"},
    "run":{"type":"object","additionalProperties":false,"required":["namespace","namespaceUid","name","uid","specSha256"],"properties":{"namespace":{"$ref":"#/$defs/id"},"namespaceUid":{"$ref":"#/$defs/id"},"name":{"$ref":"#/$defs/id"},"uid":{"$ref":"#/$defs/id"},"specSha256":{"$ref":"#/$defs/sha256"}}},
    "subject":{"type":"object","additionalProperties":false,"required":["kind","namespace","name","uid","specSha256"],"properties":{"kind":{"enum":["Agent","AgentRuntime","AgentRun"]},"namespace":{"$ref":"#/$defs/id"},"name":{"$ref":"#/$defs/id"},"uid":{"$ref":"#/$defs/id"},"specSha256":{"$ref":"#/$defs/sha256"}}},
    "parent":{"type":"object","additionalProperties":false,"required":["incarnation","turnId"],"properties":{"incarnation":{"$ref":"#/$defs/blake3"},"turnId":{"oneOf":[{"type":"null"},{"type":"string","minLength":1,"maxLength":128}]}}},
    "runtime":{"type":"object","additionalProperties":false,"required":["name","uid","revision","specSha256"],"properties":{"name":{"$ref":"#/$defs/id"},"uid":{"$ref":"#/$defs/id"},"revision":{"$ref":"#/$defs/id"},"specSha256":{"$ref":"#/$defs/sha256"}}},
    "policy":{"type":"object","additionalProperties":false,"required":["profile","revision","digest"],"properties":{"profile":{"$ref":"#/$defs/id"},"revision":{"$ref":"#/$defs/id"},"digest":{"$ref":"#/$defs/sha256"}}},
    "artifactLimits":{"type":"object","additionalProperties":false,"required":["operation","maxOperations","maxFiles","maxFileBytes","maxTotalBytes"],"properties":{"operation":{"enum":["read","write"]},"maxOperations":{"type":"integer","minimum":1,"maximum":64},"maxFiles":{"type":"integer","minimum":1,"maximum":256},"maxFileBytes":{"type":"integer","minimum":1,"maximum":4096},"maxTotalBytes":{"type":"integer","minimum":1,"maximum":1048576}}},
    "httpsLimits":{"type":"object","additionalProperties":false,"required":["allowHosts","maxRequests","maxResponseBytes","timeoutMillis"],"properties":{"allowHosts":{"type":"array","minItems":1,"maxItems":16,"items":{"type":"string","minLength":1,"maxLength":253}},"maxRequests":{"type":"integer","minimum":1,"maximum":16},"maxResponseBytes":{"type":"integer","minimum":1,"maximum":4096},"timeoutMillis":{"type":"integer","minimum":1,"maximum":30000}}},
    "limits":{"type":"object","additionalProperties":false,"required":["timeoutMillis","memoryBytes","argumentBytes","outputBytes","workspace","effects","artifacts","https"],"properties":{"timeoutMillis":{"type":"integer","minimum":1,"maximum":300000},"memoryBytes":{"type":"integer","minimum":1,"maximum":268435456},"argumentBytes":{"type":"integer","minimum":1,"maximum":65536},"outputBytes":{"type":"integer","minimum":1,"maximum":65536},"workspace":{"const":"none"},"effects":{"enum":["none","external-side-effects"]},"artifacts":{"oneOf":[{"type":"null"},{"$ref":"#/$defs/artifactLimits"}]},"https":{"oneOf":[{"type":"null"},{"$ref":"#/$defs/httpsLimits"}]}}},
    "tool":{"type":"object","additionalProperties":false,"required":["name","revision","hash","limits"],"properties":{"name":{"$ref":"#/$defs/id"},"revision":{"$ref":"#/$defs/id"},"hash":{"$ref":"#/$defs/blake3"},"limits":{"$ref":"#/$defs/limits"}}},
    "credentialSource":{"type":"object","additionalProperties":false,"required":["kind","secretUid","secretName","secretKey"],"properties":{"kind":{"const":"Secret"},"secretUid":{"$ref":"#/$defs/id"},"secretName":{"$ref":"#/$defs/id"},"secretKey":{"$ref":"#/$defs/id"}}},
    "route":{"type":"object","additionalProperties":false,"required":["modelConnectionUid","modelConnectionSpecSha256","provider","protocol","model","endpointOrigin","auth","streaming","credentialSource"],"properties":{"modelConnectionUid":{"oneOf":[{"type":"null"},{"$ref":"#/$defs/id"}]},"modelConnectionSpecSha256":{"type":"string","maxLength":71},"provider":{"type":"string","minLength":1,"maxLength":64},"protocol":{"enum":["openai-chat","anthropic-messages","none"]},"model":{"type":"string","maxLength":128},"endpointOrigin":{"type":"string","maxLength":2048},"auth":{"enum":["secret","none"]},"streaming":{"const":false},"credentialSource":{"oneOf":[{"type":"null"},{"$ref":"#/$defs/credentialSource"}]}}},
    "cap":{"type":"object","additionalProperties":false,"required":["requests","outputTokens"],"properties":{"requests":{"type":"integer","minimum":0,"maximum":6144},"outputTokens":{"type":"integer","minimum":0,"maximum":3145728}}},
    "budget":{"type":"object","additionalProperties":false,"required":["budgetId","runCap","turnCap","maxTurns","parentDeadlineUnix","turnDeadlineUnix"],"properties":{"budgetId":{"$ref":"#/$defs/sha256"},"runCap":{"$ref":"#/$defs/cap"},"turnCap":{"$ref":"#/$defs/cap"},"maxTurns":{"type":"integer","minimum":1,"maximum":1024},"parentDeadlineUnix":{"type":"integer","minimum":0},"turnDeadlineUnix":{"type":"integer","minimum":1}}},
    "windows":{"type":"object","additionalProperties":false,"required":["issuedAt","notBefore","admissionDeadline"],"properties":{"issuedAt":{"type":"integer","minimum":1},"notBefore":{"type":"integer","minimum":1},"admissionDeadline":{"type":"integer","minimum":1}}}
  }
}` + "\n"
	credential := `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://sympozium.ai/schemas/celln-authorisation/credential-v1.json",
  "title": "CellnAuthorisationCredentialClaims",
  "type": "object",
  "additionalProperties": false,
  "required": ["apiVersion","iss","aud","iat","nbf","exp","jti","decisionDigest","budgetId","operation","subject"],
  "properties": {
    "apiVersion":{"const":"celln.sympozium.ai/authorisation-credential-v1"},
    "iss":{"const":"sympozium-control-plane"},
    "aud":{"enum":["celln-execution","sympozium-model-gateway"]},
    "iat":{"type":"integer","minimum":1},"nbf":{"type":"integer","minimum":1},"exp":{"type":"integer","minimum":1},
    "jti":{"type":"string","minLength":16,"maxLength":128},
    "decisionDigest":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"},
    "budgetId":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"},
    "operation":{"enum":["execution.start","execution.turn","execution.read","execution.cleanup","model.invoke"]},
    "subject":{"type":"object","additionalProperties":false,"required":["runUid","turnId","parentIncarnation"],"properties":{"runUid":{"type":"string","minLength":1},"turnId":{"oneOf":[{"type":"null"},{"type":"string","minLength":1}]},"parentIncarnation":{"oneOf":[{"type":"null"},{"type":"string","pattern":"^blake3:[0-9a-f]{64}$"}]}}}
  }
}` + "\n"
	if err := os.MkdirAll(filepath.Join(dir, "schema"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "schema", "decision.schema.json"), []byte(decision), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "schema", "credential.schema.json"), []byte(credential), 0o644)
}
