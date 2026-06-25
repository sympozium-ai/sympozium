package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// sidecarToolEntry mirrors the JSON the controller writes into the read-only
// sidecar-tools manifest (see buildSidecarToolsManifest in
// internal/controller/agentrun_controller.go). The definitions originate from
// the SkillPack CRD and are admission-validated, so the running agent consumes
// them but cannot forge or alter them. In particular Exec (the binary) and
// Target (the IPC routing key) are controller-supplied, not model-supplied.
type sidecarToolEntry struct {
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Target         string         `json:"target"`
	Exec           []string       `json:"exec"`
	Subcommand     string         `json:"subcommand"`
	InputMode      string         `json:"inputMode"` // "args" (default) or "stdin"
	PositionalArgs []string       `json:"positionalArgs"`
	Parameters     map[string]any `json:"parameters"`
}

type sidecarToolManifest struct {
	Tools []sidecarToolEntry `json:"tools"`
}

var (
	sidecarToolRegistry   = map[string]sidecarToolEntry{}
	sidecarToolRegistryMu sync.RWMutex

	// sidecarToolsLoadTimeout is how long loadSidecarTools waits for the
	// ConfigMap-mounted manifest to appear. Overridable in tests.
	sidecarToolsLoadTimeout = 5 * time.Second
)

// loadSidecarTools reads the controller-written manifest at manifestPath and
// returns ToolDef entries for the LLM tool list, populating sidecarToolRegistry
// for dispatch. The manifest is mounted read-only from a ConfigMap, so unlike
// the legacy agent-dropped approach the agent cannot modify it. Waits up to 5
// seconds for the file to appear (the ConfigMap mount may lag pod start).
func loadSidecarTools(manifestPath string) []ToolDef {
	var data []byte
	deadline := time.Now().Add(sidecarToolsLoadTimeout)
	for {
		var err error
		data, err = os.ReadFile(manifestPath)
		if err == nil && len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			// The caller only invokes us when SIDECAR_TOOLS_MANIFEST_PATH is set,
			// so an absent manifest here means the controller-written ConfigMap
			// never mounted (mount lag, oversize/failed ConfigMap). Surface it
			// loudly rather than starting with native tools silently missing.
			log.Printf("sidecar_tools: WARNING manifest %s did not appear within %s — native sidecar tools are unavailable for this run",
				manifestPath, sidecarToolsLoadTimeout)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	var manifest sidecarToolManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		log.Printf("sidecar_tools: failed to parse %s: %v", manifestPath, err)
		return nil
	}

	var allTools []ToolDef
	sidecarToolRegistryMu.Lock()
	defer sidecarToolRegistryMu.Unlock()

	for _, entry := range manifest.Tools {
		// Runtime backstop against name shadowing. Admission already rejects
		// collisions with built-in/memory tools and duplicates across SkillPacks,
		// but MCP tool names are discovered dynamically and cannot be checked at
		// admission, so guard here too: skip rather than silently shadow.
		if _, dup := sidecarToolRegistry[entry.Name]; dup {
			log.Printf("sidecar_tools: skipping duplicate tool name %q", entry.Name)
			continue
		}
		if sidecarToolNameReserved(entry.Name) {
			log.Printf("sidecar_tools: skipping tool %q — name collides with a built-in, memory, or MCP tool", entry.Name)
			continue
		}

		params := entry.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		sidecarToolRegistry[entry.Name] = entry
		allTools = append(allTools, ToolDef{
			Name:        entry.Name,
			Description: entry.Description,
			Parameters:  params,
		})
		log.Printf("sidecar_tools: registered %s (target=%s, exec=%v, subcommand=%s)",
			entry.Name, entry.Target, entry.Exec, entry.Subcommand)
	}

	return allTools
}

