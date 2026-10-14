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
	VPCID                string   `json:"vpc_id"`
	PrivateSubnetIDs     []string `json:"private_subnet_ids"`
	RDSecurityGroupID    string   `json:"rds_security_group_id"`
	SPIRESecurityGroupID string   `json:"spire_security_group_id"`
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
	Identifier         string `json:"identifier"`
	Engine             string `json:"engine"`
	EngineVersion      string `json:"engine_version"`
	InstanceClass      string `json:"instance_class"`
	StorageGiB         int32  `json:"storage_gib"`
	DeletionProtection bool   `json:"deletion_protection"`
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported bootstrap config version %d", c.Version)
	}
	for name, value := range map[string]string{"aws.region": c.AWS.Region, "aws.account_id": c.AWS.AccountID, "network.vpc_id": c.Network.VPCID, "rds.identifier": c.RDS.Identifier, "rds.engine": c.RDS.Engine, "rds.instance_class": c.RDS.InstanceClass, "spire.ami_id": c.SPIRE.AMIID, "spire.instance_type": c.SPIRE.InstanceType, "spire.iam_instance_profile": c.SPIRE.IAMInstanceProfile} {
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
	if c.RDS.DeletionProtection == false {
		return errors.New("rds.deletion_protection must be enabled")
	}
	return nil
}
