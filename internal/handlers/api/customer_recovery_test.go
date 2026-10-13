package api

import (
	"reflect"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/Swhshd/kestreldesk/internal/services"

	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

func TestCustomerExchangeNoProofIsolation(t *testing.T) {
	db, router, channel, _ := newCustomerRecoveryFixture(t)
	a := customerRecoveryExchange(t, router, channel, "shared-guest", "Customer A", "", "")
	if a.ErrorCode != 0 || a.Data == nil || a.Data.Customer.ID <= 0 {
		t.Fatal("first actual exchange must create A")
	}
	var before models.Customer
	if err := db.First(&before, a.Data.Customer.ID).Error; err != nil {
		t.Fatal("read A before exchange")
	}
	var mappingBefore models.CustomerIdentity
	if err := db.Where("customer_id = ?", before.ID).First(&mappingBefore).Error; err != nil {
		t.Fatal("read A mapping")
	}
	b := customerRecoveryExchange(t, router, channel, "shared-guest", "Customer B", "", "")
	if b.ErrorCode != 0 || b.Data == nil {
		t.Fatal("second actual exchange must succeed")
	}
	if b.Data.Customer.ID == a.Data.Customer.ID {
		t.Errorf("no-proof exchange reused A (id=%d); must create distinct B", before.ID)
	}
	var after models.Customer
	if err := db.First(&after, before.ID).Error; err != nil {
		t.Fatal("read A after exchange")
	}
	if !reflect.DeepEqual(before, after) {
		t.Error("no-proof exchange mutated existing A")
	}
	var mappingAfter models.CustomerIdentity
	if err := db.First(&mappingAfter, mappingBefore.ID).Error; err != nil {
		t.Fatal("read preserved A mapping")
	}
	if !reflect.DeepEqual(mappingBefore, mappingAfter) {
		t.Error("no-proof exchange changed A mapping")
	}
}

func TestCustomerExchangeUserDomain(t *testing.T) {
	db, router, channel, _ := newCustomerRecoveryFixture(t)
	signUser := func(name string, expires time.Time) string {
		t.Helper()
		claims := openidentity.UserTokenClaims{UserID: "synthetic-user", Name: name, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(expires)}}
		raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(services.ChannelService.GetUserTokenSecret(channel)))
		if err != nil {
			t.Fatal("sign synthetic host user")
		}
		return raw
	}
	a := customerRecoveryExchange(t, router, channel, "shared-guest", "Untrusted guest name", "malformed-guest-proof", signUser("Host A", time.Now().Add(time.Hour)))
	if a.ErrorCode != 0 || a.Data == nil || a.Data.IdentityKey != "user:synthetic-user" || a.Data.Customer.Name != "Host A" {
		t.Fatal("verified host user must take precedence over guest header and display name")
	}
	conversation := &models.Conversation{CustomerID: a.Data.Customer.ID, CustomerName: "Host A", Status: enums.IMConversationStatusActive}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatal("seed user conversation")
	}
	b := customerRecoveryExchange(t, router, channel, "shared-guest", "Untrusted second name", "another-invalid-guest-proof", signUser("Host B", time.Now().Add(time.Hour)))
	if b.ErrorCode != 0 || b.Data == nil || b.Data.Customer.ID != a.Data.Customer.ID || b.Data.Customer.Name != "Host B" {
		t.Fatal("host user stable reuse or signed-name precedence changed")
	}
	var updated models.Conversation
	if err := db.First(&updated, conversation.ID).Error; err != nil {
		t.Fatal("read synchronized user conversation")
	}
	if updated.CustomerName != "Host B" {
		t.Error("host user name did not synchronize to conversation")
	}
	before := customerRecoveryCounts(t, db)
	for _, raw := range []string{"malformed-host-proof", signUser("Expired", time.Now().Add(-time.Hour))} {
		result := customerRecoveryExchange(t, router, channel, "shared-guest", "Guest", "", raw)
		if result.ErrorCode != 3000 || result.Data != nil {
			t.Error("invalid or expired host proof downgraded to guest success")
		}
		if customerRecoveryCounts(t, db) != before {
			t.Error("invalid host proof created rows")
		}
	}
	result := customerRecoveryExchange(t, router, channel, "synthetic-user", "Guest", b.Data.CustomerSessionToken, "")
	if result.ErrorCode != 3000 || result.Data != nil || customerRecoveryCounts(t, db) != before {
		t.Error("user customer-session proof was accepted in guest domain")
	}
	// A customer-session proof cannot replace the host JWT in Authorization.
	result = customerRecoveryExchange(t, router, channel, "synthetic-user", "Guest", "", b.Data.CustomerSessionToken)
	if result.ErrorCode != 3000 || result.Data != nil || customerRecoveryCounts(t, db) != before {
		t.Error("customer-session Authorization crossed the host-user domain")
	}
}

