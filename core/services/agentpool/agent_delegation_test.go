// SPDX-License-Identifier: MIT
package agentpool

import (
	"github.com/mudler/LocalAGI/core/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Interactive delegation scope", func() {
	DescribeTable("resolves only peers in the parent's namespace",
		func(parent, candidate, expected string, allowed bool) {
			job := types.NewJob(types.WithMetadata(map[string]any{"user_id": "bob"}))
			name, ok := resolvePoolSubAgent(parent, job, candidate)
			Expect(ok).To(Equal(allowed))
			Expect(name).To(Equal(expected))
		},
		Entry("same owner gets public alias", "alice:parent", "alice:coder", "coder", true),
		Entry("different owner is denied despite job metadata", "alice:parent", "bob:coder", "", false),
		Entry("scoped parent cannot use global peer", "alice:parent", "coder", "", false),
		Entry("global parent cannot use scoped peer", "parent", "alice:coder", "", false),
		Entry("global peers", "parent", "coder", "coder", true),
		Entry("colon in owner", "legacy-api-key:alice:parent", "legacy-api-key:alice:coder", "coder", true),
		Entry("different colon owners", "legacy-api-key:alice:parent", "legacy-api-key:bob:coder", "", false),
		Entry("nested owner does not match prefix", "alice:parent", "alice:bob:coder", "", false),
	)
})
