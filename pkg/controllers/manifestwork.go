/*
Copyright 2022.

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
	"fmt"
	"time"

	hyp "github.com/openshift/hypershift/api/v1alpha1"
	hypdeployment "github.com/stolostron/hypershift-deployment-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	workv1 "open-cluster-management.io/api/work/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	ManifestTargetNamespace       = "manifestwork-target-namespace"
	CreatedByHypershiftDeployment = "created-by-hypershiftdeployment"
	NamespaceNameSeperator        = "/"
)

func ScafoldManifestwork(hyd *hypdeployment.HypershiftDeployment) *workv1.ManifestWork {
	return &workv1.ManifestWork{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      hyd.GetName(),
			Namespace: getTargetNamespace(hyd),
			Annotations: map[string]string{
				CreatedByHypershiftDeployment: fmt.Sprintf("%s%s%s",
					hyd.GetNamespace(),
					NamespaceNameSeperator,
					hyd.GetName()),
			},
		},
		Spec: workv1.ManifestWorkSpec{},
	}
}

func getManifestWorkKey(hyd *hypdeployment.HypershiftDeployment) types.NamespacedName {
	return types.NamespacedName{
		Name:      hyd.GetName(),
		Namespace: getTargetNamespace(hyd),
	}
}

func (r *HypershiftDeploymentReconciler) createMainfestwork(ctx context.Context, req ctrl.Request, hyd *hypdeployment.HypershiftDeployment) (ctrl.Result, error) {
	m := ScafoldManifestwork(hyd)

	// if the manifestwork is created, then move the status to hypershiftDeployment
	// TODO: @ianzhang366 might want to do some upate/patch when the manifestwork is created.
	if err := r.Get(ctx, getManifestWorkKey(hyd), m); err == nil {
		workConds := m.Status.Conditions

		for _, cond := range workConds {
			if err := r.updateStatusConditionsOnChange(
				hyd,
				hypdeployment.ConditionType(cond.Type),
				metav1.ConditionTrue,
				cond.Message,
				cond.Reason,
			); err != nil {
				r.Log.Info(fmt.Sprintf("update status condition failed for %s%s, err: %v", getTargetNamespace(hyd), hyd.GetName(), err))
				return ctrl.Result{RequeueAfter: 10 * time.Second, Requeue: true}, nil
			}
		}

		return ctrl.Result{}, nil
	}

	appendSecrets, err := r.appendReferenceSecrets(ctx, hyd)
	if err != nil {
		return ctrl.Result{}, err
	}
	payload := []workv1.Manifest{}

	manifestFuncs := []loadManifest{
		appendHostedCluster,
		appendNodePool,
		appendSecrets,
	}

	for _, f := range manifestFuncs {
		f(hyd, &payload)
	}

	m.Spec.Workload.Manifests = payload

	// 	if err := controllerutil.SetOwnerReference(hyd, m, r.Scheme); err != nil {
	// 		return ctrl.Result{}, fmt.Errorf("failed to set manifestwork's owner as %s, err: %w", req, err)
	// 	}

	if err := r.Create(r.ctx, m); err != nil {
		if apierrors.IsAlreadyExists(err) {
			//TODO: ianzhang366, might want to patch the manifestwork over here.
			return ctrl.Result{}, r.Update(r.ctx, m)
		}

		return ctrl.Result{}, fmt.Errorf("failed to create manifestwork based on hypershiftDeployment: %s, err: %w", req, err)
	}

	r.Log.Info(fmt.Sprintf("created manifestwork for hypershiftDeployment: %s at targetNamespace: %s", req, getTargetNamespace(hyd)))

	return ctrl.Result{}, nil
}

func (r *HypershiftDeploymentReconciler) deleteManifestworkWaitCleanUp(ctx context.Context, hyd *hypdeployment.HypershiftDeployment) (ctrl.Result, error) {

	m := ScafoldManifestwork(hyd)

	if err := r.Delete(ctx, m); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to delete manifestwork, err: %v", err)
		}
	}

	for _, np := range hyd.Spec.NodePools {
		var nodePool hyp.NodePool
		if err := r.Get(ctx, types.NamespacedName{Namespace: getTargetNamespace(hyd), Name: np.Name}, &nodePool); err == nil {
			r.Log.Info(fmt.Sprintf("Waiting for NodePool %s/%s to be deleted", getTargetNamespace(hyd), np.Name))
			return ctrl.Result{RequeueAfter: 10 * time.Second, Requeue: true}, nil
		} else {
			r.Log.Info(fmt.Sprintf("NodePool %s/%s already deleted...", getTargetNamespace(hyd), hyd.Name))
		}
	}

	// Delete the HostedCluster
	var hc hyp.HostedCluster
	if err := r.Get(ctx, types.NamespacedName{Namespace: getTargetNamespace(hyd), Name: hyd.Name}, &hc); !apierrors.IsNotFound(err) {
		r.Log.Info(fmt.Sprintf("Waiting for HostedCluster %s/%s to be deleted", getTargetNamespace(hyd), hyd.Name))
		return ctrl.Result{RequeueAfter: 10 * time.Second, Requeue: true}, nil
	} else {
		r.Log.Info(fmt.Sprintf("HostedCluster %s/%s already deleted...", getTargetNamespace(hyd), hyd.Name))
	}

	return ctrl.Result{}, nil
}

func (r *HypershiftDeploymentReconciler) appendReferenceSecrets(ctx context.Context, hyd *hypdeployment.HypershiftDeployment) (loadManifest, error) {
	cpoCreds := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: hyd.Spec.HostedClusterSpec.Platform.AWS.ControlPlaneOperatorCreds.Name,
		Namespace: hyd.GetNamespace()}, cpoCreds); err != nil {
		return nil, fmt.Errorf("failed to get the cpo creds, err: %w", err)
	}

	kccCreds := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: hyd.Spec.HostedClusterSpec.Platform.AWS.KubeCloudControllerCreds.Name,
		Namespace: hyd.GetNamespace()}, kccCreds); err != nil {
		return nil, fmt.Errorf("failed to get the cpo creds, err: %w", err)
	}

	nmcCreds := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: hyd.Spec.HostedClusterSpec.Platform.AWS.NodePoolManagementCreds.Name,
		Namespace: hyd.GetNamespace()}, nmcCreds); err != nil {
		return nil, fmt.Errorf("failed to get the cpo creds, err: %w", err)
	}

	pullCreds := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: hyd.Spec.HostedClusterSpec.PullSecret.Name,
		Namespace: hyd.GetNamespace()}, pullCreds); err != nil {
		return nil, fmt.Errorf("failed to get the cpo creds, err: %w", err)
	}

	tempSecret := func(in *corev1.Secret) *corev1.Secret {
		out := &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: corev1.SchemeGroupVersion.String(),
			},
		}

		out.SetName(in.GetName())
		out.SetNamespace(getTargetNamespace(hyd))
		out.SetLabels(in.GetLabels())
		out.Data = in.Data

		return out
	}

	refSecrets := []*corev1.Secret{cpoCreds, kccCreds, nmcCreds, pullCreds}

	return func(hyd *hypdeployment.HypershiftDeployment, payload *[]workv1.Manifest) {
		for _, s := range refSecrets {
			o := tempSecret(s)
			*payload = append(*payload, workv1.Manifest{RawExtension: runtime.RawExtension{Object: o}})
		}

	}, nil
}

func getTargetNamespace(hyd *hypdeployment.HypershiftDeployment) string {
	anno := hyd.GetAnnotations()
	if len(anno) == 0 || len(anno[ManifestTargetNamespace]) == 0 {
		return hyd.GetNamespace()
	}

	return anno[ManifestTargetNamespace]
}

func appendHostedCluster(hyd *hypdeployment.HypershiftDeployment, payload *[]workv1.Manifest) {
	hc := &hyp.HostedCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "HostedCluster",
			APIVersion: hyp.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      hyd.GetName(),
			Namespace: getTargetNamespace(hyd), //TODO: ianzhang366, move it to user's namespace from hd.spec
		},
		Spec: *hyd.Spec.HostedClusterSpec,
	}

	*payload = append(*payload, workv1.Manifest{RawExtension: runtime.RawExtension{Object: hc}})
}

func appendNodePool(hyd *hypdeployment.HypershiftDeployment, payload *[]workv1.Manifest) {
	for _, hdNp := range hyd.Spec.NodePools {
		np := &hyp.NodePool{
			TypeMeta: metav1.TypeMeta{
				Kind:       "NodePool",
				APIVersion: hyp.GroupVersion.String(),
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      hdNp.Name,
				Namespace: getTargetNamespace(hyd),
			},
			Spec: hdNp.Spec,
		}

		*payload = append(*payload, workv1.Manifest{RawExtension: runtime.RawExtension{Object: np}})
	}
}