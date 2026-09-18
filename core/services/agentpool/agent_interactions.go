// SPDX-License-Identifier: MIT
package agentpool

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mudler/LocalAGI/core/interactions"
	coreTypes "github.com/mudler/LocalAGI/core/types"
	"github.com/mudler/LocalAI/core/services/agents"
	"github.com/mudler/cogito"
	"gorm.io/gorm"
)

var ErrInteractiveUnsupported = errors.New("interactive agent chat is available only in standalone mode")

type PendingQuestionError struct{ QuestionID string }

func (e *PendingQuestionError) Error() string { return interactions.ErrFreeTextNotAllowed.Error() }
func (e *PendingQuestionError) Unwrap() error { return interactions.ErrFreeTextNotAllowed }

type ChatReceipt struct {
	JobID      string `json:"job_id,omitempty"`
	Status     string `json:"status"`
	MessageID  string `json:"message_id,omitempty"`
	QuestionID string `json:"question_id,omitempty"`
}

// ChatInConversationForUser preserves legacy distributed chat until executor
// parity is available. Interaction endpoints explicitly reject that mode.
func (s *AgentPoolService) ChatInConversationForUser(userID, name, message, conversationID string) (ChatReceipt, error) {
	if s.localAGI.pool == nil {
		id, err := s.ChatForUser(userID, name, message)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = fmt.Errorf("%w: %s", ErrAgentNotFound, name)
		}
		return ChatReceipt{Status: "message_received", MessageID: id}, err
	}
	return s.chatInConversation(agents.AgentKey(userID, name), message, conversationID)
}

func (s *AgentPoolService) chatInConversation(key, message, conversationID string) (ChatReceipt, error) {
	if s.localAGI.pool == nil {
		return ChatReceipt{}, ErrInteractiveUnsupported
	}
	ag := s.localAGI.pool.GetAgent(key)
	if ag == nil {
		return ChatReceipt{}, fmt.Errorf("%w: %s", ErrAgentNotFound, key)
	}
	manager := s.localAGI.pool.GetManager(key)
	if manager == nil {
		return ChatReceipt{}, fmt.Errorf("SSE manager not found for agent: %s", key)
	}
	if conversationID != "" {
		questionID, handled, err := ag.Interactions().AnswerText(conversationID, message)
		if errors.Is(err, interactions.ErrFreeTextNotAllowed) {
			return ChatReceipt{}, &PendingQuestionError{QuestionID: questionID}
		}
		if err != nil {
			return ChatReceipt{}, err
		}
		if handled {
			return ChatReceipt{Status: "answer_received", QuestionID: questionID}, nil
		}
	}
	messageID := uuid.NewString()
	receipt := ChatReceipt{Status: "message_received", MessageID: messageID}
	if handled, err := ag.InjectChat(conversationID, message, messageID); handled {
		return receipt, err
	}
	return s.startDurableChat(ag, key, message, conversationID, messageID, manager.Send, func(result *coreTypes.JobResult, elapsed time.Duration) map[string]any {
		return s.decorateChatResponse(key, message, result, elapsed)
	})
}

func (s *AgentPoolService) interactionRegistryForUser(userID, name string) (*interactions.Registry, error) {
	if s.localAGI.pool == nil {
		return nil, ErrInteractiveUnsupported
	}
	ag := s.localAGI.pool.GetAgent(agents.AgentKey(userID, name))
	if ag == nil {
		return nil, fmt.Errorf("%w: %s", ErrAgentNotFound, name)
	}
	return ag.Interactions(), nil
}

func (s *AgentPoolService) PendingForUser(userID, name, conversationID string) (interactions.Snapshot, error) {
	registry, err := s.interactionRegistryForUser(userID, name)
	if err != nil {
		return interactions.Snapshot{}, err
	}
	return registry.Pending(conversationID), nil
}

func (s *AgentPoolService) AnswerForUser(userID, name, questionID string, answer cogito.UserAnswer) error {
	registry, err := s.interactionRegistryForUser(userID, name)
	if err != nil {
		return err
	}
	return registry.Answer(questionID, answer)
}

func (s *AgentPoolService) DecidePlanForUser(userID, name, planID string, approved bool, subtasks *[]string, feedback string) error {
	registry, err := s.interactionRegistryForUser(userID, name)
	if err != nil {
		return err
	}
	return registry.Decide(planID, approved, subtasks, feedback)
}
