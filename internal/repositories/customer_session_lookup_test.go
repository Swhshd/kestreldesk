package repositories

import (
	"errors"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"

	"gorm.io/gorm"
)

func TestCustomerGetForSessionFailures(t *testing.T) {
	db := newCustomerLookupTestDB(t)
	for _, input := range []struct {
		name string
		db   *gorm.DB
		id   int64
	}{{"nil_database", nil, 101}, {"zero_customer", db, 0}, {"negative_customer", db, -1}} {
		t.Run(input.name, func(t *testing.T) {
			row, err := CustomerRepository.GetForSession(input.db, input.id)
			if row != nil || !errors.Is(err, ErrInvalidCustomerSessionLookup) || errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("invalid customer lookup: row present=%t, error=%v", row != nil, err)
			}
		})
	}
	for _, fixture := range []models.Customer{{ID: 101, Status: enums.StatusOk}, {ID: 202, Status: enums.StatusDisabled}} {
		if err := db.Create(&fixture).Error; err != nil {
			t.Fatalf("create customer fixture: %v", err)
		}
		row, err := CustomerRepository.GetForSession(db, fixture.ID)
		if err != nil || row == nil || row.ID != fixture.ID || row.Status != fixture.Status {
			t.Fatalf("customer lookup added status policy or selected wrong ID: row present=%t, error=%v", row != nil, err)
		}
	}
	row, err := CustomerRepository.GetForSession(db, 303)
	if row != nil || !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing customer returned unrelated row or wrong error: row present=%t, error=%v", row != nil, err)
	}
	fault := errors.New("synthetic customer query failure")
	if err := db.Callback().Query().Before("gorm:query").Register("test:customer_query_failure", func(tx *gorm.DB) { tx.AddError(fault) }); err != nil {
		t.Fatalf("register query fault: %v", err)
	}
	row, err = CustomerRepository.GetForSession(db, 101)
	if row != nil || !errors.Is(err, fault) || errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("customer query fault confused with absence: row present=%t, error=%v", row != nil, err)
	}
}
