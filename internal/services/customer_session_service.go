package services

// This file has been modified for the KestrelDesk project.

import (
	"errors"
	"strings"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/Swhshd/kestreldesk/internal/repositories"

	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

const (
	customerSessionTokenType = "customer_session"
	customerSessionHeader    = "X-Customer-Session-Token"
	customerSessionExpHeader = "X-Customer-Session-Expires-At"
)

var CustomerSessionService = newCustomerSessionService()

func newCustomerSessionService() *customerSessionService {
	return &customerSessionService{now: time.Now}
}

type customerSessionService struct {
	now func() time.Time
}

type guestProofState uint8

const (
	guestProofMissing guestProofState = iota
	guestProofValid
	guestProofExpired
)

type guestProofResult struct {
	State        guestProofState
	Customer     *models.Customer
	ExternalUser *openidentity.ExternalUser
}

type customerSessionClaims struct {
	TokenType    string `json:"typ"`
	ChannelID    int64  `json:"channelId"`
	ChannelCode  string `json:"channelCode"`
	CustomerID   int64  `json:"customerId"`
	CustomerName string `json:"customerName"`
	IdentityKey  string `json:"identityKey"`
	jwt.RegisteredClaims
}

type CustomerSessionVerifyResult struct {
	CustomerID   int64
	ExternalUser *openidentity.ExternalUser
	Token        string
	ExpiresAt    time.Time
	Refreshed    bool
}

func (s *customerSessionService) Exchange(channel *models.Channel, externalUser openidentity.ExternalUser, guestProof string) (*response.CustomerSessionExchangeResponse, error) {
	if channel == nil || channel.Status != enums.StatusOk {
		return nil, errorsx.InvalidParamI18n("error.e0209")
	}
	if strings.TrimSpace(config.Current().CustomerSession.Secret) == "" {
		return nil, errorsx.BusinessErrorI18n(1, "error.customerSession.secretMissing")
	}
	externalUser.ExternalID = strings.TrimSpace(externalUser.ExternalID)
	var proof *guestProofResult
	if externalUser.ExternalSource == enums.ExternalSourceGuest {
		var err error
		proof, err = s.classifyGuestProof(channel, externalUser, guestProof)
		if err != nil {
			return nil, err
		}
	}
	if sqls.DB() == nil {
		return nil, errorsx.BusinessErrorI18n(1, "error.customerSession.internal")
	}
	var result *response.CustomerSessionExchangeResponse
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		var customerID int64
		var err error
		switch {
		case proof == nil:
			customerID, err = CustomerService.EnsureVerifiedExternalCustomer(ctx, externalUser)
		case proof.State == guestProofValid:
			customerID = proof.Customer.ID
			err = CustomerService.TouchVerifiedCustomer(ctx, customerID, externalUser)
		default:
			customerID, err = CustomerService.CreateFreshGuestCustomer(ctx, externalUser)
		}
		if err != nil {
			return err
		}
		customer, err := repositories.CustomerRepository.GetForSession(ctx.Tx, customerID)
		if err != nil {
			return err
		}
		if customer.Status == enums.StatusDeleted {
			return errorsx.UnauthorizedI18n("error.e0161")
		}
		token, expiresAt, err := s.Sign(channel, customer, externalUser)
		if err != nil {
			return err
		}
		result = &response.CustomerSessionExchangeResponse{
			CustomerSessionToken: token,
			ExpiresAt:            expiresAt.Format(time.DateTime),
			IdentityKey:          s.identityKey(externalUser),
			Customer: response.CustomerSessionCustomerResponse{
				ID: customer.ID, Name: strings.TrimSpace(customer.Name),
			},
		}
		return nil
	}); err != nil {
		var applicationError *errorsx.I18nError
		if errors.As(err, &applicationError) && applicationError.Code == errorsx.CodeAuthUnauthorized {
			return nil, errorsx.UnauthorizedI18n("error.e0161")
		}
		return nil, errorsx.BusinessErrorI18n(1, "error.customerSession.internal")
	}
	return result, nil
}

