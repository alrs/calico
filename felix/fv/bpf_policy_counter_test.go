// Copyright (c) 2022 Tigera, Inc. All rights reserved.
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

//go:build fvtests

package fv_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"context"
	"fmt"

	api "github.com/projectcalico/api/pkg/apis/projectcalico/v3"
	log "github.com/sirupsen/logrus"

	"github.com/projectcalico/calico/felix/bpf"
	"github.com/projectcalico/calico/felix/bpf/counters"
	"github.com/projectcalico/calico/felix/fv/infrastructure"
	"github.com/projectcalico/calico/felix/fv/utils"
	"github.com/projectcalico/calico/felix/fv/workload"
	"github.com/projectcalico/calico/libcalico-go/lib/apiconfig"
	client "github.com/projectcalico/calico/libcalico-go/lib/clientv3"
	options2 "github.com/projectcalico/calico/libcalico-go/lib/options"
)

var _ = infrastructure.DatastoreDescribe("_BPF-SAFE_ Felix bpf test policy counters", []apiconfig.DatastoreType{apiconfig.EtcdV3}, func(getInfra infrastructure.InfraFactory) {

	if !BPFMode() {
		return
	}

	var (
		infra        infrastructure.DatastoreInfra
		felixes      []*infrastructure.Felix
		calicoClient client.Interface
		w            [2]*workload.Workload
	)

	BeforeEach(func() {
		infra = getInfra()
		opts := infrastructure.DefaultTopologyOptions()
		opts.ExtraEnvVars["FELIX_BPFPolicyDebugEnabled"] = "true"
		felixes, calicoClient = infrastructure.StartNNodeTopology(1, opts, infra)
		for i := 0; i < 2; i++ {
			wIP := fmt.Sprintf("10.65.0.%d", i+2)
			w[i] = workload.Run(felixes[0], fmt.Sprintf("w%d", i), "default", wIP, "8055", "tcp")
			w[i].WorkloadEndpoint.Labels = map[string]string{"name": w[i].Name}
			w[i].ConfigureInInfra(infra)
		}
		ensureBPFProgramsAttached(felixes[0])
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			infra.DumpErrorData()
		}

		for i := 0; i < 2; i++ {
			w[i].Stop()
		}
		felixes[0].Stop()
		infra.Stop()
	})

	createPolicy := func(policy *api.GlobalNetworkPolicy) *api.GlobalNetworkPolicy {
		log.WithField("policy", dumpResource(policy)).Info("Creating policy")
		policy, err := calicoClient.GlobalNetworkPolicies().Create(utils.Ctx, policy, utils.NoOptions)
		Expect(err).NotTo(HaveOccurred())
		return policy
	}

	updatePolicy := func(policy *api.GlobalNetworkPolicy) *api.GlobalNetworkPolicy {
		log.WithField("policy", dumpResource(policy)).Info("Updating policy")
		policy, err := calicoClient.GlobalNetworkPolicies().Update(utils.Ctx, policy, utils.NoOptions)
		Expect(err).NotTo(HaveOccurred())
		return policy
	}

	It("should update rule counters", func() {

		pol := api.NewGlobalNetworkPolicy()
		pol.Namespace = "fv"
		pol.Name = "policy-test"
		pol.Spec.Selector = "all()"
		pol.Spec.Ingress = []api.Rule{{Action: "Deny"}}
		pol.Spec.Egress = []api.Rule{{Action: "Deny"}}
		pol = createPolicy(pol)

		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[0].InterfaceName, "ingress", "default.policy-test", "deny", true)
		}, "2s", "200ms").Should(BeTrue())

		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[0].InterfaceName, "egress", "default.policy-test", "deny", true)
		}, "2s", "200ms").Should(BeTrue())

		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[1].InterfaceName, "ingress", "default.policy-test", "deny", true)
		}, "2s", "200ms").Should(BeTrue())

		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[1].InterfaceName, "egress", "default.policy-test", "deny", true)
		}, "2s", "200ms").Should(BeTrue())

		for i := 0; i < 10; i++ {
			_, err := w[1].RunCmd("pktgen", w[1].IP, w[0].IP, "udp", "--port-src", "8055", "--port-dst", "8055")
			Expect(err).NotTo(HaveOccurred())
		}
		m := dumpRuleCounterMap(felixes[0])
		Expect(len(m)).To(Equal(1))
		for _, v := range m {
			Expect(v).To(Equal(uint64(10)))
		}

		pol.Spec.Ingress = []api.Rule{{Action: "Allow"}}
		pol.Spec.Egress = []api.Rule{{Action: "Allow"}}

		pol = updatePolicy(pol)
		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[0].InterfaceName, "ingress", "default.policy-test", "allow", true)
		}, "2s", "200ms").Should(BeTrue())

		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[0].InterfaceName, "egress", "default.policy-test", "allow", true)
		}, "2s", "200ms").Should(BeTrue())

		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[1].InterfaceName, "ingress", "default.policy-test", "allow", true)
		}, "2s", "200ms").Should(BeTrue())

		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[1].InterfaceName, "egress", "default.policy-test", "allow", true)
		}, "2s", "200ms").Should(BeTrue())

		for i := 0; i < 10; i++ {
			_, err := w[1].RunCmd("pktgen", w[1].IP, w[0].IP, "udp", "--port-src", "8055", "--port-dst", "8055")
			Expect(err).NotTo(HaveOccurred())
		}

		Eventually(func() int {
			m = dumpRuleCounterMap(felixes[0])
			return len(m)
		}, "2s", "200ms").Should(Equal(2))
		for _, v := range m {
			Expect(v).To(Equal(uint64(1)))
		}

		_, err := calicoClient.GlobalNetworkPolicies().Delete(context.Background(), "policy-test", options2.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[0].InterfaceName, "ingress", "default.policy-test", "allow", true)
		}, "2s", "200ms").ShouldNot(BeTrue())

		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[0].InterfaceName, "egress", "default.policy-test", "allow", true)
		}, "2s", "200ms").ShouldNot(BeTrue())

		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[1].InterfaceName, "ingress", "default.policy-test", "allow", true)
		}, "2s", "200ms").ShouldNot(BeTrue())

		Eventually(func() bool {
			return bpfCheckIfPolicyProgrammed(felixes[0], w[1].InterfaceName, "egress", "default.policy-test", "allow", true)
		}, "2s", "200ms").ShouldNot(BeTrue())

		Eventually(func() int {
			m = dumpRuleCounterMap(felixes[0])
			return len(m)
		}, "5s", "200ms").Should(Equal(0))
	})
})

func dumpRuleCounterMap(felix *infrastructure.Felix) counters.PolicyMapMem {
	rcMap := counters.PolicyMap(&bpf.MapContext{})
	m := make(counters.PolicyMapMem)
	dumpBPFMap(felix, rcMap, counters.PolicyMapMemIter(m))
	return m
}
