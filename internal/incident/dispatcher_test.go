package incident

import (
	"testing"
	"time"
)

func testClock() (func() time.Time, func(time.Duration)) {
	base := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	current := base
	return func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) }
}

type recordingNotifier struct {
	events []CaseEvent
}

func (n *recordingNotifier) Notify(_ string, event CaseEvent) {
	n.events = append(n.events, event)
}

func TestDispatcherEscalatesBySeverityAndWait(t *testing.T) {
	now, advance := testClock()
	notifier := &recordingNotifier{}
	dispatcher := NewDispatcher(DefaultPolicy(), now, notifier)
	dispatcher.SetOnCall(RoleResponder, "alice")
	dispatcher.SetOnCall(RoleLead, "bob")

	inc := New("inc-1", "S3", now().Add(time.Minute), now())

	// sev1 ladders responder at t=0
	inc.update(func(s *Snapshot) { s.Severity = Sev1 })
	dispatcher.Evaluate(inc)
	got := inc.Snapshot()
	if got.Assignee != "alice" || got.AssigneeRole != RoleResponder || got.EscalationLevel != 1 {
		t.Fatalf("initial dispatch = %+v", got)
	}
	if len(notifier.events) != 1 || !notifier.events[0].Notified {
		t.Fatalf("dispatch should notify, got %+v", notifier.events)
	}

	// Before 5m nothing changes.
	advance(4 * time.Minute)
	dispatcher.Evaluate(inc)
	if inc.Snapshot().EscalationLevel != 1 {
		t.Fatalf("should not escalate before wait threshold")
	}

	// At 5m the lead is paged.
	advance(time.Minute)
	dispatcher.Evaluate(inc)
	got = inc.Snapshot()
	if got.Assignee != "bob" || got.AssigneeRole != RoleLead || got.EscalationLevel != 2 {
		t.Fatalf("lead escalation = %+v", got)
	}
	if got.Events[len(got.Events)-1].Type != EventEscalated {
		t.Fatalf("expected escalated event, got %+v", got.Events[len(got.Events)-1])
	}

	// At 15m commander (no roster entry -> default placeholder).
	advance(10 * time.Minute)
	dispatcher.Evaluate(inc)
	got = inc.Snapshot()
	if got.AssigneeRole != RoleCommander || got.Assignee != "on-call-commander" {
		t.Fatalf("commander escalation = %+v", got)
	}
}

func TestSev3WaitsForFirstResponder(t *testing.T) {
	now, advance := testClock()
	dispatcher := NewDispatcher(DefaultPolicy(), now, nil)
	inc := New("inc-2", "S3", now().Add(time.Minute), now()) // default sev3

	dispatcher.Evaluate(inc)
	if inc.Snapshot().AssigneeRole != "" {
		t.Fatalf("sev3 should not page responder before 5m")
	}
	advance(5 * time.Minute)
	dispatcher.Evaluate(inc)
	got := inc.Snapshot()
	if got.AssigneeRole != RoleResponder || got.EscalationLevel != 1 {
		t.Fatalf("sev3 responder dispatch after wait = %+v", got)
	}
}

func TestMuteLeaseRetainsInternalProgress(t *testing.T) {
	now, advance := testClock()
	notifier := &recordingNotifier{}
	dispatcher := NewDispatcher(DefaultPolicy(), now, notifier)
	inc := New("inc-3", "S3", now().Add(time.Minute), now())
	inc.update(func(s *Snapshot) { s.Severity = Sev1 })

	dispatcher.MarkMuted(inc, true)
	dispatcher.Evaluate(inc) // due immediately for sev1
	advance(6 * time.Minute)
	dispatcher.Evaluate(inc) // lead escalation also due

	got := inc.Snapshot()
	if !got.Muted {
		t.Fatalf("case should be muted")
	}
	if got.AssigneeRole != RoleLead {
		t.Fatalf("escalation should proceed while muted, got %+v", got)
	}
	if len(notifier.events) != 0 {
		t.Fatalf("no outward notification during mute, got %+v", notifier.events)
	}
	for _, event := range got.Events {
		if event.Type == EventSuppressed || event.Type == EventDispatched || event.Type == EventEscalated {
			if event.Notified {
				t.Fatalf("event %s during mute must be retained with notified=false", event.Type)
			}
		}
	}
	if len(got.Events) < 4 { // opened + suppressed + dispatched + escalated
		t.Fatalf("internal progress lost during mute: %+v", got.Events)
	}

	// Lifting mute: subsequent events notify again; history is intact.
	dispatcher.MarkMuted(inc, false)
	dispatcher.AddSupplementaryEvidence(inc, "bob", "wet test trace attached")
	got = inc.Snapshot()
	last := got.Events[len(got.Events)-1]
	if !last.Notified || last.Type != EventSupplementaryEvidence {
		t.Fatalf("post-mute evidence should notify: %+v", last)
	}
}

