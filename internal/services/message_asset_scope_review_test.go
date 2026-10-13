package services

import (
	"fmt"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func setupMessageAssetScopeReviewDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := sqls.DB()
	previousConfig := config.GetCurrent()
	config.SetCurrent(&config.Config{})
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.User{}, &models.AgentTeam{}, &models.AgentProfile{}, &models.Asset{},
		&models.Customer{}, &models.CustomerIdentity{}, &models.Conversation{},
		&models.ConversationReadState{}, &models.ConversationEventLog{}, &models.Message{},
		&models.Channel{}, &models.ChannelMessageOutbox{},
	); err != nil {
		t.Fatal(err)
	}
	sqls.SetDB(db)
	previousWS := WsService
	WsService = newWsServiceForTest()
	t.Cleanup(func() {
		WsService = previousWS
		sqls.SetDB(previousDB)
		config.SetCurrent(previousConfig)
		raw, err := db.DB()
		if err == nil {
			_ = raw.Close()
		}
	})
	if err := db.Create(&models.AgentTeam{ID: 10, Name: "synthetic-team", Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		userID int64
		name   string
		typeID enums.UserType
	}{
		{101, "synthetic-agent", enums.UserTypeEmployee},
		{201, "synthetic-other-agent", enums.UserTypeEmployee},
		{301, "synthetic-customer", enums.UserTypeUser},
	} {
		if err := db.Create(&models.User{ID: item.userID, Username: item.name, UserType: item.typeID, Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
		if item.typeID == enums.UserTypeEmployee {
			if err := db.Create(&models.AgentProfile{UserID: item.userID, TeamID: 10, AgentCode: fmt.Sprintf("synthetic-agent-%d", item.userID), Status: enums.StatusOk}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	return db
}

func createReviewAsset(t *testing.T, db *gorm.DB, assetID string, createUserID int64) *models.Asset {
	t.Helper()
	item := &models.Asset{
		AssetID: assetID, Provider: enums.AssetProviderLocal, StorageKey: "synthetic/" + assetID,
		Filename: assetID + ".pdf", FileSize: 128, MimeType: "application/pdf", Status: enums.AssetStatusSuccess,
		AuditFields: models.AuditFields{CreateUserID: createUserID},
	}
	if err := db.Create(item).Error; err != nil {
		t.Fatal(err)
	}
	return item
}

func TestAgentMessageCannotReuseForeignAssetButOwnAssetStillWorks(t *testing.T) {
	db := setupMessageAssetScopeReviewDB(t)
	conversation := &models.Conversation{Status: enums.IMConversationStatusActive, ServiceMode: enums.IMConversationServiceModeHumanOnly, CurrentAssigneeID: 101, CurrentTeamID: 10}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatal(err)
	}
	foreign := createReviewAsset(t, db, "synthetic-foreign-agent-asset", 201)
	customerUpload := createReviewAsset(t, db, "synthetic-customer-upload", 301)
	operator := &dto.AuthPrincipal{UserID: 101, Username: "synthetic-agent", UserType: enums.UserTypeEmployee, Status: enums.StatusOk, Roles: []string{constants.RoleCodeCsUser}}
	for _, asset := range []*models.Asset{foreign, customerUpload} {
		payload := fmt.Sprintf(`{"assetId":%q}`, asset.AssetID)
		if _, err := MessageService.SendAgentMessage(conversation.ID, 101, "foreign-"+asset.AssetID, enums.IMMessageTypeAttachment, "", payload, operator); err == nil {
			t.Fatalf("agent sent attachment owned by user %d", asset.CreateUserID)
		}
	}
	var messageCount int64
	if err := db.Model(&models.Message{}).Where("conversation_id = ?", conversation.ID).Count(&messageCount).Error; err != nil {
		t.Fatal(err)
	}
	if messageCount != 0 {
		t.Fatalf("rejected foreign assets still wrote %d messages", messageCount)
	}

	own := createReviewAsset(t, db, "synthetic-own-agent-asset", 101)
	if _, err := MessageService.SendAgentMessage(conversation.ID, 101, "own-asset", enums.IMMessageTypeAttachment, "", fmt.Sprintf(`{"assetId":%q}`, own.AssetID), operator); err != nil {
		t.Fatalf("agent could not send its own attachment: %v", err)
	}
}

func TestAgentHTMLMessageCannotReuseForeignAssetButOwnAssetStillWorks(t *testing.T) {
	db := setupMessageAssetScopeReviewDB(t)
	conversation := &models.Conversation{Status: enums.IMConversationStatusActive, ServiceMode: enums.IMConversationServiceModeHumanOnly, CurrentAssigneeID: 101, CurrentTeamID: 10}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatal(err)
	}
	foreign := createReviewAsset(t, db, "synthetic-foreign-html-image", 201)
	customerUpload := createReviewAsset(t, db, "synthetic-customer-html-image", 301)
	operator := &dto.AuthPrincipal{UserID: 101, Username: "synthetic-agent", UserType: enums.UserTypeEmployee, Status: enums.StatusOk, Roles: []string{constants.RoleCodeCsUser}}
	for _, asset := range []*models.Asset{foreign, customerUpload} {
		html := fmt.Sprintf(`<p>synthetic <img data-asset-id="%s" data-provider="%s" data-storage-key="%s"></p>`, asset.AssetID, asset.Provider, asset.StorageKey)
		if _, err := MessageService.SendAgentMessage(conversation.ID, 101, "foreign-html-"+asset.AssetID, enums.IMMessageTypeHTML, html, "", operator); err == nil {
			t.Fatalf("agent sent HTML referencing an image owned by user %d", asset.CreateUserID)
		}
	}
	var messageCount int64
	if err := db.Model(&models.Message{}).Where("conversation_id = ?", conversation.ID).Count(&messageCount).Error; err != nil {
		t.Fatal(err)
	}
	if messageCount != 0 {
		t.Fatalf("rejected HTML assets still wrote %d messages", messageCount)
	}

	own := createReviewAsset(t, db, "synthetic-own-html-image", 101)
	ownHTML := fmt.Sprintf(`<p>synthetic <img data-asset-id="%s" data-provider="%s" data-storage-key="%s"></p>`, own.AssetID, own.Provider, own.StorageKey)
	if _, err := MessageService.SendAgentMessage(conversation.ID, 101, "own-html-asset", enums.IMMessageTypeHTML, ownHTML, "", operator); err != nil {
		t.Fatalf("agent could not send HTML with its own image: %v", err)
	}
}

func TestVerifiedCustomerCanStillSendItsOwnAttachment(t *testing.T) {
	db := setupMessageAssetScopeReviewDB(t)
	if err := db.Create(&models.Customer{ID: 42, Name: "synthetic-customer", Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	external := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceUser, ExternalID: "synthetic-customer-external", ExternalName: "synthetic-customer"}
	if err := db.Create(&models.CustomerIdentity{CustomerID: 42, ExternalSource: external.ExternalSource, ExternalID: external.ExternalID, Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	conversation := &models.Conversation{CustomerID: 42, Status: enums.IMConversationStatusAIServing}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatal(err)
	}
	asset := createReviewAsset(t, db, "synthetic-customer-owned-asset", 301)
	if _, err := MessageService.SendVerifiedCustomerMessageWithRequestID(conversation.ID, 42, "customer-asset", enums.IMMessageTypeAttachment, "", fmt.Sprintf(`{"assetId":%q}`, asset.AssetID), external, "synthetic-request"); err != nil {
		t.Fatalf("customer attachment path changed: %v", err)
	}
}

func TestCsUserCannotRecallWithDirectPermissionOverride(t *testing.T) {
	db := setupMessageAssetScopeReviewDB(t)
	conversation := &models.Conversation{Status: enums.IMConversationStatusActive, ServiceMode: enums.IMConversationServiceModeHumanOnly, CurrentAssigneeID: 101, CurrentTeamID: 10}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatal(err)
	}
	message := &models.Message{
		ConversationID: conversation.ID,
		ClientMsgID:    "synthetic-cs-user-recall-override",
		SenderType:     enums.IMSenderTypeAgent,
		SenderID:       101,
		MessageType:    enums.IMMessageTypeText,
		Content:        "synthetic recall target",
		SendStatus:     enums.IMMessageStatusSent,
	}
	if err := db.Create(message).Error; err != nil {
		t.Fatal(err)
	}
	operator := &dto.AuthPrincipal{
		UserID: 101, Username: "synthetic-agent", UserType: enums.UserTypeEmployee, Status: enums.StatusOk,
		Roles: []string{constants.RoleCodeCsUser}, Permissions: []string{constants.PermissionConversationRecall.Code},
	}
	if _, err := MessageService.RecallAgentMessage(message.ID, operator); err == nil {
		t.Fatal("cs_user with a direct recall permission override recalled an agent message")
	}
	var current models.Message
	if err := db.First(&current, message.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.SendStatus == enums.IMMessageStatusRecalled || current.RecalledAt != nil {
		t.Fatalf("denied cs_user recall mutated message: %+v", current)
	}
	var events int64
	if err := db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", conversation.ID).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("denied cs_user recall wrote %d conversation events", events)
	}
}

func TestRecallAgentMessageRechecksScopeInsideWriteTransaction(t *testing.T) {
	db := setupMessageAssetScopeReviewDB(t)
	conversation := &models.Conversation{Status: enums.IMConversationStatusActive, ServiceMode: enums.IMConversationServiceModeHumanOnly, CurrentAssigneeID: 101, CurrentTeamID: 10}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatal(err)
	}
	message := &models.Message{
		ConversationID: conversation.ID,
		ClientMsgID:    "synthetic-recall-scope-race",
		SenderType:     enums.IMSenderTypeAgent,
		SenderID:       101,
		MessageType:    enums.IMMessageTypeText,
		Content:        "synthetic recall target",
		SendStatus:     enums.IMMessageStatusSent,
	}
	if err := db.Create(message).Error; err != nil {
		t.Fatal(err)
	}
	operator := &dto.AuthPrincipal{UserID: 101, Username: "synthetic-agent", UserType: enums.UserTypeEmployee, Status: enums.StatusOk, Roles: []string{constants.RoleCodeCsTeamLeader}}
	conversationQueries := 0
	interleaved := false
	callbackName := "test:reassign_before_recall_transaction_" + fmt.Sprint(message.ID)
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if query.Statement.Table != "t_conversation" || interleaved {
			return
		}
		conversationQueries++
		if conversationQueries != 2 {
			return
		}
		interleaved = true
		if err := db.Model(&models.Conversation{}).Where("id = ?", conversation.ID).UpdateColumns(map[string]any{
			"current_team_id":     20,
			"current_assignee_id": 201,
		}).Error; err != nil {
			t.Errorf("interleave conversation reassignment: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	if _, err := MessageService.RecallAgentMessage(message.ID, operator); err == nil {
		t.Fatal("message recall succeeded after the conversation moved outside the operator's scope")
	}
	if !interleaved {
		t.Fatal("deterministic reassignment did not occur between validation and transaction")
	}
	var currentMessage models.Message
	if err := db.First(&currentMessage, message.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentMessage.SendStatus == enums.IMMessageStatusRecalled || currentMessage.RecalledAt != nil {
		t.Fatalf("stale-scope recall mutated message: %+v", currentMessage)
	}
	var events int64
	if err := db.Model(&models.ConversationEventLog{}).Where("conversation_id = ? AND event_type = ?", conversation.ID, enums.IMEventTypeMessageRecall).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("stale-scope recall wrote %d recall events", events)
	}
}
