// SPDX-License-Identifier: MIT
package jobs

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"os"
	"path/filepath"
	"time"
)

var _ = Describe("Durable chat store", func() {
	var directory string
	var store ChatStore
	var now time.Time
	var job ChatJob
	BeforeEach(func() {
		directory = GinkgoT().TempDir()
		now = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
		var err error
		store, err = NewChatStore(nil, directory, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		store.(*chatStore).now = func() time.Time { return now }
		job = ChatJob{ID: "root", Owner: "alice", Agent: "assistant", MessageID: "root", ConversationID: "conversation", Status: "accepted"}
	})
	It("reconstructs private owner-scoped records and rejects identity changes", func() {
		Expect(store.Create(job)).To(Succeed())
		again, err := NewChatStore(nil, directory, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		got, err := again.Get("alice", "assistant", "root")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.ConversationID).To(Equal("conversation"))
		_, err = again.Get("bob", "assistant", "root")
		Expect(err).To(MatchError(ErrChatJobNotFound))
		_, err = again.Get("alice", "other", "root")
		Expect(err).To(MatchError(ErrChatJobNotFound))
		job.Owner = "bob"
		Expect(store.Update(job)).To(MatchError(ErrChatJobNotFound))
		info, err := os.Stat(filepath.Join(directory, "chat-jobs.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0600)))
	})
	It("expires terminal results on lookup and retains scoped tombstones before deleting", func() {
		Expect(store.Create(job)).To(Succeed())
		job.Status = "completed"
		result := "private answer"
		job.Result = &result
		Expect(store.Update(job)).To(Succeed())
		now = now.Add(time.Hour)
		_, err := store.Get("alice", "assistant", "root")
		Expect(err).To(MatchError(ErrChatJobExpired))
		_, err = store.Get("bob", "assistant", "root")
		Expect(err).To(MatchError(ErrChatJobNotFound))
		Expect(store.Cleanup()).To(Succeed())
		raw, err := os.ReadFile(filepath.Join(directory, "chat-jobs.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).NotTo(ContainSubstring("private answer"))
		now = now.Add(time.Hour)
		_, err = store.Get("alice", "assistant", "root")
		Expect(err).To(MatchError(ErrChatJobNotFound))
		Expect(store.Cleanup()).To(Succeed())
	})
	It("never ages active work out and reconciles unfinished work without touching completed results", func() {
		Expect(store.Create(job)).To(Succeed())
		now = now.Add(24 * time.Hour)
		Expect(store.Cleanup()).To(Succeed())
		_, err := store.Get("alice", "assistant", "root")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Reconcile()).To(Succeed())
		got, err := store.Get("alice", "assistant", "root")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Status).To(Equal("interrupted"))
		Expect(got.Error.Code).To(Equal("execution_interrupted"))
		Expect(*got.CompletedAt).To(Equal(now))
		job.ID = "finished"
		job.MessageID = "finished"
		job.Status = "completed"
		result := "answer"
		job.Result = &result
		Expect(store.Create(job)).To(Succeed())
		Expect(store.Reconcile()).To(Succeed())
		got, err = store.Get("alice", "assistant", "finished")
		Expect(err).NotTo(HaveOccurred())
		Expect(*got.Result).To(Equal("answer"))
	})
	It("refuses corrupt storage both at startup and before writes", func() {
		path := filepath.Join(directory, "chat-jobs.json")
		Expect(os.WriteFile(path, []byte("broken"), 0600)).To(Succeed())
		Expect(store.Create(job)).NotTo(Succeed())
		_, err := NewChatStore(nil, directory, time.Hour)
		Expect(err).To(HaveOccurred())
		raw, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(Equal("broken"))
	})
	It("reports the effective expiry after retention changes", func() {
		Expect(store.Create(job)).To(Succeed())
		job.Status = "completed"
		Expect(store.Update(job)).To(Succeed())
		again, err := NewChatStore(nil, directory, 2*time.Hour)
		Expect(err).NotTo(HaveOccurred())
		again.(*chatStore).now = func() time.Time { return now }
		got, err := again.Get("alice", "assistant", "root")
		Expect(err).NotTo(HaveOccurred())
		Expect(*got.ExpiresAt).To(Equal(now.Add(2 * time.Hour)))
	})
	It("fails closed if its backing file disappears after admission", func() {
		Expect(store.Create(job)).To(Succeed())
		Expect(os.Remove(filepath.Join(directory, "chat-jobs.json"))).To(Succeed())
		_, err := store.Get("alice", "assistant", "root")
		Expect(err).To(HaveOccurred())
		Expect(err).NotTo(MatchError(ErrChatJobNotFound))
		job.ID = "second"
		Expect(store.Create(job)).NotTo(Succeed())
	})
	It("does not resurrect scrubbed tombstones when retention increases", func() {
		Expect(store.Create(job)).To(Succeed())
		job.Status = "completed"
		Expect(store.Update(job)).To(Succeed())
		now = now.Add(time.Hour)
		Expect(store.Cleanup()).To(Succeed())
		again, err := NewChatStore(nil, directory, 2*time.Hour)
		Expect(err).NotTo(HaveOccurred())
		again.(*chatStore).now = func() time.Time { return now }
		_, err = again.Get("alice", "assistant", "root")
		Expect(err).To(MatchError(ErrChatJobExpired))
	})
	It("tightens existing storage permissions on startup", func() {
		Expect(store.Create(job)).To(Succeed())
		path := filepath.Join(directory, "chat-jobs.json")
		Expect(os.Chmod(path, 0644)).To(Succeed())
		_, err := NewChatStore(nil, directory, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		info, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0600)))
	})
	It("returns write failures without creating an accepted job", func() {
		Expect(os.RemoveAll(directory)).To(Succeed())
		Expect(os.WriteFile(directory, []byte("blocked"), 0600)).To(Succeed())
		Expect(store.Create(job)).NotTo(Succeed())
		Expect(os.Remove(directory)).To(Succeed())
		Expect(os.Mkdir(directory, 0700)).To(Succeed())
		var err error
		store, err = NewChatStore(nil, directory, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		_, err = store.Get("alice", "assistant", "root")
		Expect(err).To(MatchError(ErrChatJobNotFound))
	})
})
