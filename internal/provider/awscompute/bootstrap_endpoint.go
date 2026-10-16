package awscompute

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

func bundleParameter(cfg bootstrapConfig) string { return "/crane/" + cfg.Name + "/spire-bundle" }
func bundleParameterARN(cfg bootstrapConfig) string {
	return "arn:aws:ssm:" + cfg.AWS.Region + ":" + cfg.AWS.AccountID + ":parameter" + bundleParameter(cfg)
}

// ELB names are limited to 32 characters. Include a hash so long deployment
// names cannot alias each other after truncation.
func endpointName(name string) string {
	h := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%s-spire-%x", name[:min(len(name), 16)], h[:4])
}

func checkEndpointOwner(ctx context.Context, aws AWSClient, arn, name string) error {
	var out struct{ TagDescriptions []struct{ Tags []tag } }
	if err := aws.Call(ctx, "elbv2", "describe-tags", map[string]any{"ResourceArns": []string{arn}}, &out); err != nil {
		return err
	}
	if len(out.TagDescriptions) != 1 || !owned(out.TagDescriptions[0].Tags, name) {
		return fmt.Errorf("SPIRE load balancer resource is not owned by deployment")
	}
	return nil
}

// ensureSpireEndpoint creates the endpoint before image generation. TLS passes
// through to SPIRE; the NLB never holds the SPIRE CA keys or terminates mTLS.
func ensureSpireEndpoint(ctx context.Context, aws AWSClient, cfg bootstrapConfig, infra Infrastructure) (Infrastructure, error) {
	type lb struct {
		LoadBalancerArn, DNSName, Scheme, Type, VpcId string
		SecurityGroups                                []string
		AvailabilityZones                             []struct{ SubnetId string }
	}
	var lbs struct{ LoadBalancers []lb }
	name := endpointName(cfg.Name)
	err := aws.Call(ctx, "elbv2", "describe-load-balancers", map[string]any{"Names": []string{name}}, &lbs)
	if err != nil && !isCode(err, "LoadBalancerNotFound") {
		return infra, err
	}
	if isCode(err, "LoadBalancerNotFound") {
		err = aws.Call(ctx, "elbv2", "create-load-balancer", map[string]any{
			"Name": name, "Type": "network", "Scheme": "internal", "IpAddressType": "ipv4",
			"Subnets": infra.PrivateSubnetIDs, "SecurityGroups": []string{infra.SpireLoadBalancerSecurityGroupID},
			"Tags": resourceTags(cfg.Name, "spire-nlb"),
		}, &lbs)
		if err != nil {
			return infra, err
		}
	}
	if len(lbs.LoadBalancers) != 1 {
		return infra, fmt.Errorf("expected one SPIRE load balancer")
	}
	l := lbs.LoadBalancers[0]
	if l.LoadBalancerArn == "" || l.DNSName == "" || l.Scheme != "internal" || l.Type != "network" || l.VpcId != infra.VPCID || !sameStrings(l.SecurityGroups, []string{infra.SpireLoadBalancerSecurityGroupID}) {
		return infra, fmt.Errorf("SPIRE load balancer has incompatible network or security settings")
	}
	var subnets []string
	for _, zone := range l.AvailabilityZones {
		subnets = append(subnets, zone.SubnetId)
	}
	if !sameStrings(subnets, infra.PrivateSubnetIDs) {
		return infra, fmt.Errorf("SPIRE load balancer has incompatible subnets")
	}
	if err := checkEndpointOwner(ctx, aws, l.LoadBalancerArn, cfg.Name); err != nil {
		return infra, err
	}
	infra.SpireAddress, infra.SpireLoadBalancerARN = l.DNSName, l.LoadBalancerArn
	if err := aws.Call(ctx, "elbv2", "modify-load-balancer-attributes", map[string]any{"LoadBalancerArn": l.LoadBalancerArn, "Attributes": []map[string]string{{"Key": "load_balancing.cross_zone.enabled", "Value": "true"}}}, nil); err != nil {
		return infra, err
	}

	type group struct {
		TargetGroupArn, VpcId, Protocol, TargetType, HealthCheckProtocol, HealthCheckPort string
		Port                                                                              int
	}
	var groups struct{ TargetGroups []group }
	err = aws.Call(ctx, "elbv2", "describe-target-groups", map[string]any{"Names": []string{name}}, &groups)
	if err != nil && !isCode(err, "TargetGroupNotFound") {
		return infra, err
	}
	if isCode(err, "TargetGroupNotFound") {
		err = aws.Call(ctx, "elbv2", "create-target-group", map[string]any{"Name": name, "VpcId": infra.VPCID, "Protocol": "TCP", "Port": 8081, "TargetType": "instance", "HealthCheckProtocol": "TCP", "HealthCheckPort": "8081", "Tags": resourceTags(cfg.Name, "spire-targets")}, &groups)
		if err != nil {
			return infra, err
		}
	}
	if len(groups.TargetGroups) != 1 {
		return infra, fmt.Errorf("expected one SPIRE target group")
	}
	tg := groups.TargetGroups[0]
	if tg.TargetGroupArn == "" || tg.VpcId != infra.VPCID || tg.Protocol != "TCP" || tg.Port != 8081 || tg.TargetType != "instance" || tg.HealthCheckProtocol != "TCP" || tg.HealthCheckPort != "8081" {
		return infra, fmt.Errorf("SPIRE target group has incompatible settings")
	}
	if err := checkEndpointOwner(ctx, aws, tg.TargetGroupArn, cfg.Name); err != nil {
		return infra, err
	}
	infra.SpireTargetGroupARN = tg.TargetGroupArn
	var listeners struct {
		Listeners []struct {
			Protocol       string
			Port           int
			DefaultActions []struct{ Type, TargetGroupArn string }
		}
	}
	if err := aws.Call(ctx, "elbv2", "describe-listeners", map[string]any{"LoadBalancerArn": l.LoadBalancerArn}, &listeners); err != nil {
		return infra, err
	}
	if len(listeners.Listeners) == 0 {
		err = aws.Call(ctx, "elbv2", "create-listener", map[string]any{"LoadBalancerArn": l.LoadBalancerArn, "Protocol": "TCP", "Port": 8081, "DefaultActions": []map[string]string{{"Type": "forward", "TargetGroupArn": tg.TargetGroupArn}}}, nil)
		if err != nil {
			return infra, err
		}
	} else {
		if len(listeners.Listeners) != 1 {
			return infra, fmt.Errorf("unexpected SPIRE listeners")
		}
		x := listeners.Listeners[0]
		if x.Protocol != "TCP" || x.Port != 8081 || len(x.DefaultActions) != 1 || x.DefaultActions[0].Type != "forward" || x.DefaultActions[0].TargetGroupArn != tg.TargetGroupArn {
			return infra, fmt.Errorf("incompatible SPIRE listener")
		}
	}
	return infra, nil
}

