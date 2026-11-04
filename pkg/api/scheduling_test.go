package api

import "testing"

func TestSchedulingRejectsForgedAndInvalidLabels(t *testing.T) {
	base := SchedulingTemplate{Architecture: "amd64", Region: "region", Zone: "zone", InstanceType: "machine"}
	if err := ValidateScheduling(base, "general"); err != nil {
		t.Fatal(err)
	}
	for _, labels := range []map[string]string{{"topology.kubernetes.io/zone": "forged"}, {"bad key": "value"}, {"platform.crane.io/owner": "forged"}, {"node-role.kubernetes.io/control-plane": "forged"}, {"workload": "invalid/value"}} {
		in := base
		in.Labels = labels
		if ValidateScheduling(in, "general") == nil {
			t.Fatalf("accepted invalid metadata: %v", labels)
		}
	}
	base.Taints = []SchedulingTaint{{Key: ProvisioningTaint, Effect: "NoSchedule"}}
	if ValidateScheduling(base, "general") == nil {
		t.Fatal("accepted reserved quarantine taint")
	}
}