// sidecarToolNameReserved reports whether name is already claimed by an
// earlier-dispatched tool (built-in, memory, workflow-memory, or a registered
// MCP tool), in which case a sidecar tool of the same name would be shadowed.
func sidecarToolNameReserved(name string) bool {
	switch name {
	case ToolExecuteCommand, ToolReadFile, ToolWriteFile, ToolListDirectory,
		ToolSendChannelMessage, ToolFetchURL, ToolScheduleTask,
		ToolDelegateToPersona, ToolSpawnSubagents:
		return true
	}
	if isMemoryTool(name) || isWorkflowMemoryTool(name) {
		return true
	}
	if _, ok := lookupMCPTool(name); ok {
		return true
	}
	return false
}

func lookupSidecarTool(name string) (sidecarToolEntry, bool) {
	sidecarToolRegistryMu.RLock()
	defer sidecarToolRegistryMu.RUnlock()
	entry, ok := sidecarToolRegistry[name]
	return entry, ok
}

// buildSidecarExecRequest converts a native tool call into an argv-mode
// execRequest. The executable and positional arguments are passed as discrete
// argv elements (no shell), so argument values can never inject shell syntax.
// For stdin-mode tools the remaining (non-positional) arguments are delivered as
// a JSON object on the process stdin rather than interpolated into a command.
func buildSidecarExecRequest(ctx context.Context, tool sidecarToolEntry, argsJSON string) (execRequest, error) {
	args := map[string]any{}
	if argsJSON != "" {
		// UseNumber keeps integers exact: without it every JSON number decodes
		// to float64 and a large id formats in scientific notation.
		dec := json.NewDecoder(strings.NewReader(argsJSON))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return execRequest{}, fmt.Errorf("parsing sidecar tool arguments: %w", err)
		}
	}

	// argv = Exec prefix + optional fixed subcommand + "--" + positional values.
	// The "--" end-of-options marker ensures a model-supplied positional value
	// beginning with "-" is treated as an operand, not a flag, by the wrapped
	// binary (wrapped CLIs must honor "--"; see docs/guides/writing-sidecars.md).
	argv := append([]string{}, tool.Exec...)
	if tool.Subcommand != "" {
		argv = append(argv, tool.Subcommand)
	}
	if len(tool.PositionalArgs) > 0 {
		argv = append(argv, "--")
	}
	for _, key := range tool.PositionalArgs {
		if val, ok := args[key]; ok {
			argv = append(argv, formatPositionalArg(val))
			// Remove so it is not also sent on stdin.
			delete(args, key)
		}
	}

	var stdin string
	if tool.InputMode == "stdin" {
		// Re-marshal the remaining (non-positional) args as the stdin payload.
		stdinJSON, err := json.Marshal(args)
		if err != nil {
			return execRequest{}, fmt.Errorf("marshalling sidecar tool stdin: %w", err)
		}
		stdin = string(stdinJSON)
	}

	id := fmt.Sprintf("%d", time.Now().UnixNano())
	req := execRequest{
		ID:      id,
		Argv:    argv,
		Stdin:   stdin,
		WorkDir: "/workspace",
		Timeout: 120,
		Target:  normalizeSidecarTarget(tool.Target),
	}
	req.Meta = traceMetadata(ctx)
	return req, nil
}

// formatPositionalArg renders a decoded argument value as a single CLI argument.
// Strings pass through verbatim; numbers (json.Number) keep their exact literal
// form (no float64 rounding or scientific notation); composites and bools are
// rendered as canonical JSON. The result is always one argv element.
func formatPositionalArg(val any) string {
	switch v := val.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case nil:
		return ""
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}

// executeSidecarTool dispatches a native sidecar tool through the gated exec IPC
// in argv mode, targeting the tool's owning sidecar.
func executeSidecarTool(ctx context.Context, tool sidecarToolEntry, argsJSON string) string {
	req, err := buildSidecarExecRequest(ctx, tool, argsJSON)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return dispatchExecRequest(req, fmt.Sprintf("%s %v", tool.Name, req.Argv))
}
