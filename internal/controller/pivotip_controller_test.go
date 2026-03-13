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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pivotv1alpha1 "github.com/syscode-labs/oci-pivot-controller/api/v1alpha1"
	"github.com/syscode-labs/oci-pivot-controller/internal/oci/fake"
)

// newReconciler returns a PivotIPReconciler wired to envtest's k8sClient and a fresh fake OCI client.
func newReconciler(ociClient *fake.Client) *PivotIPReconciler {
	return &PivotIPReconciler{
		Client:             k8sClient,
		Scheme:             k8sClient.Scheme(),
		OCI:                ociClient,
		DefaultCompartment: "ocid1.compartment.test",
	}
}

// makeNode creates a Ready node in envtest with the given name and providerID.
func makeNode(ctx context.Context, name, providerID string) { //nolint:unparam
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{ProviderID: providerID},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
	Expect(k8sClient.Create(ctx, node)).To(Succeed())
	// Status must be updated separately in envtest.
	Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
}

// makeService creates a ClusterIP Service in the default namespace.
func makeService(ctx context.Context, name string) { //nolint:unparam
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Port: 80}},
		},
	}
	Expect(k8sClient.Create(ctx, svc)).To(Succeed())
}

// makePivotIP creates a PivotIP resource in the default namespace.
func makePivotIP(ctx context.Context, name, serviceName string) *pivotv1alpha1.PivotIP {
	pip := &pivotv1alpha1.PivotIP{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: pivotv1alpha1.PivotIPSpec{
			ServiceRef: corev1.LocalObjectReference{Name: serviceName},
		},
	}
	Expect(k8sClient.Create(ctx, pip)).To(Succeed())
	return pip
}

func reconcileNN(ctx context.Context, r *PivotIPReconciler, name string) (reconcile.Result, error) {
	return r.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: name},
	})
}

