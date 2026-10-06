package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/wyw14/cry-103/internal/feed"
	"github.com/wyw14/cry-103/internal/incident"
	"github.com/wyw14/cry-103/internal/landing"
	"github.com/wyw14/cry-103/internal/otdr"
	"github.com/wyw14/cry-103/internal/platform"
	"github.com/wyw14/cry-103/internal/repair"
	"github.com/wyw14/cry-103/internal/repeater"
	"github.com/wyw14/cry-103/internal/route"
	"github.com/wyw14/cry-103/internal/span"
	"github.com/wyw14/cry-103/internal/store"
	"github.com/wyw14/cry-103/internal/switchover"
	"github.com/wyw14/cry-103/internal/telemetry"
	"github.com/wyw14/cry-103/internal/topology"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	clock := fixedClock{now: time.Date(2026, 8, 24, 3, 0, 0, 0, time.UTC)}
	ids := platform.UUIDGenerator{}
	registry := topology.NewRegistry()
	if _, err := registry.Register("S3", topology.PathPrimary, clock.Now()); err != nil {
		t.Fatal(err)
	}
	target, err := span.New("S3", "S3", 126, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	path, err := route.New("R-S3", "S3", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	drainer := route.NewDrainer(0)
	activator := route.NewActivator(registry)
	receipts := landing.NewReceiptRegistry()
	permitService := repair.NewPermitService(feed.NewIsolationService(), receipts, ids, clock.Now)
	circuit, err := feed.NewCircuit("B", "east", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	permitService.RegisterCircuit(circuit)
	repeaterAsset, err := repeater.New("R07", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := incident.NewRepeaterLifecycle()
	projector := repeater.NewHealthProjector(lifecycle, 85)
	projector.Register(repeaterAsset)
	repository := telemetry.NewRepository()
	mutes := telemetry.NewMuteManager(clock.Now)
	acceptance := repair.NewAcceptanceService(switchover.NewRestorer(drainer, activator, registry, ids, clock.Now), clock.Now)
	acceptance.Register("S3", target, path)
	services := &Services{
		Clock: clock, IDs: ids, Journal: store.NewJournal(filepath.Join(t.TempDir(), "events.jsonl")),
		Spans: map[string]*span.Span{"S3": target}, Routes: map[string]*route.Route{"R-S3": path},
		Repeaters: map[string]*repeater.Repeater{"R07": repeaterAsset}, Topology: registry,
		OTDR:        otdr.NewSessionService(registry, landing.NewScanner(registry, 0), ids, clock.Now),
		Switchovers: switchover.NewSessionService(drainer, activator, ids, clock.Now, time.Second),
		Permits:     permitService, Receipts: receipts, Acceptance: acceptance,
		Telemetry: telemetry.NewIngestor(repository, clock.Now, projector), TelemetryRepository: repository,
		Admission: incident.NewAdmissionService(mutes), Classifier: incident.NewClassifier(),
		Desk: incident.NewDesk(incident.DefaultPolicy(), mutes), Correlator: otdr.NewCorrelator(),
		Incidents: make(map[string]*incident.Incident),
	}
	return NewServer(services)
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

func TestHealthAndScanHTTPFlow(t *testing.T) {
	server := testServer(t)
	health := httptest.NewRecorder()
	server.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", health.Code, health.Body.String())
	}

	body, _ := json.Marshal(ScanRequest{StationID: "east", LengthKM: 126, Chunks: 3})
	scan := httptest.NewRecorder()
	server.Handler().ServeHTTP(scan, httptest.NewRequest(http.MethodPost, "/api/spans/S3/otdr-scans", bytes.NewReader(body)))
	if scan.Code != http.StatusCreated {
		t.Fatalf("scan status=%d body=%s", scan.Code, scan.Body.String())
	}
	var trace otdr.Trace
	if err := json.Unmarshal(scan.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.SpanID != "S3" || len(trace.Chunks) != 3 {
		t.Fatalf("unexpected trace: %+v", trace)
	}

	status := httptest.NewRecorder()
	server.Handler().ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", status.Code, status.Body.String())
	}
}

func TestIncidentEscalationHTTPFlow(t *testing.T) {
	server := testServer(t)
	act := func(input IncidentRequest) incident.Snapshot {
		t.Helper()
		body, _ := json.Marshal(input)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/spans/S3/incidents/INC-1", bytes.NewReader(body)))
		if response.Code != http.StatusOK {
			t.Fatalf("action %s status=%d body=%s", input.Action, response.Code, response.Body.String())
		}
		var snapshot incident.Snapshot
		if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}

	snapshot := act(IncidentRequest{Action: "escalate", Severity: "critical"})
	if snapshot.Severity != incident.SeverityCritical || snapshot.Assignment.Role != incident.RoleEngineer {
		t.Fatalf("unexpected escalation: %+v", snapshot)
	}

	snapshot = act(IncidentRequest{Action: "claim", Operator: "alice"})
	if snapshot.Assignment.Operator != "alice" {
		t.Fatalf("operator=%s, want alice", snapshot.Assignment.Operator)
	}

	snapshot = act(IncidentRequest{Action: "reassign", Operator: "bob"})
	if snapshot.Assignment.Operator != "bob" {
		t.Fatalf("operator=%s, want bob", snapshot.Assignment.Operator)
	}

	snapshot = act(IncidentRequest{Action: "add-evidence", Kind: "otdr-trace", Detail: "break at 42.1km", Operator: "bob"})
	if len(snapshot.Evidence) != 1 || snapshot.Evidence[0].Kind != "otdr-trace" {
		t.Fatalf("unexpected evidence: %+v", snapshot.Evidence)
	}

	snapshot = act(IncidentRequest{Action: "recover", Operator: "bob", Detail: "spliced and restored"})
	if snapshot.State != incident.StateResolved {
		t.Fatalf("state=%s, want %s", snapshot.State, incident.StateResolved)
	}
	last := snapshot.Notices[len(snapshot.Notices)-1]
	if last.Kind != "recovery" || len(last.Recipients) != 2 {
		t.Fatalf("unexpected recovery notice: %+v", last)
	}

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/spans/S3/incidents/INC-1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("get incident status=%d body=%s", response.Code, response.Body.String())
	}
	var stored incident.Snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.State != incident.StateResolved || len(stored.Assignments) == 0 || len(stored.Evidence) != 1 {
		t.Fatalf("case progress not preserved: %+v", stored)
	}
}

func TestIncidentRejectsInvalidActions(t *testing.T) {
	server := testServer(t)
	post := func(input IncidentRequest) *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/spans/S3/incidents/INC-2", bytes.NewReader(body)))
		return response
	}
	if response := post(IncidentRequest{Action: "claim"}); response.Code != http.StatusBadRequest {
		t.Fatalf("claim without operator status=%d, want 400", response.Code)
	}
	if response := post(IncidentRequest{Action: "escalate", Severity: "bogus"}); response.Code != http.StatusBadRequest {
		t.Fatalf("escalate with bad severity status=%d, want 400", response.Code)
	}
	if response := post(IncidentRequest{Action: "detonate"}); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown action status=%d, want 400", response.Code)
	}
}
