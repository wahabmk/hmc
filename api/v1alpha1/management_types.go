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
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	DefaultCoreHMCTemplate  = "hmc"
	DefaultCoreCAPITemplate = "cluster-api"

	DefaultCAPAConfig = `{
		"configSecret": {
           "name": "aws-variables"
        }
	}`

	ManagementName = "hmc"

	ManagementFinalizer = "hmc.mirantis.com/management"
)

var DefaultCoreConfiguration = Core{
	HMC: Component{
		Template: DefaultCoreHMCTemplate,
	},
	CAPI: Component{
		Template: DefaultCoreCAPITemplate,
	},
}

// ManagementSpec defines the desired state of Management
type ManagementSpec struct {
	// Core holds the core Management components that are mandatory.
	// If not specified, will be populated with the default values.
	Core *Core `json:"core,omitempty"`

	// Providers is the list of supported CAPI providers.
	Providers []Component `json:"providers,omitempty"`
}

// Core represents a structure describing core Management components.
type Core struct {
	// HMC represents the core HMC component and references the HMC template.
	HMC Component `json:"hmc"`
	// CAPI represents the core Cluster API component and references the Cluster API template.
	CAPI Component `json:"capi"`
}

// Component represents HMC management component
type Component struct {
	// Template is the name of the Template associated with this component.
	Template string `json:"template"`
	// Config allows to provide parameters for management component customization.
	// If no Config provided, the field will be populated with the default
	// values for the template.
	// +optional
	Config *apiextensionsv1.JSON `json:"config,omitempty"`
}

func (in *Component) HelmValues() (values map[string]interface{}, err error) {
	if in.Config != nil {
		err = yaml.Unmarshal(in.Config.Raw, &values)
	}
	return values, err
}

func (in *Component) HelmReleaseName() string {
	return in.Template
}

func (m *ManagementSpec) SetDefaults() bool {
	if m.Core != nil {
		return false
	}
	m.Core = &DefaultCoreConfiguration
	return true
}

func (m *ManagementSpec) SetProvidersDefaults() {
	m.Providers = []Component{
		{
			Template: "k0smotron",
		},
		{
			Template: "cluster-api-provider-aws",
			Config: &apiextensionsv1.JSON{
				Raw: []byte(DefaultCAPAConfig),
			},
		},
		{
			Template: "cluster-api-provider-azure",
		},
		{
			Template: "projectsveltos",
		},
	}
}

// ManagementStatus defines the observed state of Management
type ManagementStatus struct {
	// ObservedGeneration is the last observed generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// AvailableProviders holds all CAPI providers available on the Management cluster.
	AvailableProviders Providers `json:"availableProviders,omitempty"`
	// Components indicates the status of installed HMC components and CAPI providers.
	Components map[string]ComponentStatus `json:"components,omitempty"`
}

// ComponentStatus is the status of Management component installation
type ComponentStatus struct {
	// Success represents if a component installation was successful
	Success bool `json:"success,omitempty"`
	// Error stores as error message in case of failed installation
	Error string `json:"error,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hmc-mgmt;mgmt,scope=Cluster

// Management is the Schema for the managements API
type Management struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ManagementSpec   `json:"spec,omitempty"`
	Status ManagementStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ManagementList contains a list of Management
type ManagementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Management `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Management{}, &ManagementList{})
}
