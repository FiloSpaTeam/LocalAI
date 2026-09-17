// SPDX-License-Identifier: MIT
package agentpool

import (
	"encoding/json"

	"github.com/mudler/LocalAGI/core/state"
	agiConfig "github.com/mudler/LocalAGI/pkg/config"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Interactive embedded configuration metadata", func() {
	It("exposes structured delegation fields without duplicating future upstream fields", func() {
		upstream := state.AgentConfigMeta{Fields: []agiConfig.Field{{Name: "remote_agents", Label: "Upstream remote agents"}}}
		backend := newLocalAgentConfigBackend(&AgentPoolService{localAGI: localAGICore{configMeta: upstream}})
		encoded, err := json.Marshal(backend.GetConfigMeta())
		Expect(err).NotTo(HaveOccurred())
		var result struct {
			Fields []struct {
				Name         string `json:"name"`
				Label        string `json:"label"`
				DefaultValue any    `json:"defaultValue"`
			}
		}
		Expect(json.Unmarshal(encoded, &result)).To(Succeed())
		Expect(result.Fields).To(HaveLen(2))
		Expect(result.Fields[0].Name).To(Equal("remote_agents"))
		Expect(result.Fields[0].Label).To(Equal("Upstream remote agents"))
		Expect(result.Fields[1].Name).To(Equal("sub_agents"))
		Expect(result.Fields[1].DefaultValue).To(Equal([]any{}))
		Expect(upstream.Fields).To(HaveLen(1))
	})
})
