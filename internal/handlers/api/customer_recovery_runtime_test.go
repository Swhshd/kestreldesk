// This file has been modified for the KestrelDesk project.
package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/Swhshd/kestreldesk/internal/services"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

type recoveryRuntimeResult struct {
	Success   bool            `json:"success"`
	ErrorCode int             `json:"errorCode"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	Header    http.Header     `json:"-"`
}

// This acceptance catches hint-only recovery, identity reassignment, and dropping
// the verified customer ID on REST/WS paths. All HTTP and WS requests use an
// isolated real listener; AI generation is the only suppressed external effect.
func TestGuestRecoveryA2HTTPWSAcceptance(t *testing.T) {
	db, router, channel, _ := newCustomerRecoveryFixture(t)
	if err := db.AutoMigrate(&models.ConversationAssignment{}); err != nil {
		t.Fatal("migrate assignment fixture")
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal("get isolated database")
	}
	// Serialize SQLite connections, not requests. Barrier-started HTTP requests
	// still overlap through middleware, validation, and transaction acquisition.
	conn.SetMaxOpenConns(1)
	previousHook := services.TriggerAIReplyAsyncHook
	services.TriggerAIReplyAsyncHook = func(models.Conversation, models.Message) {}
	t.Cleanup(func() { services.TriggerAIReplyAsyncHook = previousHook })
	router.GET("/api/ws/open", services.WsService.HandleOpenWS)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 10 * time.Second
	hint := "synthetic-X"
	call := func(method, path, body, token, proof, user, external, name, contentType string) (recoveryRuntimeResult, error) {
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			return recoveryRuntimeResult{}, fmt.Errorf("construct request")
		}
		req.Header.Set("X-Channel-ID", channel.ChannelID)
		req.Header.Set("X-External-ID", external)
		req.Header.Set("X-External-Name", name)
		req.Header.Set("Content-Type", contentType)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if user != "" {
			req.Header.Set("Authorization", "Bearer "+user)
		}
		if proof != "" {
			req.Header.Set("X-Customer-Session-Token", proof)
		}
		res, err := client.Do(req)
		if err != nil {
			return recoveryRuntimeResult{}, fmt.Errorf("HTTP transport failed")
		}
		defer res.Body.Close()
		var out recoveryRuntimeResult
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			return out, fmt.Errorf("decode HTTP response (status %d)", res.StatusCode)
		}
		out.Header = res.Header
		return out, nil
	}
	request := func(method, path, body, token, contentType string) recoveryRuntimeResult {
		t.Helper()
		out, err := call(method, path, body, token, "", "", hint, "", contentType)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	exchange := func(external, name, proof, user string) recoveryRuntimeResult {
		t.Helper()
		out, err := call("POST", "/api/customer/session_exchange", "", "", proof, user, external, name, "application/json")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	decodeSession := func(out recoveryRuntimeResult) response.CustomerSessionExchangeResponse {
		t.Helper()
		var session response.CustomerSessionExchangeResponse
		if !out.Success || out.ErrorCode != 0 || json.Unmarshal(out.Data, &session) != nil || session.Customer.ID <= 0 || session.CustomerSessionToken == "" {
			t.Fatalf("session exchange failed (code %d)", out.ErrorCode)
		}
		return session
	}
	createConversation := func(session response.CustomerSessionExchangeResponse) int64 {
		t.Helper()
		out := request("POST", "/api/conversation/create_or_match", "{}", session.CustomerSessionToken, "application/json")
		var data struct{ ID, CustomerID int64 }
		if !out.Success || json.Unmarshal(out.Data, &data) != nil || data.ID <= 0 || data.CustomerID != session.Customer.ID {
			t.Fatalf("create conversation failed (code %d)", out.ErrorCode)
		}
		return data.ID
	}
	send := func(session response.CustomerSessionExchangeResponse, conversationID int64, marker string) {
		t.Helper()
		body := fmt.Sprintf(`{"conversationId":%d,"clientMsgId":%q,"messageType":"text","content":%q}`, conversationID, marker, marker)
		if out := request("POST", "/api/message/send", body, session.CustomerSessionToken, "application/json"); !out.Success {
			t.Fatalf("send synthetic marker failed (code %d)", out.ErrorCode)
		}
	}
	signGuest := func(id int64, identity string, expires time.Time) string {
		t.Helper()
		claims := jwt.MapClaims{"typ": "customer_session", "channelId": channel.ID, "channelCode": channel.ChannelID, "customerId": id, "customerName": "Synthetic", "identityKey": identity, "exp": expires.Unix()}
		raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(config.Current().CustomerSession.Secret))
		if err != nil {
			t.Fatal("sign synthetic guest")
		}
		return raw
	}

	a := decodeSession(exchange(hint, "Customer A", "", ""))
	convA := createConversation(a)
	send(a, convA, "private-marker-A")
	continued := decodeSession(exchange(hint, "Customer A", a.CustomerSessionToken, ""))
	if continued.Customer.ID != a.Customer.ID {
		t.Fatal("valid TA did not retain A")
	}
	var beforeA models.Customer
	var beforeMapping models.CustomerIdentity
	if db.First(&beforeA, a.Customer.ID).Error != nil || db.Where("customer_id = ?", a.Customer.ID).First(&beforeMapping).Error != nil {
		t.Fatal("read original A/mapping")
	}
	bResult := exchange(hint, "Customer B", "", "")
	// Explicit test-local witness: substitute only the observed exchange result.
	// No production code, database state, or expected isolation assertion changes.
	if os.Getenv("KESTRELDESK_RECOVERY_WITNESS") == "reuse-a" {
		bResult.Data, _ = json.Marshal(a)
	}
	b := decodeSession(bResult)
	if b.Customer.ID == a.Customer.ID {
		t.Fatal("no-proof same-X exchange reused A; B must be distinct")
	}
	if b.IdentityKey != a.IdentityKey {
		t.Fatal("fixture must preserve the same identityKey")
	}
	convB := createConversation(b)
	if convB == convA {
		t.Fatal("B reused A conversation")
	}
	send(b, convB, "private-marker-B")
	t.Logf("synthetic HTTP A=%d B=%d conversationA=%d conversationB=%d; same-X distinct", a.Customer.ID, b.Customer.ID, convA, convB)

	t.Run("REST ownership and retained history", func(t *testing.T) {
		var beforeRead []models.ConversationReadState
		var beforeAssets []models.Asset
		if db.Order("id").Find(&beforeRead).Error != nil || db.Order("id").Find(&beforeAssets).Error != nil {
			t.Fatal("snapshot state after legitimate sends")
		}
		var beforeConv models.Conversation
		if db.First(&beforeConv, convA).Error != nil {
			t.Fatal("read original conversation")
		}
		beforeCounts := customerRecoveryCounts(t, db)
		for _, action := range []string{"detail", "history", "send", "read", "close", "upload_image", "upload_attachment"} {
			method, path, body, ct := "POST", "/api/message/"+action, fmt.Sprintf(`{"conversationId":%d,"customerId":%d,"clientMsgId":"forbidden-B","content":"forbidden-B"}`, convA, a.Customer.ID), "application/json"
			switch action {
			case "detail":
				method, path, body = "GET", fmt.Sprintf("/api/conversation/%d?customerId=%d", convA, a.Customer.ID), ""
			case "history":
				method, path, body = "GET", fmt.Sprintf("/api/message/list?conversationId=%d&customerId=%d", convA, a.Customer.ID), ""
			case "close":
				path = "/api/conversation/close"
			case "upload_image", "upload_attachment":
				ct, body = "application/x-www-form-urlencoded", fmt.Sprintf("conversationId=%d&customerId=%d", convA, a.Customer.ID)
			}
			out := request(method, path, body, b.CustomerSessionToken, ct)
			if out.Success || out.Message != i18nx.Getf(i18nx.DefaultLocale, "error.e0222") || strings.Contains(string(out.Data), "private-marker-A") {
				t.Errorf("B %s A must return ownership denial before access, code=%d", action, out.ErrorCode)
			}
		}
		var afterConv models.Conversation
		if db.First(&afterConv, convA).Error != nil || !reflect.DeepEqual(beforeConv, afterConv) {
			t.Error("denied actions mutated A conversation")
		}
		if customerRecoveryCounts(t, db) != beforeCounts {
			t.Error("denied actions changed customer/identity counts")
		}
		for _, own := range []struct {
			session           response.CustomerSessionExchangeResponse
			id                int64
			marker, forbidden string
		}{{a, convA, "private-marker-A", "private-marker-B"}, {b, convB, "private-marker-B", "private-marker-A"}} {
			out := request("GET", fmt.Sprintf("/api/message/list?conversationId=%d", own.id), "", own.session.CustomerSessionToken, "application/json")
			if !out.Success || !strings.Contains(string(out.Data), own.marker) || strings.Contains(string(out.Data), own.forbidden) || strings.Contains(string(out.Data), "forbidden-B") {
				t.Error("own history missing or foreign send/history leaked")
			}
		}
		var afterRead []models.ConversationReadState
		var afterAssets []models.Asset
		if db.Order("id").Find(&afterRead).Error != nil || db.Order("id").Find(&afterAssets).Error != nil || !reflect.DeepEqual(beforeRead, afterRead) || !reflect.DeepEqual(beforeAssets, afterAssets) {
			t.Error("denied read/upload changed persistent state")
		}
	})

	expired := signGuest(a.Customer.ID, a.IdentityKey, time.Now().Add(-time.Hour))
	t.Run("proof validation and expiry", func(t *testing.T) {
		parts := strings.Split(a.CustomerSessionToken, ".")
		signature, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			t.Fatal("decode synthetic signature")
		}
		signature[0] ^= 1
		parts[2] = base64.RawURLEncoding.EncodeToString(signature)
		for _, tc := range []struct{ name, proof, external string }{
			{"malformed", "malformed-proof", hint}, {"tampered", strings.Join(parts, "."), hint},
			{"hint mismatch", a.CustomerSessionToken, "different-X"},
			{"exact identity mismatch", signGuest(a.Customer.ID, "guest:absent-X", time.Now().Add(time.Hour)), "absent-X"},
			{"expired mismatch", expired, "different-X"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := customerRecoveryCounts(t, db)
				out := exchange(tc.external, "Invalid", tc.proof, "")
				if out.Success || out.ErrorCode != 3000 || string(out.Data) != "null" || customerRecoveryCounts(t, db) != before {
					t.Error("invalid proof did not reject without creating rows")
				}
			})
		}
		if out := request("GET", fmt.Sprintf("/api/conversation/%d", convA), "", expired, "application/json"); out.Success || out.Message != i18nx.Getf(i18nx.DefaultLocale, "error.e0160") || string(out.Data) != "null" {
			t.Errorf("expired protected request must deny without data (success=%v code=%d)", out.Success, out.ErrorCode)
		}
		fresh := decodeSession(exchange(hint, "Fresh expired guest", expired, ""))
		if fresh.Customer.ID == a.Customer.ID || fresh.Customer.ID == b.Customer.ID {
			t.Error("normal expiry did not create fresh guest")
		}
		var retained models.Customer
		if db.First(&retained, a.Customer.ID).Error != nil || !reflect.DeepEqual(beforeA, retained) {
			t.Error("no-proof or expired exchange mutated A")
		}
	})

	t.Run("concurrent no-proof and TA TB refresh", func(t *testing.T) {
		type outcome struct {
			index  int
			result recoveryRuntimeResult
			err    error
		}
		parallel := func(jobs []func() (recoveryRuntimeResult, error)) []outcome {
			start, results := make(chan struct{}), make(chan outcome, len(jobs))
			for i, job := range jobs {
				go func() { <-start; out, err := job(); results <- outcome{i, out, err} }()
			}
			close(start)
			out := make([]outcome, len(jobs))
			for range jobs {
				got := <-results
				if got.err != nil {
					t.Fatal(got.err)
				}
				out[got.index] = got
			}
			return out
		}
		jobs := make([]func() (recoveryRuntimeResult, error), 2)
		for i := range jobs {
			jobs[i] = func() (recoveryRuntimeResult, error) {
				return call("POST", "/api/customer/session_exchange", "", "", "", "", hint, "Concurrent", "application/json")
			}
		}
		out := parallel(jobs)
		c, d := decodeSession(out[0].result), decodeSession(out[1].result)
		if c.Customer.ID == d.Customer.ID || c.Customer.ID == a.Customer.ID || c.Customer.ID == b.Customer.ID || d.Customer.ID == a.Customer.ID || d.Customer.ID == b.Customer.ID {
			t.Error("concurrent no-proof exchanges merged existing identities")
		}
		for i, session := range []response.CustomerSessionExchangeResponse{a, b} {
			near := signGuest(session.Customer.ID, session.IdentityKey, time.Now().Add(time.Minute))
			id := []int64{convA, convB}[i]
			jobs[i] = func() (recoveryRuntimeResult, error) {
				return call("GET", fmt.Sprintf("/api/conversation/%d", id), "", near, "", "", hint, "", "application/json")
			}
		}
		for i, result := range parallel(jobs) {
			expected := []response.CustomerSessionExchangeResponse{a, b}[i]
			if !result.result.Success || result.result.Header.Get("X-Customer-Session-Token") == "" {
				t.Fatal("concurrent protected refresh failed")
			}
			refreshed := decodeSession(exchange(hint, expected.Customer.Name, result.result.Header.Get("X-Customer-Session-Token"), ""))
			if refreshed.Customer.ID != expected.Customer.ID {
				t.Error("concurrent refresh crossed customer identity")
			}
		}
	})

	t.Run("signed user regression", func(t *testing.T) {
		var prior int64
		for _, name := range []string{"Signed user one", "Signed user two"} {
			claims := openidentity.UserTokenClaims{UserID: "synthetic-user", Name: name, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(services.ChannelService.GetUserTokenSecret(channel)))
			if err != nil {
				t.Fatal("sign synthetic host user")
			}
			user := decodeSession(exchange(hint, "Untrusted", "malformed-guest-proof", token))
			if user.IdentityKey != "user:synthetic-user" || user.Customer.Name != name || (prior > 0 && prior != user.Customer.ID) {
				t.Error("signed user precedence/stable reuse changed")
			}
			prior = user.Customer.ID
		}
	})

	t.Run("actual WS handshake subscription and ordered isolation", func(t *testing.T) {
		read := func(socket *websocket.Conn) struct {
			Type string
			Data json.RawMessage
		} {
			t.Helper()
			_ = socket.SetReadDeadline(time.Now().Add(10 * time.Second))
			var event struct {
				Type string
				Data json.RawMessage
			}
			if err := socket.ReadJSON(&event); err != nil {
				t.Fatal("read WS event before safety deadline")
			}
			return event
		}
		dial := func(token string) (*websocket.Conn, *http.Response, error) {
			header := http.Header{"X-Channel-ID": []string{channel.ChannelID}, "Authorization": []string{"Bearer " + token}}
			return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/ws/open", header)
		}
		sockets := make([]*websocket.Conn, 2)
		for i, session := range []response.CustomerSessionExchangeResponse{a, b} {
			socket, res, err := dial(session.CustomerSessionToken)
			if res != nil && res.Body != nil {
				_ = res.Body.Close()
			}
			if err != nil {
				t.Fatal("valid WS handshake failed")
			}
			defer socket.Close()
			sockets[i] = socket
			event := read(socket)
			var connected struct{ Topics []string }
			if json.Unmarshal(event.Data, &connected) != nil || event.Type != enums.IMRealtimeEventConnected || !reflect.DeepEqual(connected.Topics, []string{fmt.Sprintf("customer:%d", session.Customer.ID)}) {
				t.Fatal("connected destinations must be exact customer, without guest fallback")
			}
		}
		foreign := []string{fmt.Sprintf("customer:%d", a.Customer.ID), fmt.Sprintf("conversation:%d", convA), "guest:" + hint}
		own := fmt.Sprintf("conversation:%d", convB)
		if err := sockets[1].WriteJSON(map[string]any{"type": "subscribe", "topics": append(foreign, own)}); err != nil {
			t.Fatal("write subscriptions")
		}
		event := read(sockets[1])
		var subscribed struct{ Topics []string }
		if json.Unmarshal(event.Data, &subscribed) != nil || event.Type != enums.IMRealtimeEventSubscribed || !reflect.DeepEqual(subscribed.Topics, []string{own}) {
			t.Fatal("foreign subscription acknowledged or own subscription denied")
		}
		send(a, convA, "later-private-marker-A")
		for {
			event = read(sockets[0])
			if event.Type == enums.IMRealtimeEventMessageCreated && strings.Contains(string(event.Data), "later-private-marker-A") {
				break
			}
		}
		// Both sends synchronously publish through the real service. B's later
		// message is an ordered barrier, so absence is not inferred from a timeout.
		send(b, convB, "B-owned-barrier")
		for {
			event = read(sockets[1])
			if strings.Contains(string(event.Data), "later-private-marker-A") {
				t.Fatal("A protected marker delivered to B")
			}
			if event.Type == enums.IMRealtimeEventMessageCreated && strings.Contains(string(event.Data), "B-owned-barrier") {
				break
			}
		}
		if socket, res, err := dial(expired); err == nil {
			_ = socket.Close()
			t.Error("expired WS handshake accepted")
		} else if res == nil || res.StatusCode != http.StatusUnauthorized {
			t.Error("expired WS handshake not denied with 401")
		} else {
			_ = res.Body.Close()
		}
		t.Log("actual /api/ws/open: exact connected topics; foreign subscriptions denied; A full marker; B own ordered barrier; expired handshake 401")
	})

	t.Run("SQLite index metadata", func(t *testing.T) {
		var indexes []struct {
			Name   string
			Unique int
		}
		if db.Raw("PRAGMA index_list('t_customer_identity')").Scan(&indexes).Error != nil {
			t.Fatal("inspect SQLite index_list")
		}
		found := false
		for _, index := range indexes {
			var columns []struct {
				Seqno int
				Name  string
			}
			if db.Raw("PRAGMA index_info('"+strings.ReplaceAll(index.Name, "'", "''")+"')").Scan(&columns).Error != nil {
				t.Fatal("inspect SQLite index_info")
			}
			names := make([]string, len(columns))
			for i, c := range columns {
				names[i] = c.Name
			}
			if index.Name == "uk_customer_external" {
				found = true
				if index.Unique != 1 || !reflect.DeepEqual(names, []string{"customer_id", "external_source", "external_id"}) {
					t.Fatal("HUMAN_GATE: contradictory uk_customer_external metadata")
				}
			}
			if index.Unique == 1 && len(names) == 2 && ((names[0] == "external_source" && names[1] == "external_id") || (names[1] == "external_source" && names[0] == "external_id")) {
				t.Fatal("HUMAN_GATE: source/external_id unexpectedly unique")
			}
			t.Logf("SQLite index %s unique=%d columns=%v", index.Name, index.Unique, names)
		}
		if !found {
			t.Fatal("HUMAN_GATE: uk_customer_external missing")
		}
	})
	var afterA models.Customer
	var afterMapping models.CustomerIdentity
	if db.First(&afterA, a.Customer.ID).Error != nil || db.First(&afterMapping, beforeMapping.ID).Error != nil {
		t.Fatal("read retained A identity")
	}
	// The later legitimate TA exchange may update activity timestamps.
	if beforeA.ID != afterA.ID || beforeA.Name != afterA.Name || beforeA.Status != afterA.Status || !reflect.DeepEqual(beforeMapping, afterMapping) {
		t.Error("A customer or exact mapping mutated by isolation/refresh checks")
	}
	for _, session := range []response.CustomerSessionExchangeResponse{a, b} {
		var count int64
		if db.Model(&models.CustomerIdentity{}).Where("customer_id = ? AND external_source = ? AND external_id = ?", session.Customer.ID, enums.ExternalSourceGuest, hint).Count(&count).Error != nil || count != 1 {
			t.Error("original exact same-X mapping not retained")
		}
	}
}
