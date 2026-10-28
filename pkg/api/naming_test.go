package api

import "testing"

func TestNodeGroupNaming(t *testing.T) {
	n := NodeGroupNaming{Provider: "aws", DevelopmentStage: "production", Region: "us-west-2", AZ: "a", Group: "general-cluster-west"}
	want := "aws-production-us-west-2-a-general-cluster-west"
	if id, err := FormatNodeGroupID(n); err != nil || id != want {
		t.Fatalf("%s %v", id, err)
	}
	for _, edit := range []func(*NodeGroupNaming){
		func(n *NodeGroupNaming) { n.Provider = "" }, func(n *NodeGroupNaming) { n.DevelopmentStage = "" },
		func(n *NodeGroupNaming) { n.Region = "" }, func(n *NodeGroupNaming) { n.AZ = "" }, func(n *NodeGroupNaming) { n.Group = "" },
		func(n *NodeGroupNaming) { n.Group = "Upper" }, func(n *NodeGroupNaming) { n.Group = "-edge" },
		func(n *NodeGroupNaming) { n.Group = "edge-" }, func(n *NodeGroupNaming) { n.Group = "with_space" },
	} {
		bad := n
		edit(&bad)
		if _, err := FormatNodeGroupID(bad); err == nil {
			t.Fatalf("accepted %#v", bad)
		}
	}
	g := NodeGroupInput{Naming: n}
	if err := g.NormalizeID(); err != nil || g.ID != want {
		t.Fatal(g.ID, err)
	}
	g.TemplateRevision = "new-image"
	if err := g.NormalizeID(); err != nil || g.ID != want {
		t.Fatal("renamed on template revision", err)
	}
	g.ID = "another-id"
	if g.NormalizeID() == nil {
		t.Fatal("accepted mismatched ID")
	}
}
