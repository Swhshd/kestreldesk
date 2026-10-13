package services

import (
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/silenceper/wechat/v2/work/kf/syncmsg"
)

func TestWxWorkDelayedFailureCallbackDoesNotOverwriteNewDispatch(t *testing.T) {
	db := setupChannelOutboxTransitionTestDB(t)
	if err := db.AutoMigrate(
		&models.AIAgent{},
		&models.Channel{},
		&models.Customer{},
		&models.CustomerIdentity{},
		&models.Conversation{},
		&models.WxWorkKFConversation{},
		&models.WxWorkKFMessageRef{},
		&models.ConversationEventLog{},
	); err != nil {
		t.Fatalf("migrate synthetic WxWork callback tables: %v", err)
	}

	agent := &models.AIAgent{
		Name:        "synthetic callback agent",
		Status:      enums.StatusOk,
		ServiceMode: enums.IMConversationServiceModeAIOnly,
	}
	if err := db.Create(agent).Error; err != nil {
		t.Fatalf("create synthetic AI agent: %v", err)
	}
	channel := &models.Channel{
		Name:        "synthetic WxWork channel",
		ChannelType: string(enums.ChannelTypeWxWorkKF),
		ChannelID:   "synthetic-wxwork-callback-channel",
		AIAgentID:   agent.ID,
		ConfigJSON:  `{"openKfId":"synthetic-open-kf"}`,
		Status:      enums.StatusOk,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create synthetic WxWork channel: %v", err)
	}
	customer := &models.Customer{Name: "synthetic callback customer", Status: enums.StatusOk}
	if err := db.Create(customer).Error; err != nil {
		t.Fatalf("create synthetic customer: %v", err)
	}
	if err := db.Create(&models.CustomerIdentity{
		CustomerID:     customer.ID,
		ExternalSource: enums.ExternalSourceWxWorkKF,
		ExternalID:     "synthetic-external-user",
		Status:         enums.StatusOk,
	}).Error; err != nil {
		t.Fatalf("create synthetic customer identity: %v", err)
	}
	conversation := &models.Conversation{
		AIAgentID:    agent.ID,
		ChannelID:    channel.ID,
		CustomerID:   customer.ID,
		CustomerName: customer.Name,
		Status:       enums.IMConversationStatusActive,
		ServiceMode:  enums.IMConversationServiceModeAIOnly,
	}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatalf("create synthetic conversation: %v", err)
	}
	outbox := &models.ChannelMessageOutbox{
		ChannelType:        string(enums.ChannelTypeWxWorkKF),
		ConversationID:     conversation.ID,
		MessageID:          87001,
		SendStatus:         string(enums.ChannelMessageOutboxStatusSending),
		DispatchClaimToken: "claim-new-attempt-b",
	}
	if err := db.Create(outbox).Error; err != nil {
		t.Fatalf("create synthetic newer outbox attempt: %v", err)
	}
	oldAttemptRef := &models.WxWorkKFMessageRef{
		ConversationID:     conversation.ID,
		MessageID:          outbox.MessageID,
		WxMsgID:            "wx-msg-old-attempt-a",
		Direction:          string(enums.WxWorkKFMessageDirectionOut),
		DispatchClaimToken: "claim-old-attempt-a",
		SendStatus:         string(enums.WxWorkKFMessageSendStatusSent),
	}
	if err := db.Create(oldAttemptRef).Error; err != nil {
		t.Fatalf("create synthetic old outbound message reference: %v", err)
	}

	callback := syncmsg.Message{
		MsgID:          "wx-msg-callback-a",
		OpenKFID:       "synthetic-open-kf",
		ExternalUserID: "synthetic-external-user",
		EventType:      enums.WxWorkKFEventTypeMsgSendFail,
		OriginData:     []byte(`{"event":{"open_kfid":"synthetic-open-kf","external_userid":"synthetic-external-user","fail_msgid":"wx-msg-old-attempt-a","fail_type":10}}`),
	}
	if err := newWxWorkKFInboundService().handleMsgSendFailEvent(callback); err != nil {
		t.Fatalf("handle delayed failure callback for attempt A: %v", err)
	}

	var got models.ChannelMessageOutbox
	if err := db.First(&got, outbox.ID).Error; err != nil {
		t.Fatalf("reload outbox after delayed callback: %v", err)
	}
	if got.SendStatus != string(enums.ChannelMessageOutboxStatusSending) || got.DispatchClaimToken != "claim-new-attempt-b" {
		t.Fatalf("delayed callback overwrote newer dispatch: status=%q token=%q, want sending with claim-new-attempt-b", got.SendStatus, got.DispatchClaimToken)
	}
	var gotRef models.WxWorkKFMessageRef
	if err := db.First(&gotRef, oldAttemptRef.ID).Error; err != nil {
		t.Fatalf("reload old message ref after callback: %v", err)
	}
	if gotRef.SendStatus != string(enums.WxWorkKFMessageSendStatusFailed) {
		t.Fatalf("callback did not update its own message ref: send_status=%q, want failed", gotRef.SendStatus)
	}
	if err := db.Model(&models.ChannelMessageOutbox{}).Where("id = ?", outbox.ID).Updates(map[string]any{
		"send_status": string(enums.ChannelMessageOutboxStatusSent),
	}).Error; err != nil {
		t.Fatalf("simulate newer attempt B completing: %v", err)
	}
	if err := newWxWorkKFInboundService().handleMsgSendFailEvent(callback); err != nil {
		t.Fatalf("handle repeated delayed callback after attempt B completed: %v", err)
	}
	got = models.ChannelMessageOutbox{}
	if err := db.First(&got, outbox.ID).Error; err != nil {
		t.Fatalf("reload newer sent outbox after repeated callback: %v", err)
	}
	if got.SendStatus != string(enums.ChannelMessageOutboxStatusSent) || got.DispatchClaimToken != "claim-new-attempt-b" {
		t.Fatalf("delayed callback overwrote newer sent attempt: status=%q token=%q, want sent with claim-new-attempt-b", got.SendStatus, got.DispatchClaimToken)
	}

	currentAttempt := &models.ChannelMessageOutbox{
		ChannelType:        string(enums.ChannelTypeWxWorkKF),
		ConversationID:     conversation.ID,
		MessageID:          87002,
		SendStatus:         string(enums.ChannelMessageOutboxStatusSent),
		DispatchClaimToken: "claim-current-attempt-c",
	}
	if err := db.Create(currentAttempt).Error; err != nil {
		t.Fatalf("create current sent outbox attempt: %v", err)
	}
	currentAttemptRef := &models.WxWorkKFMessageRef{
		ConversationID:     conversation.ID,
		MessageID:          currentAttempt.MessageID,
		WxMsgID:            "wx-msg-current-attempt-c",
		Direction:          string(enums.WxWorkKFMessageDirectionOut),
		DispatchClaimToken: currentAttempt.DispatchClaimToken,
		SendStatus:         string(enums.WxWorkKFMessageSendStatusSent),
	}
	if err := db.Create(currentAttemptRef).Error; err != nil {
		t.Fatalf("create current outbound message reference: %v", err)
	}
	currentCallback := callback
	currentCallback.MsgID = "wx-msg-callback-c"
	currentCallback.OriginData = []byte(`{"event":{"open_kfid":"synthetic-open-kf","external_userid":"synthetic-external-user","fail_msgid":"wx-msg-current-attempt-c","fail_type":10}}`)
	if err := newWxWorkKFInboundService().handleMsgSendFailEvent(currentCallback); err != nil {
		t.Fatalf("handle failure callback for current attempt C: %v", err)
	}
	got = models.ChannelMessageOutbox{}
	if err := db.First(&got, currentAttempt.ID).Error; err != nil {
		t.Fatalf("reload current outbox after callback: %v", err)
	}
	if got.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) || got.DispatchClaimToken != "" {
		t.Fatalf("matching callback did not fail its sent attempt: status=%q token=%q, want failed with cleared claim", got.SendStatus, got.DispatchClaimToken)
	}
}

