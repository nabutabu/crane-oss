package awscompute

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"

	"github.com/nabutabu/crane-oss/internal/capacity"
	"github.com/nabutabu/crane-oss/pkg/api"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

var attachmentZoneNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ReconcileAttachmentDominator creates only the zone-scoped workload and its
// binding to bootstrap-prepared RBAC. Existing mismatches fail closed.
func ReconcileAttachmentDominator(ctx context.Context, kube kubernetes.Interface, op api.Operation, deployment capacity.DeploymentRuntime, platformNode string) (api.BootstrapStepResult, error) {
	var result api.BootstrapStepResult
	if kube == nil || op.Kind != "attach_zone" || op.ClusterID != deployment.ClusterID ||
		platformNode == "" || len(op.ZoneID) > 48 || !attachmentZoneNameRE.MatchString(op.ZoneID) ||
		deployment.DominatorImage == "" || deployment.RuntimeSecretName != "crane-runtime" || deployment.TrustDomain == "" || deployment.Region == "" {
		return result, errors.New("attachment Dominator configuration is invalid")
	}
	const namespace = "crane-system"
	if _, err := kube.CoreV1().Secrets(namespace).Get(ctx, deployment.RuntimeSecretName, metav1.GetOptions{}); err != nil {
		return result, errors.New("deployment runtime Secret is unavailable")
	}
	name := "dominator-" + op.ZoneID
	labels := map[string]string{"app": name, "app.kubernetes.io/managed-by": "crane"}
	serviceAccount := &core.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}}
	automount := true
	serviceAccount.AutomountServiceAccountToken = &automount
	if err := ensureAttachmentServiceAccount(ctx, kube, serviceAccount); err != nil {
		return result, err
	}
	subject := rbac.Subject{Kind: "ServiceAccount", Name: name, Namespace: namespace}
	roleName := "crane-dominator-provisioner"
	binding := &rbac.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName + "-" + op.ZoneID, Namespace: "kube-system", Labels: labels},
		Subjects: []rbac.Subject{subject}, RoleRef: rbac.RoleRef{APIGroup: rbac.GroupName, Kind: "Role", Name: roleName}}
	if err := ensureAttachmentRoleBinding(ctx, kube, binding); err != nil {
		return result, err
	}
	clusterBinding := &rbac.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName + "-" + op.ZoneID, Labels: labels},
		Subjects: []rbac.Subject{subject}, RoleRef: rbac.RoleRef{APIGroup: rbac.GroupName, Kind: "ClusterRole", Name: roleName}}
	if err := ensureAttachmentClusterRoleBinding(ctx, kube, clusterBinding); err != nil {
		return result, err
	}
	replicas := int32(1)
	caMode := int32(0444)
	deploymentObject := &apps.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}, Spec: apps.DeploymentSpec{
		Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
		Template: core.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: core.PodSpec{
			ServiceAccountName: name, AutomountServiceAccountToken: &automount, NodeName: platformNode,
			Containers: []core.Container{{Name: "dominator", Image: deployment.DominatorImage, Command: []string{"/app/dominator"},
				Ports: []core.ContainerPort{{Name: "mtls", ContainerPort: 44000}},
				Env: []core.EnvVar{
					{Name: "CRANE_ZONE_ID", Value: op.ZoneID}, {Name: "CRANE_CLUSTER_ID", Value: op.ClusterID},
					{Name: "SPIFFE_TRUST_DOMAIN", Value: deployment.TrustDomain}, {Name: "SPIFFE_ENDPOINT_SOCKET", Value: "unix:///run/spire/sockets/agent.sock"},
					{Name: "ENVIRONMENT", Value: "production"}, {Name: "AWS_REGION", Value: deployment.Region},
				}, EnvFrom: []core.EnvFromSource{{SecretRef: &core.SecretEnvSource{LocalObjectReference: core.LocalObjectReference{Name: deployment.RuntimeSecretName}}}},
				VolumeMounts: []core.VolumeMount{{Name: "spire", MountPath: "/run/spire/sockets"}, {Name: "rds-ca", MountPath: "/etc/crane", ReadOnly: true}},
			}}, Volumes: []core.Volume{
				{Name: "spire", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: "/run/spire/sockets"}}},
				{Name: "rds-ca", VolumeSource: core.VolumeSource{ConfigMap: &core.ConfigMapVolumeSource{
					LocalObjectReference: core.LocalObjectReference{Name: "crane-rds-ca"}, DefaultMode: &caMode,
					Items: []core.KeyToPath{{Key: "rds-ca.pem", Path: "rds-ca.pem"}},
				}}},
			},
		}},
	}}
	if err := ensureAttachmentDeployment(ctx, kube, deploymentObject); err != nil {
		return result, err
	}
	service := &core.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}, Spec: core.ServiceSpec{
		Type: core.ServiceTypeClusterIP, Selector: map[string]string{"app": name}, Ports: []core.ServicePort{{Name: "mtls", Port: 44000, TargetPort: intstr.FromString("mtls")}},
	}}
	if err := ensureAttachmentService(ctx, kube, service); err != nil {
		return result, err
	}
	current, err := kube.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || current.Spec.ClusterIP == "" || current.Spec.ClusterIP == core.ClusterIPNone {
		return result, errors.New("attachment Dominator Service address is unavailable")
	}
	result.DominatorEndpoint = fmt.Sprintf("https://%s:44000", current.Spec.ClusterIP)
	result.DominatorIdentity = "spiffe://" + deployment.TrustDomain + "/dominator/" + op.ZoneID
	return result, nil
}

