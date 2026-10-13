package services

import (
	"reflect"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"gorm.io/gorm"
)

// Restoring external-ID routing must leak A's frame to B in this fixture.
func TestCustomerPublicationIdentityCollisionDoesNotFanoutByExternalID(t *testing.T) {
	if runEmployeePublicationSubprocess(t) {
		return
	}
	db, svc := setupEmployeePublicationTest(t)
	externalA, externalB := createCustomerPublicationCollision(t, db, "synthetic-publication-collision")
	conversation := &models.Conversation{ID: 501, CustomerID: 41, Status: enums.IMConversationStatusAIServing, LastMessageSummary: "synthetic-owner-A-marker"}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatal(err)
	}
	a := captureCustomerRealtimeSession(t, svc, "customer A", 41, &externalA)
	b := captureCustomerRealtimeSession(t, svc, "customer B", 42, &externalB)
	// Only this regression fixture registers the formerly shared destination.
	legacyTopic := "guest:synthetic-publication-collision"
	svc.manager.Subscribe(a, []string{legacyTopic})
	svc.manager.Subscribe(b, []string{legacyTopic})
	legacyOnly := captureEmployeeRealtimeSession(t, svc, "legacy only", nil, legacyTopic)
	legacyOnly.Role = realtimeRoleUser
	// This witness can receive only through A's customer destination.
	customerOnly := captureCustomerRealtimeSession(t, svc, "customer destination witness", 41, &externalA)

	svc.PublishConversationChanged(conversation, enums.IMRealtimeEventConversationUpdated)
	event := requireCapturedRealtimeEvent(t, a, enums.IMRealtimeEventConversationUpdated)
	requireNoCapturedRealtimeEvent(t, a)
	if len(b.Send) != 0 {
		leaked := requireCapturedRealtimeEvent(t, b, enums.IMRealtimeEventConversationUpdated)
		t.Fatalf("external-ID collision delivered A event to B: A customerID=41 B customerID=42 shared destination=%q eventId=%q marker=%q", legacyTopic, leaked.EventID, leaked.Data["lastMessageSummary"])
	}
	requireNoCapturedRealtimeEvent(t, b)
	requireNoCapturedRealtimeEvent(t, legacyOnly)
	witness := requireCapturedRealtimeEvent(t, customerOnly, enums.IMRealtimeEventConversationUpdated)
	if !reflect.DeepEqual(event, witness) {
		t.Fatal("A customer destination did not receive the same original event")
	}
	if event.Topic != "conversation:501" || event.Data["lastMessageSummary"] != "synthetic-owner-A-marker" {
		t.Fatalf("customer delivery changed event envelope or marker: topic=%q marker=%q", event.Topic, event.Data["lastMessageSummary"])
	}
	requireNoCapturedRealtimeEvent(t, customerOnly)
}

func createCustomerPublicationCollision(t *testing.T, db *gorm.DB, externalID string) (openidentity.ExternalUser, openidentity.ExternalUser) {
	t.Helper()
	externalA := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: externalID, ExternalName: "synthetic-customer-A"}
	externalB := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceUser, ExternalID: externalID, ExternalName: "synthetic-customer-B"}
	for i, external := range []openidentity.ExternalUser{externalA, externalB} {
		customerID := int64(41 + i)
		if err := db.Create(&models.Customer{ID: customerID, Name: external.ExternalName, Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.CustomerIdentity{CustomerID: customerID, ExternalSource: external.ExternalSource, ExternalID: external.ExternalID, Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return externalA, externalB
}
