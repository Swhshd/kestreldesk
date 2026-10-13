package agentteam

// This file has been modified for the KestrelDesk project.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Swhshd/kestreldesk/cmd/testdata/seedlang"
	"github.com/Swhshd/kestreldesk/cmd/testdata/seeds"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var hanTextPattern = regexp.MustCompile(`\p{Han}`)

func TestEnglishAgentTeamTextDoesNotContainChineseText(t *testing.T) {
	values := []string{seeds.AgentTeamName(seedlang.English)}
	for _, user := range seeds.AgentUsers(seedlang.English, "admin") {
		values = append(values, user.Nickname)
	}
	for _, value := range values {
		if hanTextPattern.MatchString(value) {
			t.Fatalf("english agent team seed contains Chinese text: %q", value)
		}
	}
}

func setupAgentTeamInitTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "agentteam.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.AgentTeam{}, &models.AgentProfile{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	previous := sqls.DB()
	sqls.SetDB(db)
	conn, err := db.DB()
	if err != nil {
		t.Fatalf("get test database connection: %v", err)
	}
	t.Cleanup(func() {
		sqls.SetDB(previous)
		_ = conn.Close()
	})
	return db
}

func newAgentTeamTestPassword(t *testing.T) string {
	t.Helper()
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("generating test input failed: %v", err)
	}
	return "test-agentteam-" + hex.EncodeToString(value)
}

func TestAgentTeamInitUsesProvidedPasswordForNewUsers(t *testing.T) {
	db := setupAgentTeamInitTestDB(t)
	password := newAgentTeamTestPassword(t)
	leader := models.User{Username: "admin", UserType: enums.UserTypeEmployee, Status: enums.StatusOk}
	if err := db.Create(&leader).Error; err != nil {
		t.Fatalf("create team leader: %v", err)
	}
	users := seeds.AgentUsers(seedlang.English, leader.Username)
	if len(users) < 3 {
		t.Fatal("expected a leader and at least two seeded agent accounts")
	}
	existingPassword := newAgentTeamTestPassword(t)
	existingHash, err := bcrypt.GenerateFromPassword([]byte(existingPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("generate existing account test hash: %v", err)
	}
	existing := models.User{
		Username: users[1].Username,
		Nickname: "Existing name",
		Password: string(existingHash),
		Status:   enums.StatusDisabled,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing agent account: %v", err)
	}

	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	result, err := Init(seedlang.English, password)
	if err != nil {
		t.Fatalf("seed agent team: %v", err)
	}
	if result.UsersCreated != len(users)-2 {
		t.Fatalf("new agent accounts = %d, want %d", result.UsersCreated, len(users)-2)
	}
	var created models.User
	if err := db.Where("username = ?", users[2].Username).First(&created).Error; err != nil {
		t.Fatalf("load newly seeded agent: %v", err)
	}
	if created.UserType != enums.UserTypeEmployee {
		t.Fatal("newly seeded agent account was not an employee")
	}
	if created.Password == password || bcrypt.CompareHashAndPassword([]byte(created.Password), []byte(password)) != nil {
		t.Fatal("new agent account did not receive a verifiable hash of the supplied testdata password")
	}
	var preserved models.User
	if err := db.First(&preserved, existing.ID).Error; err != nil {
		t.Fatalf("reload existing agent account: %v", err)
	}
	if preserved.Password != existing.Password || preserved.Nickname != existing.Nickname || preserved.Status != existing.Status || preserved.UserType != existing.UserType {
		t.Fatal("seeding reset an existing agent account")
	}
	if bytes.Contains(output.Bytes(), []byte(password)) {
		t.Fatal("testdata password appeared in logs")
	}
}

func TestAgentTeamInitRejectsEmptyPasswordBeforeWrites(t *testing.T) {
	db := setupAgentTeamInitTestDB(t)
	if _, err := Init(seedlang.English, ""); err == nil {
		t.Fatal("empty testdata password was accepted")
	}
	for name, model := range map[string]any{
		"user":          &models.User{},
		"agent team":    &models.AgentTeam{},
		"agent profile": &models.AgentProfile{},
	} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil {
			t.Fatalf("count %s rows: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("empty password wrote %d %s rows", count, name)
		}
	}
}
