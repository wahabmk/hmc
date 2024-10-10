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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	hmc "github.com/Mirantis/hmc/api/v1alpha1"
	"github.com/Mirantis/hmc/internal/sveltos"
	"github.com/Mirantis/hmc/internal/utils"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
)

/*
apiVersion: hmc.mirantis.com/v1alpha1
kind: MultiClusterService
metadata:
  name: wali-global-ingress
spec:
  clusterSelector:
    matchLabels:
      app.kubernetes.io/managed-by: Helm
  services:
    - template: ingress-nginx-4-11-0
      name: ingress-nginx
      namespace: ingress-nginx
*/

// MultiClusterServiceReconciler reconciles a MultiClusterService object
type MultiClusterServiceReconciler struct {
	client.Client
}

// Reconcile reconciles a MultiClusterService object.
func (r *MultiClusterServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := ctrl.LoggerFrom(ctx).WithValues("MultiClusterServiceController", req.NamespacedName.String())
	l.Info("Reconciling MultiClusterService")

	// 1. Get the MultiClusterService obj from kube based on its name only (cluster-scope)
	mcsvc := &hmc.MultiClusterService{}
	err := r.Get(ctx, req.NamespacedName, mcsvc)

	fmt.Printf("\n>>>>>>>>>>>> [Reconcile] req.Namespace=%s, req=Name=%s, req.NamespacedName=%s, err=%s\n", req.Namespace, req.Name, req.NamespacedName, err)

	if apierrors.IsNotFound(err) {
		l.Info("MultiClusterService not found, ignoring since object must be deleted")
		return ctrl.Result{}, nil
	}
	if err != nil {
		l.Error(err, "Failed to get MultiClusterService")
		return ctrl.Result{}, err
	}

	b, _ := yaml.Marshal(mcsvc)
	fmt.Printf("\n>>>>>>>>>>>> [Reconcile] object=\n%s\n", string(b))

	// ================================================================================================================

	// 2. If its DeletionTimestamp.IsZero() then reconcile its deletion
	if !mcsvc.DeletionTimestamp.IsZero() {
		l.Info("Deleting MultiClusterService")
		return r.reconcileDelete(ctx)
	}

	// ================================================================================================================

	// 3. If ObservedGeneration == 0 then make a creation event for telemetry

	// ================================================================================================================

	// 4. Now reconcile
	// 4a. Add finalizer if doesn't exist
	// 4b. Reconcile the ClusterProfile object
	return r.reconcile(ctx, l, mcsvc)
}

func (r *MultiClusterServiceReconciler) reconcile(ctx context.Context, l logr.Logger, mcsvc *hmc.MultiClusterService) (ctrl.Result, error) {
	isUpdated := controllerutil.AddFinalizer(mcsvc, hmc.MultiClusterServiceFinalizer)
	if isUpdated {
		if err := r.Client.Update(ctx, mcsvc); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update MultiClusterService %s/%s with finalizer %s: %w", mcsvc.Namespace, mcsvc.Name, hmc.MultiClusterServiceFinalizer, err)
		}
		return ctrl.Result{}, nil
	}

	opts, err := r.getHelmChartOpts(ctx, l, mcsvc)
	if err != nil {
		return ctrl.Result{}, err
	}

	if _, err := sveltos.ReconcileClusterProfile(ctx, r.Client, l, mcsvc.Namespace, mcsvc.Name,
		sveltos.ReconcileProfileOpts{
			OwnerReference: &metav1.OwnerReference{
				APIVersion: hmc.GroupVersion.String(),
				Kind:       hmc.ManagedClusterKind,
				Name:       mcsvc.Name,
				UID:        mcsvc.UID,
			},
			MatchLabels: map[string]string{
				hmc.FluxHelmChartNamespaceKey: mcsvc.Namespace,
				hmc.FluxHelmChartNameKey:      mcsvc.Name,
			},
			HelmChartOpts:  opts,
			Priority:       mcsvc.Spec.Priority,
			StopOnConflict: mcsvc.Spec.StopOnConflict,
		}); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Profile: %w", err)
	}

	return ctrl.Result{RequeueAfter: DefaultRequeueInterval}, nil
}

