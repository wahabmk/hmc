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

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hmc "github.com/Mirantis/hmc/api/v1alpha1"
	"github.com/Mirantis/hmc/internal/certmanager"
	"github.com/Mirantis/hmc/internal/helm"
	"github.com/Mirantis/hmc/internal/utils"
)

// ManagementReconciler reconciles a Management object
type ManagementReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Config          *rest.Config
	SystemNamespace string
}

func (r *ManagementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx).WithValues("ManagementController", req.NamespacedName)
	log.IntoContext(ctx, l)
	l.Info("Reconciling Management")
	management := &hmc.Management{}
	if err := r.Get(ctx, req.NamespacedName, management); err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("Management not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		l.Error(err, "Failed to get Management")
		return ctrl.Result{}, err
	}

	if !management.DeletionTimestamp.IsZero() {
		l.Info("Deleting Management")
		return r.Delete(ctx, management)
	}

	return r.Update(ctx, management)
}

func (r *ManagementReconciler) Update(ctx context.Context, management *hmc.Management) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	finalizersUpdated := controllerutil.AddFinalizer(management, hmc.ManagementFinalizer)
	if finalizersUpdated {
		if err := r.Client.Update(ctx, management); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update Management %s: %w", management.Name, err)
		}
		return ctrl.Result{}, nil
	}

	// TODO: this is also implemented in admission but we have to keep it in controller as well
	// to set defaults before the admission is started
	if changed := management.Spec.SetDefaults(); changed {
		l.Info("Applying default core configuration")
		return ctrl.Result{}, r.Client.Update(ctx, management)
	}

	var errs error
	detectedProviders := hmc.Providers{}
	detectedComponents := make(map[string]hmc.ComponentStatus)

	err := r.enableAdditionalComponents(ctx, management)
	if err != nil {
		l.Error(err, "failed to enable additional HMC components")
		return ctrl.Result{}, err
	}

	components := wrappedComponents(management)
	for _, component := range components {
		fmt.Printf("\n>>>>>>>>>>>>>>>>>>>>>>>>> component.Template = %s >>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>\n", component.Template)
		template := &hmc.Template{}
		err := r.Get(ctx, types.NamespacedName{
			// WAHAB: 3
			Namespace: r.SystemNamespace,
			Name:      component.Template,
		}, template)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to get Template %s/%s: %s", r.SystemNamespace, component.Template, err)
			updateComponentsStatus(detectedComponents, &detectedProviders, component.Template, template.Status, errMsg)
			errs = errors.Join(errs, errors.New(errMsg))
			continue
		}
		if !template.Status.Valid {
			errMsg := fmt.Sprintf("Template %s/%s is not marked as valid", r.SystemNamespace, component.Template)
			updateComponentsStatus(detectedComponents, &detectedProviders, component.Template, template.Status, errMsg)
			errs = errors.Join(errs, errors.New(errMsg))
			continue
		}

		_, _, err = helm.ReconcileHelmRelease2(ctx, r.Client, component.HelmReleaseName(), r.SystemNamespace, helm.ReconcileHelmReleaseOpts{
			Values:          component.Config,
			ChartRef:        template.Status.ChartRef,
			DependsOn:       component.dependsOn,
			TargetNamespace: component.targetNamespace,
			CreateNamespace: component.createNamespace,
		})
		if err != nil {
			errMsg := fmt.Sprintf("error reconciling HelmRelease %s/%s: %s", r.SystemNamespace, component.Template, err)
			updateComponentsStatus(detectedComponents, &detectedProviders, component.Template, template.Status, errMsg)
			errs = errors.Join(errs, errors.New(errMsg))
			continue
		}
		updateComponentsStatus(detectedComponents, &detectedProviders, component.Template, template.Status, "")
	}

	management.Status.ObservedGeneration = management.Generation
	management.Status.AvailableProviders = detectedProviders
	management.Status.Components = detectedComponents
	if err := r.Status().Update(ctx, management); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to update status for Management %s: %w",
			management.Name, err))
	}
	if errs != nil {
		l.Error(errs, "Multiple errors during Management reconciliation")
		return ctrl.Result{}, errs
	}
	return ctrl.Result{}, nil
}

func (r *ManagementReconciler) Delete(ctx context.Context, management *hmc.Management) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	listOpts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{hmc.HMCManagedLabelKey: hmc.HMCManagedLabelValue}),
	}
	if err := r.removeHelmReleases(ctx, management.Spec.Core.HMC.HelmReleaseName(), listOpts); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.removeHelmCharts(ctx, listOpts); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.removeHelmRepositories(ctx, listOpts); err != nil {
		return ctrl.Result{}, err
	}

	// Removing finalizer in the end of cleanup
	l.Info("Removing Management finalizer")
	if controllerutil.RemoveFinalizer(management, hmc.ManagementFinalizer) {
		return ctrl.Result{}, r.Client.Update(ctx, management)
	}
	return ctrl.Result{}, nil
}

