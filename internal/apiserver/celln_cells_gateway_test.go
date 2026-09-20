package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	cellsTestChild  = "blake3:c8637381b4957825e2794a01e3fee6c6cb23c6be229dc6f1ff8322069b0ff61c"
	cellsTestParent = "blake3:parent"
)

var cellsTestNow = time.UnixMilli(1_700_000_000_000)

// gatewayNodeReport is one backend's entry as the router lists it.
func gatewayNodeReport(index int, node string) string {
	return fmt.Sprintf(`{"index":%d,"report":{"apiVersion":"celln.cells/v1","node":%q,
	 "cells":[{"id":"2e1e3481f3b7","description":"blake3:c8637381b4957825e2794a0…","status":"running","backend":"kvm","started_ms":1,"finished_ms":null,"duration_ms":null,"error":null,"tools":["/worker"]},
	          {"id":"0000000000aa","description":"blake3:ffff","status":"dissolved","backend":"kvm","started_ms":1,"finished_ms":3,"duration_ms":2,"error":null,"tools":[]}],
	 "parents":[{"incarnation":%q,"status":"TurnActive","statusIsLiveOwnerObservation":true,"updated_ms":123,
	   "turns":[{"turnId":"first","stage":"parent-committed","child":"blake3:0000","timeout_ms":60000,"reserved_ms":100,"succeeded":true},
	            {"turnId":"initial","stage":"reserved","child":%q,"timeout_ms":60000,"reserved_ms":122}],"turns_total":2},
	  {"incarnation":"blake3:lost","status":"ContextLost","statusIsLiveOwnerObservation":false,"updated_ms":null,"turns":[],"turns_total":0}]}}`,
		index, node, cellsTestParent, cellsTestChild)
}

func gatewayListing(entries ...string) string {
	return `{"apiVersion":"celln.cells/v1","nodes":[` + strings.Join(entries, ",") + `]}`
}

// nodeReportConfigMap is the fleet cells ConfigMap with one fresh report per node.
func nodeReportConfigMap(nodes ...string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "celln-system", Name: cellninstall.FleetCellsConfigMap}, Data: map[string]string{}}
	for _, node := range nodes {
		raw, _ := json.Marshal(map[string]any{
			"apiVersion": "sympozium.ai/celln-node-cells-v1", "node": node, "reportedMs": cellsTestNow.UnixMilli() - 5000,
			"cells":   []map[string]any{{"id": "2e1e3481f3b7", "description": "blake3:c8637381b4957825e2794a0…", "status": "running", "backend": "kvm", "started_ms": 1, "tools": []string{"/worker"}}},
			"parents": []map[string]any{{"incarnation": cellsTestParent, "updatedMs": 1, "status": "Forged", "turns": []map[string]any{{"turnId": "initial", "stage": "committed", "child": cellsTestChild}}}},
		})
		cm.Data[node] = string(raw)
	}
	return cm
}

type cellsRouter struct {
	calls   atomic.Int32
	mu      sync.Mutex
	handler http.HandlerFunc
}

func (r *cellsRouter) set(h http.HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handler = h
}

// newCellsTest stands a router up, points the API server's Celln environment
// at it, and returns a Server whose cells clock the test owns.
func newCellsTest(t *testing.T, handler http.HandlerFunc, objects ...client.Object) (*Server, *cellsRouter, *time.Time) {
	t.Helper()
	router := &cellsRouter{handler: handler}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/cells" || r.URL.RawQuery != "all=true&limit=200" || r.Header.Get("Authorization") != "Bearer "+discoveryTestToken {
			t.Errorf("wrong cells request: %s %s", r.Method, r.URL)
		}
		router.mu.Lock()
		h := router.handler
		router.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(server.Close)
	configureCapabilityTest(t, server.URL)

	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	run := &api.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hermes-abc", Namespace: "default"},
		Spec:       api.AgentRunSpec{AgentRef: "hermes"},
		Status:     api.AgentRunStatus{Phase: api.AgentRunPhaseRunning, CellnParent: &api.CellnParentStatus{Binding: api.CellnParentBinding{Incarnation: cellsTestParent}}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objects, run)...).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	now := cellsTestNow
	srv.cellnCells.now = func() time.Time { return now }
	return srv, router, &now
}

