package execute

import "time"

type ActionType string

const (
	ActionDrainHost   ActionType = "drain_host"
	ActionReplaceHost ActionType = "replace_host"
)

type Action struct {
	HostID string
	Type   ActionType
}

type ActionStatus string

const (
	ActionPending ActionStatus = "pending"
	ActionRunning ActionStatus = "running"
	ActionDone    ActionStatus = "done"
	ActionFailed  ActionStatus = "failed"
)

type ActionRecord struct {
	ID        int
	HostID    string
	Type      ActionType
	Status    ActionStatus
	Attempts  int
	CreatedAt time.Time
	UpdatedAt time.Time
}
