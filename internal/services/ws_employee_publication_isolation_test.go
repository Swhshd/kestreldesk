// This file has been modified for the KestrelDesk project.
package services

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/events"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/eventbus"
	"gorm.io/gorm"
)

const employeePublicationChildEnv = "KESTRELDESK_EMPLOYEE_PUBLICATION_CASE"

// Only the exact selected case runs in a child. Child exit terminates all its
// callbacks before the parent starts another fixture, even if they are paused.
func runEmployeePublicationSubprocess(t *testing.T) bool {
	t.Helper()
	if os.Getenv(employeePublicationChildEnv) == t.Name() {
		return false
	}
	if os.Getenv(employeePublicationChildEnv) != "" {
		t.Fatal("publication child attempted to run another fixture case")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(t.Name(), "/")
	for i, part := range parts {
		parts[i] = "^" + regexp.QuoteMeta(part) + "$"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run="+strings.Join(parts, "/"), "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), employeePublicationChildEnv+"="+t.Name())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("publication child failed: %v (context: %v)\n%s", err, ctx.Err(), output)
	}
	t.Logf("isolated publication case:\n%s", output)
	return true
}

// Pausing the real init-registered handlers after their conversation lookup
// reproduces the late sqls.DB lookup in NotificationService.CreateAndPush.
// Running publication fixtures in-process must fail this regression: releasing
// the old callback after installing the next fixture inserts into the next DB.
func TestEmployeePublicationDelayedNotificationCannotReachNextFixture(t *testing.T) {
	release := make(chan struct{})
	paused := make(chan struct{}, 16)
	written := make(chan struct{}, 1)
	delayedInThisProcess := false

	t.Run("delayed assignment", func(t *testing.T) {
		if runEmployeePublicationSubprocess(t) {
			return
		}
		db, _ := setupEmployeePublicationTest(t)
		conversation := models.Conversation{CustomerName: "synthetic-delayed-callback"}
		if err := db.Create(&conversation).Error; err != nil {
			t.Fatal(err)
		}
		handlers := eventbus.Get[events.ConversationAssignedEvent]().HandlerCount()
		if handlers != 2 {
			t.Fatalf("expected both real assignment handlers, got %d", handlers)
		}
		if err := db.Callback().Query().After("gorm:query").Register("test:pause_assignment_lookup", func(tx *gorm.DB) {
			if tx.Statement.Table == "t_conversation" {
				paused <- struct{}{}
				<-release
			}
		}); err != nil {
			t.Fatal(err)
		}
		eventbus.PublishAsync(context.Background(), events.ConversationAssignedEvent{
			ConversationID: conversation.ID, ToUserID: 101, AssignType: events.ConversationAssignTypeAssign,
		})
		for range handlers {
			select {
			case <-paused:
			case <-time.After(2 * time.Second):
				t.Fatal("real assignment callback did not pause after its lookup")
			}
		}
		delayedInThisProcess = true
		// Leave both real callbacks paused across the fixture boundary. With
		// isolation, exiting this child ends their lifetime before the next case.
	})

	t.Run("next fixture", func(t *testing.T) {
		if runEmployeePublicationSubprocess(t) {
			return
		}
		db, _ := setupEmployeePublicationTest(t)
		if err := db.Callback().Create().After("gorm:create").Register("test:observe_delayed_notification", func(tx *gorm.DB) {
			if tx.Statement.Table == "t_notification" {
				written <- struct{}{}
			}
		}); err != nil {
			t.Fatal(err)
		}
		close(release)
		if delayedInThisProcess {
			select {
			case <-written:
			case <-time.After(2 * time.Second):
				t.Fatal("delayed real notification callback did not finish its write")
			}
		}
		var count int64
		if err := db.Model(&models.Notification{}).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("delayed assignment notification crossed fixture boundary: next fixture contains %d notifications, want 0", count)
		}
	})
}
