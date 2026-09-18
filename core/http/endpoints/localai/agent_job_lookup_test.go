// SPDX-License-Identifier: MIT
package localai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/mudler/LocalAI/core/http/auth"
	"github.com/mudler/LocalAI/core/services/agentpool"
	"github.com/mudler/LocalAI/core/services/jobs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type chatJobLookupStub struct {
	job                 jobs.ChatJob
	err                 error
	userID, name, jobID string
}

func (s *chatJobLookupStub) GetChatJobForUser(userID, name, jobID string) (jobs.ChatJob, error) {
	s.userID, s.name, s.jobID = userID, name, jobID
	return s.job, s.err
}

var _ = Describe("Durable agent job lookup", func() {
	var svc *chatJobLookupStub
	request := func(user *auth.User, query string) *httptest.ResponseRecorder {
		e := echo.New()
		e.GET("/api/agents/:name/jobs/:job_id", func(c echo.Context) error {
			if user != nil {
				c.Set("auth_user", user)
			}
			return getAgentChatJobHandler(svc)(c)
		})
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/demo%3Aagent/jobs/job%3A1"+query, nil))
		return rec
	}
	BeforeEach(func() { svc = &chatJobLookupStub{} })

	It("returns persisted results using decoded public identifiers", func() {
		Expect(json.Unmarshal([]byte(`{"job_id":"job:1","agent":"demo:agent","message_id":"job:1","conversation_id":"conversation-1","status":"completed","result":"answer","created_at":"2026-09-18T10:00:00Z","updated_at":"2026-09-18T10:01:00Z"}`), &svc.job)).To(Succeed())
		rec := request(&auth.User{ID: "alice", Role: auth.RoleUser}, "")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(svc.userID).To(Equal("alice"))
		Expect(svc.name).To(Equal("demo:agent"))
		Expect(svc.jobID).To(Equal("job:1"))
		var body map[string]any
		Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
		Expect(body).To(HaveKeyWithValue("job_id", "job:1"))
		Expect(body).To(HaveKeyWithValue("result", "answer"))
		Expect(body).To(HaveKeyWithValue("conversation_id", "conversation-1"))
		Expect(body).NotTo(HaveKey("owner"))
		Expect(body).NotTo(HaveKey("owner_id"))
		Expect(body).NotTo(HaveKey("user_id"))
	})

	DescribeTable("scopes lookup to effective identity",
		func(user *auth.User, expected string) {
			rec := request(user, "?user_id=bob")
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(svc.userID).To(Equal(expected))
		},
		Entry("regular users cannot impersonate", &auth.User{ID: "alice", Role: auth.RoleUser}, "alice"),
		Entry("admin may select owner", &auth.User{ID: "admin", Role: auth.RoleAdmin}, "bob"),
		Entry("worker may select owner", &auth.User{ID: "worker", Role: auth.RoleUser, Provider: auth.ProviderAgentWorker}, "bob"),
		Entry("auth disabled cannot select owner", (*auth.User)(nil), ""),
	)

	It("rejects chat admission with 503 when durable persistence fails", func() {
		svc := &interactionServiceStub{chatErr: agentpool.ErrJobPersistence}
		e := echo.New()
		e.POST("/api/agents/:name/chat", chatWithAgentHandler(svc))
		req := httptest.NewRequest(http.MethodPost, "/api/agents/demo/chat", strings.NewReader(`{"message":"hello"}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		Expect(rec.Body.String()).NotTo(ContainSubstring("job_id"))
	})

	DescribeTable("maps safe lookup errors",
		func(err error, status int) {
			svc.err = fmt.Errorf("private storage path and secret: %w", err)
			rec := request(&auth.User{ID: "alice", Role: auth.RoleUser}, "")
			Expect(rec.Code).To(Equal(status))
			Expect(rec.Body.String()).NotTo(ContainSubstring("private storage"))
			var body struct {
				Error struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body.Error.Code).To(Equal(status))
			Expect(body.Error.Message).NotTo(BeEmpty())
		},
		Entry("unknown or other-owner job", jobs.ErrChatJobNotFound, http.StatusNotFound),
		Entry("expired retained result", jobs.ErrChatJobExpired, http.StatusGone),
		Entry("unsupported executor", agentpool.ErrInteractiveUnsupported, http.StatusNotImplemented),
		Entry("unavailable durable storage", agentpool.ErrJobPersistence, http.StatusServiceUnavailable),
		Entry("unexpected error", errors.New("secret"), http.StatusInternalServerError),
	)
})
