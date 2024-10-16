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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	BlockingFinalizer       = "hmc.mirantis.com/cleanup"
	ManagedClusterFinalizer = "hmc.mirantis.com/managed-cluster"

	FluxHelmChartNameKey      = "helm.toolkit.fluxcd.io/name"
	FluxHelmChartNamespaceKey = "helm.toolkit.fluxcd.io/namespace"

	HMCManagedLabelKey   = "hmc.mirantis.com/managed"
	HMCManagedLabelValue = "true"

	ClusterNameLabelKey = "cluster.x-k8s.io/cluster-name"
)

const (
	// ManagedClusterKind is the string representation of a ManagedCluster.
	ManagedClusterKind = "ManagedCluster"

	// CredentialReadyCondition indicates if referenced Credential exists and has Ready state
	CredentialReadyCondition = "CredentialReady"
	// TemplateReadyCondition indicates the referenced Template exists and valid.
	TemplateReadyCondition = "TemplateReady"
	// HelmChartReadyCondition indicates the corresponding HelmChart is valid and ready.
	HelmChartReadyCondition = "HelmChartReady"
	// HelmReleaseReadyCondition indicates the corresponding HelmRelease is ready and fully reconciled.
	HelmReleaseReadyCondition = "HelmReleaseReady"
	// ReadyCondition indicates the ManagedCluster is ready and fully reconciled.
	ReadyCondition string = "Ready"
)

const (
	// SucceededReason indicates a condition or event observed a success, for example when declared desired state
	// matches actual state, or a performed action succeeded.
	SucceededReason string = "Succeeded"

	// FailedReason indicates a condition or event observed a failure, for example when declared state does not match
	// actual state, or a performed action failed.
	FailedReason string = "Failed"

	// ProgressingReason indicates a condition or event observed progression, for example when the reconciliation of a
	// resource or an action has started.
	ProgressingReason string = "Progressing"
)

// ManagedClusterSpec defines the desired state of ManagedCluster
type ManagedClusterSpec struct {
	// Config allows to provide parameters for template customization.
	// If no Config provided, the field will be populated with the default values for
	// the template and DryRun will be enabled.
	Config *apiextensionsv1.JSON `json:"config,omitempty"`

	// +kubebuilder:validation:MinLength=1

	// Template is a reference to a Template object located in the same namespace.
	Template   string `json:"template"`
	Credential string `json:"credential,omitempty"`
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
	// DryRun specifies whether the template should be applied after validation or only validated.
	DryRun bool `json:"dryRun,omitempty"`

	// +kubebuilder:default:=false

	// StopOnConflict specifies what to do in case of a conflict.
	// E.g. If another object is already managing a service.
	// By default the remaining services will be deployed even if conflict is detected.
	// If set to true, the deployment will stop after encountering the first conflict.
	StopOnConflict bool `json:"stopOnConflict,omitempty"`
}

// ManagedClusterStatus defines the observed state of ManagedCluster
type ManagedClusterStatus struct {
	// Conditions contains details for the current state of the ManagedCluster
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ObservedGeneration is the last observed generation.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mcluster;mcl
// +kubebuilder:printcolumn:name="ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description="Ready",priority=0
// +kubebuilder:printcolumn:name="status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message",description="Status",priority=0
// +kubebuilder:printcolumn:name="dryRun",type="string",JSONPath=".spec.dryRun",description="Dry Run",priority=1

// ManagedCluster is the Schema for the managedclusters API
type ManagedCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ManagedClusterSpec   `json:"spec,omitempty"`
	Status ManagedClusterStatus `json:"status,omitempty"`
}

func (in *ManagedCluster) HelmValues() (values map[string]any, err error) {
	if in.Spec.Config != nil {
		err = yaml.Unmarshal(in.Spec.Config.Raw, &values)
	}
	return values, err
}

func (in *ManagedCluster) GetConditions() *[]metav1.Condition {
	return &in.Status.Conditions
}

func (in *ManagedCluster) InitConditions() {
	apimeta.SetStatusCondition(in.GetConditions(), metav1.Condition{
		Type:    TemplateReadyCondition,
		Status:  metav1.ConditionUnknown,
		Reason:  ProgressingReason,
		Message: "Template is not yet ready",
	})
	apimeta.SetStatusCondition(in.GetConditions(), metav1.Condition{
		Type:    HelmChartReadyCondition,
		Status:  metav1.ConditionUnknown,
		Reason:  ProgressingReason,
		Message: "HelmChart is not yet ready",
	})
	if !in.Spec.DryRun {
		apimeta.SetStatusCondition(in.GetConditions(), metav1.Condition{
			Type:    HelmReleaseReadyCondition,
			Status:  metav1.ConditionUnknown,
			Reason:  ProgressingReason,
			Message: "HelmRelease is not yet ready",
		})
	}
	apimeta.SetStatusCondition(in.GetConditions(), metav1.Condition{
		Type:    ReadyCondition,
		Status:  metav1.ConditionUnknown,
		Reason:  ProgressingReason,
		Message: "ManagedCluster is not yet ready",
	})
}

// +kubebuilder:object:root=true

// ManagedClusterList contains a list of ManagedCluster
type ManagedClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ManagedCluster{}, &ManagedClusterList{})
}
