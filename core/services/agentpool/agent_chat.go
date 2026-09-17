// SPDX-License-Identifier: MIT
package agentpool

import (
	"encoding/json"
	"time"

	"github.com/mudler/LocalAGI/core/chat"
	"github.com/mudler/LocalAGI/core/sse"
	"github.com/mudler/LocalAGI/core/types"
)

// decoratedChatAgent keeps LocalAI's metrics, citations and artifact handling
// around the shared lifecycle, including its second live-loop admission check.
type decoratedChatAgent struct {
	chat.Agent
	decorate func(*types.JobResult, time.Duration) map[string]any
	metadata map[string]any
}

func (a *decoratedChatAgent) Ask(opts ...types.JobOption) *types.JobResult {
	started := time.Now()
	result := a.Agent.Ask(opts...)
	if a.decorate != nil {
		a.metadata = a.decorate(result, time.Since(started))
	}
	return result
}

func (a *decoratedChatAgent) InjectChat(conversationID, message, messageID string) (bool, error) {
	if live, ok := a.Agent.(interface {
		InjectChat(string, string, string) (bool, error)
	}); ok {
		return live.InjectChat(conversationID, message, messageID)
	}
	return false, nil
}

func runAgentChat(agent chat.Agent, message, conversationID, messageID string, send func(sse.Envelope), decorate func(*types.JobResult, time.Duration) map[string]any) {
	wrapped := &decoratedChatAgent{Agent: agent, decorate: decorate}
	chat.Run(wrapped, message, conversationID, messageID, func(envelope sse.Envelope) {
		// Only the final reply reads metadata: it is sent synchronously after Ask
		// returns, whereas parked replies and job events may arrive concurrently.
		if event, ok := envelope.(*sse.Message); ok && event.Event == "json_message" {
			var payload map[string]any
			if json.Unmarshal([]byte(event.Data), &payload) == nil && payload["id"] == messageID+"-agent" {
				if len(wrapped.metadata) > 0 {
					payload["metadata"] = wrapped.metadata
					if encoded, err := json.Marshal(payload); err == nil {
						envelope = sse.NewMessage(string(encoded)).WithEvent(event.Event)
					}
				}
			}
		}
		send(envelope)
	})
}
