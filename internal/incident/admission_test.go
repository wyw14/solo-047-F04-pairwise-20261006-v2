package incident

import (
	"testing"
	"time"

	"github.com/wyw14/cry-103/internal/telemetry"
)

func TestAdmissionRetainsSuppressedAlarmsDuringMute(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC) }
	mutes := telemetry.NewMuteManager(now)
	admission := NewAdmissionService(mutes)

	if admission.Accept(Alarm{ID: "a1", SpanID: "S3", Kind: "loss", ObservedAt: now()}) != true {
		t.Fatalf("unmuted alarm should be admitted")
	}

	mutes.Acquire("lease-1", "S3", time.Minute)
	if admission.Accept(Alarm{ID: "a2", SpanID: "S3", Kind: "loss", ObservedAt: now()}) != false {
		t.Fatalf("muted alarm should not be admitted outwardly")
	}
	if admission.Accept(Alarm{ID: "a3", SpanID: "S9", Kind: "loss", ObservedAt: now()}) != true {
		t.Fatalf("alarm on another span should still be admitted")
	}

	all := admission.Alarms("S3")
	if len(all) != 2 {
		t.Fatalf("internal record should keep muted alarm, got %+v", all)
	}
	suppressed := admission.Suppressed("S3")
	if len(suppressed) != 1 || suppressed[0].ID != "a2" || suppressed[0].Notified {
		t.Fatalf("suppressed alarm retained internally without notification: %+v", suppressed)
	}
	if accepted := admission.Accepted("S3"); len(accepted) != 1 || accepted[0].ID != "a1" {
		t.Fatalf("accepted list must exclude suppressed alarms: %+v", accepted)
	}

	mutes.Release("lease-1")
	if admission.Accept(Alarm{ID: "a4", SpanID: "S3", Kind: "loss", ObservedAt: now()}) != true {
		t.Fatalf("alarm after lease release should be admitted")
	}
}
