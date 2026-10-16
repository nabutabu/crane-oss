package awscompute

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

type bootstrapConfig struct {
	Name      string          `json:"name"`
	AWS       AWSConfig       `json:"aws"`
	Network   NetworkConfig   `json:"network"`
	SPIRE     SPIREConfig     `json:"spire"`
	RDS       RDSConfig       `json:"rds"`
	Platform  PlatformConfig  `json:"platform"`
	FirstZone json.RawMessage `json:"first_zone"`
}

type AWSConfig struct {
	Region    string `json:"region"`
	AccountID string `json:"account_id"`
}

type NetworkConfig struct {
	VPCCIDR                        string   `json:"vpc_cidr"`
	PublicSubnetCIDRs              []string `json:"public_subnet_cidrs"`
	PrivateSubnetCIDRs             []string `json:"private_subnet_cidrs"`
	AvailabilityZones              []string `json:"availability_zones"`
	VPCID                          string   `json:"vpc_id"`
	PrivateSubnetIDs               []string `json:"private_subnet_ids"`
	RDSecurityGroupID              string   `json:"rds_security_group_id"`
	RDSecurityGroupName            string   `json:"rds_security_group_name"`
	PostgresIngressSecurityGroupID string   `json:"postgres_ingress_security_group_id"`
	DBSubnetGroupName              string   `json:"db_subnet_group_name"`
	SPIRESecurityGroupID           string   `json:"spire_security_group_id"`
}

type SPIREConfig struct {
	Version            string `json:"version"`
	AMIID              string `json:"ami_id"`
	InstanceType       string `json:"instance_type"`
	IAMInstanceProfile string `json:"iam_instance_profile"`
	KeyName            string `json:"key_name"`
	StorageGiB         int32  `json:"storage_gib"`
	TrustDomain        string `json:"trust_domain"`
	Image              string `json:"image"`
}

// PlatformConfig pins the initial platform cluster and its protected host.
type PlatformConfig struct {
	CraneAPIImage     string `json:"crane_api_image"`
	DominatorImage    string `json:"dominator_image"`
	AMIID             string `json:"ami_id"`
	InstanceType      string `json:"instance_type"`
	StorageGiB        int32  `json:"storage_gib"`
	KubernetesVersion string `json:"kubernetes_version"`
	CiliumVersion     string `json:"cilium_version"`
	PodCIDR           string `json:"pod_cidr"`
}

type RDSConfig struct {
	Identifier               string   `json:"identifier"`
	Engine                   string   `json:"engine"`
	EngineVersion            string   `json:"engine_version"`
	InstanceClass            string   `json:"instance_class"`
	StorageGiB               int32    `json:"storage_gib"`
	DeletionProtection       bool     `json:"deletion_protection"`
	MasterUsername           string   `json:"master_username"`
	Port                     int32    `json:"port"`
	MultiAZ                  bool     `json:"multi_az"`
	StorageType              string   `json:"storage_type"`
	ManageMasterUserPassword bool     `json:"manage_master_user_password"`
	Databases                []string `json:"databases"`
}

