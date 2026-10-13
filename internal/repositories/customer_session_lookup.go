package repositories

import (
	"errors"

	"github.com/Swhshd/kestreldesk/internal/models"

	"gorm.io/gorm"
)

// ErrInvalidCustomerSessionLookup identifies invalid repository lookup inputs.
var ErrInvalidCustomerSessionLookup = errors.New("invalid customer session lookup")

// GetForSession reads a customer by ID without applying session status policy.
func (r *customerRepository) GetForSession(db *gorm.DB, customerID int64) (*models.Customer, error) {
	if db == nil || customerID <= 0 {
		return nil, ErrInvalidCustomerSessionLookup
	}
	ret := &models.Customer{}
	if err := db.First(ret, "id = ?", customerID).Error; err != nil {
		return nil, err
	}
	return ret, nil
}
