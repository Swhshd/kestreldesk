package services

import (
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"

	"github.com/mlogclub/simple/sqls"
)

func TestVerifiedCustomerConversationMatching(t *testing.T) {
	previousDB := sqls.DB()
	db := setupMessageWelcomeTestDB(t)
	t.Cleanup(func() { sqls.SetDB(previousDB) })
	if err := db.AutoMigrate(&models.ConversationAssignment{}); err != nil {
		t.Fatal(err)
	}
	ai := createWelcomeTestAIAgent(t, db, "synthetic welcome")
	guest := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "X", ExternalName: "B renamed"}
	for _, id := range []int64{41, 42} {
		if err := db.Create(&models.Customer{ID: id, Name: "original", Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.CustomerIdentity{CustomerID: id, ExternalSource: enums.ExternalSourceGuest, ExternalID: "X"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	a := &models.Conversation{CustomerID: 41, CustomerName: "A", Status: enums.IMConversationStatusAIServing}
	if err := db.Create(a).Error; err != nil {
		t.Fatal(err)
	}
	b, err := ConversationService.CreateForCustomer(42, guest, 11, ai.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.CustomerID != 42 || b.ID == a.ID || b.CustomerName != "B renamed" {
		t.Fatalf("wrong B conversation: %+v", b)
	}
	reused, err := ConversationService.CreateForCustomer(42, guest, 99, ai.ID)
	if err != nil || reused == nil || reused.ID != b.ID || reused.ChannelID != 11 {
		t.Fatal("existing matching/name/channel behavior changed")
	}
	var count int64
	if err := db.Model(&models.Message{}).Where("conversation_id = ?", b.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("welcome messages=%d err=%v", count, err)
	}
	for _, tc := range []struct {
		conv *models.Conversation
		id   int64
		want bool
	}{{b, 42, true}, {a, 42, false}, {b, 0, false}, {b, -1, false}, {nil, 42, false}, {&models.Conversation{}, 0, false}} {
		if got := ConversationService.IsVerifiedCustomerConversationOwner(tc.conv, tc.id); got != tc.want {
			t.Errorf("ownership id=%d got=%v want=%v", tc.id, got, tc.want)
		}
	}
	if err := ConversationService.CloseVerifiedCustomerConversation(a.ID, 42); err == nil {
		t.Error("B closed A")
	}
	if ConversationService.Get(a.ID).Status != enums.IMConversationStatusAIServing {
		t.Error("denied close mutated A")
	}
	if err := ConversationService.CloseVerifiedCustomerConversation(b.ID, 0); err == nil {
		t.Error("zero customer closed B")
	}
	if err := ConversationService.CloseVerifiedCustomerConversation(b.ID, 42); err != nil {
		t.Fatal(err)
	}
	next, err := ConversationService.CreateForCustomer(42, guest, 11, ai.ID)
	if err != nil || next == nil || next.ID == b.ID || next.CustomerID != 42 {
		t.Fatal("closed B reused rather than new B")
	}
	var before int64
	if err := db.Model(&models.Conversation{}).Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id       int64
		external openidentity.ExternalUser
	}{{0, guest}, {-1, guest}, {99, guest}, {42, openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "other"}}, {42, openidentity.ExternalUser{ExternalSource: enums.ExternalSourceUser, ExternalID: "X"}}, {42, openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest}}, {42, openidentity.ExternalUser{ExternalID: "X"}}} {
		if conv, err := ConversationService.CreateForCustomer(tc.id, tc.external, 11, ai.ID); err == nil || conv != nil {
			t.Errorf("invalid binding accepted for id=%d", tc.id)
		}
	}
	if err := db.Model(&models.Customer{}).Where("id = ?", 42).Update("status", enums.StatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if conv, err := ConversationService.CreateForCustomer(42, guest, 11, ai.ID); err != nil || conv == nil || conv.ID != next.ID {
		t.Fatal("disabled customer eligibility changed")
	}
	if err := db.Model(&models.Customer{}).Where("id = ?", 42).Update("status", enums.StatusDeleted).Error; err != nil {
		t.Fatal(err)
	}
	if conv, err := ConversationService.CreateForCustomer(42, guest, 11, ai.ID); err == nil || conv != nil {
		t.Error("deleted customer accepted")
	}
	if err := db.Delete(&models.Customer{}, 41).Error; err != nil {
		t.Fatal(err)
	}
	if conv, err := ConversationService.CreateForCustomer(41, guest, 11, ai.ID); err == nil || conv != nil {
		t.Error("absent customer with mapping accepted")
	}
	var after int64
	if err := db.Model(&models.Conversation{}).Count(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("invalid binding wrote conversations: before=%d after=%d", before, after)
	}
}

func TestLegacyGuestConversationCreateRejected(t *testing.T) {
	previousDB := sqls.DB()
	db := setupMessageWelcomeTestDB(t)
	t.Cleanup(func() { sqls.SetDB(previousDB) })
	ai := createWelcomeTestAIAgent(t, db, "")
	guest := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "X"}
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		_, err := CustomerService.CreateFreshGuestCustomer(ctx, guest)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if conv, err := ConversationService.Create(guest, 11, ai.ID); err == nil || conv != nil {
		t.Error("legacy Create recovered guest by hint")
	}
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		_, err := CustomerService.EnsureExternalCustomer(ctx, guest)
		return err
	}); err == nil {
		t.Error("legacy customer wrapper recovered guest")
	}
	for _, source := range []enums.ExternalSource{enums.ExternalSourceUser, enums.ExternalSourceDiscord, enums.ExternalSourceSlack, enums.ExternalSourceTelegram, enums.ExternalSourceLark} {
		external := openidentity.ExternalUser{ExternalSource: source, ExternalID: "verified-source", ExternalName: "signed name"}
		first, err := ConversationService.Create(external, 11, ai.ID)
		if err != nil {
			t.Fatal(err)
		}
		second, err := ConversationService.Create(external, 11, ai.ID)
		if err != nil || second == nil || second.ID != first.ID {
			t.Errorf("non-guest reuse changed for %s", source)
		}
	}
}
