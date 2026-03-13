/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pivotv1alpha1 "github.com/syscode-labs/oci-pivot-controller/api/v1alpha1"
	ociutil "github.com/syscode-labs/oci-pivot-controller/internal/oci"
)

const (
	finalizerName       = "pivot.oci.io/finalizer"
	defaultCompartment  = "" // overridden by controller flag at startup
)

// PivotIPReconciler reconciles PivotIP objects.
type PivotIPReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	OCI                 *ociutil.Client
	DefaultCompartment  string
}

// +kubebuilder:rbac:groups=pivot.oci.io,resources=pivotips,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=pivot.oci.io,resources=pivotips/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=pivot.oci.io,resources=pivotips/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

func (r *PivotIPReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var pip pivotv1alpha1.PivotIP
	if err := r.Get(ctx, req.NamespacedName, &pip); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !pip.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &pip)
	}

	if !controllerutil.ContainsFinalizer(&pip, finalizerName) {
		controllerutil.AddFinalizer(&pip, finalizerName)
		if err := r.Update(ctx, &pip); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	result, err := r.reconcileNormal(ctx, &pip)
	if err != nil {
		log.Error(err, "reconcile failed")
		r.setCondition(&pip, pivotv1alpha1.ConditionAssigned, metav1.ConditionFalse, "ReconcileError", err.Error())
		_ = r.Status().Update(ctx, &pip)
	}
	return result, err
}

func (r *PivotIPReconciler) reconcileNormal(ctx context.Context, pip *pivotv1alpha1.PivotIP) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	compartment := pip.Spec.CompartmentID
	if compartment == "" {
		compartment = r.DefaultCompartment
	}

	// Elect the best node (fewest current pivot assignments, must be Ready).
	elected, err := r.electNode(ctx, pip)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("elect node: %w", err)
	}

	privateIPOCID := pip.Status.PrivateIPOCID
	publicIPOCID := pip.Status.PublicIPOCID
	assignedNode := pip.Status.AssignedNode

	if assignedNode != elected.Name {
		log.Info("node change detected", "from", assignedNode, "to", elected.Name)

		instanceOCID := ociutil.InstanceOCIDFromProviderID(elected.Spec.ProviderID)
		if instanceOCID == "" {
			return ctrl.Result{}, fmt.Errorf("node %s has no providerID", elected.Name)
		}

		vnicID, err := r.OCI.PrimaryVNICForInstance(ctx, instanceOCID)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("get vnic for node %s: %w", elected.Name, err)
		}

		// Delete the old secondary private IP (if any) — it is tied to the old node's VNIC.
		if privateIPOCID != "" {
			if err := r.OCI.DeletePrivateIP(ctx, privateIPOCID); err != nil {
				log.Error(err, "failed to delete old secondary private IP (continuing)", "ocid", privateIPOCID)
			}
			privateIPOCID = ""
		}

		// Create a new secondary private IP on the elected node's VNIC.
		var privAddr string
		privateIPOCID, privAddr, err = r.OCI.CreateSecondaryPrivateIP(ctx, vnicID)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("create secondary private ip: %w", err)
		}
		log.Info("secondary private IP created", "ocid", privateIPOCID, "ip", privAddr)

		if publicIPOCID != "" {
			// Move the existing reserved public IP to point at the new secondary private IP.
			if err := r.OCI.ReassignPublicIP(ctx, publicIPOCID, privateIPOCID); err != nil {
				return ctrl.Result{}, fmt.Errorf("reassign public ip: %w", err)
			}
			log.Info("reserved public IP reassigned", "publicIPOCID", publicIPOCID, "newPrivateIPOCID", privateIPOCID)
		} else {
			// First assignment: create the reserved public IP.
			displayName := fmt.Sprintf("pivot-%s-%s", pip.Namespace, pip.Name)
			var pubAddr string
			publicIPOCID, pubAddr, err = r.OCI.CreateReservedPublicIP(ctx, privateIPOCID, displayName)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("create reserved public ip: %w", err)
			}
			log.Info("reserved public IP created", "ocid", publicIPOCID, "ip", pubAddr)
		}

		assignedNode = elected.Name
	}

	// Fetch the current public IP address (may have changed after reassignment).
	publicIPAddr, err := r.OCI.GetPublicIPAddress(ctx, publicIPOCID)
	if err != nil {
		return ctrl.Result{RequeueAfter: requeueAfterProvisioning}, fmt.Errorf("get public ip address: %w", err)
	}

	// Patch Service.spec.externalIPs so the elected node intercepts traffic.
	if err := r.patchServiceExternalIPs(ctx, pip.Namespace, pip.Spec.ServiceRef.Name, publicIPAddr); err != nil {
		return ctrl.Result{}, err
	}

	// Persist status.
	pip.Status.PublicIP = publicIPAddr
	pip.Status.PublicIPOCID = publicIPOCID
	pip.Status.PrivateIPOCID = privateIPOCID
	pip.Status.AssignedNode = assignedNode
	r.setCondition(pip, pivotv1alpha1.ConditionAssigned, metav1.ConditionTrue, "Assigned",
		fmt.Sprintf("IP %s assigned to node %s", publicIPAddr, assignedNode))

	return ctrl.Result{}, r.Status().Update(ctx, pip)
}

