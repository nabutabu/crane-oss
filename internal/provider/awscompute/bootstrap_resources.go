package awscompute

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// EnsureInfrastructure creates/reuses dedicated AWS resources for the bootstrap
func EnsureInfrastructure(ctx context.Context, aws AWSClient, cfg bootstrapConfig) (Infrastructure, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return Infrastructure{}, err
	}

	infra := Infrastructure{}
	var err error
	if infra, err = ensureSecrets(ctx, aws, cfg, infra); err != nil {
		return infra, fmt.Errorf("ensure runtime secret: %w", err)
	}
	return ensureComputeInfrastructure(ctx, aws, cfg, infra)
}

func ensureComputeInfrastructure(ctx context.Context, aws AWSClient, cfg bootstrapConfig, stored Infrastructure) (Infrastructure, error) {
	infra, err := ensureNetwork(ctx, aws, cfg)
	if err != nil {
		return infra, fmt.Errorf("ensure network: %w", err)
	}
	infra.RuntimeSecretARN = stored.RuntimeSecretARN

	// Stage 2: Security groups
	if infra, err = ensureSecurityGroups(ctx, aws, cfg, infra); err != nil {
		return infra, fmt.Errorf("ensure security groups: %w", err)
	}

	if infra, err = ensureSpireEndpoint(ctx, aws, cfg, infra); err != nil {
		return infra, fmt.Errorf("ensure SPIRE endpoint: %w", err)
	}

	// Stage 3: RDS
	if infra, err = ensureRDS(ctx, aws, cfg, infra); err != nil {
		return infra, fmt.Errorf("ensure rds: %w", err)
	}

	// Stage 5: IAM roles/profiles
	if infra, err = ensureIAM(ctx, aws, cfg, infra); err != nil {
		return infra, fmt.Errorf("ensure iam: %w", err)
	}

	return infra, nil
}

type tag struct{ Key, Value string }

func owned(tags []tag, name string) bool {
	for _, t := range tags {
		if t.Key == "CraneBootstrap" && t.Value == name {
			return true
		}
	}
	return false
}
func resourceTags(name, kind string) []tag {
	return []tag{{"CraneBootstrap", name}, {"Name", name + "-" + kind}}
}
func tagSpec(name, kind, resourceType string) []map[string]any {
	return []map[string]any{{"ResourceType": resourceType, "Tags": resourceTags(name, kind)}}
}
func filter(key string, values ...string) map[string]any {
	return map[string]any{"Name": key, "Values": values}
}
func isCode(err error, codes ...string) bool {
	var e *AWSError
	if !errors.As(err, &e) {
		return false
	}
	for _, code := range codes {
		if e.Code == code {
			return true
		}
	}
	return false
}

