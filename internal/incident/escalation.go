package incident

import "time"

// Severity ranks how urgently a fault needs on-call attention.
type Severity string

const (
	SeverityMinor    Severity = "minor"
	SeverityMajor    Severity = "major"
	SeverityCritical Severity = "critical"
)

// Role identifies the on-call duty an incident is dispatched to.
type Role string

const (
	RoleObserver   Role = "observer"
	RoleTechnician Role = "duty-technician"
	RoleEngineer   Role = "duty-engineer"
	RoleCommander  Role = "incident-commander"
)

// Step promotes an incident to Role once it has waited at least After.
type Step struct {
	After time.Duration
	Role  Role
}

// Policy dispatches on-call roles from fault severity and wait time.
// Steps per severity must be ordered by ascending After.
type Policy struct {
	steps map[Severity][]Step
}

func DefaultPolicy() Policy {
	return Policy{steps: map[Severity][]Step{
		SeverityCritical: {
			{After: 0, Role: RoleEngineer},
			{After: 2 * time.Minute, Role: RoleCommander},
		},
		SeverityMajor: {
			{After: 0, Role: RoleTechnician},
			{After: 5 * time.Minute, Role: RoleEngineer},
			{After: 15 * time.Minute, Role: RoleCommander},
		},
		SeverityMinor: {
			{After: 0, Role: RoleObserver},
			{After: 10 * time.Minute, Role: RoleTechnician},
			{After: 30 * time.Minute, Role: RoleEngineer},
		},
	}}
}

func (p Policy) Dispatch(severity Severity, waited time.Duration) Role {
	steps, ok := p.steps[severity]
	if !ok {
		steps = p.steps[SeverityMinor]
	}
	role := RoleObserver
	for _, step := range steps {
		if waited >= step.After {
			role = step.Role
		}
	}
	return role
}

// SeverityFor derives a default severity from the classified state.
func SeverityFor(state State) Severity {
	switch state {
	case StateCableCut:
		return SeverityCritical
	case StateStationFault:
		return SeverityMajor
	default:
		return SeverityMinor
	}
}

func ParseSeverity(value string) (Severity, bool) {
	switch Severity(value) {
	case "":
		return "", true
	case SeverityMinor, SeverityMajor, SeverityCritical:
		return Severity(value), true
	default:
		return "", false
	}
}
