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

// WAHAB:
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

	if !managedCluster.Spec.DryRun {
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
				// WAHAB: Why do we need this, shouldn't just returning err automatically requeue?
				return ctrl.Result{RequeueAfter: 10 * time.Second}, err
			} else {
				return ctrl.Result{}, err
			}
		}

		if requeue {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		if !fluxconditions.IsReady(hr) {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// ///////////////////////////////////////////////////////////////////////////////////////////////////////
		// ///////////////////////////////////////////////////////////////////////////////////////////////////////
		// profile := sveltosv1beta1.ClusterProfile{
		// 	// TypeMeta: metav1.TypeMeta{
		// 	// 	Kind:       "ClusterProfile",
		// 	// 	APIVersion: "config.projectsveltos.io/v1beta1",
		// 	// },
		// 	ObjectMeta: metav1.ObjectMeta{
		// 		Name:      managedCluster.Name,
		// 		Namespace: managedCluster.Namespace,
		// 	},
		// 	Spec: sveltosv1beta1.Spec{
		// 		ClusterSelector: libsveltosv1beta1.Selector{
		// 			LabelSelector: metav1.LabelSelector{
		// 				MatchLabels: map[string]string{
		// 					"helm.toolkit.fluxcd.io/name":      managedCluster.Name,
		// 					"helm.toolkit.fluxcd.io/namespace": managedCluster.Namespace,
		// 				},
		// 			},
		// 		},
		// 		// ClusterRefs: []corev1.ObjectReference{
		// 		// 	{
		// 		// 		Kind:      "Cluster",
		// 		// 		Name:      managedCluster.GetName(),
		// 		// 		Namespace: managedCluster.GetNamespace(),
		// 		// 	},
		// 		// },
		// 	},
		// }

		opts := []sveltos.HelmChartOpts{}
		for i, svc := range managedCluster.Spec.Services {
			if !svc.Install {
				continue
			}

			tmpl := &hmc.ServiceTemplate{}
			tmplRef := types.NamespacedName{Name: svc.Template, Namespace: r.SystemNamespace}
			if err := r.Get(ctx, tmplRef, tmpl); err != nil {
				// WAHAB: You should continue and store the error so that other
				// services have a chance to run as well. Then at the end of the loop
				// return error if any one of them failed.
				// Also might need to update the status here.
				// return ctrl.Result{}, err
				continue
			}

			/*
				// README: https://projectsveltos.github.io/sveltos/addons/helm_charts/
				url := fmt.Sprintf("oci://hmc-local-registry:5000/charts/%s", tmpl.Spec.Helm.ChartName)
				opts = append(opts, sveltos.HelmChartOpts{
					// This URL can be retrieved from servicetemplate -> helmchart -> helmrepository -> spec.url
					// RepositoryURL:    "oci://hmc-local-registry:5000/charts",
					RepositoryURL:    url,
					RepositoryName:   tmpl.Spec.Helm.ChartName,
					ChartName:        url,
					ChartVersion:     tmpl.Spec.Helm.ChartVersion,
					ReleaseName:      tmpl.Spec.Helm.ChartName,
					ReleaseNamespace: tmpl.Spec.Helm.ChartName,
					CreateNamespace:  true,
				})
			*/

			opts = append(opts, sveltos.HelmChartOpts{
				// This URL can be retrieved from servicetemplate -> helmchart -> helmrepository -> spec.url
				RepositoryURL:    "oci://hmc-local-registry:5000/charts",
				RepositoryName:   tmpl.Spec.Helm.ChartName,
				ChartName:        tmpl.Spec.Helm.ChartName,
				ChartVersion:     tmpl.Spec.Helm.ChartVersion,
				ReleaseName:      tmpl.Spec.Helm.ChartName,
				ReleaseNamespace: tmpl.Spec.Helm.ChartName,
				CreateNamespace:  true,
			})

			// profile.Spec.HelmCharts = append(profile.Spec.HelmCharts, sveltosv1beta1.HelmChart{
			// 	// This URL can be retrieved from servicetemplate -> helmchart -> helmrepository -> spec.url
			// 	RepositoryURL:    "oci://hmc-local-registry:5000/charts",
			// 	RepositoryName:   tmpl.Spec.Helm.ChartName,
			// 	ChartName:        tmpl.Spec.Helm.ChartName,
			// 	ChartVersion:     tmpl.Spec.Helm.ChartVersion,
			// 	ReleaseName:      tmpl.Spec.Helm.ChartName,
			// 	ReleaseNamespace: tmpl.Spec.Helm.ChartName,
			// 	HelmChartAction:  sveltosv1beta1.HelmChartActionInstall,
			// 	Options: &sveltosv1beta1.HelmOptions{
			// 		InstallOptions: sveltosv1beta1.HelmInstallOptions{
			// 			CreateNamespace: true, // defaults to true
			// 		},
			// 	},
			// })

			fmt.Printf("\n\n>>>>>>>>>>>>>>>>> %d) template=%s, install=%v, config=%s, values=%s\n", i, svc.Template, svc.Install, string(svc.Config.Raw), string(svc.Values.Raw))
			fmt.Printf("\n\n>>>>>>>>>>>>>>>>> %d) chartName=%s, chartVersion=%s, chartRef=%v, status.chartRef=%v\n\n", i, tmpl.Spec.Helm.ChartName, tmpl.Spec.Helm.ChartVersion, tmpl.Spec.Helm.ChartRef, tmpl.Status.ChartRef)

		}

		_, res, err := sveltos.ReconcileClusterProfile(ctx, r.Client, managedCluster.Name, managedCluster.Namespace, sveltos.ReconcileClusterProfileOpts{
			OwnerReference: &metav1.OwnerReference{
				APIVersion: hmc.GroupVersion.String(),
				Kind:       hmc.ManagedClusterKind,
				Name:       managedCluster.Name,
				UID:        managedCluster.UID,
			},
			HelmChartOpts: opts,
		})
		// res, err := ctrl.CreateOrUpdate(ctx, r.Client, &profile, func() error {
		// 	return nil
		// })
		fmt.Printf("\n###################################### OperationResult=%s, Reported Err=%s", res, err)
		if err != nil {
			fmt.Printf("\n###################################### failed to create clusterprofile err = %s", err)
			return ctrl.Result{}, fmt.Errorf("failed to create cluster profile: %w", err)
		}

		// ///////////////////////////////////////////////////////////////////////////////////////////////////////
		// ///////////////////////////////////////////////////////////////////////////////////////////////////////
	}
	return ctrl.Result{}, nil
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

	err = helm.DeleteHelmRelease(ctx, r.Client, managedCluster.Name, managedCluster.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.releaseCluster(ctx, managedCluster.Namespace, managedCluster.Name, managedCluster.Spec.Template)
	if err != nil {
		return ctrl.Result{}, err
	}

	l.Info("HelmRelease still exists, retrying")
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
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
