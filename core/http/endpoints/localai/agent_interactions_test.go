// SPDX-License-Identifier: MIT
package localai

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"

	"github.com/labstack/echo/v4"
	"github.com/mudler/LocalAGI/core/interactions"
	"github.com/mudler/LocalAI/core/http/auth"
	"github.com/mudler/LocalAI/core/services/agentpool"
	"github.com/mudler/cogito"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type interactionServiceStub struct {
	chatReceipt agentpool.ChatReceipt
	chatErr     error
	answerErr   error
	planErr     error
	pending     interactions.Snapshot
	pendingErr  error

	userID         string
	name           string
	message        string
	conversationID string
	questionID     string
	answer         cogito.UserAnswer
	planID         string
	approved       bool
	subtasks       *[]string
	feedback       string
}

func (s *interactionServiceStub) ChatInConversationForUser(userID, name, message, conversationID string) (agentpool.ChatReceipt, error) {
	s.userID, s.name, s.message, s.conversationID = userID, name, message, conversationID
	return s.chatReceipt, s.chatErr
}

func (s *interactionServiceStub) AnswerForUser(userID, name, questionID string, answer cogito.UserAnswer) error {
	s.userID, s.name, s.questionID, s.answer = userID, name, questionID, answer
	return s.answerErr
}

func (s *interactionServiceStub) DecidePlanForUser(userID, name, planID string, approved bool, subtasks *[]string, feedback string) error {
	s.userID, s.name, s.planID, s.approved, s.subtasks, s.feedback = userID, name, planID, approved, subtasks, feedback
	return s.planErr
}

func (s *interactionServiceStub) PendingForUser(userID, name, conversationID string) (interactions.Snapshot, error) {
	s.userID, s.name, s.conversationID = userID, name, conversationID
	return s.pending, s.pendingErr
}

