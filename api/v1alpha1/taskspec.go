package v1alpha1

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// TaskSpec is the polymorphic task description for an AgentRun. It accepts
// either a string (legacy Path A: the prompt passed to the LLM via the TASK
// env var) or an object describing an orchestration mode (e.g. sidecar-driven).
//
// On unmarshal:
//   - String JSON ("do the thing") → IsString()=true, GetPrompt() returns the
//     prompt. All object fields are zero.
//   - Object JSON ({mode, tool, parameters}) → IsObject()=true. The Mode field
//     is required by the handler (controller / handler validation), not by the
//     CRD schema (intentionally loose).
//
// On marshal:
//   - String form round-trips to a JSON string.
//   - Object form round-trips to a JSON object with Prompt omitted.
type TaskSpec struct {
	// Prompt is set only on the string form (Path A). The conversational
	// prompt passed to the LLM via the TASK env var.
	Prompt string `json:"-"`

	// Mode is the orchestration mode identifier for object form. The
	// controller looks up the matching TaskModeHandler in its registry.
	// Examples: "sidecar-driven".
	Mode string `json:"mode,omitempty"`

	// Tool is the SkillPack tool name to invoke on the resolved sidecar.
	// Semantics are mode-specific (the handler decides). For sidecar-driven
	// mode this is required and identifies the sidecar's primary tool.
	Tool string `json:"tool,omitempty"`

	// Parameters is the open shape of arguments to pass to the orchestrator.
	// Each handler decides how to interpret them. For sidecar-driven mode
	// the controller serialises them to JSON and sets the
	// SYMPOZIUM_RUN_CONFIG_JSON env var on the resolved sidecar.
	Parameters map[string]string `json:"parameters,omitempty"`

	// isString records which JSON shape was last unmarshalled so MarshalJSON
	// round-trips. Unexported because callers should use IsString()/IsObject().
	isString bool `json:"-"`
}

// IsString reports whether the TaskSpec was unmarshalled from a JSON string
// (Path A). True means the prompt goes to the LLM via TASK; no orchestration
// dispatch is performed.
func (t *TaskSpec) IsString() bool {
	if t == nil {
		return false
	}
	return t.isString
}

// IsObject reports whether the TaskSpec was unmarshalled from a JSON object
// with an orchestration mode (Path B). True means the controller will
// dispatch by Mode to a registered TaskModeHandler.
func (t *TaskSpec) IsObject() bool {
	if t == nil {
		return false
	}
	return !t.isString && (t.Mode != "" || t.Tool != "" || t.Parameters != nil)
}

// GetMode returns the orchestration mode, or "" for string form.
func (t *TaskSpec) GetMode() string {
	if t == nil {
		return ""
	}
	return t.Mode
}

// GetPrompt returns the legacy Path A prompt string, or "" for object form.
func (t *TaskSpec) GetPrompt() string {
	if t == nil {
		return ""
	}
	return t.Prompt
}

// UnmarshalJSON accepts either a JSON string or a JSON object. On success,
// t.isString is set to indicate which form was received so MarshalJSON can
// round-trip.
//
// Object form requires the controller/handler to validate Mode presence; the
// CRD schema marks Mode as required but is intentionally loose so handlers
// can extend the object shape per-mode without CRD changes.
func (t *TaskSpec) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("TaskSpec: UnmarshalJSON on nil pointer")
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var raw interface{}
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("TaskSpec: decode: %w", err)
	}

	switch v := raw.(type) {
	case string:
		t.Prompt = v
		t.Mode = ""
		t.Tool = ""
		t.Parameters = nil
		t.isString = true
		return nil

	case map[string]interface{}:
		// Reset string-form state in case this TaskSpec is being reused.
		t.Prompt = ""
		t.isString = false

		if mode, ok := v["mode"].(string); ok {
			t.Mode = mode
		} else {
			t.Mode = ""
		}
		if tool, ok := v["tool"].(string); ok {
			t.Tool = tool
		} else {
			t.Tool = ""
		}
		if rawParams, ok := v["parameters"].(map[string]interface{}); ok {
			t.Parameters = make(map[string]string, len(rawParams))
			for k, val := range rawParams {
				s, ok := val.(string)
				if !ok {
					return fmt.Errorf("TaskSpec: parameters.%s must be a string, got %T", k, val)
				}
				t.Parameters[k] = s
			}
		} else if v["parameters"] != nil {
			return fmt.Errorf("TaskSpec: parameters must be an object of string→string, got %T", v["parameters"])
		} else {
			t.Parameters = nil
		}
		// Other fields (e.g. sidecar) are intentionally ignored at this
		// layer. Sidecar resolution is owned by the per-mode handler.
		return nil

	default:
		return fmt.Errorf("TaskSpec: must be a string or object, got %T", raw)
	}
}

// MarshalJSON emits the JSON form that round-trips with UnmarshalJSON.
// String form is emitted as a plain JSON string. Object form is emitted
// as an object with Prompt omitted.
func (t *TaskSpec) MarshalJSON() ([]byte, error) {
	if t == nil {
		return []byte("null"), nil
	}
	if t.isString {
		return json.Marshal(t.Prompt)
	}
	// Object form: only emit non-zero fields. Prompt is intentionally omitted.
	obj := map[string]interface{}{}
	if t.Mode != "" {
		obj["mode"] = t.Mode
	}
	if t.Tool != "" {
		obj["tool"] = t.Tool
	}
	if len(t.Parameters) > 0 {
		obj["parameters"] = t.Parameters
	}
	return json.Marshal(obj)
}

// NewStringTask returns a *TaskSpec wrapping a Path A prompt. Use this when
// constructing a TaskSpec programmatically from a legacy string-form input
// (e.g. the orchestrator's sub-agent spawn, which always passes the LLM
// prompt as a string). Returns nil for an empty prompt so the field stays
// omitted from the CRD (which is the historical behaviour for runs that
// didn't set a task string).
func NewStringTask(prompt string) *TaskSpec {
	if prompt == "" {
		return nil
	}
	return &TaskSpec{
		Prompt:   prompt,
		isString: true,
	}
}
