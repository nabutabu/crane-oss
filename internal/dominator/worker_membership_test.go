package dominator

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"github.com/nabutabu/crane-oss/pkg/api"
	certificates "k8s.io/api/certificates/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"math/big"
	"testing"
	"time"
)

func membershipFixture(t *testing.T) ([]byte, api.ClusterMembership) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic-root"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "system:node:node", Organization: []string{"system:nodes"}}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &nodeKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	proof := api.ClusterMembership{NodeName: "node", NodeUID: "uid", ContractHash: "contract", Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
	sum := sha256.Sum256(api.MembershipProofData("challenge", proof))
	proof.Signature, err = ecdsa.SignASN1(rand.Reader, nodeKey, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), proof
}

func TestMembershipRequiresSignedHostCredentialAndFreshChallenge(t *testing.T) {
	ca, proof := membershipFixture(t)
	if err := verifyMembership(proof, "challenge", "contract", "node", ca); err != nil {
		t.Fatal(err)
	}
	if verifyMembership(proof, "replayed-challenge", "contract", "node", ca) == nil {
		t.Fatal("replayed challenge accepted")
	}
	if verifyMembership(proof, "challenge", "contract", "another-node", ca) == nil {
		t.Fatal("wrong node accepted")
	}
	proof.NodeUID = "replacement"
	if verifyMembership(proof, "challenge", "contract", "node", ca) == nil {
		t.Fatal("forged node UID accepted")
	}
}

func TestClusterAccessRequiresConfiguredIdentityAndTrustRoot(t *testing.T) {
	ca, _ := membershipFixture(t)
	pin, err := api.KubernetesCAHash(ca)
	if err != nil {
		t.Fatal(err)
	}
	p := ProvisionExecutor{ClusterID: "shared", ClusterCA: ca, Kubernetes: fake.NewClientset(&core.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "crane-cluster-identity", Namespace: "kube-system"}, Data: map[string]string{"cluster_id": "shared"}})}
	op := api.Operation{ClusterID: "shared", CACertHash: pin}
	if err := p.validateCluster(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	op.ClusterID = "another-cluster"
	if p.validateCluster(context.Background(), op) == nil {
		t.Fatal("wrong cluster accepted")
	}
	op.ClusterID = "shared"
	op.CACertHash = "wrong-root"
	if p.validateCluster(context.Background(), op) == nil {
		t.Fatal("wrong CA accepted")
	}
}

func TestWorkerCSRRequiresTokenScopedNodeName(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "system:node:node", Organization: []string{"system:nodes"}}}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr := &certificates.CertificateSigningRequest{Spec: certificates.CertificateSigningRequestSpec{Username: "system:bootstrap:token", Groups: []string{"system:bootstrappers:crane"}, SignerName: certificates.KubeAPIServerClientKubeletSignerName, Request: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), Usages: []certificates.KeyUsage{certificates.UsageClientAuth, certificates.UsageDigitalSignature}}}
	if !validWorkerCSR(csr, "token", "node") {
		t.Fatal("valid CSR rejected")
	}
	if validWorkerCSR(csr, "token", "forged-node") || validWorkerCSR(csr, "other-token", "node") {
		t.Fatal("unbound CSR accepted")
	}
	csr.Spec.Usages = append(csr.Spec.Usages, certificates.UsageServerAuth)
	if validWorkerCSR(csr, "token", "node") {
		t.Fatal("server certificate usage accepted")
	}
}
