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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/K0rdent/kcm/api/v1alpha1"
)

type MultiClusterServiceValidator struct {
	DynamicClient *dynamic.DynamicClient
	// Mapper        meta.RESTMapper
	client.Client
	SystemNamespace string
}

const invalidMultiClusterServiceMsg = "the MultiClusterService is invalid"

// SetupWebhookWithManager will setup the manager to manage the webhooks
func (v *MultiClusterServiceValidator) SetupWebhookWithManager(mgr ctrl.Manager) error {
	v.Client = mgr.GetClient()

	dc, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	v.DynamicClient = dc

	// dd := discovery.NewDiscoveryClientForConfigOrDie(mgr.GetConfig())
	// groupResources, err := restmapper.GetAPIGroupResources(dd)
	// if err != nil {
	// 	return err
	// }
	// mapper := restmapper.NewDiscoveryRESTMapper(groupResources)
	// v.Mapper = mapper

	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1alpha1.MultiClusterService{}).
		WithValidator(v).
		WithDefaulter(v).
		Complete()
}

var (
	_ webhook.CustomValidator = &MultiClusterServiceValidator{}
	_ webhook.CustomDefaulter = &MultiClusterServiceValidator{}
)

// Default implements webhook.Defaulter so a webhook will be registered for the type.
func (*MultiClusterServiceValidator) Default(_ context.Context, _ runtime.Object) error {
	return nil
}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type.
func (v *MultiClusterServiceValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	mcs, ok := obj.(*v1alpha1.MultiClusterService)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected MultiClusterService but got a %T", obj))
	}

	// if err := validateServices(ctx, v.Client, v.SystemNamespace, mcs.Spec.ServiceSpec.Services); err != nil {
	// 	return nil, fmt.Errorf("%s: %w", invalidMultiClusterServiceMsg, err)
	// }

	if err := validateServiceSpec(ctx, v.Client, v.DynamicClient /*v.Mapper,*/, v.SystemNamespace, &mcs.Spec.ServiceSpec, false); err != nil {
		return nil, fmt.Errorf("%s: %w", invalidMultiClusterServiceMsg, err)
	}

	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type.
func (v *MultiClusterServiceValidator) ValidateUpdate(ctx context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	mcs, ok := newObj.(*v1alpha1.MultiClusterService)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected MultiClusterService but got a %T", newObj))
	}

	if err := validateServiceSpec(ctx, v.Client, v.DynamicClient /*v.Mapper,*/, v.SystemNamespace, &mcs.Spec.ServiceSpec, false); err != nil {
		return nil, fmt.Errorf("%s: %w", invalidMultiClusterServiceMsg, err)
	}

	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type.
func (*MultiClusterServiceValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func getServiceTemplate(ctx context.Context, c client.Client, templateNamespace, templateName string) (tpl *v1alpha1.ServiceTemplate, err error) {
	tpl = new(v1alpha1.ServiceTemplate)
	return tpl, c.Get(ctx, client.ObjectKey{Namespace: templateNamespace, Name: templateName}, tpl)
}

// func validateServices(ctx context.Context, c client.Client, namespace string, services []v1alpha1.Service) (errs error) {
// 	for _, svc := range services {
// 		tpl, err := getServiceTemplate(ctx, c, namespace, svc.Template)
// 		if err != nil {
// 			errs = errors.Join(errs, err)
// 			continue
// 		}

// 		errs = errors.Join(errs, isTemplateValid(tpl.GetCommonStatus()))

// 		for _, v := range svc.ValuesFrom {
// 			if v.Namespace != namespace {
// 				errs = errors.Join(errs, fmt.Errorf("cannot refer to a ConfigMap or Secret in a namespace other than %s", namespace))
// 			}
// 		}
// 	}

// 	return errs
// }

func validateServiceSpec(ctx context.Context, c client.Client, dc *dynamic.DynamicClient /*, mapper meta.RESTMapper*/, namespace string, serviceSpec *v1alpha1.ServiceSpec, disallowCrossNamespaceRefs bool) (errs error) {
	// Validate TemplateResourceRefs
	for _, templRef := range serviceSpec.TemplateResourceRefs {
		if disallowCrossNamespaceRefs {
			if templRef.Resource.Namespace != namespace {
				errs = errors.Join(errs, fmt.Errorf("%s %q is in namespace %s, cannot refer to a resource in a namespace other than %s in .spec.serviceSpec.templateResourceRefs", templRef.Resource.Kind, templRef.Resource.Name, templRef.Resource.Namespace, namespace))
				continue
			}
		}

		gvk := templRef.Resource.GroupVersionKind()
		// fmt.Printf("###################### %s\n", gvk.Ve)
		m, err := c.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
		// m, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return fmt.Errorf("ERRRRRR 1: %w", err)
		}

		obj, err := dc.Resource(m.Resource).Namespace(namespace).Get(ctx, templRef.Resource.Name, v1.GetOptions{})
		fmt.Printf("\n>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>> obj = %s\n", obj)
		fmt.Printf(">>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>> err = %s\n\n", err)
		if err != nil {
			return fmt.Errorf("ERRRRRR 2: %w", err)
		}
		// // The resource being referred has to be either a ConfigMap or Secret
		// var obj client.Object
		// if templRef.Resource.Kind == "ConfigMap" {
		// 	obj = &corev1.ConfigMap{}
		// } else if templRef.Resource.Kind == "Secret" {
		// 	obj = &corev1.Secret{}
		// }

		// if err := c.Get(ctx, client.ObjectKey{Namespace: templRef.Resource.Namespace, Name: templRef.Resource.Name}, obj); err != nil {
		// 	errs = errors.Join(errs, err)
		// }
	}

	if errs != nil {
		return errs
	}

	// Validate Services
	for _, svc := range serviceSpec.Services {
		for _, v := range svc.ValuesFrom {
			if disallowCrossNamespaceRefs {
				if v.Namespace != namespace {
					errs = errors.Join(errs, fmt.Errorf("%s %q is in namespace %s, cannot refer to a resource in a namespace other than %s in .spec.serviceSpec.services[].valuesFrom", v.Kind, v.Name, v.Namespace, namespace))
					continue
				}
			}

			// The resource being referred has to be either a ConfigMap or Secret
			var obj client.Object
			if v.Kind == "ConfigMap" {
				obj = &corev1.ConfigMap{}
			} else if v.Kind == "Secret" {
				obj = &corev1.Secret{}
			}

			if err := c.Get(ctx, client.ObjectKey{Namespace: v.Namespace, Name: v.Name}, obj); err != nil {
				errs = errors.Join(errs, err)
			}
		}

		if errs != nil {
			return errs
		}

		tpl, err := getServiceTemplate(ctx, c, namespace, svc.Template)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}

		errs = errors.Join(errs, isTemplateValid(tpl.GetCommonStatus()))
	}

	return errs
}
