package dominator

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"github.com/nabutabu/crane-oss/pkg/api"
	"go.opentelemetry.io/otel/trace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (p *ProvisionExecutor) validateCluster(ctx context.Context, op api.Operation) error {
	invalid := errors.New("provisioning cluster access does not match approved destination")
	if p.ClusterID == "" || p.ClusterID != op.ClusterID || p.Kubernetes == nil {
		return invalid
	}
	pin, err := api.KubernetesCAHash(p.ClusterCA)
	if err != nil || pin != op.CACertHash {
		return invalid
	}
	marker, err := p.Kubernetes.CoreV1().ConfigMaps("kube-system").Get(ctx, "crane-cluster-identity", metav1.GetOptions{})
	if err != nil || marker.Data["cluster_id"] != op.ClusterID {
		return invalid
	}
	return nil
}

func (p *ProvisionExecutor) registration(ctx context.Context, op api.Operation, host api.Host) (api.WorkerRegistration, error) {
	identity, err := p.verifiedWorker(ctx, op, host)
	if err != nil {
		return api.WorkerRegistration{}, err
	}
	reg := api.WorkerRegistration{ClusterID: op.ClusterID, HostID: host.ID, NodeGroupID: op.NodeGroupID, NodePoolID: op.NodePoolID, TemplateRevision: op.TemplateRevision, NodeName: api.WorkerNodeName(host.ID), ProviderID: identity.KubernetesProviderID, Scheduling: op.Scheduling}
	return reg, reg.Validate()
}

func verifyMembership(proof api.ClusterMembership, challenge, contract, nodeName string, ca []byte) error {
	invalid := errors.New("worker kubelet membership proof rejected")
	if proof.ContractHash != contract || proof.NodeName != nodeName || proof.NodeUID == "" {
		return invalid
	}
	block, _ := pem.Decode(proof.Certificate)
	if block == nil {
		return invalid
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || cert.Subject.CommonName != "system:node:"+nodeName || len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != "system:nodes" {
		return invalid
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return invalid
	}
	if _, err = cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return invalid
	}
	sum := sha256.Sum256(api.MembershipProofData(challenge, proof))
	switch key := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], proof.Signature) != nil {
			return invalid
		}
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(key, sum[:], proof.Signature) {
			return invalid
		}
	case ed25519.PublicKey:
		if !ed25519.Verify(key, sum[:], proof.Signature) {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}

func (p *ProvisionExecutor) workerMembership(ctx context.Context, op api.Operation, host api.Host) (api.ClusterMembership, error) {
	reg, err := p.registration(ctx, op, host)
	if err != nil {
		return api.ClusterMembership{}, err
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return api.ClusterMembership{}, errors.New("cannot generate membership challenge")
	}
	challenge := base64.RawURLEncoding.EncodeToString(nonce[:])
	var proof api.ClusterMembership
	code, err := p.Caller.Call(ctx, host.SubdEndpoint, "spiffe://"+p.TrustDomain+"/subd/"+host.ProviderID, "POST", "/v1/cluster-membership", api.ClusterMembershipRequest{Challenge: challenge}, &proof)
	contract := api.JoinFingerprint(api.ClusterJoinRequest{APIServerEndpoint: op.APIServerEndpoint, CACertHash: op.CACertHash, Registration: reg})
	if err != nil || code != 200 || verifyMembership(proof, challenge, contract, reg.NodeName, p.ClusterCA) != nil {
		return api.ClusterMembership{}, errors.New("worker kubelet membership verification failed")
	}
	trace.SpanFromContext(ctx).AddEvent("worker kubelet membership verified")
	return proof, nil
}
