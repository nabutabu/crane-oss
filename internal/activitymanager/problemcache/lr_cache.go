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

type LRCache struct {
	LR       *Node
	MR       *Node
	Capacity int
	TTL      time.Duration
	Cache    map[string]*Node
	mu       sync.Mutex
}

func NewCache(capacity int, ttl time.Duration) *LRCache {
	// initialize LR and MR
	lr := &Node{}
	mr := &Node{}

	lr.Next = mr
	mr.Prev = lr

	cache := &LRCache{
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

func (lrc *LRCache) PeriodicRun() {
	lrc.mu.Lock()
	defer lrc.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-lrc.TTL)

	// traverse from LR to find expired nodes
	current := lrc.LR.Next
	for current != lrc.MR && current.LastSeen.Before(cutoff) {
		next := current.Next
		lrc.removeNode(current)
		current = next
	}
}

func (lrc *LRCache) startPeriodicCleanup() {
	ticker := time.NewTicker(lrc.TTL)
	defer ticker.Stop()

	for range ticker.C {
		lrc.PeriodicRun()
	}
}

func (lrc *LRCache) removeNode(node_to_remove *Node) {
	prevNode := node_to_remove.Prev
	nextNode := node_to_remove.Next

	prevNode.Next = nextNode
	nextNode.Prev = prevNode

	delete(lrc.Cache, node_to_remove.Key)
}

func (lrc *LRCache) addMR(key string) *Node {
	node_to_add := NewNode(key, time.Now())

	prevNode := lrc.MR.Prev
	prevNode.Next = node_to_add
	node_to_add.Next = lrc.MR
	node_to_add.Prev = prevNode
	lrc.MR.Prev = node_to_add

	lrc.Cache[key] = node_to_add

	return node_to_add
}

func (lrc *LRCache) Record(key string) {
	lrc.mu.Lock()
	defer lrc.mu.Unlock()
	if _, found := lrc.Cache[key]; found {
		lrc.removeNode(lrc.Cache[key])
		lrc.addMR(key)
	}

	if lrc.Capacity == len(lrc.Cache) {
		// remove lr
		lrc.removeNode(lrc.LR.Next)
	}

	lrc.addMR(key)
}

func (lrc *LRCache) SeenRecord(key string) bool {
	lrc.mu.Lock()
	defer lrc.mu.Unlock()
	if _, found := lrc.Cache[key]; found {
		lrc.removeNode(lrc.Cache[key])
		lrc.addMR(key)
		return true
	}

	return false
}
