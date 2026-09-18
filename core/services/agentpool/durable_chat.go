// SPDX-License-Identifier: MIT
package agentpool

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mudler/LocalAGI/core/chat"
	"github.com/mudler/LocalAGI/core/sse"
	"github.com/mudler/LocalAGI/core/types"
	"github.com/mudler/LocalAI/core/services/jobs"
	"github.com/mudler/xlog"
)

var ErrJobPersistence = errors.New("agent job persistence is unavailable")

func (s *AgentPoolService) initChatStore(ctx context.Context) error {
	days := s.appConfig.AgentJobRetentionDays
	if days <= 0 {
		days = 30
	}
	store, err := jobs.NewChatStore(s.users.authDB, filepath.Join(s.stateDir, "chat-jobs"), time.Duration(days)*24*time.Hour)
	if err != nil {
		return ErrJobPersistence
	}
	if err = store.Reconcile(); err != nil {
		return ErrJobPersistence
	}
	if err = store.Cleanup(); err != nil {
		return ErrJobPersistence
	}
	s.chatStore = store
	cleanupCtx, cancel := context.WithCancel(ctx)
	s.chatCleanupCancel = cancel
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-cleanupCtx.Done():
				return
			case <-ticker.C:
				if store.Cleanup() != nil {
					xlog.Error("Agent job retention cleanup failed")
				}
			}
		}
	}()
	return nil
}

func (s *AgentPoolService) GetChatJobForUser(owner, agent, id string) (jobs.ChatJob, error) {
	if s.localAGI.pool == nil {
		return jobs.ChatJob{}, ErrInteractiveUnsupported
	}
	if s.chatStore == nil {
		return jobs.ChatJob{}, ErrJobPersistence
	}
	record, err := s.chatStore.Get(owner, agent, id)
	if errors.Is(err, jobs.ErrChatJobNotFound) || errors.Is(err, jobs.ErrChatJobExpired) {
		return jobs.ChatJob{}, err
	}
	if err != nil {
		return jobs.ChatJob{}, ErrJobPersistence
	}
	// Authorize first: even transient persistence failures must not disclose IDs.
	if _, bad := s.chatPersistenceFailures.Load(id); bad {
		return jobs.ChatJob{}, ErrJobPersistence
	}
	return record, nil
}

type chatAdmission struct {
	receipt ChatReceipt
	err     error
}
type durableChatAgent struct {
	chat.Agent
	service                            *AgentPoolService
	record                             jobs.ChatJob
	notify                             chan chatAdmission
	send                               func(sse.Envelope)
	decorate                           func(*types.JobResult, time.Duration) map[string]any
	mu                                 sync.Mutex
	admitted, finished, terminalFailed bool
	buffered                           []sse.Envelope
}

