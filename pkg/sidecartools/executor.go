package sidecartools

import _ "embed"

// ExecutorScript is the canonical tool-executor loop run by skill sidecars: it
// claims exec requests from /ipc/tools (filtered by SYMPOZIUM_SKILL_PACK), runs
// them in shell or argv mode, and writes the results back. The skill images
// build it in, and the controller mounts it into every skill sidecar at
// ExecutorScriptPath so a stock image can run it without a custom build.
//
//go:embed tool-executor.sh
var ExecutorScript string

const (
	// ExecutorMountDir is where the controller mounts the script directory in
	// skill sidecars.
	ExecutorMountDir = "/sympozium/bin"

	// ExecutorScriptName is the script's file name, and its ConfigMap key.
	ExecutorScriptName = "tool-executor.sh"

	// ExecutorScriptPath is the mounted script, for use as a SkillPack sidecar
	// command: command: ["/sympozium/bin/tool-executor.sh"].
	ExecutorScriptPath = ExecutorMountDir + "/" + ExecutorScriptName
)
