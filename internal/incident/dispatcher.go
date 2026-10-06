package incident

import (
	"sync"
	"time"
)

// Notifier receives outward dispatch messages. It is only invoked for events
// recorded with Notified=true; events produced during a mute lease stay
// internal and never reach a notifier.
type Notifier interface {
	Notify(incidentID string, event CaseEvent)
}

// Dispatcher runs the severity x wait-time escalation ladder and owns the
// duty-role operations: take over, reassign, supplementary evidence and
// restoration notice.
type Dispatcher struct {
	mu     sync.RWMutex
	policy EscalationPolicy
	now    func() time.Time
	roster map[Role]string
	notify Notifier
}

func NewDispatcher(policy EscalationPolicy, now func() time.Time, notifier Notifier) *Dispatcher {
	return &Dispatcher{policy: policy, now: now, roster: make(map[Role]string), notify: notifier}
}

// SetOnCall registers the person currently covering a duty role.
func (d *Dispatcher) SetOnCall(role Role, person string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if person == "" {
		delete(d.roster, role)
		return
	}
	d.roster[role] = person
}

func (d *Dispatcher) onCall(role Role) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if person := d.roster[role]; person != "" {
		return person
	}
	return "on-call-" + string(role)
}

// Evaluate advances the incident to the highest escalation step whose wait
// time has elapsed. During a mute lease the assignment still happens and is
// recorded internally, but no notification is sent.
func (d *Dispatcher) Evaluate(inc *Incident) Snapshot {
	now := d.now()
	return inc.update(func(s *Snapshot) {
		if isClosed(s.State) {
			return
		}
		due := d.policy.dueLevel(s.Severity, s.OpenedAt, now)
		if due <= s.EscalationLevel {
			return
		}
		targetRole := d.policy.roleAt(s.Severity, due)
		if targetRole == "" {
			return
		}
		person := d.onCall(targetRole)
		previousRole := s.AssigneeRole
		s.Assignee = person
		s.AssigneeRole = targetRole
		s.EscalationLevel = due
		s.UpdatedAt = now
		if previousRole == "" {
			event := s.recordEvent(EventDispatched, now, person, targetRole,
				"dispatched to "+string(targetRole))
			d.emit(s.ID, event)
			return
		}
		event := s.recordEvent(EventEscalated, now, person, targetRole,
			"escalated from "+string(previousRole)+" to "+string(targetRole))
		d.emit(s.ID, event)
	})
}

// TakeOver hands the incident to another responder at the same duty level.
// Before anyone has been paged it performs the initial dispatch to the actor.
func (d *Dispatcher) TakeOver(inc *Incident, actor string) (Snapshot, error) {
	if actor == "" {
		return Snapshot{}, ErrActorRequired
	}
	now := d.now()
	snapshot := inc.update(func(s *Snapshot) {
		if isClosed(s.State) {
			return
		}
		if s.AssigneeRole == "" {
			due := d.policy.dueLevel(s.Severity, s.OpenedAt, now)
			if due < 1 {
				due = 1
			}
			role := d.policy.roleAt(s.Severity, due)
			s.Assignee = actor
			s.AssigneeRole = role
			s.EscalationLevel = due
			s.UpdatedAt = now
			event := s.recordEvent(EventDispatched, now, actor, role, "taken over before initial dispatch")
			d.emit(s.ID, event)
			return
		}
		previous := s.Assignee
		s.Assignee = actor
		s.UpdatedAt = now
		event := s.recordEvent(EventTakenOver, now, actor, s.AssigneeRole,
			"ownership taken over from "+previous)
		d.emit(s.ID, event)
	})
	return snapshot, nil
}

// Reassign moves the incident onto another duty role (or a specific person on
// that role). The escalation level advances to the role's ladder step.
func (d *Dispatcher) Reassign(inc *Incident, actor string, role Role) (Snapshot, error) {
	if actor == "" {
		return Snapshot{}, ErrActorRequired
	}
	if !validRole(role) {
		return Snapshot{}, ErrUnknownRole
	}
	now := d.now()
	snapshot := inc.update(func(s *Snapshot) {
		if isClosed(s.State) {
			return
		}
		level := d.policy.levelForRole(s.Severity, role)
		if level == 0 {
			level = s.EscalationLevel
		}
		previous := s.AssigneeRole
		s.Assignee = actor
		s.AssigneeRole = role
		if level > s.EscalationLevel {
			s.EscalationLevel = level
		}
		s.UpdatedAt = now
		note := "reassigned from " + string(previous) + " to " + string(role)
		event := s.recordEvent(EventReassigned, now, actor, role, note)
		d.emit(s.ID, event)
	})
	return snapshot, nil
}

// AddSupplementaryEvidence appends human/peer-supplied case context without
// changing the diagnostic state. Notes survive mute leases and closure.
func (d *Dispatcher) AddSupplementaryEvidence(inc *Incident, actor, note string) Snapshot {
	now := d.now()
	return inc.update(func(s *Snapshot) {
		if isClosed(s.State) {
			return
		}
		if actor == "" {
			actor = s.Assignee
		}
		s.UpdatedAt = now
		event := s.recordEvent(EventSupplementaryEvidence, now, actor, s.AssigneeRole, note)
		d.emit(s.ID, event)
	})
}

// MarkMuted toggles the case mute flag. Entering mute records an internal
// suppression event; while muted every subsequent event is retained with
// Notified=false and the notifier is skipped.
func (d *Dispatcher) MarkMuted(inc *Incident, muted bool) Snapshot {
	now := d.now()
	return inc.update(func(s *Snapshot) {
		if s.Muted == muted {
			return
		}
		s.Muted = muted
		s.UpdatedAt = now
		if muted {
			event := s.recordEvent(EventSuppressed, now, s.Assignee, s.AssigneeRole,
				"mute lease active: notifications suppressed, case progress retained internally")
			d.emit(s.ID, event)
		}
	})
}

// Restore records the recovery notification after the wet span or station
// fault has been cleared. It does not close the case; Resolve does.
func (d *Dispatcher) Restore(inc *Incident, actor, note string) Snapshot {
	now := d.now()
	return inc.update(func(s *Snapshot) {
		if isClosed(s.State) {
			return
		}
		s.State = StateRestored
		s.Reason = "service restored; recovery notification issued"
		if note != "" {
			s.Reason = note
		}
		s.UpdatedAt = now
		event := s.recordEvent(EventRestored, now, actor, s.AssigneeRole, s.Reason)
		d.emit(s.ID, event)
	})
}

// Resolve closes the case after restoration is confirmed.
func (d *Dispatcher) Resolve(inc *Incident, actor string) Snapshot {
	now := d.now()
	return inc.update(func(s *Snapshot) {
		if s.State == StateResolved {
			return
		}
		s.State = StateResolved
		s.UpdatedAt = now
		event := s.recordEvent(EventResolved, now, actor, s.AssigneeRole, "case resolved")
		d.emit(s.ID, event)
	})
}

func (d *Dispatcher) emit(incidentID string, event CaseEvent) {
	if d.notify != nil && event.Notified {
		d.notify.Notify(incidentID, event)
	}
}

func isClosed(state State) bool {
	return state == StateResolved
}

func validRole(role Role) bool {
	switch role {
	case RoleResponder, RoleLead, RoleCommander, RoleDirector:
		return true
	default:
		return false
	}
}
