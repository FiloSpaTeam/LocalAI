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
	"gorm.io/gorm"
)

var _ = Describe("Durable agent job lookup authentication", func() {
	DescribeTable("requires valid credentials when authentication is configured",
		func(authenticated bool, expectedStatus int) {
			svc := &chatJobLookupStub{}
			e := echo.New()
			e.Use(auth.Middleware(nil, &config.ApplicationConfig{ApiKeys: []string{"lookup-test-secret"}}))
			e.GET("/api/agents/:name/jobs/:job_id", getAgentChatJobHandler(svc))
			req := httptest.NewRequest(http.MethodGet, "/api/agents/demo/jobs/job-1", nil)
			if authenticated {
				req.Header.Set(echo.HeaderAuthorization, "Bearer lookup-test-secret")
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(expectedStatus))
			if authenticated {
				Expect(svc.userID).To(Equal("legacy-api-key"))
			} else {
				Expect(svc.jobID).To(BeEmpty())
			}
		},
		Entry("missing credentials", false, http.StatusUnauthorized),
		Entry("valid credentials", true, http.StatusOK),
	)

	DescribeTable("requires the agents feature for a regular user",
		func(enabled bool, expectedStatus int) {
			svc := &chatJobLookupStub{}
			e := echo.New()
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Set("auth_user", &auth.User{ID: "alice", Role: auth.RoleUser})
					// Seed the same request cache that permission middleware populates,
					// keeping the feature decision independent of database drivers.
					c.Set("auth_permissions", &auth.UserPermission{UserID: "alice", Permissions: auth.PermissionMap{auth.FeatureAgents: enabled}})
					return next(c)
				}
			})
			group := e.Group("/api/agents", auth.RequireFeature(&gorm.DB{}, auth.FeatureAgents))
			group.GET("/:name/jobs/:job_id", getAgentChatJobHandler(svc))
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/demo/jobs/job-1", nil))
			Expect(rec.Code).To(Equal(expectedStatus))
			if enabled {
				Expect(svc.userID).To(Equal("alice"))
			} else {
				Expect(svc.jobID).To(BeEmpty())
			}
		},
		Entry("feature disabled", false, http.StatusForbidden),
		Entry("feature enabled", true, http.StatusOK),
	)
})
