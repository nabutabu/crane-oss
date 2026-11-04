package api

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
)

type WorkerRegistration struct {
	ClusterID        string             `json:"cluster_id"`
	HostID           string             `json:"host_id"`
	NodeGroupID      string             `json:"node_group_id"`
	NodePoolID       string             `json:"node_pool_id"`
	TemplateRevision string             `json:"template_revision"`
	NodeName         string             `json:"node_name"`
	ProviderID       string             `json:"provider_id"`
	Scheduling       SchedulingTemplate `json:"scheduling"`
}

func WorkerNodeName(hostID string) string {
	sum := sha256.Sum256([]byte(hostID))
	return "crane-" + hex.EncodeToString(sum[:16])
}

func (r WorkerRegistration) Validate() error {
	if r.ClusterID == "" || r.HostID == "" || r.NodeGroupID == "" || r.NodePoolID == "" || r.TemplateRevision == "" || r.ProviderID == "" || r.NodeName != WorkerNodeName(r.HostID) || ValidateScheduling(r.Scheduling, r.NodePoolID) != nil {
		return errors.New("invalid worker registration")
	}
	return nil
}

// JoinFingerprint excludes rotating credentials and lease fences.
func JoinFingerprint(in ClusterJoinRequest) string {
	data, _ := json.Marshal(struct {
		Endpoint, CA string
		Registration WorkerRegistration
	}{in.APIServerEndpoint, in.CACertHash, in.Registration})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type ClusterMembershipRequest struct {
	Challenge string `json:"challenge"`
}
type ClusterMembership struct {
	ContractHash string `json:"contract_hash"`
	NodeName     string `json:"node_name"`
	NodeUID      string `json:"node_uid"`
	Certificate  []byte `json:"certificate"`
	Signature    []byte `json:"signature"`
}

func MembershipProofData(challenge string, in ClusterMembership) []byte {
	data, _ := json.Marshal(struct{ Challenge, ContractHash, NodeName, NodeUID string }{challenge, in.ContractHash, in.NodeName, in.NodeUID})
	return data
}

// KubernetesCAHash is kubeadm's SHA-256 pin of the CA public key.
func KubernetesCAHash(data []byte) (string, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return "", errors.New("invalid cluster trust root")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA {
		return "", errors.New("invalid cluster trust root")
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
