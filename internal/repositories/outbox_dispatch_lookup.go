package repositories

import (
	"context"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

const outboundDispatchLookupTimeout = 5 * time.Second

func outboundDispatchDB(db *gorm.DB) (*gorm.DB, context.CancelFunc) {
	parent := context.Background()
	if db != nil && db.Statement != nil && db.Statement.Context != nil {
		parent = db.Statement.Context
	}
	ctx, cancel := context.WithTimeout(parent, outboundDispatchLookupTimeout)
	return db.WithContext(ctx), cancel
}

// These error-returning lookups let outbound dispatch distinguish a missing
// record from a temporary database failure without changing legacy callers.
func (r *messageRepository) GetForOutboundDispatch(db *gorm.DB, id int64) (*models.Message, error) {
	db, cancel := outboundDispatchDB(db)
	defer cancel()
	ret := &models.Message{}
	if err := db.First(ret, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *channelMessageOutboxRepository) GetForOutboundDispatch(db *gorm.DB, id int64) (*models.ChannelMessageOutbox, error) {
	db, cancel := outboundDispatchDB(db)
	defer cancel()
	ret := &models.ChannelMessageOutbox{}
	if err := db.First(ret, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *messageRepository) FindOneForOutboundDispatch(db *gorm.DB, cnd *sqls.Cnd) (*models.Message, error) {
	db, cancel := outboundDispatchDB(db)
	defer cancel()
	ret := &models.Message{}
	if err := cnd.FindOne(db, ret); err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *messageRepository) FindForOutboundDispatch(db *gorm.DB, cnd *sqls.Cnd) ([]models.Message, error) {
	db, cancel := outboundDispatchDB(db)
	defer cancel()
	ret := make([]models.Message, 0)
	if err := cnd.Build(db).Find(&ret).Error; err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *conversationRepository) GetForOutboundDispatch(db *gorm.DB, id int64) (*models.Conversation, error) {
	db, cancel := outboundDispatchDB(db)
	defer cancel()
	ret := &models.Conversation{}
	if err := db.First(ret, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *channelRepository) GetForOutboundDispatch(db *gorm.DB, id int64) (*models.Channel, error) {
	db, cancel := outboundDispatchDB(db)
	defer cancel()
	ret := &models.Channel{}
	if err := db.First(ret, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *customerRepository) GetForOutboundDispatch(db *gorm.DB, id int64) (*models.Customer, error) {
	db, cancel := outboundDispatchDB(db)
	defer cancel()
	ret := &models.Customer{}
	if err := db.First(ret, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *customerIdentityRepository) FindOneForOutboundDispatch(db *gorm.DB, cnd *sqls.Cnd) (*models.CustomerIdentity, error) {
	db, cancel := outboundDispatchDB(db)
	defer cancel()
	ret := &models.CustomerIdentity{}
	if err := cnd.FindOne(db, ret); err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *wxWorkKFConversationRepository) TakeForOutboundDispatch(db *gorm.DB, where ...interface{}) (*models.WxWorkKFConversation, error) {
	db, cancel := outboundDispatchDB(db)
	defer cancel()
	ret := &models.WxWorkKFConversation{}
	if err := db.Take(ret, where...).Error; err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *assetRepository) GetByAssetIDForOutboundDispatch(db *gorm.DB, assetID string) (*models.Asset, error) {
	db, cancel := outboundDispatchDB(db)
	defer cancel()
	ret := &models.Asset{}
	if err := db.First(ret, "asset_id = ?", assetID).Error; err != nil {
		return nil, err
	}
	return ret, nil
}
