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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PivotIPSpec defines the desired state of PivotIP.
type PivotIPSpec struct {
	// ServiceRef is the Service in the same namespace to assign a floating public IP to.
	// The controller will set Service.spec.externalIPs to the assigned OCI reserved public IP.
	// +required
	ServiceRef corev1.LocalObjectReference `json:"serviceRef"`

	// NodeSelector restricts which nodes are eligible to hold the floating IP.
	// Nodes must also be Ready. If omitted, all Ready nodes are eligible.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// CompartmentID is the OCI compartment OCID in which to create secondary private IPs
	// and reserved public IPs. If omitted, the controller's --compartment-id flag is used.
	// +optional
	CompartmentID string `json:"compartmentId,omitempty"`
}

// PivotIPStatus defines the observed state of PivotIP.
type PivotIPStatus struct {
	// PublicIP is the assigned OCI reserved public IP address (e.g. "1.2.3.4").
	// +optional
	PublicIP string `json:"publicIP,omitempty"`

	// PublicIPOCID is the OCID of the OCI reserved public IP resource.
	// +optional
	PublicIPOCID string `json:"publicIPOCID,omitempty"`

	// PrivateIPOCID is the OCID of the OCI secondary private IP on the elected node's VNIC.
	// +optional
	PrivateIPOCID string `json:"privateIPOCID,omitempty"`

	// AssignedNode is the name of the Kubernetes Node currently holding the floating IP.
	// +optional
	AssignedNode string `json:"assignedNode,omitempty"`

	// Conditions summarise the current state of this PivotIP.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types.
const (
	// ConditionAssigned is True when a public IP has been successfully assigned to a node.
	ConditionAssigned = "Assigned"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Public IP",type=string,JSONPath=`.status.publicIP`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.status.assignedNode`
// +kubebuilder:printcolumn:name="Service",type=string,JSONPath=`.spec.serviceRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PivotIP assigns a floating OCI reserved public IP to a Kubernetes Service.
// The controller elects the healthy node with the fewest current assignments,
// creates a secondary private IP on its VNIC, attaches a reserved public IP,
// and sets Service.spec.externalIPs so the node intercepts and routes traffic.
type PivotIP struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec PivotIPSpec `json:"spec"`

	// +optional
	Status PivotIPStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PivotIPList contains a list of PivotIP.
type PivotIPList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PivotIP `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PivotIP{}, &PivotIPList{})
}
