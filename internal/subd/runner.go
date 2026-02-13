package subd

import (
	"log"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type Runner struct {
	client            Client
	servicesCollector ServicesCollector
	pkgsCollector     PackagesCollector
	interval          time.Duration
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
		log.Printf("Error Performing actions %s\n", err)
	}

	for _, action := range pkgActions {
		if action.Action == api.InstallPackage {
			r.pkgsCollector.Install(action.Name)
		} else {
			r.pkgsCollector.Remove(action.Name)
		}
	}
}

func (r *Runner) Run() {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for range ticker.C {
		r.Check()
	}
}
