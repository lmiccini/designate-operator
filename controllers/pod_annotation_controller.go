/*
Copyright 2024 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may
not use this file except in compliance with the License. You may obtain
a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
License for the specific language governing permissions and limitations
under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	designatev1beta1 "github.com/openstack-k8s-operators/designate-operator/api/v1beta1"
	"github.com/openstack-k8s-operators/designate-operator/pkg/designate"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	networkattachment "github.com/openstack-k8s-operators/lib-common/modules/common/networkattachment"
)

// PodAnnotationReconciler reconciles Pod objects to add predictable IP annotations
type PodAnnotationReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=designate.openstack.org,resources=designates,verbs=get;list;watch
//+kubebuilder:rbac:groups=designate.openstack.org,resources=designatebackendbind9s,verbs=get;list;watch
//+kubebuilder:rbac:groups=designate.openstack.org,resources=designatemdnses,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *PodAnnotationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Pod for predictable IP annotations")

	// Fetch the Pod instance
	pod := &corev1.Pod{}
	err := r.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			Log.Info("Pod resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		Log.Error(err, "Failed to get Pod")
		return ctrl.Result{}, err
	}

	// Check if this pod is a designate pod that needs predictable IP annotations
	if !r.isDesignatePod(pod) {
		return ctrl.Result{}, nil
	}

	// Handle pod deletion - release IP from ipset
	if pod.DeletionTimestamp != nil {
		return r.handlePodDeletion(ctx, pod, Log)
	}

	// Check if annotations are already set
	if pod.Annotations["designate.openstack.org/predictable-ip"] != "" {
		Log.Info("Pod already has predictable IP annotations, skipping")
		return ctrl.Result{}, nil
	}

	// Check if the pod is in a terminal state (succeeded, failed) - don't annotate these
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		Log.Info("Pod is in terminal state, skipping annotation")
		return ctrl.Result{}, nil
	}

	// Extract pod index from pod name
	podIndex, err := designate.ExtractPodIndexFromName(pod.Name)
	if err != nil {
		Log.Error(err, "Failed to extract pod index from pod name")
		return ctrl.Result{}, err
	}

	// Get the parent Designate CR
	parentDesignate, err := r.getParentDesignate(ctx, pod)
	if err != nil {
		Log.Error(err, "Failed to get parent Designate CR")
		return ctrl.Result{}, err
	}

	// Get network parameters
	h, err := helper.NewHelper(parentDesignate, r.Client, r.Kclient, r.Scheme, Log)
	if err != nil {
		Log.Error(err, "Failed to create helper")
		return ctrl.Result{}, err
	}

	nad, err := networkattachment.GetNADWithName(ctx, h, parentDesignate.Spec.DesignateNetworkAttachment, pod.Namespace)
	if err != nil {
		Log.Error(err, "Failed to get network attachment definition")
		return ctrl.Result{}, err
	}

	networkParameters, err := designate.GetNetworkParametersFromNAD(nad)
	if err != nil {
		Log.Error(err, "Failed to get network parameters")
		return ctrl.Result{}, err
	}

	predictableIPParams, err := designate.GetPredictableIPAM(networkParameters)
	if err != nil {
		Log.Error(err, "Failed to get predictable IP parameters")
		return ctrl.Result{}, err
	}

	// Determine network name and interface name based on pod type
	networkName := parentDesignate.Spec.DesignateNetworkAttachment
	interfaceName := "designate"

	// Create per-pod annotations using ipset ConfigMaps
	annotations, err := r.createIPSetAnnotations(networkName, interfaceName, podIndex, predictableIPParams, Log)
	if err != nil {
		Log.Error(err, "Failed to create ipset annotations")
		return ctrl.Result{}, err
	}

	// Update pod annotations with retry logic to handle race conditions
	err = r.updatePodAnnotationsWithRetry(ctx, pod, annotations, Log)
	if err != nil {
		Log.Error(err, "Failed to update pod annotations after retries")
		return ctrl.Result{}, err
	}

	Log.Info(fmt.Sprintf("Successfully added predictable IP annotations to pod %s: %s", pod.Name, annotations["designate.openstack.org/predictable-ip"]))
	return ctrl.Result{}, nil
}

// isDesignatePod checks if the pod is a designate pod that needs predictable IP annotations
func (r *PodAnnotationReconciler) isDesignatePod(pod *corev1.Pod) bool {
	// Check if pod has designate-related labels
	if pod.Labels["app.kubernetes.io/name"] == "designate" {
		return true
	}

	// Check if pod is owned by a designate StatefulSet
	if pod.OwnerReferences != nil {
		for _, owner := range pod.OwnerReferences {
			if owner.Kind == "StatefulSet" {
				// Check if the StatefulSet name contains designate service names
				designateServices := []string{"designate-backendbind9", "designate-mdns", "designate-unbound"}
				for _, service := range designateServices {
					if strings.Contains(owner.Name, service) {
						return true
					}
				}
			}
		}
	}

	// Check if pod name matches designate StatefulSet pod naming pattern
	// StatefulSet pods are named as {statefulset-name}-{ordinal}
	designateServices := []string{"designate-backendbind9", "designate-mdns", "designate-unbound"}
	for _, service := range designateServices {
		if strings.HasPrefix(pod.Name, service+"-") {
			// Additional check: ensure it ends with a number (ordinal)
			suffix := strings.TrimPrefix(pod.Name, service+"-")
			if len(suffix) > 0 && suffix[0] >= '0' && suffix[0] <= '9' {
				return true
			}
		}
	}

	return false
}

// getParentDesignate gets the parent Designate CR for the pod
func (r *PodAnnotationReconciler) getParentDesignate(ctx context.Context, pod *corev1.Pod) (*designatev1beta1.Designate, error) {
	// Try to find the parent Designate CR by looking at owner references or labels
	// For now, we'll look for a Designate CR in the same namespace
	designateList := &designatev1beta1.DesignateList{}
	err := r.List(ctx, designateList, client.InNamespace(pod.Namespace))
	if err != nil {
		return nil, err
	}

	if len(designateList.Items) == 0 {
		return nil, fmt.Errorf("no Designate CR found in namespace %s", pod.Namespace)
	}

	// Return the first Designate CR found
	// In a more sophisticated implementation, you might want to match based on labels or owner references
	return &designateList.Items[0], nil
}

// mergeAnnotations merges new annotations into existing ones
func mergeAnnotations(existing, new map[string]string) map[string]string {
	if existing == nil {
		existing = make(map[string]string)
	}

	for k, v := range new {
		existing[k] = v
	}

	return existing
}

// updatePodAnnotationsWithRetry updates pod annotations with retry logic to handle race conditions
func (r *PodAnnotationReconciler) updatePodAnnotationsWithRetry(ctx context.Context, pod *corev1.Pod, annotations map[string]string, Log logr.Logger) error {
	const maxRetries = 5
	const baseDelay = 100 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Merge annotations
		pod.Annotations = mergeAnnotations(pod.Annotations, annotations)

		// Try to update the pod
		err := r.Update(ctx, pod)
		if err == nil {
			// Success!
			return nil
		}

		// Check if it's a conflict error (object modified)
		if k8s_errors.IsConflict(err) {
			Log.Info(fmt.Sprintf("Pod %s was modified by another controller, retrying (attempt %d/%d)", pod.Name, attempt+1, maxRetries))

			// Re-fetch the pod to get the latest version
			freshPod := &corev1.Pod{}
			err = r.Get(ctx, client.ObjectKeyFromObject(pod), freshPod)
			if err != nil {
				return fmt.Errorf("failed to re-fetch pod after conflict: %w", err)
			}

			// Update our local copy with the fresh data
			*pod = *freshPod

			// Wait before retrying (exponential backoff)
			if attempt < maxRetries-1 {
				delay := baseDelay * time.Duration(1<<attempt) // 100ms, 200ms, 400ms, 800ms
				time.Sleep(delay)
			}
			continue
		}

		// For non-conflict errors, return immediately
		return fmt.Errorf("failed to update pod annotations: %w", err)
	}

	return fmt.Errorf("failed to update pod annotations after %d attempts due to conflicts", maxRetries)
}

// createIPSetAnnotations creates pod annotations using ipset ConfigMaps for IP allocation
func (r *PodAnnotationReconciler) createIPSetAnnotations(networkName, interfaceName string, podIndex int, predParams *designate.NADIpam, Log logr.Logger) (map[string]string, error) {
	// Create ipset manager for this network using ConfigMaps
	ipsetName := fmt.Sprintf("designate-%s", networkName)
	ipsetMgr := designate.NewIPSetManager(r.Client, r.Scheme, ipsetName, "openstack")

	// Allocate IP using ipset ConfigMaps
	predictableIP, err := ipsetMgr.AllocateIP(predParams.CIDR, podIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate IP via ipset ConfigMap: %w", err)
	}

	Log.Info(fmt.Sprintf("Allocated IP %s for pod index %d via ipset ConfigMap", predictableIP, podIndex))

	// Create Multus annotation with the specific IP
	networks := []designate.MultusNetworkConfig{
		{
			Name:      networkName,
			Interface: interfaceName,
			IPs:       []string{predictableIP},
		},
	}

	multusAnnotation, err := designate.CreateMultusAnnotation(networks)
	if err != nil {
		return nil, err
	}

	annotations := make(map[string]string)
	annotations["k8s.v1.cni.cncf.io/networks"] = multusAnnotation
	annotations["designate.openstack.org/predictable-ip"] = predictableIP
	annotations["designate.openstack.org/pod-index"] = fmt.Sprintf("%d", podIndex)
	annotations["designate.openstack.org/ipset-name"] = ipsetName

	return annotations, nil
}

// handlePodDeletion releases the IP from ipset ConfigMap when pod is deleted
func (r *PodAnnotationReconciler) handlePodDeletion(ctx context.Context, pod *corev1.Pod, Log logr.Logger) (ctrl.Result, error) {
	predictableIP := pod.Annotations["designate.openstack.org/predictable-ip"]
	ipsetName := pod.Annotations["designate.openstack.org/ipset-name"]

	if predictableIP != "" && ipsetName != "" {
		ipsetMgr := designate.NewIPSetManager(r.Client, r.Scheme, ipsetName, "openstack")
		if err := ipsetMgr.ReleaseIP(predictableIP); err != nil {
			Log.Error(err, fmt.Sprintf("Failed to release IP %s from ipset ConfigMap %s", predictableIP, ipsetName))
		} else {
			Log.Info(fmt.Sprintf("Released IP %s from ipset ConfigMap %s", predictableIP, ipsetName))
		}
	}

	return ctrl.Result{}, nil
}

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *PodAnnotationReconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("PodAnnotation")
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodAnnotationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			pod := obj.(*corev1.Pod)
			return r.isDesignatePod(pod)
		})).
		Complete(r)
}
