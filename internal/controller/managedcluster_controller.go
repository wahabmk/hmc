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
	"errors"
	"fmt"
	"time"

	hcv2 "github.com/fluxcd/helm-controller/api/v2"
	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	fluxconditions "github.com/fluxcd/pkg/runtime/conditions"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hmc "github.com/Mirantis/hmc/api/v1alpha1"
	"github.com/Mirantis/hmc/internal/helm"
	"github.com/Mirantis/hmc/internal/sveltos"
	"github.com/Mirantis/hmc/internal/telemetry"
)

const (
	DefaultRequeueInterval = 10 * time.Second
)

// ManagedClusterReconciler reconciles a ManagedCluster object
type ManagedClusterReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Config          *rest.Config
	DynamicClient   *dynamic.DynamicClient
	SystemNamespace string
}

type providerSchema struct {
	machine, cluster schema.GroupVersionKind
}

var (
	gvkAWSCluster = schema.GroupVersionKind{
		Group:   "infrastructure.cluster.x-k8s.io",
		Version: "v1beta2",
		Kind:    "awscluster",
	}

	gvkAzureCluster = schema.GroupVersionKind{
		Group:   "infrastructure.cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "azurecluster",
	}

	gvkMachine = schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "machine",
	}
)

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ManagedClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx).WithValues("ManagedClusterController", req.NamespacedName)
	l.Info("Reconciling ManagedCluster")
	managedCluster := &hmc.ManagedCluster{}
	if err := r.Get(ctx, req.NamespacedName, managedCluster); err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("ManagedCluster not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		l.Error(err, "Failed to get ManagedCluster")
		return ctrl.Result{}, err
	}

	if !managedCluster.DeletionTimestamp.IsZero() {
		l.Info("Deleting ManagedCluster")
		return r.Delete(ctx, l, managedCluster)
	}

	if managedCluster.Status.ObservedGeneration == 0 {
		mgmt := &hmc.Management{}
		mgmtRef := types.NamespacedName{Name: hmc.ManagementName}
		if err := r.Get(ctx, mgmtRef, mgmt); err != nil {
			l.Error(err, "Failed to get Management object")
			return ctrl.Result{}, err
		}
		if err := telemetry.TrackManagedClusterCreate(string(mgmt.UID), string(managedCluster.UID), managedCluster.Spec.Template, managedCluster.Spec.DryRun); err != nil {
			l.Error(err, "Failed to track ManagedCluster creation")
		}
	}
	return r.Update(ctx, l, managedCluster)
}

func (r *ManagedClusterReconciler) setStatusFromClusterStatus(ctx context.Context, l logr.Logger, managedCluster *hmc.ManagedCluster) (bool, error) {
	resourceId := schema.GroupVersionResource{
		Group:    "cluster.x-k8s.io",
		Version:  "v1beta1",
		Resource: "clusters",
	}

	list, err := r.DynamicClient.Resource(resourceId).Namespace(managedCluster.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{hmc.FluxHelmChartNameKey: managedCluster.Name}).String(),
	})

	if apierrors.IsNotFound(err) || len(list.Items) == 0 {
		l.Info("Clusters not found, ignoring since object must be deleted or not yet created")
		return true, nil
	}

	if err != nil {
		return true, fmt.Errorf("failed to get cluster information for managedCluster %s in namespace: %s: %w",
			managedCluster.Namespace, managedCluster.Name, err)
	}
	conditions, found, err := unstructured.NestedSlice(list.Items[0].Object, "status", "conditions")
	if err != nil {
		return true, fmt.Errorf("failed to get cluster information for managedCluster %s in namespace: %s: %w",
			managedCluster.Namespace, managedCluster.Name, err)
	}
	if !found {
		return true, fmt.Errorf("failed to get cluster information for managedCluster %s in namespace: %s: status.conditions not found",
			managedCluster.Namespace, managedCluster.Name)
	}

	allConditionsComplete := true
	for _, condition := range conditions {
		conditionMap, ok := condition.(map[string]interface{})
		if !ok {
			return true, fmt.Errorf("failed to cast condition to map[string]interface{} for managedCluster: %s in namespace: %s: %w",
				managedCluster.Namespace, managedCluster.Name, err)
		}

		var metaCondition metav1.Condition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(conditionMap, &metaCondition); err != nil {
			return true, fmt.Errorf("failed to convert unstructured conditions to metav1.Condition for managedCluster %s in namespace: %s: %w",
				managedCluster.Namespace, managedCluster.Name, err)
		}

		if metaCondition.Status != "True" {
			allConditionsComplete = false
		}

		if metaCondition.Reason == "" && metaCondition.Status == "True" {
			metaCondition.Reason = "Succeeded"
		}
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metaCondition)
	}

	return !allConditionsComplete, nil
}