func (s *customerSessionService) Sign(channel *models.Channel, customer *models.Customer, externalUser openidentity.ExternalUser) (string, time.Time, error) {
	cfg := config.Current().CustomerSession
	secret := strings.TrimSpace(cfg.Secret)
	if secret == "" {
		return "", time.Time{}, errorsx.BusinessErrorI18n(1, "error.customerSession.secretMissing")
	}
	if channel == nil || customer == nil {
		return "", time.Time{}, errorsx.InvalidParamI18n("error.e0158")
	}
	now := s.now()
	expiresAt := now.Add(time.Duration(cfg.TTL()) * time.Minute)
	claims := customerSessionClaims{
		TokenType:    customerSessionTokenType,
		ChannelID:    channel.ID,
		ChannelCode:  strings.TrimSpace(channel.ChannelID),
		CustomerID:   customer.ID,
		CustomerName: strings.TrimSpace(customer.Name),
		IdentityKey:  s.identityKey(externalUser),
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, errorsx.BusinessErrorI18n(1, "error.customerSession.internal")
	}
	return token, expiresAt, nil
}

func (s *customerSessionService) VerifyRequest(ctx *gin.Context, channel *models.Channel) (*CustomerSessionVerifyResult, error) {
	token := s.getCustomerSessionToken(ctx)
	if token == "" {
		return nil, errorsx.UnauthorizedI18n("error.e0157")
	}
	claims, err := s.verifyToken(token)
	if err != nil {
		return nil, err
	}
	if channel == nil || channel.Status != enums.StatusOk {
		return nil, errorsx.InvalidParamI18n("error.e0209")
	}
	if claims.ChannelID != channel.ID || strings.TrimSpace(claims.ChannelCode) != strings.TrimSpace(channel.ChannelID) {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	customer, err := customerForSession(claims.CustomerID)
	if err != nil {
		return nil, err
	}
	external, err := s.externalUserFromClaims(claims, customer)
	if err != nil {
		return nil, err
	}
	result := &CustomerSessionVerifyResult{
		CustomerID:   claims.CustomerID,
		ExternalUser: external,
		Token:        token,
		ExpiresAt:    claims.ExpiresAt.Time,
	}
	if s.shouldRefresh(claims.ExpiresAt.Time) {
		newToken, expiresAt, err := s.Sign(channel, customer, *external)
		if err != nil {
			return nil, err
		}
		result.Token = newToken
		result.ExpiresAt = expiresAt
		result.Refreshed = true
	}
	return result, nil
}

func (s *customerSessionService) SetRefreshHeaders(ctx *gin.Context, result *CustomerSessionVerifyResult) {
	if ctx == nil || result == nil || !result.Refreshed {
		return
	}
	ctx.Header(customerSessionHeader, result.Token)
	ctx.Header(customerSessionExpHeader, result.ExpiresAt.Format(time.DateTime))
}

func (s *customerSessionService) verifyToken(rawToken string) (*customerSessionClaims, error) {
	claims, err := s.parsePossessionProof(rawToken)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if err := validateCustomerSessionNonExpiry(claims, now); err != nil {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	if !now.Before(claims.ExpiresAt.Time) {
		return nil, errorsx.UnauthorizedI18n("error.e0160")
	}
	return claims, nil
}

// parsePossessionProof authenticates the signature before any claims are trusted.
// Expiry is classified separately only after the caller verifies the binding.
func (s *customerSessionService) parsePossessionProof(rawToken string) (*customerSessionClaims, error) {
	cfg := config.Current().CustomerSession
	secret := strings.TrimSpace(cfg.Secret)
	if secret == "" {
		return nil, errorsx.BusinessErrorI18n(1, "error.customerSession.secretMissing")
	}
	claims := &customerSessionClaims{}
	token, err := jwt.ParseWithClaims(rawToken, claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unsupported signing method")
		}
		return []byte(secret), nil
	}, jwt.WithoutClaimsValidation(), jwt.WithValidMethods([]string{
		jwt.SigningMethodHS256.Alg(),
		jwt.SigningMethodHS384.Alg(),
		jwt.SigningMethodHS512.Alg(),
	}))
	if err != nil {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	if token == nil || !token.Valid || claims.TokenType != customerSessionTokenType || claims.ExpiresAt == nil {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	if claims.ChannelID <= 0 || strings.TrimSpace(claims.ChannelCode) == "" || claims.CustomerID <= 0 || strings.TrimSpace(claims.IdentityKey) == "" {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	return claims, nil
}

func validateCustomerSessionNonExpiry(claims *customerSessionClaims, now time.Time) error {
	if claims == nil || claims.ExpiresAt == nil {
		return errorsx.UnauthorizedI18n("error.e0161")
	}
	// Preserve the signed expiry. The copy retains the existing registered-claim
	// rules (including nbf), with only expiry removed as a possible failure.
	copy := claims.RegisteredClaims
	copy.ExpiresAt = jwt.NewNumericDate(now.Add(time.Hour))
	return jwt.NewValidator(jwt.WithExpirationRequired(), jwt.WithTimeFunc(func() time.Time { return now })).Validate(&copy)
}

func (s *customerSessionService) classifyGuestProof(channel *models.Channel, hint openidentity.ExternalUser, rawToken string) (*guestProofResult, error) {
	if strings.TrimSpace(config.Current().CustomerSession.Secret) == "" {
		return nil, errorsx.BusinessErrorI18n(1, "error.customerSession.secretMissing")
	}
	if channel == nil || channel.Status != enums.StatusOk {
		return nil, errorsx.InvalidParamI18n("error.e0209")
	}
	if hint.ExternalSource != enums.ExternalSourceGuest || strings.TrimSpace(hint.ExternalID) == "" {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return &guestProofResult{State: guestProofMissing}, nil
	}
	claims, err := s.parsePossessionProof(rawToken)
	if err != nil {
		return nil, err
	}
	if claims.ChannelID != channel.ID || strings.TrimSpace(claims.ChannelCode) != strings.TrimSpace(channel.ChannelID) || strings.TrimSpace(claims.IdentityKey) != s.identityKey(hint) {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	customer, err := customerForSession(claims.CustomerID)
	if err != nil {
		return nil, err
	}
	external, err := s.externalUserFromClaims(claims, customer)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if err := validateCustomerSessionNonExpiry(claims, now); err != nil {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	if !now.Before(claims.ExpiresAt.Time) {
		return &guestProofResult{State: guestProofExpired}, nil
	}
	return &guestProofResult{State: guestProofValid, Customer: customer, ExternalUser: external}, nil
}

func customerForSession(customerID int64) (*models.Customer, error) {
	customer, err := repositories.CustomerRepository.GetForSession(sqls.DB(), customerID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	if err != nil {
		return nil, errorsx.BusinessErrorI18n(1, "error.customerSession.internal")
	}
	if customer.Status == enums.StatusDeleted {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	return customer, nil
}

func (s *customerSessionService) externalUserFromClaims(claims *customerSessionClaims, customer *models.Customer) (*openidentity.ExternalUser, error) {
	if claims == nil || customer == nil || customer.ID != claims.CustomerID || customer.Status == enums.StatusDeleted {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	identityKey := strings.TrimSpace(claims.IdentityKey)
	parts := strings.SplitN(identityKey, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	var source enums.ExternalSource
	switch parts[0] {
	case "user":
		source = enums.ExternalSourceUser
	case "guest":
		source = enums.ExternalSourceGuest
	default:
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	_, err := repositories.CustomerIdentityRepository.GetByCustomerIdentity(sqls.DB(), claims.CustomerID, source, parts[1])
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errorsx.UnauthorizedI18n("error.e0161")
	}
	if err != nil {
		return nil, errorsx.BusinessErrorI18n(1, "error.customerSession.internal")
	}
	name := strings.TrimSpace(claims.CustomerName)
	if customer != nil && strings.TrimSpace(customer.Name) != "" {
		name = strings.TrimSpace(customer.Name)
	}
	return &openidentity.ExternalUser{
		ExternalSource: source,
		ExternalID:     parts[1],
		ExternalName:   name,
	}, nil
}

func (s *customerSessionService) shouldRefresh(expiresAt time.Time) bool {
	threshold := config.Current().CustomerSession.RefreshThreshold()
	return expiresAt.Sub(s.now()) <= time.Duration(threshold)*time.Minute
}

func (s *customerSessionService) identityKey(externalUser openidentity.ExternalUser) string {
	switch externalUser.ExternalSource {
	case enums.ExternalSourceUser:
		return "user:" + strings.TrimSpace(externalUser.ExternalID)
	default:
		return "guest:" + strings.TrimSpace(externalUser.ExternalID)
	}
}

func (s *customerSessionService) getCustomerSessionToken(ctx *gin.Context) string {
	auth := strings.TrimSpace(ctx.GetHeader("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		if token := strings.TrimSpace(auth[7:]); token != "" {
			return token
		}
	}
	token, _ := params.Get(ctx, "customerSessionToken")
	return strings.TrimSpace(token)
}
