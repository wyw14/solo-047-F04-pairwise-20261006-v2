package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func postIncidentAction(t *testing.T, server *Server, body IncidentRequest) incidentView {
	t.Helper()
	raw, _ := json.Marshal(body)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/spans/S3/incidents/inc-http-1", bytes.NewReader(raw)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("incident action %s status=%d body=%s", body.Action, recorder.Code, recorder.Body.String())
	}
	var view incidentView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

type incidentView struct {
	ID              string      `json:"id"`
	State           string      `json:"state"`
	Severity        string      `json:"severity"`
	Assignee        string      `json:"assignee"`
	AssigneeRole    string      `json:"assignee_role"`
	EscalationLevel int         `json:"escalation_level"`
	Muted           bool        `json:"muted"`
	Events          []caseEvent `json:"events"`
}

type caseEvent struct {
	Type     string `json:"type"`
	Notified bool   `json:"notified"`
	Note     string `json:"note"`
}

func TestIncidentEscalationHTTPFlow(t *testing.T) {
	clock := &mutableClock{now: time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)}
	server := testServerWithClock(t, clock)

	// Paired evidence classifies a cable cut and promotes severity to sev2;
	// sev2 dispatches a responder immediately.
	postIncidentAction(t, server, IncidentRequest{Action: "local-complete", RequiresPeer: true})
	snap := postIncidentAction(t, server, IncidentRequest{
		Action: "peer-evidence", StationID: "west", TraceID: "tr-9", DistanceKM: 42,
	})
	if snap.State != "cable-cut" || snap.Severity != "sev2" {
		t.Fatalf("expected classified cable-cut sev2, got %+v", snap)
	}
	if snap.AssigneeRole != "responder" || snap.EscalationLevel != 1 {
		t.Fatalf("expected immediate responder dispatch for sev2, got %+v", snap)
	}

	// On-call responder takes over before auto-escalation.
	snap = postIncidentAction(t, server, IncidentRequest{Action: "takeover", Actor: "alice"})
	if snap.Assignee != "alice" {
		t.Fatalf("takeover failed: %+v", snap)
	}

	// After the sev2 lead wait (15m), the case escalates to the lead.
	clock.advance(16 * time.Minute)
	snap = postIncidentAction(t, server, IncidentRequest{Action: "escalate"})
	if snap.AssigneeRole != "lead" || snap.EscalationLevel != 2 {
		t.Fatalf("expected lead escalation after wait, got %+v", snap)
	}

	// Explicit reassign jumps to the commander role.
	snap = postIncidentAction(t, server, IncidentRequest{Action: "reassign", Actor: "erin", Role: "commander"})
	if snap.AssigneeRole != "commander" || snap.EscalationLevel != 3 {
		t.Fatalf("reassign failed: %+v", snap)
	}

	// Supplementary evidence is retained without changing state.
	snap = postIncidentAction(t, server, IncidentRequest{
		Action: "supplement-evidence", Actor: "erin", Note: "repair vessel on site",
	})
	if snap.State != "cable-cut" {
		t.Fatalf("evidence must not change state: %+v", snap)
	}

	// Recovery notice then closure.
	snap = postIncidentAction(t, server, IncidentRequest{Action: "restore", Actor: "erin", Note: "fiber spliced"})
	if snap.State != "restored" {
		t.Fatalf("restore failed: %+v", snap)
	}
	snap = postIncidentAction(t, server, IncidentRequest{Action: "resolve", Actor: "erin"})
	if snap.State != "resolved" {
		t.Fatalf("resolve failed: %+v", snap)
	}

	// GET returns the full internal case history.
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/spans/S3/incidents/inc-http-1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("get incident status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var detail struct {
		Incident incidentView `json:"incident"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, event := range detail.Incident.Events {
		types[event.Type] = true
		// The initial "opened" entry is internal bookkeeping; every action
		// event outside a mute lease must carry a notification.
		if event.Type != "opened" && !event.Notified {
			t.Fatalf("no mute active, event should be notified: %+v", event)
		}
	}
	for _, expected := range []string{"opened", "dispatched", "taken-over", "escalated",
		"reassigned", "supplementary-evidence", "restored", "resolved"} {
		if !types[expected] {
			t.Fatalf("case history missing %s: %+v", expected, detail.Incident.Events)
		}
	}

	// Unknown case -> 404.
	missing := httptest.NewRecorder()
	server.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet,
		"/api/spans/S3/incidents/nope", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", missing.Code)
	}
}

func TestReassignRequiresValidRoleAndActor(t *testing.T) {
	server := testServer(t)
	for _, body := range []IncidentRequest{
		{Action: "reassign", Actor: "", Role: "lead"},
		{Action: "reassign", Actor: "alice", Role: "wizard"},
	} {
		raw, _ := json.Marshal(body)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
			"/api/spans/S3/incidents/inc-bad", bytes.NewReader(raw)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %+v expected 400, got %d: %s", body, recorder.Code, recorder.Body.String())
		}
	}
}
