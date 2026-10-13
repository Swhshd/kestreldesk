package repositories

// This file has been modified for the KestrelDesk project.

import (
	"strings"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"

	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"

	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

var ChannelMessageOutboxRepository = newChannelMessageOutboxRepository()

func newChannelMessageOutboxRepository() *channelMessageOutboxRepository {
	return &channelMessageOutboxRepository{}
}

type channelMessageOutboxRepository struct {
}

func (r *channelMessageOutboxRepository) Get(db *gorm.DB, id int64) *models.ChannelMessageOutbox {
	ret := &models.ChannelMessageOutbox{}
	if err := db.First(ret, "id = ?", id).Error; err != nil {
		return nil
	}
	return ret
}

func (r *channelMessageOutboxRepository) Take(db *gorm.DB, where ...interface{}) *models.ChannelMessageOutbox {
	ret := &models.ChannelMessageOutbox{}
	if err := db.Take(ret, where...).Error; err != nil {
		return nil
	}
	return ret
}

func (r *channelMessageOutboxRepository) Find(db *gorm.DB, cnd *sqls.Cnd) (list []models.ChannelMessageOutbox) {
	cnd.Find(db, &list)
	return
}

func (r *channelMessageOutboxRepository) ListPending(db *gorm.DB, channelType string, limit int, now time.Time) (list []models.ChannelMessageOutbox) {
	if limit <= 0 {
		limit = 20
	}
	db.Model(&models.ChannelMessageOutbox{}).
		Where("channel_type = ? AND ((send_status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)) OR (send_status = ? AND next_retry_at IS NOT NULL AND next_retry_at <= ?))",
			channelType,
			string(enums.ChannelMessageOutboxStatusPending),
			now,
			string(enums.ChannelMessageOutboxStatusFailed),
			now,
		).
		Order("next_retry_at ASC").
		Order("id ASC").
		Limit(limit).
		Find(&list)
	return
}

func (r *channelMessageOutboxRepository) ResolveStaleSendingClaims(db *gorm.DB, channelType string, staleBefore, now time.Time, reason string) (int64, error) {
	result := db.Model(&models.ChannelMessageOutbox{}).
		Where("channel_type = ? AND send_status = ? AND updated_at <= ?",
			channelType,
			string(enums.ChannelMessageOutboxStatusSending),
			staleBefore,
		).
		Updates(map[string]any{
			"send_status":          string(enums.ChannelMessageOutboxStatusFailed),
			"dispatch_claim_token": "",
			"next_retry_at":        nil,
			"last_error":           reason,
			"updated_at":           now,
		})
	return result.RowsAffected, result.Error
}

func (r *channelMessageOutboxRepository) FindOne(db *gorm.DB, cnd *sqls.Cnd) *models.ChannelMessageOutbox {
	ret := &models.ChannelMessageOutbox{}
	if err := cnd.FindOne(db, &ret); err != nil {
		return nil
	}
	return ret
}

func (r *channelMessageOutboxRepository) FindPageByParams(db *gorm.DB, params *params.QueryParams) (list []models.ChannelMessageOutbox, paging *sqls.Paging) {
	return r.FindPageByCnd(db, &params.Cnd)
}

func (r *channelMessageOutboxRepository) FindPageByCnd(db *gorm.DB, cnd *sqls.Cnd) (list []models.ChannelMessageOutbox, paging *sqls.Paging) {
	cnd.Find(db, &list)
	count := cnd.Count(db, &models.ChannelMessageOutbox{})

	paging = &sqls.Paging{
		Page:  cnd.Paging.Page,
		Limit: cnd.Paging.Limit,
		Total: count,
	}
	return
}

func (r *channelMessageOutboxRepository) FindBySql(db *gorm.DB, sqlStr string, paramArr ...interface{}) (list []models.ChannelMessageOutbox) {
	db.Raw(sqlStr, paramArr...).Scan(&list)
	return
}

func (r *channelMessageOutboxRepository) CountBySql(db *gorm.DB, sqlStr string, paramArr ...interface{}) (count int64) {
	db.Raw(sqlStr, paramArr...).Count(&count)
	return
}

func (r *channelMessageOutboxRepository) Count(db *gorm.DB, cnd *sqls.Cnd) int64 {
	return cnd.Count(db, &models.ChannelMessageOutbox{})
}

func (r *channelMessageOutboxRepository) Create(db *gorm.DB, t *models.ChannelMessageOutbox) (err error) {
	err = db.Create(t).Error
	return
}

func (r *channelMessageOutboxRepository) Update(db *gorm.DB, t *models.ChannelMessageOutbox) (err error) {
	err = db.Save(t).Error
	return
}

func (r *channelMessageOutboxRepository) Updates(db *gorm.DB, id int64, columns map[string]interface{}) (err error) {
	err = db.Model(&models.ChannelMessageOutbox{}).Where("id = ?", id).Updates(columns).Error
	return
}

func (r *channelMessageOutboxRepository) UpdatesIfStatus(db *gorm.DB, id int64, channelType string, statuses []string, columns map[string]interface{}) (bool, error) {
	if len(statuses) == 0 {
		return false, nil
	}
	result := db.Model(&models.ChannelMessageOutbox{}).
		Where("id = ? AND channel_type = ? AND send_status IN ?", id, channelType, statuses).
		Updates(columns)
	return result.RowsAffected == 1, result.Error
}

func (r *channelMessageOutboxRepository) UpdatesIfStatusAndClaim(db *gorm.DB, id int64, channelType string, statuses []string, claimToken string, columns map[string]interface{}) (bool, error) {
	if id <= 0 || len(statuses) == 0 {
		return false, nil
	}
	result := db.Model(&models.ChannelMessageOutbox{}).
		Where("id = ? AND channel_type = ? AND send_status IN ? AND dispatch_claim_token = ?", id, channelType, statuses, claimToken).
		Updates(columns)
	return result.RowsAffected == 1, result.Error
}

func (r *channelMessageOutboxRepository) UpdatesIfClaim(db *gorm.DB, outbox *models.ChannelMessageOutbox, columns map[string]any) (bool, error) {
	if outbox == nil || outbox.ID <= 0 || strings.TrimSpace(outbox.DispatchClaimToken) == "" {
		return false, nil
	}
	result := db.Model(&models.ChannelMessageOutbox{}).
		Where("id = ? AND channel_type = ? AND send_status = ? AND dispatch_claim_token = ?",
			outbox.ID,
			outbox.ChannelType,
			string(enums.ChannelMessageOutboxStatusSending),
			outbox.DispatchClaimToken,
		).
		Updates(columns)
	return result.RowsAffected == 1, result.Error
}

// TouchDispatchClaim renews an active lease only while the caller still owns
// the sending row. A stale worker cannot revive a claim after recovery/retry.
func (r *channelMessageOutboxRepository) TouchDispatchClaim(db *gorm.DB, outbox *models.ChannelMessageOutbox, now time.Time) (bool, error) {
	if outbox == nil || outbox.ID <= 0 || strings.TrimSpace(outbox.DispatchClaimToken) == "" {
		return false, nil
	}
	result := db.Model(&models.ChannelMessageOutbox{}).
		Where("id = ? AND channel_type = ? AND send_status = ? AND dispatch_claim_token = ?",
			outbox.ID,
			outbox.ChannelType,
			string(enums.ChannelMessageOutboxStatusSending),
			outbox.DispatchClaimToken,
		).
		Update("updated_at", now)
	return result.RowsAffected == 1, result.Error
}

// ClaimForSending atomically moves one due outbox row into the sending state.
// The status and retry deadline predicates ensure overlapping dispatchers that
// selected the same pending row cannot both perform the external side effect.
func (r *channelMessageOutboxRepository) ClaimForSending(db *gorm.DB, outbox *models.ChannelMessageOutbox, now time.Time, claimToken string) (bool, error) {
	if outbox == nil || claimToken == "" {
		return false, nil
	}
	result := db.Model(&models.ChannelMessageOutbox{}).
		Where("id = ? AND channel_type = ? AND ((send_status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)) OR (send_status = ? AND next_retry_at IS NOT NULL AND next_retry_at <= ?))",
			outbox.ID,
			outbox.ChannelType,
			string(enums.ChannelMessageOutboxStatusPending),
			now,
			string(enums.ChannelMessageOutboxStatusFailed),
			now,
		).
		Updates(map[string]any{
			"send_status":          string(enums.ChannelMessageOutboxStatusSending),
			"dispatch_claim_token": claimToken,
			"updated_at":           now,
			"update_user_id":       outbox.UpdateUserID,
			"update_user_name":     outbox.UpdateUserName,
		})
	return result.RowsAffected == 1, result.Error
}

func (r *channelMessageOutboxRepository) RetryUnsentClaimAfterResponseLoss(db *gorm.DB, id int64, channelType, claimToken string, columns map[string]interface{}) (bool, error) {
	if id <= 0 || strings.TrimSpace(claimToken) == "" {
		return false, nil
	}
	result := db.Model(&models.ChannelMessageOutbox{}).
		Where("id = ? AND channel_type = ? AND send_status = ? AND dispatch_claim_token = ?",
			id, channelType, string(enums.ChannelMessageOutboxStatusSending), claimToken).
		Updates(columns)
	return result.RowsAffected == 1, result.Error
}

func (r *channelMessageOutboxRepository) UpdateColumn(db *gorm.DB, id int64, name string, value interface{}) (err error) {
	err = db.Model(&models.ChannelMessageOutbox{}).Where("id = ?", id).UpdateColumn(name, value).Error
	return
}

func (r *channelMessageOutboxRepository) Delete(db *gorm.DB, id int64) {
	db.Delete(&models.ChannelMessageOutbox{}, "id = ?", id)
}
