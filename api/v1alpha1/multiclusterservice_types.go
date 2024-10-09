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

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mclustersvc,scope=Cluster
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`

// MultiClusterService is the Schema for the multiclusterservice API.
type MultiClusterService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MultiClusterServiceSpec   `json:"spec,omitempty"`
	Status MultiClusterServiceStatus `json:"status,omitempty"`
}

// MultiClusterServiceSpec defines the desired state of MultiClusterService.
type MultiClusterServiceSpec struct {
	// ClusterSelector identifies target clusters to manage services on.
	// +optional
	ClusterSelector metav1.LabelSelector `json:"clusterSelector,omitempty"`
	// Tier sets the priority for the services defined in this spec.
	// Higher value means lower priority and vice versa.
	// In case of conflict with another object managing the service,
	// the one with lower priority will get to deploy its services.
	// +kubebuilder:default:=100
	// +kubebuilder:validation:Minimum=1
	// +optional
	Tier int32 `json:"tier,omitempty"`
	// StopOnConflict specifies what to do in case of a conflict.
	// E.g. If another object is already managing a service.
	// By default the remaining services will be deployed even if conflict is detected.
	// If set to true, the deployment will stop after encountering the first conflict.
	// +optional
	StopOnConflict bool `json:"stopOnConflict,omitempty"`
	// Services is a list of services created via ServiceTemplates
	// that could be installed on the target cluster.
	// +optional
	Services []ServiceSpec `json:"services,omitempty"`
}

// ServiceSpec represents a Service to be managed
type ServiceSpec struct {
	// Template is a reference to a Template object located in the same namespace.
	// +kubebuilder:validation:MinLength=1
	Template string `json:"template"`
	// Disable can be set to disable handling of this service.
	// +optional
	Disable bool `json:"disable"`
	// Name is the chart release.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Namespace is the namespace the release will be installed in.
	// It will default to Name if not provided.
	// +optional
	Namespace string `json:"namespace"`
	// Values is the helm values to be passed to the template.
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
}

// MultiClusterServiceStatus defines the observed state of MultiClusterService.
//
// TODO(https://github.com/Mirantis/hmc/issues/460):
// If this status ends up being common with ManagedClusterStatus,
// then make a common status struct that can be shared by both.
type MultiClusterServiceStatus struct {
	State string `json:"state,omitempty"`
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
