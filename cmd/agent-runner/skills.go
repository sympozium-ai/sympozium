package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

const defaultSkillsDir = "/skills"

// skillEntry is one indexed skill file. The controller mounts each skill
// ConfigMap (one per SkillPack or configMapRef) at /skills/<ref>/, so Name is
// "<ref>/<file>" with a trailing .md dropped. A file directly under /skills
// (the flat layout of older controllers) keeps its bare name.
type skillEntry struct {
	Name        string
	Path        string
	Description string
}

// skillMeta is the YAML frontmatter the SkillPack controller writes at the top
// of every skill file. Hand-authored ConfigMaps may carry their own or none.
type skillMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// loadSkillIndex scans skillsDir one pack deep and returns the skills sorted by
// name. Bodies stay on disk: the model loads them through the skills tool, or
// inlineSkills reads them when that tool is unavailable.
func loadSkillIndex(skillsDir string) []skillEntry {
	if skillsDir == "" {
		skillsDir = defaultSkillsDir
	}

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		slog.Info("skills.dir.not_found", "dir", skillsDir, "error", err)
		return nil
	}

	var skills []skillEntry
	for _, entry := range entries {
		// Skip hidden entries (ConfigMap volumes create ..data, ..timestamp, etc.).
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(skillsDir, entry.Name())
		if entry.IsDir() {
			skills = append(skills, loadPackSkills(path, entry.Name())...)
			continue
		}
		if s, ok := readSkillEntry(path, skillName("", entry.Name())); ok {
			skills = append(skills, s)
		}
	}

	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	if len(skills) > 0 {
		slog.Info("skills.indexed", "count", len(skills), "dir", skillsDir)
	}
	return skills
}

// loadPackSkills indexes the files of one mounted skill ConfigMap. They are
// symlinks into the volume's ..data directory, which DirEntry reports as
// non-directories.
func loadPackSkills(packDir, pack string) []skillEntry {
	entries, err := os.ReadDir(packDir)
	if err != nil {
		slog.Warn("skills.pack.read_failed", "dir", packDir, "error", err)
		return nil
	}
	var skills []skillEntry
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if s, ok := readSkillEntry(filepath.Join(packDir, entry.Name()), skillName(pack, entry.Name())); ok {
			skills = append(skills, s)
		}
	}
	return skills
}

func skillName(pack, file string) string {
	name := strings.TrimSuffix(file, ".md")
	if pack == "" {
		return name
	}
	return pack + "/" + name
}

// readSkillEntry indexes one skill file, skipping unreadable or empty ones.
func readSkillEntry(path, name string) (skillEntry, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("skills.file.read_failed", "path", path, "error", err)
		return skillEntry{}, false
	}
	meta, body := splitFrontmatter(string(data))
	if strings.TrimSpace(body) == "" {
		return skillEntry{}, false
	}
	desc := meta.Description
	if desc == "" {
		desc = legacySkillSummary(body)
	}
	return skillEntry{Name: name, Path: path, Description: desc}, true
}

// splitFrontmatter separates a leading YAML frontmatter block (fenced by "---"
// lines) from the Markdown body. Content without a block, or whose block is
// unterminated or does not parse, is returned whole as the body.
func splitFrontmatter(content string) (skillMeta, string) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return skillMeta{}, content
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") != "---" {
			continue
		}
		var meta skillMeta
		if err := yaml.Unmarshal([]byte(strings.Join(lines[1:i], "\n")), &meta); err != nil {
			return skillMeta{}, content
		}
		meta.Description = strings.Join(strings.Fields(meta.Description), " ")
		return meta, strings.Join(lines[i+1:], "\n")
	}
	return skillMeta{}, content
}

// legacySkillSummary describes a skill file that has no frontmatter description:
// the "> description" line older SkillPack controllers wrote under the heading,
// otherwise the first line that is not a heading.
func legacySkillSummary(body string) string {
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, ">"))
	}
	return ""
}

// inlineSkills renders every skill body for the system prompt, the delivery
// used when the run cannot call the skills tool. A skill with a frontmatter
// description gets the "# name / > description" header older controllers
// baked into the file, so the inlined prompt matches what it was before.
func inlineSkills(skills []skillEntry) string {
	var sb strings.Builder
	for _, s := range skills {
		data, err := os.ReadFile(s.Path)
		if err != nil {
			slog.Warn("skills.file.read_failed", "path", s.Path, "error", err)
			continue
		}
		meta, body := splitFrontmatter(string(data))
		content := strings.TrimSpace(body)
		if meta.Description != "" {
			title := meta.Name
			if title == "" {
				title = s.Name
			}
			content = fmt.Sprintf("# %s\n\n> %s\n\n%s", title, meta.Description, content)
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString(content)
	}
	return sb.String()
}

// buildSystemPrompt assembles the full system prompt from the base prompt,
// the indexed skills, and tool availability. With skillsTool the prompt only
// points at the skills tool, whose description carries the catalog; without
// it the skill bodies are inlined.
func buildSystemPrompt(base string, skills []skillEntry, skillsTool, toolsEnabled bool) string {
	var sb strings.Builder

	sb.WriteString(base)

	if len(skills) > 0 && skillsTool {
		sb.WriteString("\n\n## Your Skills\n\n")
		fmt.Fprintf(&sb, "You have %d skill(s): operator-written instructions for specific kinds of work, "+
			"listed with a one-line summary each in the description of the `%s` tool. "+
			"When a task matches a skill, call `%s` with its name to load the full instructions "+
			"before starting the work, and follow them rather than working from the summary.",
			len(skills), ToolSkills, ToolSkills)
	} else if inlined := inlineSkills(skills); inlined != "" {
		sb.WriteString("\n\n## Your Skills\n\n")
		sb.WriteString("The following skill instructions have been loaded. Follow them when they are relevant to the task:\n\n")
		sb.WriteString(inlined)
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
