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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	shipmatev1alpha1 "github.com/Albaraazain/shipmate/api/v1alpha1"
)

var _ = Describe("App Controller", func() {
	const (
		resourceName      = "test-app"
		resourceNamespace = "default"
	)

	ctx := context.Background()

	appKey := types.NamespacedName{Name: resourceName, Namespace: resourceNamespace}
	backupKey := types.NamespacedName{Name: resourceName + "-backup", Namespace: resourceNamespace}

	var reconciler *AppReconciler

	// reconcileApp runs a single reconcile pass and asserts it succeeds.
	reconcileApp := func() {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: appKey})
		Expect(err).NotTo(HaveOccurred())
	}

	// updateApp fetches the latest App, applies mutate, saves it, and
	// reconciles — mirroring a user editing the spec with kubectl.
	updateApp := func(mutate func(*shipmatev1alpha1.App)) {
		GinkgoHelper()
		app := &shipmatev1alpha1.App{}
		Expect(k8sClient.Get(ctx, appKey, app)).To(Succeed())
		mutate(app)
		Expect(k8sClient.Update(ctx, app)).To(Succeed())
		reconcileApp()
	}

	BeforeEach(func() {
		reconciler = &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		app := &shipmatev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
			Spec: shipmatev1alpha1.AppSpec{
				Image: "nginx:1.27",
			},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
	})

	AfterEach(func() {
		app := &shipmatev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace}}
		Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		// envtest runs no garbage collector, so owner-reference cleanup never
		// fires; remove the children explicitly to isolate the tests.
		children := []client.Object{
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace}},
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace}},
			&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace}},
			&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-backup", Namespace: resourceNamespace}},
		}
		for _, child := range children {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, child))).To(Succeed())
		}
	})

	It("creates a Deployment and Service for a minimal spec", func() {
		reconcileApp()

		deployment := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, appKey, deployment)).To(Succeed())
		Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.27"))
		Expect(*deployment.Spec.Replicas).To(Equal(int32(1)), "replicas should default to 1")
		Expect(metav1.IsControlledBy(deployment, currentApp(ctx, appKey))).To(BeTrue())

		service := &corev1.Service{}
		Expect(k8sClient.Get(ctx, appKey, service)).To(Succeed())
		Expect(service.Spec.Ports[0].Port).To(Equal(int32(80)))
	})

	It("creates no Ingress or CronJob when domain and backup are unset", func() {
		reconcileApp()

		Expect(errors.IsNotFound(k8sClient.Get(ctx, appKey, &networkingv1.Ingress{}))).To(BeTrue())
		Expect(errors.IsNotFound(k8sClient.Get(ctx, backupKey, &batchv1.CronJob{}))).To(BeTrue())
	})

	It("adds an Ingress when a domain is set and removes it when cleared", func() {
		reconcileApp()

		updateApp(func(app *shipmatev1alpha1.App) { app.Spec.Domain = "demo.florya.co" })

		ingress := &networkingv1.Ingress{}
		Expect(k8sClient.Get(ctx, appKey, ingress)).To(Succeed())
		Expect(ingress.Spec.Rules[0].Host).To(Equal("demo.florya.co"))

		fetched := currentApp(ctx, appKey)
		Expect(fetched.Status.URL).To(Equal("http://demo.florya.co"))

		updateApp(func(app *shipmatev1alpha1.App) { app.Spec.Domain = "" })

		Expect(errors.IsNotFound(k8sClient.Get(ctx, appKey, &networkingv1.Ingress{}))).To(BeTrue())
		Expect(currentApp(ctx, appKey).Status.URL).To(BeEmpty())
	})

	It("adds a backup CronJob when backup is set and removes it when cleared", func() {
		reconcileApp()

		updateApp(func(app *shipmatev1alpha1.App) {
			app.Spec.Backup = &shipmatev1alpha1.BackupSpec{
				Schedule: "0 3 * * *",
				Image:    "backup-tool:latest",
				S3: shipmatev1alpha1.S3Spec{
					Endpoint:  "https://fsn1.example.com",
					Bucket:    "backups",
					Prefix:    "test-app/",
					SecretRef: corev1.LocalObjectReference{Name: "s3-creds"},
				},
			}
		})

		cronJob := &batchv1.CronJob{}
		Expect(k8sClient.Get(ctx, backupKey, cronJob)).To(Succeed())
		Expect(cronJob.Spec.Schedule).To(Equal("0 3 * * *"))
		container := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
		Expect(container.Env).To(ContainElement(corev1.EnvVar{Name: "S3_BUCKET", Value: "backups"}))
		Expect(container.EnvFrom[0].SecretRef.Name).To(Equal("s3-creds"))

		updateApp(func(app *shipmatev1alpha1.App) { app.Spec.Backup = nil })

		Expect(errors.IsNotFound(k8sClient.Get(ctx, backupKey, &batchv1.CronJob{}))).To(BeTrue())
	})

	It("corrects drift when a child is mutated out from under the spec", func() {
		reconcileApp()

		deployment := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, appKey, deployment)).To(Succeed())
		deployment.Spec.Template.Spec.Containers[0].Image = "tampered:latest"
		Expect(k8sClient.Update(ctx, deployment)).To(Succeed())

		reconcileApp()

		Expect(k8sClient.Get(ctx, appKey, deployment)).To(Succeed())
		Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.27"))
	})

	It("reports Available=False while replicas are not ready", func() {
		reconcileApp()

		app := currentApp(ctx, appKey)
		available := meta.FindStatusCondition(app.Status.Conditions, conditionAvailable)
		Expect(available).NotTo(BeNil())
		// envtest runs no kubelet, so pods never become ready.
		Expect(available.Status).To(Equal(metav1.ConditionFalse))
		Expect(available.Reason).To(Equal("ReplicasNotReady"))

		progressing := meta.FindStatusCondition(app.Status.Conditions, conditionProgressing)
		Expect(progressing).NotTo(BeNil())
		Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
	})

	It("does not delete a same-named Ingress it does not own", func() {
		foreign := &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
			Spec: networkingv1.IngressSpec{
				DefaultBackend: &networkingv1.IngressBackend{
					Service: &networkingv1.IngressServiceBackend{
						Name: "something-else",
						Port: networkingv1.ServiceBackendPort{Number: 80},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())

		reconcileApp() // domain unset → controller wants no Ingress

		Expect(k8sClient.Get(ctx, appKey, &networkingv1.Ingress{})).To(Succeed(),
			"foreign Ingress must survive reconciliation")
	})
})

// currentApp fetches the latest App state or fails the test.
func currentApp(ctx context.Context, key types.NamespacedName) *shipmatev1alpha1.App {
	GinkgoHelper()
	app := &shipmatev1alpha1.App{}
	Expect(k8sClient.Get(ctx, key, app)).To(Succeed())
	return app
}
