package chunk

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
)

type Provider interface {
	Name() string
	Supports(contentType enums.KnowledgeDocumentContentType) bool
	Chunk(ctx context.Context, req *ChunkRequest) ([]ChunkResult, error)
}
