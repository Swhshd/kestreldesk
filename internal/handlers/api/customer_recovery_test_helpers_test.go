package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/middleware"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func newCustomerRecoveryFixture(t *testing.T) (*gorm.DB, *gin.Engine, *models.Channel, *models.AIAgent) {
	t.Helper()
	previousDB, previousConfig := sqls.DB(), config.GetCurrent()
	previousGinMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(previousGinMode) })
	db, err := gorm.Open(sqlite.Open("file:recovery-"+customerRecoveryRandomSecret(t)+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true}, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal("open isolated recovery database")
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal("open recovery database connection")
	}
	t.Cleanup(func() { sqls.SetDB(previousDB); config.SetCurrent(previousConfig); _ = conn.Close() })
	if err := db.AutoMigrate(&models.Customer{}, &models.CustomerIdentity{}, &models.Channel{}, &models.AIAgent{}, &models.Conversation{}, &models.ConversationParticipant{}, &models.ConversationReadState{}, &models.ConversationEventLog{}, &models.Message{}, &models.ChannelMessageOutbox{}, &models.Asset{}); err != nil {
		t.Fatal("migrate recovery fixture")
	}
	sqls.SetDB(db)
	config.SetCurrent(&config.Config{CustomerSession: config.CustomerSessionConfig{Secret: customerRecoveryRandomSecret(t)}})
	agent := &models.AIAgent{Name: "Synthetic recovery agent", Status: enums.StatusOk, ServiceMode: enums.IMConversationServiceModeAIOnly}
	if err := db.Create(agent).Error; err != nil {
		t.Fatal("seed recovery agent")
	}
	webConfig, err := json.Marshal(dto.WebChannelConfig{UserTokenSecret: customerRecoveryRandomSecret(t)})
	if err != nil {
		t.Fatal("encode recovery channel config")
	}
	channel := &models.Channel{ChannelID: "synthetic-recovery-channel", ChannelType: enums.ChannelTypeWeb, Status: enums.StatusOk, AIAgentID: agent.ID, ConfigJSON: string(webConfig)}
	if err := db.Create(channel).Error; err != nil {
		t.Fatal("seed recovery channel")
	}
	router := gin.New()
	router.POST("/api/customer/session_exchange", CustomerPostSession_exchange)
	router.Any("/api/channel/config", ChannelAnyConfig)
	protected := router.Group("/api", middleware.ExternalUserMiddleware)
	protected.GET("/conversation/:id", ConversationGetBy)
	protected.POST("/conversation/create_or_match", ConversationPostCreate_or_match)
	protected.POST("/conversation/close", ConversationPostClose)
	protected.Any("/message/list", MessageAnyList)
	protected.POST("/message/send", MessagePostSend)
	protected.POST("/message/read", MessagePostRead)
	protected.POST("/message/upload_image", MessagePostUpload_image)
	protected.POST("/message/upload_attachment", MessagePostUpload_attachment)
	return db, router, channel, agent
}

func customerRecoveryRandomSecret(t *testing.T) string {
	t.Helper()
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		t.Fatal("generate in-memory fixture key")
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

type customerRecoveryExchangeResult struct {
	ErrorCode int                                       `json:"errorCode"`
	Message   string                                    `json:"message"`
	Data      *response.CustomerSessionExchangeResponse `json:"data"`
}

func customerRecoveryExchange(t *testing.T, router *gin.Engine, channel *models.Channel, hint, name, guestProof, userProof string) customerRecoveryExchangeResult {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/customer/session_exchange", nil)
	req.Header.Set("X-Channel-ID", channel.ChannelID)
	req.Header.Set("X-External-ID", hint)
	req.Header.Set("X-External-Name", name)
	if guestProof != "" {
		req.Header.Set("X-Customer-Session-Token", guestProof)
	}
	if userProof != "" {
		req.Header.Set("Authorization", "Bearer "+userProof)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var result customerRecoveryExchangeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal("decode exchange response")
	}
	if rec.Code != 200 {
		t.Fatalf("exchange HTTP status = %d", rec.Code)
	}
	return result
}
