//go:build e2e
// +build e2e

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

package e2e

import (
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/armada/configbundle/test/utils"
)

// cbNamespace is the namespace used for ConfigBundle e2e tests.
// Matches the namespace in config/samples/.
const cbNamespace = "configbundle-system"

var _ = Describe("ConfigBundle", Ordered, func() {
	SetDefaultEventuallyTimeout(30 * time.Second)
	SetDefaultEventuallyPollingInterval(time.Second)

	BeforeAll(func() {
		By("ensuring the configbundle-system namespace exists")
		// Ignore error: the namespace likely already exists when running locally.
		cmd := exec.Command("kubectl", "create", "namespace", cbNamespace)
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		By("cleaning up ConfigBundle resources after the test")
		cmd := exec.Command("kubectl", "delete", "configbundle", "--all",
			"-n", cbNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		// Wait for cascade delete to complete before the next test.
		Eventually(func() string {
			c := exec.Command("kubectl", "get", "serverconfig",
				"-n", cbNamespace, "--no-headers", "-o", "name")
			out, _ := utils.Run(c)
			return out
		}).Should(BeEmpty())
	})

	It("decomposes a ConfigBundle into child ServerConfig CRs", func() {
		By("applying the example ConfigBundle")
		cmd := exec.Command("kubectl", "apply", "-f", "config/samples/v1_configbundle.yaml")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("verifying the child ServerConfig CR is created with the correct name")
		Eventually(func() string {
			c := exec.Command("kubectl", "get", "serverconfig", "colo-r740-01",
				"-n", cbNamespace, "-o", "jsonpath={.spec.serviceTag}")
			out, _ := utils.Run(c)
			return out
		}).Should(Equal("3RK3V64"))
	})

	It("cascades deletion of child ServerConfig CRs when the ConfigBundle is deleted", func() {
		By("applying the example ConfigBundle")
		cmd := exec.Command("kubectl", "apply", "-f", "config/samples/v1_configbundle.yaml")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for child ServerConfig to exist")
		Eventually(func() error {
			c := exec.Command("kubectl", "get", "serverconfig", "colo-r740-01", "-n", cbNamespace)
			_, err := utils.Run(c)
			return err
		}).Should(Succeed())

		By("deleting the ConfigBundle")
		cmd = exec.Command("kubectl", "delete", "configbundle", "colo-galleon",
			"-n", cbNamespace, "--wait=true")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("verifying the child ServerConfig is garbage collected")
		Eventually(func() error {
			c := exec.Command("kubectl", "get", "serverconfig", "colo-r740-01", "-n", cbNamespace)
			_, err := utils.Run(c)
			return err
		}).ShouldNot(Succeed())
	})

	It("restores a child CR field mutated out-of-band", func() {
		By("applying the example ConfigBundle")
		cmd := exec.Command("kubectl", "apply", "-f", "config/samples/v1_configbundle.yaml")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for child ServerConfig to exist")
		Eventually(func() error {
			c := exec.Command("kubectl", "get", "serverconfig", "colo-r740-01", "-n", cbNamespace)
			_, err := utils.Run(c)
			return err
		}).Should(Succeed())

		By("mutating sshEnabled on the child CR directly")
		cmd = exec.Command("kubectl", "patch", "serverconfig", "colo-r740-01",
			"-n", cbNamespace, "--type=merge",
			"-p", `{"spec":{"idrac":{"sshEnabled":true}}}`)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("verifying the controller restores sshEnabled to false")
		Eventually(func() string {
			c := exec.Command("kubectl", "get", "serverconfig", "colo-r740-01",
				"-n", cbNamespace, "-o", "jsonpath={.spec.idrac.sshEnabled}")
			out, _ := utils.Run(c)
			return out
		}).Should(Equal("false"))
	})
})
