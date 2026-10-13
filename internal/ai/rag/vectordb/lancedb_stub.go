//go:build !lancedb

package vectordb

// This file has been modified for the KestrelDesk project.

import (
	"fmt"

	"github.com/Swhshd/kestreldesk/internal/pkg/config"
)

func NewLanceDBProvider(_ *config.LanceDBVectorDBConfig) (Provider, error) {
	return nil, fmt.Errorf("LanceDB provider is not built. Rebuild with -tags lancedb and configure LanceDB native libraries")
}
