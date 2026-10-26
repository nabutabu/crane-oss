package hostoperations

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nabutabu/crane-oss/internal/subd"
	"github.com/nabutabu/crane-oss/pkg/api"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type kubernetesTestHost struct{ subd.LocalHost }

func (kubernetesTestHost) Collect(context.Context) (api.CurrentState, error) {
	return api.CurrentState{}, nil
}

// This opt-in test uses only the named disposable kind cluster, never a default
// kubeconfig. PostgreSQL also uses the per-test isolated schema fixture.
func TestKubernetesSecretBlockingIntegration(t *testing.T) {
	path := os.Getenv("CRANE_TEST_KUBECONFIG")
	if path == "" {
		t.Skip("set CRANE_TEST_KUBECONFIG for the disposable kind integration")
	}
	file, err := clientcmd.LoadFromFile(path)
	if err != nil || file.CurrentContext != "kind-crane-secret-test" {
		t.Fatal("integration requires the dedicated kind-crane-secret-test context")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal("cannot load test cluster credentials")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal("cannot construct test cluster client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_, err = client.CoreV1().Namespaces().Create(ctx, &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "crane-system"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal("test namespace must not already exist")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = client.CoreV1().Namespaces().Delete(cleanup, "crane-system", metav1.DeleteOptions{})
	})
	_, err = client.AppsV1().Deployments("crane-system").Create(ctx, &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "otel-collector"},
		Spec: apps.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "collector"}},
			Template: core.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "collector"}}, Spec: core.PodSpec{
				Containers: []core.Container{{Name: "collector", Image: "otel/opentelemetry-collector-contrib:0.161.0", Args: []string{"--config=/etc/otel-exporter/exporter.yaml"},
					VolumeMounts:   []core.VolumeMount{{Name: "exporter", MountPath: "/etc/otel-exporter", ReadOnly: true}},
					ReadinessProbe: &core.Probe{ProbeHandler: core.ProbeHandler{HTTPGet: &core.HTTPGetAction{Path: "/", Port: intstr.FromInt32(13133)}}, PeriodSeconds: 1}}},
				Volumes: []core.Volume{{Name: "exporter", VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{SecretName: "otel-exporter-config"}}}},
			}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal("cannot create test collector Deployment")
	}
	if _, err = client.CoreV1().Secrets("crane-system").Get(ctx, "otel-exporter-config", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("test requires an absent exporter Secret")
	}
	db := operationDatabase(t)
	stateDir := t.TempDir()
	agent, err := subd.NewServer(subd.Config{ListenAddress: "127.0.0.1:44001", TrustDomain: "test.internal", WriterIdentity: ownID}, "spiffe://test.internal/subd/provider-host", filepath.Join(stateDir, "state.json"), filepath.Join(stateDir, "status.json"), kubernetesTestHost{subd.LocalHost{Kubeconfig: path}})
	if err != nil {
		t.Fatal(err)
	}
	var dominator http.Handler
	caller := callerFunc(func(ctx context.Context, endpoint, _, method, path string, in, out any) (int, error) {
		body, _ := json.Marshal(in)
		r := httptest.NewRequest(method, path, strings.NewReader(string(body))).WithContext(ctx)
		actor := ownID
		handler := agent.Handler()
		if endpoint == "https://127.0.0.1:44000" {
			actor = apiID
			handler = dominator
		}
		u, _ := url.Parse(actor)
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{u}}}}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if json.Unmarshal(w.Body.Bytes(), out) != nil {
			return w.Code, errors.New("invalid integration response")
		}
		return w.Code, nil
	})
	dominator = (Controller{Store: Store{DB: db}, Caller: caller, ZoneID: "zone", TrustDomain: "test.internal", OwnIdentity: ownID}).Handler(apiID)
	mux := http.NewServeMux()
	mux.Handle(PublicPattern, API{Store: Store{DB: db}, Caller: caller, AdminToken: "synthetic-admin"})
	fragment := "extensions:\n  health_check:\n    endpoint: 0.0.0.0:13133\nreceivers:\n  otlp:\n    protocols:\n      grpc:\n        endpoint: 0.0.0.0:4317\nexporters:\n  debug: {}\nservice:\n  extensions: [health_check]\n  pipelines:\n    logs:\n      receivers: [otlp]\n      exporters: [debug]\n"
	call := func(id, fragment string) api.SecretResult {
		t.Helper()
		in := secretRequest(id)
		in.StringData["exporter.yaml"] = fragment
		body, _ := json.Marshal(in)
		r := httptest.NewRequest("PUT", "/v1/hosts/"+url.PathEscape(hostID)+"/kubernetes/namespaces/crane-system/secrets/otel-exporter-config", strings.NewReader(string(body))).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer synthetic-admin")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var result api.SecretResult
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Kubernetes == nil || result.Kubernetes.Rollout != "ready" {
			t.Fatal("blocking collector operation failed", w.Code, w.Body.String())
		}
		return result
	}
	first := call("initial", fragment)
	before, err := client.AppsV1().Deployments("crane-system").Get(ctx, "otel-collector", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result := call("initial", fragment); result.Revision != first.Revision {
		t.Fatal("replay allocated another revision")
	}
	after, err := client.AppsV1().Deployments("crane-system").Get(ctx, "otel-collector", metav1.GetOptions{})
	if err != nil || after.Generation != before.Generation {
		t.Fatal("replay restarted collector")
	}
	if result := call("rotation", fragment+"# rotated\n"); result.Revision <= first.Revision {
		t.Fatal("rotation did not advance revision")
	}
}
