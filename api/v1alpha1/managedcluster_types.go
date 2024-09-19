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

	FluxHelmChartNameKey = "helm.toolkit.fluxcd.io/name"
	HMCManagedLabelKey   = "hmc.mirantis.com/managed"
	HMCManagedLabelValue = "true"

	ClusterNameLabelKey = "cluster.x-k8s.io/cluster-name"
)

const (
	// ManagedClusterKind is the string representation of a ManagedCluster.
	ManagedClusterKind = "ManagedCluster"

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

// //////////////////////////////////////////////////////////////////
// ManagedClusterServiceSpec represents a service within managed cluster
type ManagedClusterServiceSpec struct {
	// Template is a reference to a Template object located in the same namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Template string `json:"template"`
	Install  bool   `json:"install"`
	// Config allows to provide parameters for template customization.
	// +optional
	Config apiextensionsv1.JSON `json:"config,omitempty"`
	// Values is the helm values to be passed to the template.
	// +optional
	Values apiextensionsv1.JSON `json:"values,omitempty"`
}

// //////////////////////////////////////////////////////////////////

// ManagedClusterSpec defines the desired state of ManagedCluster
type ManagedClusterSpec struct {
	// DryRun specifies whether the template should be applied after validation or only validated.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
	// Template is a reference to a Template object located in the same namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Template string `json:"template"`
	// Config allows to provide parameters for template customization.
	// If no Config provided, the field will be populated with the default values for
	// the template and DryRun will be enabled.
	// +optional
	Config *apiextensionsv1.JSON `json:"config,omitempty"`
	// Services is a list of services that could be installed on the
	// target cluster created with the template.
	// +optional
	Services []ManagedClusterServiceSpec `json:"services,omitempty"`
}

// ManagedClusterStatus defines the observed state of ManagedCluster
type ManagedClusterStatus struct {
	// ObservedGeneration is the last observed generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions contains details for the current state of the ManagedCluster
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hmc-deploy;deploy
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

func (in *ManagedCluster) HelmValues() (values map[string]interface{}, err error) {
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

//+kubebuilder:object:root=true

// ManagedClusterList contains a list of ManagedCluster
type ManagedClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ManagedCluster{}, &ManagedClusterList{})
}
