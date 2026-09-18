// SPDX-License-Identifier: MIT
package agentpool

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mudler/LocalAGI/core/agent"
	"github.com/mudler/LocalAGI/core/state"
	"github.com/mudler/LocalAGI/core/types"
	"github.com/mudler/cogito"
	"github.com/mudler/cogito/structures"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

type chatErrorBackend struct {
	AgentConfigBackend
	err error
}

func (b chatErrorBackend) Chat(_, _, _ string) (string, error) { return "", b.err }

var _ = Describe("Interactive service user scope", func() {
	var svc *AgentPoolService
	BeforeEach(func() {
		pool, err := state.NewAgentPool("", "", "", "", "", "", "", GinkgoT().TempDir(),
			func(*state.AgentConfig) func(context.Context, *state.AgentPool) []types.Action {
				return func(context.Context, *state.AgentPool) []types.Action { return nil }
			},
			func(*state.AgentConfig) []state.Connector { return nil },
			func(*state.AgentConfig) func(context.Context, *state.AgentPool) []agent.DynamicPrompt {
				return func(context.Context, *state.AgentPool) []agent.DynamicPrompt { return nil }
			},
			func(*state.AgentConfig) types.JobFilters { return nil }, "10m", false, nil, state.PoolLimits{})
		Expect(err).NotTo(HaveOccurred())
		svc = &AgentPoolService{localAGI: localAGICore{pool: pool}}
		for _, key := range []string{"alice:helper", "bob:helper"} {
			Expect(pool.CreateAgent(key, &state.AgentConfig{Name: key, StandaloneJob: true, PeriodicRuns: "24h"})).To(Succeed())
		}
		DeferCleanup(pool.StopAll)
	})

	It("looks up pending questions and accepts answers only on the owner's agent", func() {
		ag := svc.localAGI.pool.GetAgent("alice:helper")
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		done := make(chan error, 1)
		job := types.NewJob(types.WithUUID("job"), types.WithMetadata(map[string]any{types.MetadataKeyConversationID: "conversation"}))
		go func() {
			_, err := ag.Interactions().HandleQuestion(job, ctx, cogito.UserQuestion{ID: "question", Question: "Continue?", Options: []string{"yes"}, AskedAt: time.Now()})
			done <- err
		}()
		Eventually(func() int {
			snapshot, err := svc.PendingForUser("alice", "helper", "conversation")
			Expect(err).NotTo(HaveOccurred())
			return len(snapshot.Questions)
		}).Should(Equal(1))
		snapshot, err := svc.PendingForUser("bob", "helper", "conversation")
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot.Questions).To(BeEmpty())
		Expect(svc.AnswerForUser("bob", "helper", "question", cogito.UserAnswer{Selected: []string{"yes"}})).To(MatchError(cogito.ErrQuestionNotFound))
		_, err = svc.chatInConversation("alice:helper", "yes", "conversation")
		Expect(err).To(BeAssignableToTypeOf(&PendingQuestionError{}))
		Expect(err.(*PendingQuestionError).QuestionID).To(Equal("question"))
		Expect(svc.AnswerForUser("alice", "helper", "question", cogito.UserAnswer{Selected: []string{"yes"}})).To(Succeed())
		Eventually(done).Should(Receive(BeNil()))
		snapshot, err = svc.PendingForUser("alice", "helper", "conversation")
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot.Questions).To(BeEmpty())
	})

	It("routes a free-text message to the pending question without enqueueing a chat", func() {
		ag := svc.localAGI.pool.GetAgent("alice:helper")
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		done := make(chan cogito.UserAnswer, 1)
		job := types.NewJob(types.WithMetadata(map[string]any{types.MetadataKeyConversationID: "conversation"}))
		go func() {
			answer, _ := ag.Interactions().HandleQuestion(job, ctx, cogito.UserQuestion{ID: "question", Question: "Details?", AllowFreeText: true, AskedAt: time.Now()})
			done <- answer
		}()
		Eventually(func() int { return len(ag.Interactions().Pending("conversation").Questions) }).Should(Equal(1))
		receipt, err := svc.ChatInConversationForUser("alice", "helper", "print commands", "conversation")
		Expect(err).NotTo(HaveOccurred())
		Expect(receipt.Status).To(Equal("answer_received"))
		Expect(receipt.JobID).To(BeEmpty())
		Expect(receipt.QuestionID).To(Equal("question"))
		Expect(receipt.MessageID).To(BeEmpty())
		Eventually(done).Should(Receive(Equal(cogito.UserAnswer{Text: "print commands"})))
	})

	It("scopes plan decisions to the agent owner and preserves edited subtasks", func() {
		ag := svc.localAGI.pool.GetAgent("alice:helper")
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		done := make(chan cogito.PlanDecision, 1)
		job := types.NewJob(types.WithMetadata(map[string]any{types.MetadataKeyConversationID: "conversation"}))
		go func() {
			done <- ag.Interactions().ApprovePlan(job, ctx, &structures.Plan{Description: "Implement", Subtasks: []string{"read", "edit", "test"}}, nil)
		}()
		Eventually(func() bool { return ag.Interactions().Pending("conversation").Plan != nil }).Should(BeTrue())
		planID := ag.Interactions().Pending("conversation").Plan.ID
		Expect(svc.DecidePlanForUser("bob", "helper", planID, true, nil, "")).NotTo(Succeed())
		edited := []string{"edit", "test"}
		Expect(svc.DecidePlanForUser("alice", "helper", planID, true, &edited, "")).To(Succeed())
		var decision cogito.PlanDecision
		Eventually(done).Should(Receive(&decision))
		Expect(decision.Approved).To(BeTrue())
		Expect(decision.Plan.Subtasks).To(Equal(edited))
		Expect(ag.Interactions().Pending("conversation").Plan).To(BeNil())
	})

	It("distinguishes a missing agent from unsupported distributed interactions", func() {
		_, err := svc.PendingForUser("carol", "helper", "conversation")
		Expect(errors.Is(err, ErrAgentNotFound)).To(BeTrue())
		svc.localAGI.pool = nil
		_, err = svc.PendingForUser("alice", "helper", "conversation")
		Expect(err).To(MatchError(ErrInteractiveUnsupported))
	})

	It("keeps distributed missing agents distinct from database failures", func() {
		svc.localAGI.pool = nil
		svc.configBackend = chatErrorBackend{err: fmt.Errorf("agent config not found: %w", gorm.ErrRecordNotFound)}
		_, err := svc.ChatInConversationForUser("alice", "missing", "hello", "conversation")
		Expect(errors.Is(err, ErrAgentNotFound)).To(BeTrue())
		failure := fmt.Errorf("database offline")
		svc.configBackend = chatErrorBackend{err: failure}
		_, err = svc.ChatInConversationForUser("alice", "helper", "hello", "conversation")
		Expect(err).To(BeIdenticalTo(failure))
	})
})
