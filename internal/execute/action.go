package execute

import (
	"time"
)

type ActionType string

const (
	ActionCreateHost  ActionType = "create_host"
	ActionDrainHost   ActionType = "drain_host"
	ActionReplaceHost ActionType = "replace_host"
	ActionAssignHost  ActionType = "assign_host"
)

type Action struct {
	HostID string
	Type   ActionType
	PoolID string
	Cost   int // number of credits used for this action
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
