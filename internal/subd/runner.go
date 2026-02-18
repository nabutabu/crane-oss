package subd

import (
	"log"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type Runner struct {
	client            *Client
	servicesCollector *ServicesCollector
	pkgsCollector     *PackagesCollector
	interval          time.Duration
	healthChecked     bool
}

func NewRunner(client *Client, svcsCollector *ServicesCollector, pkgsCollector *PackagesCollector, interval time.Duration) *Runner {
	return &Runner{
		client:            client,
		servicesCollector: svcsCollector,
		pkgsCollector:     pkgsCollector,
		interval:          interval,
	}
}

func (r *Runner) Check() {
	// Observe local state from collector
	services, err := r.servicesCollector.Collect()
	if err != nil {
		log.Printf("Erroring collecting host information %w\n", err)
	}

	pkgs, err := r.pkgsCollector.Collect()
	if err != nil {
		log.Printf("Erroring collecting host information %w\n", err)
	}

	// make currState obj
	currState := &api.CurrentState{
		Services: services,
		Packages: pkgs,
	}

	// Send heartbeat through api
	desiredState, err := r.client.Heartbeat(*currState)
	if err != nil {
		log.Println(err)
	}

	// Receive desired state in api response and Compute diff
	actions := r.servicesCollector.DiffServices(desiredState.Services, currState.Services)
	pkgActions := r.pkgsCollector.DiffPackages(desiredState.Packages, pkgs)

	// perform actions
	err = r.servicesCollector.PerformActions(actions)
	if err != nil {
		log.Printf("Error Performing actions on services %s\n", err)
	}

	for _, action := range pkgActions {
		if action.Action == api.InstallPackage {
			err = r.pkgsCollector.Install(action.Name)
		} else {
			err = r.pkgsCollector.Remove(action.Name)
		}

		if err != nil {
			log.Printf("Error performing actions on packages %s\n", err)
		}
	}
}

func (r *Runner) Run() {
	if !r.healthChecked {
		if err := r.client.Health(); err != nil {
			log.Printf("Failed to connect to dominator: %v\n", err)
		} else {
			log.Println("Connected to dominator")
		}
		r.healthChecked = true
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for range ticker.C {
		r.Check()
	}
}
