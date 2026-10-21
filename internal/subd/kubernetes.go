package subd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"time"

	"github.com/nabutabu/crane-oss/pkg/api"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

type secretHost interface {
	EnsureSecret(context.Context, string, api.KubernetesSecret) (*api.KubernetesResult, error)
}

func (h LocalHost) EnsureSecret(ctx context.Context, token string, secret api.KubernetesSecret) (*api.KubernetesResult, error) {
	failed := &api.KubernetesResult{Secret: "failed", Rollout: "not_requested"}
	if secret.RolloutDeployment != "" {
		failed.Rollout = "pending"
	}
	if h.Kubeconfig == "" {
		return failed, errors.New("local Kubernetes credentials are not configured")
	}
	config, err := clientcmd.BuildConfigFromFlags("", h.Kubeconfig)
	if err != nil {
		return failed, errors.New("cannot load local Kubernetes credentials")
	}
	config.Timeout = 30 * time.Second
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return failed, errors.New("cannot initialize local Kubernetes client")
	}
	return ensureSecret(ctx, client, token, secret, time.Second)
}

func ensureSecret(ctx context.Context, client kubernetes.Interface, token string, in api.KubernetesSecret, interval time.Duration) (*api.KubernetesResult, error) {
	result := &api.KubernetesResult{Secret: "failed", Rollout: "not_requested"}
	if in.RolloutDeployment != "" {
		result.Rollout = "pending"
	}
	if err := in.Normalize(); err != nil {
		return result, err
	}
	secrets := client.CoreV1().Secrets(in.Namespace)
	err := retry.OnError(retry.DefaultBackoff, func(err error) bool { return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) }, func() error {
		current, err := secrets.Get(ctx, in.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: in.Name, Namespace: in.Namespace}, Type: corev1.SecretType(in.Type), Data: in.Data}, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if current.Type == corev1.SecretType(in.Type) && maps.EqualFunc(current.Data, in.Data, bytes.Equal) {
			return nil
		}
		current.Type, current.Data = corev1.SecretType(in.Type), in.Data
		_, err = secrets.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return result, errors.New("Secret write failed")
	}
	current, err := secrets.Get(ctx, in.Name, metav1.GetOptions{})
	if err != nil || current.Type != corev1.SecretType(in.Type) || !maps.EqualFunc(current.Data, in.Data, bytes.Equal) {
		return result, errors.New("Secret verification failed")
	}
	result.Secret = "verified"
	if in.RolloutDeployment == "" {
		return result, nil
	}
	result.Rollout = "failed"
	deployments := client.AppsV1().Deployments(in.Namespace)
	sum := sha256.Sum256([]byte(token))
	annotation := hex.EncodeToString(sum[:])
	const key = "crane.io/secret-operation"
	var desired *appsv1.Deployment
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		deployment, err := deployments.Get(ctx, in.RolloutDeployment, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if deployment.Spec.Template.Annotations[key] == annotation {
			desired = deployment
			return nil
		}
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = map[string]string{}
		}
		deployment.Spec.Template.Annotations[key] = annotation
		desired, err = deployments.Update(ctx, deployment, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return result, errors.New("Deployment restart failed")
	}
	err = wait.PollUntilContextCancel(ctx, interval, true, func(ctx context.Context) (bool, error) {
		current, err := deployments.Get(ctx, in.RolloutDeployment, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if current.UID != desired.UID || current.Spec.Template.Annotations[key] != annotation {
			return false, errors.New("Deployment rollout superseded")
		}
		for _, condition := range current.Status.Conditions {
			if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded" {
				return false, errors.New("Deployment progress deadline exceeded")
			}
		}
		replicas := int32(1)
		if current.Spec.Replicas != nil {
			replicas = *current.Spec.Replicas
		}
		if replicas == 0 {
			return false, errors.New("Deployment has no replicas")
		}
		return current.Status.ObservedGeneration >= current.Generation && current.Status.UpdatedReplicas == replicas && current.Status.Replicas == replicas && current.Status.AvailableReplicas >= replicas, nil
	})
	if err != nil {
		return result, errors.New("Deployment rollout failed")
	}
	result.Rollout = "ready"
	return result, nil
}
