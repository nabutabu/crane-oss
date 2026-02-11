package reconcile

import (
	"github.com/nabutabu/crane-oss/pkg/api"
)

type ReconcileDecision string

const (
	DecisionNone    ReconcileDecision = "none"
	DecisionDrain   ReconcileDecision = "drain"
	DecisionReplace ReconcileDecision = "replace"
)

func Decide(host *api.Host) {

}
