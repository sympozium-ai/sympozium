package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type sidecarToolManifest struct {
	Tools []sidecarToolEntry `json:"tools"`
}

type sidecarToolEntry struct {
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Target         string         `json:"target"`
	Subcommand     string         `json:"subcommand"`
	Exec           string         `json:"exec,omitempty"`    // executable command prefix (default: "node /app/dist/cli.js")
	InputMode      string         `json:"inputMode"`         // "stdin" or "args"
	PositionalArgs []string       `json:"positionalArgs"`    // parameter names to pass as positional CLI args (in order)
	Timeout        int            `json:"timeout,omitempty"` // per-tool timeout in seconds (0 = use default, max 120s)
	Parameters     map[string]any `json:"parameters"`
}

const defaultSidecarExec = "node /app/dist/cli.js"

var (
	sidecarToolRegistry   = map[string]sidecarToolEntry{}
	sidecarToolRegistryMu sync.RWMutex
)

// loadSidecarTools reads all sidecar-tools-*.json manifests from ipcToolsDir
// and returns ToolDef entries for the LLM tool list. It also populates
// sidecarToolRegistry for dispatch. Waits up to 5 seconds for at least one
// manifest to appear (sidecars may still be starting).
func loadSidecarTools(ipcToolsDir string) []ToolDef {
	pattern := filepath.Join(ipcToolsDir, "sidecar-tools-*.json")

	var files []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		files, err = filepath.Glob(pattern)
		if err == nil && len(files) > 0 {
			break
		}
		files = nil
		time.Sleep(500 * time.Millisecond)
	}

	if len(files) == 0 {
		return nil
	}

	var allTools []ToolDef
	sidecarToolRegistryMu.Lock()
	defer sidecarToolRegistryMu.Unlock()

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			log.Printf("sidecar_tools: failed to read %s: %v", f, err)
			continue
		}

		var manifest sidecarToolManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			log.Printf("sidecar_tools: failed to parse %s: %v", f, err)
			continue
		}

		for _, entry := range manifest.Tools {
			entry.Target = normalizeSidecarTarget(entry.Target)
			if entry.InputMode != "stdin" && entry.InputMode != "args" && entry.InputMode != "" {
				log.Printf("sidecar_tools: %s: unrecognized inputMode %q, defaulting to args", entry.Name, entry.InputMode)
				entry.InputMode = "args"
			}
			sidecarToolRegistry[entry.Name] = entry
			allTools = append(allTools, ToolDef{
				Name:        entry.Name,
				Description: entry.Description,
				Parameters:  entry.Parameters,
			})
			log.Printf("sidecar_tools: registered %s (target=%s, subcommand=%s)",
				entry.Name, entry.Target, entry.Subcommand)
		}
	}

	return allTools
}

func lookupSidecarTool(name string) (sidecarToolEntry, bool) {
	sidecarToolRegistryMu.RLock()
	defer sidecarToolRegistryMu.RUnlock()
	entry, ok := sidecarToolRegistry[name]
	return entry, ok
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// buildSidecarCommand constructs the shell command string for a sidecar tool
// invocation without dispatching it. Exported for testing.
//
// In args mode, only parameters listed in PositionalArgs are passed (as
// positional CLI arguments in declared order). Parameters not in PositionalArgs
// are intentionally dropped — args-mode sidecars receive input solely through
// positional arguments; named parameters exist in the schema for the LLM's
// benefit but are not forwarded.
func buildSidecarCommand(tool sidecarToolEntry, argsJSON string) string {
	exec := tool.Exec
	if exec == "" {
		exec = defaultSidecarExec
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}

	var posSuffix string
	if len(tool.PositionalArgs) > 0 {
		var parts []string
		for _, key := range tool.PositionalArgs {
			if val, ok := args[key]; ok {
				parts = append(parts, shellQuote(fmt.Sprintf("%v", val)))
				delete(args, key)
			}
		}
		if len(parts) > 0 {
			posSuffix = " " + strings.Join(parts, " ")
		}
	}

	if tool.InputMode == "stdin" {
		stdinJSON, err := json.Marshal(args)
		if err != nil {
			return ""
		}
		escaped := shellQuote(string(stdinJSON))
		return fmt.Sprintf("echo %s | %s %s%s",
			escaped, exec, tool.Subcommand, posSuffix)
	}
	return fmt.Sprintf("%s %s%s", exec, tool.Subcommand, posSuffix)
}

// executeSidecarTool constructs the shell command for a sidecar native tool
// and dispatches it via the existing IPC executeCommand mechanism.
func executeSidecarTool(ctx context.Context, tool sidecarToolEntry, argsJSON string) string {
	command := buildSidecarCommand(tool, argsJSON)
	if command == "" {
		return "Error building sidecar tool command"
	}

	execArgs := map[string]any{
		"command": command,
		"target":  tool.Target,
	}
	if tool.Timeout > 0 {
		execArgs["timeout"] = float64(tool.Timeout)
	}
	return executeCommand(ctx, execArgs)
}
