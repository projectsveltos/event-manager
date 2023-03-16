/*
Copyright 2023. projectsveltos.io. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;list;watch

func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogInfo).Info("Reconciling Cluster")

	// Fecth the Cluster instance
	cluster := &clusterv1.Cluster{}
	return processCluster(ctx, r.Client, cluster, req, logger)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1.Cluster{}).
		Complete(r)
}

func processCluster(ctx context.Context, c client.Client, cluster client.Object, req ctrl.Request,
	logger logr.Logger) (ctrl.Result, error) {

	if err := c.Get(ctx, req.NamespacedName, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			err = removeEventReportsFromCluster(ctx, c, req.Namespace, req.Name,
				libsveltosv1alpha1.ClusterTypeCapi, map[string]bool{}, logger)
			if err != nil {
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch Cluster")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to fetch Cluster %s",
			req.NamespacedName,
		)
	}

	// Handle deleted cluster
	if !cluster.GetDeletionTimestamp().IsZero() {
		err := removeEventReportsFromCluster(ctx, c, req.Namespace, req.Name,
			libsveltosv1alpha1.ClusterTypeCapi, map[string]bool{}, logger)
		if err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
	}

	return reconcile.Result{}, nil
}
