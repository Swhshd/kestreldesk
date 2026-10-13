package services

// This file has been modified for the KestrelDesk project.

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"

	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Wrong ID authorization, bypassing ownership on duplicate sends, or changing
// sender/read metadata must fail these assertions on persisted behavior.
func TestVerifiedCustomerMessageAuthority(t *testing.T) {
	db := setupMessageWelcomeTestDB(t)
	agent := createWelcomeTestAIAgent(t, db, "")
	ext := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "X", ExternalName: "B"}
	for _, id := range []int64{41, 42} {
		if err := db.Create(&models.Customer{ID: id, Name: "synthetic"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.CustomerIdentity{CustomerID: id, ExternalSource: enums.ExternalSourceGuest, ExternalID: "X"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	a := &models.Conversation{CustomerID: 41, AIAgentID: agent.ID, Status: enums.IMConversationStatusAIServing, CustomerUnreadCount: 1}
	b := &models.Conversation{CustomerID: 42, AIAgentID: agent.ID, Status: enums.IMConversationStatusAIServing, CustomerUnreadCount: 1}
	for _, conv := range []*models.Conversation{a, b} {
		if err := db.Create(conv).Error; err != nil {
			t.Fatal(err)
		}
	}
	previousHook := TriggerAIReplyAsyncHook
	TriggerAIReplyAsyncHook = func(models.Conversation, models.Message) {}
	t.Cleanup(func() { TriggerAIReplyAsyncHook = previousHook })
	for _, tc := range []struct {
		id  int64
		ext *openidentity.ExternalUser
	}{{0, &ext}, {-1, &ext}, {41, &ext}, {42, nil}, {42, &openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest}}, {42, &openidentity.ExternalUser{ExternalID: "X"}}} {
		if _, err := MessageService.ValidateVerifiedCustomerSender(b.ID, tc.id, tc.ext); err == nil {
			t.Errorf("invalid verified sender accepted id=%d", tc.id)
		}
	}
	m, err := MessageService.SendVerifiedCustomerMessageWithRequestID(b.ID, 42, "own-id", "", "  hello B  ", "", ext, "trace-B")
	if err != nil {
		t.Fatal(err)
	}
	if m.Content != "hello B" || m.RequestID != "trace-B" || m.SenderID != 0 || m.SenderType != enums.IMSenderTypeCustomer || m.MessageType != enums.IMMessageTypeText || m.CreateUserID != 0 || m.CreateUserName != "B" {
		t.Fatalf("own send semantics changed: %+v", m)
	}
	again, err := MessageService.SendVerifiedCustomerMessageWithRequestID(b.ID, 42, "own-id", enums.IMMessageTypeText, "replacement", "", ext, "different-trace")
	if err != nil || again == nil || again.ID != m.ID || again.Content != "hello B" || again.Payload != m.Payload || again.RequestID != "trace-B" || again.SenderID != 0 || again.SenderType != enums.IMSenderTypeCustomer || again.CreateUserName != "B" {
		t.Error("idempotent own send changed persisted message")
	}
	if _, err := MessageService.SendVerifiedCustomerMessageWithRequestID(b.ID, 41, "own-id", enums.IMMessageTypeText, "foreign", "", ext, "foreign-trace"); err == nil {
		t.Error("cross ID returned existing message before ownership")
	}
	var event models.ConversationEventLog
	if err := db.Where("conversation_id = ?", b.ID).First(&event).Error; err != nil || event.RequestID != "trace-B" {
		t.Error("send trace event changed")
	}
	markers := []models.Message{{ConversationID: a.ID, ClientMsgID: "A-marker", SenderType: enums.IMSenderTypeAI, MessageType: enums.IMMessageTypeText, Content: "A", SendStatus: enums.IMMessageStatusSent}, {ConversationID: b.ID, ClientMsgID: "B-marker", SenderType: enums.IMSenderTypeAI, MessageType: enums.IMMessageTypeText, Content: "B", SendStatus: enums.IMMessageStatusSent}}
	if err := db.Create(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if err := ConversationService.MarkVerifiedCustomerConversationReadToMessage(a.ID, markers[0].ID, 42, &ext); err == nil {
		t.Error("B read A accepted")
	}
	if err := ConversationService.MarkVerifiedCustomerConversationReadToMessage(b.ID, markers[1].ID, 42, &ext); err != nil {
		t.Fatal(err)
	}
	var states []models.ConversationReadState
	if err := db.Find(&states).Error; err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ConversationID != b.ID || states[0].LastReadMessageID != markers[1].ID || states[0].ReaderID != 0 || states[0].ExternalReaderID != "X" || states[0].UpdateUserName != "B" {
		t.Errorf("read metadata escaped conversation B: %+v", states)
	}
	if err := db.Model(b).Update("status", enums.IMConversationStatusClosed).Error; err != nil {
		t.Fatal(err)
	}
	_, err = MessageService.SendVerifiedCustomerMessageWithRequestID(b.ID, 42, "own-id", enums.IMMessageTypeText, "closed", "", ext, "")
	var appErr *errorsx.I18nError
	if !errors.As(err, &appErr) || appErr.Key != "error.e0119" {
		t.Errorf("closed conversation no longer rejected: %v", err)
	}
}

func TestLegacyGuestMessageRejected(t *testing.T) {
	db := setupMessageWelcomeTestDB(t)
	if err := db.AutoMigrate(&models.ConversationAssignment{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Customer{ID: 41}).Error; err != nil {
		t.Fatal(err)
	}
	ext := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "X"}
	if err := db.Create(&models.CustomerIdentity{CustomerID: 41, ExternalSource: ext.ExternalSource, ExternalID: "X"}).Error; err != nil {
		t.Fatal(err)
	}
	conv := &models.Conversation{CustomerID: 41, Status: enums.IMConversationStatusAIServing}
	if err := db.Create(conv).Error; err != nil {
		t.Fatal(err)
	}
	if ConversationService.IsCustomerConversationOwner(conv, ext) {
		t.Error("legacy guest owner guessed mapping")
	}
	if _, err := MessageService.ValidateConversationSender(conv.ID, enums.IMSenderTypeCustomer, nil, &ext); err == nil {
		t.Error("legacy guest sender accepted")
	}
	if _, err := MessageService.SendCustomerMessage(conv.ID, "legacy", enums.IMMessageTypeText, "legacy", "", ext); err == nil {
		t.Error("legacy guest send accepted")
	}
	if _, err := MessageService.SendCustomerMessageWithRequestID(conv.ID, "legacy-trace", enums.IMMessageTypeText, "legacy", "", ext, "trace"); err == nil {
		t.Error("legacy guest send with request accepted")
	}
	if err := ConversationService.MarkCustomerConversationReadToMessage(conv.ID, 0, &ext); err == nil {
		t.Error("legacy guest read accepted")
	}
	if err := ConversationService.CloseCustomerConversation(conv.ID, ext); err == nil {
		t.Error("legacy guest close accepted")
	}
	var after models.Conversation
	if err := db.First(&after, conv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != enums.IMConversationStatusAIServing {
		t.Error("legacy guest mutated conversation")
	}
}

func TestAllowAIMessageOnPendingHandoff(t *testing.T) {
	conversation := &models.Conversation{
		Status:            enums.IMConversationStatusPending,
		CurrentAssigneeID: 0,
		HandoffAt:         ptrTime(time.Now()),
	}
	if !MessageService.allowAIMessageOnPendingHandoff(conversation) {
		t.Fatalf("expected pending handoff conversation to allow ai handoff notice")
	}

	conversation.Status = enums.IMConversationStatusAIServing
	if MessageService.allowAIMessageOnPendingHandoff(conversation) {
		t.Fatalf("expected ai serving conversation not to use pending handoff allowance")
	}
}

func ptrTime(v time.Time) *time.Time {
	return &v
}

func setupMessageWelcomeTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dbName := "message_welcome_test_" + strings.NewReplacer("/", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			TablePrefix:   "t_",
			SingularTable: true,
		},
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlite db: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("close sqlite db: %v", err)
		}
	})
	if err := db.AutoMigrate(
		&models.AIAgent{},
		&models.Channel{},
		&models.ChannelMessageOutbox{},
		&models.Customer{},
		&models.CustomerIdentity{},
		&models.Conversation{},
		&models.ConversationParticipant{},
		&models.ConversationReadState{},
		&models.ConversationEventLog{},
		&models.Message{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	sqls.SetDB(db)
	return db
}

func createWelcomeTestAIAgent(t *testing.T, db *gorm.DB, welcomeMessage string) *models.AIAgent {
	t.Helper()

	now := time.Now()
	aiAgent := &models.AIAgent{
		Name:           "welcome-test-agent",
		Status:         enums.StatusOk,
		ServiceMode:    enums.IMConversationServiceModeAIOnly,
		WelcomeMessage: welcomeMessage,
		AuditFields: models.AuditFields{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	if err := db.Create(aiAgent).Error; err != nil {
		t.Fatalf("create ai agent: %v", err)
	}
	return aiAgent
}

func welcomeTestExternalUser(id string) openidentity.ExternalUser {
	return openidentity.ExternalUser{
		ExternalSource: enums.ExternalSourceUser,
		ExternalID:     id,
		ExternalName:   "访客" + id,
	}
}

func createMessageTestConversation(t *testing.T, db *gorm.DB, aiAgentID int64) *models.Conversation {
	t.Helper()
	now := time.Now()
	conversation := &models.Conversation{
		CustomerID:   1,
		ChannelID:    11,
		AIAgentID:    aiAgentID,
		Status:       enums.IMConversationStatusAIServing,
		LastActiveAt: now,
		AuditFields: models.AuditFields{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	return conversation
}

func workflowTestAIPrincipal() *dto.AuthPrincipal {
	return &dto.AuthPrincipal{UserID: 0, Username: "AI", Nickname: "AI"}
}

func TestConversationCreateCreatesAIWelcomeMessage(t *testing.T) {
	db := setupMessageWelcomeTestDB(t)
	aiAgent := createWelcomeTestAIAgent(t, db, "  您好，请问有什么可以帮您？  ")

	conversation, err := ConversationService.Create(welcomeTestExternalUser("welcome-1"), 11, aiAgent.ID)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if conversation == nil {
		t.Fatalf("expected conversation")
	}

	var messages []models.Message
	if err := db.Find(&messages).Error; err != nil {
		t.Fatalf("find messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected exactly one welcome message, got %d", len(messages))
	}
	message := messages[0]
	if message.ConversationID != conversation.ID {
		t.Fatalf("expected conversation_id %d, got %d", conversation.ID, message.ConversationID)
	}
	if message.SenderType != enums.IMSenderTypeAI {
		t.Fatalf("expected sender type ai, got %q", message.SenderType)
	}
	if message.SenderID != aiAgent.ID {
		t.Fatalf("expected sender id %d, got %d", aiAgent.ID, message.SenderID)
	}
	if message.MessageType != enums.IMMessageTypeText {
		t.Fatalf("expected message type text, got %q", message.MessageType)
	}
	if message.Content != "您好，请问有什么可以帮您？" {
		t.Fatalf("expected trimmed welcome content, got %q", message.Content)
	}
	if message.SendStatus != enums.IMMessageStatusSent {
		t.Fatalf("expected sent status, got %d", message.SendStatus)
	}

	var updated models.Conversation
	if err := db.First(&updated, conversation.ID).Error; err != nil {
		t.Fatalf("find conversation: %v", err)
	}
	if updated.LastMessageID != message.ID {
		t.Fatalf("expected last message id %d, got %d", message.ID, updated.LastMessageID)
	}
	if updated.LastMessageSummary != "您好，请问有什么可以帮您？" {
		t.Fatalf("expected last message summary, got %q", updated.LastMessageSummary)
	}
	if updated.CustomerUnreadCount != 1 {
		t.Fatalf("expected customer unread count 1, got %d", updated.CustomerUnreadCount)
	}
	if updated.AgentUnreadCount != 0 {
		t.Fatalf("expected agent unread count 0, got %d", updated.AgentUnreadCount)
	}
}

func TestSendCustomerMessageStoresRequestIDOnMessageAndEvent(t *testing.T) {
	db := setupMessageWelcomeTestDB(t)
	aiAgent := createWelcomeTestAIAgent(t, db, "")
	external := welcomeTestExternalUser("trace-user")
	conversation, err := ConversationService.Create(external, 11, aiAgent.ID)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	message, err := MessageService.SendCustomerMessageWithRequestID(
		conversation.ID,
		"client-msg-trace",
		enums.IMMessageTypeText,
		"hello",
		"",
		external,
		"trace-123",
	)
	if err != nil {
		t.Fatalf("SendCustomerMessageWithRequestID() error = %v", err)
	}
	if message.RequestID != "trace-123" {
		t.Fatalf("message.RequestID=%q want %q", message.RequestID, "trace-123")
	}

	var event models.ConversationEventLog
	if err := db.Where("conversation_id = ?", conversation.ID).Order("id DESC").First(&event).Error; err != nil {
		t.Fatalf("find event: %v", err)
	}
	if event.RequestID != "trace-123" {
		t.Fatalf("event.RequestID=%q want %q", event.RequestID, "trace-123")
	}
}

func TestSendCustomerMessagesConcurrentlyAssignsUniqueIDs(t *testing.T) {
	db := setupMessageWelcomeTestDB(t)
	aiAgent := createWelcomeTestAIAgent(t, db, "")
	external := welcomeTestExternalUser("concurrent-user")
	conversation, err := ConversationService.Create(external, 11, aiAgent.ID)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	const messageCount = 10
	var wg sync.WaitGroup
	errCh := make(chan error, messageCount)
	for i := 0; i < messageCount; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := MessageService.SendCustomerMessageWithRequestID(
				conversation.ID,
				fmt.Sprintf("client-msg-concurrent-%d", i),
				enums.IMMessageTypeText,
				"hello concurrent",
				"",
				external,
				"trace-concurrent",
			)
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("SendCustomerMessageWithRequestID() concurrent error = %v", err)
		}
	}

	var messages []models.Message
	if err := db.
		Where("conversation_id = ? AND sender_type = ?", conversation.ID, enums.IMSenderTypeCustomer).
		Order("id ASC").
		Find(&messages).Error; err != nil {
		t.Fatalf("find messages: %v", err)
	}
	if len(messages) != messageCount {
		t.Fatalf("expected %d customer messages, got %d", messageCount, len(messages))
	}
	seen := make(map[int64]struct{}, messageCount)
	for _, message := range messages {
		if message.ID <= 0 {
			t.Fatalf("expected persisted message id, got %d", message.ID)
		}
		if _, ok := seen[message.ID]; ok {
			t.Fatalf("duplicate message id %d", message.ID)
		}
		seen[message.ID] = struct{}{}
	}
}

func TestUnreadCountUsesLastReadMessageID(t *testing.T) {
	db := setupMessageWelcomeTestDB(t)
	aiAgent := createWelcomeTestAIAgent(t, db, "")
	conversation := createMessageTestConversation(t, db, aiAgent.ID)
	now := time.Now()

	messages := []models.Message{
		{
			ConversationID: conversation.ID,
			ClientMsgID:    "read-message",
			SenderType:     enums.IMSenderTypeCustomer,
			MessageType:    enums.IMMessageTypeText,
			Content:        "read",
			SendStatus:     enums.IMMessageStatusSent,
			SentAt:         &now,
			AuditFields:    models.AuditFields{CreatedAt: now, UpdatedAt: now},
		},
		{
			ConversationID: conversation.ID,
			ClientMsgID:    "unread-message-1",
			SenderType:     enums.IMSenderTypeCustomer,
			MessageType:    enums.IMMessageTypeText,
			Content:        "unread 1",
			SendStatus:     enums.IMMessageStatusSent,
			SentAt:         &now,
			AuditFields:    models.AuditFields{CreatedAt: now, UpdatedAt: now},
		},
		{
			ConversationID: conversation.ID,
			ClientMsgID:    "unread-message-2",
			SenderType:     enums.IMSenderTypeCustomer,
			MessageType:    enums.IMMessageTypeText,
			Content:        "unread 2",
			SendStatus:     enums.IMMessageStatusSent,
			SentAt:         &now,
			AuditFields:    models.AuditFields{CreatedAt: now, UpdatedAt: now},
		},
	}
	if err := db.Create(&messages).Error; err != nil {
		t.Fatalf("create messages: %v", err)
	}

	readState := &models.ConversationReadState{
		ConversationID:    conversation.ID,
		ReaderType:        enums.IMSenderTypeAgent,
		ReaderID:          1,
		LastReadMessageID: messages[0].ID,
		LastReadAt:        &now,
		AuditFields:       models.AuditFields{CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(readState).Error; err != nil {
		t.Fatalf("create read state: %v", err)
	}

	err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		count, err := ConversationService.countUnreadByState(ctx, conversation.ID, readState, enums.IMSenderTypeCustomer)
		if err != nil {
			return err
		}
		if count != 2 {
			t.Fatalf("unread count=%d want 2", count)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("count unread: %v", err)
	}
}

func TestSendAIMessageStoresWorkflowRunID(t *testing.T) {
	db := setupMessageWelcomeTestDB(t)
	aiAgent := createWelcomeTestAIAgent(t, db, "")
	conversation := createMessageTestConversation(t, db, aiAgent.ID)

	message, err := MessageService.SendAIMessageWithRequestIDAndWorkflowRunID(
		conversation.ID,
		aiAgent.ID,
		"ai-reply-workflow-1",
		enums.IMMessageTypeText,
		"AI reply",
		"",
		workflowTestAIPrincipal(),
		"trace-workflow-1",
		9988,
	)
	if err != nil {
		t.Fatalf("SendAIMessageWithRequestIDAndWorkflowRunID() error = %v", err)
	}
	if message.WorkflowRunID != 9988 {
		t.Fatalf("message.WorkflowRunID=%d want 9988", message.WorkflowRunID)
	}

	var stored models.Message
	if err := db.First(&stored, message.ID).Error; err != nil {
		t.Fatalf("find message: %v", err)
	}
	if stored.WorkflowRunID != 9988 {
		t.Fatalf("stored.WorkflowRunID=%d want 9988", stored.WorkflowRunID)
	}
}

func TestConversationCreateDoesNotDuplicateWelcomeMessageForExistingConversation(t *testing.T) {
	db := setupMessageWelcomeTestDB(t)
	aiAgent := createWelcomeTestAIAgent(t, db, "欢迎咨询")
	external := welcomeTestExternalUser("u-2")

	first, err := ConversationService.Create(external, 11, aiAgent.ID)
	if err != nil {
		t.Fatalf("create first conversation: %v", err)
	}
	second, err := ConversationService.Create(external, 11, aiAgent.ID)
	if err != nil {
		t.Fatalf("create second conversation: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected existing conversation id %d, got %d", first.ID, second.ID)
	}

	var count int64
	if err := db.Model(&models.Message{}).Where("conversation_id = ?", first.ID).Count(&count).Error; err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one welcome message, got %d", count)
	}
}

func TestConversationCreateSkipsBlankWelcomeMessage(t *testing.T) {
	db := setupMessageWelcomeTestDB(t)
	aiAgent := createWelcomeTestAIAgent(t, db, "   ")

	conversation, err := ConversationService.Create(welcomeTestExternalUser("blank-welcome-1"), 11, aiAgent.ID)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if conversation == nil {
		t.Fatalf("expected conversation")
	}

	var count int64
	if err := db.Model(&models.Message{}).Where("conversation_id = ?", conversation.ID).Count(&count).Error; err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no welcome messages, got %d", count)
	}

	var updated models.Conversation
	if err := db.First(&updated, conversation.ID).Error; err != nil {
		t.Fatalf("find conversation: %v", err)
	}
	if updated.LastMessageID != 0 {
		t.Fatalf("expected last message id 0, got %d", updated.LastMessageID)
	}
	if updated.CustomerUnreadCount != 0 {
		t.Fatalf("expected customer unread count 0, got %d", updated.CustomerUnreadCount)
	}
}

func TestConversationCreateWelcomeMessageDoesNotTriggerAIReplyHook(t *testing.T) {
	db := setupMessageWelcomeTestDB(t)
	aiAgent := createWelcomeTestAIAgent(t, db, "欢迎咨询")

	previousHook := TriggerAIReplyAsyncHook
	called := false
	TriggerAIReplyAsyncHook = func(conversation models.Conversation, message models.Message) {
		called = true
	}
	t.Cleanup(func() {
		TriggerAIReplyAsyncHook = previousHook
	})

	if _, err := ConversationService.Create(welcomeTestExternalUser("hook-welcome-1"), 11, aiAgent.ID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if called {
		t.Fatalf("expected welcome message not to trigger ai reply hook")
	}
}