func (a *durableChatAgent) InjectChat(conv, message, id string) (bool, error) {
	if live, ok := a.Agent.(interface {
		InjectChat(string, string, string) (bool, error)
	}); ok {
		handled, err := live.InjectChat(conv, message, id)
		if handled {
			a.notify <- chatAdmission{receipt: ChatReceipt{Status: "message_received", MessageID: id}, err: err}
		}
		return handled, err
	}
	return false, nil
}
func (a *durableChatAgent) Ask(opts ...types.JobOption) (result *types.JobResult) {
	if err := a.service.chatStore.Create(a.record); err != nil {
		a.notify <- chatAdmission{err: ErrJobPersistence}
		return &types.JobResult{Error: ErrJobPersistence}
	}
	a.mu.Lock()
	a.admitted = true
	buffered := a.buffered
	a.buffered = nil
	a.mu.Unlock()
	a.notify <- chatAdmission{receipt: ChatReceipt{Status: "message_received", MessageID: a.record.MessageID, JobID: a.record.ID}}
	for _, e := range buffered {
		a.emit(e)
	}
	started := time.Now()
	defer func() {
		if recover() != nil {
			result = &types.JobResult{Error: errors.New("agent execution failed")}
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		now := time.Now().UTC()
		a.record.CompletedAt = &now
		a.record.UpdatedAt = now
		a.finished = true
		if result == nil || result.Error != nil {
			a.record.Status = "failed"
			a.record.Error = &jobs.ChatJobError{Code: "execution_failed", Message: "Agent execution failed or was cancelled."}
			result = &types.JobResult{Error: errors.New(a.record.Error.Message)}
		} else {
			a.record.Status = "completed"
			response := result.Response
			a.record.Result = &response
		}
		if a.service.chatStore.Update(a.record) != nil {
			a.terminalFailed = true
			a.service.chatPersistenceFailures.Store(a.record.ID, true)
			xlog.Error("Agent terminal result persistence failed", "job_id", a.record.ID)
			a.send(sse.NewMessage(marshalJobEvent(map[string]any{"job_id": a.record.ID, "message_id": a.record.MessageID, "conversation_id": a.record.ConversationID, "error": "Agent result persistence is unavailable; use job lookup to check recovery.", "code": "persistence_unavailable"})).WithEvent("json_error"))
		} else {
			a.service.chatPersistenceFailures.Delete(a.record.ID)
		}
	}()
	result = a.Agent.Ask(opts...)
	if a.decorate != nil {
		a.decorate(result, time.Since(started))
	}
	return result
}
func marshalJobEvent(payload map[string]any) string { b, _ := json.Marshal(payload); return string(b) }
func (a *durableChatAgent) emit(e sse.Envelope) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.admitted {
		a.buffered = append(a.buffered, e)
		return
	}
	if a.terminalFailed {
		return
	}
	event, ok := e.(*sse.Message)
	if !ok {
		a.send(e)
		return
	}
	var payload map[string]any
	if json.Unmarshal([]byte(event.Data), &payload) != nil {
		return
	}
	// Child interaction statuses use the root message ID and must surface a
	// pending human decision. Only Ask return, never a child event, is terminal.
	if !a.finished && event.Event == "json_message_status" && payload["message_id"] == a.record.MessageID {
		status, _ := payload["status"].(string)
		switch status {
		case "processing":
			status = "running"
		case "waiting_user", "waiting_agents":
		default:
			status = ""
		}
		if status != "" {
			a.record.Status = status
			a.record.UpdatedAt = time.Now().UTC()
			if a.service.chatStore.Update(a.record) != nil {
				xlog.Error("Agent status persistence failed", "job_id", a.record.ID)
				a.service.chatPersistenceFailures.Store(a.record.ID, true)
			} else {
				a.service.chatPersistenceFailures.Delete(a.record.ID)
			}
		}
	}
	payload["job_id"] = a.record.ID
	a.send(sse.NewMessage(marshalJobEvent(payload)).WithEvent(event.Event))
}
func (s *AgentPoolService) startDurableChat(agent chat.Agent, key, message, conv, id string, send func(sse.Envelope), decorate func(*types.JobResult, time.Duration) map[string]any) (ChatReceipt, error) {
	if s.chatStore == nil {
		return ChatReceipt{}, ErrJobPersistence
	}
	owner, alias := "", key
	if i := strings.LastIndexByte(key, ':'); i >= 0 {
		owner, alias = key[:i], key[i+1:]
	}
	now := time.Now().UTC()
	wrapper := &durableChatAgent{Agent: agent, service: s, record: jobs.ChatJob{ID: id, Owner: owner, Agent: alias, MessageID: id, ConversationID: conv, Status: "accepted", CreatedAt: now, UpdatedAt: now}, notify: make(chan chatAdmission, 1), send: send}
	// Decorate before terminal persistence so the durable report matches the final reply.
	var metadata map[string]any
	wrapper.decorate = func(r *types.JobResult, d time.Duration) map[string]any {
		if decorate != nil {
			metadata = decorate(r, d)
		}
		return metadata
	}
	go runAgentChat(wrapper, message, conv, id, wrapper.emit, func(*types.JobResult, time.Duration) map[string]any { return metadata })
	admitted := <-wrapper.notify
	return admitted.receipt, admitted.err
}
