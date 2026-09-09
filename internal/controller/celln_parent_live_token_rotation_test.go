package controller_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestParentTokenPrivateAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "unsafe\nsecret", strings.Repeat("x", 16385)} {
		if err := replaceParentToken(path, bad); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	if err := replaceParentToken(path, "replacement"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "replacement" {
		t.Fatal("replacement missing")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("replacement not private")
	}
	link := filepath.Join(filepath.Dir(path), "symlink")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := replaceParentToken(link, "not-published"); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := replaceParentToken(path, "not-published"); err == nil {
		t.Fatal("public file accepted")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 2 {
		t.Fatal("temporary token leaked")
	}
}

func TestParentTokenRenewal(t *testing.T) {
	for _, scenario := range []string{"rotate", "transient", "unavailable", "short-lived"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte("initial"), 0600); err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				started := time.Now()
				err := renewParentToken(ctx, path, func(context.Context) (string, time.Time, error) {
					calls++
					if scenario == "short-lived" {
						return "short", time.Now().Add(time.Minute), nil
					}
					if scenario == "unavailable" && calls > 1 || scenario == "transient" && calls == 2 {
						return "", time.Time{}, errors.New("provider-secret-must-not-escape")
					}
					if calls == 3 {
						cancel()
					}
					return "renewed", time.Now().Add(10 * time.Minute), nil
				})
				switch scenario {
				case "rotate":
					if err != nil || calls != 3 || time.Since(started) != 10*time.Minute {
						t.Fatalf("rotation cadence: calls=%d elapsed=%s err=%v", calls, time.Since(started), err)
					}
				case "transient":
					if err != nil || calls != 3 || time.Since(started) != 5*time.Minute+5*time.Second {
						t.Fatalf("transient retry: calls=%d elapsed=%s err=%v", calls, time.Since(started), err)
					}
				case "unavailable":
					if err == nil || strings.Contains(err.Error(), "provider-secret") || time.Since(started) != 9*time.Minute {
						t.Fatalf("credential safety deadline: elapsed=%s err=%v", time.Since(started), err)
					}
				case "short-lived":
					if err == nil || calls != 1 {
						t.Fatal("short token lifetime accepted")
					}
				}
			})
		})
	}
}
