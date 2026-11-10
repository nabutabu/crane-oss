package awscompute

import (
	"encoding/json"
	"path"
	"testing"
)

// Check the generated role policies, including the independent authorization
// of the existing VPC and the not-yet-created subnet in CreateSubnet.
func checkAttachmentNetworkPermissions(t *testing.T, policies map[string]string, cfg bootstrapConfig, vpcID string) {
	t.Helper()
	prefix := "arn:aws:ec2:" + cfg.AWS.Region + ":" + cfg.AWS.AccountID + ":"
	cases := []struct {
		name, action, resource, owner, requestOwner, createAction, region string
		allow                                                             bool
	}{
		{"discover VPC", "ec2:DescribeVpcs", "*", "", "", "", cfg.AWS.Region, true},
		{"discover routes", "ec2:DescribeRouteTables", "*", "", "", "", cfg.AWS.Region, true},
		{"owned VPC", "ec2:CreateSubnet", prefix + "vpc/" + vpcID, cfg.Name, "", "", cfg.AWS.Region, true},
		{"foreign VPC", "ec2:CreateSubnet", prefix + "vpc/vpc-other", cfg.Name, "", "", cfg.AWS.Region, false},
		{"untagged VPC", "ec2:CreateSubnet", prefix + "vpc/" + vpcID, "", "", "", cfg.AWS.Region, false},
		{"new subnet", "ec2:CreateSubnet", prefix + "subnet/subnet-new", "", cfg.Name, "", cfg.AWS.Region, true},
		{"untagged new subnet", "ec2:CreateSubnet", prefix + "subnet/subnet-new", "", "", "", cfg.AWS.Region, false},
		{"tag on creation", "ec2:CreateTags", prefix + "subnet/subnet-new", "", cfg.Name, "CreateSubnet", cfg.AWS.Region, true},
		{"retag existing subnet", "ec2:CreateTags", prefix + "subnet/subnet-new", cfg.Name, cfg.Name, "", cfg.AWS.Region, false},
		{"modify owned subnet", "ec2:ModifySubnetAttribute", prefix + "subnet/subnet-new", cfg.Name, "", "", cfg.AWS.Region, true},
		{"modify foreign subnet", "ec2:ModifySubnetAttribute", prefix + "subnet/subnet-new", "other", "", "", cfg.AWS.Region, false},
		{"associate subnet", "ec2:AssociateRouteTable", prefix + "subnet/subnet-new", cfg.Name, "", "", cfg.AWS.Region, true},
		{"associate route table", "ec2:AssociateRouteTable", prefix + "route-table/rtb-owned", cfg.Name, "", "", cfg.AWS.Region, true},
		{"foreign route table", "ec2:AssociateRouteTable", prefix + "route-table/rtb-other", "other", "", "", cfg.AWS.Region, false},
		{"other region", "ec2:DescribeVpcs", "*", "", "", "", "other-region", false},
	}
	for role, raw := range policies {
		var policy struct {
			Statement []struct {
				Effect           string
				Action, Resource any
				Condition        map[string]map[string]string
			}
		}
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			t.Fatal(err)
		}
		for _, tc := range cases {
			t.Run(role+"/"+tc.name, func(t *testing.T) {
				context := map[string]string{"aws:RequestedRegion": tc.region, "ec2:ResourceTag/CraneBootstrap": tc.owner, "aws:RequestTag/CraneBootstrap": tc.requestOwner, "ec2:CreateAction": tc.createAction}
				allowed := false
				for _, s := range policy.Statement {
					if s.Effect != "Allow" || !iamTestMatch(s.Action, tc.action) || !iamTestMatch(s.Resource, tc.resource) {
						continue
					}
					matches := true
					for op, terms := range s.Condition {
						for k, v := range terms {
							if op != "StringEquals" || context[k] == "" || context[k] != v {
								matches = false
							}
						}
					}
					if matches {
						allowed = true
					}
				}
				want := tc.allow && role == cfg.Name+"-platform"
				if allowed != want {
					t.Fatalf("%s on %s: allowed=%v, want %v", tc.action, tc.resource, allowed, want)
				}
			})
		}
	}
}

// Only the wildcard/string and StringEquals subset used by these permissions
// is evaluated here; this is not a substitute for AWS IAM acceptance testing.
func iamTestMatch(value any, target string) bool {
	switch v := value.(type) {
	case string:
		ok, _ := path.Match(v, target)
		return ok
	case []any:
		for _, item := range v {
			if iamTestMatch(item, target) {
				return true
			}
		}
	}
	return false
}
