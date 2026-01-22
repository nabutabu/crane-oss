package problemcache

import (
	"time"
)

type Node struct {
	Key   string
	Value time.Time
	Next  *Node
	Prev  *Node
}

func NewNode(key string, value time.Time) *Node {
	return &Node{
		Key:   key,
		Value: value,
	}
}

type lr_cache struct {
	LR       *Node
	MR       *Node
	Capacity int
	TTL      time.Duration
	Cache    map[string]*Node
}

func NewCache(capacity int) *lr_cache {
	// initialize LR and MR
	lr := NewNode("", time.Now())
	mr := NewNode("", time.Now())

	lr.Next = mr
	mr.Prev = lr

	return &lr_cache{
		LR:       lr,
		MR:       mr,
		Capacity: capacity,
	}
}

func (lrc *lr_cache) PeriodicRun() {
	// from LR traverse through all nodes to find first node such that node.Value < time.Now + TTL
	// once found remove all nodes between that node and LR
}

func (lrc *lr_cache) RemoveNode(node_to_remove *Node) {
	prevNode := node_to_remove.Prev
	nextNode := node_to_remove.Next

	prevNode.Next = nextNode
	nextNode.Prev = prevNode

	delete(lrc.Cache, node_to_remove.Key)
}

func (lrc *lr_cache) AddMR(key string) *Node {
	node_to_add := NewNode(key, time.Now())

	prevNode := lrc.MR.Prev
	prevNode.Next = node_to_add
	node_to_add.Next = lrc.MR
	node_to_add.Prev = prevNode
	lrc.MR.Prev = node_to_add

	lrc.Cache[key] = node_to_add

	return node_to_add
}

func (lrc *lr_cache) Record(key string) time.Time {
	if _, found := lrc.Cache[key]; found {
		lrc.RemoveNode(lrc.Cache[key])
		addedNode := lrc.AddMR(key)
		return addedNode.Value
	}

	if lrc.Capacity == len(lrc.Cache) {
		// remove lr
		lrc.RemoveNode(lrc.LR.Next)
	}

	addedNode := lrc.AddMR(key)

	return addedNode.Value
}

func (lrc *lr_cache) SeenRecord(key string) *time.Time {
	if _, found := lrc.Cache[key]; found {
		value := lrc.Cache[key].Value
		lrc.RemoveNode(lrc.Cache[key])
		lrc.AddMR(key)
		return &value
	}

	return nil
}
