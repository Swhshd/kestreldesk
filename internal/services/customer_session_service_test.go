package services

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCustomerSessionVerifyRequestReturnsCustomerID(t *testing.T) {
	channel, customerA, _, externalA, _ := setupCustomerSessionIdentityTest(t)
	ctx := signedCustomerSessionTestContext(t, channel, customerA, externalA)
	result, err := CustomerSessionService.VerifyRequest(ctx, channel)
	if err != nil {
		t.Fatalf("VerifyRequest rejected a valid session: %v", err)
	}
	if result == nil || result.CustomerID != 101 {
		t.Fatal("VerifyRequest must return Customer A's verified positive ID")
	}
}

func TestGuestProofClassificationPrecedence(t *testing.T) {
	now := time.Date(2030, 4, 5, 6, 7, 8, 0, time.UTC)
	type proofCase struct {
		name      string
		mutate    func(jwt.MapClaims)
		prepare   func(*testing.T, *gorm.DB, *models.Channel, *openidentity.ExternalUser)
		raw       string
		algorithm jwt.SigningMethod
		wrongKey  bool
		wantState guestProofState
		wantKey   string
		wantCode  int
	}
	cases := []proofCase{
		{name: "valid", wantState: guestProofValid},
		{name: "HS384", algorithm: jwt.SigningMethodHS384, wantState: guestProofValid},
		{name: "HS512", algorithm: jwt.SigningMethodHS512, wantState: guestProofValid},
		{name: "missing", raw: "missing", wantState: guestProofMissing},
		{name: "empty-header", raw: "empty", wantState: guestProofMissing},
		{name: "expiry-equality", mutate: func(c jwt.MapClaims) { c["exp"] = now.Unix() }, wantState: guestProofExpired},
		{name: "expiry-just-past", mutate: func(c jwt.MapClaims) { c["exp"] = now.Add(-time.Second).Unix() }, wantState: guestProofExpired},
		{name: "expiry-just-future", mutate: func(c jwt.MapClaims) { c["exp"] = now.Add(time.Second).Unix() }, wantState: guestProofValid},
		{name: "expiry-zero-is-expired", mutate: func(c jwt.MapClaims) { c["exp"] = 0 }, wantState: guestProofExpired},
		{name: "expiry-null", mutate: func(c jwt.MapClaims) { c["exp"] = nil }, wantKey: "error.e0161"},
		{name: "malformed", raw: "malformed", wantKey: "error.e0161"},
		{name: "tampered", wrongKey: true, wantKey: "error.e0161"},
		{name: "unsupported-algorithm", algorithm: jwt.SigningMethodNone, wantKey: "error.e0161"},
		{name: "future-nbf", mutate: func(c jwt.MapClaims) { c["nbf"] = now.Add(time.Second).Unix() }, wantKey: "error.e0161"},
		{name: "invalid-nbf", mutate: func(c jwt.MapClaims) { c["nbf"] = "invalid" }, wantKey: "error.e0161"},
		{name: "unsupported-source", mutate: func(c jwt.MapClaims) { c["identityKey"] = "other:synthetic-guest-a" }, wantKey: "error.e0161"},
		{name: "user-source", mutate: func(c jwt.MapClaims) { c["identityKey"] = "user:synthetic-guest-a" }, wantKey: "error.e0161"},
		{name: "missing-identity-separator", mutate: func(c jwt.MapClaims) { c["identityKey"] = "guest" }, wantKey: "error.e0161"},
		{name: "missing-identity-id", mutate: func(c jwt.MapClaims) { c["identityKey"] = "guest:  " }, wantKey: "error.e0161"},
		{name: "wrong-channel-id", mutate: func(c jwt.MapClaims) { c["channelId"] = 999 }, wantKey: "error.e0161"},
		{name: "wrong-channel-code", mutate: func(c jwt.MapClaims) { c["channelCode"] = "other-channel" }, wantKey: "error.e0161"},
		{name: "wrong-customer-binding", mutate: func(c jwt.MapClaims) { c["customerId"] = 202 }, wantKey: "error.e0161"},
		{name: "disabled-channel", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) {
			ch.Status = enums.StatusDisabled
		}, wantKey: "error.e0209", wantCode: 1000},
		{name: "wrong-hint", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) {
			h.ExternalID = "other-hint"
		}, wantKey: "error.e0161"},
		{name: "wrong-request-source", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) {
			h.ExternalSource = enums.ExternalSourceUser
		}, wantKey: "error.e0161"},
		{name: "blank-hint", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) { h.ExternalID = "  " }, wantKey: "error.e0161"},
		{name: "mapping-absent", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) {
			sessionTestDBWrite(t, db.Where("customer_id = ?", 101).Delete(&models.CustomerIdentity{}))
		}, wantKey: "error.e0161"},
		{name: "customer-absent", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) {
			sessionTestDBWrite(t, db.Delete(&models.Customer{}, 101))
		}, wantKey: "error.e0161"},
		{name: "customer-deleted", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) {
			sessionTestDBWrite(t, db.Model(&models.Customer{}).Where("id = ?", 101).Update("status", enums.StatusDeleted))
		}, wantKey: "error.e0161"},
		{name: "customer-disabled-preserved", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) {
			sessionTestDBWrite(t, db.Model(&models.Customer{}).Where("id = ?", 101).Update("status", enums.StatusDisabled))
		}, wantState: guestProofValid},
		{name: "identity-status-preserved", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) {
			sessionTestDBWrite(t, db.Model(&models.CustomerIdentity{}).Where("customer_id = ?", 101).Update("status", enums.StatusDeleted))
		}, wantState: guestProofValid},
		{name: "customer-query-fault", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) {
			installSessionQueryFault(t, db, "t_customer")
		}, wantKey: "error.customerSession.internal", wantCode: 2001},
		{name: "mapping-query-fault", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) {
			installSessionQueryFault(t, db, "t_customer_identity")
		}, wantKey: "error.customerSession.internal", wantCode: 2001},
		{name: "nil-db", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) { sqls.SetDB(nil) }, wantKey: "error.customerSession.internal", wantCode: 2001},
		{name: "missing-config", prepare: func(t *testing.T, db *gorm.DB, ch *models.Channel, h *openidentity.ExternalUser) {
			config.SetCurrent(&config.Config{})
		}, wantKey: "error.customerSession.secretMissing", wantCode: 2001},
		{name: "existing-iat-aud-iss-sub-policy", mutate: func(c jwt.MapClaims) {
			c["iat"] = now.Add(time.Hour).Unix()
			c["aud"] = []string{"other"}
			c["iss"] = "other"
			c["sub"] = "other"
		}, wantState: guestProofValid},
		{name: "invalid-iat-type", mutate: func(c jwt.MapClaims) { c["iat"] = "invalid" }, wantKey: "error.e0161"},
		{name: "invalid-aud-type", mutate: func(c jwt.MapClaims) { c["aud"] = 7 }, wantKey: "error.e0161"},
		{name: "invalid-iss-type", mutate: func(c jwt.MapClaims) { c["iss"] = []string{"invalid"} }, wantKey: "error.e0161"},
		{name: "invalid-sub-type", mutate: func(c jwt.MapClaims) { c["sub"] = false }, wantKey: "error.e0161"},
	}
	for _, field := range []string{"typ", "channelId", "channelCode", "customerId", "identityKey", "exp"} {
		for _, variant := range []string{"missing", "invalid-type", "invalid-value"} {
			field, variant := field, variant
			cases = append(cases, proofCase{name: field + "-" + variant, wantKey: "error.e0161", mutate: func(c jwt.MapClaims) {
				switch variant {
				case "missing":
					delete(c, field)
				case "invalid-type":
					c[field] = []string{"invalid"}
				case "invalid-value":
					switch field {
					case "channelId", "customerId":
						c[field] = 0
					case "exp":
						c[field] = "invalid"
					default:
						c[field] = " "
					}
				}
			}})
		}
	}
	for _, tc := range cases {
		for _, expired := range []bool{false, true} {
			name := tc.name
			if expired {
				name += "/with-expired-exp"
			}
			t.Run(name, func(t *testing.T) {
				channel, _, _, hint, _ := setupCustomerSessionIdentityTest(t)
				db := sqls.DB()
				svc := &customerSessionService{now: func() time.Time { return now }}
				claims := jwt.MapClaims{"typ": "customer_session", "channelId": 301, "channelCode": "synthetic-session-channel", "customerId": 101, "customerName": "Original", "identityKey": "guest:synthetic-guest-a", "exp": now.Add(time.Hour).Unix(), "iat": now.Add(-time.Hour).Unix()}
				if expired {
					claims["exp"] = now.Add(-time.Second).Unix()
				}
				if tc.mutate != nil {
					tc.mutate(claims)
				}
				method := tc.algorithm
				if method == nil {
					method = jwt.SigningMethodHS256
				}
				key := any([]byte(config.Current().CustomerSession.Secret))
				if tc.wrongKey {
					wrongKey := make([]byte, 32)
					if _, err := rand.Read(wrongKey); err != nil {
						t.Fatal("generate unrelated in-memory signing key")
					}
					key = wrongKey
				}
				if method == jwt.SigningMethodNone {
					key = jwt.UnsafeAllowNoneSignatureType
				}
				raw, err := jwt.NewWithClaims(method, claims).SignedString(key)
				if err != nil {
					t.Fatal("sign synthetic matrix proof")
				}
				switch tc.raw {
				case "missing":
					raw = ""
				case "empty":
					raw = "  "
				case "malformed":
					raw = "not-a-jwt"
				}
				if tc.prepare != nil {
					tc.prepare(t, db, channel, &hint)
				}
				before := customerSessionRowCounts(t, db)
				customersBefore, identitiesBefore := customerSessionExistingRows(t, db)
				result, err := svc.classifyGuestProof(channel, hint, raw)
				if tc.wantKey != "" {
					wantCode := tc.wantCode
					if wantCode == 0 {
						wantCode = 3000
					}
					assertSessionError(t, err, wantCode, tc.wantKey)
					if result != nil {
						t.Error("rejected proof returned a classification")
					}
					// A user request source is rejected by the guest classifier; Exchange has its own verified user domain.
					if tc.name != "wrong-request-source" {
						out, exchangeErr := svc.Exchange(channel, hint, raw)
						assertSessionError(t, exchangeErr, wantCode, tc.wantKey)
						if out != nil {
							t.Error("rejected exchange returned a session")
						}
					}
					if customerSessionRowCounts(t, db) != before {
						t.Error("rejected proof created customer or identity rows")
					}
					customersAfter, identitiesAfter := customerSessionExistingRows(t, db)
					if !reflect.DeepEqual(customersBefore, customersAfter) || !reflect.DeepEqual(identitiesBefore, identitiesAfter) {
						t.Error("rejected proof mutated existing customer or identity")
					}
					return
				}
				wantState := tc.wantState
				if expired && wantState == guestProofValid && tc.name != "expiry-just-future" {
					wantState = guestProofExpired
				}
				if err != nil || result == nil || result.State != wantState {
					t.Fatalf("proof state mismatch, want %d", wantState)
				}
				if wantState == guestProofValid && (result.Customer == nil || result.Customer.ID != 101 || result.ExternalUser == nil || result.ExternalUser.ExternalID != hint.ExternalID) {
					t.Fatal("valid proof lost exact customer binding")
				}
				out, err := svc.Exchange(channel, hint, raw)
				if err != nil || out == nil {
					t.Fatal("accepted exchange failed")
				}
				if wantState == guestProofValid {
					if out.Customer.ID != 101 || customerSessionRowCounts(t, db) != before {
						t.Error("valid proof did not continue exact A")
					}
				} else if out.Customer.ID == 101 || customerSessionRowCounts(t, db) != [2]int64{before[0] + 1, before[1] + 1} {
					t.Error("missing or expiry-only proof must create fresh customer and identity")
				}
				if wantState != guestProofValid {
					customersAfter, identitiesAfter := customerSessionExistingRows(t, db)
					if !reflect.DeepEqual(customersBefore, customersAfter) || !reflect.DeepEqual(identitiesBefore, identitiesAfter) {
						t.Error("fresh exchange mutated existing customer or identity")
					}
				}
			})
		}
	}
}

