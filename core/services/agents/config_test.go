// SPDX-License-Identifier: MIT
package agents

import (
	"encoding/json"

	"github.com/mudler/LocalAGI/core/state"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentConfig interactive settings", func() {
	It("preserves interaction and delegation settings across the LocalAGI JSON boundary", func() {
		upstream := state.AgentConfig{
			EnableUserQuestions: true,
			RequirePlanApproval: true,
			EnableSubAgents:     true,
			SubAgents:           []string{"researcher", "reviewer"},
			RemoteAgents: []state.RemoteAgent{{
				Name:        "remote-reviewer",
				Description: "Reviews a proposed implementation",
				URL:         "https://agents.example.test",
				APIKey:      "remote-secret",
			}},
		}

		serialized, err := json.Marshal(upstream)
		Expect(err).NotTo(HaveOccurred())

		var native AgentConfig
		Expect(json.Unmarshal(serialized, &native)).To(Succeed())
		Expect(native.EnableUserQuestions).To(BeTrue())
		Expect(native.RequirePlanApproval).To(BeTrue())
		Expect(native.EnableSubAgents).To(BeTrue())
		Expect(native.SubAgents).To(Equal([]string{"researcher", "reviewer"}))
		Expect(native.RemoteAgents).To(Equal([]RemoteAgent{{
			Name:        "remote-reviewer",
			Description: "Reviews a proposed implementation",
			URL:         "https://agents.example.test",
			APIKey:      "remote-secret",
		}}))

		serialized, err = json.Marshal(native)
		Expect(err).NotTo(HaveOccurred())

		var restored state.AgentConfig
		Expect(json.Unmarshal(serialized, &restored)).To(Succeed())
		Expect(restored.EnableUserQuestions).To(BeTrue())
		Expect(restored.RequirePlanApproval).To(BeTrue())
		Expect(restored.EnableSubAgents).To(BeTrue())
		Expect(restored.SubAgents).To(Equal(upstream.SubAgents))
		Expect(restored.RemoteAgents).To(Equal(upstream.RemoteAgents))
	})

	It("describes opt-in switches and discoverable structured delegation settings", func() {
		fields := map[string]ConfigField{}
		for _, field := range DefaultConfigMeta().Fields {
			fields[field.Name] = field
		}

		for _, name := range []string{"enable_user_questions", "require_plan_approval", "enable_sub_agents"} {
			field, found := fields[name]
			Expect(found).To(BeTrue(), "missing config metadata for %s", name)
			Expect(field.Type).To(Equal(FieldCheckbox))
			Expect(field.DefaultValue).To(Equal(false))
		}
		Expect(fields["require_plan_approval"].Tags.DependsOn).To(Equal("enable_planning"))
		Expect(fields["sub_agents"].Type).To(Equal(FieldTextarea))
		Expect(fields["sub_agents"].DefaultValue).To(Equal([]string{}))
		Expect(fields["remote_agents"].Type).To(Equal(FieldTextarea))
		Expect(fields["remote_agents"].DefaultValue).To(Equal([]RemoteAgent{}))
	})
})
