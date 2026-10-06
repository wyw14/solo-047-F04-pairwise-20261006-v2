package incident

import (
	"testing"
	"time"

	"github.com/wyw14/cry-103/internal/telemetry"
)

var base = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

type stubMutes map[string]bool

func (s stubMutes) Muted(spanID string) bool { return s[spanID] }

func TestPolicyDispatchBySeverityAndWait(t *testing.T) {
	policy := DefaultPolicy()
	cases := []struct {
		severity Severity
		waited   time.Duration
		want     Role
	}{
		{SeverityCritical, 0, RoleEngineer},
		{SeverityCritical, 3 * time.Minute, RoleCommander},
		{SeverityMajor, 0, RoleTechnician},
		{SeverityMajor, 6 * time.Minute, RoleEngineer},
		{SeverityMajor, 20 * time.Minute, RoleCommander},
		{SeverityMinor, 0, RoleObserver},
		{SeverityMinor, 11 * time.Minute, RoleTechnician},
		{SeverityMinor, 31 * time.Minute, RoleEngineer},
	}
	for _, tc := range cases {
		if got := policy.Dispatch(tc.severity, tc.waited); got != tc.want {
			t.Errorf("Dispatch(%s, %s) = %s, want %s", tc.severity, tc.waited, got, tc.want)
		}
	}
}

func TestDeskEscalationClaimReassignRecover(t *testing.T) {
	desk := NewDesk(DefaultPolicy(), nil)
	target := New("INC-1", "S3", base.Add(time.Minute), base)

	snapshot := desk.Escalate(target, SeverityCritical, base)
	if snapshot.Severity != SeverityCritical {
		t.Fatalf("severity=%s, want %s", snapshot.Severity, SeverityCritical)
	}
	if snapshot.Assignment.Role != RoleEngineer {
		t.Fatalf("role=%s, want %s", snapshot.Assignment.Role, RoleEngineer)
	}
	if len(snapshot.Notices) != 1 || snapshot.Notices[0].Kind != "escalation" {
		t.Fatalf("expected escalation notice, got %+v", snapshot.Notices)
	}

	snapshot = desk.Claim(target, "alice", base.Add(time.Minute))
	if snapshot.Assignment.Operator != "alice" {
		t.Fatalf("operator=%s, want alice", snapshot.Assignment.Operator)
	}

	// Two minutes in, a critical fault promotes to the incident commander.
	snapshot = desk.Reassign(target, "bob", base.Add(2*time.Minute))
	if snapshot.Assignment.Role != RoleCommander {
		t.Fatalf("role=%s, want %s", snapshot.Assignment.Role, RoleCommander)
	}
	if snapshot.Assignment.Operator != "bob" {
		t.Fatalf("operator=%s, want bob", snapshot.Assignment.Operator)
	}

	snapshot = desk.AddEvidence(target, "otdr-trace", "break at 42.1km", "bob", base.Add(3*time.Minute))
	if len(snapshot.Evidence) != 1 || snapshot.Evidence[0].Detail != "break at 42.1km" {
		t.Fatalf("unexpected evidence: %+v", snapshot.Evidence)
	}
	if snapshot.Evidence[0].Muted {
		t.Fatal("evidence should not be muted without a lease")
	}

	snapshot = desk.Recover(target, "bob", "spliced and restored", base.Add(4*time.Minute))
	if snapshot.State != StateResolved {
		t.Fatalf("state=%s, want %s", snapshot.State, StateResolved)
	}
	last := snapshot.Notices[len(snapshot.Notices)-1]
	if last.Kind != "recovery" || last.Message != "spliced and restored" {
		t.Fatalf("unexpected recovery notice: %+v", last)
	}
	if len(last.Recipients) != 2 || last.Recipients[0] != "alice" || last.Recipients[1] != "bob" {
		t.Fatalf("recipients=%v, want [alice bob]", last.Recipients)
	}
}

func TestDeskKeepsProgressFlaggedDuringMute(t *testing.T) {
	desk := NewDesk(DefaultPolicy(), stubMutes{"S3": true})
	target := New("INC-2", "S3", base.Add(time.Minute), base)

	snapshot := desk.Escalate(target, SeverityMajor, base)
	if len(snapshot.Notices) != 1 || !snapshot.Notices[0].Suppressed {
		t.Fatalf("escalation notice should be suppressed under mute: %+v", snapshot.Notices)
	}

	snapshot = desk.AddEvidence(target, "note", "technician on site", "alice", base)
	if len(snapshot.Evidence) != 1 || !snapshot.Evidence[0].Muted {
		t.Fatalf("evidence should be flagged muted: %+v", snapshot.Evidence)
	}

	snapshot = desk.Recover(target, "alice", "", base)
	if snapshot.State != StateResolved {
		t.Fatalf("state=%s, want %s", snapshot.State, StateResolved)
	}
	last := snapshot.Notices[len(snapshot.Notices)-1]
	if last.Kind != "recovery" || !last.Suppressed {
		t.Fatalf("recovery notice should be recorded but suppressed: %+v", last)
	}
}

func TestAdmissionKeepsSuppressedAlarmsDuringMute(t *testing.T) {
	now := base
	mutes := telemetry.NewMuteManager(func() time.Time { return now })
	service := NewAdmissionService(mutes)
	mutes.Acquire("lease-1", "S3", time.Minute)

	if service.Accept(Alarm{ID: "a1", SpanID: "S3", Kind: "los", ObservedAt: now}) {
		t.Fatal("alarm should not be admitted while muted")
	}
	if suppressed := service.Suppressed("S3"); len(suppressed) != 1 || suppressed[0].ID != "a1" {
		t.Fatalf("suppressed=%+v, want alarm a1 kept internally", suppressed)
	}
	if accepted := service.Accepted("S3"); len(accepted) != 0 {
		t.Fatalf("accepted=%+v, want none while muted", accepted)
	}

	now = base.Add(2 * time.Minute)
	if !service.Accept(Alarm{ID: "a2", SpanID: "S3", Kind: "los", ObservedAt: now}) {
		t.Fatal("alarm should be admitted once the lease expired")
	}
	if accepted := service.Accepted("S3"); len(accepted) != 1 || accepted[0].ID != "a2" {
		t.Fatalf("accepted=%+v, want alarm a2", accepted)
	}
}