var _ = Describe("Agent interaction endpoints", func() {
	var (
		e   *echo.Echo
		svc *interactionServiceStub
	)

	request := func(method, target, body string, handler echo.HandlerFunc) *httptest.ResponseRecorder {
		e = echo.New()
		e.Add(method, "/api/agents/:name/action", handler)
		req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	BeforeEach(func() {
		svc = &interactionServiceStub{}
	})

	It("forwards conversation identity and returns the service chat receipt", func() {
		svc.chatReceipt = agentpool.ChatReceipt{Status: "waiting_agents", MessageID: "message-1"}
		handler := func(c echo.Context) error {
			c.Set("auth_user", &auth.User{ID: "alice", Role: auth.RoleUser})
			return chatWithAgentHandler(svc)(c)
		}
		rec := request(http.MethodPost, "/api/agents/demo%3Aagent/action", `{"message":" hello ","conversation_id":"conversation-1"}`, handler)

		Expect(rec.Code).To(Equal(http.StatusAccepted))
		Expect(svc.userID).To(Equal("alice"))
		Expect(svc.name).To(Equal("demo:agent"))
		Expect(svc.message).To(Equal("hello"))
		Expect(svc.conversationID).To(Equal("conversation-1"))
		Expect(rec.Body.String()).To(MatchJSON(`{"status":"waiting_agents","message_id":"message-1"}`))
	})

	It("reports the pending question when chat free text is forbidden", func() {
		svc.chatErr = &agentpool.PendingQuestionError{QuestionID: "question-1"}
		rec := request(http.MethodPost, "/api/agents/demo/action", `{"message":"typed answer","conversation_id":"conversation-1"}`, chatWithAgentHandler(svc))

		Expect(rec.Code).To(Equal(http.StatusConflict))
		Expect(rec.Body.String()).To(MatchJSON(`{"pending_question_id":"question-1"}`))
	})

	It("forwards an answer with effective user scoping", func() {
		handler := func(c echo.Context) error {
			c.Set("auth_user", &auth.User{ID: "root", Role: auth.RoleAdmin})
			return answerAgentQuestionHandler(svc)(c)
		}
		rec := request(http.MethodPost, "/api/agents/demo/action?user_id=alice", `{"question_id":"question-1","selected":["A"],"text":"because"}`, handler)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(svc.userID).To(Equal("alice"))
		Expect(svc.questionID).To(Equal("question-1"))
		Expect(svc.answer).To(Equal(cogito.UserAnswer{Selected: []string{"A"}, Text: "because"}))
		Expect(rec.Body.String()).To(MatchJSON(`{"status":"answer_received"}`))
	})

	DescribeTable("maps answer errors",
		func(serviceErr error, status int) {
			svc.answerErr = serviceErr
			rec := request(http.MethodPost, "/api/agents/demo/action", `{"question_id":"question-1","text":"answer"}`, answerAgentQuestionHandler(svc))
			Expect(rec.Code).To(Equal(status))
		},
		Entry("unknown question", cogito.ErrQuestionNotFound, http.StatusNotFound),
		Entry("invalid answer", cogito.ErrInvalidAnswer, http.StatusBadRequest),
		Entry("missing agent", agentpool.ErrAgentNotFound, http.StatusNotFound),
		Entry("unsupported executor", agentpool.ErrInteractiveUnsupported, http.StatusNotImplemented),
		Entry("unexpected failure", errors.New("boom"), http.StatusInternalServerError),
	)

	It("requires a non-empty question_id", func() {
		rec := request(http.MethodPost, "/api/agents/demo/action", `{"text":"answer"}`, answerAgentQuestionHandler(svc))
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(rec.Body.String()).To(MatchJSON(`{"error":"question_id is required"}`))
	})

	It("accepts an explicit false plan decision and preserves optional edits", func() {
		rec := request(http.MethodPost, "/api/agents/demo/action", `{"plan_id":"plan-1","approved":false,"subtasks":["revised"],"feedback":"retry"}`, decideAgentPlanHandler(svc))

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(svc.planID).To(Equal("plan-1"))
		Expect(svc.approved).To(BeFalse())
		Expect(svc.subtasks).NotTo(BeNil())
		Expect(*svc.subtasks).To(Equal([]string{"revised"}))
		Expect(svc.feedback).To(Equal("retry"))
		Expect(rec.Body.String()).To(MatchJSON(`{"status":"plan_decision_received"}`))
	})

	DescribeTable("validates plan decisions",
		func(body, message string) {
			rec := request(http.MethodPost, "/api/agents/demo/action", body, decideAgentPlanHandler(svc))
			Expect(rec.Code).To(Equal(http.StatusBadRequest))
			var response map[string]string
			Expect(json.Unmarshal(rec.Body.Bytes(), &response)).To(Succeed())
			Expect(response["error"]).To(Equal(message))
		},
		Entry("plan id", `{"approved":true}`, "plan_id is required"),
		Entry("approved", `{"plan_id":"plan-1"}`, "approved is required"),
	)

	DescribeTable("maps plan errors",
		func(serviceErr error, status int) {
			svc.planErr = serviceErr
			rec := request(http.MethodPost, "/api/agents/demo/action", `{"plan_id":"plan-1","approved":true}`, decideAgentPlanHandler(svc))
			Expect(rec.Code).To(Equal(status))
		},
		Entry("unknown plan", interactions.ErrPlanNotFound, http.StatusNotFound),
		Entry("invalid decision", interactions.ErrInvalidPlanDecision, http.StatusBadRequest),
		Entry("missing agent", agentpool.ErrAgentNotFound, http.StatusNotFound),
		Entry("unsupported executor", agentpool.ErrInteractiveUnsupported, http.StatusNotImplemented),
	)

	It("returns the pending snapshot for the requested conversation", func() {
		svc.pending = interactions.Snapshot{Questions: []interactions.Question{}, Plan: nil}
		rec := request(http.MethodGet, "/api/agents/demo/action?conversation_id=conversation-2", "", pendingAgentInteractionsHandler(svc))

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(svc.conversationID).To(Equal("conversation-2"))
		Expect(rec.Body.String()).To(MatchJSON(`{"questions":[],"plan":null}`))
	})

	It("maps an unsupported pending lookup to 501", func() {
		svc.pendingErr = agentpool.ErrInteractiveUnsupported
		rec := request(http.MethodGet, "/api/agents/demo/action", "", pendingAgentInteractionsHandler(svc))
		Expect(rec.Code).To(Equal(http.StatusNotImplemented))
	})
})
