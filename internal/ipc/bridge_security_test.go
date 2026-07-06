package ipc

import "testing"

func TestIsSafeIPCID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want bool
	}{
		{"simple", "abc123", true},
		{"uuid-like", "b3f1e2d4-5a6b-7c8d-9e0f-1a2b3c4d5e6f", true},
		{"empty", "", false},
		{"parent-traversal", "../../../tmp/evil", false},
		{"embedded-slash", "a/b", false},
		{"backslash", "a\\b", false},
		{"leading-dotdot", "..", false},
		{"dotdot-substring", "x..y", false},
		{"absolute", "/etc/passwd", false},
		{"too-long", string(make([]byte, 254)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSafeIPCID(tc.id); got != tc.want {
				t.Errorf("isSafeIPCID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}
