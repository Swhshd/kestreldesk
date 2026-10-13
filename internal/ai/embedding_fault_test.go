package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

func TestEmbeddingProviderFailuresReturnErrorsWithoutRetrying(t *testing.T) {
	var attempts atomic.Int32
	timeoutRequestStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		var input struct {
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid synthetic request", http.StatusBadRequest)
			return
		}

		switch input.Input {
		case "synthetic-503":
			http.Error(w, `{"error":{"message":"synthetic unavailable"}}`, http.StatusServiceUnavailable)
		case "synthetic-malformed":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[]}`)
		case "synthetic-timeout":
			timeoutRequestStarted <- struct{}{}
			<-r.Context().Done()
		default:
			http.Error(w, "unexpected synthetic input", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open synthetic embedding config database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get synthetic embedding SQL database: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close synthetic embedding SQL database: %v", err)
		}
	})
	if err := db.AutoMigrate(&models.AIConfig{}); err != nil {
		t.Fatalf("migrate synthetic embedding config: %v", err)
	}
	previousDB := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() { sqls.SetDB(previousDB) })
	const syntheticAPIKey = "synthetic-embedding-key"
	if err := db.Create(&models.AIConfig{
		Name:          "synthetic embedding provider",
		Provider:      enums.AIProviderOpenAI,
		BaseURL:       server.URL + "/v1",
		APIKey:        syntheticAPIKey,
		ModelType:     enums.AIModelTypeEmbedding,
		ModelName:     "synthetic-embedding-model",
		Dimension:     3,
		TimeoutMS:     500,
		MaxRetryCount: 0,
		Status:        enums.StatusOk,
	}).Error; err != nil {
		t.Fatalf("create synthetic embedding config: %v", err)
	}

	for _, input := range []string{"synthetic-503", "synthetic-malformed", "synthetic-timeout"} {
		t.Run(input, func(t *testing.T) {
			attempts.Store(0)
			started := time.Now()
			type embeddingCall struct {
				result *EmbeddingResult
				err    error
			}
			done := make(chan embeddingCall, 1)
			go func() {
				result, err := Embedding.GenerateEmbedding(t.Context(), input)
				done <- embeddingCall{result: result, err: err}
			}()
			if input == "synthetic-timeout" {
				select {
				case <-timeoutRequestStarted:
				case early := <-done:
					t.Fatalf("embedding timed out before the fake server received the request: %v", early.err)
				case <-time.After(2 * time.Second):
					t.Fatal("fake embedding server did not receive the timeout request")
				}
			}
			var call embeddingCall
			select {
			case call = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("embedding call did not finish within the bounded timeout")
			}
			result, err := call.result, call.err
			if err == nil {
				t.Fatal("GenerateEmbedding() error = nil, want synthetic provider failure")
			}
			if result != nil {
				t.Fatalf("GenerateEmbedding() returned a result on failure: %#v", result)
			}
			if attempts.Load() != 1 {
				t.Fatalf("synthetic request attempts = %d, want 1", attempts.Load())
			}
			if input == "synthetic-timeout" && time.Since(started) > 2*time.Second {
				t.Fatalf("embedding timeout was not bounded: %s", time.Since(started))
			}
			if strings.Contains(err.Error(), syntheticAPIKey) {
				t.Fatalf("embedding error exposed synthetic API key: %v", err)
			}
		})
	}
}
