package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
)

func syntheticChatConfig(baseURL string, retries, timeoutMS int) models.AIConfig {
	return models.AIConfig{
		BaseURL:       baseURL + "/v1",
		APIKey:        "synthetic-test-key",
		ModelName:     "synthetic-test-model",
		MaxRetryCount: retries,
		TimeoutMS:     timeoutMS,
	}
}

func TestChatWithConfigRetriesOnlyBoundedTransientProviderStatuses(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.Header().Set("Retry-After-Ms", "0")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, `{"error":{"message":"synthetic provider failure","type":"server_error"}}`)
			}))
			defer server.Close()

			_, err := LLM.ChatWithConfig(context.Background(), syntheticChatConfig(server.URL, 2, 1000), "synthetic system", "synthetic user")
			if err == nil {
				t.Fatal("ChatWithConfig() error = nil, want synthetic provider error")
			}
			if got := attempts.Load(); got != 3 {
				t.Fatalf("provider attempts = %d, want initial attempt plus two bounded retries", got)
			}
		})
	}
}

func TestChatWithConfigDoesNotRetryNonTransientOrMalformedResponses(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		contentTyp string
	}{
		{name: "bad request", status: http.StatusBadRequest, body: `{"error":{"message":"synthetic invalid request"}}`, contentTyp: "application/json"},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":{"message":"synthetic unauthorized"}}`, contentTyp: "application/json"},
		{name: "payment required", status: http.StatusPaymentRequired, body: `{"error":{"message":"synthetic insufficient balance"}}`, contentTyp: "application/json"},
		{name: "unprocessable entity", status: http.StatusUnprocessableEntity, body: `{"error":{"message":"synthetic invalid input"}}`, contentTyp: "application/json"},
		{name: "malformed success response", status: http.StatusOK, body: `{malformed`, contentTyp: "application/json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", tc.contentTyp)
				w.Header().Set("Retry-After-Ms", "0")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()

			_, err := LLM.ChatWithConfig(context.Background(), syntheticChatConfig(server.URL, 2, 1000), "synthetic system", "synthetic user")
			if err == nil {
				t.Fatal("ChatWithConfig() error = nil, want provider/decoding error")
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("provider attempts = %d, want no blind retry", got)
			}
		})
	}
}

func TestChatWithConfigTimeoutStopsNetworkRetry(t *testing.T) {
	var attempts atomic.Int32
	requestStarted := make(chan struct{}, 8)
	releaseHandlers := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		requestStarted <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-releaseHandlers:
		}
	}))
	defer func() {
		close(releaseHandlers)
		server.Close()
	}()

	startedAt := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := LLM.ChatWithConfig(context.Background(), syntheticChatConfig(server.URL, 2, 500), "synthetic system", "synthetic user")
		done <- err
	}()
	select {
	case <-requestStarted:
	case err := <-done:
		t.Fatalf("provider call failed before the fake server received it: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("fake LLM server did not receive the timeout request")
	}
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("provider call did not finish within the bounded timeout")
	}
	if err == nil {
		t.Fatal("ChatWithConfig() error = nil, want configured request timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("ChatWithConfig() error = %v, want a timeout/deadline error", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("provider timeout took %s, want it bounded by the configured deadline", elapsed)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("provider attempts = %d, want configured timeout to stop network retries", got)
	}
}
