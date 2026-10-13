package services_test

// This file has been modified for the KestrelDesk project.

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/Swhshd/kestreldesk/internal/services"

	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// An identity insert error must roll back the preceding customer insert.
func TestGuestFreshCreationAtomicity(t *testing.T) {
	db := setupCustomerServiceTestDB(t)
	injected := errors.New("identity insert unavailable")
	if err := db.Callback().Create().Before("gorm:create").Register("a2_identity_failure", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CustomerIdentity" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove("a2_identity_failure") })
	err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		id, err := services.CustomerService.CreateFreshGuestCustomer(ctx, openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "X"})
		if id != 0 {
			t.Error("failed creation returned usable customer ID")
		}
		return err
	})
	if !errors.Is(err, injected) {
		t.Fatalf("expected identity insert failure, got %v", err)
	}
	for _, model := range []any{&models.Customer{}, &models.CustomerIdentity{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("partial creation persisted %T: count=%d", model, count)
		}
	}
}

func TestGuestFreshCreationInvalidInput(t *testing.T) {
	db := setupCustomerServiceTestDB(t)
	guest := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "X"}
	for _, ctx := range []*sqls.TxContext{nil, {}} {
		if id, err := services.CustomerService.CreateFreshGuestCustomer(ctx, guest); err == nil || id != 0 {
			t.Error("nil transaction accepted")
		}
	}
	for _, user := range []openidentity.ExternalUser{
		{ExternalSource: enums.ExternalSourceUser, ExternalID: "X"},
		{ExternalID: "X"},
		{ExternalSource: enums.ExternalSourceGuest, ExternalID: " \t "},
	} {
		if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
			id, err := services.CustomerService.CreateFreshGuestCustomer(ctx, user)
			if err == nil || id != 0 {
				t.Error("invalid guest identity accepted")
			}
			return err
		}); err == nil {
			t.Error("invalid creation committed")
		}
	}
	var count int64
	if err := db.Model(&models.Customer{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("invalid creation wrote a customer")
	}
}

// Exact B binding must win over the earlier A row with the same guest hint.
func TestTouchVerifiedCustomerIsolation(t *testing.T) {
	db := setupCustomerServiceTestDB(t)
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	var customers []models.Customer
	var conversations []models.Conversation
	for _, name := range []string{"A", "B"} {
		customer := models.Customer{Name: name, LastActiveAt: &old, Status: enums.StatusOk, AuditFields: models.AuditFields{CreatedAt: old, UpdatedAt: old}}
		if err := db.Create(&customer).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.CustomerIdentity{CustomerID: customer.ID, ExternalSource: enums.ExternalSourceGuest, ExternalID: "X"}).Error; err != nil {
			t.Fatal(err)
		}
		conversation := models.Conversation{CustomerID: customer.ID, CustomerName: name, AuditFields: models.AuditFields{CreatedAt: old, UpdatedAt: old}}
		if err := db.Create(&conversation).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&customer, customer.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&conversation, conversation.ID).Error; err != nil {
			t.Fatal(err)
		}
		customers = append(customers, customer)
		conversations = append(conversations, conversation)
	}
	bID := customers[1].ID
	user := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: " X ", ExternalName: "B changed"}
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		if err := services.CustomerService.TouchVerifiedCustomer(ctx, bID, user); err != nil {
			return err
		}
		var beforeCommit models.Conversation
		if err := ctx.Tx.First(&beforeCommit, conversations[1].ID).Error; err != nil {
			return err
		}
		if beforeCommit.CustomerName != "B" {
			t.Error("conversation name changed before commit")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var a, b models.Customer
	var ca, cb models.Conversation
	if err := db.First(&a, customers[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&b, bID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&ca, conversations[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&cb, conversations[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, customers[0]) || !reflect.DeepEqual(ca, conversations[0]) {
		t.Error("exact B touch mutated A")
	}
	if b.Name != "B changed" || !b.LastActiveAt.After(old) || !b.UpdatedAt.After(old) || cb.CustomerName != "B changed" {
		t.Error("B metadata/activity or post-commit conversation sync missing")
	}
	for _, mismatch := range []openidentity.ExternalUser{
		{ExternalSource: enums.ExternalSourceGuest, ExternalID: "Y", ExternalName: "wrong"},
		{ExternalSource: enums.ExternalSourceUser, ExternalID: "X", ExternalName: "wrong"},
	} {
		if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
			return services.CustomerService.TouchVerifiedCustomer(ctx, bID, mismatch)
		}); err == nil {
			t.Error("mismatched mapping accepted")
		}
	}
	rollback := errors.New("caller rolled back")
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		user.ExternalName = "rolled back"
		if err := services.CustomerService.TouchVerifiedCustomer(ctx, bID, user); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	var after models.Customer
	var afterConversation models.Conversation
	if err := db.First(&after, bID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&afterConversation, cb.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, b) || !reflect.DeepEqual(afterConversation, cb) {
		t.Error("rollback persisted customer change or ran name callback")
	}
	user.ExternalName = " \t "
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error { return services.CustomerService.TouchVerifiedCustomer(ctx, bID, user) }); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&after, bID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&afterConversation, cb.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Name != "B changed" || afterConversation.CustomerName != "B changed" {
		t.Error("blank supplied name overwrote metadata")
	}
}

