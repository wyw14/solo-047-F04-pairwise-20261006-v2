package incident

import "errors"

var (
	ErrActorRequired = errors.New("incident: actor is required")
	ErrUnknownRole   = errors.New("incident: unknown duty role")
)