func ensureNetwork(ctx context.Context, aws AWSClient, cfg bootstrapConfig) (Infrastructure, error) {
	infra := Infrastructure{}
	var az struct {
		AvailabilityZones []struct{ ZoneName, State, ZoneType string }
	}
	if err := aws.Call(ctx, "ec2", "describe-availability-zones", map[string]any{"Filters": []any{filter("state", "available"), filter("zone-type", "availability-zone")}}, &az); err != nil {
		return infra, err
	}
	var available []string
	for _, z := range az.AvailabilityZones {
		if z.State == "available" {
			available = append(available, z.ZoneName)
		}
	}
	sort.Strings(available)
	if len(cfg.Network.AvailabilityZones) > 0 {
		infra.AvailabilityZones = cfg.Network.AvailabilityZones
		for _, zone := range infra.AvailabilityZones {
			found := false
			for _, candidate := range available {
				if zone == candidate {
					found = true
				}
			}
			if !found {
				return infra, fmt.Errorf("availability zone %s is not available in region %s", zone, cfg.AWS.Region)
			}
		}
	} else {
		if len(available) < 2 {
			return infra, fmt.Errorf("bootstrap requires two available AWS availability zones")
		}
		infra.AvailabilityZones = available[:2]
	}
	type vpc struct {
		VpcID            string `json:"VpcId"`
		CidrBlock, State string
		Tags             []tag
	}
	var vpcs struct{ Vpcs []vpc }
	if err := aws.Call(ctx, "ec2", "describe-vpcs", map[string]any{"Filters": []any{filter("tag:CraneBootstrap", cfg.Name)}}, &vpcs); err != nil {
		return infra, err
	}
	if len(vpcs.Vpcs) > 1 {
		return infra, fmt.Errorf("multiple VPCs belong to deployment %s", cfg.Name)
	}
	if len(vpcs.Vpcs) == 1 {
		v := vpcs.Vpcs[0]
		if v.CidrBlock != cfg.Network.VPCCIDR || !owned(v.Tags, cfg.Name) {
			return infra, fmt.Errorf("existing deployment VPC does not match configured CIDR/ownership")
		}
		infra.VPCID = v.VpcID
	} else {
		var out struct{ Vpc vpc }
		if err := aws.Call(ctx, "ec2", "create-vpc", map[string]any{"CidrBlock": cfg.Network.VPCCIDR, "TagSpecifications": tagSpec(cfg.Name, "vpc", "vpc")}, &out); err != nil {
			return infra, err
		}
		infra.VPCID = out.Vpc.VpcID
	}
	if infra.VPCID == "" {
		return infra, fmt.Errorf("AWS returned no VPC ID")
	}
	for {
		var out struct{ Vpcs []vpc }
		if err := aws.Call(ctx, "ec2", "describe-vpcs", map[string]any{"VpcIds": []string{infra.VPCID}}, &out); err != nil {
			return infra, err
		}
		if len(out.Vpcs) == 1 && out.Vpcs[0].State == "available" {
			break
		}
		if err := pause(ctx, 2*time.Second); err != nil {
			return infra, err
		}
	}
	for _, attribute := range []string{"EnableDnsSupport", "EnableDnsHostnames"} {
		if err := aws.Call(ctx, "ec2", "modify-vpc-attribute", map[string]any{"VpcId": infra.VPCID, attribute: map[string]bool{"Value": true}}, nil); err != nil {
			return infra, err
		}
	}
	for i := range 2 {
		for _, public := range []bool{true, false} {
			cidr := cfg.Network.PrivateSubnetCIDRs[i]
			kind := fmt.Sprintf("private-%d", i+1)
			if public {
				cidr = cfg.Network.PublicSubnetCIDRs[i]
				kind = fmt.Sprintf("public-%d", i+1)
			}
			id, err := ensureSubnet(ctx, aws, cfg, infra.VPCID, cidr, infra.AvailabilityZones[i], kind, public)
			if err != nil {
				return infra, err
			}
			if public {
				infra.PublicSubnetIDs = append(infra.PublicSubnetIDs, id)
			} else {
				infra.PrivateSubnetIDs = append(infra.PrivateSubnetIDs, id)
			}
		}
	}
	gateway, err := ensureGateway(ctx, aws, cfg, infra.VPCID)
	if err != nil {
		return infra, err
	}
	if err = ensureRoutes(ctx, aws, cfg, infra.VPCID, "public", gateway, infra.PublicSubnetIDs); err != nil {
		return infra, err
	}
	if err = ensureRoutes(ctx, aws, cfg, infra.VPCID, "private", "", infra.PrivateSubnetIDs); err != nil {
		return infra, err
	}
	return infra, nil
}

func ensureSubnet(ctx context.Context, aws AWSClient, cfg bootstrapConfig, vpc, cidr, zone, kind string, public bool) (string, error) {
	type subnet struct {
		SubnetID                           string `json:"SubnetId"`
		AvailabilityZone, CidrBlock, State string
		Tags                               []tag
	}
	var out struct{ Subnets []subnet }
	if err := aws.Call(ctx, "ec2", "describe-subnets", map[string]any{"Filters": []any{filter("vpc-id", vpc), filter("cidr-block", cidr)}}, &out); err != nil {
		return "", err
	}
	if len(out.Subnets) > 1 {
		return "", fmt.Errorf("multiple subnets match %s", cidr)
	}
	id := ""
	if len(out.Subnets) == 1 {
		s := out.Subnets[0]
		if s.AvailabilityZone != zone || !owned(s.Tags, cfg.Name) {
			return "", fmt.Errorf("subnet %s has incompatible availability zone or ownership", cidr)
		}
		id = s.SubnetID
	} else {
		var created struct{ Subnet subnet }
		if err := aws.Call(ctx, "ec2", "create-subnet", map[string]any{"VpcId": vpc, "CidrBlock": cidr, "AvailabilityZone": zone, "TagSpecifications": tagSpec(cfg.Name, kind, "subnet")}, &created); err != nil {
			return "", err
		}
		id = created.Subnet.SubnetID
	}
	if id == "" {
		return "", fmt.Errorf("AWS returned no subnet ID")
	}
	if err := aws.Call(ctx, "ec2", "modify-subnet-attribute", map[string]any{"SubnetId": id, "MapPublicIpOnLaunch": map[string]bool{"Value": public}}, nil); err != nil {
		return "", err
	}
	return id, nil
}

