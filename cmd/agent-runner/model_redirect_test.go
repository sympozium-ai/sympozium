package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The model client never follows a redirect: Go would re-send x-api-key to
// the redirect target.
func TestModelClientDoesNotFollowRedirects(t *testing.T) {
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("x-api-key") != ""
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/messages", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	req, _ := http.NewRequest(http.MethodPost, origin.URL+"/v1/messages", nil)
	req.Header.Set("x-api-key", "sk-ant-DUMMY")
	resp, err := tracingHTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if leaked || resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect followed: leaked=%v status=%d", leaked, resp.StatusCode)
	}
}
