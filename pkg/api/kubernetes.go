package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

const KubernetesBodyLimit = 2 << 20
const OperationTimeout = 10 * time.Minute

// KubernetesSecret is a typed, explicit update, never an arbitrary manifest.
// Data is the complete desired data map; unrelated object metadata is preserved.
type KubernetesSecret struct {
	Namespace         string            `json:"namespace"`
	Name              string            `json:"name"`
	Type              string            `json:"type,omitempty"`
	Data              map[string][]byte `json:"data,omitempty"`
	StringData        map[string]string `json:"stringData,omitempty"`
	RolloutDeployment string            `json:"rollout_deployment,omitempty"`
}

type SecretRequest struct {
	RequestID string `json:"request_id"`
	KubernetesSecret
}

// KubernetesResult deliberately contains no resource data or API error bodies.
type KubernetesResult struct {
	Secret  string `json:"secret"`  // pending, verified, failed
	Rollout string `json:"rollout"` // not_requested, pending, ready, failed
}

type SecretResult struct {
	Error      string            `json:"error,omitempty"`
	HostID     string            `json:"host_id"`
	ZoneID     string            `json:"zone_id"`
	RequestID  string            `json:"request_id"`
	Revision   uint64            `json:"revision"`
	Outcome    string            `json:"outcome"`
	Kubernetes *KubernetesResult `json:"kubernetes,omitempty"`
}

// Normalize validates and combines Kubernetes data/stringData without leaking
// rejected values. Overlapping keys are rejected rather than silently replaced.
func (s *KubernetesSecret) Normalize() error {
	if len(validation.IsDNS1123Label(s.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(s.Name)) != 0 ||
		(s.RolloutDeployment != "" && len(validation.IsDNS1123Subdomain(s.RolloutDeployment)) != 0) {
		return errors.New("invalid Kubernetes resource name")
	}
	if s.Type == "" {
		s.Type = "Opaque"
	}
	if len(s.Type) > 253 || len(validation.IsQualifiedName(s.Type)) != 0 {
		return errors.New("invalid Secret type")
	}
	data := make(map[string][]byte, len(s.Data)+len(s.StringData))
	for key, value := range s.Data {
		data[key] = value
	}
	for key, value := range s.StringData {
		if _, exists := data[key]; exists {
			return errors.New("duplicate Secret key")
		}
		data[key] = []byte(value)
	}
	size := 0
	for key, value := range data {
		if len(validation.IsConfigMapKey(key)) != 0 {
			return errors.New("invalid Secret key")
		}
		size += len(value)
	}
	if size > 1<<20 {
		return errors.New("Secret data exceeds size limit")
	}
	s.Data, s.StringData = data, nil
	return nil
}

func (r *SecretRequest) Normalize() error {
	if r.RequestID == "" || len(r.RequestID) > 128 {
		return errors.New("invalid request ID")
	}
	return r.KubernetesSecret.Normalize()
}

func SecretFingerprint(s KubernetesSecret) string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
