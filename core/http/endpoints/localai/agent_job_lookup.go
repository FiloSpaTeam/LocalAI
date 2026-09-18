// SPDX-License-Identifier: MIT
package localai

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/mudler/LocalAI/core/application"
	"github.com/mudler/LocalAI/core/schema"
	"github.com/mudler/LocalAI/core/services/agentpool"
	"github.com/mudler/LocalAI/core/services/jobs"
)

type agentChatJobLookupService interface {
	GetChatJobForUser(userID, name, jobID string) (jobs.ChatJob, error)
}

// GetAgentChatJobEndpoint returns a durable root chat status and retained result.
// @Summary Get a durable agent chat job
// @Description Look up a root chat job, including after its agent is deleted. Unknown and other-owner jobs return the same 404. Expired results return 410 while their tombstone is retained.
// @Tags agents
// @Produce json
// @Param name path string true "Public agent name"
// @Param job_id path string true "Job ID returned by chat"
// @Param user_id query string false "Target user ID (admin or agent worker only)"
// @Success 200 {object} jobs.ChatJob
// @Failure 401 {object} schema.ErrorResponse
// @Failure 403 {object} schema.ErrorResponse
// @Failure 404 {object} schema.ErrorResponse
// @Failure 410 {object} schema.ErrorResponse
// @Failure 500 {object} schema.ErrorResponse
// @Failure 501 {object} schema.ErrorResponse
// @Failure 503 {object} schema.ErrorResponse
// @Router /api/agents/{name}/jobs/{job_id} [get]
func GetAgentChatJobEndpoint(app *application.Application) echo.HandlerFunc {
	return func(c echo.Context) error {
		return getAgentChatJobHandler(app.AgentPoolService())(c)
	}
}

func getAgentChatJobHandler(svc agentChatJobLookupService) echo.HandlerFunc {
	return func(c echo.Context) error {
		job, err := svc.GetChatJobForUser(effectiveUserID(c), decodedParam(c, "name"), decodedParam(c, "job_id"))
		if err == nil {
			return c.JSON(http.StatusOK, job)
		}
		status, message, errorType := http.StatusInternalServerError, "unable to retrieve agent job", "server_error"
		switch {
		case errors.Is(err, jobs.ErrChatJobNotFound):
			status, message, errorType = http.StatusNotFound, "agent job not found", "not_found"
		case errors.Is(err, jobs.ErrChatJobExpired):
			status, message, errorType = http.StatusGone, "agent job result expired", "expired"
		case errors.Is(err, agentpool.ErrInteractiveUnsupported):
			status, message, errorType = http.StatusNotImplemented, "durable agent jobs require the embedded executor", "unsupported"
		case errors.Is(err, agentpool.ErrJobPersistence):
			status, message = http.StatusServiceUnavailable, "agent job storage unavailable"
		}
		return c.JSON(status, schema.ErrorResponse{Error: &schema.APIError{Code: status, Message: message, Type: errorType}})
	}
}