// wahab: needs to be shared somehow
func (r *MultiClusterServiceReconciler) getHelmChartOpts(ctx context.Context, l logr.Logger, mcsvc *hmc.MultiClusterService) ([]sveltos.HelmChartOpts, error) {
	opts := []sveltos.HelmChartOpts{}

	// NOTE: The Profile object will be updated with no helm
	// charts if len(mc.Spec.Services) == 0. This will result in the
	// helm charts being uninstalled on matching clusters if
	// Profile originally had len(m.Spec.Sevices) > 0.
	for _, svc := range mcsvc.Spec.Services {
		if svc.Disable {
			l.Info(fmt.Sprintf("Skip adding Template (%s) to Profile (%s) because Disable=true", svc.Template, mcsvc.Name))
			continue
		}

		tmpl := &hmc.ServiceTemplate{}
		tmplRef := types.NamespacedName{Name: svc.Template, Namespace: utils.DefaultSystemNamespace}
		if err := r.Get(ctx, tmplRef, tmpl); err != nil {
			return nil, fmt.Errorf("failed to get Template (%s): %w", tmplRef.String(), err)
		}

		source, err := r.getServiceTemplateSource(ctx, tmpl)
		if err != nil {
			return nil, fmt.Errorf("could not get repository url: %w", err)
		}

		opts = append(opts, sveltos.HelmChartOpts{
			Values:        svc.Values,
			RepositoryURL: source.Spec.URL,
			// We don't have repository name so chart name becomes repository name.
			RepositoryName: tmpl.Spec.Helm.ChartName,
			ChartName: func() string {
				if source.Spec.Type == utils.RegistryTypeOCI {
					return tmpl.Spec.Helm.ChartName
				}
				// Sveltos accepts ChartName in <repository>/<chart> format for non-OCI.
				// We don't have a repository name, so we can use <chart>/<chart> instead.
				// See: https://projectsveltos.github.io/sveltos/addons/helm_charts/.
				return fmt.Sprintf("%s/%s", tmpl.Spec.Helm.ChartName, tmpl.Spec.Helm.ChartName)
			}(),
			ChartVersion: tmpl.Spec.Helm.ChartVersion,
			ReleaseName:  svc.Name,
			ReleaseNamespace: func() string {
				if svc.Namespace != "" {
					return svc.Namespace
				}
				return svc.Name
			}(),
			// The reason it is passed to PlainHTTP instead of InsecureSkipTLSVerify is because
			// the source.Spec.Insecure field is meant to be used for connecting to repositories
			// over plain HTTP, which is different than what InsecureSkipTLSVerify is meant for.
			// See: https://github.com/fluxcd/source-controller/pull/1288
			PlainHTTP: source.Spec.Insecure,
		})
	}

	return opts, nil
}

// wahab: needs to be shared
func (r *MultiClusterServiceReconciler) getServiceTemplateSource(ctx context.Context, tmpl *hmc.ServiceTemplate) (*sourcev1.HelmRepository, error) {
	tmplRef := types.NamespacedName{Namespace: tmpl.Namespace, Name: tmpl.Name}

	if tmpl.Status.ChartRef == nil {
		return nil, fmt.Errorf("status for ServiceTemplate (%s) has not been updated yet", tmplRef.String())
	}

	hc := &sourcev1.HelmChart{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: tmpl.Status.ChartRef.Namespace,
		Name:      tmpl.Status.ChartRef.Name,
	}, hc); err != nil {
		return nil, fmt.Errorf("failed to get HelmChart (%s): %w", tmplRef.String(), err)
	}

	repo := &sourcev1.HelmRepository{}
	if err := r.Get(ctx, types.NamespacedName{
		// Using chart's namespace because it's source
		// (helm repository in this case) should be within the same namespace.
		Namespace: hc.Namespace,
		Name:      hc.Spec.SourceRef.Name,
	}, repo); err != nil {
		return nil, fmt.Errorf("failed to get HelmRepository (%s): %w", tmplRef.String(), err)
	}

	return repo, nil
}

func (r *MultiClusterServiceReconciler) reconcileDelete(_ context.Context) (ctrl.Result, error) {
	// 2a. Handle what you need to handle
	// 2b. Remove finalizer
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MultiClusterServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hmc.MultiClusterService{}).
		Complete(r)
}
