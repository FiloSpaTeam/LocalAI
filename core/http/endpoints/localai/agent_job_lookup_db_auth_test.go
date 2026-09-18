//go:build auth

// SPDX-License-Identifier: MIT
package localai

import (
	"net/http"
	"net/http/httptest"

	"github.com/labstack/echo/v4"
	"github.com/mudler/LocalAI/core/config"
	"github.com/mudler/LocalAI/core/http/auth"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Durable agent job lookup database authentication", func() {
	DescribeTable("requires authentication and the agents feature",
		func(authenticated, enabled bool, expectedStatus int) {
			db, err := auth.InitDB(":memory:")
			Expect(err).NotTo(HaveOccurred())
			sqlDB, err := db.DB()
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { Expect(sqlDB.Close()).To(Succeed()) })
			user := auth.User{ID: "alice", Email: "alice@example.com", Role: auth.RoleUser, Status: "active", Provider: auth.ProviderLocal}
			Expect(db.Create(&user).Error).To(Succeed())
			Expect(auth.UpdateUserPermissions(db, user.ID, auth.PermissionMap{auth.FeatureAgents: enabled})).To(Succeed())
			token, _, err := auth.CreateAPIKey(db, user.ID, "test", auth.RoleUser, "lookup-test-secret", nil)
			Expect(err).NotTo(HaveOccurred())
			svc := &chatJobLookupStub{}
			e := echo.New()
			e.Use(auth.Middleware(db, &config.ApplicationConfig{Auth: config.AuthConfig{Enabled: true, APIKeyHMACSecret: "lookup-test-secret"}}))
			group := e.Group("/api/agents", auth.RequireFeature(db, auth.FeatureAgents))
			group.GET("/:name/jobs/:job_id", getAgentChatJobHandler(svc))
			req := httptest.NewRequest(http.MethodGet, "/api/agents/demo/jobs/job-1", nil)
			if authenticated {
				req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(expectedStatus))
			if expectedStatus == http.StatusOK {
				Expect(svc.userID).To(Equal(user.ID))
			} else {
				Expect(svc.jobID).To(BeEmpty())
			}
		},
		Entry("missing credentials", false, true, http.StatusUnauthorized),
		Entry("feature disabled", true, false, http.StatusForbidden),
		Entry("feature enabled", true, true, http.StatusOK),
	)
})
