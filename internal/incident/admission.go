package incident

import (
	"sync"
	"time"

	"github.com/wyw14/cry-103/internal/telemetry"
)

type Alarm struct {
	ID         string    `json:"id"`
	SpanID     string    `json:"span_id"`
	Kind       string    `json:"kind"`
	ObservedAt time.Time `json:"observed_at"`
	// Suppressed is true when a mute lease hid the alarm. Suppressed alarms
	// are retained as internal case material instead of being dropped.
	Suppressed bool `json:"suppressed"`
	Notified   bool `json:"notified"`
}

type AdmissionService struct {
	mu     sync.RWMutex
	mutes  *telemetry.MuteManager
	alarms []Alarm
}

func NewAdmissionService(mutes *telemetry.MuteManager) *AdmissionService {
	return &AdmissionService{mutes: mutes}
}

// Accept admits an alarm. During a mute lease the alarm is not admitted
// outwardly but is still stored with Suppressed=true/Notified=false, so the
// internal case history keeps every observation.
func (s *AdmissionService) Accept(alarm Alarm) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mutes.Muted(alarm.SpanID) {
		alarm.Suppressed = true
		alarm.Notified = false
		s.alarms = append(s.alarms, alarm)
		return false
	}
	alarm.Notified = true
	s.alarms = append(s.alarms, alarm)
	return true
}

// Alarms returns every recorded alarm for a span, including suppressed ones.
func (s *AdmissionService) Alarms(spanID string) []Alarm {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Alarm
	for _, alarm := range s.alarms {
		if alarm.SpanID == spanID {
			result = append(result, alarm)
		}
	}
	return result
}

// Muted reports whether a mute lease currently covers the span.
func (s *AdmissionService) Muted(spanID string) bool {
	return s.mutes.Muted(spanID)
}

// Accepted returns only outwardly admitted alarms for a span.
func (s *AdmissionService) Accepted(spanID string) []Alarm {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Alarm
	for _, alarm := range s.alarms {
		if alarm.SpanID == spanID && !alarm.Suppressed {
			result = append(result, alarm)
		}
	}
	return result
}

// Suppressed returns alarms hidden by mute leases for a span.
func (s *AdmissionService) Suppressed(spanID string) []Alarm {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Alarm
	for _, alarm := range s.alarms {
		if alarm.SpanID == spanID && alarm.Suppressed {
			result = append(result, alarm)
		}
	}
	return result
}
