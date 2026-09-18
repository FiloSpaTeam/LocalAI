// SPDX-License-Identifier: MIT
//go:build auth

package jobs_test

import (
	"time"

	"github.com/mudler/LocalAI/core/http/auth"
	"github.com/mudler/LocalAI/core/services/jobs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Durable chat store SQLite", func() {
	It("reconstructs scoped results in the auth database and prevents terminal rewrites", func() {
		db, err := auth.InitDB(":memory:")
		Expect(err).NotTo(HaveOccurred())
		store, err := jobs.NewChatStore(db, "", time.Hour)
		Expect(err).NotTo(HaveOccurred())
		job := jobs.ChatJob{ID: "root", Owner: "alice", Agent: "assistant", MessageID: "root", Status: "accepted"}
		Expect(store.Create(job)).To(Succeed())
		job.Owner = "bob"
		Expect(store.Update(job)).To(MatchError(jobs.ErrChatJobNotFound))
		job.Owner = "alice"
		result := "answer"
		job.Status = "completed"
		job.Result = &result
		Expect(store.Update(job)).To(Succeed())
		again, err := jobs.NewChatStore(db, "", time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Reconcile()).To(Succeed())
		got, err := again.Get("alice", "assistant", "root")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Status).To(Equal("completed"))
		Expect(*got.Result).To(Equal("answer"))
		_, err = again.Get("bob", "assistant", "root")
		Expect(err).To(MatchError(jobs.ErrChatJobNotFound))
		_, err = again.Get("alice", "other", "root")
		Expect(err).To(MatchError(jobs.ErrChatJobNotFound))
		job.Status = "running"
		Expect(again.Update(job)).NotTo(Succeed())
	})
	It("reconciles unfinished records and removes expired response content", func() {
		db, err := auth.InitDB(":memory:")
		Expect(err).NotTo(HaveOccurred())
		store, err := jobs.NewChatStore(db, "", time.Hour)
		Expect(err).NotTo(HaveOccurred())
		for _, status := range []string{"accepted", "running", "waiting_user", "waiting_agents"} {
			Expect(store.Create(jobs.ChatJob{ID: status, Owner: "alice", Agent: "assistant", Status: status})).To(Succeed())
		}
		Expect(store.Reconcile()).To(Succeed())
		for _, id := range []string{"accepted", "running", "waiting_user", "waiting_agents"} {
			got, err := store.Get("alice", "assistant", id)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Status).To(Equal("interrupted"))
			Expect(got.Error.Code).To(Equal("execution_interrupted"))
		}
		completed := time.Now().Add(-90 * time.Minute)
		result := "private response"
		Expect(store.Create(jobs.ChatJob{ID: "expired", Owner: "alice", Agent: "assistant", Status: "completed", CompletedAt: &completed, Result: &result})).To(Succeed())
		Expect(store.Cleanup()).To(Succeed())
		_, err = store.Get("alice", "assistant", "expired")
		Expect(err).To(MatchError(jobs.ErrChatJobExpired))
		var record jobs.ChatJob
		Expect(db.First(&record, "id = ?", "expired").Error).To(Succeed())
		Expect(record.Result).To(BeNil())
		completed = time.Now().Add(-3 * time.Hour)
		Expect(store.Create(jobs.ChatJob{ID: "forgotten", Owner: "alice", Agent: "assistant", Status: "failed", CompletedAt: &completed})).To(Succeed())
		Expect(store.Cleanup()).To(Succeed())
		_, err = store.Get("alice", "assistant", "forgotten")
		Expect(err).To(MatchError(jobs.ErrChatJobNotFound))
	})
})
