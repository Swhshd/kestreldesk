package services

// This file has been modified for the KestrelDesk project.

import (
	"encoding/json"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/Swhshd/kestreldesk/internal/services/storage"
	"github.com/mlogclub/simple/sqls"
	"strings"
)

type imMessageAssetPayload struct {
	AssetID    string              `json:"assetId"`
	Provider   enums.AssetProvider `json:"provider,omitempty"`
	StorageKey string              `json:"storageKey,omitempty"`
	Filename   string              `json:"filename,omitempty"`
	FileSize   int64               `json:"fileSize,omitempty"`
	MimeType   string              `json:"mimeType,omitempty"`
	URL        string              `json:"url,omitempty"`
}

func parseIMMessageAssetPayload(payload string) (*imMessageAssetPayload, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil, errorsx.InvalidParamI18n("error.e0346")
	}
	ret := &imMessageAssetPayload{}
	if err := json.Unmarshal([]byte(payload), ret); err != nil {
		return nil, errorsx.InvalidParamI18n("error.e0344")
	}
	ret.AssetID = strings.TrimSpace(ret.AssetID)
	ret.Provider = enums.AssetProvider(strings.TrimSpace(string(ret.Provider)))
	ret.StorageKey = strings.TrimSpace(ret.StorageKey)
	if ret.AssetID == "" {
		return nil, errorsx.InvalidParamI18n("error.e0345")
	}
	return ret, nil
}

func buildIMMessageAssetPayload(asset *models.Asset) (string, error) {
	if asset == nil {
		return "", errorsx.InvalidParamI18n("error.e0342")
	}
	payload, err := json.Marshal(imMessageAssetPayload{
		AssetID:    asset.AssetID,
		Provider:   asset.Provider,
		StorageKey: asset.StorageKey,
		Filename:   asset.Filename,
		FileSize:   asset.FileSize,
		MimeType:   asset.MimeType,
	})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func buildIMMessageAssetPayloadForResponse(payload string) string {
	assetPayload, err := parseIMMessageAssetPayload(payload)
	if err != nil {
		return strings.TrimSpace(payload)
	}
	assetPayload = hydrateIMMessageAssetPayload(assetPayload)
	if assetPayload.Provider != "" && assetPayload.StorageKey != "" {
		if provider, err := storage.NewProvider(assetPayload.Provider); err == nil {
			assetPayload.URL = provider.GetSignedURL(assetPayload.StorageKey)
		}
	}
	data, err := json.Marshal(assetPayload)
	if err != nil {
		return strings.TrimSpace(payload)
	}
	return string(data)
}

func hydrateIMMessageAssetPayload(payload *imMessageAssetPayload) *imMessageAssetPayload {
	if payload == nil {
		return nil
	}
	if payload.Provider != "" && payload.StorageKey != "" {
		return payload
	}
	if payload.AssetID == "" {
		return payload
	}
	asset := AssetService.GetByAssetID(payload.AssetID)
	if asset == nil {
		return payload
	}
	if payload.Provider == "" {
		payload.Provider = asset.Provider
	}
	if payload.StorageKey == "" {
		payload.StorageKey = strings.TrimSpace(asset.StorageKey)
	}
	if payload.Filename == "" {
		payload.Filename = strings.TrimSpace(asset.Filename)
	}
	if payload.FileSize <= 0 {
		payload.FileSize = asset.FileSize
	}
	if payload.MimeType == "" {
		payload.MimeType = strings.TrimSpace(asset.MimeType)
	}
	return payload
}

func hydrateIMMessageAssetPayloadForOutboundDispatch(payload *imMessageAssetPayload) (*imMessageAssetPayload, error) {
	if payload == nil {
		return nil, nil
	}
	if payload.Provider != "" && payload.StorageKey != "" {
		return payload, nil
	}
	if payload.AssetID == "" {
		return payload, nil
	}
	asset, err := repositories.AssetRepository.GetByAssetIDForOutboundDispatch(sqls.DB(), payload.AssetID)
	if err != nil {
		return payload, err
	}
	if payload.Provider == "" {
		payload.Provider = asset.Provider
	}
	if payload.StorageKey == "" {
		payload.StorageKey = strings.TrimSpace(asset.StorageKey)
	}
	if payload.Filename == "" {
		payload.Filename = strings.TrimSpace(asset.Filename)
	}
	if payload.FileSize <= 0 {
		payload.FileSize = asset.FileSize
	}
	if payload.MimeType == "" {
		payload.MimeType = strings.TrimSpace(asset.MimeType)
	}
	return payload, nil
}

func outboundMessageAssetPayload(message *models.Message) (*imMessageAssetPayload, error) {
	if message == nil || (message.MessageType != enums.IMMessageTypeImage && message.MessageType != enums.IMMessageTypeAttachment) {
		return nil, nil
	}
	payload, err := parseIMMessageAssetPayload(message.Payload)
	if err != nil || payload == nil {
		return nil, nil
	}
	return hydrateIMMessageAssetPayloadForOutboundDispatch(payload)
}

func validateConversationAsset(asset *models.Asset, conversationID int64, messageType enums.IMMessageType) error {
	if asset == nil {
		return errorsx.InvalidParamI18n("error.e0342")
	}
	if asset.Status != enums.AssetStatusSuccess {
		return errorsx.InvalidParamI18n("error.e0343")
	}
	return nil
}