func attachmentOwned(meta metav1.ObjectMeta, expected map[string]string) bool {
	return meta.Labels["app"] == expected["app"] && meta.Labels["app.kubernetes.io/managed-by"] == "crane"
}

func ensureAttachmentServiceAccount(ctx context.Context, kube kubernetes.Interface, desired *core.ServiceAccount) error {
	_, err := kube.CoreV1().ServiceAccounts(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return errors.New("create attachment ServiceAccount")
	}
	current, err := kube.CoreV1().ServiceAccounts(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if err != nil || !attachmentOwned(current.ObjectMeta, desired.Labels) || current.AutomountServiceAccountToken == nil || !*current.AutomountServiceAccountToken {
		return errors.New("attachment ServiceAccount conflicts with deployment")
	}
	return nil
}

func ensureAttachmentRoleBinding(ctx context.Context, kube kubernetes.Interface, desired *rbac.RoleBinding) error {
	_, err := kube.RbacV1().RoleBindings(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return errors.New("create attachment RoleBinding")
	}
	current, err := kube.RbacV1().RoleBindings(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if err != nil || !attachmentOwned(current.ObjectMeta, desired.Labels) || current.RoleRef != desired.RoleRef ||
		len(current.Subjects) != 1 || current.Subjects[0] != desired.Subjects[0] {
		return errors.New("attachment RoleBinding conflicts with deployment")
	}
	return nil
}

func ensureAttachmentClusterRoleBinding(ctx context.Context, kube kubernetes.Interface, desired *rbac.ClusterRoleBinding) error {
	_, err := kube.RbacV1().ClusterRoleBindings().Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return errors.New("create attachment ClusterRoleBinding")
	}
	current, err := kube.RbacV1().ClusterRoleBindings().Get(ctx, desired.Name, metav1.GetOptions{})
	if err != nil || !attachmentOwned(current.ObjectMeta, desired.Labels) || current.RoleRef != desired.RoleRef ||
		len(current.Subjects) != 1 || current.Subjects[0] != desired.Subjects[0] {
		return errors.New("attachment ClusterRoleBinding conflicts with deployment")
	}
	return nil
}

func ensureAttachmentDeployment(ctx context.Context, kube kubernetes.Interface, desired *apps.Deployment) error {
	_, err := kube.AppsV1().Deployments(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return errors.New("create attachment Dominator Deployment")
	}
	current, err := kube.AppsV1().Deployments(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if err != nil || !attachmentOwned(current.ObjectMeta, desired.Labels) || current.Spec.Replicas == nil || *current.Spec.Replicas != 1 || current.Spec.Selector == nil ||
		!reflect.DeepEqual(current.Spec.Selector.MatchLabels, desired.Spec.Selector.MatchLabels) ||
		!reflect.DeepEqual(current.Spec.Template.Labels, desired.Spec.Template.Labels) ||
		current.Spec.Template.Spec.ServiceAccountName != desired.Spec.Template.Spec.ServiceAccountName ||
		current.Spec.Template.Spec.NodeName != desired.Spec.Template.Spec.NodeName ||
		len(current.Spec.Template.Spec.Containers) != 1 || !sameAttachmentContainer(current.Spec.Template.Spec.Containers[0], desired.Spec.Template.Spec.Containers[0]) ||
		!reflect.DeepEqual(current.Spec.Template.Spec.Volumes, desired.Spec.Template.Spec.Volumes) ||
		!reflect.DeepEqual(current.Spec.Template.Spec.AutomountServiceAccountToken, desired.Spec.Template.Spec.AutomountServiceAccountToken) {
		return errors.New("attachment Dominator Deployment conflicts with deployment")
	}
	return nil
}

func sameAttachmentContainer(current, desired core.Container) bool {
	return current.Name == desired.Name && current.Image == desired.Image &&
		reflect.DeepEqual(current.Command, desired.Command) &&
		reflect.DeepEqual(current.Env, desired.Env) &&
		reflect.DeepEqual(current.EnvFrom, desired.EnvFrom) &&
		reflect.DeepEqual(current.Ports, desired.Ports) &&
		reflect.DeepEqual(current.VolumeMounts, desired.VolumeMounts)
}

func ensureAttachmentService(ctx context.Context, kube kubernetes.Interface, desired *core.Service) error {
	_, err := kube.CoreV1().Services(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return errors.New("create attachment Dominator Service")
	}
	current, err := kube.CoreV1().Services(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if err != nil || !attachmentOwned(current.ObjectMeta, desired.Labels) || current.Spec.Type != core.ServiceTypeClusterIP || current.Spec.Selector["app"] != desired.Spec.Selector["app"] ||
		len(current.Spec.Ports) != 1 || current.Spec.Ports[0].Port != 44000 || current.Spec.Ports[0].TargetPort.StrVal != "mtls" {
		return errors.New("attachment Dominator Service conflicts with deployment")
	}
	return nil
}
