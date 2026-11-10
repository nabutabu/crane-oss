package awscompute

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestVerifyAttachmentClusterPinsMarkerCAAndProtectedNode(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-root"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pin, err := api.KubernetesCAHash(ca)
	if err != nil {
		t.Fatal(err)
	}
	immutable := true
	kube := fake.NewClientset(
		&core.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "crane-cluster-identity", Namespace: "kube-system"}, Data: map[string]string{"cluster_id": "cluster"}, Immutable: &immutable},
		&core.Node{ObjectMeta: metav1.ObjectMeta{Name: "platform-node"}, Spec: core.NodeSpec{ProviderID: "aws:///us-west-2a/i-11111111111111111"}},
	)
	op := api.Operation{Kind: "attach_zone", ClusterID: "cluster", CACertHash: pin}
	if name, err := VerifyAttachmentCluster(context.Background(), kube, ca, op, "i-11111111111111111"); err != nil || name != "platform-node" {
		t.Fatalf("verified platform node %q: %v", name, err)
	}
	op.CACertHash = "sha256:wrong"
	if _, err := VerifyAttachmentCluster(context.Background(), kube, ca, op, "i-11111111111111111"); err == nil {
		t.Fatal("different cluster CA accepted")
	}
	op.CACertHash = pin
	if _, err := VerifyAttachmentCluster(context.Background(), kube, ca, op, "i-22222222222222222"); err == nil {
		t.Fatal("unrelated platform host accepted")
	}
}