func TestTakeOverReassignEvidenceRestoreResolve(t *testing.T) {
	now, _ := testClock()
	notifier := &recordingNotifier{}
	dispatcher := NewDispatcher(DefaultPolicy(), now, notifier)
	inc := New("inc-4", "S3", now().Add(time.Minute), now())

	// Early takeover performs initial dispatch.
	snap, err := dispatcher.TakeOver(inc, "carol")
	if err != nil || snap.Assignee != "carol" || snap.AssigneeRole != RoleResponder {
		t.Fatalf("early takeover = %+v err=%v", snap, err)
	}
	// A second responder takes over, level unchanged.
	snap, err = dispatcher.TakeOver(inc, "dave")
	if err != nil || snap.Assignee != "dave" || snap.AssigneeRole != RoleResponder {
		t.Fatalf("takeover = %+v err=%v", snap, err)
	}
	if snap.Events[len(snap.Events)-1].Type != EventTakenOver {
		t.Fatalf("expected taken-over event")
	}
	if _, err := dispatcher.TakeOver(inc, ""); err != ErrActorRequired {
		t.Fatalf("expected actor error, got %v", err)
	}

	// Reassign up to commander (sev1 ladder has a commander step).
	inc.update(func(s *Snapshot) { s.Severity = Sev1 })
	snap, err = dispatcher.Reassign(inc, "erin", RoleCommander)
	if err != nil || snap.AssigneeRole != RoleCommander || snap.EscalationLevel != 3 {
		t.Fatalf("reassign = %+v err=%v", snap, err)
	}
	if _, err := dispatcher.Reassign(inc, "x", Role("wizard")); err != ErrUnknownRole {
		t.Fatalf("expected unknown role error, got %v", err)
	}

	// Supplementary evidence keeps case state.
	snap = dispatcher.AddSupplementaryEvidence(inc, "erin", "vessel drag confirmed at 42km")
	if snap.State != StateCollecting {
		t.Fatalf("evidence must not change diagnostic state: %+v", snap)
	}

	// Restore notification then resolve closes the case and blocks moves.
	snap = dispatcher.Restore(inc, "erin", "traffic back on primary route")
	if snap.State != StateRestored {
		t.Fatalf("restore = %+v", snap)
	}
	snap = dispatcher.Resolve(inc, "erin")
	if snap.State != StateResolved {
		t.Fatalf("resolve = %+v", snap)
	}
	before := len(snap.Events)
	dispatcher.Restore(inc, "erin", "again")
	dispatcher.AddSupplementaryEvidence(inc, "erin", "late note")
	if len(inc.Snapshot().Events) != before {
		t.Fatalf("closed case must not accept new events")
	}
}

func TestClassifierPromotesCableCutToSev2(t *testing.T) {
	now, _ := testClock()
	classifier := NewClassifier()
	inc := New("inc-5", "S3", now().Add(time.Minute), now())

	classifier.LocalComplete(inc, true, now())
	if inc.Snapshot().State != StateWaitingPeer {
		t.Fatalf("expected waiting-peer")
	}
	classifier.PeerEvidence(inc, now())
	got := inc.Snapshot()
	if got.State != StateCableCut || got.Severity != Sev2 {
		t.Fatalf("paired evidence should classify cable cut at sev2: %+v", got)
	}
	classified := got.Events[len(got.Events)-1]
	if classified.Type != EventClassified || classified.Note != "classified as cable-cut" {
		t.Fatalf("unexpected classified event: %+v", classified)
	}
}

func TestExpireRecordsInternalEvent(t *testing.T) {
	now, _ := testClock()
	classifier := NewClassifier()
	deadline := now().Add(time.Minute)
	inc := New("inc-6", "S3", deadline, now())
	classifier.LocalComplete(inc, true, now())
	classifier.Expire(inc, deadline.Add(time.Second))
	got := inc.Snapshot()
	if got.State != StateStationFault {
		t.Fatalf("expected station fault after expiry: %+v", got)
	}
	found := false
	for _, event := range got.Events {
		if event.Type == EventExpired {
			found = true
		}
	}
	if !found {
		t.Fatalf("expiry event missing: %+v", got.Events)
	}
}
