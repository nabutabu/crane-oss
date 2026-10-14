// Package bootstrap contains configuration and validation for the layer-zero
// bootstrap command. Secrets are intentionally represented by references only.
package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Config struct {
	Version   int             `json:"version"`
	AWS       AWSConfig       `json:"aws"`
	Network   NetworkConfig   `json:"network"`
	SPIRE     SPIREConfig     `json:"spire"`
	RDS       RDSConfig       `json:"rds"`
	FirstZone json.RawMessage `json:"first_zone"`
}

type AWSConfig struct {
	Region    string `json:"region"`
	AccountID string `json:"account_id"`
}

type NetworkConfig struct {
	VPCID                          string   `json:"vpc_id"`
	PrivateSubnetIDs               []string `json:"private_subnet_ids"`
	RDSecurityGroupID              string   `json:"rds_security_group_id"`
	RDSecurityGroupName            string   `json:"rds_security_group_name"`
	PostgresIngressSecurityGroupID string   `json:"postgres_ingress_security_group_id"`
	DBSubnetGroupName              string   `json:"db_subnet_group_name"`
	SPIRESecurityGroupID           string   `json:"spire_security_group_id"`
}

type SPIREConfig struct {
	AMIID              string `json:"ami_id"`
	InstanceType       string `json:"instance_type"`
	IAMInstanceProfile string `json:"iam_instance_profile"`
	KeyName            string `json:"key_name"`
	StorageGiB         int32  `json:"storage_gib"`
	TrustDomain        string `json:"trust_domain"`
	Image              string `json:"image"`
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

func (c Config) WithDefaults() Config {
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

func (c Config) Validate() error {
	c = c.WithDefaults()
	if c.Version != 1 {
		return fmt.Errorf("unsupported bootstrap config version %d", c.Version)
	}
	required := map[string]string{
		"aws.region":                 c.AWS.Region,
		"aws.account_id":             c.AWS.AccountID,
		"network.vpc_id":             c.Network.VPCID,
		"rds.identifier":             c.RDS.Identifier,
		"rds.engine":                 c.RDS.Engine,
		"rds.instance_class":         c.RDS.InstanceClass,
		"spire.ami_id":               c.SPIRE.AMIID,
		"spire.instance_type":        c.SPIRE.InstanceType,
		"spire.iam_instance_profile": c.SPIRE.IAMInstanceProfile,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if len(c.Network.PrivateSubnetIDs) == 0 {
		return errors.New("network.private_subnet_ids is required")
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
	if c.Network.PostgresIngressSecurityGroupID == "" && c.Network.SPIRESecurityGroupID == "" {
		return errors.New("network.postgres_ingress_security_group_id is required")
	}
	for _, db := range c.RDS.Databases {
		if strings.TrimSpace(db) == "" || strings.ContainsAny(db, " ;\"'") {
			return fmt.Errorf("invalid database name %q", db)
		}
	}
	return nil
}
