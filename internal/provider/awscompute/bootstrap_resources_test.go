package awscompute

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestInfrastructureFirstRunAndRetry(t *testing.T) {
	cfg := testConfig().WithDefaults()
	inventory := map[string]any{}
	creates := 0
	policies := map[string]string{}
	schemaChecked := map[string]bool{}
	aws := &fakeAWS{call: func(service, op string, input any) (any, error) {
		in, _ := input.(map[string]any)
		key := service + ":" + op
		if os.Getenv("CRANE_TEST_AWS_CLI") == "1" && !schemaChecked[key] {
			schemaChecked[key] = true
			data, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "input.json")
			if err = os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("aws", service, op, "--region", cfg.AWS.Region, "--cli-input-json", "file://"+file, "--generate-cli-skeleton", "output", "--no-cli-pager")
			command.Env = append(os.Environ(), "AWS_ACCESS_KEY_ID=test", "AWS_SECRET_ACCESS_KEY=test", "AWS_EC2_METADATA_DISABLED=true", "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off")
			if out, err := command.CombinedOutput(); err != nil {
				// Some AWS CLI versions generate invalid *output* skeletons
				// (e.g. ARN="ARN"). Request validation has already run; only
				// ignore validation errors whose fields are absent from input.
				message := string(out)
				outputOnly := strings.Contains(message, "Parameter validation failed:") && !strings.Contains(message, "Unknown parameter in input")
				matches := regexp.MustCompile(`(?:for parameter|for parameter in) ([A-Za-z0-9_]+)`).FindAllStringSubmatch(message, -1)
				if len(matches) == 0 {
					outputOnly = false
				}
				for _, match := range matches {
					if _, exists := in[match[1]]; exists {
						outputOnly = false
					}
				}
				if !outputOnly {
					t.Fatalf("AWS CLI rejected offline schema for %s: %v\n%s", key, err, out)
				}
				t.Logf("%s: request validated; CLI generated an invalid response skeleton", key)
			}
		}
		tags := resourceTags(cfg.Name, "test")
		kind := func() string {
			for _, raw := range in["Filters"].([]any) {
				f := raw.(map[string]any)
				if f["Name"] == "tag:Name" {
					return strings.TrimPrefix(f["Values"].([]string)[0], cfg.Name+"-")
				}
				if f["Name"] == "group-name" {
					return strings.TrimPrefix(f["Values"].([]string)[0], cfg.Name+"-")
				}
				if f["Name"] == "cidr-block" {
					return f["Values"].([]string)[0]
				}
			}
			return ""
		}
		absent := func(code string) (any, error) { return nil, &AWSError{Code: code} }
		switch key {
		case "ec2:describe-availability-zones":
			return map[string]any{"AvailabilityZones": []any{map[string]string{"ZoneName": "us-west-2a", "State": "available"}, map[string]string{"ZoneName": "us-west-2b", "State": "available"}}}, nil
		case "ec2:describe-vpcs":
			if v, ok := inventory["vpc"]; ok {
				return map[string]any{"Vpcs": []any{v}}, nil
			}
			return map[string]any{"Vpcs": []any{}}, nil
		case "ec2:create-vpc":
			creates++
			v := map[string]any{"VpcId": "vpc-created", "CidrBlock": in["CidrBlock"], "State": "available", "Tags": tags}
			inventory["vpc"] = v
			return map[string]any{"Vpc": v}, nil
		case "ec2:modify-vpc-attribute", "ec2:modify-subnet-attribute":
			return nil, nil
		case "ec2:describe-subnets":
			if s, ok := inventory["subnet-"+kind()]; ok {
				return map[string]any{"Subnets": []any{s}}, nil
			}
			return map[string]any{"Subnets": []any{}}, nil
		case "ec2:create-subnet":
			creates++
			cidr := in["CidrBlock"].(string)
			s := map[string]any{"SubnetId": "subnet-" + strings.ReplaceAll(cidr, "/", "-"), "CidrBlock": cidr, "AvailabilityZone": in["AvailabilityZone"], "State": "available", "Tags": tags}
			inventory["subnet-"+cidr] = s
			return map[string]any{"Subnet": s}, nil
		case "ec2:describe-internet-gateways":
			if g, ok := inventory["gateway"]; ok {
				return map[string]any{"InternetGateways": []any{g}}, nil
			}
			return map[string]any{"InternetGateways": []any{}}, nil
		case "ec2:create-internet-gateway":
			creates++
			g := map[string]any{"InternetGatewayId": "igw-created", "Tags": tags}
			inventory["gateway"] = g
			return map[string]any{"InternetGateway": g}, nil
		case "ec2:attach-internet-gateway":
			inventory["gateway"].(map[string]any)["Attachments"] = []any{map[string]string{"VpcId": "vpc-created"}}
			return nil, nil
		case "ec2:describe-route-tables":
			if r, ok := inventory["route-"+kind()]; ok {
				return map[string]any{"RouteTables": []any{r}}, nil
			}
			return map[string]any{"RouteTables": []any{}}, nil
		case "ec2:create-route-table":
			creates++
			spec := in["TagSpecifications"].([]map[string]any)[0]
			routeKind := strings.TrimPrefix(spec["Tags"].([]tag)[1].Value, cfg.Name+"-")
			r := map[string]any{"RouteTableId": routeKind, "Tags": tags, "Associations": []any{}}
			inventory["route-"+routeKind] = r
			return map[string]any{"RouteTable": r}, nil
		case "ec2:create-route":
			inventory["route-"+in["RouteTableId"].(string)].(map[string]any)["Routes"] = []any{map[string]any{"DestinationCidrBlock": in["DestinationCidrBlock"], "GatewayId": in["GatewayId"]}}
			return nil, nil
		case "ec2:associate-route-table":
			r := inventory["route-"+in["RouteTableId"].(string)].(map[string]any)
			r["Associations"] = append(r["Associations"].([]any), map[string]any{"SubnetId": in["SubnetId"]})
			return nil, nil
		case "ec2:describe-security-groups":
			if g, ok := inventory["sg-"+kind()]; ok {
				return map[string]any{"SecurityGroups": []any{g}}, nil
			}
			return map[string]any{"SecurityGroups": []any{}}, nil
		case "ec2:create-security-group":
			creates++
			sgKind := strings.TrimPrefix(in["GroupName"].(string), cfg.Name+"-")
			g := map[string]any{"GroupId": "sg-" + sgKind, "Tags": tags}
			inventory["sg-"+sgKind] = g
			return g, nil
		case "ec2:authorize-security-group-ingress":
			return absent("InvalidPermission.Duplicate")
		case "rds:describe-db-subnet-groups":
			if g, ok := inventory["db-subnets"]; ok {
				return map[string]any{"DBSubnetGroups": []any{g}}, nil
			}
			return absent("DBSubnetGroupNotFoundFault")
		case "rds:create-db-subnet-group":
			creates++
			var subnets []any
			for _, id := range in["SubnetIds"].([]string) {
				subnets = append(subnets, map[string]string{"SubnetIdentifier": id})
			}
			inventory["db-subnets"] = map[string]any{"DBSubnetGroupName": in["DBSubnetGroupName"], "VpcId": "vpc-created", "Subnets": subnets}
			return nil, nil
		case "rds:describe-db-instances":
			if db, ok := inventory["db"]; ok {
				return map[string]any{"DBInstances": []any{db}}, nil
			}
			return absent("DBInstanceNotFound")
		case "rds:create-db-instance":
			creates++
			if in["StorageEncrypted"] != true || in["ManageMasterUserPassword"] != true || in["PubliclyAccessible"] != false || in["DeletionProtection"] != true {
				t.Fatal("unsafe RDS create request")
			}
			inventory["db"] = map[string]any{"DBInstanceIdentifier": in["DBInstanceIdentifier"], "DBInstanceArn": "arn:db", "DBInstanceStatus": "available", "Engine": "postgres", "DBInstanceClass": in["DBInstanceClass"], "MasterUsername": in["MasterUsername"], "AllocatedStorage": in["AllocatedStorage"], "StorageType": in["StorageType"], "MultiAZ": in["MultiAZ"], "StorageEncrypted": true, "DeletionProtection": true, "DBSubnetGroup": inventory["db-subnets"], "VpcSecurityGroups": []any{map[string]string{"VpcSecurityGroupId": "sg-rds"}}, "Endpoint": map[string]any{"Address": "db.example", "Port": in["Port"]}, "MasterUserSecret": map[string]string{"SecretArn": "arn:master", "SecretStatus": "active"}}
			return nil, nil
		case "rds:list-tags-for-resource":
			return map[string]any{"TagList": tags}, nil

		case "elbv2:describe-load-balancers":
			if l, ok := inventory["lb"]; ok {
				return map[string]any{"LoadBalancers": []any{l}}, nil
			}
			return absent("LoadBalancerNotFound")
		case "elbv2:create-load-balancer":
			creates++
			l := map[string]any{"LoadBalancerArn": "arn:lb", "DNSName": "internal-spire.elb.us-west-2.amazonaws.com", "Type": in["Type"], "Scheme": in["Scheme"], "VpcId": "vpc-created", "SecurityGroups": in["SecurityGroups"], "AvailabilityZones": []any{map[string]string{"SubnetId": in["Subnets"].([]string)[0]}, map[string]string{"SubnetId": in["Subnets"].([]string)[1]}}}
			inventory["lb"] = l
			return map[string]any{"LoadBalancers": []any{l}}, nil
		case "elbv2:describe-tags":
			return map[string]any{"TagDescriptions": []any{map[string]any{"Tags": tags}}}, nil
		case "elbv2:modify-load-balancer-attributes", "elbv2:modify-target-group-attributes":
			return nil, nil
		case "elbv2:describe-target-groups":
			if g, ok := inventory["tg"]; ok {
				return map[string]any{"TargetGroups": []any{g}}, nil
			}
			return absent("TargetGroupNotFound")
		case "elbv2:create-target-group":
			creates++
			in["TargetGroupArn"] = "arn:tg"
			inventory["tg"] = in
			return map[string]any{"TargetGroups": []any{in}}, nil
		case "elbv2:describe-listeners":
			if l, ok := inventory["listener"]; ok {
				return map[string]any{"Listeners": []any{l}}, nil
			}
			return map[string]any{"Listeners": []any{}}, nil
		case "elbv2:create-listener":
			creates++
			inventory["listener"] = in
			return nil, nil
		case "secretsmanager:describe-secret":
			if _, ok := inventory["secret"]; !ok {
				return absent("ResourceNotFoundException")
			}
			return map[string]any{"ARN": "arn:runtime", "Tags": tags}, nil
		case "secretsmanager:create-secret":
			creates++
			inventory["secret"] = in["SecretString"]
			return map[string]string{"ARN": "arn:runtime"}, nil
		case "secretsmanager:get-secret-value":
			return map[string]any{"SecretString": inventory["secret"]}, nil
		case "iam:get-role":
			if r, ok := inventory["role-"+in["RoleName"].(string)]; ok {
				return map[string]any{"Role": r}, nil
			}
			return absent("NoSuchEntity")
		case "iam:create-role":
			creates++
			inventory["role-"+in["RoleName"].(string)] = map[string]any{"RoleName": in["RoleName"], "Tags": tags}
			return nil, nil
		case "iam:update-assume-role-policy", "iam:attach-role-policy":
			return nil, nil
		case "iam:put-role-policy":
			policies[in["RoleName"].(string)] = in["PolicyDocument"].(string)
			return nil, nil
		case "iam:get-instance-profile":
			if p, ok := inventory["profile-"+in["InstanceProfileName"].(string)]; ok {
				return map[string]any{"InstanceProfile": p}, nil
			}
			return absent("NoSuchEntity")
		case "iam:create-instance-profile":
			creates++
			inventory["profile-"+in["InstanceProfileName"].(string)] = map[string]any{"Tags": tags}
			return nil, nil
		case "iam:add-role-to-instance-profile":
			inventory["profile-"+in["InstanceProfileName"].(string)].(map[string]any)["Roles"] = []any{map[string]any{"RoleName": in["RoleName"]}}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected AWS operation %s", key)
		}
	}}
	first, err := EnsureInfrastructure(context.Background(), aws, cfg)
	if err != nil {
		t.Fatal(err)
	}
	createdFirst := creates
	retry, err := EnsureInfrastructure(context.Background(), aws, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(retry)
	if string(a) != string(b) {
		t.Fatal("retry changed infrastructure")
	}
	if creates != createdFirst {
		t.Fatalf("retry created %d additional resources", creates-createdFirst)
	}
	if first.VPCID == "" || len(first.PrivateSubnetIDs) != 2 || len(first.PublicSubnetIDs) != 2 || first.DBSecretARN == "" || first.RuntimeSecretARN == "" || first.SpireProfile == "" || first.PlatformProfile == "" {
		t.Fatalf("lost infrastructure state: %+v", first)
	}
	if !strings.Contains(policies[cfg.Name+"-spire"], bundleParameterARN(cfg)) || !strings.Contains(policies[cfg.Name+"-spire"], "ssm:PutParameter") {
		t.Fatal("SPIRE cannot publish its bundle")
	}
	if !strings.Contains(policies[cfg.Name+"-platform"], "ec2:ResourceTag/CraneBootstrap") {
		t.Fatal("platform role does not protect bootstrap instances")
	}
	if !strings.Contains(policies[cfg.Name+"-platform"], cfg.Observability.NewRelicSecretARN) || !strings.Contains(policies[cfg.Name+"-platform"], "secretsmanager:GetSecretValue") {
		t.Fatal("platform role cannot read the configured New Relic secret")
	}
	if strings.Contains(policies[cfg.Name+"-spire"], cfg.Observability.NewRelicSecretARN) {
		t.Fatal("SPIRE role must not read the New Relic secret")
	}
}

func TestDiscoveryDeniedCannotProvision(t *testing.T) {
	aws := &fakeAWS{call: func(_, op string, _ any) (any, error) {
		if op != "describe-secret" {
			t.Fatalf("unexpected operation after permission failure %s", op)
		}
		return nil, &AWSError{Code: "AccessDenied"}
	}}
	if _, err := EnsureInfrastructure(context.Background(), aws, testConfig()); err == nil {
		t.Fatal("permission error was treated as missing infrastructure")
	}
}
