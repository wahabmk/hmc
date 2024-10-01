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

package webhook

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/Mirantis/hmc/api/v1alpha1"
	"github.com/Mirantis/hmc/test/objects/managedcluster"
	"github.com/Mirantis/hmc/test/objects/template"
	"github.com/Mirantis/hmc/test/scheme"
)

func TestClusterTemplateValidateDelete(t *testing.T) {
	ctx := context.Background()
	const namespace = "test"

	tpl := template.NewClusterTemplate(template.WithName("testTemplateFail"), template.WithNamespace(namespace))
	tplTest := template.NewClusterTemplate(template.WithName("testTemplate"), template.WithNamespace(namespace))

	tests := []struct {
		name            string
		template        *v1alpha1.ClusterTemplate
		existingObjects []runtime.Object
		err             string
		warnings        admission.Warnings
	}{
		{
			name:     "should fail if ManagedCluster objects exist in the same namespace",
			template: tpl,
			existingObjects: []runtime.Object{managedcluster.NewManagedCluster(
				managedcluster.WithNamespace(namespace),
				managedcluster.WithTemplate(tpl.Name),
			)},
			warnings: admission.Warnings{"The ClusterTemplate object can't be removed if ManagedCluster objects referencing it still exist"},
			err:      "template deletion is forbidden",
		},
		{
			name:     "should succeed if some ManagedCluster from another namespace references the template",
			template: tpl,
			existingObjects: []runtime.Object{managedcluster.NewManagedCluster(
				managedcluster.WithNamespace("new"),
				managedcluster.WithTemplate(tpl.Name),
			)},
		},
		{
			name:            "should be OK because of a different cluster",
			template:        tpl,
			existingObjects: []runtime.Object{managedcluster.NewManagedCluster()},
		},
		{
			name:            "should succeed",
			template:        template.NewClusterTemplate(),
			existingObjects: []runtime.Object{managedcluster.NewManagedCluster(managedcluster.WithTemplate(tplTest.Name))},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			c := fake.NewClientBuilder().
				WithScheme(scheme.Scheme).
				WithRuntimeObjects(tt.existingObjects...).
				WithIndex(tt.existingObjects[0], v1alpha1.TemplateKey, v1alpha1.ExtractTemplateName).
				Build()
			validator := &ClusterTemplateValidator{Client: c}
			warn, err := validator.ValidateDelete(ctx, tt.template)
			if tt.err != "" {
				g.Expect(err).To(MatchError(tt.err))
			} else {
				g.Expect(err).Should(Succeed())
			}

			if len(tt.warnings) > 0 {
				g.Expect(warn).To(Equal(tt.warnings))
			} else {
				g.Expect(warn).To(BeEmpty())
			}
		})
	}
}

func TestServiceTemplateValidateDelete(t *testing.T) {
	ctx := context.Background()
	tmpl := template.NewServiceTemplate(template.WithNamespace("mynamespace"), template.WithName("mytemplate"))

	tests := []struct {
		title           string
		template        *v1alpha1.ServiceTemplate
		existingObjects []runtime.Object
		warnings        admission.Warnings
		err             string
	}{
		{
			title:    "should fail if ManagedCluster exists in same namespace",
			template: tmpl,
			existingObjects: []runtime.Object{
				managedcluster.NewManagedCluster(
					managedcluster.WithNamespace(tmpl.Namespace),
					managedcluster.WithServiceTemplate(tmpl.Name),
				),
			},
			warnings: admission.Warnings{"The ServiceTemplate object can't be removed if ManagedCluster objects referencing it still exist"},
			err:      errTemplateDeletionForbidden.Error(),
		},
		{
			title:    "should success if managedCluster referencing ServiceTemplate is another namespace",
			template: tmpl,
			existingObjects: []runtime.Object{
				managedcluster.NewManagedCluster(
					managedcluster.WithNamespace("someothernamespace"),
					managedcluster.WithServiceTemplate(tmpl.Name),
				),
			},
		},
		{
			title:           "should be OK because of a different cluster",
			template:        tmpl,
			existingObjects: []runtime.Object{managedcluster.NewManagedCluster()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			g := NewWithT(t)

			c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithRuntimeObjects(tt.existingObjects...).Build()
			validator := &ServiceTemplateValidator{Client: c}
			warn, err := validator.ValidateDelete(ctx, tt.template)
			if tt.err != "" {
				g.Expect(err).To(MatchError(tt.err))
			} else {
				g.Expect(err).Should(Succeed())
			}

			if len(tt.warnings) > 0 {
				g.Expect(warn).To(Equal(tt.warnings))
			} else {
				g.Expect(warn).To(BeEmpty())
			}
		})
	}
}
