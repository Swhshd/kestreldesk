package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ragchunk "github.com/Swhshd/kestreldesk/internal/ai/rag/chunk"
	"github.com/Swhshd/kestreldesk/internal/ai/rag/vectordb"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

func TestQueryEmbeddingFailureIsReturnedInsteadOfEmptyRetrieval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"synthetic unavailable"}}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open synthetic embedding config database: %v", err)
	}
	closeEmbeddingFaultTestDB(t, db)
	if err := db.AutoMigrate(&models.AIConfig{}); err != nil {
		t.Fatalf("migrate synthetic embedding config: %v", err)
	}
	previousDB := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() { sqls.SetDB(previousDB) })
	if err := db.Create(&models.AIConfig{
		Name:          "synthetic embedding provider",
		Provider:      enums.AIProviderOpenAI,
		BaseURL:       server.URL + "/v1",
		APIKey:        "synthetic-embedding-key",
		ModelType:     enums.AIModelTypeEmbedding,
		ModelName:     "synthetic-embedding-model",
		Dimension:     3,
		TimeoutMS:     500,
		MaxRetryCount: 0,
		Status:        enums.StatusOk,
	}).Error; err != nil {
		t.Fatalf("create synthetic embedding config: %v", err)
	}

	results, trace, err := (&retrieve{}).searchKnowledgeBaseVectors(context.Background(), RetrieveRequest{Query: "synthetic question"}, []models.KnowledgeBase{{ID: 17}})
	if err == nil {
		t.Fatal("searchKnowledgeBaseVectors() error = nil, want embedding dependency failure")
	}
	if !strings.Contains(err.Error(), "failed to generate query embedding") {
		t.Fatalf("unexpected retrieval failure: %v", err)
	}
	if results != nil {
		t.Fatalf("failed query embedding returned search results: %#v", results)
	}
	if trace == nil {
		t.Fatal("searchKnowledgeBaseVectors() trace = nil, want failure trace")
	}

	vectors, chunks, dimension, err := Index.prepareDocumentVectors(context.Background(), models.KnowledgeBase{ID: 17}, models.KnowledgeDocument{ID: 23, Title: "synthetic document"}, []ragchunk.ChunkResult{{ChunkNo: 1, Content: "synthetic chunk"}})
	if err == nil {
		t.Fatal("prepareDocumentVectors() error = nil, want embedding dependency failure")
	}
	if vectors != nil || chunks != nil || dimension != 0 {
		t.Fatalf("failed embedding produced index output: vectors=%#v chunks=%#v dimension=%d", vectors, chunks, dimension)
	}
}

func TestIndexDocumentMarksFailedWhenEmbeddingFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid synthetic request", http.StatusBadRequest)
			return
		}
		if request.Input == "synthetic retrieval query" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"model":"synthetic-embedding-model","usage":{"prompt_tokens":1,"total_tokens":1}}`)
			return
		}
		http.Error(w, `{"error":{"message":"synthetic unavailable"}}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open synthetic indexing database: %v", err)
	}
	closeEmbeddingFaultTestDB(t, db)
	if err := db.AutoMigrate(&models.AIConfig{}, &models.KnowledgeBase{}, &models.KnowledgeDocument{}, &models.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate synthetic indexing tables: %v", err)
	}
	previousDB := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() { sqls.SetDB(previousDB) })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve synthetic Qdrant endpoint: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release synthetic Qdrant endpoint: %v", err)
	}
	if err := vectordb.Init(&config.VectorDBConfig{
		Type:   "qdrant",
		Qdrant: config.QdrantVectorDBConfig{Host: "127.0.0.1", GrpcPort: port},
	}); err != nil {
		t.Fatalf("initialize isolated Qdrant client: %v", err)
	}
	t.Cleanup(func() {
		if err := vectordb.Close(); err != nil {
			t.Errorf("close isolated Qdrant client: %v", err)
		}
	})

	if err := db.Create(&models.AIConfig{
		Name:          "synthetic embedding provider",
		Provider:      enums.AIProviderOpenAI,
		BaseURL:       server.URL + "/v1",
		APIKey:        "synthetic-embedding-key",
		ModelType:     enums.AIModelTypeEmbedding,
		ModelName:     "synthetic-embedding-model",
		Dimension:     3,
		TimeoutMS:     500,
		MaxRetryCount: 0,
		Status:        enums.StatusOk,
	}).Error; err != nil {
		t.Fatalf("create synthetic embedding config: %v", err)
	}
	knowledgeBase := models.KnowledgeBase{Name: "synthetic indexing KB", KnowledgeType: "document", Status: enums.StatusOk}
	if err := db.Create(&knowledgeBase).Error; err != nil {
		t.Fatalf("create synthetic knowledge base: %v", err)
	}
	document := models.KnowledgeDocument{
		KnowledgeBaseID: knowledgeBase.ID,
		Title:           "synthetic failure document",
		ContentType:     enums.KnowledgeDocumentContentTypeMarkdown,
		Content:         "# Synthetic heading\n\nSynthetic document content.",
		Status:          enums.StatusOk,
		IndexStatus:     enums.KnowledgeDocumentIndexStatusPending,
	}
	if err := db.Create(&document).Error; err != nil {
		t.Fatalf("create synthetic knowledge document: %v", err)
	}

	if err := Index.IndexDocument(t.Context(), document); err == nil {
		t.Fatal("IndexDocument() error = nil, want synthetic embedding failure")
	}
	var persisted models.KnowledgeDocument
	if err := db.First(&persisted, document.ID).Error; err != nil {
		t.Fatalf("reload synthetic knowledge document: %v", err)
	}
	if persisted.IndexStatus != enums.KnowledgeDocumentIndexStatusFailed {
		t.Fatalf("document index status = %q, want failed", persisted.IndexStatus)
	}
	if persisted.IndexedAt != nil {
		t.Fatalf("failed document retained indexed_at: %v", persisted.IndexedAt)
	}
	if !strings.Contains(persisted.IndexError, "failed to generate embedding") {
		t.Fatalf("document index error = %q, want embedding failure", persisted.IndexError)
	}
	var chunkCount int64
	if err := db.Model(&models.KnowledgeChunk{}).Where("document_id = ?", document.ID).Count(&chunkCount).Error; err != nil {
		t.Fatalf("count synthetic document chunks: %v", err)
	}
	if chunkCount != 0 {
		t.Fatalf("failed indexing persisted %d chunk rows, want 0", chunkCount)
	}

	retrievalCtx, cancelRetrieval := context.WithTimeout(t.Context(), time.Second)
	defer cancelRetrieval()
	startedAt := time.Now()
	results, trace, err := Retrieve.RetrieveWithTrace(retrievalCtx, RetrieveRequest{
		Query:            "synthetic retrieval query",
		KnowledgeBaseIDs: []int64{knowledgeBase.ID},
	})
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("failed retrieval was not bounded by context: %s", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "failed to search vectors") {
		t.Fatalf("retrieval error = %v, want the Qdrant dependency failure", err)
	}
	if results != nil {
		t.Fatalf("Qdrant failure returned successful empty/partial results: %#v", results)
	}
	if trace == nil {
		t.Fatal("failed retrieval trace = nil, want dependency failure trace")
	}
}

func closeEmbeddingFaultTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get synthetic embedding SQL database: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close synthetic embedding SQL database: %v", err)
		}
	})
}
