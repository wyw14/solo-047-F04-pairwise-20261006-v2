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
}

type AdmissionService struct {
	mu         sync.RWMutex
	mutes      *telemetry.MuteManager
	accepted   []Alarm
	suppressed []Alarm
}

func NewAdmissionService(mutes *telemetry.MuteManager) *AdmissionService {
	return &AdmissionService{mutes: mutes}
}

// Accept files the alarm for processing. Alarms arriving under a mute lease
// are not dispatched, but stay on the internal case file as suppressed
// entries so case progress is preserved for the lease duration.
func (s *AdmissionService) Accept(alarm Alarm) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mutes.Muted(alarm.SpanID) {
		s.suppressed = append(s.suppressed, alarm)
		return false
	}
	s.accepted = append(s.accepted, alarm)
	return true
}

func (s *AdmissionService) Accepted(spanID string) []Alarm {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Alarm
	for _, alarm := range s.accepted {
		if alarm.SpanID == spanID {
			result = append(result, alarm)
		}
	}
	return result
}

// Suppressed lists alarms held back by an active mute lease.
func (s *AdmissionService) Suppressed(spanID string) []Alarm {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Alarm
	for _, alarm := range s.suppressed {
		if alarm.SpanID == spanID {
			result = append(result, alarm)
		}
	}
	return result
}