func getCells(t *testing.T, srv *Server, ctx context.Context) []CellnNodeCells {
	t.Helper()
	res := httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/cells", nil).WithContext(ctx))
	if res.Code != http.StatusOK {
		t.Fatalf("cells answered %d: %s", res.Code, res.Body.String())
	}
	var nodes []CellnNodeCells
	if err := json.Unmarshal(res.Body.Bytes(), &nodes); err != nil || nodes == nil {
		t.Fatalf("cells body %q: %v", res.Body.String(), err)
	}
	return nodes
}

func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestCellsPreferTheGatewayAndJoinItToRuns(t *testing.T) {
	// The ConfigMap is present too: the gateway still wins.
	srv, router, _ := newCellsTest(t, respond(200, gatewayListing(gatewayNodeReport(0, "framework"))), nodeReportConfigMap("framework"))
	nodes := getCells(t, srv, context.Background())
	if len(nodes) != 1 || router.calls.Load() != 1 {
		t.Fatalf("nodes=%+v calls=%d", nodes, router.calls.Load())
	}
	fw := nodes[0]
	if fw.Node != "framework" || fw.Source != cellnSourceGateway || fw.Stale || fw.Error != "" || fw.ReportedMs != cellsTestNow.UnixMilli() || len(fw.Cells) != 2 || len(fw.Parents) != 2 {
		t.Fatalf("gateway node: %+v", fw)
	}
	cell := fw.Cells[0]
	if cell.Run == nil || cell.Run.Name != "hermes-abc" || cell.Run.Agent != "hermes" || !cell.Run.Live || cell.Parent != cellsTestParent || cell.Turn != "initial" {
		t.Fatalf("turn cell not attributed to its run: %+v %+v", cell, cell.Run)
	}
	if fw.Cells[1].Run != nil || fw.Cells[1].Tools == nil {
		t.Fatalf("unrelated cell: %+v", fw.Cells[1])
	}
	parent := fw.Parents[0]
	if parent.Run == nil || parent.Run.Phase != "Running" || parent.Status != "TurnActive" || parent.StatusLive == nil || !*parent.StatusLive || parent.UpdatedMs != 123 {
		t.Fatalf("parent: %+v", parent)
	}
	if parent.Turns[0].Stage != cellnStageCommitted || parent.Turns[0].Succeeded == nil || parent.Turns[0].TimeoutMs != 60000 ||
		parent.Turns[1].Stage != cellnStageReserved || parent.Turns[1].Succeeded != nil {
		t.Fatalf("stages not normalised: %+v", parent.Turns)
	}
	lost := fw.Parents[1]
	if lost.Run != nil || lost.Status != "ContextLost" || lost.StatusLive == nil || *lost.StatusLive || lost.UpdatedMs != 0 || lost.Turns == nil {
		t.Fatalf("lost parent: %+v", lost)
	}
}

func TestCellsFallBackToNodeReportsAndRememberAnOldRouter(t *testing.T) {
	srv, router, now := newCellsTest(t, respond(404, "404 page not found"), nodeReportConfigMap("framework"))
	for i := 0; i < 3; i++ {
		nodes := getCells(t, srv, context.Background())
		if len(nodes) != 1 || nodes[0].Source != cellnSourceNodeReport || nodes[0].Stale || nodes[0].Cells[0].Run == nil {
			t.Fatalf("fallback: %+v", nodes)
		}
		// A node's report cannot claim the owner's status.
		if p := nodes[0].Parents[0]; p.Status != "" || p.StatusLive != nil || p.Turns[0].Stage != cellnStageCommitted {
			t.Fatalf("node-report parent: %+v", p)
		}
		*now = now.Add(20 * time.Second)
	}
	if calls := router.calls.Load(); calls != 1 {
		t.Fatalf("old router probed %d times within the TTL", calls)
	}
	// After the TTL the router is asked again, and an upgraded one is used.
	router.set(respond(200, gatewayListing(gatewayNodeReport(0, "framework"))))
	*now = cellsTestNow.Add(cellnGatewayUnsupportedTTL + time.Second)
	if nodes := getCells(t, srv, context.Background()); router.calls.Load() != 2 || nodes[0].Source != cellnSourceGateway {
		t.Fatalf("router not asked again after the TTL: calls=%d %+v", router.calls.Load(), nodes)
	}
}

