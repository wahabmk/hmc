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
	"fmt"
	"strings"

	helmcontrollerv2 "github.com/fluxcd/helm-controller/api/v2"
	v2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"helm.sh/helm/v3/pkg/chart"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hmc "github.com/Mirantis/hmc/api/v1alpha1"
	"github.com/Mirantis/hmc/internal/helm"
)

const (
	defaultRepoName = "hmc-templates"
)

var (
	errNoProviderType = fmt.Errorf("template type is not supported: %s chart annotation must be one of [%s/%s/%s]",
		hmc.ChartAnnotationType, hmc.TemplateTypeDeployment, hmc.TemplateTypeProvider, hmc.TemplateTypeCore)
)

// TemplateReconciler reconciles a Template object
type TemplateReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	downloadHelmChartFunc func(context.Context, *sourcev1.Artifact) (*chart.Chart, error)
}

func (r *TemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx).WithValues("TemplateController", req.NamespacedName)
	l.Info("Reconciling Template")

	template := &hmc.Template{}
	if err := r.Get(ctx, req.NamespacedName, template); err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("Template not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		l.Error(err, "Failed to get Template")
		return ctrl.Result{}, err
	}

	var hcChart *sourcev1.HelmChart
	var err error
	if template.Spec.Helm.ChartRef != nil {
		hcChart, err = r.getHelmChartFromChartRef(ctx, template.Spec.Helm.ChartRef)
		if err != nil {
			l.Error(err, "failed to get artifact from chartRef", "kind", template.Spec.Helm.ChartRef.Kind, "namespace", template.Spec.Helm.ChartRef.Namespace, "name", template.Spec.Helm.ChartRef.Name)
			return ctrl.Result{}, err
		}
	} else {
		if template.Spec.Helm.ChartName == "" {
			err = fmt.Errorf("neither chartName nor chartRef is set")
			l.Error(err, "invalid helm chart reference")
			return ctrl.Result{}, err
		}
		l.Info("Reconciling helm-controller objects ")
		hcChart, err = r.reconcileHelmChart(ctx, template)
		if err != nil {
			l.Error(err, "Failed to reconcile HelmChart")
			return ctrl.Result{}, err
		}
	}
	if hcChart == nil {
		err := fmt.Errorf("HelmChart is nil")
		l.Error(err, "could not get the helm chart")
		return ctrl.Result{}, err
	}
	template.Status.ChartRef = &v2.CrossNamespaceSourceReference{
		Kind:      sourcev1.HelmChartKind,
		Name:      hcChart.Name,
		Namespace: hcChart.Namespace,
	}
	if err, reportStatus := helm.ArtifactReady(hcChart); err != nil {
		l.Info("HelmChart Artifact is not ready")
		if reportStatus {
			_ = r.updateStatus(ctx, template, err.Error())
		}
		return ctrl.Result{}, err
	}

	artifact := hcChart.Status.Artifact

	if r.downloadHelmChartFunc == nil {
		r.downloadHelmChartFunc = helm.DownloadChartFromArtifact
	}

	l.Info("Downloading Helm chart")
	helmChart, err := r.downloadHelmChartFunc(ctx, artifact)
	if err != nil {
		l.Error(err, "Failed to download Helm chart")
		err = fmt.Errorf("failed to download chart: %s", err)
		_ = r.updateStatus(ctx, template, err.Error())
		return ctrl.Result{}, err
	}
	l.Info("Validating Helm chart")
	if err := r.parseChartMetadata(template, helmChart); err != nil {
		l.Error(err, "Failed to parse Helm chart metadata")
		_ = r.updateStatus(ctx, template, err.Error())
		return ctrl.Result{}, err
	}
	if err = helmChart.Validate(); err != nil {
		l.Error(err, "Helm chart validation failed")
		_ = r.updateStatus(ctx, template, err.Error())
		return ctrl.Result{}, err
	}

	template.Status.Description = helmChart.Metadata.Description
	rawValues, err := json.Marshal(helmChart.Values)
	if err != nil {
		l.Error(err, "Failed to parse Helm chart values")
		err = fmt.Errorf("failed to parse Helm chart values: %s", err)
		_ = r.updateStatus(ctx, template, err.Error())
		return ctrl.Result{}, err
	}
	template.Status.Config = &apiextensionsv1.JSON{Raw: rawValues}
	l.Info("Chart validation completed successfully")

	return ctrl.Result{}, r.updateStatus(ctx, template, "")
}

