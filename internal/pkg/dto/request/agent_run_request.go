package request

// This file has been modified for the KestrelDesk project.

import "github.com/Swhshd/kestreldesk/internal/pkg/enums"

type SaveAgentRunQualityFeedbackRequest struct {
	AgentRunID       int64                          `json:"agentRunId"`
	ResolutionStatus enums.AgentRunResolutionStatus `json:"resolutionStatus"`
	EvidenceStatus   enums.AgentRunEvidenceStatus   `json:"evidenceStatus"`
	Comment          string                         `json:"comment"`
}
