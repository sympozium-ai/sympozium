package tooloutput

const (
	// MaxBytesEnv configures the byte limit for built-in and MCP tool output.
	MaxBytesEnv = "MAX_TOOL_OUTPUT_BYTES"

	// DefaultMaxBytes preserves the existing agent-runner output limit.
	DefaultMaxBytes = 8_000
)
