package subd

import (
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/nabutabu/crane-oss/pkg/api"
)

type PackagesCollector struct {
}

func NewPackagesCollecetor() *PackagesCollector {
	return &PackagesCollector{}
}

func getPackages() ([]api.Package, error) {
	cmd := exec.Command("dpkg-query", "-W", "-f=${Package} ${Version}\n")

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	var packages []api.Package

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		name := fields[0]
		version := fields[1]

		packages = append(packages, api.Package{
			Name:    name,
			Version: version,
		})
	}

	return packages, nil
}

func (c *PackagesCollector) Collect() ([]api.Package, error) {
	log.Println("Collecting Packages...")
	pkgs, err := getPackages()
	if err != nil {
		return nil, err
	}

	return pkgs, nil
}
func (c *PackagesCollector) DiffPackages(observed, desired []api.Package) []api.PackageAction {
	obs := make(map[string]struct{})
	des := make(map[string]struct{})

	for _, p := range observed {
		obs[p.Name] = struct{}{}
	}
	for _, p := range desired {
		des[p.Name] = struct{}{}
	}

	var actions []api.PackageAction

	// install missing
	for name := range des {
		if _, exists := obs[name]; !exists {
			actions = append(actions, api.PackageAction{
				Name:   name,
				Action: api.InstallPackage,
			})
		}
	}

	// remove extra
	for name := range obs {
		if _, exists := des[name]; !exists {
			actions = append(actions, api.PackageAction{
				Name:   name,
				Action: api.UninstallPackage,
			})
		}
	}

	return actions
}

func (c *PackagesCollector) Install(name string) error {
	cmd := exec.Command("pkexec", "apt-get", "install", "-y", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *PackagesCollector) Remove(name string) error {
	cmd := exec.Command("pkexec", "apt-get", "remove", "-y", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