func sessionTestDBWrite(t *testing.T, result *gorm.DB) {
	t.Helper()
	if result.Error != nil {
		t.Fatal("prepare session fixture")
	}
}

func installSessionQueryFault(t *testing.T, db *gorm.DB, table string) {
	t.Helper()
	const callback = "test:customer-session-query-fault"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == table {
			tx.AddError(errors.New("synthetic session query failure"))
		}
	}); err != nil {
		t.Fatal("register session query fault")
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
}

func customerSessionRowCounts(t *testing.T, db *gorm.DB) [2]int64 {
	t.Helper()
	// Raw count avoids the injected model-query failure while still reading real persisted rows.
	var counts [2]int64
	if err := db.Raw("SELECT COUNT(*) FROM t_customer").Scan(&counts[0]).Error; err != nil {
		t.Fatal("count customers")
	}
	if err := db.Raw("SELECT COUNT(*) FROM t_customer_identity").Scan(&counts[1]).Error; err != nil {
		t.Fatal("count identities")
	}
	return counts
}

func customerSessionExistingRows(t *testing.T, db *gorm.DB) ([]models.Customer, []models.CustomerIdentity) {
	t.Helper()
	var customers []models.Customer
	var identities []models.CustomerIdentity
	if err := db.Raw("SELECT * FROM t_customer WHERE id IN (101, 202) ORDER BY id").Scan(&customers).Error; err != nil {
		t.Fatal("snapshot existing session customers")
	}
	if err := db.Raw("SELECT * FROM t_customer_identity WHERE customer_id IN (101, 202) ORDER BY id").Scan(&identities).Error; err != nil {
		t.Fatal("snapshot existing session identities")
	}
	return customers, identities
}

