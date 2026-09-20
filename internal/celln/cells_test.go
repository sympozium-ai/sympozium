package celln

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const cellsFixture = `{"apiVersion":"celln.cells/v1","nodes":[
 {"index":0,"report":{"apiVersion":"celln.cells/v1","node":"framework",
  "cells":[{"id":"2e1e3481f3b7","description":"blake3:abc…","status":"running","backend":"kvm","started_ms":1,"finished_ms":null,"duration_ms":null,"error":null,"tools":["/worker"]}],
  "parents":[{"incarnation":"blake3:parent","status":"TurnActive","statusIsLiveOwnerObservation":true,"updated_ms":null,
   "turns":[{"turnId":"initial","stage":"reserved","child":"blake3:abcdef","timeout_ms":60000,"reserved_ms":122},
            {"turnId":"t0","stage":"parent-committed","child":"blake3:ffff","timeout_ms":60000,"reserved_ms":100,"succeeded":true}],"turns_total":2}]}},
 {"index":1,"reason":"unreachable_unauthorized_or_incompatible"}]}`

func cellsClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{BaseURL: server.URL, TokenFile: writeToken(t, goodToken)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestListCellsDecodesReportsAndReasons(t *testing.T) {
	c := cellsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/cells" || r.URL.RawQuery != "all=true&limit=200" || r.Header.Get("Authorization") != "Bearer "+goodToken {
			t.Errorf("wrong cells request: %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(cellsFixture))
	})
	listing, err := c.ListCells(context.Background(), true, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Nodes) != 2 || listing.Nodes[0].Report == nil || listing.Nodes[1].Reason == "" || listing.Nodes[1].Index != 1 {
		t.Fatalf("listing: %+v", listing)
	}
	report := listing.Nodes[0].Report
	if report.Node != "framework" || len(report.Cells) != 1 || report.Cells[0].FinishedMs != nil || report.Cells[0].Tools[0] != "/worker" {
		t.Fatalf("report: %+v", report)
	}
	parent := report.Parents[0]
	if parent.Status != "TurnActive" || !parent.Live || parent.UpdatedMs != nil || parent.TurnsTotal != 2 ||
		parent.Turns[0].Succeeded != nil || parent.Turns[1].Succeeded == nil || !*parent.Turns[1].Succeeded {
		t.Fatalf("parent: %+v", parent)
	}
}

func TestListCellsRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		status      int
		unsupported bool
	}{
		{"old-router", "not found", 404, true},
		{"busy", `{"error":"listing in flight"}`, 503, false},
		{"unauthorized", "", 401, false},
		{"malformed", `{`, 200, false},
		{"trailing", cellsFixture + `{}`, 200, false},
		{"unknown-field", strings.Replace(cellsFixture, `"index":1,`, `"index":1,"token":"x",`, 1), 200, false},
		{"wrong-version", strings.Replace(cellsFixture, "celln.cells/v1", "celln.cells/v2", 1), 200, false},
		{"wrong-report-version", strings.Replace(cellsFixture, `"report":{"apiVersion":"celln.cells/v1"`, `"report":{"apiVersion":"other"`, 1), 200, false},
		{"neither-report-nor-reason", `{"apiVersion":"celln.cells/v1","nodes":[{"index":0}]}`, 200, false},
		{"too-many-backends", `{"apiVersion":"celln.cells/v1","nodes":[` + strings.TrimSuffix(strings.Repeat(`{"index":0,"reason":"x"},`, 33), ",") + `]}`, 200, false},
		{"oversized", `{"apiVersion":"celln.cells/v1","nodes":[{"index":0,"reason":"` + strings.Repeat("x", maxCellsResponseBytes) + `"}]}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cellsClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := c.ListCells(context.Background(), false, 1)
			if err == nil || errors.Is(err, ErrCellsUnsupported) != tc.unsupported {
				t.Fatalf("err=%v", err)
			}
			if strings.Contains(err.Error(), goodToken) {
				t.Fatal("credential leaked into error")
			}
		})
	}
}

func TestListCellsValidatesLimitAndNeverFollowsRedirects(t *testing.T) {
	var leaked, called atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Store(true) }))
	defer target.Close()
	c := cellsClient(t, func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		http.Redirect(w, r, target.URL, http.StatusFound)
	})
	for _, limit := range []int{0, -1, MaxCellsLimit + 1} {
		if _, err := c.ListCells(context.Background(), true, limit); err == nil || called.Load() {
			t.Fatalf("limit %d accepted", limit)
		}
	}
	if _, err := c.ListCells(context.Background(), true, MaxCellsLimit); err == nil || leaked.Load() {
		t.Fatal("redirect followed")
	}
}