func (r *ManagedClusterReconciler) Update(ctx context.Context, l logr.Logger, managedCluster *hmc.ManagedCluster) (result ctrl.Result, err error) {
	finalizersUpdated := controllerutil.AddFinalizer(managedCluster, hmc.ManagedClusterFinalizer)
	if finalizersUpdated {
		if err := r.Client.Update(ctx, managedCluster); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update managedCluster %s/%s: %w", managedCluster.Namespace, managedCluster.Name, err)
		}
		return ctrl.Result{}, nil
	}

	if len(managedCluster.Status.Conditions) == 0 {
		managedCluster.InitConditions()
	}

	defer func() {
		err = errors.Join(err, r.updateStatus(ctx, managedCluster))
	}()

	template := &hmc.ClusterTemplate{}
	templateRef := types.NamespacedName{Name: managedCluster.Spec.Template, Namespace: r.SystemNamespace}
	if err := r.Get(ctx, templateRef, template); err != nil {
		l.Error(err, "Failed to get Template")
		errMsg := fmt.Sprintf("failed to get provided template: %s", err)
		if apierrors.IsNotFound(err) {
			errMsg = "provided template is not found"
		}
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.TemplateReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: errMsg,
		})
		return ctrl.Result{}, err
	}
	if !template.Status.Valid {
		errMsg := "provided template is not marked as valid"
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.TemplateReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: errMsg,
		})
		return ctrl.Result{}, errors.New(errMsg)
	}
	apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
		Type:    hmc.TemplateReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  hmc.SucceededReason,
		Message: "Template is valid",
	})
	source, err := r.getSource(ctx, template.Status.ChartRef)
	if err != nil {
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.HelmChartReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: fmt.Sprintf("failed to get helm chart source: %s", err),
		})
		return ctrl.Result{}, err
	}
	l.Info("Downloading Helm chart")
	hcChart, err := helm.DownloadChartFromArtifact(ctx, source.GetArtifact())
	if err != nil {
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.HelmChartReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: fmt.Sprintf("failed to download helm chart: %s", err),
		})
		return ctrl.Result{}, err
	}

	l.Info("Initializing Helm client")
	getter := helm.NewMemoryRESTClientGetter(r.Config, r.RESTMapper())
	actionConfig := new(action.Configuration)
	err = actionConfig.Init(getter, managedCluster.Namespace, "secret", l.Info)
	if err != nil {
		return ctrl.Result{}, err
	}

	l.Info("Validating Helm chart with provided values")
	if err := r.validateReleaseWithValues(ctx, actionConfig, managedCluster, hcChart); err != nil {
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.HelmChartReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: fmt.Sprintf("failed to validate template with provided configuration: %s", err),
		})
		return ctrl.Result{}, err
	}

	apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
		Type:    hmc.HelmChartReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  hmc.SucceededReason,
		Message: "Helm chart is valid",
	})

	if managedCluster.Spec.DryRun {
		l.Info("Not reconciling ManagedCluster because DryRun is enabled")
		return ctrl.Result{}, nil
	}

	hr, _, err := helm.ReconcileHelmRelease(ctx, r.Client, managedCluster.Name, managedCluster.Namespace, helm.ReconcileHelmReleaseOpts{
		Values: managedCluster.Spec.Config,
		OwnerReference: &metav1.OwnerReference{
			APIVersion: hmc.GroupVersion.String(),
			Kind:       hmc.ManagedClusterKind,
			Name:       managedCluster.Name,
			UID:        managedCluster.UID,
		},
		ChartRef: template.Status.ChartRef,
	})
	if err != nil {
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.HelmReleaseReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: err.Error(),
		})
		return ctrl.Result{}, err
	}

	hrReadyCondition := fluxconditions.Get(hr, fluxmeta.ReadyCondition)
	if hrReadyCondition != nil {
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.HelmReleaseReadyCondition,
			Status:  hrReadyCondition.Status,
			Reason:  hrReadyCondition.Reason,
			Message: hrReadyCondition.Message,
		})
	}

	requeue, err := r.setStatusFromClusterStatus(ctx, l, managedCluster)
	if err != nil {
		if requeue {
			return ctrl.Result{RequeueAfter: DefaultRequeueInterval}, err
		} else {
			return ctrl.Result{}, err
		}
	}

	if requeue {
		return ctrl.Result{RequeueAfter: DefaultRequeueInterval}, nil
	}

	if !fluxconditions.IsReady(hr) {
		return ctrl.Result{RequeueAfter: DefaultRequeueInterval}, nil
	}

	return r.updateServices(ctx, l, managedCluster)
}