var _ = Describe("PivotIP Controller", func() {
	ctx := context.Background()

	Describe("Finalizer management", func() {
		It("adds the finalizer on first reconcile", func() {
			ociClient := fake.New()
			r := newReconciler(ociClient)

			makeNode(ctx, "node-fin-1", "oci://ocid1.instance.fin1")
			ociClient.VNICByInstance["ocid1.instance.fin1"] = "vnic-fin-1"
			makeService(ctx, "svc-fin")
			pip := makePivotIP(ctx, "pip-fin", "svc-fin")

			_, err := reconcileNN(ctx, r, pip.Name)
			Expect(err).NotTo(HaveOccurred())

			updated := &pivotv1alpha1.PivotIP{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: pip.Name}, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(finalizerName))

			// cleanup
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-fin-1"}})).To(Succeed())
			Expect(k8sClient.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc-fin", Namespace: "default"}})).To(Succeed())
		})
	})

	Describe("First assignment", func() {
		var (
			ociClient *fake.Client
			r         *PivotIPReconciler
			pip       *pivotv1alpha1.PivotIP
		)

		BeforeEach(func() {
			ociClient = fake.New()
			r = newReconciler(ociClient)

			makeNode(ctx, "node-a1", "oci://ocid1.instance.a1")
			ociClient.VNICByInstance["ocid1.instance.a1"] = "vnic-a1"
			makeService(ctx, "svc-a")
			pip = makePivotIP(ctx, "pip-a", "svc-a")
		})

		AfterEach(func() {
			updated := &pivotv1alpha1.PivotIP{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: pip.Name}, updated); err == nil {
				updated.Finalizers = nil
				Expect(k8sClient.Update(ctx, updated)).To(Succeed())
				Expect(k8sClient.Delete(ctx, updated)).To(Succeed())
			}
			_ = k8sClient.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a1"}})
			_ = k8sClient.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc-a", Namespace: "default"}})
		})

		It("creates a secondary private IP and reserved public IP on first reconcile", func() {
			// First reconcile — adds finalizer only.
			_, err := reconcileNN(ctx, r, pip.Name)
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile — does the OCI work.
			_, err = reconcileNN(ctx, r, pip.Name)
			Expect(err).NotTo(HaveOccurred())

			Expect(ociClient.CallsFor("CreateSecondaryPrivateIP")).To(HaveLen(1))
			Expect(ociClient.CallsFor("CreateReservedPublicIP")).To(HaveLen(1))
			Expect(ociClient.CallsFor("ReassignPublicIP")).To(BeEmpty())
		})

		It("patches Service.spec.externalIPs with the assigned public IP", func() {
			_, _ = reconcileNN(ctx, r, pip.Name)
			_, err := reconcileNN(ctx, r, pip.Name)
			Expect(err).NotTo(HaveOccurred())

			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "svc-a"}, svc)).To(Succeed())
			Expect(svc.Spec.ExternalIPs).To(HaveLen(1))
			Expect(svc.Spec.ExternalIPs[0]).To(MatchRegexp(`^\d+\.\d+\.\d+\.\d+$`))
		})

		It("records the assigned node and OCIDs in status", func() {
			_, _ = reconcileNN(ctx, r, pip.Name)
			_, err := reconcileNN(ctx, r, pip.Name)
			Expect(err).NotTo(HaveOccurred())

			updated := &pivotv1alpha1.PivotIP{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: pip.Name}, updated)).To(Succeed())
			Expect(updated.Status.AssignedNode).To(Equal("node-a1"))
			Expect(updated.Status.PublicIPOCID).NotTo(BeEmpty())
			Expect(updated.Status.PrivateIPOCID).NotTo(BeEmpty())
			Expect(updated.Status.PublicIP).NotTo(BeEmpty())
		})
	})

	Describe("Node failover", func() {
		It("reassigns the public IP when the assigned node changes", func() {
			ociClient := fake.New()
			r := newReconciler(ociClient)

			makeNode(ctx, "node-b1", "oci://ocid1.instance.b1")
			makeNode(ctx, "node-b2", "oci://ocid1.instance.b2")
			ociClient.VNICByInstance["ocid1.instance.b1"] = "vnic-b1"
			ociClient.VNICByInstance["ocid1.instance.b2"] = "vnic-b2"
			makeService(ctx, "svc-b")
			pip := makePivotIP(ctx, "pip-b", "svc-b")

			// Bootstrap: add finalizer + first assignment.
			_, _ = reconcileNN(ctx, r, pip.Name)
			_, err := reconcileNN(ctx, r, pip.Name)
			Expect(err).NotTo(HaveOccurred())
			ociClient.Reset()

			// Simulate node-b1 going NotReady — mark it not-ready in envtest.
			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "node-b1"}, node)).To(Succeed())
			node.Status.Conditions = []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			}
			Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

			// Reconcile — should elect node-b2 and reassign.
			_, err = reconcileNN(ctx, r, pip.Name)
			Expect(err).NotTo(HaveOccurred())

			Expect(ociClient.CallsFor("DeletePrivateIP")).To(HaveLen(1))
			Expect(ociClient.CallsFor("CreateSecondaryPrivateIP")).To(HaveLen(1))
			Expect(ociClient.CallsFor("ReassignPublicIP")).To(HaveLen(1))
			Expect(ociClient.CallsFor("CreateReservedPublicIP")).To(BeEmpty())

			updated := &pivotv1alpha1.PivotIP{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: pip.Name}, updated)).To(Succeed())
			Expect(updated.Status.AssignedNode).To(Equal("node-b2"))

			// cleanup
			updated.Finalizers = nil
			Expect(k8sClient.Update(ctx, updated)).To(Succeed())
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())
			_ = k8sClient.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-b1"}})
			_ = k8sClient.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-b2"}})
			_ = k8sClient.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc-b", Namespace: "default"}})
		})
	})

	Describe("Deletion", func() {
		It("cleans up OCI resources and clears externalIPs on deletion", func() {
			ociClient := fake.New()
			r := newReconciler(ociClient)

			makeNode(ctx, "node-d1", "oci://ocid1.instance.d1")
			ociClient.VNICByInstance["ocid1.instance.d1"] = "vnic-d1"
			makeService(ctx, "svc-d")
			pip := makePivotIP(ctx, "pip-d", "svc-d")

			// Bootstrap.
			_, _ = reconcileNN(ctx, r, pip.Name)
			_, err := reconcileNN(ctx, r, pip.Name)
			Expect(err).NotTo(HaveOccurred())
			ociClient.Reset()

			// Trigger deletion.
			updated := &pivotv1alpha1.PivotIP{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: pip.Name}, updated)).To(Succeed())
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())

			// Reconcile deletion.
			_, err = reconcileNN(ctx, r, pip.Name)
			Expect(err).NotTo(HaveOccurred())

			Expect(ociClient.CallsFor("DeletePublicIP")).To(HaveLen(1))
			Expect(ociClient.CallsFor("DeletePrivateIP")).To(HaveLen(1))

			// Service externalIPs should be cleared.
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "svc-d"}, svc)).To(Succeed())
			Expect(svc.Spec.ExternalIPs).To(BeEmpty())

			// cleanup
			_ = k8sClient.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-d1"}})
			_ = k8sClient.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc-d", Namespace: "default"}})
		})
	})

	Describe("Election balance", func() {
		It("elects the node with the fewest existing assignments", func() {
			ociClient := fake.New()
			r := newReconciler(ociClient)

			makeNode(ctx, "node-e1", "oci://ocid1.instance.e1")
			makeNode(ctx, "node-e2", "oci://ocid1.instance.e2")
			makeNode(ctx, "node-e3", "oci://ocid1.instance.e3")
			ociClient.VNICByInstance["ocid1.instance.e1"] = "vnic-e1"
			ociClient.VNICByInstance["ocid1.instance.e2"] = "vnic-e2"
			ociClient.VNICByInstance["ocid1.instance.e3"] = "vnic-e3"

			// Create two PivotIPs already assigned to node-e1 and node-e2.
			pip1 := &pivotv1alpha1.PivotIP{
				ObjectMeta: metav1.ObjectMeta{Name: "pip-e1", Namespace: "default"},
				Spec:       pivotv1alpha1.PivotIPSpec{ServiceRef: corev1.LocalObjectReference{Name: "svc-e1"}},
				Status:     pivotv1alpha1.PivotIPStatus{AssignedNode: "node-e1"},
			}
			pip2 := &pivotv1alpha1.PivotIP{
				ObjectMeta: metav1.ObjectMeta{Name: "pip-e2", Namespace: "default"},
				Spec:       pivotv1alpha1.PivotIPSpec{ServiceRef: corev1.LocalObjectReference{Name: "svc-e2"}},
				Status:     pivotv1alpha1.PivotIPStatus{AssignedNode: "node-e2"},
			}
			makeService(ctx, "svc-e1")
			makeService(ctx, "svc-e2")
			makeService(ctx, "svc-e3")
			Expect(k8sClient.Create(ctx, pip1)).To(Succeed())
			pip1.Status.AssignedNode = "node-e1" // re-set: Create clears status in returned object
			Expect(k8sClient.Status().Update(ctx, pip1)).To(Succeed())
			Expect(k8sClient.Create(ctx, pip2)).To(Succeed())
			pip2.Status.AssignedNode = "node-e2"
			Expect(k8sClient.Status().Update(ctx, pip2)).To(Succeed())

			// New PivotIP with no preference — should land on node-e3 (fewest = 0).
			pip3 := makePivotIP(ctx, "pip-e3", "svc-e3")
			_, _ = reconcileNN(ctx, r, pip3.Name)
			_, err := reconcileNN(ctx, r, pip3.Name)
			Expect(err).NotTo(HaveOccurred())

			updated := &pivotv1alpha1.PivotIP{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: pip3.Name}, updated)).To(Succeed())
			Expect(updated.Status.AssignedNode).To(Equal("node-e3"))

			// cleanup
			for _, name := range []string{"pip-e1", "pip-e2", "pip-e3"} {
				p := &pivotv1alpha1.PivotIP{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: name}, p); err == nil {
					p.Finalizers = nil
					_ = k8sClient.Update(ctx, p)
					_ = k8sClient.Delete(ctx, p)
				}
			}
			for _, name := range []string{"node-e1", "node-e2", "node-e3"} {
				_ = k8sClient.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}})
			}
			for _, name := range []string{"svc-e1", "svc-e2", "svc-e3"} {
				_ = k8sClient.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}})
			}
		})
	})
})
