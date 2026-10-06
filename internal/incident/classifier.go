package incident

import "time"

type Classifier struct{}

func NewClassifier() *Classifier {
	return &Classifier{}
}

func (c *Classifier) LocalComplete(inc *Incident, requiresPeer bool, at time.Time) Snapshot {
	return inc.update(func(current *Snapshot) {
		if isClosed(current.State) {
			return
		}
		current.LocalComplete = true
		current.UpdatedAt = at.UTC()
		current.recordEvent(EventLocalComplete, at.UTC(), current.Assignee, current.AssigneeRole,
			"local diagnostics completed")
		if requiresPeer && !current.PeerComplete && at.Before(current.Deadline) {
			current.State = StateWaitingPeer
			current.Reason = "waiting for peer optical evidence"
			return
		}
		c.classify(current, at.UTC())
	})
}

func (c *Classifier) PeerEvidence(inc *Incident, at time.Time) Snapshot {
	return inc.update(func(current *Snapshot) {
		if current.State == StateCableCut || isClosed(current.State) {
			return
		}
		current.PeerComplete = true
		current.UpdatedAt = at.UTC()
		current.recordEvent(EventPeerEvidence, at.UTC(), current.Assignee, current.AssigneeRole,
			"peer landing optical evidence received")
		if current.LocalComplete {
			c.classify(current, at.UTC())
		}
	})
}

func (c *Classifier) Expire(inc *Incident, at time.Time) Snapshot {
	return inc.update(func(current *Snapshot) {
		if at.Before(current.Deadline) || current.State != StateWaitingPeer {
			return
		}
		current.State = StateStationFault
		current.Reason = "peer evidence window expired"
		current.UpdatedAt = at.UTC()
		current.recordEvent(EventExpired, at.UTC(), current.Assignee, current.AssigneeRole, current.Reason)
		current.recordEvent(EventClassified, at.UTC(), current.Assignee, current.AssigneeRole,
			"classified as station fault")
	})
}

// classify finalizes the diagnosis from completed local/peer flags. A paired
// wet-span cut is more severe than a station fault, so it promotes the case.
func (c *Classifier) classify(current *Snapshot, at time.Time) {
	if current.PeerComplete {
		current.State = StateCableCut
		current.Reason = "paired landing evidence identifies wet span fault"
		current.promoteSeverity(Sev2)
	} else {
		current.State = StateStationFault
		current.Reason = "local diagnostics completed without peer wet span evidence"
	}
	current.recordEvent(EventClassified, at, current.Assignee, current.AssigneeRole,
		"classified as "+string(current.State))
}
