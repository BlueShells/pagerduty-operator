// Copyright 2019 RedHat
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

package clusterdeployment

import (
	"context"

	hivev1 "github.com/openshift/hive/pkg/apis/hive/v1alpha1"
	"github.com/openshift/pagerduty-operator/config"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openshift/pagerduty-operator/pkg/kube"
	"github.com/openshift/pagerduty-operator/pkg/localmetrics"
	pd "github.com/openshift/pagerduty-operator/pkg/pagerduty"
	"github.com/openshift/pagerduty-operator/pkg/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *ReconcileClusterDeployment) handleCreate(request reconcile.Request, instance *hivev1.ClusterDeployment) (reconcile.Result, error) {
	if !instance.Status.Installed {
		// Cluster isn't installed yet, return
		return reconcile.Result{}, nil

	}

	if utils.HasFinalizer(instance, config.OperatorFinalizer) == false {
		utils.AddFinalizer(instance, config.OperatorFinalizer)
		err := r.client.Update(context.TODO(), instance)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	ClusterID := instance.Spec.ClusterName

	pdData := &pd.Data{
		ClusterID:  instance.Spec.ClusterName,
		BaseDomain: instance.Spec.BaseDomain,
	}
	pdData.ParsePDConfig(r.client)
	// To prevent scoping issues in the err check below.
	var pdIntegrationKey string

	err := pdData.ParseClusterConfig(r.client, request.Namespace, request.Name)
	if err != nil {
		var createErr error
		pdIntegrationKey, createErr = r.pdclient.CreateService(pdData)
		if createErr != nil {
			localmetrics.UpdateMetricPagerDutyCreateFailure(1, ClusterID)
			return reconcile.Result{}, createErr
		}
	}
	localmetrics.UpdateMetricPagerDutyCreateFailure(0, ClusterID)

	pdIntegrationKey, err = r.pdclient.GetIntegrationKey(pdData)
	if err != nil {
		return reconcile.Result{}, err
	}
	//add secret part
	sc := &corev1.Secret{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: "pd-secret", Namespace: instance.Namespace}, sc)
	secret := kube.GeneratePdSecret(instance.Namespace, "pd-secret", pdIntegrationKey)
	if err != nil {
		if errors.IsNotFound(err) {
			r.reqLogger.Info("creating pd secret")
			//add reference
			if err = controllerutil.SetControllerReference(instance, secret, r.scheme); err != nil {
				r.reqLogger.Error(err, "Error setting controller reference on secret")
				return reconcile.Result{}, err
			}

			if err = r.client.Create(context.TODO(), secret); err != nil {
				log.Info("start create a the secret ")
				if !errors.IsAlreadyExists(err) {
					return reconcile.Result{}, err
				}
			}
		}
	}
	r.reqLogger.Info("Creating syncset")
	newSS := &hivev1.SyncSet{}
	oldSS := &hivev1.SyncSet{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: request.Name + "-pd-sync", Namespace: instance.Namespace}, oldSS)
	//get if syncset already there
	if err != nil {
		r.reqLogger.Info("error finding the old syncset")
		if errors.IsNotFound(err) {
			r.reqLogger.Info("syncset not found , create a new one on this ")
			newSS = kube.GenerateSyncSet(request.Namespace, request.Name, secret)
			if err = controllerutil.SetControllerReference(instance, newSS, r.scheme); err != nil {
				r.reqLogger.Error(err, "Error setting controller reference on syncset")
				return reconcile.Result{}, err
			}
			if err := r.client.Create(context.TODO(), newSS); err != nil {
				return reconcile.Result{}, err
			}
		}
	}

	r.reqLogger.Info("Creating configmap")
	newCM := kube.GenerateConfigMap(request.Namespace, request.Name, pdData.ServiceID, pdData.IntegrationID)
	if err = controllerutil.SetControllerReference(instance, newCM, r.scheme); err != nil {
		r.reqLogger.Error(err, "Error setting controller reference on configmap")
		return reconcile.Result{}, err
	}
	if err := r.client.Create(context.TODO(), newCM); err != nil {
		if errors.IsAlreadyExists(err) {
			if updateErr := r.client.Update(context.TODO(), newCM); updateErr != nil {
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}