func assertSessionError(t *testing.T, err error, code int, key string) {
	t.Helper()
	var applicationError *errorsx.I18nError
	if !errors.As(err, &applicationError) {
		t.Fatal("expected localized application error")
	}
	if applicationError.Code != code || applicationError.Key != key {
		t.Errorf("error code/key = %d/%s, want %d/%s", applicationError.Code, applicationError.Key, code, key)
	}
}

func TestCustomerSessionNonExpiryValidationPreservesOriginalClaims(t *testing.T) {
	now := time.Date(2030, 4, 5, 6, 7, 8, 0, time.UTC)
	claims := &customerSessionClaims{RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(now.Add(-time.Hour)), IssuedAt: jwt.NewNumericDate(now.Add(time.Hour)), Audience: jwt.ClaimStrings{"other"}, Issuer: "other", Subject: "other"}}
	before := *claims
	originalExpiry := claims.ExpiresAt.Time
	if err := validateCustomerSessionNonExpiry(claims, now); err != nil {
		t.Fatal("expiry alone must not fail non-expiry validation")
	}
	if !reflect.DeepEqual(*claims, before) || !claims.ExpiresAt.Time.Equal(originalExpiry) {
		t.Fatal("non-expiry validator mutated signed claims")
	}
	claims.NotBefore = jwt.NewNumericDate(now.Add(time.Second))
	if err := validateCustomerSessionNonExpiry(claims, now); err == nil {
		t.Fatal("expired plus future nbf must reject")
	}
	claims.NotBefore = jwt.NewNumericDate(now)
	if err := validateCustomerSessionNonExpiry(claims, now); err != nil {
		t.Fatal("nbf equality should be valid")
	}
	claims.ExpiresAt = nil
	if err := validateCustomerSessionNonExpiry(claims, now); err == nil {
		t.Fatal("missing original expiry must reject")
	}
}

