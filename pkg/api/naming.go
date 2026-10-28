package api

import (
	"fmt"
	"regexp"
	"strings"
)

var namingComponent = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// FormatNodeGroupID uses explicit inputs, never parses an ID for policy.
func FormatNodeGroupID(n NodeGroupNaming) (string, error) {
	parts := []string{n.Provider, n.DevelopmentStage, n.Region, n.AZ, n.Group}
	for _, part := range parts {
		if !namingComponent.MatchString(part) {
			return "", fmt.Errorf("node-group naming requires provider, development_stage, region, az and group using lowercase alphanumeric components with internal hyphens")
		}
	}
	return strings.Join(parts, "-"), nil
}

func (g *NodeGroupInput) NormalizeID() error {
	id, err := FormatNodeGroupID(g.Naming)
	if err != nil {
		return err
	}
	if g.ID != "" && g.ID != id {
		return fmt.Errorf("node-group ID does not match naming inputs")
	}
	g.ID = id
	return nil
}
