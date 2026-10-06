package incident

import (
	"sync"
	"time"
)

type State string

const (
	StateCollecting   State = "collecting"
	StateWaitingPeer  State = "waiting-peer"
	StateCableCut     State = "cable-cut"
	StateStationFault State = "station-fault"
	StateRestored     State = "restored"
	StateResolved     State = "resolved"
)

type Severity string

const (
	// Sev3 covers local station faults; Sev2 is a wet-span cable cut; Sev1 is
	// reserved for escalations driven by severity x wait-time policy.
	Sev3 Severity = "sev3"
	Sev2 Severity = "sev2"
	Sev1 Severity = "sev1"
)

// Rank orders severities for promotion; higher is more severe.
func (s Severity) Rank() int {
	switch s {
	case Sev1:
		return 3
	case Sev2:
		return 2
	default:
		return 1
	}
}

type EventType string

const (
	EventOpened                EventType = "opened"
	EventSuppressed            EventType = "suppressed"
	EventDispatched            EventType = "dispatched"
	EventEscalated             EventType = "escalated"
	EventTakenOver             EventType = "taken-over"
	EventReassigned            EventType = "reassigned"
	EventLocalComplete         EventType = "local-complete"
	EventPeerEvidence          EventType = "peer-evidence"
	EventSupplementaryEvidence EventType = "supplementary-evidence"
	EventClassified            EventType = "classified"
	EventExpired               EventType = "expired"
	EventRestored              EventType = "restored"
	EventResolved              EventType = "resolved"
)

// CaseEvent is one entry in the internal case history. While a mute lease is
// active the event is still recorded with Notified=false, so case progress is
// never lost; only outward notification is withheld.
type CaseEvent struct {
	Sequence  int       `json:"sequence"`
	Type      EventType `json:"type"`
	Actor     string    `json:"actor,omitempty"`
	Role      Role      `json:"role,omitempty"`
	Note      string    `json:"note,omitempty"`
	Notified  bool      `json:"notified"`
	Timestamp time.Time `json:"timestamp"`
}

type Snapshot struct {
	ID              string      `json:"id"`
	SpanID          string      `json:"span_id"`
	State           State       `json:"state"`
	Severity        Severity    `json:"severity"`
	Assignee        string      `json:"assignee"`
	AssigneeRole    Role        `json:"assignee_role"`
	EscalationLevel int         `json:"escalation_level"`
	LocalComplete   bool        `json:"local_complete"`
	PeerComplete    bool        `json:"peer_complete"`
	Deadline        time.Time   `json:"deadline"`
	Reason          string      `json:"reason"`
	Muted           bool        `json:"muted"`
	OpenedAt        time.Time   `json:"opened_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	Events          []CaseEvent `json:"events"`
}

type Incident struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

func New(id, spanID string, deadline, at time.Time) *Incident {
	now := at.UTC()
	return &Incident{snapshot: Snapshot{
		ID: id, SpanID: spanID, State: StateCollecting, Severity: Sev3,
		Deadline: deadline.UTC(), OpenedAt: now, UpdatedAt: now,
		Events: []CaseEvent{{Sequence: 1, Type: EventOpened, Timestamp: now}},
	}}
}

func (i *Incident) Snapshot() Snapshot {
	i.mu.RLock()
	defer i.mu.RUnlock()
	snapshot := i.snapshot
	snapshot.Events = append([]CaseEvent(nil), i.snapshot.Events...)
	return snapshot
}

func (i *Incident) update(mutator func(*Snapshot)) Snapshot {
	i.mu.Lock()
	defer i.mu.Unlock()
	mutator(&i.snapshot)
	snapshot := i.snapshot
	snapshot.Events = append([]CaseEvent(nil), i.snapshot.Events...)
	return snapshot
}

// recordEvent appends an internal case event. Muted cases keep every event but
// mark it Notified=false so notifications remain suppressed during the lease.
func (s *Snapshot) recordEvent(kind EventType, at time.Time, actor string, role Role, note string) CaseEvent {
	event := CaseEvent{
		Sequence:  len(s.Events) + 1,
		Type:      kind,
		Actor:     actor,
		Role:      role,
		Note:      note,
		Notified:  !s.Muted,
		Timestamp: at.UTC(),
	}
	s.Events = append(s.Events, event)
	return event
}

// promoteSeverity raises the severity only when the candidate is more severe.
func (s *Snapshot) promoteSeverity(severity Severity) bool {
	if severity.Rank() > s.Severity.Rank() {
		s.Severity = severity
		return true
	}
	return false
}