func TestCustomerSessionExchangeAtomicFailures(t *testing.T) {
	for _, failure := range []string{"secret-before-write", "customer-create", "identity-create", "customer-read", "sign"} {
		t.Run(failure, func(t *testing.T) {
			channel, _, _, hint, _ := setupCustomerSessionIdentityTest(t)
			db := sqls.DB()
			before := customerSessionRowCounts(t, db)
			if failure == "secret-before-write" {
				config.SetCurrent(&config.Config{})
			}
			if failure == "customer-create" || failure == "identity-create" {
				if err := db.Callback().Create().Before("gorm:create").Register("test:session-create-fault", func(tx *gorm.DB) {
					if (failure == "customer-create" && tx.Statement.Table == "t_customer") || (failure == "identity-create" && tx.Statement.Table == "t_customer_identity") {
						tx.AddError(errors.New("synthetic create failure"))
					}
				}); err != nil {
					t.Fatal("install create fault")
				}
				t.Cleanup(func() { _ = db.Callback().Create().Remove("test:session-create-fault") })
			}
			if failure == "customer-read" {
				installSessionQueryFault(t, db, "t_customer")
			}
			if failure == "sign" {
				if err := db.Callback().Query().After("gorm:query").Register("test:session-sign-fault", func(tx *gorm.DB) {
					if tx.Statement.Table == "t_customer" {
						config.SetCurrent(&config.Config{})
					}
				}); err != nil {
					t.Fatal("install signing configuration failure")
				}
				t.Cleanup(func() { _ = db.Callback().Query().Remove("test:session-sign-fault") })
			}
			out, err := newCustomerSessionService().Exchange(channel, hint, "")
			key := "error.customerSession.internal"
			if failure == "secret-before-write" {
				key = "error.customerSession.secretMissing"
			}
			assertSessionError(t, err, 2001, key)
			if out != nil {
				t.Error("failed exchange returned usable session")
			}
			if customerSessionRowCounts(t, db) != before {
				t.Error("failed exchange did not roll back customer and identity")
			}
		})
	}
}