func TestWxWorkOutboundRefsAreStoredForEachClaimAttempt(t *testing.T) {
	db := setupChannelOutboxTransitionTestDB(t)
	if err := db.AutoMigrate(&models.WxWorkKFMessageRef{}); err != nil {
		t.Fatalf("migrate synthetic WxWork message refs: %v", err)
	}

	conversation := &models.Conversation{ID: 87003}
	message := &models.Message{ID: 87003}
	mapping := &models.WxWorkKFConversation{OpenKfID: "synthetic-open-kf", ExternalUserID: "synthetic-external-user"}
	outbox := &models.ChannelMessageOutbox{MessageID: message.ID, Payload: "synthetic payload", DispatchClaimToken: "claim-attempt-a"}
	chunks := []wxWorkKFOutboundChunk{
		{MessageType: enums.IMMessageTypeText, Content: "first chunk"},
		{MessageType: enums.IMMessageTypeText, Content: "second chunk"},
	}
	createdAt := time.Now()
	attemptAIDs := []string{
		WxWorkKFOutboundService.buildOutboundClientMsgID(outbox.DispatchClaimToken, message.ID, 0),
		WxWorkKFOutboundService.buildOutboundClientMsgID(outbox.DispatchClaimToken, message.ID, 1),
	}
	if err := WxWorkKFOutboundService.createOutboundAttemptRefs(db, outbox, conversation, message, mapping, chunks, attemptAIDs, createdAt); err != nil {
		t.Fatalf("persist first attempt refs: %v", err)
	}
	outbox.DispatchClaimToken = "claim-attempt-b"
	attemptBIDs := []string{
		WxWorkKFOutboundService.buildOutboundClientMsgID(outbox.DispatchClaimToken, message.ID, 0),
		WxWorkKFOutboundService.buildOutboundClientMsgID(outbox.DispatchClaimToken, message.ID, 1),
	}
	if err := WxWorkKFOutboundService.createOutboundAttemptRefs(db, outbox, conversation, message, mapping, chunks, attemptBIDs, createdAt.Add(time.Second)); err != nil {
		t.Fatalf("persist retried attempt refs: %v", err)
	}

	wantTokens := map[string]string{
		attemptAIDs[0]: "claim-attempt-a",
		attemptAIDs[1]: "claim-attempt-a",
		attemptBIDs[0]: "claim-attempt-b",
		attemptBIDs[1]: "claim-attempt-b",
	}
	for wxMsgID, wantToken := range wantTokens {
		ref := WxWorkKFMessageRefService.GetByWxMsgID(wxMsgID)
		if ref == nil {
			t.Fatalf("missing provider message reference %q", wxMsgID)
		}
		if ref.DispatchClaimToken != wantToken {
			t.Errorf("provider message %q claim token = %q, want %q", wxMsgID, ref.DispatchClaimToken, wantToken)
		}
	}
}

