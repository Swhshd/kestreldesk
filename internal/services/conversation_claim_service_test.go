package services_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/services"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

func TestConversationClaimAuditFailureRollsBack(t *testing.T) {
	db, p, s := claimFixture(t)
	if err := db.Callback().Create().Before("gorm:create").Register("synthetic:reject_claim_audit", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "ConversationEventLog" {
			tx.AddError(errors.New("synthetic persistence failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	err := s.ClaimConversation(4, p)
	var localized *errorsx.I18nError
	if !errors.As(err, &localized) || localized.Key != "error.conversation.claimFailed" {
		t.Fatalf("unstable persistence error: %v", err)
	}
	var c models.Conversation
	db.First(&c, 4)
	var n int64
	db.Model(&models.ConversationAssignment{}).Count(&n)
	if c.Status != enums.IMConversationStatusPending || c.CurrentAssigneeID != 0 || n != 0 {
		t.Fatalf("partial claim persisted: %+v, assignments %d", c, n)
	}
}

type selfClaimer interface {
	ClaimConversation(int64, *dto.AuthPrincipal) error
}

func claimFixture(t *testing.T) (*gorm.DB, *dto.AuthPrincipal, selfClaimer) {
	t.Helper()
	db, p := scopeFixture(t)
	previous := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() { sqls.SetDB(previous) })
	raw, _ := db.DB()
	raw.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.ConversationAssignment{}, &models.ConversationEventLog{}); err != nil {
		t.Fatal(err)
	}
	p.Permissions = append(p.Permissions, "conversation.claim")
	claimer, ok := any(services.ConversationService).(selfClaimer)
	if !ok {
		t.Fatal("dedicated self-claim capability is missing")
	}
	return db, p, claimer
}

func TestConversationClaimOwnTeamWaiting(t *testing.T) {
	for _, role := range []string{constants.RoleCodeCsUser, constants.RoleCodeCsTeamLeader, constants.RoleCodeSuperAdmin} {
		t.Run(role, func(t *testing.T) {
			db, p, s := claimFixture(t)
			p.Roles = []string{role}
			if err := s.ClaimConversation(4, p); err != nil {
				t.Fatal(err)
			}
			var c models.Conversation
			if err := db.First(&c, 4).Error; err != nil {
				t.Fatal(err)
			}
			if c.CurrentAssigneeID != p.UserID || c.CurrentTeamID != 10 || c.Status != enums.IMConversationStatusActive {
				t.Fatalf("unexpected claimed state: %+v", c)
			}
			var a []models.ConversationAssignment
			db.Find(&a)
			if len(a) != 1 || a[0].ToUserID != p.UserID || a[0].OperatorID != p.UserID || a[0].FromUserID != 0 {
				t.Fatalf("assignment audit=%+v", a)
			}
			var n int64
			db.Model(&models.ConversationEventLog{}).Count(&n)
			if n != 1 {
				t.Fatalf("event audit count=%d", n)
			}
		})
	}
}

func TestConversationClaimDeniedWithoutWrites(t *testing.T) {
	for _, id := range []int64{1, 2, 3, 5, 6, 7, 8, 9, 999} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			db, p, s := claimFixture(t)
			if err := s.ClaimConversation(id, p); err == nil {
				t.Fatalf("claim %d allowed", id)
			}
			var n int64
			db.Model(&models.ConversationAssignment{}).Count(&n)
			if n != 0 {
				t.Fatalf("assignment write on denial")
			}
			db.Model(&models.ConversationEventLog{}).Count(&n)
			if n != 0 {
				t.Fatalf("audit write on denial")
			}
			var c models.Conversation
			db.First(&c, 4)
			if c.CurrentAssigneeID != 0 || c.Status != enums.IMConversationStatusPending {
				t.Fatal("queue changed")
			}
		})
	}
	for _, test := range []string{"missing-profile", "disabled-profile", "disabled-team", "disabled-user", "missing-permission", "super-admin-other-team", "super-admin-missing-profile"} {
		t.Run(test, func(t *testing.T) {
			db, p, s := claimFixture(t)
			id := int64(4)
			switch test {
			case "missing-profile":
				db.Where("user_id = ?", 101).Delete(&models.AgentProfile{})
			case "disabled-profile":
				db.Model(&models.AgentProfile{}).Where("user_id = ?", 101).Update("status", enums.StatusDisabled)
			case "disabled-team":
				db.Model(&models.AgentTeam{}).Where("id = ?", 10).Update("status", enums.StatusDisabled)
			case "disabled-user":
				db.Model(&models.User{}).Where("id = ?", 101).Update("status", enums.StatusDisabled)
			case "missing-permission":
				p.Permissions = nil
			case "super-admin-other-team":
				p.Roles = []string{constants.RoleCodeSuperAdmin}
				id = 5
			case "super-admin-missing-profile":
				p.Roles = []string{constants.RoleCodeSuperAdmin}
				db.Where("user_id = ?", 101).Delete(&models.AgentProfile{})
			}
			if err := s.ClaimConversation(id, p); err == nil {
				t.Fatal("invalid claim allowed")
			}
			var n int64
			db.Model(&models.ConversationAssignment{}).Count(&n)
			if n != 0 {
				t.Fatal("assignment on denial")
			}
		})
	}
}

func TestConversationClaimConcurrentExactlyOne(t *testing.T) {
	db, p, s := claimFixture(t)
	raw, _ := db.DB()
	raw.SetMaxOpenConns(2)
	second := *p
	second.UserID = 102
	second.Username = "synthetic-employee-102"
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	var wg sync.WaitGroup
	for _, principal := range []*dto.AuthPrincipal{p, &second} {
		ready.Add(1)
		wg.Add(1)
		go func(principal *dto.AuthPrincipal) {
			defer wg.Done()
			ready.Done()
			<-start
			results <- s.ClaimConversation(4, principal)
		}(principal)
	}
	ready.Wait()
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successes=%d, want exactly one", successes)
	}
	var audits []models.ConversationAssignment
	db.Find(&audits)
	var c models.Conversation
	db.First(&c, 4)
	if len(audits) != 1 || audits[0].ToUserID != c.CurrentAssigneeID || audits[0].OperatorID != c.CurrentAssigneeID || c.Status != enums.IMConversationStatusActive {
		t.Fatalf("state=%+v audits=%+v", c, audits)
	}
	var n int64
	db.Model(&models.ConversationEventLog{}).Count(&n)
	if n != 1 {
		t.Fatalf("event audits=%d", n)
	}
}
