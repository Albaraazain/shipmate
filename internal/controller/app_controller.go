/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	shipmatev1alpha1 "github.com/Albaraazain/shipmate/api/v1alpha1"
)

const (
	managedByLabel = "shipmate"

	// portName links the container port, Service target port, and Ingress
	// backend port by name, so the numeric port lives in exactly one place.
	portName = "http"

	conditionAvailable   = "Available"
	conditionProgressing = "Progressing"
)

// AppReconciler reconciles an App by driving a Deployment, a Service, and —
// when the spec asks for them — an Ingress and a backup CronJob. All children
// carry an owner reference, so deleting the App garbage-collects everything.
// No finalizer is used on purpose: the controller owns no external state
// (backup objects in S3 are deliberately retained after app deletion).
type AppReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=shipmate.florya.co,resources=apps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=shipmate.florya.co,resources=apps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=shipmate.florya.co,resources=apps/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile converges the cluster toward the App spec: it creates or updates
// the always-present children (Deployment, Service), creates, updates, or
// deletes the optional ones (Ingress, backup CronJob) depending on whether
// their spec fields are set, then reports readiness through status conditions.
func (r *AppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	app := &shipmatev1alpha1.App{}
	if err := r.Get(ctx, req.NamespacedName, app); err != nil {
		// Not found means the App was deleted; owner references handle cleanup.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	deployment, err := r.reconcileDeployment(ctx, app)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling Deployment: %w", err)
	}
	if err := r.reconcileService(ctx, app); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling Service: %w", err)
	}
	if err := r.reconcileIngress(ctx, app); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling Ingress: %w", err)
	}
	if err := r.reconcileBackupCronJob(ctx, app); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling backup CronJob: %w", err)
	}

	if err := r.updateStatus(ctx, app, deployment); err != nil {
		if apierrors.IsConflict(err) {
			// A concurrent status write won the race; requeue and retry on
			// a fresh copy instead of surfacing a spurious error.
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	log.V(1).Info("reconciled", "readyReplicas", deployment.Status.ReadyReplicas)
	return ctrl.Result{}, nil
}

// selectorLabels are the immutable pod-selector labels. They must never
// change for an existing App, so they contain only the App's name.
func selectorLabels(app *shipmatev1alpha1.App) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       app.Name,
		"app.kubernetes.io/managed-by": managedByLabel,
	}
}

func (r *AppReconciler) reconcileDeployment(ctx context.Context, app *shipmatev1alpha1.App) (*appsv1.Deployment, error) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: app.Name, Namespace: app.Namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		labels := selectorLabels(app)
		deployment.Labels = labels
		// Selector is immutable; it is only ever set to this same value.
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deployment.Spec.Replicas = app.Spec.Replicas
		deployment.Spec.Template.Labels = labels
		deployment.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:      "app",
			Image:     app.Spec.Image,
			Env:       app.Spec.Env,
			Resources: app.Spec.Resources,
			Ports: []corev1.ContainerPort{{
				Name:          portName,
				ContainerPort: app.Spec.Port,
			}},
		}}
		return ctrl.SetControllerReference(app, deployment, r.Scheme)
	})
	return deployment, err
}

func (r *AppReconciler) reconcileService(ctx context.Context, app *shipmatev1alpha1.App) error {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: app.Name, Namespace: app.Namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		service.Labels = selectorLabels(app)
		service.Spec.Selector = selectorLabels(app)
		service.Spec.Ports = []corev1.ServicePort{{
			Name:       portName,
			Port:       80,
			TargetPort: intstr.FromString(portName),
		}}
		return ctrl.SetControllerReference(app, service, r.Scheme)
	})
	return err
}

// reconcileIngress creates or updates the Ingress when spec.domain is set and
// deletes it when the domain is cleared, so toggling exposure is a pure spec
// edit with no manual cleanup.
func (r *AppReconciler) reconcileIngress(ctx context.Context, app *shipmatev1alpha1.App) error {
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: app.Name, Namespace: app.Namespace},
	}

	if app.Spec.Domain == "" {
		return r.deleteIfOwned(ctx, app, ingress)
	}

	pathType := networkingv1.PathTypePrefix
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ingress, func() error {
		ingress.Labels = selectorLabels(app)
		ingress.Spec.Rules = []networkingv1.IngressRule{{
			Host: app.Spec.Domain,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{
						Path:     "/",
						PathType: &pathType,
						Backend: networkingv1.IngressBackend{
							Service: &networkingv1.IngressServiceBackend{
								Name: app.Name,
								Port: networkingv1.ServiceBackendPort{Name: portName},
							},
						},
					}},
				},
			},
		}}
		return ctrl.SetControllerReference(app, ingress, r.Scheme)
	})
	return err
}

