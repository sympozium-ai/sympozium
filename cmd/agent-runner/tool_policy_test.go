package main

import (
	"testing"
)

func TestApplyToolPolicy(t *testing.T) {
	allTools := []ToolDef{
		{Name: "read_file"},
		{Name: "write_file"},
		{Name: "execute_command"},
		{Name: "delegate_to_persona"},
		{Name: "memory_store"},
	}

	names := func(tools []ToolDef) []string {
		out := make([]string, len(tools))
		for i, t := range tools {
			out[i] = t.Name
		}
		return out
	}

	cases := []struct {
		name      string
		allow     string
		deny      string
		wantNames []string
	}{
		{
			name:      "deny only — removes listed tools",
			deny:      "execute_command,delegate_to_persona",
			wantNames: []string{"read_file", "write_file", "memory_store"},
		},
		{
			name:      "allow only — keeps only listed tools",
			allow:     "read_file,write_file",
			wantNames: []string{"read_file", "write_file"},
		},
		{
			name:      "both — deny wins on conflict",
			allow:     "read_file,write_file,execute_command",
			deny:      "execute_command",
			wantNames: []string{"read_file", "write_file"},
		},
		{
			name:      "tool in both lists is denied",
			allow:     "write_file",
			deny:      "write_file",
			wantNames: []string{},
		},
		{
			name:      "whitespace and empty entries are ignored",
			deny:      " execute_command , , delegate_to_persona ",
			wantNames: []string{"read_file", "write_file", "memory_store"},
		},
		{
			name:      "empty deny string — no filtering",
			deny:      "",
			wantNames: []string{"read_file", "write_file", "execute_command", "delegate_to_persona", "memory_store"},
		},
		{
			name:      "empty allow string with deny — blocklist mode",
			allow:     "",
			deny:      "memory_store",
			wantNames: []string{"read_file", "write_file", "execute_command", "delegate_to_persona"},
		},
		{
			name:      "allow all — everything passes",
			allow:     "read_file,write_file,execute_command,delegate_to_persona,memory_store",
			wantNames: []string{"read_file", "write_file", "execute_command", "delegate_to_persona", "memory_store"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := make([]ToolDef, len(allTools))
			copy(input, allTools)

			got := applyToolPolicy(input, tc.allow, tc.deny)
			gotNames := names(got)

			if len(gotNames) != len(tc.wantNames) {
				t.Fatalf("got %d tools %v, want %d tools %v", len(gotNames), gotNames, len(tc.wantNames), tc.wantNames)
			}
			for i, name := range tc.wantNames {
				if gotNames[i] != name {
					t.Errorf("tool[%d] = %q, want %q", i, gotNames[i], name)
				}
			}
		})
	}
}