// updateServices reconciles services provided in ManagedCluster.Spec.Services.
// TODO(https://github.com/Mirantis/hmc/issues/361): Set status to ManagedCluster object at appropriate places.
func (r *ManagedClusterReconciler) updateServices(ctx context.Context, l logr.Logger, mc *hmc.ManagedCluster) (ctrl.Result, error) {
	opts := []sveltos.HelmChartOpts{}

	// NOTE: The ClusterProfile object will be updated with no helm
	// charts if len(mc.Spec.Services) == 0. This will result in the
	// helm charts being uninstalled on matching clusters if
	// ClusterProfile originally had len(m.Spec.Sevices) > 0.
	for _, svc := range mc.Spec.Services {
		if !svc.Install {
			l.Info(fmt.Sprintf("Skip adding Template (%s) to ClusterProfile (%s) because install=false", svc.Template, sveltos.ClusterProfileName(mc.Namespace, mc.Name)))
			continue
		}

		tmpl := &hmc.ServiceTemplate{}
		tmplRef := types.NamespacedName{Name: svc.Template, Namespace: mc.Namespace}
		if err := r.Get(ctx, tmplRef, tmpl); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get Template (%s)", tmplRef.String())
		}

		url, err := r.getServiceTemplateRepoURL(ctx, tmpl)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("could not get repository url: %w", err)
		}

		opts = append(opts, sveltos.HelmChartOpts{
			RepositoryURL:  url,
			RepositoryName: tmpl.Spec.Helm.ChartName,
			ChartName:      tmpl.Spec.Helm.ChartName,
			ChartVersion:   tmpl.Spec.Helm.ChartVersion,
			ReleaseName:    svc.ReleaseName,
			Values:         svc.Values,
			ReleaseNamespace: func() string {
				if svc.ReleaseNamespace != "" {
					return svc.ReleaseNamespace
				}
				return svc.ReleaseName
			}(),
			CreateNamespace: svc.CreateNamespace,
			PlainHTTP:       svc.RegistryConfig.PlainHTTP,
			Insecure:        svc.RegistryConfig.Insecure,
		})
	}

	cp, _, err := sveltos.ReconcileClusterProfile(ctx, r.Client, mc.Namespace, mc.Name,
		map[string]string{
			hmc.FluxHelmChartNamespaceKey: mc.Namespace,
			hmc.FluxHelmChartNameKey:      mc.Name,
		},
		sveltos.ReconcileClusterProfileOpts{
			OwnerReference: &metav1.OwnerReference{
				APIVersion: hmc.GroupVersion.String(),
				Kind:       hmc.ManagedClusterKind,
				Name:       mc.Name,
				UID:        mc.UID,
			},
			HelmChartOpts: opts,
		})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create ClusterProfile: %w", err)
	}

	l.Info(fmt.Sprintf("Successfully created ClusterProfile (%s)", cp.Name))

	// We don't technically need to requeue here, but doing so because golint fails with:
	// `(*ManagedClusterReconciler).updateServices` - result `res` is always `nil` (unparam)
	//
	// This will be automatically resolved once setting status is implemented (https://github.com/Mirantis/hmc/issues/361),
	// as it is likely that some execution path in the function will have to return with a requeue to fetch latest status.
	return ctrl.Result{RequeueAfter: DefaultRequeueInterval}, nil
}

