package problemcache

import (
	"github.com/nabutabu/crane-oss/internal/badhost/problem"
)

type SeenProblemCache interface {
	SeenRecently(key string) bool
	Record(key string)
}

type ProblemCacheKey struct {
	Host_id string
	Type    problem.ProblemType
}

func (k ProblemCacheKey) String() string {
	return k.Host_id + "|" + string(k.Type)
}
