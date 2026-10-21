package subd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
	apps "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type secretFakeHost struct {
	fakeHost
	secretCalls  int
	rolloutFails bool
}

func (h *secretFakeHost) EnsureSecret(context.Context, string, api.KubernetesSecret) (*api.KubernetesResult, error) {
	h.secretCalls++
	if h.rolloutFails {
		return &api.KubernetesResult{Secret: "verified", Rollout: "failed"}, errors.New("synthetic-private-body")
	}
	return &api.KubernetesResult{Secret: "verified", Rollout: "ready"}, nil
}

func TestSecretProtocolPersistsFinalOutcomeAndRetries(t *testing.T) {
	h := &secretFakeHost{rolloutFails: true}
	s := testServer(t, h)
	body := `{"revision":2,"secret":{"namespace":"default","name":"config","stringData":{"key":"synthetic-value"},"rollout_deployment":"collector"}}`
	w := request(t, s, testConfig().WriterIdentity, "PUT", "/v1/expected-state", body)
	var status api.HostAgentStatus
	if w.Code != 500 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Kubernetes.Secret != "verified" || status.Kubernetes.Rollout != "failed" || strings.Contains(w.Body.String(), "synthetic") {
		t.Fatal("incorrect or unsafe failure response")
	}
	h.rolloutFails = false
	w = request(t, s, testConfig().WriterIdentity, "PUT", "/v1/expected-state", body)
	if w.Code != 200 || h.secretCalls != 2 {
		t.Fatal(w.Code, h.secretCalls)
	}
	restarted, err := NewServer(testConfig(), s.identity, s.statePath, s.statusPath, h)
	if err != nil {
		t.Fatal(err)
	}
	w = request(t, restarted, testConfig().WriterIdentity, "PUT", "/v1/expected-state", body)
	if w.Code != 200 || h.secretCalls != 2 || strings.Contains(w.Body.String(), "synthetic") {
		t.Fatal("replay executed or leaked data")
	}
}

func TestSecretCreateUpdateAndMetadata(t *testing.T) {
	client := fake.NewClientset()
	in := api.KubernetesSecret{Namespace: "crane-system", Name: "config", StringData: map[string]string{"exporter.yaml": "synthetic"}}
	result, err := ensureSecret(context.Background(), client, "operation", in, time.Millisecond)
	if err != nil || result.Secret != "verified" {
		t.Fatal(result, err)
	}
	secret, _ := client.CoreV1().Secrets(in.Namespace).Get(context.Background(), in.Name, metav1.GetOptions{})
	secret.Labels = map[string]string{"owner": "operator"}
	_, _ = client.CoreV1().Secrets(in.Namespace).Update(context.Background(), secret, metav1.UpdateOptions{})
	in.StringData["exporter.yaml"] = "replacement"
	if _, err := ensureSecret(context.Background(), client, "next", in, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	secret, _ = client.CoreV1().Secrets(in.Namespace).Get(context.Background(), in.Name, metav1.GetOptions{})
	if secret.Labels["owner"] != "operator" || string(secret.Data["exporter.yaml"]) != "replacement" {
		t.Fatal("incorrect Secret update")
	}
}

func TestSecretRolloutFailureAndRetryDoesNotRestartAgain(t *testing.T) {
	client := fake.NewClientset(&apps.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "collector", Namespace: "crane-system", Generation: 2}})
	in := api.KubernetesSecret{Namespace: "crane-system", Name: "config", RolloutDeployment: "collector"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result, err := ensureSecret(ctx, client, "same-operation", in, time.Millisecond)
	if err == nil || result.Secret != "verified" || result.Rollout != "failed" {
		t.Fatal(result, err)
	}
	deployment, _ := client.AppsV1().Deployments(in.Namespace).Get(context.Background(), "collector", metav1.GetOptions{})
	deployment.Status = apps.DeploymentStatus{ObservedGeneration: 2, UpdatedReplicas: 1, Replicas: 1, AvailableReplicas: 1}
	_, _ = client.AppsV1().Deployments(in.Namespace).UpdateStatus(context.Background(), deployment, metav1.UpdateOptions{})
	updates := 0
	client.PrependReactor("update", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "" {
			updates++
		}
		return false, nil, nil
	})
	result, err = ensureSecret(context.Background(), client, "same-operation", in, time.Millisecond)
	if err != nil || result.Rollout != "ready" || updates != 0 {
		t.Fatal(result, err, updates)
	}
}

func TestSecretErrorsDoNotExposeKubernetesBodies(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("get", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("synthetic-private-body")
	})
	result, err := ensureSecret(context.Background(), client, "id", api.KubernetesSecret{Namespace: "default", Name: "config"}, time.Millisecond)
	if err == nil || strings.Contains(err.Error(), "synthetic-private-body") || result.Secret != "failed" {
		t.Fatal("unsafe error")
	}
}

func TestSecretValidation(t *testing.T) {
	for _, in := range []api.KubernetesSecret{
		{Namespace: "--bad", Name: "config"},
		{Namespace: "default", Name: "config", Data: map[string][]byte{"key": {1}}, StringData: map[string]string{"key": "overlap"}},
		{Namespace: "default", Name: "config", StringData: map[string]string{"key": strings.Repeat("x", (1<<20)+1)}},
		{Namespace: "default", Name: "config", RolloutDeployment: "../bad"},
	} {
		if in.Normalize() == nil {
			t.Fatal("accepted invalid Secret")
		}
	}
	if _, err := (LocalHost{}).EnsureSecret(context.Background(), "id", api.KubernetesSecret{}); err == nil {
		t.Fatal("missing credentials accepted")
	}
}