// getServiceTemplateRepoURL returns the URL of the Helm Repository used by the ServiceTemplate.
// It is fetched by querying for ServiceTemplate -> HelmChart -> HelmRepository.Spec.URL.
func (r *ManagedClusterReconciler) getServiceTemplateRepoURL(ctx context.Context, tmpl *hmc.ServiceTemplate) (string, error) {
	tmplRef := types.NamespacedName{Namespace: tmpl.Namespace, Name: tmpl.Name}

	if tmpl.Status.ChartRef == nil {
		return "", fmt.Errorf("status for ServiceTemplate (%s) has not been updated yet", tmplRef.String())
	}

	chart := &sourcev1.HelmChart{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: tmpl.Status.ChartRef.Namespace,
		Name:      tmpl.Spec.Helm.ChartName,
	}, chart); err != nil {
		return "", fmt.Errorf("failed to get HelmChart (%s)", tmplRef.String())
	}

	repo := &sourcev1.HelmRepository{}
	if err := r.Get(ctx, types.NamespacedName{
		// Using chart's namespace because it's source
		// (helm repository in this case) should be within the same namespace.
		Namespace: chart.Namespace,
		Name:      chart.Spec.SourceRef.Name,
	}, repo); err != nil {
		return "", fmt.Errorf("failed to get HelmRepository (%s)", tmplRef.String())
	}

	return repo.Spec.URL, nil
}

func (r *ManagedClusterReconciler) validateReleaseWithValues(ctx context.Context, actionConfig *action.Configuration, managedCluster *hmc.ManagedCluster, hcChart *chart.Chart) error {
	install := action.NewInstall(actionConfig)
	install.DryRun = true
	install.ReleaseName = managedCluster.Name
	install.Namespace = managedCluster.Namespace
	install.ClientOnly = true

	vals, err := managedCluster.HelmValues()
	if err != nil {
		return err
	}
	_, err = install.RunWithContext(ctx, hcChart, vals)
	if err != nil {
		return err
	}
	return nil
}

func (r *ManagedClusterReconciler) updateStatus(ctx context.Context, managedCluster *hmc.ManagedCluster) error {
	managedCluster.Status.ObservedGeneration = managedCluster.Generation
	warnings := ""
	errs := ""
	for _, condition := range managedCluster.Status.Conditions {
		if condition.Type == hmc.ReadyCondition {
			continue
		}
		if condition.Status == metav1.ConditionUnknown {
			warnings += condition.Message + ". "
		}
		if condition.Status == metav1.ConditionFalse {
			errs += condition.Message + ". "
		}
	}
	condition := metav1.Condition{
		Type:    hmc.ReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  hmc.SucceededReason,
		Message: "ManagedCluster is ready",
	}
	if warnings != "" {
		condition.Status = metav1.ConditionUnknown
		condition.Reason = hmc.ProgressingReason
		condition.Message = warnings
	}
	if errs != "" {
		condition.Status = metav1.ConditionFalse
		condition.Reason = hmc.FailedReason
		condition.Message = errs
	}
	apimeta.SetStatusCondition(managedCluster.GetConditions(), condition)
	if err := r.Status().Update(ctx, managedCluster); err != nil {
		return fmt.Errorf("failed to update status for managedCluster %s/%s: %w", managedCluster.Namespace, managedCluster.Name, err)
	}
	return nil
}

func (r *ManagedClusterReconciler) getSource(ctx context.Context, ref *hcv2.CrossNamespaceSourceReference) (sourcev1.Source, error) {
	if ref == nil {
		return nil, fmt.Errorf("helm chart source is not provided")
	}
	chartRef := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
	hc := sourcev1.HelmChart{}
	if err := r.Client.Get(ctx, chartRef, &hc); err != nil {
		return nil, err
	}
	return &hc, nil
}

