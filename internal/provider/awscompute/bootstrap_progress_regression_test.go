package awscompute

import (
	"os"
	"strings"
	"testing"
)

func TestBootstrapSocketAndManifestRegression(t *testing.T) {
	unit, err := os.ReadFile("../../../golden_image_generator/templates/spire-agent.service")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "RuntimeDirectoryPreserve=yes") {
		t.Fatal("SPIRE socket directory must survive restart")
	}
	scripts := renderAll(t)
	platform := scripts["platform"]
	if strings.Count(platform, `service.instance.id=\$(POD_NAME)`) != 2 {
		t.Fatal("pod names must survive shell heredoc expansion")
	}
	if !strings.Contains(platform, `values: ["$NODE_NAME"]`) {
		t.Fatal("node substitution was lost")
	}
	subd := scripts["subd"]
	if !strings.Contains(subd, "systemctl start spire-agent.service") || strings.Contains(subd, "systemctl restart spire-agent.service") {
		t.Fatal("Subd bootstrap must preserve running agent")
	}
	validate := scripts["validate"]
	if !strings.Contains(validate, "checking Subd writer identity on host") || !strings.Contains(validate, "checking Subd reader identity on host") {
		t.Fatal("probe target is not labelled")
	}
}