func TestWxWorkOutboundClientMsgIDIsAttemptScopedAndWithinProviderLimit(t *testing.T) {
	service := newWxWorkKFOutboundService()
	attemptAChunk0 := service.buildOutboundClientMsgID("claim-attempt-a", 87004, 0)
	attemptAChunk0Again := service.buildOutboundClientMsgID("claim-attempt-a", 87004, 0)
	attemptAChunk1 := service.buildOutboundClientMsgID("claim-attempt-a", 87004, 1)
	attemptBChunk0 := service.buildOutboundClientMsgID("claim-attempt-b", 87004, 0)

	if attemptAChunk0 != attemptAChunk0Again {
		t.Fatalf("client msgid changed within one dispatch attempt: %q != %q", attemptAChunk0, attemptAChunk0Again)
	}
	if attemptAChunk0 == attemptAChunk1 {
		t.Fatalf("different chunks in one attempt share client msgid %q", attemptAChunk0)
	}
	if attemptAChunk0 == attemptBChunk0 {
		t.Fatalf("retry reused the previous attempt client msgid %q", attemptAChunk0)
	}
	for _, msgID := range []string{attemptAChunk0, attemptAChunk1, attemptBChunk0} {
		if len(msgID) > 32 {
			t.Errorf("client msgid length = %d, exceeds WxWork limit 32: %q", len(msgID), msgID)
		}
		for _, char := range msgID {
			if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
				t.Errorf("client msgid contains invalid provider character %q: %q", char, msgID)
				break
			}
		}
	}
}
