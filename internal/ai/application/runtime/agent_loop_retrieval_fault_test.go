package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
)

func TestAgentLoopSeparatesRetrievalFailureFromNoEvidence(t *testing.T) {
	agent := models.AIAgent{KnowledgeIDs: "17", FallbackMode: enums.AIAgentFallbackModeSuggestRetry}

	t.Run("retrieval dependency failure", func(t *testing.T) {
		retrieveErr := errors.New("synthetic retrieval dependency failure")
		policy := evaluateAgentLoopResponsePolicy(agent, "", retrieveErr)
		prompt := buildAgentLoopSystemPrompt(agent, true, "", retrieveErr)
		if policy.Action != "retrieval_unavailable" || policy.Reason != "knowledge_retrieve_error" {
			t.Fatalf("retrieval failure policy = %#v", policy)
		}
		if !strings.Contains(prompt, "Knowledge retrieval is temporarily unavailable") {
			t.Fatalf("prompt did not identify dependency unavailability: %q", prompt)
		}
		if strings.Contains(prompt, "found no supporting evidence") {
			t.Fatalf("retrieval failure was described as a successful empty search: %q", prompt)
		}
	})

	t.Run("successful retrieval with no evidence", func(t *testing.T) {
		policy := evaluateAgentLoopResponsePolicy(agent, "", nil)
		prompt := buildAgentLoopSystemPrompt(agent, true, "", nil)
		if policy.Action != "evidence_required" || policy.Reason != "knowledge_evidence_missing" {
			t.Fatalf("empty evidence policy = %#v", policy)
		}
		if !strings.Contains(prompt, "found no supporting evidence") {
			t.Fatalf("prompt did not identify a successful empty search: %q", prompt)
		}
		if strings.Contains(prompt, "temporarily unavailable") {
			t.Fatalf("successful empty search was described as a dependency failure: %q", prompt)
		}
	})
}
