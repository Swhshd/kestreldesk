package repositories

import (
	"strings"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"

	"gorm.io/gorm"
)

// GetByCustomerIdentity resolves the exact customer/source/external ID binding.
func (r *customerIdentityRepository) GetByCustomerIdentity(db *gorm.DB, customerID int64, externalSource enums.ExternalSource, externalID string) (*models.CustomerIdentity, error) {
	externalID = strings.TrimSpace(externalID)
	if db == nil || customerID <= 0 || strings.TrimSpace(string(externalSource)) == "" || externalID == "" {
		return nil, ErrInvalidCustomerSessionLookup
	}
	ret := &models.CustomerIdentity{}
	if err := db.First(ret, "customer_id = ? AND external_source = ? AND external_id = ?", customerID, externalSource, externalID).Error; err != nil {
		return nil, err
	}
	return ret, nil
}

// GetByExternalIdentity preserves first-row reuse for verified non-guest callers.
// It must not be used for guest authorization or fresh guest creation.
func (r *customerIdentityRepository) GetByExternalIdentity(db *gorm.DB, externalSource enums.ExternalSource, externalID string) (*models.CustomerIdentity, error) {
	externalID = strings.TrimSpace(externalID)
	if db == nil || strings.TrimSpace(string(externalSource)) == "" || externalID == "" {
		return nil, ErrInvalidCustomerSessionLookup
	}
	ret := &models.CustomerIdentity{}
	if err := db.First(ret, "external_source = ? AND external_id = ?", externalSource, externalID).Error; err != nil {
		return nil, err
	}
	return ret, nil
}