func (r *TemplateReconciler) parseChartMetadata(template *hmc.Template, chart *chart.Chart) error {
	if chart.Metadata == nil {
		return fmt.Errorf("chart metadata is empty")
	}
	// the value in spec has higher priority
	templateType := template.Spec.Type
	if templateType == "" {
		templateType = hmc.TemplateType(chart.Metadata.Annotations[hmc.ChartAnnotationType])
		switch templateType {
		case hmc.TemplateTypeDeployment, hmc.TemplateTypeProvider, hmc.TemplateTypeCore:
		default:
			return errNoProviderType
		}
	}
	template.Status.Type = templateType

	// the value in spec has higher priority
	if len(template.Spec.Providers.InfrastructureProviders) > 0 {
		template.Status.Providers.InfrastructureProviders = template.Spec.Providers.InfrastructureProviders
	} else {
		infraProviders := chart.Metadata.Annotations[hmc.ChartAnnotationInfraProviders]
		if infraProviders != "" {
			template.Status.Providers.InfrastructureProviders = strings.Split(infraProviders, ",")
		}
	}
	if len(template.Spec.Providers.BootstrapProviders) > 0 {
		template.Status.Providers.BootstrapProviders = template.Spec.Providers.BootstrapProviders
	} else {
		bootstrapProviders := chart.Metadata.Annotations[hmc.ChartAnnotationBootstrapProviders]
		if bootstrapProviders != "" {
			template.Status.Providers.BootstrapProviders = strings.Split(bootstrapProviders, ",")
		}
	}
	if len(template.Spec.Providers.ControlPlaneProviders) > 0 {
		template.Status.Providers.ControlPlaneProviders = template.Spec.Providers.ControlPlaneProviders
	} else {
		cpProviders := chart.Metadata.Annotations[hmc.ChartAnnotationControlPlaneProviders]
		if cpProviders != "" {
			template.Status.Providers.ControlPlaneProviders = strings.Split(cpProviders, ",")
		}
	}
	return nil
}

func (r *TemplateReconciler) updateStatus(ctx context.Context, template *hmc.Template, validationError string) error {
	template.Status.ObservedGeneration = template.Generation
	template.Status.ValidationError = validationError
	template.Status.Valid = validationError == ""
	if err := r.Status().Update(ctx, template); err != nil {
		return fmt.Errorf("failed to update status for template %s/%s: %w", template.Namespace, template.Name, err)
	}
	return nil
}

func (r *TemplateReconciler) reconcileHelmChart(ctx context.Context, template *hmc.Template) (*sourcev1.HelmChart, error) {
	if template.Spec.Helm.ChartRef != nil {
		// HelmChart is not managed by the controller
		return nil, nil
	}
	helmChart := &sourcev1.HelmChart{
		ObjectMeta: metav1.ObjectMeta{
			Name:      template.Name,
			Namespace: template.Namespace,
		},
	}

	_, err := ctrl.CreateOrUpdate(ctx, r.Client, helmChart, func() error {
		if helmChart.Labels == nil {
			helmChart.Labels = make(map[string]string)
		}
		helmChart.Labels[hmc.HMCManagedLabelKey] = hmc.HMCManagedLabelValue
		helmChart.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: hmc.GroupVersion.String(),
				Kind:       hmc.TemplateKind,
				Name:       template.Name,
				UID:        template.UID,
			},
		}
		helmChart.Spec = sourcev1.HelmChartSpec{
			Chart:   template.Spec.Helm.ChartName,
			Version: template.Spec.Helm.ChartVersion,
			// WAHAB 4: Due to this, the Template object for projectsveltos will
			// have to be within the hmc-system namespace. Because the helm-templates Flux source
			// is within the hmc-system namespace.
			SourceRef: sourcev1.LocalHelmChartSourceReference{
				Kind: sourcev1.HelmRepositoryKind,
				Name: defaultRepoName,
			},
			Interval: metav1.Duration{Duration: helm.DefaultReconcileInterval},
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return helmChart, nil
}

func (r *TemplateReconciler) getHelmChartFromChartRef(ctx context.Context, chartRef *helmcontrollerv2.CrossNamespaceSourceReference) (*sourcev1.HelmChart, error) {
	if chartRef.Kind != sourcev1.HelmChartKind {
		return nil, fmt.Errorf("invalid chartRef.Kind: %s. Only HelmChart kind is supported", chartRef.Kind)
	}
	helmChart := &sourcev1.HelmChart{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: chartRef.Namespace,
		Name:      chartRef.Name,
	}, helmChart)
	if err != nil {
		return nil, err
	}
	return helmChart, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hmc.Template{}).
		Complete(r)
}
