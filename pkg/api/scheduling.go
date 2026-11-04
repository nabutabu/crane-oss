package api

import (
	"errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"strings"
)

const ProvisioningTaint = "platform.crane.io/provisioning"

// SchedulingLabels is shared by advertised templates and worker registration.
func SchedulingLabels(t SchedulingTemplate, pool string) map[string]string {
	labels := map[string]string{"kubernetes.io/os": "linux", "kubernetes.io/arch": t.Architecture,
		"topology.kubernetes.io/region": t.Region, "topology.kubernetes.io/zone": t.Zone,
		"node.kubernetes.io/instance-type": t.InstanceType, "platform.crane.io/node-pool": pool}
	for k, v := range t.Labels {
		labels[k] = v
	}
	return labels
}

func ValidateScheduling(t SchedulingTemplate, pool string) error {
	invalid := errors.New("invalid scheduling metadata")
	if t.InstanceType == "" || t.Region == "" || t.Zone == "" || pool == "" || (t.Architecture != "amd64" && t.Architecture != "arm64") {
		return invalid
	}
	canonical := SchedulingLabels(SchedulingTemplate{Architecture: t.Architecture, InstanceType: t.InstanceType, Region: t.Region, Zone: t.Zone}, pool)
	for k, v := range SchedulingLabels(t, pool) {
		if len(validation.IsQualifiedName(k)) != 0 || len(validation.IsValidLabelValue(v)) != 0 || v == "" {
			return invalid
		}
		if expected, ok := canonical[k]; ok && v != expected {
			return invalid
		}
		if _, ok := canonical[k]; !ok && (strings.HasPrefix(k, "platform.crane.io/") || strings.Contains(k, "node-restriction.kubernetes.io/")) {
			return invalid
		}
	}
	seen := map[string]bool{}
	for _, taint := range t.Taints {
		key := taint.Key + "/" + taint.Effect
		if taint.Key == ProvisioningTaint || seen[key] || len(validation.IsQualifiedName(taint.Key)) != 0 || len(validation.IsValidLabelValue(taint.Value)) != 0 || (taint.Effect != "NoSchedule" && taint.Effect != "NoExecute" && taint.Effect != "PreferNoSchedule") {
			return invalid
		}
		seen[key] = true
	}
	return nil
}
