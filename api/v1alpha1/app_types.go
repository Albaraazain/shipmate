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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// S3Spec points a backup job at an S3-compatible object store. Credentials
// are never inlined: they are read from a Secret carrying the keys
// AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY.
type S3Spec struct {
	// endpoint is the S3-compatible endpoint URL, e.g. https://fsn1.your-objectstorage.com.
	// +required
	Endpoint string `json:"endpoint"`

	// bucket is the target bucket name.
	// +required
	Bucket string `json:"bucket"`

	// prefix is prepended to every object key so multiple apps can share
	// one bucket without collisions.
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// secretRef names a Secret in the App's namespace holding
	// AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY.
	// +required
	SecretRef corev1.LocalObjectReference `json:"secretRef"`
}

// BackupSpec declares a recurring backup executed as a CronJob. The command
// runs with the S3 connection details exposed as environment variables
// (S3_ENDPOINT, S3_BUCKET, S3_PREFIX plus the credential keys from secretRef),
// so any image that can talk to S3 works — pg_dump piped to a CLI uploader,
// restic, rclone, or a bespoke script.
type BackupSpec struct {
	// schedule is a standard cron expression, e.g. "0 3 * * *".
	// +required
	// +kubebuilder:validation:MinLength=9
	Schedule string `json:"schedule"`

	// image is the container image the backup job runs.
	// +required
	Image string `json:"image"`

	// command overrides the image entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// s3 is the destination object store.
	// +required
	S3 S3Spec `json:"s3"`
}

// DatabaseSpec provisions a single-instance Postgres alongside the app as a
// StatefulSet with a PersistentVolumeClaim. Credentials are generated once
// into a Secret named "<app>-db-credentials" on first reconcile and are
// never regenerated afterward — Postgres bakes the password into its data
// directory at initdb time, so replacing the Secret later would desync it
// from what the running database actually accepts. The same env vars are
// injected into the app container so any Postgres client library picks them
// up with zero extra configuration (DATABASE_URL, PGHOST, PGPORT, PGUSER,
// PGPASSWORD, PGDATABASE).
//
// This is deliberately a minimal, single-replica database, not a substitute
// for a dedicated Postgres operator (e.g. CloudNativePG): no replication, no
// automated failover, no point-in-time recovery. Use spec.backup alongside
// it for durability, and reach for a real Postgres operator when an app
// needs HA. The point here is demonstrating shipmate's own reconciliation
// and credential-lifecycle logic, not re-solving database HA.
type DatabaseSpec struct {
	// engine selects the database engine. Only "postgres" is supported today.
	// +kubebuilder:validation:Enum=postgres
	// +kubebuilder:default=postgres
	// +optional
	Engine string `json:"engine,omitempty"`

	// version is the image tag for the database engine.
	// +kubebuilder:default="16-alpine"
	// +optional
	Version string `json:"version,omitempty"`

	// storageSize is the size of the PersistentVolumeClaim for data.
	// +kubebuilder:default="1Gi"
	// +optional
	StorageSize resource.Quantity `json:"storageSize,omitempty"`

	// storageClassName selects a specific StorageClass; empty uses the
	// cluster default.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// TLSSpec requests a certificate from cert-manager for spec.domain via the
// named ClusterIssuer. shipmate only annotates the Ingress and sets its TLS
// block — it does not install, manage, or depend on cert-manager being
// present. If cert-manager's CRDs and webhook are not installed, the
// annotation is inert: the Ingress simply never receives a certificate,
// there is no reconcile failure. This is unlike a hypothetical ServiceMonitor
// integration, where creating an object of a CRD kind that doesn't exist in
// the cluster would hard-fail the reconcile — an annotation on a
// core/networking type has no such dependency.
type TLSSpec struct {
	// clusterIssuerName is the cert-manager ClusterIssuer to request
	// certificates from.
	// +required
	// +kubebuilder:validation:MinLength=1
	ClusterIssuerName string `json:"clusterIssuerName"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.tls) || self.domain != ''",message="spec.tls requires spec.domain to be set"

// AppSpec defines the desired state of App: one deployable web workload with
// optional ingress, TLS, a database, scheduled backups, and Prometheus
// scraping.
type AppSpec struct {
	// image is the container image to deploy.
	// +required
	Image string `json:"image"`

	// port is the container port the app listens on. The Service targets it
	// and, when a domain is set, the Ingress routes to it.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	// +optional
	Port int32 `json:"port,omitempty"`

	// replicas is the desired number of pods.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// domain exposes the app through an Ingress at this host. Leave empty
	// for cluster-internal apps; clearing it later removes the Ingress.
	// +optional
	Domain string `json:"domain,omitempty"`

	// tls requests a cert-manager certificate for domain. Requires domain to
	// be set; clearing it later removes the TLS configuration from the
	// Ingress (the Secret cert-manager created is left in place).
	// +optional
	TLS *TLSSpec `json:"tls,omitempty"`

	// env is passed verbatim to the app container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// resources are the app container's compute requests and limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// database provisions a single-instance Postgres and injects its
	// connection details into the app container; clearing it later removes
	// the database StatefulSet and Service but retains its Secret and
	// PersistentVolumeClaim (see DatabaseSpec).
	// +optional
	Database *DatabaseSpec `json:"database,omitempty"`

	// backup schedules recurring backups; clearing it removes the CronJob.
	// +optional
	Backup *BackupSpec `json:"backup,omitempty"`
}

// AppStatus defines the observed state of App.
type AppStatus struct {
	// conditions represent the current state of the App resource.
	// Condition types used by the controller:
	// - "Available": all desired replicas are ready
	// - "Progressing": a rollout is underway or replicas are still coming up
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// readyReplicas mirrors the underlying Deployment's ready replica count.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// url is the externally reachable address when a domain is configured.
	// +optional
	URL string `json:"url,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// App is the Schema for the apps API
type App struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of App
	// +required
	Spec AppSpec `json:"spec"`

	// status defines the observed state of App
	// +optional
	Status AppStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AppList contains a list of App
type AppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []App `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &App{}, &AppList{})
		return nil
	})
}