func TestCellsFallBackWithoutRememberingATransientFailure(t *testing.T) {
	release := make(chan struct{})
	hang := func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}
	oversized := gatewayListing(`{"index":0,"reason":"` + strings.Repeat("x", 16*1024*1024) + `"}`)
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration
	}{
		{"busy", respond(503, `{"error":"listing in flight"}`), time.Minute},
		{"server-error", respond(500, "boom"), time.Minute},
		{"malformed", respond(200, `{"apiVersion":"celln.cells/v1","nodes":[{"index":0}]}`), time.Minute},
		{"oversized", respond(200, oversized), time.Minute},
		{"timeout", hang, 200 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, router, _ := newCellsTest(t, tc.handler, nodeReportConfigMap("framework"))
			for want := int32(1); want <= 2; want++ {
				ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
				nodes := getCells(t, srv, ctx)
				cancel()
				if len(nodes) != 1 || nodes[0].Source != cellnSourceNodeReport || nodes[0].Cells[0].Run == nil {
					t.Fatalf("fallback: %+v", nodes)
				}
				if router.calls.Load() != want {
					t.Fatalf("a transient failure must not stop the next probe: calls=%d want %d", router.calls.Load(), want)
				}
			}
		})
	}
	close(release)
}

func TestCellsFallBackWhenTheRouterIsUnreachable(t *testing.T) {
	srv, _, _ := newCellsTest(t, respond(200, gatewayListing()), nodeReportConfigMap("framework"))
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	t.Setenv("CELLN_ROUTER_URL", dead.URL)
	if nodes := getCells(t, srv, context.Background()); len(nodes) != 1 || nodes[0].Source != cellnSourceNodeReport {
		t.Fatalf("fallback: %+v", nodes)
	}
}

func TestCellsFillAFailingBackendFromItsNodeReport(t *testing.T) {
	listing := gatewayListing(gatewayNodeReport(0, "framework"), `{"index":1,"reason":"unreachable_unauthorized_or_incompatible"}`)

	// The ConfigMap reports a node the gateway did not list: it stands in.
	srv, _, _ := newCellsTest(t, respond(200, listing), nodeReportConfigMap("framework", "thinkpad"))
	nodes := getCells(t, srv, context.Background())
	if len(nodes) != 2 || nodes[0].Node != "framework" || nodes[0].Source != cellnSourceGateway ||
		nodes[1].Node != "thinkpad" || nodes[1].Source != cellnSourceNodeReport || nodes[1].Error != "" || nodes[1].Cells[0].Run == nil {
		t.Fatalf("mixed sources: %+v", nodes)
	}

	// Only the listed node has a report (or there is none): the backend keeps its error.
	for _, objects := range [][]client.Object{{nodeReportConfigMap("framework")}, nil} {
		srv, _, _ = newCellsTest(t, respond(200, listing), objects...)
		nodes = getCells(t, srv, context.Background())
		if len(nodes) != 2 || nodes[0].Node != "framework" || nodes[1].Node != "node-1" || nodes[1].Source != cellnSourceGateway ||
			!strings.Contains(nodes[1].Error, "unreachable unauthorized or incompatible") || nodes[1].Cells == nil || nodes[1].Parents == nil {
			t.Fatalf("unfilled backend: %+v", nodes)
		}
	}
}

func TestCellsAreEmptyWithoutGatewayOrNodeReports(t *testing.T) {
	srv, router, _ := newCellsTest(t, respond(404, ""))
	if nodes := getCells(t, srv, context.Background()); len(nodes) != 0 {
		t.Fatalf("nodes: %+v", nodes)
	}
	t.Setenv("CELLN_ENABLED", "false")
	before := router.calls.Load()
	if nodes := getCells(t, srv, context.Background()); len(nodes) != 0 || router.calls.Load() != before {
		t.Fatalf("disabled Celln contacted the router or listed nodes: %+v", nodes)
	}
}

func TestCellsGatewayNodeNamesAreValidated(t *testing.T) {
	listing := gatewayListing(gatewayNodeReport(0, "framework"), gatewayNodeReport(1, "framework"), gatewayNodeReport(2, "<script>"))
	srv, _, _ := newCellsTest(t, respond(200, listing))
	nodes := getCells(t, srv, context.Background())
	if len(nodes) != 3 || nodes[0].Node != "framework" || nodes[1].Node != "node-1" || nodes[2].Node != "node-2" {
		t.Fatalf("node names: %+v", nodes)
	}
}