func (c bootstrapConfig) WithDefaults() bootstrapConfig {
	if strings.TrimSpace(string(c.FirstZone)) == "null" {
		c.FirstZone = nil
	}
	if c.Name == "" {
		c.Name = "crane-platform"
	}
	if c.Network.VPCCIDR == "" {
		c.Network.VPCCIDR = "10.80.0.0/16"
	}
	if len(c.Network.PublicSubnetCIDRs) == 0 {
		c.Network.PublicSubnetCIDRs = []string{"10.80.0.0/24", "10.80.1.0/24"}
	}
	if len(c.Network.PrivateSubnetCIDRs) == 0 {
		c.Network.PrivateSubnetCIDRs = []string{"10.80.16.0/24", "10.80.17.0/24"}
	}
	if c.SPIRE.Version == "" {
		c.SPIRE.Version = "1.9.3"
	}
	if c.SPIRE.InstanceType == "" {
		c.SPIRE.InstanceType = "t3.small"
	}
	if c.SPIRE.StorageGiB == 0 {
		c.SPIRE.StorageGiB = 20
	}
	if c.SPIRE.TrustDomain == "" {
		c.SPIRE.TrustDomain = "crane.internal"
	}
	if c.Platform.InstanceType == "" {
		c.Platform.InstanceType = "t3.medium"
	}
	if c.Platform.StorageGiB == 0 {
		c.Platform.StorageGiB = 30
	}
	if c.Platform.KubernetesVersion == "" {
		c.Platform.KubernetesVersion = "v1.35.0"
	}
	if c.Platform.CiliumVersion == "" {
		c.Platform.CiliumVersion = "1.19.0"
	}
	if c.Platform.PodCIDR == "" {
		c.Platform.PodCIDR = "10.244.0.0/16"
	}
	if c.RDS.Identifier == "" {
		c.RDS.Identifier = c.Name + "-db"
	}
	if c.RDS.Engine == "" {
		c.RDS.Engine = "postgres"
	}
	if c.RDS.InstanceClass == "" {
		c.RDS.InstanceClass = "db.t3.micro"
	}
	if c.RDS.StorageGiB == 0 {
		c.RDS.StorageGiB = 20
	}
	c.RDS.ManageMasterUserPassword = true
	c.RDS.DeletionProtection = true
	if c.RDS.MasterUsername == "" {
		c.RDS.MasterUsername = "postgres"
	}
	if c.RDS.Port == 0 {
		c.RDS.Port = 5432
	}
	if c.RDS.StorageType == "" {
		c.RDS.StorageType = "gp3"
	}
	if c.Network.RDSecurityGroupName == "" {
		c.Network.RDSecurityGroupName = "spire-rds-sg"
	}
	if len(c.RDS.Databases) == 0 {
		c.RDS.Databases = []string{"spire", "crane"}
	}
	return c
}