func TestCustomerSessionClockRefreshAndProtectedExpiry(t *testing.T) {
	channel, customer, _, hint, _ := setupCustomerSessionIdentityTest(t)
	now := time.Date(2030, 4, 5, 6, 7, 8, 0, time.UTC)
	svc := &customerSessionService{now: func() time.Time { return now }}
	raw, exp, err := svc.Sign(channel, customer, hint)
	if err != nil || exp != now.Add(120*time.Minute) {
		t.Fatal("Sign must use private fixed clock and existing TTL")
	}
	claims, err := svc.parsePossessionProof(raw)
	if err != nil || claims.IssuedAt == nil || !claims.IssuedAt.Time.Equal(now) {
		t.Fatal("Sign issued-at must use private fixed clock")
	}
	for _, offset := range []time.Duration{-time.Second, 0, time.Second, 30 * time.Minute, 30*time.Minute + time.Second} {
		claims.ExpiresAt = jwt.NewNumericDate(now.Add(offset))
		raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(config.Current().CustomerSession.Secret))
		if err != nil {
			t.Fatal("sign boundary session")
		}
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest("GET", "/protected", nil)
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)
		out, err := svc.VerifyRequest(ctx, channel)
		if offset <= 0 {
			assertSessionError(t, err, 3000, "error.e0160")
			if out != nil {
				t.Error("expired protected proof accepted")
			}
			continue
		}
		if err != nil || out == nil {
			t.Fatal("unexpired protected proof rejected")
		}
		if out.Refreshed != (offset <= 30*time.Minute) {
			t.Error("refresh threshold boundary changed")
		}
		svc.SetRefreshHeaders(ctx, out)
		if out.Refreshed && (ctx.Writer.Header().Get(customerSessionHeader) == "" || ctx.Writer.Header().Get(customerSessionExpHeader) == "") {
			t.Error("refresh response headers missing")
		}
	}
}

func TestCustomerSessionSameGuestHintContinuity(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		name := "A-first"
		if reversed {
			name = "B-first"
		}
		t.Run(name, func(t *testing.T) {
			channel, a, b, hint, _ := setupCustomerSessionIdentityTest(t)
			db := sqls.DB()
			if err := db.Where("1 = 1").Delete(&models.CustomerIdentity{}).Error; err != nil {
				t.Fatal("clear fixture mappings")
			}
			ids := []int64{a.ID, b.ID}
			if reversed {
				ids = []int64{b.ID, a.ID}
			}
			for _, id := range ids {
				if err := db.Create(&models.CustomerIdentity{CustomerID: id, ExternalSource: hint.ExternalSource, ExternalID: hint.ExternalID, Status: enums.StatusOk}).Error; err != nil {
					t.Fatal("create duplicate guest mapping")
				}
			}
			for _, customer := range []*models.Customer{a, b} {
				ctx := signedCustomerSessionTestContext(t, channel, customer, hint)
				result, err := CustomerSessionService.VerifyRequest(ctx, channel)
				if err != nil || result == nil {
					t.Errorf("valid exact session for customer %d rejected", customer.ID)
					continue
				}
				if result.CustomerID != customer.ID {
					t.Errorf("session selected customer %d, want exact %d", result.CustomerID, customer.ID)
				}
				proof, err := CustomerSessionService.classifyGuestProof(channel, hint, result.Token)
				if err != nil || proof == nil || proof.State != guestProofValid || proof.Customer.ID != customer.ID {
					t.Fatal("duplicate mapping changed classified customer")
				}
				out, err := CustomerSessionService.Exchange(channel, hint, result.Token)
				if err != nil || out == nil || out.Customer.ID != customer.ID {
					t.Fatal("duplicate mapping changed exchange customer")
				}
			}
		})
	}
}