func TestTouchVerifiedCustomerInvalidCustomer(t *testing.T) {
	db := setupCustomerServiceTestDB(t)
	user := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "X", ExternalName: "changed"}
	// An orphaned exact mapping must not make an absent customer valid.
	if err := db.Create(&models.CustomerIdentity{CustomerID: 999, ExternalSource: enums.ExternalSourceGuest, ExternalID: "X"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{0, 999} {
		if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error { return services.CustomerService.TouchVerifiedCustomer(ctx, id, user) }); err == nil {
			t.Error("invalid customer accepted")
		}
	}
	if err := services.CustomerService.TouchVerifiedCustomer(nil, 1, user); err == nil {
		t.Error("nil transaction accepted")
	}
	for _, status := range []enums.Status{enums.StatusDeleted, enums.StatusDisabled} {
		customer := models.Customer{Name: "original", Status: status}
		if err := db.Create(&customer).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.CustomerIdentity{CustomerID: customer.ID, ExternalSource: enums.ExternalSourceGuest, ExternalID: "X"}).Error; err != nil {
			t.Fatal(err)
		}
		err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
			return services.CustomerService.TouchVerifiedCustomer(ctx, customer.ID, user)
		})
		if status == enums.StatusDeleted && err == nil {
			t.Error("deleted customer accepted")
		}
		if status == enums.StatusDisabled && err != nil {
			t.Errorf("disabled customer semantics changed: %v", err)
		}
	}
}

func TestEnsureExternalCustomerVerifiedReuse(t *testing.T) {
	db := setupCustomerServiceTestDB(t)
	user := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceUser, ExternalID: " user-verified ", ExternalName: "first"}
	var firstID int64
	for _, name := range []string{"first", "second"} {
		user.ExternalName = name
		if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
			id, err := services.CustomerService.EnsureVerifiedExternalCustomer(ctx, user)
			if firstID == 0 {
				firstID = id
			} else if id != firstID {
				t.Error("verified identity did not reuse customer")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := services.CustomerService.Get(firstID); got == nil || got.Name != "second" {
		t.Error("verified reuse failed to update name")
	}
	for _, invalid := range []openidentity.ExternalUser{{ExternalSource: enums.ExternalSourceGuest, ExternalID: "X"}, {ExternalID: "X"}, {ExternalSource: enums.ExternalSourceUser, ExternalID: " "}} {
		if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
			_, err := services.CustomerService.EnsureVerifiedExternalCustomer(ctx, invalid)
			return err
		}); err == nil {
			t.Error("unverified or blank identity accepted")
		}
	}
	injected := errors.New("identity query unavailable")
	if err := db.Callback().Query().Before("gorm:query").Register("a2_query_failure", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CustomerIdentity" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove("a2_query_failure") })
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		_, err := services.CustomerService.EnsureVerifiedExternalCustomer(ctx, user)
		return err
	}); !errors.Is(err, injected) {
		t.Errorf("query failure treated as missing identity: %v", err)
	}
	var count int64
	if err := db.Model(&models.Customer{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Error("query failure created a customer")
	}
}