func (c bootstrapConfig) Validate() error {
	c = c.WithDefaults()
	required := map[string]string{
		"aws.region":          c.AWS.Region,
		"rds.identifier":      c.RDS.Identifier,
		"rds.engine":          c.RDS.Engine,
		"rds.instance_class":  c.RDS.InstanceClass,
		"spire.instance_type": c.SPIRE.InstanceType,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if c.RDS.StorageGiB < 20 {
		return errors.New("rds.storage_gib must be at least 20")
	}
	if c.SPIRE.StorageGiB < 8 {
		return errors.New("spire.storage_gib must be at least 8")
	}
	if c.SPIRE.TrustDomain == "" {
		return errors.New("spire.trust_domain is required")
	}
	if !c.RDS.DeletionProtection {
		return errors.New("rds.deletion_protection must be enabled")
	}
	if c.RDS.Port < 1 || c.RDS.Port > 65535 {
		return errors.New("rds.port must be between 1 and 65535")
	}
	if strings.TrimSpace(c.RDS.MasterUsername) == "" {
		return errors.New("rds.master_username is required")
	}
	for _, db := range c.RDS.Databases {
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(db) {
			return fmt.Errorf("invalid database name %q", db)
		}
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{2,29}$`).MatchString(c.Name) {
		return errors.New("name must be 3-30 lowercase letters, digits or hyphens, starting with a letter")
	}
	if !regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+$`).MatchString(c.AWS.Region) {
		return errors.New("invalid aws.region")
	}
	if strings.HasPrefix(c.AWS.Region, "cn-") || strings.HasPrefix(c.AWS.Region, "us-gov-") || strings.HasPrefix(c.AWS.Region, "us-iso") {
		return errors.New("bootstrap currently supports commercial AWS regions only")
	}
	if c.AWS.AccountID != "" && !regexp.MustCompile(`^[0-9]{12}$`).MatchString(c.AWS.AccountID) {
		return errors.New("aws.account_id must contain 12 digits")
	}
	if c.Network.VPCID != "" || len(c.Network.PrivateSubnetIDs) != 0 {
		return errors.New("bootstrap creates its own network; omit existing network IDs")
	}
	if c.Network.RDSecurityGroupID != "" || c.Network.SPIRESecurityGroupID != "" || c.Network.PostgresIngressSecurityGroupID != "" || c.Network.DBSubnetGroupName != "" || c.SPIRE.IAMInstanceProfile != "" || c.SPIRE.KeyName != "" {
		return errors.New("bootstrap provisions its own security groups, subnet group and instance profiles; omit existing IDs and SSH keys")
	}
	if c.SPIRE.Image != "" && c.SPIRE.Image != "ghcr.io/spiffe/spire-server:"+c.SPIRE.Version {
		return errors.New("spire.image must match the pinned public SPIRE server version, or be omitted")
	}
	if !sameStrings(c.RDS.Databases, []string{"spire", "crane"}) {
		return errors.New("bootstrap requires exactly the spire and crane databases")
	}
	vpc, err := netip.ParsePrefix(c.Network.VPCCIDR)
	if err != nil || !vpc.Addr().Is4() || vpc != vpc.Masked() || vpc.Bits() < 16 || vpc.Bits() > 28 {
		return errors.New("network.vpc_cidr must be a canonical IPv4 CIDR")
	}
	if len(c.Network.PublicSubnetCIDRs) != 2 || len(c.Network.PrivateSubnetCIDRs) != 2 {
		return errors.New("provide two public and two private subnet CIDRs")
	}
	if len(c.Network.AvailabilityZones) != 0 && (len(c.Network.AvailabilityZones) != 2 || c.Network.AvailabilityZones[0] == c.Network.AvailabilityZones[1]) {
		return errors.New("provide two distinct availability zones, or omit to discover them")
	}
	var prefixes []netip.Prefix
	for _, raw := range append(append([]string{}, c.Network.PublicSubnetCIDRs...), c.Network.PrivateSubnetCIDRs...) {
		p, err := netip.ParsePrefix(raw)
		if err != nil || !p.Addr().Is4() || p != p.Masked() || p.Bits() <= vpc.Bits() || p.Bits() > 28 || !vpc.Contains(p.Addr()) {
			return fmt.Errorf("subnet %q must be a canonical IPv4 CIDR within network.vpc_cidr", raw)
		}
		for _, other := range prefixes {
			if other.Overlaps(p) {
				return errors.New("subnet CIDRs must not overlap")
			}
		}
		prefixes = append(prefixes, p)
	}
	pod, err := netip.ParsePrefix(c.Platform.PodCIDR)
	if err != nil || !pod.Addr().Is4() || pod != pod.Masked() || pod.Overlaps(vpc) {
		return errors.New("platform.pod_cidr must be a canonical IPv4 CIDR outside the VPC")
	}
	if c.RDS.Engine != "postgres" {
		return errors.New("rds.engine must be postgres")
	}
	if c.Platform.StorageGiB < 20 {
		return errors.New("platform.storage_gib must be at least 20")
	}
	for name, value := range map[string]string{"spire.version": c.SPIRE.Version, "platform.cilium_version": c.Platform.CiliumVersion} {
		if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(value) {
			return fmt.Errorf("%s must be an exact release version", name)
		}
	}
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(c.Platform.KubernetesVersion) {
		return errors.New("platform.kubernetes_version must be an exact v-prefixed version")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`).MatchString(c.SPIRE.TrustDomain) {
		return errors.New("invalid spire.trust_domain")
	}
	for name, value := range map[string]string{"rds.identifier": c.RDS.Identifier, "rds.master_username": c.RDS.MasterUsername, "rds.instance_class": c.RDS.InstanceClass, "rds.engine_version": c.RDS.EngineVersion, "spire.instance_type": c.SPIRE.InstanceType, "spire.ami_id": c.SPIRE.AMIID, "platform.instance_type": c.Platform.InstanceType, "platform.ami_id": c.Platform.AMIID} {
		if value != "" && !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(value) {
			return fmt.Errorf("invalid %s", name)
		}
	}
	for name, value := range map[string]string{"platform.crane_api_image": c.Platform.CraneAPIImage, "platform.dominator_image": c.Platform.DominatorImage} {
		if value != "" && !regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]*(?::v?[0-9]+\.[0-9]+\.[0-9]+|:[a-f0-9]{40}|@sha256:[a-f0-9]{64})$`).MatchString(value) {
			return fmt.Errorf("%s must be a versioned GHCR/ECR image or a digest reference", name)
		}
	}
	if len(c.FirstZone) > 0 && !json.Valid(c.FirstZone) {
		return errors.New("first_zone must be valid JSON")
	}
	if len(c.FirstZone) > 0 {
		if _, err := topology(c, Infrastructure{AvailabilityZones: []string{c.AWS.Region + "a"}, PublicSubnetIDs: []string{"validation-only"}}); err != nil {
			return err
		}
	}
	return nil
}
