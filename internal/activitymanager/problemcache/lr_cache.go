package problemcache

import (
	"sync"
	"time"
)

type Node struct {
	Key      string
	LastSeen time.Time
	Next     *Node
	Prev     *Node
}

func NewNode(key string, value time.Time) *Node {
	return &Node{
		Key:      key,
		LastSeen: value,
	}
}

type lr_cache struct {
	LR       *Node
	MR       *Node
	Capacity int
	TTL      time.Duration
	Cache    map[string]*Node
	mu       sync.Mutex
}

func NewCache(capacity int, ttl time.Duration) *lr_cache {
	// initialize LR and MR
	lr := NewNode("", time.Now())
	mr := NewNode("", time.Now())

	lr.Next = mr
	mr.Prev = lr

	cache := &lr_cache{
		LR:       lr,
		MR:       mr,
		Capacity: capacity,
		TTL:      ttl,
		Cache:    make(map[string]*Node),
	}

	// Start periodic cleanup
	go cache.startPeriodicCleanup()

	return cache
}

func (lrc *lr_cache) PeriodicRun() {
	now := time.Now()
	cutoff := now.Add(-lrc.TTL)

	// traverse from LR to find expired nodes
	current := lrc.LR.Next
	for current != lrc.MR && current.LastSeen.Before(cutoff) {
		next := current.Next
		lrc.RemoveNode(current)
		current = next
	}
}

func (lrc *lr_cache) startPeriodicCleanup() {
	ticker := time.NewTicker(lrc.TTL)
	defer ticker.Stop()

	for range ticker.C {
		lrc.PeriodicRun()
	}
}

func (lrc *lr_cache) RemoveNode(node_to_remove *Node) {
	lrc.mu.Lock()
	defer lrc.mu.Unlock()
	prevNode := node_to_remove.Prev
	nextNode := node_to_remove.Next

	prevNode.Next = nextNode
	nextNode.Prev = prevNode

	delete(lrc.Cache, node_to_remove.Key)
}

func (lrc *lr_cache) AddMR(key string) *Node {
	lrc.mu.Lock()
	defer lrc.mu.Unlock()
	node_to_add := NewNode(key, time.Now())

	prevNode := lrc.MR.Prev
	prevNode.Next = node_to_add
	node_to_add.Next = lrc.MR
	node_to_add.Prev = prevNode
	lrc.MR.Prev = node_to_add

	lrc.Cache[key] = node_to_add

	return node_to_add
}

func (lrc *lr_cache) Record(key string) {
	if _, found := lrc.Cache[key]; found {
		lrc.RemoveNode(lrc.Cache[key])
		lrc.AddMR(key)
	}

	if lrc.Capacity == len(lrc.Cache) {
		// remove lr
		lrc.RemoveNode(lrc.LR.Next)
	}

	lrc.AddMR(key)
}

func (lrc *lr_cache) SeenRecord(key string) bool {
	if _, found := lrc.Cache[key]; found {
		lrc.RemoveNode(lrc.Cache[key])
		lrc.AddMR(key)
		return true
	}

	return false
}