func TestCustomerExchangeGuestProofTransport(t *testing.T) {
	db, router, channel, _ := newCustomerRecoveryFixture(t)
	a := customerRecoveryExchange(t, router, channel, "shared-guest", "Customer A", "", "")
	b := customerRecoveryExchange(t, router, channel, "shared-guest", "Customer B", "", "")
	if a.Data == nil || b.Data == nil || a.Data.Customer.ID == b.Data.Customer.ID {
		t.Fatal("seed distinct guests through handler")
	}
	continued := customerRecoveryExchange(t, router, channel, "shared-guest", "Customer B renamed", b.Data.CustomerSessionToken, "")
	if continued.ErrorCode != 0 || continued.Data == nil || continued.Data.Customer.ID != b.Data.Customer.ID || continued.Data.Customer.Name != "Customer B renamed" {
		t.Fatal("guest header must continue exact B and update only B metadata")
	}
	var oldA models.Customer
	if err := db.First(&oldA, a.Data.Customer.ID).Error; err != nil || oldA.Name != "Customer A" {
		t.Error("B continuity modified A")
	}
	before := customerRecoveryCounts(t, db)
	for _, proof := range []string{"malformed-proof", a.Data.CustomerSessionToken} {
		result := customerRecoveryExchange(t, router, channel, "different-hint", "Changed", proof, "")
		if result.ErrorCode != 3000 || result.Data != nil || customerRecoveryCounts(t, db) != before {
			t.Error("malformed or mismatched header must reject without fallback")
		}
	}
	// Authentic expiry-only proof creates another customer without touching A.
	claims := jwt.MapClaims{"typ": "customer_session", "channelId": channel.ID, "channelCode": channel.ChannelID, "customerId": a.Data.Customer.ID, "customerName": "Customer A", "identityKey": "guest:shared-guest", "exp": time.Now().Add(-time.Hour).Unix()}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(config.Current().CustomerSession.Secret))
	if err != nil {
		t.Fatal("sign expired guest proof")
	}
	result := customerRecoveryExchange(t, router, channel, "shared-guest", "Customer C", expired, "")
	if result.ErrorCode != 0 || result.Data == nil || result.Data.Customer.ID == a.Data.Customer.ID || result.Data.Customer.ID == b.Data.Customer.ID {
		t.Error("expiry-only guest header must create fresh C")
	}
	var afterA models.Customer
	if err := db.First(&afterA, a.Data.Customer.ID).Error; err != nil {
		t.Fatal("read A after expired exchange")
	}
	if !reflect.DeepEqual(oldA, afterA) {
		t.Error("expired exchange mutated A")
	}
}

func TestCustomerExchangeInternalFaultResponse(t *testing.T) {
	for _, failure := range []string{"secret", "persistence"} {
		t.Run(failure, func(t *testing.T) {
			db, router, channel, _ := newCustomerRecoveryFixture(t)
			before := customerRecoveryCounts(t, db)
			if failure == "secret" {
				config.SetCurrent(&config.Config{})
			} else {
				if err := db.Callback().Create().Before("gorm:create").Register("test:exchange-failure", func(tx *gorm.DB) { tx.AddError(gorm.ErrInvalidDB) }); err != nil {
					t.Fatal("install exchange fault")
				}
				t.Cleanup(func() { _ = db.Callback().Create().Remove("test:exchange-failure") })
			}
			out := customerRecoveryExchange(t, router, channel, "shared-guest", "Guest", "", "")
			if out.ErrorCode != 2001 || out.Data != nil {
				t.Error("exchange fault must return business error without success payload")
			}
			if customerRecoveryCounts(t, db) != before {
				t.Error("failed exchange created rows")
			}
			if failure == "persistence" {
				if out.Message != "客服会话暂时不可用。" && out.Message != "Support session is temporarily unavailable." {
					t.Error("internal exchange cause was not localized and sanitized")
				}
			}
		})
	}
}

func customerRecoveryCounts(t *testing.T, db *gorm.DB) [2]int64 {
	t.Helper()
	var result [2]int64
	if err := db.Model(&models.Customer{}).Count(&result[0]).Error; err != nil {
		t.Fatal("count recovery customers")
	}
	if err := db.Model(&models.CustomerIdentity{}).Count(&result[1]).Error; err != nil {
		t.Fatal("count recovery identities")
	}
	return result
}