func registerSpireTarget(ctx context.Context, aws AWSClient, infra Infrastructure, instance string) error {
	// Refuse to mix a fresh SPIRE authority with an older backend. Replacing a
	// server requires restoring its datastore/keys and explicitly draining the
	// old target; a bootstrap retry must never silently mix trust authorities.
	var out struct {
		TargetHealthDescriptions []struct {
			Target struct {
				Id   string
				Port int
			}
		}
	}
	if err := aws.Call(ctx, "elbv2", "describe-target-health", map[string]any{"TargetGroupArn": infra.SpireTargetGroupARN}, &out); err != nil {
		return err
	}
	for _, t := range out.TargetHealthDescriptions {
		if t.Target.Id != instance || t.Target.Port != 8081 {
			return fmt.Errorf("SPIRE target group already contains another backend; restore the authority and reconcile targets explicitly")
		}
	}
	return aws.Call(ctx, "elbv2", "register-targets", map[string]any{"TargetGroupArn": infra.SpireTargetGroupARN, "Targets": []map[string]any{{"Id": instance, "Port": 8081}}}, nil)
}

func validateGoldenImage(ctx context.Context, aws AWSClient, cfg bootstrapConfig, infra Infrastructure, ami string) error {
	var out struct {
		Images []struct {
			State, Architecture string
			Tags                []tag
		}
	}
	if err := aws.Call(ctx, "ec2", "describe-images", map[string]any{"ImageIds": []string{ami}}, &out); err != nil {
		return err
	}
	if len(out.Images) != 1 || out.Images[0].State != "available" || out.Images[0].Architecture != "x86_64" {
		return fmt.Errorf("bootstrap requires an available amd64 golden AMI")
	}
	tags := map[string]string{}
	for _, t := range out.Images[0].Tags {
		tags[t.Key] = t.Value
	}
	for k, v := range map[string]string{"CraneImage": "v2", "SpireServerAddress": infra.SpireAddress, "SpireTrustDomain": cfg.SPIRE.TrustDomain, "SpireBundleParameter": bundleParameter(cfg), "SpireVersion": cfg.SPIRE.Version, "KubernetesVersion": cfg.Platform.KubernetesVersion, "AWSRegion": cfg.AWS.Region} {
		if strings.TrimSpace(tags[k]) != v {
			return fmt.Errorf("golden AMI metadata %s does not match deployment; rebuild with the prepared endpoint/configuration", k)
		}
	}
	return nil
}
