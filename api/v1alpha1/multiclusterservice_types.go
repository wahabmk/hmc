// Copyright 2024
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// MultiClusterServiceFinalizer is finalizer applied to MultiClusterService objects.
	MultiClusterServiceFinalizer = "hmc.mirantis.com/multicluster-service"
	// MultiClusterServiceKind is the string representation of a MultiClusterServiceKind.
	MultiClusterServiceKind = "MultiClusterService"
)

// ServiceSpec represents a Service to be managed
type ServiceSpec struct {
	// Values is the helm values to be passed to the template.
	Values *apiextensionsv1.JSON `json:"values,omitempty"`

	// +kubebuilder:validation:MinLength=1

	// Template is a reference to a Template object located in the same namespace.
	Template string `json:"template"`

	// +kubebuilder:validation:MinLength=1

	// Name is the chart release.
	Name string `json:"name"`
	// Namespace is the namespace the release will be installed in.
	// It will default to Name if not provided.
	Namespace string `json:"namespace,omitempty"`
	// Disable can be set to disable handling of this service.
	Disable bool `json:"disable,omitempty"`
}

// MultiClusterServiceSpec defines the desired state of MultiClusterService
type MultiClusterServiceSpec struct {
	// ClusterSelector identifies target clusters to manage services on.
	ClusterSelector metav1.LabelSelector `json:"clusterSelector,omitempty"`
	// Services is a list of services created via ServiceTemplates
	// that could be installed on the target cluster.
	Services []ServiceSpec `json:"services,omitempty"`

	// +kubebuilder:default:=100
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2147483646

	// Priority sets the priority for the services defined in this spec.
	// Higher value means higher priority and lower means lower.
	// In case of conflict with another object managing the service,
	// the one with higher priority will get to deploy its services.
	Priority int32 `json:"priority,omitempty"`

	// +kubebuilder:default:=false

	// StopOnConflict specifies what to do in case of a conflict.
	// E.g. If another object is already managing a service.
	// By default the remaining services will be deployed even if conflict is detected.
	// If set to true, the deployment will stop after encountering the first conflict.
	StopOnConflict bool `json:"stopOnConflict,omitempty"`
}

// MultiClusterServiceStatus defines the observed state of MultiClusterService
//
// TODO(https://github.com/Mirantis/hmc/issues/460):
// If this status ends up being common with ManagedClusterStatus,
// then make a common status struct that can be shared by both.
type MultiClusterServiceStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// MultiClusterService is the Schema for the multiclusterservices API
type MultiClusterService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MultiClusterServiceSpec   `json:"spec,omitempty"`
	Status MultiClusterServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MultiClusterServiceList contains a list of MultiClusterService
type MultiClusterServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MultiClusterService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MultiClusterService{}, &MultiClusterServiceList{})
}