func (r *ManagementReconciler) removeHelmReleases(ctx context.Context, hmcReleaseName string, opts *client.ListOptions) error {
	l := log.FromContext(ctx)
	l.Info("Suspending HMC Helm Release reconciles")
	hmcRelease := &fluxv2.HelmRelease{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.SystemNamespace, Name: hmcReleaseName}, hmcRelease)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && !hmcRelease.Spec.Suspend {
		hmcRelease.Spec.Suspend = true
		if err := r.Client.Update(ctx, hmcRelease); err != nil {
			return err
		}
	}
	l.Info("Ensuring all HelmReleases owned by HMC are removed")
	gvk := fluxv2.GroupVersion.WithKind(fluxv2.HelmReleaseKind)
	if err := utils.EnsureDeleteAllOf(ctx, r.Client, gvk, opts); err != nil {
		l.Error(err, "Not all HelmReleases owned by HMC are removed")
		return err
	}
	return nil
}

func (r *ManagementReconciler) removeHelmCharts(ctx context.Context, opts *client.ListOptions) error {
	l := log.FromContext(ctx)
	l.Info("Ensuring all HelmCharts owned by HMC are removed")
	gvk := sourcev1.GroupVersion.WithKind(sourcev1.HelmChartKind)
	if err := utils.EnsureDeleteAllOf(ctx, r.Client, gvk, opts); err != nil {
		l.Error(err, "Not all HelmCharts owned by HMC are removed")
		return err
	}
	return nil
}

func (r *ManagementReconciler) removeHelmRepositories(ctx context.Context, opts *client.ListOptions) error {
	l := log.FromContext(ctx)
	l.Info("Ensuring all HelmRepositories owned by HMC are removed")
	gvk := sourcev1.GroupVersion.WithKind(sourcev1.HelmRepositoryKind)
	if err := utils.EnsureDeleteAllOf(ctx, r.Client, gvk, opts); err != nil {
		l.Error(err, "Not all HelmRepositories owned by HMC are removed")
		return err
	}
	return nil
}

type component struct {
	hmc.Component

	// helm release dependencies
	dependsOn       []meta.NamespacedObjectReference
	targetNamespace string
	createNamespace bool
}

func wrappedComponents(mgmt *hmc.Management) (components []component) {
	if mgmt.Spec.Core == nil {
		return
	}

	components = append(components, component{Component: mgmt.Spec.Core.HMC})
	components = append(components, component{Component: mgmt.Spec.Core.CAPI, dependsOn: []meta.NamespacedObjectReference{{Name: mgmt.Spec.Core.HMC.Template}}})

	for i := range mgmt.Spec.Providers {
		c := component{Component: mgmt.Spec.Providers[i], dependsOn: []meta.NamespacedObjectReference{{Name: mgmt.Spec.Core.CAPI.Template}}}

		if mgmt.Spec.Providers[i].Template == hmc.ProviderSveltosName {
			c.targetNamespace = hmc.ProviderSveltosTargetNamespace
			c.createNamespace = hmc.ProviderSveltosCreateNamespace
		}
		components = append(components, c)
	}

	return
}

// enableAdditionalComponents enables the admission controller and cluster api operator
// once the cert manager is ready
func (r *ManagementReconciler) enableAdditionalComponents(ctx context.Context, mgmt *hmc.Management) error {
	l := log.FromContext(ctx)

	hmcComponent := &mgmt.Spec.Core.HMC
	config := make(map[string]interface{})

	if hmcComponent.Config != nil {
		err := json.Unmarshal(hmcComponent.Config.Raw, &config)
		if err != nil {
			return fmt.Errorf("failed to unmarshal HMC config into map[string]interface{}: %v", err)
		}
	}
	admissionWebhookValues := make(map[string]interface{})
	if config["admissionWebhook"] != nil {
		admissionWebhookValues = config["admissionWebhook"].(map[string]interface{})
	}
	capiOperatorValues := make(map[string]interface{})
	if config["cluster-api-operator"] != nil {
		capiOperatorValues = config["cluster-api-operator"].(map[string]interface{})
	}

	err := certmanager.VerifyAPI(ctx, r.Config, r.Scheme, r.SystemNamespace)
	if err != nil {
		return fmt.Errorf("failed to check in the cert-manager API is installed: %v", err)
	}
	l.Info("Cert manager is installed, enabling the HMC admission webhook")

	admissionWebhookValues["enabled"] = true
	config["admissionWebhook"] = admissionWebhookValues

	// Enable HMC capi operator only if it was not explicitly disabled in the config to
	// support installation with existing cluster api operator
	if capiOperatorValues["enabled"] != false {
		l.Info("Enabling cluster API operator")
		capiOperatorValues["enabled"] = true
	}
	config["cluster-api-operator"] = capiOperatorValues

	updatedConfig, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal HMC config: %v", err)
	}
	hmcComponent.Config = &apiextensionsv1.JSON{
		Raw: updatedConfig,
	}
	return nil
}

func updateComponentsStatus(
	components map[string]hmc.ComponentStatus,
	providers *hmc.Providers,
	componentName string,
	templateStatus hmc.TemplateStatus,
	err string,
) {
	components[componentName] = hmc.ComponentStatus{
		Error:   err,
		Success: err == "",
	}

	if err == "" {
		providers.InfrastructureProviders = append(providers.InfrastructureProviders, templateStatus.Providers.InfrastructureProviders...)
		providers.BootstrapProviders = append(providers.BootstrapProviders, templateStatus.Providers.BootstrapProviders...)
		providers.ControlPlaneProviders = append(providers.ControlPlaneProviders, templateStatus.Providers.ControlPlaneProviders...)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ManagementReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hmc.Management{}).
		Complete(r)
}
