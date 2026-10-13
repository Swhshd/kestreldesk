package repositories

import (
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"gorm.io/gorm"
)

var PasswordResetTokenRepository = newPasswordResetTokenRepository()

func newPasswordResetTokenRepository() *passwordResetTokenRepository {
	return &passwordResetTokenRepository{}
}

type passwordResetTokenRepository struct{}

func (r *passwordResetTokenRepository) Create(db *gorm.DB, token *models.PasswordResetToken) error {
	return db.Create(token).Error
}

func (r *passwordResetTokenRepository) GetByHash(db *gorm.DB, tokenHash string) (*models.PasswordResetToken, error) {
	token := &models.PasswordResetToken{}
	if err := db.First(token, "token_hash = ?", tokenHash).Error; err != nil {
		return nil, err
	}
	return token, nil
}

// Consume succeeds only once and only while the token is unexpired and bound
// to the requested surface.
func (r *passwordResetTokenRepository) Consume(db *gorm.DB, tokenHash, surface string, now time.Time) (int64, error) {
	result := db.Model(&models.PasswordResetToken{}).
		Where("token_hash = ? AND surface = ? AND used_at IS NULL AND expires_at > ?", tokenHash, surface, now).
		Updates(map[string]interface{}{
			"used_at":          now,
			"updated_at":       now,
			"update_user_id":   0,
			"update_user_name": "system",
		})
	return result.RowsAffected, result.Error
}

func (r *passwordResetTokenRepository) InvalidateUnusedByUser(db *gorm.DB, userID int64, now time.Time) error {
	return db.Model(&models.PasswordResetToken{}).
		Where("user_id = ? AND used_at IS NULL", userID).
		Updates(map[string]interface{}{
			"used_at":          now,
			"updated_at":       now,
			"update_user_id":   0,
			"update_user_name": "system",
		}).Error
}

func (r *passwordResetTokenRepository) MarkUsedByHash(db *gorm.DB, tokenHash string, now time.Time) error {
	return db.Model(&models.PasswordResetToken{}).
		Where("token_hash = ? AND used_at IS NULL", tokenHash).
		Updates(map[string]interface{}{
			"used_at":          now,
			"updated_at":       now,
			"update_user_id":   0,
			"update_user_name": "system",
		}).Error
}
