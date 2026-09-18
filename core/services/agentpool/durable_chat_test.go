// SPDX-License-Identifier: MIT
package agentpool

import (
	"errors"
	"sync"
	"time"

	"github.com/mudler/LocalAGI/core/sse"
	"github.com/mudler/LocalAGI/core/state"
	"github.com/mudler/LocalAGI/core/types"
	"github.com/mudler/LocalAI/core/services/jobs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type failingChatStore struct {
	jobs.ChatStore
	createErr, updateErr error
}

func (s failingChatStore) Create(j jobs.ChatJob) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.ChatStore.Create(j)
}
func (s failingChatStore) Update(j jobs.ChatJob) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	return s.ChatStore.Update(j)
}

type durableWorker struct {
	*chatTestAgent
	ask func(*types.Job) *types.JobResult
}

func (w durableWorker) Ask(opts ...types.JobOption) *types.JobResult {
	return w.ask(types.NewJob(opts...))
}

var _ = Describe("Durable chat lifecycle", func() {
	var store jobs.ChatStore
	var directory string
	var svc *AgentPoolService
	var worker durableWorker
	BeforeEach(func() {
		var err error
		directory = GinkgoT().TempDir()
		store, err = jobs.NewChatStore(nil, directory, 24*time.Hour)
		Expect(err).NotTo(HaveOccurred())
		svc = &AgentPoolService{chatStore: store, localAGI: localAGICore{pool: &state.AgentPool{}}}
		worker = durableWorker{chatTestAgent: &chatTestAgent{state: types.NewAgentSharedState(time.Minute)}}
	})
	It("persists before execution and acknowledgement and saves before terminal publication", func() {
		release := make(chan struct{})
		entered := make(chan struct{})
		done := make(chan struct{})
		worker.ask = func(j *types.Job) *types.JobResult {
			defer GinkgoRecover()
			saved, err := store.Get("alice", "coder", "root")
			Expect(err).NotTo(HaveOccurred())
			Expect(saved.MessageID).To(Equal("root"))
			close(entered)
			<-release
			j.EventCallback("sub_agent", map[string]any{"agent_id": "child", "status": "completed", "message_id": "root"})
			j.EventCallback("json_message_status", map[string]any{"agent_id": "child", "status": "completed", "message_id": "root"})
			childSnapshot, childErr := store.Get("alice", "coder", "root")
			Expect(childErr).NotTo(HaveOccurred())
			Expect(childSnapshot.CompletedAt).To(BeNil())
			return &types.JobResult{Response: "final report"}
		}
		receipt, err := svc.startDurableChat(worker, "alice:coder", "task", "conv", "root", func(e sse.Envelope) {
			defer GinkgoRecover()
			if event := e.(*sse.Message); event.Event == "json_message_status" && event.Data != "" {
				saved, lookupErr := store.Get("alice", "coder", "root")
				Expect(lookupErr).NotTo(HaveOccurred())
				if saved.Status == "completed" {
					select {
					case <-done:
					default:
						close(done)
					}
				}
			}
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(receipt.JobID).To(Equal("root"))
		Expect(receipt.MessageID).To(Equal("root"))
		Expect(store.Get("alice", "coder", "root")).Error().NotTo(HaveOccurred())
		Eventually(entered).Should(BeClosed())
		close(release)
		Eventually(done).Should(BeClosed())
		saved, err := store.Get("alice", "coder", "root")
		Expect(err).NotTo(HaveOccurred())
		Expect(*saved.Result).To(Equal("final report"))
	})
	It("does not execute or acknowledge a durable root if creation fails", func() {
		svc.chatStore = failingChatStore{ChatStore: store, createErr: errors.New("secret backend failure")}
		var called bool
		worker.ask = func(*types.Job) *types.JobResult { called = true; return nil }
		receipt, err := svc.startDurableChat(worker, "alice:coder", "task", "", "root", func(sse.Envelope) {}, nil)
		Expect(err).To(MatchError(ErrJobPersistence))
		Expect(receipt.JobID).To(BeEmpty())
		Expect(called).To(BeFalse())
	})
	It("does not create a job for a second-check live-loop injection", func() {
		worker.injected = true
		worker.ask = func(*types.Job) *types.JobResult { Fail("injected message executed"); return nil }
		receipt, err := svc.startDurableChat(worker, "alice:coder", "followup", "conv", "followup", func(sse.Envelope) {}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(receipt.JobID).To(BeEmpty())
		_, err = store.Get("alice", "coder", "followup")
		Expect(err).To(MatchError(jobs.ErrChatJobNotFound))
	})
	It("stores a safe failure without subscribers or raw exception secrets", func() {
		worker.ask = func(*types.Job) *types.JobResult { return &types.JobResult{Error: errors.New("token=secret")} }
		_, err := svc.startDurableChat(worker, "alice:coder", "task", "conv", "root", func(sse.Envelope) {}, nil)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string { j, _ := store.Get("alice", "coder", "root"); return j.Status }).Should(Equal("failed"))
		j, _ := store.Get("alice", "coder", "root")
		Expect(j.Error.Code).To(Equal("execution_failed"))
		Expect(j.Error.Message).NotTo(ContainSubstring("secret"))
	})
	It("reports failed terminal persistence without publishing ordinary completion", func() {
		svc.chatStore = failingChatStore{ChatStore: store, updateErr: errors.New("disk offline")}
		worker.ask = func(*types.Job) *types.JobResult { return &types.JobResult{Response: "report"} }
		var mu sync.Mutex
		var events []string
		_, err := svc.startDurableChat(worker, "alice:coder", "task", "conv", "root", func(e sse.Envelope) { mu.Lock(); defer mu.Unlock(); events = append(events, e.(*sse.Message).Data) }, nil)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool { _, ok := svc.chatPersistenceFailures.Load("root"); return ok }).Should(BeTrue())
		Eventually(func() string {
			mu.Lock()
			defer mu.Unlock()
			if len(events) > 0 {
				return events[len(events)-1]
			}
			return ""
		}).Should(ContainSubstring("persistence_unavailable"))
		mu.Lock()
		snapshot := append([]string(nil), events...)
		mu.Unlock()
		for _, e := range snapshot {
			Expect(e).NotTo(ContainSubstring(`"status":"completed"`))
		}
	})
	It("retains the decorated terminal result across service reconstruction and scopes public aliases", func() {
		worker.ask = func(*types.Job) *types.JobResult { return &types.JobResult{Response: "report"} }
		_, err := svc.startDurableChat(worker, "legacy-api-key:alice:coder", "task", "conv", "root", func(sse.Envelope) {}, func(r *types.JobResult, _ time.Duration) map[string]any {
			r.Response += " [citation]"
			return map[string]any{"secret-tool-payload": "not stored"}
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string { j, _ := store.Get("legacy-api-key:alice", "coder", "root"); return j.Status }).Should(Equal("completed"))
		reopened, err := jobs.NewChatStore(nil, directory, 24*time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(reopened.Reconcile()).To(Succeed())
		restarted := &AgentPoolService{chatStore: reopened, localAGI: localAGICore{pool: &state.AgentPool{}}}
		saved, err := restarted.GetChatJobForUser("legacy-api-key:alice", "coder", "root")
		Expect(err).NotTo(HaveOccurred())
		Expect(*saved.Result).To(Equal("report [citation]"))
		_, err = restarted.GetChatJobForUser("bob", "coder", "root")
		Expect(err).To(MatchError(jobs.ErrChatJobNotFound))
		_, err = restarted.GetChatJobForUser("legacy-api-key:alice", "legacy-api-key:alice:coder", "root")
		Expect(err).To(MatchError(jobs.ErrChatJobNotFound))
	})
	It("reports unknown interrupted outcome after reconstruction following a terminal write failure", func() {
		svc.chatStore = failingChatStore{ChatStore: store, updateErr: errors.New("disk unavailable")}
		worker.ask = func(*types.Job) *types.JobResult { return &types.JobResult{Response: "side effects happened"} }
		done := make(chan struct{})
		var once sync.Once
		_, err := svc.startDurableChat(worker, "alice:coder", "task", "conv", "root", func(e sse.Envelope) {
			if e.(*sse.Message).Event == "json_error" {
				once.Do(func() { close(done) })
			}
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		Eventually(done).Should(BeClosed())
		_, err = svc.GetChatJobForUser("alice", "coder", "root")
		Expect(err).To(MatchError(ErrJobPersistence))
		_, err = svc.GetChatJobForUser("bob", "coder", "root")
		Expect(err).To(MatchError(jobs.ErrChatJobNotFound))
		reopened, err := jobs.NewChatStore(nil, directory, 24*time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(reopened.Reconcile()).To(Succeed())
		saved, err := reopened.Get("alice", "coder", "root")
		Expect(err).NotTo(HaveOccurred())
		Expect(saved.Status).To(Equal("interrupted"))
		Expect(saved.Result).To(BeNil())
	})
	DescribeTable("records unsuccessful worker termination safely", func(panicWorker bool) {
		worker.ask = func(*types.Job) *types.JobResult {
			if panicWorker {
				panic("secret")
			}
			return nil
		}
		_, err := svc.startDurableChat(worker, "alice:coder", "task", "conv", "root", func(sse.Envelope) {}, nil)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string { j, _ := store.Get("alice", "coder", "root"); return j.Status }).Should(Equal("failed"))
	}, Entry("nil response", false), Entry("panic", true))

	It("persists a delegated question arriving after the root parked without treating child completion as terminal", func() {
		worker.ask = func(j *types.Job) *types.JobResult {
			defer GinkgoRecover()
			j.EventCallback("json_message_status", map[string]any{"message_id": "root", "status": "waiting_agents"})
			j.EventCallback("json_message_status", map[string]any{"message_id": "root", "agent_id": "child", "status": "waiting_user"})
			saved, err := store.Get("alice", "coder", "root")
			Expect(err).NotTo(HaveOccurred())
			Expect(saved.Status).To(Equal("waiting_user"))
			j.EventCallback("json_message_status", map[string]any{"message_id": "root", "agent_id": "child", "status": "completed"})
			saved, err = store.Get("alice", "coder", "root")
			Expect(err).NotTo(HaveOccurred())
			Expect(saved.Status).To(Equal("waiting_user"))
			Expect(saved.CompletedAt).To(BeNil())
			j.EventCallback("json_message_status", map[string]any{"message_id": "root", "agent_id": "child", "status": "processing"})
			saved, err = store.Get("alice", "coder", "root")
			Expect(err).NotTo(HaveOccurred())
			Expect(saved.Status).To(Equal("running"))
			return &types.JobResult{Response: "done"}
		}
		_, err := svc.startDurableChat(worker, "alice:coder", "task", "conv", "root", func(sse.Envelope) {}, nil)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string { j, _ := store.Get("alice", "coder", "root"); return j.Status }).Should(Equal("completed"))
	})

})
