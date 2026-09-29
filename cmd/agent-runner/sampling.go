package main

import (
	"log"
	"math"
	"strconv"
	"strings"
)

// samplingConfig carries the per-run model-tuning controls the controller
// injects from AgentRun.spec.model (inherited from the Agent when unset):
// THINKING_MODE, MAX_TOKENS and TEMPERATURE. Providers read it once at
// construction and apply it to every request they build.
type samplingConfig struct {
	// thinking is the normalized reasoning level: "" (off), minimal, low,
	// medium or high.
	thinking string
	// maxTokens caps output tokens per call; 0 means the provider default.
	maxTokens int64
	// temperature is the sampling temperature; NaN means the provider default.
	temperature float64
}

// samplingFromEnv reads the sampling controls from the agent container env.
func samplingFromEnv() samplingConfig {
	return samplingConfig{
		thinking:    normalizeThinking(getEnv("THINKING_MODE", "")),
		maxTokens:   parseMaxTokens(getEnv("MAX_TOKENS", "")),
		temperature: parseTemperature(getEnv("TEMPERATURE", "")),
	}
}

// normalizeThinking maps the thinking field to a reasoning level. Empty and
// the "off" spellings disable reasoning. Unknown values also disable it, with
// a log line, rather than failing the run: the field has always been
// free-form, so stored Agents may carry values outside the documented set.
func normalizeThinking(mode string) string {
	switch m := strings.ToLower(strings.TrimSpace(mode)); m {
	case "", "off", "none", "disabled", "false":
		return ""
	case "minimal", "low", "medium", "high":
		return m
	default:
		log.Printf("WARNING: unknown THINKING_MODE %q; treating as off (want off, minimal, low, medium or high)", mode)
		return ""
	}
}

// parseMaxTokens decodes MAX_TOKENS. Empty, malformed and non-positive values
// return 0, meaning "use the provider default".
func parseMaxTokens(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		log.Printf("WARNING: ignoring invalid MAX_TOKENS %q", s)
		return 0
	}
	return n
}

// parseTemperature decodes TEMPERATURE. Empty, malformed and negative values
// return NaN, meaning "use the provider default"; callers test math.IsNaN.
func parseTemperature(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		log.Printf("WARNING: ignoring invalid TEMPERATURE %q", s)
		return math.NaN()
	}
	return f
}
