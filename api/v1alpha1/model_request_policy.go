package v1alpha1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Model request policy is what an operator pins onto every provider request of
// one model route: a bounded JSON object of extra request fields ("model
// parameters") and the output tokens one request may produce. The rules below
// are the one implementation, shared by the fleet backends
// (internal/cellninstall) and the namespaced ModelConnection the model gateway
// enforces. The guest never sees or changes either setting.

const (
	maxModelParameterKeys   = 16
	maxModelParameterDepth  = 3
	maxModelParameterString = 256
	maxModelParameterArray  = 8
	// MaxModelParametersBytes bounds the serialized object.
	MaxModelParametersBytes = 2048
)

var modelParameterKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ReservedModelParameters are the request fields the host owns; a parameter
// may not set them.
var ReservedModelParameters = []string{"model", "messages", "system", "stream", "stream_options", "max_tokens", "max_completion_tokens", "n", "tools", "tool_choice", "functions", "function_call", "parallel_tool_calls", "user"}

// ValidateModelParameters applies the rules for model parameters. A nil or
// empty object is valid and means no parameters.
func ValidateModelParameters(parameters map[string]any) error {
	if len(parameters) == 0 {
		return nil
	}
	if len(parameters) > maxModelParameterKeys {
		return fmt.Errorf("model parameters: at most %d top-level keys, got %d", maxModelParameterKeys, len(parameters))
	}
	for _, key := range sortedParameterKeys(parameters) {
		for _, reserved := range ReservedModelParameters {
			if key == reserved {
				return fmt.Errorf("model parameters: %q is reserved (Celln sets it on every request)", key)
			}
		}
	}
	if err := validateModelParameterObject(parameters, "", 1); err != nil {
		return err
	}
	raw, err := json.Marshal(parameters)
	if err != nil {
		return fmt.Errorf("model parameters: %w", err)
	}
	if len(raw) > MaxModelParametersBytes {
		return fmt.Errorf("model parameters: serialized size %d bytes exceeds %d", len(raw), MaxModelParametersBytes)
	}
	return nil
}

func validateModelParameterObject(object map[string]any, path string, depth int) error {
	if depth > maxModelParameterDepth {
		return fmt.Errorf("model parameters: %s nests deeper than %d levels", path, maxModelParameterDepth)
	}
	for _, key := range sortedParameterKeys(object) {
		at := key
		if path != "" {
			at = path + "." + key
		}
		if !modelParameterKeyPattern.MatchString(key) {
			return fmt.Errorf("model parameters: key %q must match %s", at, modelParameterKeyPattern)
		}
		switch value := object[key].(type) {
		case map[string]any:
			if err := validateModelParameterObject(value, at, depth+1); err != nil {
				return err
			}
		case []any:
			if len(value) > maxModelParameterArray {
				return fmt.Errorf("model parameters: %s holds %d items, at most %d", at, len(value), maxModelParameterArray)
			}
			for i, item := range value {
				switch item.(type) {
				case map[string]any, []any:
					return fmt.Errorf("model parameters: %s[%d] must be a boolean, number or string", at, i)
				}
				if err := validateModelParameterScalar(item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
					return err
				}
			}
		default:
			if err := validateModelParameterScalar(value, at); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateModelParameterScalar(value any, at string) error {
	switch v := value.(type) {
	case bool:
		return nil
	case string:
		if len(v) > maxModelParameterString || strings.ContainsRune(v, 0) {
			return fmt.Errorf("model parameters: %s must be a string of at most %d bytes without NUL", at, maxModelParameterString)
		}
		return nil
	case json.Number:
		f, err := v.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("model parameters: %s must be a finite number", at)
		}
		return nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("model parameters: %s must be a finite number", at)
		}
		return nil
	case float32:
		return validateModelParameterScalar(float64(v), at)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return nil
	case nil:
		return fmt.Errorf("model parameters: %s is null; use a boolean, number, string, object or array", at)
	default:
		return fmt.Errorf("model parameters: %s has unsupported type %T", at, value)
	}
}

func sortedParameterKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ParseModelParameters decodes and validates one JSON object. Numbers keep
// their written form. Empty input and {} mean no parameters (nil).
func ParseModelParameters(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	if len(raw) > 4*MaxModelParametersBytes {
		return nil, fmt.Errorf("model parameters: serialized size exceeds %d bytes", MaxModelParametersBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("model parameters: not valid JSON: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("model parameters: one JSON object expected")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("model parameters: a JSON object is required")
	}
	if len(object) == 0 {
		return nil, nil
	}
	if err := ValidateModelParameters(object); err != nil {
		return nil, err
	}
	return object, nil
}

// ValidateRequestOutputTokens is the one rule for an output cap per model
// request: 0 means the default (DefaultRequestOutputTokens), anything else
// must be within MinRequestOutputTokens..MaxRequestOutputTokens.
func ValidateRequestOutputTokens(tokens int64) error {
	if tokens != 0 && (tokens < MinRequestOutputTokens || tokens > MaxRequestOutputTokens) {
		return fmt.Errorf("max output tokens per request must be %d–%d (omit it for the default %d), got %d", MinRequestOutputTokens, MaxRequestOutputTokens, DefaultRequestOutputTokens, tokens)
	}
	return nil
}

// EffectiveRequestOutputTokens is the cap that applies: the default when unset.
func EffectiveRequestOutputTokens(tokens int64) int64 {
	if tokens == 0 {
		return DefaultRequestOutputTokens
	}
	return tokens
}
