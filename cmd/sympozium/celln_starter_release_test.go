package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

// The release republishes the previous starter package when the inputs that
// decide it are unchanged. These tests run the two scripts the release
// workflow runs, against a copy of the repository files they read.

func starterScriptsTree(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"bash", "python3", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	tree := t.TempDir()
	for _, name := range []string{"hack/celln-starter-inputs.sh", "hack/celln-starter-reuse.sh", "hack/build-celln-starter.sh", "config/celln/release.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		writeTreeFile(t, tree, name, string(data))
	}
	return tree
}

func writeTreeFile(t *testing.T, tree, name, content string) {
	t.Helper()
	path := filepath.Join(tree, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func starterFingerprint(t *testing.T, tree string, args ...string) string {
	t.Helper()
	out, err := exec.Command(filepath.Join(tree, "hack/celln-starter-inputs.sh"), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("celln-starter-inputs.sh: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestStarterInputsFingerprintFollowsOnlyThePackageInputs(t *testing.T) {
	tree := starterScriptsTree(t)
	base := starterFingerprint(t, tree)
	if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(base) {
		t.Fatalf("fingerprint %q is not sha256:<hex>", base)
	}
	if again := starterFingerprint(t, tree); again != base {
		t.Fatalf("fingerprint is not stable: %s then %s", base, again)
	}
	// The working tree's own fingerprint is what the copy computes: nothing
	// outside the listed inputs takes part.
	if live := starterFingerprint(t, filepath.Join("..", "..")); live != base {
		t.Fatalf("the repository computes %s, a copy of its inputs %s", live, base)
	}

	writeTreeFile(t, tree, "README.md", "unrelated")
	writeTreeFile(t, tree, "hack/unrelated.sh", "#!/bin/sh\n")
	if got := starterFingerprint(t, tree); got != base {
		t.Fatalf("an unrelated file changed the fingerprint: %s -> %s", base, got)
	}

	pin, err := os.ReadFile(filepath.Join(tree, "config/celln/release.json"))
	if err != nil {
		t.Fatal(err)
	}
	var release map[string]string
	if err := json.Unmarshal(pin, &release); err != nil {
		t.Fatal(err)
	}
	mutate := func(field, value string) string {
		changed := map[string]string{}
		for k, v := range release {
			changed[k] = v
		}
		changed[field] = value
		data, _ := json.Marshal(changed)
		writeTreeFile(t, tree, "config/celln/release.json", string(data))
		defer writeTreeFile(t, tree, "config/celln/release.json", string(pin))
		return starterFingerprint(t, tree)
	}
	if got := mutate("version", "v99.0.0"); got == base {
		t.Fatal("a new Celln version kept the fingerprint")
	}
	if got := mutate("archiveSHA256", strings.Repeat("0", 64)); got == base {
		t.Fatal("a new Celln archive kept the fingerprint")
	}
	// The installer image digest is not packaged.
	if got := mutate("imageDigest", "sha256:"+strings.Repeat("0", 64)); got != base {
		t.Fatal("the celln-installer image digest changed the fingerprint")
	}
	if got := starterFingerprint(t, tree); got != base {
		t.Fatalf("restoring the pin did not restore the fingerprint: %s -> %s", base, got)
	}

	if got := starterFingerprint(t, tree, "--tool-image", "busybox", "--tool-image", "curl"); got == base {
		t.Fatal("different tool images kept the fingerprint")
	}

	recipe, err := os.ReadFile(filepath.Join(tree, "hack/build-celln-starter.sh"))
	if err != nil {
		t.Fatal(err)
	}
	writeTreeFile(t, tree, "hack/build-celln-starter.sh", string(recipe)+"\n# changed\n")
	if got := starterFingerprint(t, tree); got == base {
		t.Fatal("a changed build script kept the fingerprint")
	}
}

func TestStarterInputsRejectAMalformedPin(t *testing.T) {
	tree := starterScriptsTree(t)
	writeTreeFile(t, tree, "config/celln/release.json", `{"version":"v1.0.0"}`)
	if out, err := exec.Command(filepath.Join(tree, "hack/celln-starter-inputs.sh")).CombinedOutput(); err == nil {
		t.Fatalf("a pin without an archive hash produced %s", out)
	}
}

func TestStarterReuseDecision(t *testing.T) {
	tree := starterScriptsTree(t)
	fingerprint := "sha256:" + strings.Repeat("a", 64)
	identity := map[string]any{
		"image":        "ghcr.io/sympozium-ai/sympozium/celln-starter@sha256:" + strings.Repeat("1", 64),
		"packageHash":  "blake3:" + strings.Repeat("2", 64),
		"publisher":    strings.Repeat("3", 64),
		"cellnVersion": "v0.5.28",
		"inputs":       fingerprint,
	}
	with := func(field string, value any) map[string]any {
		changed := map[string]any{}
		for k, v := range identity {
			changed[k] = v
		}
		if value == nil {
			delete(changed, field)
		} else {
			changed[field] = value
		}
		return changed
	}
	encode := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, tc := range []struct {
		name, previous, reason string
		reuse                  bool
	}{
		{name: "previous missing", reason: "no previous release published a celln-starter.json"},
		{name: "previous empty", previous: " ", reason: "not JSON"},
		{name: "not an object", previous: `[]`, reason: "not an object"},
		{name: "no inputs field", previous: encode(with("inputs", nil)), reason: "records no inputs"},
		{name: "mismatch", previous: encode(with("inputs", "sha256:"+strings.Repeat("b", 64))), reason: "package inputs changed"},
		{name: "tag-pinned image", previous: encode(with("image", "ghcr.io/sympozium-ai/sympozium/celln-starter:v1")), reason: "no usable image"},
		{name: "missing publisher", previous: encode(with("publisher", nil)), reason: "no usable publisher"},
		{name: "publisher that would split a helm value", previous: encode(with("publisher", "a,b=c")), reason: "no usable publisher"},
		{name: "malformed hash", previous: encode(with("packageHash", "blake3:zz")), reason: "no usable packageHash"},
		{name: "match", previous: encode(with("extra", "dropped")), reuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "celln-starter.json")
			if tc.previous != "" {
				if err := os.WriteFile(path, []byte(tc.previous), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(filepath.Join(tree, "hack/celln-starter-reuse.sh"), path, fingerprint)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if !tc.reuse {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 {
					t.Fatalf("want exit 1 (build), got %v\n%s", err, stderr.String())
				}
				if stdout.Len() != 0 {
					t.Fatalf("a build decision wrote an identity: %s", stdout.String())
				}
				if !strings.HasPrefix(stderr.String(), "building: ") || !strings.Contains(stderr.String(), tc.reason) {
					t.Fatalf("reason %q does not say %q", stderr.String(), tc.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("want reuse, got %v\n%s", err, stderr.String())
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("%v: %s", err, stdout.String())
			}
			if encode(got) != encode(identity) {
				t.Fatalf("reuse must republish exactly the previous identity:\n got %s\nwant %s", encode(got), encode(identity))
			}
		})
	}
}

// --check-image asks the registry; a digest that is gone must build.
func TestStarterReuseRequiresAPullableImage(t *testing.T) {
	tree := starterScriptsTree(t)
	fingerprint := "sha256:" + strings.Repeat("a", 64)
	previous := filepath.Join(t.TempDir(), "celln-starter.json")
	image := "ghcr.io/sympozium-ai/sympozium/celln-starter@sha256:" + strings.Repeat("1", 64)
	if err := os.WriteFile(previous, []byte(`{"image":"`+image+`","packageHash":"blake3:`+strings.Repeat("2", 64)+`","publisher":"p","cellnVersion":"v1","inputs":"`+fingerprint+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, docker string
		reuse        bool
	}{
		{name: "pullable", docker: "#!/bin/sh\n[ \"$1 $2 $3 $4\" = \"buildx imagetools inspect " + image + "\" ]\n", reuse: true},
		{name: "gone", docker: "#!/bin/sh\nexit 1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			writeTreeFile(t, bin, "docker", tc.docker)
			cmd := exec.Command(filepath.Join(tree, "hack/celln-starter-reuse.sh"), "--check-image", previous, fingerprint)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			if tc.reuse && err != nil {
				t.Fatalf("want reuse, got %v\n%s", err, stderr.String())
			}
			if !tc.reuse && (err == nil || !strings.Contains(stderr.String(), "no longer pullable")) {
				t.Fatalf("want a build because the image is gone, got %v\n%s", err, stderr.String())
			}
		})
	}
}

// The identity the build script writes and the one the release reuses carry
// the same fields, so either path feeds the CLI's linker flags unchanged.
func TestBuildScriptRecordsItsInputs(t *testing.T) {
	recipe, err := os.ReadFile(filepath.Join("..", "..", "hack/build-celln-starter.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`celln-starter-inputs.sh" "${inputs_args[@]}"`, `"cellnVersion": version, "inputs": inputs}`} {
		if !strings.Contains(string(recipe), want) {
			t.Fatalf("hack/build-celln-starter.sh no longer contains %q", want)
		}
	}
}

func TestFleetPackageUnchangedNotice(t *testing.T) {
	const installed, other = "blake3:aaaa", "blake3:bbbb"
	published := cellninstall.FleetPublication{Exists: true, Package: installed, Scope: "starter"}
	for _, tc := range []struct {
		name        string
		publication cellninstall.FleetPublication
		scope, hash string
		want        string
	}{
		{name: "first install", publication: cellninstall.FleetPublication{}, scope: "starter", hash: installed},
		{name: "same package", publication: published, scope: "starter", hash: installed, want: "Celln fleet package unchanged (blake3:aaaa); live conversations are kept"},
		{name: "same package, scope unrecorded", publication: cellninstall.FleetPublication{Exists: true, Package: installed}, scope: "starter", hash: installed, want: "Celln fleet package unchanged (blake3:aaaa); live conversations are kept"},
		{name: "new package", publication: published, scope: "starter", hash: other},
		{name: "new scope", publication: published, scope: "team", hash: installed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fleetPackageUnchangedNotice(tc.publication, tc.scope, tc.hash); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
