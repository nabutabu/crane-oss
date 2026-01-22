package problemcache

type SeenProblemCache interface {
	SeenRecently(key string) bool
	Record(key string)
}
