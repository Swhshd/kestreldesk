// This file has been modified for the KestrelDesk project.
package repositories

import (
	"errors"
	"os"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newCustomerLookupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open isolated database: %v", err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatalf("get database connection: %v", err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(&models.CustomerIdentity{}, &models.Customer{}); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	return db
}

// The control deliberately omits customerID using the existing global lookup.
// It is test-local and cannot alter production lookup selection.
func customerIdentityLookupForTest(db *gorm.DB, customerID int64, source enums.ExternalSource, externalID string) (*models.CustomerIdentity, error) {
	if os.Getenv("KESTRELDESK_A2_LOOKUP_CONTROL") == "global" {
		row := CustomerIdentityRepository.GetBy(db, source, externalID)
		if row == nil {
			return nil, gorm.ErrRecordNotFound
		}
		return row, nil
	}
	return CustomerIdentityRepository.GetByCustomerIdentity(db, customerID, source, externalID)
}

func TestCustomerIdentityLookupExactDuplicates(t *testing.T) {
	for _, order := range []struct {
		name string
		ids  []int64
	}{{"A_then_B", []int64{101, 202}}, {"B_then_A", []int64{202, 101}}} {
		t.Run(order.name, func(t *testing.T) {
			db := newCustomerLookupTestDB(t)
			for _, id := range order.ids {
				row := &models.CustomerIdentity{CustomerID: id, ExternalSource: enums.ExternalSourceGuest, ExternalID: "synthetic-X"}
				if err := db.Create(row).Error; err != nil {
					t.Fatalf("create duplicate hint fixture: %v", err)
				}
			}
			for _, id := range []int64{101, 202} {
				row, err := customerIdentityLookupForTest(db, id, enums.ExternalSourceGuest, "synthetic-X")
				if err != nil || row == nil {
					t.Fatalf("lookup customer %d: row present=%t, error=%v", id, row != nil, err)
				}
				if row.CustomerID != id || row.ExternalSource != enums.ExternalSourceGuest || row.ExternalID != "synthetic-X" {
					t.Errorf("requested customer %d resolved customer %d or wrong identity", id, row.CustomerID)
				}
			}
			row, err := CustomerIdentityRepository.GetByCustomerIdentity(db, 202, enums.ExternalSourceGuest, " \t synthetic-X \n")
			if err != nil || row == nil || row.CustomerID != 202 {
				t.Fatalf("trimmed external ID did not resolve customer B: row present=%t, error=%v", row != nil, err)
			}
			for _, mismatch := range []struct {
				name   string
				id     int64
				source enums.ExternalSource
				key    string
			}{{"wrong_customer", 303, enums.ExternalSourceGuest, "synthetic-X"}, {"wrong_source", 101, enums.ExternalSourceUser, "synthetic-X"}, {"wrong_key", 101, enums.ExternalSourceGuest, "synthetic-Y"}} {
				t.Run(mismatch.name, func(t *testing.T) {
					row, err := CustomerIdentityRepository.GetByCustomerIdentity(db, mismatch.id, mismatch.source, mismatch.key)
					if row != nil || !errors.Is(err, gorm.ErrRecordNotFound) {
						t.Fatalf("mismatched triple returned a row or wrong error: row present=%t, error=%v", row != nil, err)
					}
				})
			}
		})
	}
}

func TestCustomerIdentityLookupFailures(t *testing.T) {
	db := newCustomerLookupTestDB(t)
	for _, input := range []struct {
		name   string
		db     *gorm.DB
		id     int64
		source enums.ExternalSource
		key    string
	}{{"nil_database", nil, 101, enums.ExternalSourceGuest, "synthetic-X"}, {"zero_customer", db, 0, enums.ExternalSourceGuest, "synthetic-X"}, {"negative_customer", db, -1, enums.ExternalSourceGuest, "synthetic-X"}, {"empty_source", db, 101, "", "synthetic-X"}, {"blank_source", db, 101, " \t", "synthetic-X"}, {"empty_key", db, 101, enums.ExternalSourceGuest, ""}, {"blank_key", db, 101, enums.ExternalSourceGuest, " \n"}} {
		t.Run(input.name, func(t *testing.T) {
			row, err := CustomerIdentityRepository.GetByCustomerIdentity(input.db, input.id, input.source, input.key)
			if row != nil || !errors.Is(err, ErrInvalidCustomerSessionLookup) || errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("invalid exact lookup: row present=%t, error=%v", row != nil, err)
			}
			if input.name != "zero_customer" && input.name != "negative_customer" {
				row, err = CustomerIdentityRepository.GetByExternalIdentity(input.db, input.source, input.key)
				if row != nil || !errors.Is(err, ErrInvalidCustomerSessionLookup) || errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatalf("invalid external lookup: row present=%t, error=%v", row != nil, err)
				}
			}
		})
	}
	for _, id := range []int64{101, 202} {
		row := &models.CustomerIdentity{CustomerID: id, ExternalSource: enums.ExternalSourceUser, ExternalID: "synthetic-user"}
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("create verified-source fixture: %v", err)
		}
	}
	row, err := CustomerIdentityRepository.GetByExternalIdentity(db, enums.ExternalSourceUser, " synthetic-user ")
	if err != nil || row == nil || row.CustomerID != 101 {
		t.Fatalf("external reuse did not preserve first-row selection: row present=%t, error=%v", row != nil, err)
	}
	row, err = CustomerIdentityRepository.GetByExternalIdentity(db, enums.ExternalSourceUser, "synthetic-absent")
	if row != nil || !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("external absence: row present=%t, error=%v", row != nil, err)
	}
	fault := errors.New("synthetic query failure")
	if err := db.Callback().Query().Before("gorm:query").Register("test:customer_identity_query_failure", func(tx *gorm.DB) { tx.AddError(fault) }); err != nil {
		t.Fatalf("register query fault: %v", err)
	}
	row, err = CustomerIdentityRepository.GetByCustomerIdentity(db, 101, enums.ExternalSourceUser, "synthetic-user")
	if row != nil || !errors.Is(err, fault) || errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("exact query fault confused with absence: row present=%t, error=%v", row != nil, err)
	}
	row, err = CustomerIdentityRepository.GetByExternalIdentity(db, enums.ExternalSourceUser, "synthetic-user")
	if row != nil || !errors.Is(err, fault) || errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("external query fault confused with absence: row present=%t, error=%v", row != nil, err)
	}
}
