package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// skillRegistry maps skill names ("<ref>/<file>") to their indexed entries.
// Populated by registerSkills only when the skills tool is offered, then
// read-only during tool dispatch.
var (
	skillRegistry   = map[string]skillEntry{}
	skillRegistryMu sync.RWMutex
)

func registerSkills(skills []skillEntry) {
	skillRegistryMu.Lock()
	defer skillRegistryMu.Unlock()
	skillRegistry = make(map[string]skillEntry, len(skills))
	for _, s := range skills {
		skillRegistry[s.Name] = s
	}
}

func lookupSkill(name string) (skillEntry, bool) {
	skillRegistryMu.RLock()
	defer skillRegistryMu.RUnlock()
	s, ok := skillRegistry[name]
	return s, ok
}

// skillsToolEnabled reports whether skills are delivered through the skills
// tool instead of being inlined into the system prompt. It needs tools, at
// least one skill (an empty enum is invalid for some providers), and a tool
// policy that keeps the tool — a run whose allow list does not name `skills`
// still gets its skills inline rather than a catalog it cannot open.
func skillsToolEnabled(skills []skillEntry, toolsEnabled bool, allowList, denyList string) bool {
	if !toolsEnabled || len(skills) == 0 {
		return false
	}
	return len(applyToolPolicy([]ToolDef{{Name: ToolSkills}}, allowList, denyList)) == 1
}

// skillsToolDef builds the skills tool. The catalog lives in the description,
// so skill bodies stay out of the context until the model asks for one.
func skillsToolDef(skills []skillEntry) ToolDef {
	names := make([]string, 0, len(skills))
	var catalog strings.Builder
	for _, s := range skills {
		names = append(names, s.Name)
		fmt.Fprintf(&catalog, "- `%s`", s.Name)
		if s.Description != "" {
			fmt.Fprintf(&catalog, ": %s", s.Description)
		}
		catalog.WriteByte('\n')
	}

	return ToolDef{
		Name: ToolSkills,
		Description: "Load the full instructions for one of your skills. Each skill is an " +
			"operator-written procedure (commands to run, files to read, checks to make). " +
			"Call this before starting work that matches a skill's summary — the instructions " +
			"are authoritative and should not be guessed from the summary.\n\nAvailable skills:\n" +
			catalog.String(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Name of the skill to load, exactly as listed.",
					"enum":        names,
				},
			},
			"required": []string{"name"},
		},
	}
}

// skillsTool returns the body of the named skill, without its frontmatter.
func skillsTool(args map[string]any) string {
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "Error: 'name' is required — pass one of the skill names listed in the tool description."
	}
	entry, ok := lookupSkill(name)
	if !ok {
		return fmt.Sprintf("Error: unknown skill %q. Pass one of the skill names listed in the tool description.", name)
	}
	data, err := os.ReadFile(entry.Path)
	if err != nil {
		return fmt.Sprintf("Error: loading skill %q: %v", name, err)
	}
	_, body := splitFrontmatter(string(data))
	return strings.TrimSpace(body)
}
