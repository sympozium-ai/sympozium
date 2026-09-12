package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	cmd, fixtures, err := parseCLI(os.Args[1:])
	if err == nil {
		switch cmd {
		case "gen":
			err = generateFixtures(fixtures)
		case "verify":
			err = verifyFixtures(fixtures)
		default:
			err = fmt.Errorf("unknown command %q", cmd)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func parseCLI(args []string) (string, string, error) {
	cmd := "verify"
	rest := args
	if len(rest) > 0 && (rest[0] == "verify" || rest[0] == "gen") {
		cmd = rest[0]
		rest = rest[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fixtures := fs.String("fixtures", "test/fixtures/celln-authorisation/v1", "fixture directory")
	if err := fs.Parse(rest); err != nil {
		return "", "", err
	}
	if fs.NArg() > 0 {
		if len(args) > 0 && (args[0] == "verify" || args[0] == "gen") {
			return "", "", fmt.Errorf("unexpected arguments: %v", fs.Args())
		}
		cmd = fs.Arg(0)
		if fs.NArg() > 1 {
			return "", "", fmt.Errorf("unexpected arguments: %v", fs.Args()[1:])
		}
	}
	return cmd, *fixtures, nil
}

func readStrict(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return strictDecode(b, v)
}

func readGzipStrict(path string, target any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, 1<<20))
	if err != nil {
		return err
	}
	return strictDecode(raw, target)
}

func verifyFixtures(dir string) error {
	if err := verifyBundle(dir); err != nil {
		return err
	}
	var m Manifest
	if err := readStrict(filepath.Join(dir, "manifest.json"), &m); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if m.APIVersion != "celln.sympozium.ai/conformance-manifest-v2" {
		return fmt.Errorf("unsupported manifest version")
	}
	if err := verifyInventory("vectors", m.VectorNames, requiredVectorNames); err != nil {
		return err
	}
	if err := verifyInventory("sequences", m.SequenceNames, requiredSequenceNames); err != nil {
		return err
	}
	var c Cases
	if err := readGzipStrict(filepath.Join(dir, "cases.json.gz"), &c); err != nil {
		return fmt.Errorf("cases: %w", err)
	}
	if c.APIVersion != "celln.sympozium.ai/conformance-cases-v2" {
		return fmt.Errorf("unsupported cases version")
	}
	actualV := make([]string, 0, len(c.Vectors))
	for _, v := range c.Vectors {
		actualV = append(actualV, v.Name)
	}
	if err := verifyInventory("case vectors", actualV, m.VectorNames); err != nil {
		return err
	}
	actualS := make([]string, 0, len(c.Sequences))
	for _, s := range c.Sequences {
		actualS = append(actualS, s.Name)
	}
	if err := verifyInventory("case sequences", actualS, m.SequenceNames); err != nil {
		return err
	}
	var jwks JWKS
	if err := readStrict(filepath.Join(dir, "signing", "test-jwks.json"), &jwks); err != nil {
		return err
	}
	activeJWKS = jwks
	passed := 0
	for _, v := range c.Vectors {
		df, ok := c.Decisions[v.DecisionRef]
		if !ok {
			return fmt.Errorf("%s references missing decision %s", v.Name, v.DecisionRef)
		}
		var decision Decision
		if err := strictDecode([]byte(df.Canonical), &decision); err != nil {
			return fmt.Errorf("%s decision: %w", v.Name, err)
		}
		raw, _ := json.Marshal(decision)
		canon, err := canonicalizeJSON(raw)
		if err != nil {
			return fmt.Errorf("%s canonical: %w", v.Name, err)
		}
		if string(canon) != df.Canonical {
			return fmt.Errorf("%s canonical bytes differ", v.Name)
		}
		if sha256Digest(canon) != v.DecisionRef {
			return fmt.Errorf("%s digest differs", v.Name)
		}
		if df.RequestCanonical != "" {
			rc, err := canonicalizeJSON([]byte(df.RequestCanonical))
			if err != nil {
				return fmt.Errorf("%s request: %w", v.Name, err)
			}
			if !bytes.Equal(rc, []byte(df.RequestCanonical)) {
				return fmt.Errorf("%s request is not canonical", v.Name)
			}
			if sha256Digest(rc) != decision.RequestDigest {
				return fmt.Errorf("%s decision requestDigest does not bind fixture request", v.Name)
			}
		}
		var outcome, reason string
		switch v.Evaluator {
		case "verify":
			if v.Verify == nil {
				return fmt.Errorf("%s missing verify context", v.Name)
			}
			outcome, reason, err = Verify(v.Credential, raw, *v.Verify)
		case "resolver":
			if v.Resolver == nil {
				return fmt.Errorf("%s missing resolver context", v.Name)
			}
			outcome, reason = ResolveCheck(decision, *v.Resolver)
		default:
			return fmt.Errorf("%s unknown evaluator %q", v.Name, v.Evaluator)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", v.Name, err)
		}
		if outcome != v.Expect.Outcome || reason != v.Expect.Reason {
			return fmt.Errorf("%s expected %s/%s got %s/%s", v.Name, v.Expect.Outcome, v.Expect.Reason, outcome, reason)
		}
		fmt.Printf("ok   %-40s %s%s\n", v.Name, outcome, formatReason(reason))
		passed++
	}
	for _, s := range c.Sequences {
		l := newLedger(s.RunCap, s.TurnCap)
		for i, a := range s.Actions {
			got := l.reserve(a.TurnID, a.ID, a.Digest, a.ReserveOutput)
			if got != a.Expect {
				return fmt.Errorf("%s action %d expected %s got %s", s.Name, i, a.Expect, got)
			}
		}
		fmt.Printf("ok   %-40s sequence\n", s.Name)
		passed++
	}
	bundle, _ := os.ReadFile(filepath.Join(dir, "bundle", "BUNDLE.sha256"))
	fmt.Printf("\n%d/%d conformance cases passed; bundle %s\n", passed, len(c.Vectors)+len(c.Sequences), strings.TrimSpace(string(bundle)))
	return nil
}
func formatReason(r string) string {
	if r == "" {
		return ""
	}
	return " " + r
}

func verifyInventory(label string, actual, required []string) error {
	if len(actual) == 0 {
		return fmt.Errorf("%s inventory is empty", label)
	}
	seen := map[string]bool{}
	for _, n := range actual {
		if n == "" || seen[n] {
			return fmt.Errorf("%s contains empty/duplicate %q", label, n)
		}
		seen[n] = true
	}
	if len(actual) != len(required) {
		return fmt.Errorf("%s inventory count %d want %d", label, len(actual), len(required))
	}
	for _, n := range required {
		if !seen[n] {
			return fmt.Errorf("%s missing required %q", label, n)
		}
	}
	return nil
}

func verifyBundle(dir string) error {
	sumsRaw, err := os.ReadFile(filepath.Join(dir, "bundle", "SHA256SUMS"))
	if err != nil {
		return err
	}
	pinRaw, err := os.ReadFile(filepath.Join(dir, "bundle", "BUNDLE.sha256"))
	if err != nil {
		return err
	}
	h := sha256.Sum256(sumsRaw)
	pin := "sha256:" + hex.EncodeToString(h[:])
	if strings.TrimSpace(string(pinRaw)) != pin {
		return fmt.Errorf("bundle pin mismatch")
	}
	expected := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(sumsRaw)), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			return fmt.Errorf("malformed SHA256SUMS line")
		}
		if _, ok := expected[parts[1]]; ok {
			return fmt.Errorf("duplicate SHA256SUMS path %s", parts[1])
		}
		expected[parts[1]] = parts[0]
	}
	actual, err := computeSums(dir)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("bundle inventory mismatch: %d listed, %d actual", len(expected), len(actual))
	}
	for _, e := range actual {
		if expected[e.rel] != e.hash {
			return fmt.Errorf("bundle mismatch for %s", e.rel)
		}
	}
	return nil
}

func sortedCopy(in []string) []string { o := append([]string{}, in...); sort.Strings(o); return o }
