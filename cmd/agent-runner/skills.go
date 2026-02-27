package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const defaultSkillsDir = "/skills"

// loadSkills reads all skill files from the skills directory and returns
// their concatenated content suitable for prepending to the system prompt.
func loadSkills(skillsDir string) string {
	if skillsDir == "" {
		skillsDir = defaultSkillsDir
	}

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		slog.Info("skills.dir.not_found", "dir", skillsDir, "error", err)
		return ""
	}

	var sb strings.Builder
	count := 0
	for _, entry := range entries {
		// Skip directories and hidden files (Kubernetes projected volumes
		// create ..data, ..timestamp, etc.).
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(skillsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("skills.file.read_failed", "path", path, "error", err)
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString(content)
		count++
	}

	if count > 0 {
		slog.Info("skills.loaded", "count", count, "dir", skillsDir)
	}
	return sb.String()
}

// buildSystemPrompt assembles the full system prompt from the base prompt,
// loaded skills, and tool availability.
func buildSystemPrompt(base string, skills string, toolsEnabled bool) string {
	var sb strings.Builder

	sb.WriteString(base)

	if skills != "" {
		sb.WriteString("\n\n## Your Skills\n\n")
		sb.WriteString("The following skill instructions have been loaded. Follow them when they are relevant to the task:\n\n")
		sb.WriteString(skills)
	}

	if toolsEnabled {
		sb.WriteString("\n\n## Tool Usage\n\n")
		sb.WriteString("You have access to tools that let you execute commands, inspect files, fetch web content, and send messages through channels. ")
		sb.WriteString("When the task requires interacting with Kubernetes or running shell commands, ")
		sb.WriteString("use the `execute_command` tool to run them. The commands run inside a sidecar container ")
		sb.WriteString("that has kubectl and other CLI tools available.\n\n")
		sb.WriteString("**Important: You are running inside a Kubernetes pod with full cluster admin access. ")
		sb.WriteString("kubectl is pre-configured via a mounted ServiceAccount token and works out of the box. ")
		sb.WriteString("You have RBAC permissions to read all resources cluster-wide and manage workloads in any namespace. ")
		sb.WriteString("Do NOT check kubeconfig, contexts, or try to configure cluster access — just run kubectl commands directly. ")
		sb.WriteString("Commands like `kubectl get pods -A` and `kubectl get nodes` work. ")
		sb.WriteString("`kubectl config current-context` will always error in-cluster; this is normal and expected.**\n\n")
		sb.WriteString("Always use tools to gather real information rather than guessing. ")
		sb.WriteString("For example, if asked about pod status, run `kubectl get pods` rather than speculating.\n\n")
		sb.WriteString("After executing commands, summarise the results clearly for the user.\n\n")
		sb.WriteString("### Fetching Web Content\n\n")
		sb.WriteString("You have a `fetch_url` tool that lets you download and read web pages, API responses, ")
		sb.WriteString("and online documentation. HTML pages are automatically converted to readable plain text. ")
		sb.WriteString("Use this to research information, read docs, check endpoints, or download data from the internet.\n\n")
		sb.WriteString("### Writing Files\n\n")
		sb.WriteString("You have a `write_file` tool that lets you create or overwrite files under /workspace or /tmp. ")
		sb.WriteString("Use this to save reports, create scripts, write configuration files, or produce any output artifacts.\n\n")
		sb.WriteString("### Sending Messages Through Channels\n\n")
		sb.WriteString("You have a `send_channel_message` tool that lets you send messages through connected channels ")
		sb.WriteString("(WhatsApp, Telegram, Discord, Slack). Use it whenever the user asks you to notify someone, ")
		sb.WriteString("send a summary, or deliver any message. You can send to specific chat IDs, phone numbers, ")
		sb.WriteString("or leave the chatId empty to send to the device owner.\n")
		sb.WriteString("For WhatsApp, use the phone number in international format without + (e.g. '447450248165' for +44 7450 248165).\n")
		sb.WriteString("For Telegram, use the numeric chat ID.\n")
		sb.WriteString("For Discord/Slack, use the channel ID.")
	}

	return sb.String()
}
