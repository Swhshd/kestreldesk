package storage

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
)

type UploadInfo struct {
	Prefix    string
	Filename  string
	FileSize  int64
	MimeType  string
	Principal *dto.AuthPrincipal
}

type StoredFile struct {
	Provider   enums.AssetProvider
	StorageKey string
	URL        string
	Filename   string
	FileSize   int64
	MimeType   string
}