func (r *PivotIPReconciler) reconcileDelete(ctx context.Context, pip *pivotv1alpha1.PivotIP) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if pip.Status.PublicIPOCID != "" {
		if err := r.OCI.DeletePublicIP(ctx, pip.Status.PublicIPOCID); err != nil {
			log.Error(err, "failed to delete reserved public IP", "ocid", pip.Status.PublicIPOCID)
		}
	}

	if pip.Status.PrivateIPOCID != "" {
		if err := r.OCI.DeletePrivateIP(ctx, pip.Status.PrivateIPOCID); err != nil {
			log.Error(err, "failed to delete secondary private IP", "ocid", pip.Status.PrivateIPOCID)
		}
	}

	// Remove externalIPs from the Service if it still exists.
	svc := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Namespace: pip.Namespace, Name: pip.Spec.ServiceRef.Name}, svc)
	if err == nil {
		patch := client.MergeFrom(svc.DeepCopy())
		svc.Spec.ExternalIPs = nil
		if patchErr := r.Patch(ctx, svc, patch); patchErr != nil {
			log.Error(patchErr, "failed to clear externalIPs from service")
		}
	} else if !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(pip, finalizerName)
	return ctrl.Result{}, r.Update(ctx, pip)
}

// electNode picks the Ready node with the fewest currently assigned PivotIPs.
// If the current node is still Ready, it is preferred on ties (avoids churn).
func (r *PivotIPReconciler) electNode(ctx context.Context, pip *pivotv1alpha1.PivotIP) (*corev1.Node, error) {
	var nodeList corev1.NodeList
	opts := []client.ListOption{}
	if len(pip.Spec.NodeSelector) > 0 {
		opts = append(opts, client.MatchingLabels(pip.Spec.NodeSelector))
	}
	if err := r.List(ctx, &nodeList, opts...); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	var ready []*corev1.Node
	for i := range nodeList.Items {
		if isNodeReady(&nodeList.Items[i]) {
			ready = append(ready, &nodeList.Items[i])
		}
	}
	if len(ready) == 0 {
		return nil, fmt.Errorf("no Ready nodes available (selector: %v)", pip.Spec.NodeSelector)
	}

	// Count assignments per node across all PivotIPs in this namespace.
	var allPIPs pivotv1alpha1.PivotIPList
	if err := r.List(ctx, &allPIPs, client.InNamespace(pip.Namespace)); err != nil {
		return nil, fmt.Errorf("list pivotips: %w", err)
	}
	counts := make(map[string]int, len(ready))
	for _, p := range allPIPs.Items {
		if p.Name != pip.Name && p.Status.AssignedNode != "" {
			counts[p.Status.AssignedNode]++
		}
	}

	best := ready[0]
	// Start score: current node gets -1 to win ties (avoid unnecessary moves).
	bestScore := counts[best.Name]
	if best.Name == pip.Status.AssignedNode {
		bestScore = -1
	}

	for _, n := range ready[1:] {
		score := counts[n.Name]
		if n.Name == pip.Status.AssignedNode {
			score = -1
		}
		if score < bestScore {
			best = n
			bestScore = score
		}
	}

	return best, nil
}

func (r *PivotIPReconciler) patchServiceExternalIPs(ctx context.Context, namespace, name, publicIP string) error {
	svc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, svc); err != nil {
		return fmt.Errorf("get service %s/%s: %w", namespace, name, err)
	}
	if len(svc.Spec.ExternalIPs) == 1 && svc.Spec.ExternalIPs[0] == publicIP {
		return nil // already correct
	}
	patch := client.MergeFrom(svc.DeepCopy())
	svc.Spec.ExternalIPs = []string{publicIP}
	return r.Patch(ctx, svc, patch)
}

func (r *PivotIPReconciler) setCondition(pip *pivotv1alpha1.PivotIP, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range pip.Status.Conditions {
		if c.Type == condType {
			pip.Status.Conditions[i].Status = status
			pip.Status.Conditions[i].Reason = reason
			pip.Status.Conditions[i].Message = message
			pip.Status.Conditions[i].LastTransitionTime = now
			return
		}
	}
	pip.Status.Conditions = append(pip.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

func isNodeReady(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// requeueAfterProvisioning is used when the OCI API hasn't assigned an IP address yet.
const requeueAfterProvisioning = 5 * 1e9 // 5 seconds in nanoseconds

// SetupWithManager registers the controller and watches PivotIP + Node resources.
// Node watch triggers re-election when a node becomes NotReady.
func (r *PivotIPReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// When a Node changes readiness, requeue all PivotIPs that reference it.
	nodeMapper := handler.MapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		var pipList pivotv1alpha1.PivotIPList
		if err := mgr.GetClient().List(ctx, &pipList); err != nil {
			return nil
		}
		node := obj.GetName()
		var reqs []reconcile.Request
		for _, pip := range pipList.Items {
			if pip.Status.AssignedNode == node || pip.Status.AssignedNode == "" {
				reqs = append(reqs, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: pip.Namespace,
						Name:      pip.Name,
					},
				})
			}
		}
		return reqs
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&pivotv1alpha1.PivotIP{}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(nodeMapper)).
		Named("pivotip").
		Complete(r)
}
