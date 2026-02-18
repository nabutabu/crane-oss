package subd

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type ServicesCollector struct {
}

func NewServicesCollector() *ServicesCollector {
	return &ServicesCollector{}
}

func getServices() (map[string]api.Service, error) {
	cmd := exec.Command(
		"systemctl",
		"list-units",
		"--type=service",
		"--state=running",
		"--no-pager",
		"--no-legend",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("systemctl failed: %w", err)
	}

	services := make(map[string]api.Service)

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		name := fields[0]

		log.Println(name)

		services[name] = api.Service{
			Name:    name,
			Running: true,
		}
	}

	return services, nil
}

func (c *ServicesCollector) DiffServices(
	desired map[string]api.Service,
	current map[string]api.Service,
) []api.ServiceAction {
	var actions []api.ServiceAction

	for name, desiredSvc := range desired {
		currSvc, exists := current[name]

		if desiredSvc.Running && (!exists || !currSvc.Running) {
			actions = append(actions, api.ServiceAction{
				Name:   name,
				Action: api.StartService,
			})
		}

		if !desiredSvc.Running && exists && currSvc.Running {
			actions = append(actions, api.ServiceAction{
				Name:   name,
				Action: api.StopService,
			})
		}
	}

	return actions
}

func (c *ServicesCollector) PerformActions(actions []api.ServiceAction) error {
	var errors []string

	for _, action := range actions {
		cmd := exec.Command("systemctl", string(action.Action), action.Name)

		done := make(chan error, 1)
		go func() {
			done <- cmd.Run()
		}()

		select {
		case err := <-done:
			if err != nil {
				errorMsg := fmt.Sprintf("failed to %s service %s: %v", action.Action, action.Name, err)
				log.Println(errorMsg)
				errors = append(errors, errorMsg)
			} else {
				log.Printf("successfully %s service %s", action.Action, action.Name)
			}
		case <-time.After(30 * time.Second):
			errorMsg := fmt.Sprintf("timeout after 30s trying to %s service %s", action.Action, action.Name)
			log.Println(errorMsg)
			errors = append(errors, errorMsg)
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		}
	}

	return nil
}

func (c *ServicesCollector) Collect() (map[string]api.Service, error) {
	log.Println("Collecting...")
	services, err := getServices()
	if err != nil {
		return nil, err
	}

	return services, nil
}