// reconcileBackupCronJob mirrors reconcileIngress: present while spec.backup
// is set, removed when it is cleared.
func (r *AppReconciler) reconcileBackupCronJob(ctx context.Context, app *shipmatev1alpha1.App) error {
	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: app.Name + "-backup", Namespace: app.Namespace},
	}

	backup := app.Spec.Backup
	if backup == nil {
		return r.deleteIfOwned(ctx, app, cronJob)
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cronJob, func() error {
		cronJob.Labels = selectorLabels(app)
		cronJob.Spec.Schedule = backup.Schedule
		cronJob.Spec.JobTemplate.Spec.Template.Labels = selectorLabels(app)
		cronJob.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
		cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:    "backup",
			Image:   backup.Image,
			Command: backup.Command,
			Env: []corev1.EnvVar{
				{Name: "S3_ENDPOINT", Value: backup.S3.Endpoint},
				{Name: "S3_BUCKET", Value: backup.S3.Bucket},
				{Name: "S3_PREFIX", Value: backup.S3.Prefix},
			},
			EnvFrom: []corev1.EnvFromSource{{
				SecretRef: &corev1.SecretEnvSource{LocalObjectReference: backup.S3.SecretRef},
			}},
		}}
		return ctrl.SetControllerReference(app, cronJob, r.Scheme)
	})
	return err
}

// deleteIfOwned removes an optional child that the spec no longer asks for.
// It only deletes objects this App actually owns, so a same-named object
// created by someone else is left alone.
func (r *AppReconciler) deleteIfOwned(ctx context.Context, app *shipmatev1alpha1.App, obj client.Object) error {
	if err := r.Get(ctx, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}, obj); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(obj, app) {
		return nil
	}
	if err := r.Delete(ctx, obj); err != nil {
		return client.IgnoreNotFound(err)
	}
	logf.FromContext(ctx).Info("deleted child no longer requested by spec",
		"kind", fmt.Sprintf("%T", obj), "name", obj.GetName())
	return nil
}

func (r *AppReconciler) updateStatus(ctx context.Context, app *shipmatev1alpha1.App, deployment *appsv1.Deployment) error {
	before := app.Status.DeepCopy()

	desired := int32(1)
	if app.Spec.Replicas != nil {
		desired = *app.Spec.Replicas
	}
	ready := deployment.Status.ReadyReplicas

	app.Status.ReadyReplicas = ready

	app.Status.URL = ""
	if app.Spec.Domain != "" {
		app.Status.URL = "http://" + app.Spec.Domain
	}

	available := metav1.ConditionFalse
	availableReason := "ReplicasNotReady"
	availableMessage := fmt.Sprintf("%d/%d replicas ready", ready, desired)
	if ready >= desired {
		available = metav1.ConditionTrue
		availableReason = "AllReplicasReady"
		if desired == 0 {
			availableReason = "ScaledToZero"
			availableMessage = "app is scaled to zero replicas"
		}
	}
	meta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
		Type:               conditionAvailable,
		Status:             available,
		Reason:             availableReason,
		Message:            availableMessage,
		ObservedGeneration: app.Generation,
	})

	progressing := metav1.ConditionTrue
	progressingReason := "RolloutInProgress"
	if ready >= desired {
		progressing = metav1.ConditionFalse
		progressingReason = "RolloutComplete"
	}
	meta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
		Type:               conditionProgressing,
		Status:             progressing,
		Reason:             progressingReason,
		Message:            availableMessage,
		ObservedGeneration: app.Generation,
	})

	// Skip the write when nothing changed: every owned-object event lands
	// here, and an unconditional update would cost a wasted PUT plus one
	// spurious follow-up reconcile per event.
	if equality.Semantic.DeepEqual(before, &app.Status) {
		return nil
	}
	return r.Status().Update(ctx, app)
}

// SetupWithManager sets up the controller with the Manager. Owns() registers
// watches on every child type, so drift in a child (manual edit, deletion)
// triggers reconciliation immediately instead of waiting for a resync.
func (r *AppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&shipmatev1alpha1.App{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&batchv1.CronJob{}).
		Named("app").
		Complete(r)
}
