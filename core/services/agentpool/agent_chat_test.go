// SPDX-License-Identifier: MIT
package agentpool

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/mudler/LocalAGI/core/sse"
	"github.com/mudler/LocalAGI/core/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

type chatTestAgent struct {
	state    *types.AgentSharedState
	jobs     []*types.Job
	result   *types.JobResult
	injected bool
}

func (a *chatTestAgent) SharedState() *types.AgentSharedState    { return a.state }
func (a *chatTestAgent) InjectChat(_, _, _ string) (bool, error) { return a.injected, nil }
func (a *chatTestAgent) Ask(opts ...types.JobOption) *types.JobResult {
	job := types.NewJob(opts...)
	a.jobs = append(a.jobs, job)
	if a.result != nil {
		return a.result
	}
	return &types.JobResult{Response: "done", Conversation: append(job.ConversationHistory, openai.ChatCompletionMessage{Role: "assistant", Content: "done"})}
}

var _ = Describe("Interactive chat lifecycle", func() {
	var ag *chatTestAgent
	var events []*sse.Message
	var send func(sse.Envelope)
	BeforeEach(func() {
		ag = &chatTestAgent{state: types.NewAgentSharedState(time.Minute)}
		events = nil
		send = func(e sse.Envelope) { events = append(events, e.(*sse.Message)) }
	})

	It("reuses and saves history only for the requested conversation", func() {
		runAgentChat(ag, "first", "one", "m1", send, nil)
		runAgentChat(ag, "second", "one", "m2", send, nil)
		runAgentChat(ag, "other", "two", "m3", send, nil)
		Expect(ag.jobs[1].ConversationHistory).To(HaveLen(3))
		Expect(ag.jobs[1].ConversationHistory[0].Content).To(Equal("first"))
		Expect(ag.jobs[1].Metadata[types.MetadataKeyConversationID]).To(Equal("one"))
		Expect(ag.jobs[1].UUID).To(Equal("m2"))
		Expect(ag.jobs[2].ConversationHistory).To(HaveLen(1))
		Expect(ag.state.ConversationTracker.GetConversation("one")).To(HaveLen(4))
	})

	It("keeps legacy messages stateless", func() {
		runAgentChat(ag, "first", "", "m1", send, nil)
		runAgentChat(ag, "second", "", "m2", send, nil)
		Expect(ag.jobs[1].ConversationHistory).To(HaveLen(1))
		Expect(ag.state.ConversationTracker.GetConversation("")).To(BeEmpty())
	})

	It("correlates events and preserves response decoration and metadata", func() {
		runAgentChat(ag, "first", "one", "m1", send, func(r *types.JobResult, _ time.Duration) map[string]any {
			r.Response += " [citation]"
			return map[string]any{"files": []string{"artifact.txt"}}
		})
		for _, event := range events {
			var data map[string]any
			Expect(json.Unmarshal([]byte(event.Data), &data)).To(Succeed())
			Expect(data["conversation_id"]).To(Equal("one"))
			Expect(data["message_id"]).To(Equal("m1"))
			if data["id"] == "m1-agent" {
				Expect(data["content"]).To(Equal("done [citation]"))
				Expect(data["metadata"]).To(HaveKey("files"))
			}
		}
		Expect(events).To(HaveLen(4))
	})

	It("retries live-loop admission without starting or completing a second job", func() {
		ag.injected = true
		runAgentChat(ag, "followup", "one", "m1", send, nil)
		Expect(ag.jobs).To(BeEmpty())
		Expect(events).To(BeEmpty())
	})

	It("does not overwrite history already saved by the live loop", func() {
		ag.state.ConversationTracker.SetConversation("one", []openai.ChatCompletionMessage{{Role: "user", Content: "newer"}})
		ag.result = &types.JobResult{ConversationSaved: true, Response: "done"}
		runAgentChat(ag, "first", "one", "m1", send, nil)
		Expect(ag.state.ConversationTracker.GetConversation("one")[0].Content).To(Equal("newer"))
	})

	It("completes failed jobs with a correlated error", func() {
		ag.result = &types.JobResult{Error: errors.New("failed")}
		runAgentChat(ag, "first", "one", "m1", send, nil)
		Expect(events[2].Event).To(Equal("json_error"))
		Expect(events[2].Data).To(ContainSubstring(`"conversation_id":"one"`))
		Expect(events[3].Data).To(ContainSubstring(`"status":"completed"`))
		Expect(ag.state.ConversationTracker.GetConversation("one")).To(BeEmpty())
	})
})
