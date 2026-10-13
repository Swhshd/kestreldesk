package services

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"fmt"

	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
)

var AgentEvaluationService = newAgentEvaluationService()
var AgentEvaluationRunHook func(context.Context, request.RunAgentEvaluationRequest) (*response.AgentEvaluationReportResponse, error)

type agentEvaluationService struct{}

func newAgentEvaluationService() *agentEvaluationService { return &agentEvaluationService{} }

func (s *agentEvaluationService) Run(ctx context.Context, req request.RunAgentEvaluationRequest) (*response.AgentEvaluationReportResponse, error) {
	if req.AIAgentID <= 0 {
		return nil, errorsx.InvalidParam("ai agent id is required")
	}
	if len(req.Cases) == 0 {
		return nil, errorsx.InvalidParam("evaluation cases are required")
	}
	if len(req.Cases) > 100 {
		return nil, errorsx.InvalidParam("evaluation case limit exceeded")
	}
	if AgentEvaluationRunHook == nil {
		return nil, fmt.Errorf("agent evaluation runner is not initialized")
	}
	return AgentEvaluationRunHook(ctx, req)
}
