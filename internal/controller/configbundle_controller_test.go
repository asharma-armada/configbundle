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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	armadav1 "github.com/armada/configbundle/api/v1"
)

var _ = Describe("ConfigBundle Controller", func() {
	const (
		timeout  = 10 * time.Second
		interval = 250 * time.Millisecond
	)

	ctx := context.Background()

	var (
		ns        string
		nsCounter int
	)

	BeforeEach(func() {
		nsCounter++
		ns = fmt.Sprintf("test-%d", nsCounter)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})).To(Succeed())
	})

	AfterEach(func() {
		Expect(k8sClient.Delete(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})).To(Succeed())
	})

	Describe("child CR decomposition", func() {
		It("creates a ServerConfig named by lowercase hostname", func() {
			cb := singleServerBundle("test-bundle", ns, "colo-r740-01", "3RK3V64", "10.10.1.45")
			Expect(k8sClient.Create(ctx, cb)).To(Succeed())

			sc := &armadav1.ServerConfig{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "colo-r740-01", Namespace: ns}, sc)
			}, timeout, interval).Should(Succeed())

			Expect(sc.Spec.ServiceTag).To(Equal("3RK3V64"))
			Expect(sc.Spec.Hostname).To(Equal("colo-r740-01"))
			Expect(sc.Spec.OobIP).To(Equal("10.10.1.45"))
		})

		It("propagates all idrac fields to the child CR", func() {
			cb := singleServerBundle("test-bundle", ns, "colo-r740-01", "3RK3V64", "10.10.1.45")
			cb.Spec.Servers[0].Idrac = armadav1.IdracSpec{
				FirmwareVersion:             "7.20.10.05",
				SSHEnabled:                  false,
				IPMIEnabled:                 false,
				LockdownModeEnabled:         false,
				OsToIdracPassThroughEnabled: false,
				UsbManagementPortEnabled:    true,
				DHCPEnabled:                 false,
				RacadmEnabled:               true,
			}
			Expect(k8sClient.Create(ctx, cb)).To(Succeed())

			sc := &armadav1.ServerConfig{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "colo-r740-01", Namespace: ns}, sc)
			}, timeout, interval).Should(Succeed())

			Expect(sc.Spec.Idrac.FirmwareVersion).To(Equal("7.20.10.05"))
			Expect(sc.Spec.Idrac.UsbManagementPortEnabled).To(BeTrue())
			Expect(sc.Spec.Idrac.RacadmEnabled).To(BeTrue())
			Expect(sc.Spec.Idrac.SSHEnabled).To(BeFalse())
			Expect(sc.Spec.Idrac.IPMIEnabled).To(BeFalse())
			Expect(sc.Spec.Idrac.DHCPEnabled).To(BeFalse())
			Expect(sc.Spec.Idrac.LockdownModeEnabled).To(BeFalse())
			Expect(sc.Spec.Idrac.OsToIdracPassThroughEnabled).To(BeFalse())
		})

		It("creates one ServerConfig per server in a multi-server bundle", func() {
			cb := &armadav1.ConfigBundle{
				ObjectMeta: metav1.ObjectMeta{Name: "multi-galleon", Namespace: ns},
				Spec: armadav1.ConfigBundleSpec{
					Datacenter: "colo",
					Servers: []armadav1.ServerSpec{
						{ServiceTag: "3RK3V64", Hostname: "colo-r740-01", OobIP: "10.10.1.45"},
						{ServiceTag: "FQK3V64", Hostname: "colo-r740-02", OobIP: "10.10.1.46"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cb)).To(Succeed())

			for _, hostname := range []string{"colo-r740-01", "colo-r740-02"} {
				sc := &armadav1.ServerConfig{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{Name: hostname, Namespace: ns}, sc)
				}, timeout, interval).Should(Succeed(), "expected ServerConfig %s to exist", hostname)
			}
		})
	})

	Describe("desired state enforcement", func() {
		It("restores a child CR field mutated out-of-band", func() {
			cb := singleServerBundle("test-bundle", ns, "colo-r740-01", "3RK3V64", "10.10.1.45")
			cb.Spec.Servers[0].Idrac.SSHEnabled = false
			Expect(k8sClient.Create(ctx, cb)).To(Succeed())

			// Wait for the child CR to be created.
			sc := &armadav1.ServerConfig{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "colo-r740-01", Namespace: ns}, sc)
			}, timeout, interval).Should(Succeed())

			// Simulate unauthorized drift: patch sshEnabled to true directly on the child.
			scPatched := sc.DeepCopy()
			scPatched.Spec.Idrac.SSHEnabled = true
			Expect(k8sClient.Patch(ctx, scPatched, client.MergeFrom(sc))).To(Succeed())

			// The controller (triggered by Owns watch) should restore sshEnabled to false.
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "colo-r740-01", Namespace: ns}, sc)).To(Succeed())
				g.Expect(sc.Spec.Idrac.SSHEnabled).To(BeFalse())
			}, timeout, interval).Should(Succeed())
		})

		It("propagates a ConfigBundle spec update to the child CR", func() {
			cb := singleServerBundle("test-bundle", ns, "colo-r740-01", "3RK3V64", "10.10.1.45")
			cb.Spec.Servers[0].Idrac.SSHEnabled = false
			Expect(k8sClient.Create(ctx, cb)).To(Succeed())

			// Wait for child CR.
			sc := &armadav1.ServerConfig{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "colo-r740-01", Namespace: ns}, sc)
			}, timeout, interval).Should(Succeed())

			// Update the ConfigBundle spec — desired state changes.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-bundle", Namespace: ns}, cb)).To(Succeed())
			cb.Spec.Servers[0].Idrac.SSHEnabled = true
			Expect(k8sClient.Update(ctx, cb)).To(Succeed())

			// Child CR must reflect the updated desired state.
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "colo-r740-01", Namespace: ns}, sc)).To(Succeed())
				g.Expect(sc.Spec.Idrac.SSHEnabled).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})
	})
})

// singleServerBundle returns a ConfigBundle with one server entry for use in tests.
func singleServerBundle(name, ns, hostname, serviceTag, oobIP string) *armadav1.ConfigBundle {
	return &armadav1.ConfigBundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: armadav1.ConfigBundleSpec{
			Datacenter: "colo",
			Servers: []armadav1.ServerSpec{
				{ServiceTag: serviceTag, Hostname: hostname, OobIP: oobIP},
			},
		},
	}
}