func ensureGateway(ctx context.Context, aws AWSClient, cfg bootstrapConfig, vpc string) (string, error) {
	type gateway struct {
		InternetGatewayID string `json:"InternetGatewayId"`
		Attachments       []struct {
			VpcID string `json:"VpcId"`
		}
		Tags []tag
	}
	var out struct{ InternetGateways []gateway }
	if err := aws.Call(ctx, "ec2", "describe-internet-gateways", map[string]any{"Filters": []any{filter("tag:CraneBootstrap", cfg.Name)}}, &out); err != nil {
		return "", err
	}
	if len(out.InternetGateways) > 1 {
		return "", fmt.Errorf("multiple internet gateways belong to deployment")
	}
	var g gateway
	if len(out.InternetGateways) == 1 {
		g = out.InternetGateways[0]
	} else {
		var created struct{ InternetGateway gateway }
		if err := aws.Call(ctx, "ec2", "create-internet-gateway", map[string]any{"TagSpecifications": tagSpec(cfg.Name, "gateway", "internet-gateway")}, &created); err != nil {
			return "", err
		}
		g = created.InternetGateway
	}
	if g.InternetGatewayID == "" {
		return "", fmt.Errorf("AWS returned no internet gateway ID")
	}
	if len(g.Attachments) > 0 {
		if len(g.Attachments) != 1 || g.Attachments[0].VpcID != vpc {
			return "", fmt.Errorf("deployment gateway is attached to a different VPC")
		}
	} else {
		if err := aws.Call(ctx, "ec2", "attach-internet-gateway", map[string]any{"VpcId": vpc, "InternetGatewayId": g.InternetGatewayID}, nil); err != nil {
			return "", err
		}
	}
	return g.InternetGatewayID, nil
}

