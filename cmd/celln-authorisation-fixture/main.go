package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func main() {
	var fixtures string
	flag.StringVar(&fixtures, "fixtures", "test/fixtures/celln-authorisation/v1", "fixture directory")
	flag.Parse()

	args := flag.Args()
	cmd := "verify"
	if len(args) > 0 {
		cmd = args[0]
	}
	var err error
	switch cmd {
	case "gen":
		err = generateFixtures(fixtures)
	case "verify":
		err = verifyFixtures(fixtures)
	default:
		err = fmt.Errorf("unknown command %q (want gen|verify)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

type resultRow struct {
	name   string
	expect Expect
	got    string // "" means accepted
	ok     bool
	note   string
}

func verifyFixtures(dir string) error {
	var manifest Manifest
	if err := readJSON(filepath.Join(dir, "manifest.json"), &manifest); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	var jwks JWKS
	if err := readJSON(filepath.Join(dir, "signing", "test-jwks.json"), &jwks); err != nil {
		return fmt.Errorf("jwks: %w", err)
	}
	activeJWKS = jwks

	decisionSchema, err := compileSchema(filepath.Join(dir, "schema", "decision.schema.json"))
	if err != nil {
		return fmt.Errorf("decision schema: %w", err)
	}
	credentialSchema, err := compileSchema(filepath.Join(dir, "schema", "credential.schema.json"))
	if err != nil {
		return fmt.Errorf("credential schema: %w", err)
	}

	// Verify the pinned bundle before trusting any expectation.
	if err := verifyBundle(dir, manifest.BundleHash); err != nil {
		return err
	}

	var rows []resultRow
	for _, mv := range manifest.Vectors {
		vdir := filepath.Join(dir, "vectors", mv.Name)
		expectBytes, err := os.ReadFile(filepath.Join(vdir, "expect.json"))
		if err != nil {
			return fmt.Errorf("%s: %w", mv.Name, err)
		}
		var expect Expect
		if err := json.Unmarshal(expectBytes, &expect); err != nil {
			return fmt.Errorf("%s expect: %w", mv.Name, err)
		}
		if expect.Outcome != mv.Outcome || expect.Reason != mv.Reason {
			return fmt.Errorf("%s: manifest expectation diverges from expect.json", mv.Name)
		}

		decisionRaw, err := os.ReadFile(filepath.Join(vdir, "decision.json"))
		if err != nil {
			return fmt.Errorf("%s: %w", mv.Name, err)
		}
		canonical, err := os.ReadFile(filepath.Join(vdir, "decision.canonical"))
		if err != nil {
			return fmt.Errorf("%s: %w", mv.Name, err)
		}
		declared, err := os.ReadFile(filepath.Join(vdir, "decision.digest"))
		if err != nil {
			return fmt.Errorf("%s: %w", mv.Name, err)
		}
		credRaw, err := os.ReadFile(filepath.Join(vdir, "credential.jws"))
		if err != nil {
			return fmt.Errorf("%s: %w", mv.Name, err)
		}
		observedRaw, err := os.ReadFile(filepath.Join(vdir, "observed.json"))
		if err != nil {
			return fmt.Errorf("%s: %w", mv.Name, err)
		}
		var observed Observed
		if err := json.Unmarshal(observedRaw, &observed); err != nil {
			return fmt.Errorf("%s observed: %w", mv.Name, err)
		}
		compact := strings.TrimSpace(string(credRaw))

		got, err := Evaluate(compact, decisionRaw, canonical, bytes.TrimSpace(declared), observed)
		if err != nil {
			return fmt.Errorf("%s evaluate: %w", mv.Name, err)
		}

		row := resultRow{name: mv.Name, expect: expect, got: got}
		if expect.Outcome == "accept" {
			if got != "" {
				row.note = "expected acceptance but got " + got
			} else {
				// Schema is a positive-only additional check.
				if err := validateSchema(decisionSchema, decisionRaw); err != nil {
					row.note = "decision schema: " + err.Error()
				} else if err := validateCredentialSchema(credentialSchema, compact); err != nil {
					row.note = "credential schema: " + err.Error()
				} else {
					row.ok = true
				}
			}
		} else {
			if got != expect.Reason {
				row.note = fmt.Sprintf("expected reason %s but got %q", expect.Reason, got)
			} else {
				row.ok = true
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	passed := 0
	for _, r := range rows {
		status := "ok"
		if !r.ok {
			status = "FAIL"
		} else {
			passed++
		}
		detail := ""
		if r.expect.Outcome == "reject" {
			detail = "reject " + r.expect.Reason
		} else {
			detail = "accept"
		}
		if r.note != "" {
			detail += " (" + r.note + ")"
		}
		fmt.Printf("%-4s %-32s %s\n", status, r.name, detail)
	}
	fmt.Printf("\n%d/%d vectors passed; bundle %s\n", passed, len(rows), manifest.BundleHash)
	if passed != len(rows) {
		return fmt.Errorf("%d vector(s) failed", len(rows)-passed)
	}
	return nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func compileSchema(path string) (*jsonschema.Schema, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	base := "file://" + filepath.ToSlash(path)
	if err := c.AddResource(base, doc); err != nil {
		return nil, err
	}
	return c.Compile(base)
}

func validateSchema(sch *jsonschema.Schema, raw []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return sch.Validate(inst)
}

func validateCredentialSchema(sch *jsonschema.Schema, compact string) error {
	pj, err := parseCompact(compact)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(pj.payloadRaw))
	if err != nil {
		return err
	}
	return sch.Validate(inst)
}

func verifyBundle(dir, declared string) error {
	sumsPath := filepath.Join(dir, "bundle", "SHA256SUMS")
	sumsRaw, err := os.ReadFile(sumsPath)
	if err != nil {
		return fmt.Errorf("bundle SHA256SUMS: %w", err)
	}
	bundleDeclared, err := os.ReadFile(filepath.Join(dir, "bundle", "BUNDLE.sha256"))
	if err != nil {
		return fmt.Errorf("bundle hash file: %w", err)
	}
	computed := "sha256:" + hex.EncodeToString(sha256Sum(sumsRaw))
	if strings.TrimSpace(string(bundleDeclared)) != computed {
		return fmt.Errorf("BUNDLE.sha256 does not match SHA256SUMS (want %s got %s)", declared, computed)
	}
	if declared != computed {
		return fmt.Errorf("manifest bundleHash %s does not match computed %s", declared, computed)
	}
	// Verify every listed file exists and matches.
	for _, line := range strings.Split(strings.TrimSpace(string(sumsRaw)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "  ", 2)
		if len(fields) != 2 {
			return fmt.Errorf("malformed SHA256SUMS line %q", line)
		}
		path := filepath.Join(dir, fields[1])
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("bundle references missing file %s", fields[1])
		}
		got := hex.EncodeToString(sha256Sum(b))
		if got != fields[0] {
			return fmt.Errorf("bundle mismatch for %s: want %s got %s", fields[1], fields[0], got)
		}
	}
	return nil
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
