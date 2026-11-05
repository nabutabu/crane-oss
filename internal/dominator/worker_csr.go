package dominator

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"github.com/nabutabu/crane-oss/pkg/api"
	"go.opentelemetry.io/otel/trace"
	certificates "k8s.io/api/certificates/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log/slog"
	"slices"
	"time"
)

func validWorkerCSR(csr *certificates.CertificateSigningRequest, tokenID, nodeName string) bool {
	if csr.Spec.Username != "system:bootstrap:"+tokenID || csr.Spec.SignerName != certificates.KubeAPIServerClientKubeletSignerName || !slices.Contains(csr.Spec.Groups, "system:bootstrappers:crane") {
		return false
	}
	block, _ := pem.Decode(csr.Spec.Request)
	if block == nil {
		return false
	}
	req, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || req.CheckSignature() != nil || req.Subject.CommonName != "system:node:"+nodeName || len(req.Subject.Organization) != 1 || req.Subject.Organization[0] != "system:nodes" || len(req.DNSNames)+len(req.IPAddresses)+len(req.EmailAddresses)+len(req.URIs) != 0 {
		return false
	}
	if !slices.Contains(csr.Spec.Usages, certificates.UsageClientAuth) || !slices.Contains(csr.Spec.Usages, certificates.UsageDigitalSignature) {
		return false
	}
	for _, usage := range csr.Spec.Usages {
		if usage != certificates.UsageClientAuth && usage != certificates.UsageDigitalSignature && usage != certificates.UsageKeyEncipherment {
			return false
		}
	}
	return true
}

func (p *ProvisionExecutor) approveWorkerCSRs(ctx context.Context, op api.Operation, tokenID, nodeName string) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.Store.CheckProvisionLease(ctx, op); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			requests, err := p.Kubernetes.CertificatesV1().CertificateSigningRequests().List(ctx, metav1.ListOptions{})
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("cannot inspect worker certificate requests")
			}
			for i := range requests.Items {
				csr := &requests.Items[i]
				if csr.Spec.Username != "system:bootstrap:"+tokenID {
					continue
				}
				if !validWorkerCSR(csr, tokenID, nodeName) {
					return errors.New("worker certificate request conflicts with join contract")
				}
				if len(csr.Status.Conditions) != 0 {
					continue
				}
				csr.Status.Conditions = []certificates.CertificateSigningRequestCondition{{Type: certificates.CertificateApproved, Status: core.ConditionTrue, Reason: "CraneWorkerJoin", Message: "Private host join contract verified", LastUpdateTime: metav1.Now()}}
				if _, err = p.Kubernetes.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{}); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return errors.New("cannot approve worker certificate request")
				}
				trace.SpanFromContext(ctx).AddEvent("worker certificate request approved")
				slog.InfoContext(ctx, "worker certificate request approved", "operation_id", op.ID, "host_id", op.HostID)
			}
		}
	}
}
