package incident

import "time"

// Role is an on-call duty role that can own an incident.
type Role string

const (
	RoleResponder Role = "responder"
	RoleLead      Role = "lead"
	RoleCommander Role = "commander"
	RoleDirector  Role = "director"
)

// EscalationStep maps an escalation level to the duty role paged at that level.
type EscalationStep struct {
	Level    int
	Role     Role
	WaitTime time.Duration
}

// EscalationPolicy is the severity x wait-time dispatch ladder. Earlier steps
// page first responders; each later step waits from case open before paging
// the next duty role.
type EscalationPolicy struct {
	Steps map[Severity][]EscalationStep
}

// DefaultPolicy ladders:
//
//	sev1 (highest): responder immediately, lead after 5m, commander after 15m, director after 30m
//	sev2:           responder immediately, lead after 15m, commander after 45m
//	sev3:           responder after 5m, lead after 60m
func DefaultPolicy() EscalationPolicy {
	return EscalationPolicy{Steps: map[Severity][]EscalationStep{
		Sev1: {
			{Level: 1, Role: RoleResponder, WaitTime: 0},
			{Level: 2, Role: RoleLead, WaitTime: 5 * time.Minute},
			{Level: 3, Role: RoleCommander, WaitTime: 15 * time.Minute},
			{Level: 4, Role: RoleDirector, WaitTime: 30 * time.Minute},
		},
		Sev2: {
			{Level: 1, Role: RoleResponder, WaitTime: 0},
			{Level: 2, Role: RoleLead, WaitTime: 15 * time.Minute},
			{Level: 3, Role: RoleCommander, WaitTime: 45 * time.Minute},
		},
		Sev3: {
			{Level: 1, Role: RoleResponder, WaitTime: 5 * time.Minute},
			{Level: 2, Role: RoleLead, WaitTime: 60 * time.Minute},
		},
	}}
}

// dueLevel returns the highest escalation step whose wait time has elapsed
// since opened, given the case severity. Zero when nothing is due yet.
func (p EscalationPolicy) dueLevel(severity Severity, openedAt, at time.Time) int {
	waited := at.UTC().Sub(openedAt.UTC())
	if waited < 0 {
		waited = 0
	}
	level := 0
	for _, step := range p.Steps[severity] {
		if waited >= step.WaitTime {
			if step.Level > level {
				level = step.Level
			}
		}
	}
	return level
}

func (p EscalationPolicy) roleAt(severity Severity, level int) Role {
	for _, step := range p.Steps[severity] {
		if step.Level == level {
			return step.Role
		}
	}
	return ""
}

// levelForRole returns the ladder step at which a role is paged for a
// severity, or zero when the role is not on that severity's ladder.
func (p EscalationPolicy) levelForRole(severity Severity, role Role) int {
	for _, step := range p.Steps[severity] {
		if step.Role == role {
			return step.Level
		}
	}
	return 0
}
