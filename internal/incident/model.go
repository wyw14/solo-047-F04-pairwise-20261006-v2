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
	StateResolved     State = "resolved"
)

type Snapshot struct {
	ID            string           `json:"id"`
	SpanID        string           `json:"span_id"`
	State         State            `json:"state"`
	Severity      Severity         `json:"severity,omitempty"`
	LocalComplete bool             `json:"local_complete"`
	PeerComplete  bool             `json:"peer_complete"`
	Deadline      time.Time        `json:"deadline"`
	Reason        string           `json:"reason"`
	Assignment    Assignment       `json:"assignment"`
	Assignments   []Assignment     `json:"assignments,omitempty"`
	Evidence      []EvidenceRecord `json:"evidence,omitempty"`
	Notices       []Notice         `json:"notices,omitempty"`
	OpenedAt      time.Time        `json:"opened_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

func (s Snapshot) clone() Snapshot {
	s.Assignments = append([]Assignment(nil), s.Assignments...)
	s.Evidence = append([]EvidenceRecord(nil), s.Evidence...)
	s.Notices = append([]Notice(nil), s.Notices...)
	for index := range s.Notices {
		s.Notices[index].Recipients = append([]string(nil), s.Notices[index].Recipients...)
	}
	return s
}

type Incident struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

func New(id, spanID string, deadline, at time.Time) *Incident {
	return &Incident{snapshot: Snapshot{
		ID: id, SpanID: spanID, State: StateCollecting, Deadline: deadline.UTC(),
		OpenedAt: at.UTC(), UpdatedAt: at.UTC(),
	}}
}

func (i *Incident) Snapshot() Snapshot {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.snapshot.clone()
}

func (i *Incident) update(mutator func(*Snapshot)) Snapshot {
	i.mu.Lock()
	defer i.mu.Unlock()
	mutator(&i.snapshot)
	return i.snapshot.clone()
}
