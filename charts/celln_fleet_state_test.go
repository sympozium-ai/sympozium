package charts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitPreparedScript renders the fleet and returns the wait-prepared init
// container's shell script.
func waitPreparedScript(t *testing.T) string {
	t.Helper()
	raw, err := renderNativeParent(t, fleetValues())
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	spec := decodeFleet(t, raw).daemonSets["celln-node"].Spec.Template.Spec
	if len(spec.InitContainers) != 1 || len(spec.InitContainers[0].Command) != 3 {
		t.Fatalf("unexpected wait-prepared shape: %+v", spec.InitContainers)
	}
	return spec.InitContainers[0].Command[2]
}

func TestWaitPreparedNamesAnUnreadableStateDirectory(t *testing.T) {
	script := waitPreparedScript(t)
	for _, want := range []string{`ls -A "$dir/."`, "stat -c '%u:%g mode %a'", "exit 1", "sudo mv $FLEET_STATE", "chown 0:0", "chcon", "$NODE_NAME"} {
		if !strings.Contains(script, want) {
			t.Fatalf("wait-prepared script lacks %q:\n%s", want, script)
		}
	}
	if strings.Index(script, "exit 1") > strings.Index(script, "until ") {
		t.Fatal("the unreadable check must run before the wait loop")
	}
}

// runWaitPrepared executes the rendered script against a state directory.
func runWaitPrepared(t *testing.T, script, state string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-ec", script)
	cmd.Env = append(os.Environ(), "FLEET_STATE="+state, "FLEET_PACKAGE_HEX=abc", "NODE_NAME=node-a")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestWaitPreparedFailsFastOnAnUnreadableStateDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory; the unreadable case cannot be staged")
	}
	script := waitPreparedScript(t)
	for _, unreadable := range []string{"", "authority"} {
		t.Run("unreadable "+filepath.Join("state", unreadable), func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "starter")
			if err := os.MkdirAll(filepath.Join(state, "authority"), 0700); err != nil {
				t.Fatal(err)
			}
			closed := filepath.Join(state, unreadable)
			if err := os.Chmod(closed, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(closed, 0700) })
			out, err := runWaitPrepared(t, script, state)
			if err == nil {
				t.Fatalf("an unreadable state directory must fail, got: %s", out)
			}
			for _, want := range []string{"cannot read the Celln state directory " + closed, "node-a", "mode 0", "sudo mv " + state} {
				if !strings.Contains(out, want) {
					t.Fatalf("message lacks %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "waiting for celln-node-configure") {
				t.Fatalf("an unreadable directory must not enter the wait loop:\n%s", out)
			}
		})
	}
}

func TestWaitPreparedPassesOncePrepared(t *testing.T) {
	script := waitPreparedScript(t)
	state := filepath.Join(t.TempDir(), "starter")
	if err := os.MkdirAll(filepath.Join(state, "authority"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"admitted-abc", "authority/trusted-parent-clients.json"} {
		if err := os.WriteFile(filepath.Join(state, f), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := runWaitPrepared(t, script, state); err != nil {
		t.Fatalf("prepared state must pass: %v: %s", err, out)
	}
}

func TestFleetPrepareResetsAStateDirectoryLeftToAnotherUID(t *testing.T) {
	raw, err := os.ReadFile("sympozium/files/celln/fleet-prepare.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	reset := strings.Index(script, `chown 0:0 "$dir"`)
	if reset < 0 || !strings.Contains(script, `for dir in "$FLEET_STATE" "$root"; do`) || !strings.Contains(script, "so the celln-node pod can read it") {
		t.Fatal("fleet-prepare.sh does not reset the owner of the state directories and say so")
	}
	if reset > strings.Index(script, `install -d -m 0700 "$FLEET_STATE"`) {
		t.Fatal("the owner is reset before the directories are (re)created 0700")
	}
	// Only the two directories the init container looks into change owner;
	// nothing is opened to group or other, and nothing is changed recursively.
	for _, loose := range []string{"chown -R", "chmod -R", "chmod 0755", "chmod 755", "a+r", "go+"} {
		if strings.Contains(script, loose) {
			t.Fatalf("fleet-prepare.sh loosens state permissions with %q", loose)
		}
	}
}