func TestCustomerSessionVerifyRequestRejectsIdentityMappingMismatch(t *testing.T) {
	channel, customerA, _, _, externalB := setupCustomerSessionIdentityTest(t)
	ctx := signedCustomerSessionTestContext(t, channel, customerA, externalB)
	result, err := CustomerSessionService.VerifyRequest(ctx, channel)
	if err == nil || result != nil {
		t.Fatal("VerifyRequest must reject a signed A claim whose identity maps to B")
	}
}

func TestUpgradeConnectionUsesVerifiedCustomerIDNotClientQuery(t *testing.T) {
	channel, customerA, _, externalA, _ := setupCustomerSessionIdentityTest(t)
	ctx := signedCustomerSessionTestContext(t, channel, customerA, externalA)
	verified, err := CustomerSessionService.VerifyRequest(ctx, channel)
	if err != nil {
		t.Fatalf("verify synthetic session: %v", err)
	}

	svc := newWsServiceForTest()
	upgraded := make(chan error, 1)
	router := gin.New()
	router.GET("/ws", func(ctx *gin.Context) {
		upgraded <- svc.upgradeConnection(ctx, nil, verified.ExternalUser, realtimeRoleUser, verified)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?customerId=202", nil)
	if err != nil {
		t.Fatalf("websocket handshake: %v", err)
	}
	defer conn.Close()
	select {
	case err := <-upgraded:
		if err != nil {
			t.Fatalf("upgradeConnection: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upgradeConnection did not finish")
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read connected frame: %v", err)
	}
	var connected struct {
		Type string `json:"type"`
		Data struct {
			ConnID string `json:"connId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &connected); err != nil {
		t.Fatal("decode connected frame")
	}
	if connected.Type != enums.IMRealtimeEventConnected || connected.Data.ConnID == "" {
		t.Fatal("expected connected frame with connection ID")
	}
	svc.manager.mu.RLock()
	session := svc.manager.sessions[connected.Data.ConnID]
	svc.manager.mu.RUnlock()
	if session == nil {
		t.Fatal("upgraded session was not registered")
	}
	defer svc.closeSession(session)
	if session.CustomerID != 101 {
		t.Fatalf("registered CustomerID = %d, want verified A (101), despite query B (202)", session.CustomerID)
	}
}

func setupCustomerSessionIdentityTest(t *testing.T) (*models.Channel, *models.Customer, *models.Customer, openidentity.ExternalUser, openidentity.ExternalUser) {
	t.Helper()
	previousDB := sqls.DB()
	previousConfig := config.GetCurrent()
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		config.SetCurrent(previousConfig)
	})
	db := openHumanDispatchRealtimeTestDB(t, false)
	db.Config.Logger = logger.Default.LogMode(logger.Silent)
	signingKey := make([]byte, 32)
	if _, err := rand.Read(signingKey); err != nil {
		t.Fatal("generate in-memory signing key")
	}
	config.SetCurrent(&config.Config{CustomerSession: config.CustomerSessionConfig{
		Secret: base64.RawURLEncoding.EncodeToString(signingKey),
	}})
	channel := &models.Channel{ID: 301, ChannelID: "synthetic-session-channel", Status: enums.StatusOk}
	customerA := &models.Customer{ID: 101, Name: "Synthetic Customer A", Status: enums.StatusOk}
	customerB := &models.Customer{ID: 202, Name: "Synthetic Customer B", Status: enums.StatusOk}
	externalA := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "synthetic-guest-a"}
	externalB := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "synthetic-guest-b"}
	for _, row := range []any{
		channel, customerA, customerB,
		&models.CustomerIdentity{CustomerID: 101, ExternalSource: externalA.ExternalSource, ExternalID: externalA.ExternalID, Status: enums.StatusOk},
		&models.CustomerIdentity{CustomerID: 202, ExternalSource: externalB.ExternalSource, ExternalID: externalB.ExternalID, Status: enums.StatusOk},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("create synthetic session fixture: %v", err)
		}
	}
	return channel, customerA, customerB, externalA, externalB
}

func signedCustomerSessionTestContext(t *testing.T, channel *models.Channel, customer *models.Customer, external openidentity.ExternalUser) *gin.Context {
	t.Helper()
	token, _, err := CustomerSessionService.Sign(channel, customer, external)
	if err != nil {
		t.Fatal("sign synthetic customer session")
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("GET", "/ws", nil)
	ctx.Request.Header.Set("Authorization", "Bearer "+token)
	return ctx
}