func (r *ManagedClusterReconciler) Delete(ctx context.Context, l logr.Logger, managedCluster *hmc.ManagedCluster) (ctrl.Result, error) {
	hr := &hcv2.HelmRelease{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      managedCluster.Name,
		Namespace: managedCluster.Namespace,
	}, hr)
	if err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("Removing Finalizer", "finalizer", hmc.ManagedClusterFinalizer)
			finalizersUpdated := controllerutil.RemoveFinalizer(managedCluster, hmc.ManagedClusterFinalizer)
			if finalizersUpdated {
				if err := r.Client.Update(ctx, managedCluster); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to update managedCluster %s/%s: %w", managedCluster.Namespace, managedCluster.Name, err)
				}
			}
			l.Info("ManagedCluster deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	err = sveltos.DeleteClusterProfile(ctx, r.Client, managedCluster.Namespace, managedCluster.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	l.Info(fmt.Sprintf("Issued delete on ClusterProfile (%s)", sveltos.ClusterProfileName(managedCluster.Namespace, managedCluster.Name)))

	err = helm.DeleteHelmRelease(ctx, r.Client, managedCluster.Name, managedCluster.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.releaseCluster(ctx, managedCluster.Namespace, managedCluster.Name, managedCluster.Spec.Template)
	if err != nil {
		return ctrl.Result{}, err
	}

	l.Info("HelmRelease still exists, retrying")
	return ctrl.Result{RequeueAfter: DefaultRequeueInterval}, nil
}

func (r *ManagedClusterReconciler) releaseCluster(ctx context.Context, namespace, name, templateName string) error {
	providers, err := r.getProviders(ctx, templateName)
	if err != nil {
		return err
	}

	providerGVKs := map[string]providerSchema{
		"aws":   {machine: gvkMachine, cluster: gvkAWSCluster},
		"azure": {machine: gvkMachine, cluster: gvkAzureCluster},
	}

	// Associate the provider with it's GVK
	for _, provider := range providers {
		gvk, ok := providerGVKs[provider]
		if !ok {
			continue
		}

		cluster, err := r.getCluster(ctx, namespace, name, gvk.cluster)
		if err != nil {
			return err
		}

		found, err := r.machinesAvailable(ctx, namespace, cluster.Name, gvk.machine)
		if err != nil {
			return err
		}

		if !found {
			return r.removeClusterFinalizer(ctx, cluster)
		}
	}

	return nil
}

func (r *ManagedClusterReconciler) getProviders(ctx context.Context, templateName string) ([]string, error) {
	template := &hmc.ClusterTemplate{}
	templateRef := types.NamespacedName{Name: templateName, Namespace: r.SystemNamespace}
	if err := r.Get(ctx, templateRef, template); err != nil {
		log.FromContext(ctx).Error(err, "Failed to get Template", "templateName", templateName)
		return nil, err
	}
	return template.Status.Providers.InfrastructureProviders, nil
}

func (r *ManagedClusterReconciler) getCluster(ctx context.Context, namespace, name string, gvk schema.GroupVersionKind) (*metav1.PartialObjectMetadata, error) {
	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{hmc.FluxHelmChartNameKey: name}),
		Namespace:     namespace,
	}
	itemsList := &metav1.PartialObjectMetadataList{}
	itemsList.SetGroupVersionKind(gvk)
	if err := r.Client.List(ctx, itemsList, opts); err != nil {
		return nil, err
	}
	if len(itemsList.Items) == 0 {
		return nil, fmt.Errorf("%s with name %s was not found", gvk.Kind, name)
	}

	return &itemsList.Items[0], nil
}

func (r *ManagedClusterReconciler) removeClusterFinalizer(ctx context.Context, cluster *metav1.PartialObjectMetadata) error {
	originalCluster := *cluster
	finalizersUpdated := controllerutil.RemoveFinalizer(cluster, hmc.BlockingFinalizer)
	if finalizersUpdated {
		log.FromContext(ctx).Info("Allow to stop cluster", "finalizer", hmc.BlockingFinalizer)
		if err := r.Client.Patch(ctx, cluster, client.MergeFrom(&originalCluster)); err != nil {
			return fmt.Errorf("failed to patch cluster %s/%s: %w", cluster.Namespace, cluster.Name, err)
		}
	}

	return nil
}

func (r *ManagedClusterReconciler) machinesAvailable(ctx context.Context, namespace, clusterName string, gvk schema.GroupVersionKind) (bool, error) {
	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{hmc.ClusterNameLabelKey: clusterName}),
		Namespace:     namespace,
		Limit:         1,
	}
	itemsList := &metav1.PartialObjectMetadataList{}
	itemsList.SetGroupVersionKind(gvk)
	if err := r.Client.List(ctx, itemsList, opts); err != nil {
		return false, err
	}
	return len(itemsList.Items) != 0, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ManagedClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hmc.ManagedCluster{}).
		Watches(&hcv2.HelmRelease{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
				managedCluster := hmc.ManagedCluster{}
				managedClusterRef := types.NamespacedName{
					Namespace: o.GetNamespace(),
					Name:      o.GetName(),
				}
				err := r.Client.Get(ctx, managedClusterRef, &managedCluster)
				if err != nil {
					return []ctrl.Request{}
				}
				return []reconcile.Request{
					{
						NamespacedName: managedClusterRef,
					},
				}
			}),
		).
		Complete(r)
}
