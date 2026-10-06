package incident

import (
	"fmt"
	"time"
)

// Assignment records who an incident is dispatched to. The zero value
// means the incident has not been dispatched yet.
type Assignment struct {
	Role     Role      `json:"role"`
	Operator string    `json:"operator,omitempty"`
	Since    time.Time `json:"since"`
	Reason   string    `json:"reason"`
}

// EvidenceRecord is a supplementary case entry beyond peer OTDR pairing.
type EvidenceRecord struct {
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
	Actor  string    `json:"actor,omitempty"`
	Muted  bool      `json:"muted"`
	At     time.Time `json:"at"`
}

// Notice is an outbound notification to on-call staff. Suppressed notices
// were held back by an active mute lease but stay on the case file.
type Notice struct {
	Kind       string    `json:"kind"`
	Role       Role      `json:"role,omitempty"`
	Recipients []string  `json:"recipients,omitempty"`
	Message    string    `json:"message"`
	Suppressed bool      `json:"suppressed"`
	At         time.Time `json:"at"`
}

// MuteLookup reports whether a span is currently under a mute lease.
type MuteLookup interface {
	Muted(spanID string) bool
}

// Desk escalates incidents to on-call roles and keeps the internal case
// file (assignments, evidence, notices) even while a span is muted.
type Desk struct {
	policy Policy
	mutes  MuteLookup
}

func NewDesk(policy Policy, mutes MuteLookup) *Desk {
	return &Desk{policy: policy, mutes: mutes}
}

func (d *Desk) muted(spanID string) bool {
	return d.mutes != nil && d.mutes.Muted(spanID)
}

// Escalate dispatches the incident to the on-call role implied by its
// severity and how long it has been waiting. An explicit severity overrides
// the one derived from the classified state.
func (d *Desk) Escalate(target *Incident, severity Severity, at time.Time) Snapshot {
	return target.update(func(current *Snapshot) {
		if current.State == StateResolved {
			return
		}
		if severity != "" {
			current.Severity = severity
		}
		d.ensureRole(current, at)
	})
}

// Claim lets an operator take over the incident, dispatching a role first
// if none has been assigned yet.
func (d *Desk) Claim(target *Incident, operator string, at time.Time) Snapshot {
	return target.update(func(current *Snapshot) {
		if current.State == StateResolved || operator == "" {
			return
		}
		d.ensureRole(current, at)
		if current.Assignment.Operator == operator {
			return
		}
		assignment := current.Assignment
		assignment.Operator = operator
		assignment.Since = at.UTC()
		if current.Assignment.Operator == "" {
			assignment.Reason = "claimed by " + operator
		} else {
			assignment.Reason = fmt.Sprintf("takeover from %s", current.Assignment.Operator)
		}
		current.Assignment = assignment
		current.Assignments = append(current.Assignments, assignment)
		current.UpdatedAt = at.UTC()
	})
}

// Reassign hands the incident to another operator under the same role.
func (d *Desk) Reassign(target *Incident, operator string, at time.Time) Snapshot {
	return target.update(func(current *Snapshot) {
		if current.State == StateResolved || operator == "" {
			return
		}
		d.ensureRole(current, at)
		if current.Assignment.Operator == operator {
			return
		}
		assignment := current.Assignment
		assignment.Since = at.UTC()
		if current.Assignment.Operator == "" {
			assignment.Reason = "assigned to " + operator
		} else {
			assignment.Reason = fmt.Sprintf("reassigned from %s", current.Assignment.Operator)
		}
		assignment.Operator = operator
		current.Assignment = assignment
		current.Assignments = append(current.Assignments, assignment)
		current.UpdatedAt = at.UTC()
	})
}

// AddEvidence supplements the case file with a note, trace reference or
// other record. Evidence gathered during a mute lease is kept and flagged.
func (d *Desk) AddEvidence(target *Incident, kind, detail, actor string, at time.Time) Snapshot {
	return target.update(func(current *Snapshot) {
		if current.State == StateResolved {
			return
		}
		if kind == "" {
			kind = "note"
		}
		current.Evidence = append(current.Evidence, EvidenceRecord{
			Kind: kind, Detail: detail, Actor: actor,
			Muted: d.muted(current.SpanID),
			At:    at.UTC(),
		})
		current.UpdatedAt = at.UTC()
	})
}

// Recover resolves the incident and notifies every operator who held the
// case. The notice is recorded even while a mute lease suppresses delivery.
func (d *Desk) Recover(target *Incident, operator, message string, at time.Time) Snapshot {
	return target.update(func(current *Snapshot) {
		if current.State == StateResolved {
			return
		}
		current.State = StateResolved
		if message == "" {
			message = "service restored"
		}
		current.Reason = message
		current.UpdatedAt = at.UTC()
		current.Notices = append(current.Notices, Notice{
			Kind:       "recovery",
			Role:       current.Assignment.Role,
			Recipients: recipients(current.Assignments),
			Message:    message,
			Suppressed: d.muted(current.SpanID),
			At:         at.UTC(),
		})
	})
}

// ensureRole dispatches (or promotes) the on-call role from severity and
// wait time, logging an escalation notice whenever the role changes.
func (d *Desk) ensureRole(current *Snapshot, at time.Time) {
	if current.Severity == "" {
		current.Severity = SeverityFor(current.State)
	}
	waited := at.Sub(current.OpenedAt)
	if waited < 0 {
		waited = 0
	}
	role := d.policy.Dispatch(current.Severity, waited)
	if current.Assignment.Role == role {
		return
	}
	assignment := Assignment{
		Role:   role,
		Since:  at.UTC(),
		Reason: fmt.Sprintf("severity %s waited %s", current.Severity, waited),
	}
	current.Assignment = assignment
	current.Assignments = append(current.Assignments, assignment)
	current.Notices = append(current.Notices, Notice{
		Kind:       "escalation",
		Role:       role,
		Message:    fmt.Sprintf("dispatched %s for %s fault", role, current.Severity),
		Suppressed: d.muted(current.SpanID),
		At:         at.UTC(),
	})
	current.UpdatedAt = at.UTC()
}

func recipients(assignments []Assignment) []string {
	seen := make(map[string]bool)
	var result []string
	for _, assignment := range assignments {
		if assignment.Operator == "" || seen[assignment.Operator] {
			continue
		}
		seen[assignment.Operator] = true
		result = append(result, assignment.Operator)
	}
	return result
}