func ensureRoutes(ctx context.Context, aws AWSClient, cfg bootstrapConfig, vpc, kind, gateway string, subnets []string) error {
	type route struct {
		DestinationCidrBlock string
		GatewayID            string `json:"GatewayId"`
	}
	type table struct {
		RouteTableID string `json:"RouteTableId"`
		Routes       []route
		Associations []struct {
			SubnetID string `json:"SubnetId"`
		}
		Tags []tag
	}
	var out struct {
		RouteTables []struct {
			RouteTableID string `json:"RouteTableId"`
			Routes       []route
			Associations []struct {
				SubnetID string `json:"SubnetId"`
			}
			Tags []tag
		}
	}
	if err := aws.Call(ctx, "ec2", "describe-route-tables", map[string]any{"Filters": []any{filter("vpc-id", vpc), filter("tag:Name", cfg.Name+"-"+kind), filter("tag:CraneBootstrap", cfg.Name)}}, &out); err != nil {
		return err
	}
	if len(out.RouteTables) > 1 {
		return fmt.Errorf("multiple %s route tables belong to deployment", kind)
	}
	id := ""
	associated := map[string]bool{}
	hasDefault := false
	if len(out.RouteTables) == 1 {
		t := out.RouteTables[0]
		id = t.RouteTableID
		for _, r := range t.Routes {
			if r.DestinationCidrBlock == "0.0.0.0/0" {
				if gateway == "" || r.GatewayID != gateway {
					return fmt.Errorf("%s route table has an unexpected default route", kind)
				}
				hasDefault = true
			}
		}
		for _, a := range t.Associations {
			associated[a.SubnetID] = true
		}
	} else {
		var created struct{ RouteTable table }
		if err := aws.Call(ctx, "ec2", "create-route-table", map[string]any{"VpcId": vpc, "TagSpecifications": tagSpec(cfg.Name, kind, "route-table")}, &created); err != nil {
			return err
		}
		id = created.RouteTable.RouteTableID
	}
	if id == "" {
		return fmt.Errorf("AWS returned no route table ID")
	}
	if gateway != "" && !hasDefault {
		if err := aws.Call(ctx, "ec2", "create-route", map[string]any{"RouteTableId": id, "DestinationCidrBlock": "0.0.0.0/0", "GatewayId": gateway}, nil); err != nil {
			return err
		}
	}
	for _, s := range subnets {
		if !associated[s] {
			if err := aws.Call(ctx, "ec2", "associate-route-table", map[string]any{"RouteTableId": id, "SubnetId": s}, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureSecurityGroups(ctx context.Context, aws AWSClient, cfg bootstrapConfig, infra Infrastructure) (Infrastructure, error) {
	type permission struct {
		IpProtocol           string
		FromPort, ToPort     int32
		IpRanges, Ipv6Ranges []any
		PrefixListIDs        []any `json:"PrefixListIds"`
		UserIdGroupPairs     []struct {
			GroupID string `json:"GroupId"`
		}
	}
	ids := map[string]string{}
	existingPermissions := map[string][]permission{}
	for _, kind := range []string{"spire", "spire-nlb", "platform", "rds"} {
		var out struct {
			SecurityGroups []struct {
				GroupID       string `json:"GroupId"`
				Tags          []tag
				IpPermissions []permission
			}
		}
		if err := aws.Call(ctx, "ec2", "describe-security-groups", map[string]any{"Filters": []any{filter("vpc-id", infra.VPCID), filter("group-name", cfg.Name+"-"+kind)}}, &out); err != nil {
			return infra, err
		}
		if len(out.SecurityGroups) > 1 {
			return infra, fmt.Errorf("multiple %s security groups", kind)
		}
		if len(out.SecurityGroups) == 1 {
			g := out.SecurityGroups[0]
			if !owned(g.Tags, cfg.Name) {
				return infra, fmt.Errorf("security group %s is not owned by this bootstrap", kind)
			}
			for _, p := range g.IpPermissions {
				if len(p.IpRanges) > 0 || len(p.Ipv6Ranges) > 0 || len(p.PrefixListIDs) > 0 {
					return infra, fmt.Errorf("security group %s contains unexpected CIDR/prefix-list ingress", kind)
				}
			}
			ids[kind] = g.GroupID
			existingPermissions[kind] = g.IpPermissions
		} else {
			var created struct {
				GroupID string `json:"GroupId"`
			}
			if err := aws.Call(ctx, "ec2", "create-security-group", map[string]any{"GroupName": cfg.Name + "-" + kind, "Description": "Crane protected " + kind, "VpcId": infra.VPCID, "TagSpecifications": tagSpec(cfg.Name, kind, "security-group")}, &created); err != nil {
				return infra, err
			}
			ids[kind] = created.GroupID
		}
		if ids[kind] == "" {
			return infra, fmt.Errorf("AWS returned no %s security group ID", kind)
		}
	}
	infra.SpireLoadBalancerSecurityGroupID = ids["spire-nlb"]
	infra.SpireSecurityGroupID = ids["spire"]
	infra.PlatformSecurityGroupID = ids["platform"]
	infra.RDSSecurityGroupID = ids["rds"]
	for kind, permissions := range existingPermissions {
		for _, p := range permissions {
			for _, source := range p.UserIdGroupPairs {
				allowed := (kind == "spire" && source.GroupID == ids["spire-nlb"] && p.IpProtocol == "tcp" && p.FromPort == 8081 && p.ToPort == 8081) ||
					(kind == "spire-nlb" && source.GroupID == ids["platform"] && p.IpProtocol == "tcp" && p.FromPort == 8081 && p.ToPort == 8081) ||
					(kind == "spire-nlb" && source.GroupID == ids["spire"] && p.IpProtocol == "tcp" && p.FromPort == 8081 && p.ToPort == 8081) ||
					(kind == "spire" && source.GroupID == ids["platform"] && p.IpProtocol == "tcp" && p.FromPort == 44001 && p.ToPort == 44001) ||
					(kind == "platform" && source.GroupID == ids["platform"] && p.IpProtocol == "-1") ||
					(kind == "rds" && (source.GroupID == ids["spire"] || source.GroupID == ids["platform"]) && p.IpProtocol == "tcp" && p.FromPort == cfg.RDS.Port && p.ToPort == cfg.RDS.Port)
				if !allowed {
					return infra, fmt.Errorf("security group %s has an unexpected source/protocol/port rule", kind)
				}
			}
		}
	}
	for _, rule := range []struct {
		target, source, protocol string
		port                     int32
	}{{ids["spire"], ids["spire-nlb"], "tcp", 8081}, {ids["spire-nlb"], ids["platform"], "tcp", 8081}, {ids["spire-nlb"], ids["spire"], "tcp", 8081}, {ids["spire"], ids["platform"], "tcp", 44001}, {ids["platform"], ids["platform"], "-1", 0}, {ids["rds"], ids["spire"], "tcp", cfg.RDS.Port}, {ids["rds"], ids["platform"], "tcp", cfg.RDS.Port}} {
		p := map[string]any{"IpProtocol": rule.protocol, "UserIdGroupPairs": []map[string]string{{"GroupId": rule.source}}}
		if rule.protocol != "-1" {
			p["FromPort"] = rule.port
			p["ToPort"] = rule.port
		}
		if err := aws.Call(ctx, "ec2", "authorize-security-group-ingress", map[string]any{"GroupId": rule.target, "IpPermissions": []any{p}}, nil); err != nil && !isCode(err, "InvalidPermission.Duplicate") {
			return infra, err
		}
	}
	return infra, nil
}

func ensureRDS(ctx context.Context, aws AWSClient, cfg bootstrapConfig, infra Infrastructure) (Infrastructure, error) {
	name := cfg.Name + "-db-subnets"
	// AWS uses VpcId and DBSubnetGroupName as distinct JSON members.
	var groups struct {
		DBSubnetGroups []struct {
			DBSubnetGroupName string
			VpcID             string `json:"VpcId"`
			Subnets           []struct{ SubnetIdentifier string }
		}
	}
	err := aws.Call(ctx, "rds", "describe-db-subnet-groups", map[string]any{"DBSubnetGroupName": name}, &groups)
	if err != nil && !isCode(err, "DBSubnetGroupNotFoundFault", "DBSubnetGroupNotFound") {
		return infra, err
	}
	if err == nil {
		if len(groups.DBSubnetGroups) != 1 {
			return infra, fmt.Errorf("RDS returned unexpected subnet group count")
		}
		g := groups.DBSubnetGroups[0]
		var ids []string
		for _, s := range g.Subnets {
			ids = append(ids, s.SubnetIdentifier)
		}
		if g.VpcID != infra.VPCID || !sameStrings(ids, infra.PrivateSubnetIDs) {
			return infra, fmt.Errorf("existing RDS subnet group has different VPC/subnets")
		}
	} else {
		if err = aws.Call(ctx, "rds", "create-db-subnet-group", map[string]any{"DBSubnetGroupName": name, "DBSubnetGroupDescription": "Crane private PostgreSQL subnets", "SubnetIds": infra.PrivateSubnetIDs, "Tags": resourceTags(cfg.Name, "db-subnets")}, nil); err != nil {
			return infra, err
		}
	}
	type instance struct {
		DBInstanceIdentifier, DBInstanceArn, DBInstanceStatus, Engine, EngineVersion, DBInstanceClass, MasterUsername, StorageType string
		AllocatedStorage                                                                                                           int32
		MultiAZ, StorageEncrypted, PubliclyAccessible, DeletionProtection                                                          bool
		Endpoint                                                                                                                   *struct {
			Address string
			Port    int32
		}
		MasterUserSecret *struct{ SecretArn, SecretStatus string }
		DBSubnetGroup    struct {
			DBSubnetGroupName string
			VpcID             string `json:"VpcId"`
		}
		VpcSecurityGroups []struct {
			VpcSecurityGroupID string `json:"VpcSecurityGroupId"`
		}
	}
	var out struct{ DBInstances []instance }
	err = aws.Call(ctx, "rds", "describe-db-instances", map[string]any{"DBInstanceIdentifier": cfg.RDS.Identifier}, &out)
	if err != nil && !isCode(err, "DBInstanceNotFound", "DBInstanceNotFoundFault") {
		return infra, err
	}
	if err != nil {
		input := map[string]any{"DBInstanceIdentifier": cfg.RDS.Identifier, "DBInstanceClass": cfg.RDS.InstanceClass, "Engine": "postgres", "MasterUsername": cfg.RDS.MasterUsername, "ManageMasterUserPassword": true, "AllocatedStorage": cfg.RDS.StorageGiB, "StorageType": cfg.RDS.StorageType, "StorageEncrypted": true, "DeletionProtection": true, "PubliclyAccessible": false, "MultiAZ": cfg.RDS.MultiAZ, "Port": cfg.RDS.Port, "DBSubnetGroupName": name, "VpcSecurityGroupIds": []string{infra.RDSSecurityGroupID}, "BackupRetentionPeriod": 7, "Tags": resourceTags(cfg.Name, "db")}
		if cfg.RDS.EngineVersion != "" {
			input["EngineVersion"] = cfg.RDS.EngineVersion
		}
		if err = aws.Call(ctx, "rds", "create-db-instance", input, nil); err != nil {
			return infra, err
		}
	}
	for {
		if err = aws.Call(ctx, "rds", "describe-db-instances", map[string]any{"DBInstanceIdentifier": cfg.RDS.Identifier}, &out); err != nil {
			return infra, err
		}
		if len(out.DBInstances) != 1 {
			return infra, fmt.Errorf("RDS returned unexpected instance count")
		}
		db := out.DBInstances[0]
		if db.Engine != "postgres" || db.DBInstanceClass != cfg.RDS.InstanceClass || db.MasterUsername != cfg.RDS.MasterUsername || db.AllocatedStorage < cfg.RDS.StorageGiB || db.StorageType != cfg.RDS.StorageType || db.MultiAZ != cfg.RDS.MultiAZ || !db.StorageEncrypted || db.PubliclyAccessible || !db.DeletionProtection || db.DBSubnetGroup.DBSubnetGroupName != name || db.DBSubnetGroup.VpcID != infra.VPCID {
			return infra, fmt.Errorf("existing RDS instance does not match protected deployment configuration")
		}
		if cfg.RDS.EngineVersion != "" && db.EngineVersion != cfg.RDS.EngineVersion {
			return infra, fmt.Errorf("existing RDS engine version differs from configuration")
		}
		var securityIDs []string
		for _, sg := range db.VpcSecurityGroups {
			securityIDs = append(securityIDs, sg.VpcSecurityGroupID)
		}
		if !sameStrings(securityIDs, []string{infra.RDSSecurityGroupID}) {
			return infra, fmt.Errorf("RDS instance has unexpected security groups")
		}
		var tags struct{ TagList []tag }
		if err = aws.Call(ctx, "rds", "list-tags-for-resource", map[string]any{"ResourceName": db.DBInstanceArn}, &tags); err != nil {
			return infra, err
		}
		if !owned(tags.TagList, cfg.Name) {
			return infra, fmt.Errorf("RDS instance is not owned by this deployment")
		}
		if db.DBInstanceStatus == "available" && db.Endpoint != nil && db.MasterUserSecret != nil && db.MasterUserSecret.SecretArn != "" && db.MasterUserSecret.SecretStatus == "active" {
			if db.Endpoint.Port != cfg.RDS.Port {
				return infra, fmt.Errorf("RDS port does not match configuration")
			}
			infra.DBEndpoint = db.Endpoint.Address
			infra.DBSecretARN = db.MasterUserSecret.SecretArn
			return infra, nil
		}
		if strings.Contains(db.DBInstanceStatus, "failed") || strings.HasPrefix(db.DBInstanceStatus, "incompatible") || db.DBInstanceStatus == "storage-full" || db.DBInstanceStatus == "deleting" {
			return infra, fmt.Errorf("RDS entered unusable state %s", db.DBInstanceStatus)
		}
		if err = pause(ctx, 15*time.Second); err != nil {
			return infra, err
		}
	}
}

func sameStrings(a, b []string) bool {
	aa := append([]string{}, a...)
	bb := append([]string{}, b...)
	sort.Strings(aa)
	sort.Strings(bb)
	return strings.Join(aa, "\x00") == strings.Join(bb, "\x00")
}

func ensureSecrets(ctx context.Context, aws AWSClient, cfg bootstrapConfig, infra Infrastructure) (Infrastructure, error) {
	secretName := cfg.Name + "/runtime"
	var desc struct {
		ARN         string
		Tags        []tag
		DeletedDate any
	}
	err := aws.Call(ctx, "secretsmanager", "describe-secret", map[string]any{"SecretId": secretName}, &desc)
	if err != nil {
		if !isCode(err, "ResourceNotFoundException") {
			return infra, err
		}
		values := map[string]string{"spire_username": "spire", "crane_username": "crane"}
		for _, key := range []string{"spire_password", "crane_password", "admin_token", "adapter_token"} {
			b := make([]byte, 32)
			if _, err = rand.Read(b); err != nil {
				return infra, err
			}
			values[key] = base64.RawURLEncoding.EncodeToString(b)
		}
		data, _ := json.Marshal(values)
		var created struct{ ARN string }
		if err = aws.Call(ctx, "secretsmanager", "create-secret", map[string]any{"Name": secretName, "SecretString": string(data), "Tags": resourceTags(cfg.Name, "runtime")}, &created); err != nil {
			return infra, err
		}
		infra.RuntimeSecretARN = created.ARN
	} else {
		if !owned(desc.Tags, cfg.Name) || desc.DeletedDate != nil {
			return infra, fmt.Errorf("runtime secret has incompatible ownership or is scheduled for deletion")
		}
		var value struct{ SecretString string }
		if err = aws.Call(ctx, "secretsmanager", "get-secret-value", map[string]any{"SecretId": desc.ARN}, &value); err != nil {
			return infra, err
		}
		var values map[string]string
		if err = json.Unmarshal([]byte(value.SecretString), &values); err != nil {
			return infra, fmt.Errorf("runtime secret is not valid JSON")
		}
		for _, key := range []string{"spire_username", "spire_password", "crane_username", "crane_password", "admin_token", "adapter_token"} {
			if values[key] == "" {
				return infra, fmt.Errorf("runtime secret is missing required field %s", key)
			}
		}
		if values["spire_username"] != "spire" || values["crane_username"] != "crane" {
			return infra, fmt.Errorf("runtime secret database users differ from deployment")
		}
		for _, key := range []string{"spire_password", "crane_password", "admin_token", "adapter_token"} {
			if !regexp.MustCompile(`^[A-Za-z0-9_-]{43,}$`).MatchString(values[key]) {
				return infra, fmt.Errorf("runtime secret field %s has an unexpected credential format", key)
			}
		}
		infra.RuntimeSecretARN = desc.ARN
	}
	if infra.RuntimeSecretARN == "" {
		return infra, fmt.Errorf("AWS returned no runtime secret ARN")
	}
	return infra, nil
}

func ensureIAM(ctx context.Context, aws AWSClient, cfg bootstrapConfig, infra Infrastructure) (Infrastructure, error) {
	for _, kind := range []string{"spire", "platform"} {
		name := cfg.Name + "-" + kind
		trust := map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Principal": map[string]string{"Service": "ec2.amazonaws.com"}, "Action": "sts:AssumeRole"}}}
		trustJSON, _ := json.Marshal(trust)
		var out struct {
			Role struct {
				RoleName string
				Tags     []tag
			}
		}
		err := aws.Call(ctx, "iam", "get-role", map[string]any{"RoleName": name}, &out)
		if err != nil {
			if !isCode(err, "NoSuchEntity") {
				return infra, err
			}
			if err = aws.Call(ctx, "iam", "create-role", map[string]any{"RoleName": name, "AssumeRolePolicyDocument": string(trustJSON), "Tags": resourceTags(cfg.Name, kind)}, nil); err != nil {
				return infra, err
			}
		} else {
			if !owned(out.Role.Tags, cfg.Name) {
				return infra, fmt.Errorf("runtime IAM role %s is not owned by deployment", name)
			}
			if err = aws.Call(ctx, "iam", "update-assume-role-policy", map[string]any{"RoleName": name, "PolicyDocument": string(trustJSON)}, nil); err != nil {
				return infra, err
			}
		}
		statements := []any{
			map[string]any{"Effect": "Allow", "Action": "ssm:GetParameter", "Resource": bundleParameterARN(cfg)},
			map[string]any{"Effect": "Allow", "Action": "secretsmanager:GetSecretValue", "Resource": []string{infra.RuntimeSecretARN}},
			map[string]any{"Effect": "Allow", "Action": []string{"ec2:DescribeInstances", "ec2:DescribeInstanceStatus"}, "Resource": "*", "Condition": map[string]any{"StringEquals": map[string]string{"aws:RequestedRegion": cfg.AWS.Region}}},
		}
		if kind == "spire" {
			statements = append(statements, map[string]any{"Effect": "Allow", "Action": "secretsmanager:GetSecretValue", "Resource": infra.DBSecretARN}, map[string]any{"Effect": "Allow", "Action": "ssm:PutParameter", "Resource": bundleParameterARN(cfg)})
		} else {
			if cfg.Observability.NewRelicSecretARN != "" {
				statements = append(statements, map[string]any{"Effect": "Allow", "Action": "secretsmanager:GetSecretValue", "Resource": cfg.Observability.NewRelicSecretARN})
			}
			statements = append(statements,
				map[string]any{"Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*"},
				map[string]any{"Effect": "Allow", "Action": []string{"ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"}, "Resource": "arn:aws:ecr:" + cfg.AWS.Region + ":" + cfg.AWS.AccountID + ":repository/*"},
				map[string]any{"Effect": "Allow", "Action": "ec2:RunInstances", "Resource": "*", "Condition": map[string]any{"StringEquals": map[string]string{"aws:RequestedRegion": cfg.AWS.Region}}},
				map[string]any{"Effect": "Allow", "Action": "ec2:CreateTags", "Resource": "*", "Condition": map[string]any{"StringEquals": map[string]string{"ec2:CreateAction": "RunInstances", "aws:RequestedRegion": cfg.AWS.Region}}},
				map[string]any{"Effect": "Allow", "Action": "ec2:TerminateInstances", "Resource": "arn:aws:ec2:" + cfg.AWS.Region + ":" + cfg.AWS.AccountID + ":instance/*", "Condition": map[string]any{"Null": map[string]string{"ec2:ResourceTag/CraneBootstrap": "true"}}},
				map[string]any{"Effect": "Allow", "Action": "iam:PassRole", "Resource": "arn:aws:iam::" + cfg.AWS.AccountID + ":role/" + name, "Condition": map[string]any{"StringEquals": map[string]string{"iam:PassedToService": "ec2.amazonaws.com"}}})
		}
		policyJSON, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
		if err = aws.Call(ctx, "iam", "put-role-policy", map[string]any{"RoleName": name, "PolicyName": "crane-runtime", "PolicyDocument": string(policyJSON)}, nil); err != nil {
			return infra, err
		}
		if err = aws.Call(ctx, "iam", "attach-role-policy", map[string]any{"RoleName": name, "PolicyArn": "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"}, nil); err != nil {
			return infra, err
		}
		var profile struct {
			InstanceProfile struct {
				Tags  []tag
				Roles []struct{ RoleName string }
			}
		}
		err = aws.Call(ctx, "iam", "get-instance-profile", map[string]any{"InstanceProfileName": name}, &profile)
		if err != nil {
			if !isCode(err, "NoSuchEntity") {
				return infra, err
			}
			if err = aws.Call(ctx, "iam", "create-instance-profile", map[string]any{"InstanceProfileName": name, "Tags": resourceTags(cfg.Name, kind)}, nil); err != nil {
				return infra, err
			}
		} else {
			if !owned(profile.InstanceProfile.Tags, cfg.Name) {
				return infra, fmt.Errorf("IAM profile %s is not owned by deployment", name)
			}
			if len(profile.InstanceProfile.Roles) > 1 || (len(profile.InstanceProfile.Roles) == 1 && profile.InstanceProfile.Roles[0].RoleName != name) {
				return infra, fmt.Errorf("IAM profile has incompatible runtime role")
			}
		}
		if len(profile.InstanceProfile.Roles) == 0 {
			if err = aws.Call(ctx, "iam", "add-role-to-instance-profile", map[string]any{"InstanceProfileName": name, "RoleName": name}, nil); err != nil {
				return infra, err
			}
		}
		if kind == "spire" {
			infra.SpireProfile = name
		} else {
			infra.PlatformProfile = name
		}
	}
	return infra, nil
}
