// This file has been modified for the KestrelDesk project.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

func TestTestdataAgentTeamPasswordPreflight(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setEnv   bool
		envValue string
	}{
		{name: "missing"},
		{name: "empty", setEnv: true, envValue: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const envName = "KESTRELDESK_TESTDATA_AGENTTEAM_PASSWORD"
			old, existed := os.LookupEnv(envName)
			if tc.setEnv {
				if err := os.Setenv(envName, tc.envValue); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Unsetenv(envName); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if existed {
					_ = os.Setenv(envName, old)
				} else {
					_ = os.Unsetenv(envName)
				}
			})
			t.Setenv("KESTRELDESK_ENV_FILE", os.DevNull)
			t.Setenv("ENV_FILE", os.DevNull)

			tempDir := t.TempDir()
			dsn := filepath.ToSlash(filepath.Join(tempDir, "sentinel.db"))
			db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
			if err != nil {
				t.Fatalf("open sentinel database: %v", err)
			}
			if err := db.Exec("CREATE TABLE sentinel (id INTEGER PRIMARY KEY, marker TEXT NOT NULL)").Error; err != nil {
				t.Fatalf("create sentinel table: %v", err)
			}
			if err := db.Exec("INSERT INTO sentinel (id, marker) VALUES (1, 'keep')").Error; err != nil {
				t.Fatalf("insert sentinel row: %v", err)
			}
			conn, err := db.DB()
			if err != nil {
				t.Fatalf("get sentinel connection: %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("close sentinel connection: %v", err)
			}

			configPath := filepath.Join(tempDir, "config.yaml")
			configText := fmt.Sprintf("db:\n  type: sqlite\n  dsn: %s\n", strconv.Quote(dsn))
			if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			err = runWithOptions(configPath, true, "en")
			if err == nil || !strings.Contains(err.Error(), envName) {
				t.Fatalf("runWithOptions() error did not identify required testdata input: %v", err)
			}

			checkDB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
			if err != nil {
				t.Fatalf("reopen sentinel database: %v", err)
			}
			checkConn, err := checkDB.DB()
			if err != nil {
				t.Fatalf("get sentinel verification connection: %v", err)
			}
			t.Cleanup(func() { _ = checkConn.Close() })
			var marker string
			if err := checkDB.Raw("SELECT marker FROM sentinel WHERE id = 1").Scan(&marker).Error; err != nil {
				t.Fatalf("read sentinel row: %v", err)
			}
			if marker != "keep" {
				t.Fatal("testdata preflight changed the sentinel database")
			}
		})
	}
}

func TestTestdataBootstrapCredentialPreflight(t *testing.T) {
	previousDB := sqls.DB()
	t.Cleanup(func() { sqls.SetDB(previousDB) })
	t.Setenv("KESTRELDESK_TESTDATA_AGENTTEAM_PASSWORD", "testdata-preflight-only")
	t.Setenv("KESTRELDESK_AUTH_BOOTSTRAPADMINPASSWORD", "")
	t.Setenv("BOOTSTRAP_ADMIN_PASSWORD", "")
	t.Setenv("KESTRELDESK_ENV_FILE", os.DevNull)
	t.Setenv("ENV_FILE", os.DevNull)

	tempDir := t.TempDir()
	dsn := filepath.ToSlash(filepath.Join(tempDir, "sentinel.db"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sentinel database: %v", err)
	}
	if err := db.Exec("CREATE TABLE sentinel (id INTEGER PRIMARY KEY, marker TEXT NOT NULL)").Error; err != nil {
		t.Fatalf("create sentinel table: %v", err)
	}
	if err := db.Exec("INSERT INTO sentinel (id, marker) VALUES (1, 'keep')").Error; err != nil {
		t.Fatalf("insert sentinel row: %v", err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatalf("get sentinel connection: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close sentinel connection: %v", err)
	}

	configPath := filepath.Join(tempDir, "config.yaml")
	configText := fmt.Sprintf("db:\n  type: sqlite\n  dsn: %s\n", strconv.Quote(dsn))
	if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := runWithOptions(configPath, true, "en"); err == nil {
		t.Fatal("runWithOptions() succeeded without the bootstrap admin credential")
	}
	if initializedDB := sqls.DB(); initializedDB != nil {
		initializedConn, err := initializedDB.DB()
		if err != nil {
			t.Fatalf("get initialized database connection: %v", err)
		}
		if err := initializedConn.Close(); err != nil {
			t.Fatalf("close initialized database connection: %v", err)
		}
	}

	checkDB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("reopen sentinel database: %v", err)
	}
	checkConn, err := checkDB.DB()
	if err != nil {
		t.Fatalf("get sentinel verification connection: %v", err)
	}
	t.Cleanup(func() { _ = checkConn.Close() })
	var marker string
	if err := checkDB.Raw("SELECT marker FROM sentinel WHERE id = 1").Scan(&marker).Error; err != nil {
		t.Fatalf("read sentinel row: %v", err)
	}
	if marker != "keep" {
		t.Fatal("missing bootstrap credential caused the testdata command to change the database")
	}
}
