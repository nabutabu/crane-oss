package autoscaler

import (
	"context"
	"testing"
)

func TestStaticProvider_TemplateZoneSpecific(t *testing.T) {
	groups := []StaticNodeGroup{
		{
			ID:           "ng-zone-a-p1",
			Zone:         "us-west-2a",
			Region:       "us-west-2",
			Pool:         "pool-1",
			InstanceType: "t3.medium",
			CPU:          2,
			MemoryMB:     4096,
			Labels:       map[string]string{"custom": "value"},
		},
	}
	p := NewStaticProvider(groups)
	tpl, err := p.Template(context.Background(), "ng-zone-a-p1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tpl == nil {
		t.Fatal("expected template")
	}
	if tpl.Zone != "us-west-2a" {
		t.Errorf("zone mismatch: %s", tpl.Zone)
	}
	if tpl.Region != "us-west-2" {
		t.Errorf("region mismatch: %s", tpl.Region)
	}
	if tpl.Labels["topology.kubernetes.io/zone"] != "us-west-2a" {
		t.Errorf("missing topology zone label")
	}
	if tpl.Labels["topology.kubernetes.io/region"] != "us-west-2" {
		t.Errorf("missing topology region label")
	}
	if tpl.Labels["platform.crane.io/node-pool"] != "pool-1" {
		t.Errorf("missing pool label")
	}
	if tpl.Labels["custom"] != "value" {
		t.Errorf("custom label lost")
	}
	if tpl.Labels["node.kubernetes.io/instance-type"] != "t3.medium" {
		t.Errorf("missing instance type label")
	}
}
