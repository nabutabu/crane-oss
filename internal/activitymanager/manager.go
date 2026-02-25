package activitymanager

import (
	"context"
	"log"
	"time"

	"github.com/nabutabu/crane-oss/internal/activitymanager/problemcache"
	"github.com/nabutabu/crane-oss/internal/badhost/problem"
	"github.com/nabutabu/crane-oss/internal/execute"
)

type ActivityManager struct {
	problemStore  problem.ProblemStore
	actionStore   execute.ActionStore
	cache         problemcache.SeenProblemCache
	contextWindow time.Duration // the context window will use the problems within this window from now
	ScanInterval  time.Duration // run the activity manager every ScanInterval minutes
}

func NewActivityManager(pStore problem.ProblemStore, aStore execute.ActionStore, cache problemcache.SeenProblemCache, window time.Duration, interval time.Duration) *ActivityManager {
	return &ActivityManager{
		problemStore:  pStore,
		actionStore:   aStore,
		cache:         cache,
		contextWindow: window,
		ScanInterval:  interval,
	}
}

func (am *ActivityManager) Run(ctx context.Context) {
	ticker := time.NewTicker(am.ScanInterval)
	defer ticker.Stop()

	log.Printf("[ActivityManager] starting")

	for {
		select {
		case <-ctx.Done():
			log.Println("[ActivityManager] shutting down")
			return

		case <-ticker.C:
			am.RunHelp(ctx)
		}
	}
}

func (am *ActivityManager) RunHelp(ctx context.Context) {
	// get problems from ProblemStore
	problems, err := am.problemStore.GetRecentProblems(ctx, am.contextWindow)
	if err != nil {
		log.Println(err)
		return
	}

	log.Printf("[Info:ActivityManager]/RunHelp: Problems seen: %v", problems)

	// group problems by host
	hostProblems := make(map[string][]problem.Problem)
	for _, p := range problems {
		hostProblems[p.Host_id] = append(hostProblems[p.Host_id], p)
	}

	// process each host's problems
	for hostID, hostProblemList := range hostProblems {
		for _, hostProblem := range hostProblemList {
			cacheKey := problemcache.ProblemCacheKey{
				Host_id: hostID,
				Type:    hostProblem.Type,
			}

			if am.cache.SeenRecently(cacheKey.String()) {
				continue
			}

			// record that we've processed this host
			am.cache.Record(cacheKey.String())

			// make a decision based on this host's problems
			action := Decide(hostID, hostProblemList)

			if action != nil {
				log.Printf("Host: %s - Action: %s", hostID, action.Type)

				// enqueue the action if one was decided
				err := am.actionStore.Enqueue(ctx, action)
				if err != nil {
					log.Printf("Failed to enqueue action for host %s: %v", hostID, err)
				}
			}
		}
	}
}
