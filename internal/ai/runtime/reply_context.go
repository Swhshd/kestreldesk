package runtime

// This file has been modified for the KestrelDesk project.

import (
	applicationruntime "github.com/Swhshd/kestreldesk/internal/ai/application/runtime"
	"github.com/Swhshd/kestreldesk/internal/models"
)

type aiReplyContext struct {
	Conversation     models.Conversation
	Message          models.Message
	AIAgent          models.AIAgent
	SummaryRef       **applicationruntime.RunResult
	PendingInterrupt *models.ConversationInterrupt
}

func (c aiReplyContext) setSummary(summary *applicationruntime.RunResult) {
	if c.SummaryRef != nil {
		*c.SummaryRef = summary
	}
}