// A lookup by guest hint would return and mutate A instead of creating B.
func TestGuestFreshCreationContract(t *testing.T) {
	db := setupCustomerServiceTestDB(t)
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	a := models.Customer{Name: "A", LastActiveAt: &old, Status: enums.StatusOk, AuditFields: models.AuditFields{CreatedAt: old, UpdatedAt: old}}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	identity := models.CustomerIdentity{CustomerID: a.ID, ExternalSource: enums.ExternalSourceGuest, ExternalID: "X", Status: enums.StatusOk, AuditFields: models.AuditFields{CreatedAt: old, UpdatedAt: old}}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	conversation := models.Conversation{CustomerID: a.ID, CustomerName: "A", Status: enums.IMConversationStatusActive, AuditFields: models.AuditFields{CreatedAt: old, UpdatedAt: old}}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	// Reload snapshots to compare the database's time representation.
	if err := db.First(&a, a.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&identity, identity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&conversation, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	var bID int64
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		create := services.CustomerService.CreateFreshGuestCustomer
		if os.Getenv("KESTRELDESK_A2_FRESH_CONTROL") == "legacy" {
			create = services.CustomerService.EnsureExternalCustomer
		}
		id, err := create(ctx, openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: " X ", ExternalName: "B"})
		bID = id
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var afterA models.Customer
	var afterIdentity models.CustomerIdentity
	var afterConversation models.Conversation
	if err := db.First(&afterA, a.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&afterIdentity, identity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&afterConversation, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if bID <= 0 || bID == a.ID {
		t.Errorf("fresh guest must create B distinct from A; got B=%d A=%d", bID, a.ID)
	}
	if !reflect.DeepEqual(afterA, a) {
		t.Error("fresh guest mutated A name/activity/customer record")
	}
	if !reflect.DeepEqual(afterIdentity, identity) {
		t.Error("fresh guest mutated A mapping")
	}
	if !reflect.DeepEqual(afterConversation, conversation) {
		t.Error("fresh guest mutated A conversation")
	}
	var identities []models.CustomerIdentity
	if err := db.Where("external_source = ? AND external_id = ?", enums.ExternalSourceGuest, "X").Order("customer_id").Find(&identities).Error; err != nil {
		t.Fatal(err)
	}
	if len(identities) != 2 || identities[0].CustomerID != a.ID || identities[1].CustomerID != bID {
		t.Errorf("expected both A and B mappings, got %v", identities)
	}
	b := services.CustomerService.Get(bID)
	if b == nil || b.Name != "B" || b.LastActiveAt == nil {
		t.Error("fresh B must retain supplied name and activity")
	}
}

func TestEnsureExternalCustomerUpdatesNameFromExternalIdentity(t *testing.T) {
	db := setupCustomerServiceTestDB(t)

	var firstID int64
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		id, err := services.CustomerService.EnsureExternalCustomer(ctx, openidentity.ExternalUser{
			ExternalSource: enums.ExternalSourceUser,
			ExternalID:     "user-1",
			ExternalName:   "张三",
		})
		firstID = id
		return err
	}); err != nil {
		t.Fatalf("EnsureExternalCustomer() first error = %v", err)
	}

	conversation := &models.Conversation{
		CustomerID:   firstID,
		CustomerName: "张三",
		Status:       enums.IMConversationStatusActive,
		AuditFields:  models.AuditFields{CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatalf("create conversation error = %v", err)
	}

	var secondID int64
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		id, err := services.CustomerService.EnsureExternalCustomer(ctx, openidentity.ExternalUser{
			ExternalSource: enums.ExternalSourceUser,
			ExternalID:     "user-1",
			ExternalName:   "李四",
		})
		secondID = id
		return err
	}); err != nil {
		t.Fatalf("EnsureExternalCustomer() second error = %v", err)
	}
	if secondID != firstID {
		t.Fatalf("expected same customer id, got %d and %d", firstID, secondID)
	}

	customer := services.CustomerService.Get(firstID)
	if customer == nil {
		t.Fatalf("expected customer to exist")
	}
	if customer.Name != "李四" {
		t.Fatalf("expected customer name updated, got %q", customer.Name)
	}

	var updatedConversation models.Conversation
	if err := db.First(&updatedConversation, conversation.ID).Error; err != nil {
		t.Fatalf("get conversation error = %v", err)
	}
	if updatedConversation.CustomerName != "李四" {
		t.Fatalf("expected conversation customer name updated, got %q", updatedConversation.CustomerName)
	}
}

func setupCustomerServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			TablePrefix:   "t_",
			SingularTable: true,
		},
	})
	if err != nil {
		t.Fatalf("open sqlite error = %v", err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&models.Customer{}, &models.CustomerIdentity{}, &models.Conversation{}); err != nil {
		t.Fatalf("auto migrate error = %v", err)
	}
	sqls.SetDB(db)
	return db
}
