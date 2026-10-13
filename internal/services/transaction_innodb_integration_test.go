// This file has been modified for the KestrelDesk project.
package services_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These opt-in tests only create synthetic tables in a dedicated disposable
// database. Never supply an application DSN. Example database name:
// kestrel_e1_test_012345abcdef, with a database-scoped e1_fixture account.
const innodbFixtureDSNEnv = "KESTREL_E1_MYSQL_TEST_DSN"

func TestInnoDBFixtureDSNRejectsApplicationDatabases(t *testing.T) {
	for _, dsn := range []string{
		"e1_fixture:synthetic@tcp(127.0.0.1:3306)/kestreldesk",
		"e1_fixture:synthetic@tcp(127.0.0.1:3306)/kestreldesk",
		"e1_fixture:synthetic@tcp(127.0.0.1:3306)/mysql",
		"root:synthetic@tcp(127.0.0.1:3306)/kestrel_e1_test_012345abcdef",
		"e1_fixture:synthetic@tcp(remote.example:3306)/kestrel_e1_test_012345abcdef",
		"e1_fixture:synthetic@tcp(127.0.0.1:3306)/kestrel_e1_test_012345abcdef?multiStatements=true",
	} {
		if _, err := parseInnoDBFixtureDSN(dsn); err == nil {
			t.Fatal("unsafe fixture DSN accepted")
		}
	}
	if _, err := parseInnoDBFixtureDSN("e1_fixture:synthetic@tcp(127.0.0.1:3306)/kestrel_e1_test_012345abcdef?parseTime=true"); err != nil {
		t.Fatalf("isolated fixture rejected: %v", err)
	}
}

func parseInnoDBFixtureDSN(dsn string) (*drivermysql.Config, error) {
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid fixture DSN")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	ip := net.ParseIP(host)
	loopbackTCP := err == nil && cfg.Net == "tcp" && ip != nil && ip.IsLoopback()
	fixtureUser := cfg.User == "e1_fixture"
	fixtureDatabase := regexp.MustCompile(`^kestrel_e1_test_[0-9a-f]{12}$`).MatchString(cfg.DBName)
	if !loopbackTCP || !fixtureUser || !fixtureDatabase || cfg.MultiStatements || len(cfg.Params) != 0 {
		return nil, fmt.Errorf("fixture DSN rejected (loopback_tcp=%t fixture_user=%t fixture_database=%t multi_statements=%t extra_params=%t)", loopbackTCP, fixtureUser, fixtureDatabase, cfg.MultiStatements, len(cfg.Params) != 0)
	}
	cfg.Timeout = 3 * time.Second
	cfg.ReadTimeout = 15 * time.Second
	cfg.WriteTimeout = 3 * time.Second
	return cfg, nil
}

type innodbFixture struct {
	db          *sql.DB
	connections [2]*sql.Conn
	table       string
}

func newInnoDBFixture(t *testing.T) *innodbFixture {
	t.Helper()
	dsn := os.Getenv(innodbFixtureDSNEnv)
	if dsn == "" {
		t.Skip("set " + innodbFixtureDSNEnv + " only for a disposable MySQL fixture")
	}
	cfg, err := parseInnoDBFixtureDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open isolated MySQL fixture failed")
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("connect to isolated MySQL fixture failed")
	}
	var existingTables int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&existingTables); err != nil {
		t.Fatal("inspect isolated MySQL fixture failed")
	}
	if existingTables != 0 {
		t.Fatal("isolated MySQL fixture database must be empty")
	}
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("InnoDB server version: %s", version)
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	f := &innodbFixture{db: db, table: "e1_rows_" + hex.EncodeToString(suffix[:])}
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+f.table+" (id INT PRIMARY KEY, value INT NOT NULL) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanupCtx, "DROP TABLE "+f.table); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.ExecContext(ctx, "INSERT INTO "+f.table+" VALUES (1, 0), (2, 0)"); err != nil {
		t.Fatal(err)
	}
	for i := range f.connections {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f.connections[i] = conn
		t.Cleanup(func() {
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	return f
}

func (f *innodbFixture) gormOn(t *testing.T, index int) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: f.connections[index], SkipInitializeWithVersion: true}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func (f *innodbFixture) assertValues(t *testing.T, first, second int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for id, want := range []int{first, second} {
		var got int
		if err := f.db.QueryRowContext(ctx, "SELECT value FROM "+f.table+" WHERE id = ?", id+1).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("row %d value = %d, want %d (partial transaction persisted)", id+1, got, want)
		}
	}
}

func assertMySQLFailure(t *testing.T, err error, number uint16, state string) {
	t.Helper()
	var mysqlErr *drivermysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != number || string(mysqlErr.SQLState[:]) != state {
		t.Fatalf("error = %v, want MySQL %d / %s", err, number, state)
	}
}

// Opposing row orders deterministically create a cycle. The same GORM
// transaction boundary used by sqls.WithTransaction must discard the victim's
// first write. Only the surviving operation may commit both rows.
func TestInnoDBDeadlockAbandonsWholeTransaction(t *testing.T) {
	f := newInnoDBFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dbs := [2]*gorm.DB{f.gormOn(t, 0), f.gormOn(t, 1)}
	ready := make(chan error, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for i := range dbs {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			entered := false
			err := dbs[i].WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				entered = true
				err := tx.Exec("UPDATE "+f.table+" SET value = ? WHERE id = ?", i+1, i+1).Error
				ready <- err
				if err != nil {
					return err
				}
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				return tx.Exec("UPDATE "+f.table+" SET value = ? WHERE id = ?", i+1, 2-i).Error
			})
			if !entered {
				ready <- err
			}
			results <- err
		}(i)
	}
	// Join before assertions, including failure paths; the context bounds every
	// query and releases a worker if its peer cannot reach the first lock.
	firstReady, secondReady := <-ready, <-ready
	close(release)
	workers.Wait()
	if firstReady != nil || secondReady != nil {
		t.Fatalf("initial writes: %v, %v", firstReady, secondReady)
	}
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("want one commit and one victim: %v, %v", first, second)
	}
	victim := first
	if victim == nil {
		victim = second
	}
	assertMySQLFailure(t, victim, 1213, "40001")
	var winner int
	if err := f.db.QueryRowContext(ctx, "SELECT value FROM "+f.table+" WHERE id = 1").Scan(&winner); err != nil {
		t.Fatal(err)
	}
	if winner != 1 && winner != 2 {
		t.Fatalf("unexpected winner %d", winner)
	}
	f.assertValues(t, winner, winner)
}

// InnoDB normally rolls back only the timed-out statement (1205), so returning
// that failure through the application's sqls boundary is essential: otherwise
// the earlier write can commit, and post-commit callbacks can escape.
func TestInnoDBLockTimeoutRollsBackAndSuppressesCallbacks(t *testing.T) {
	f := newInnoDBFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	waiting := f.connections[1]
	var originalTimeout int
	if err := waiting.QueryRowContext(ctx, "SELECT @@SESSION.innodb_lock_wait_timeout").Scan(&originalTimeout); err != nil {
		t.Fatal(err)
	}
	if _, err := waiting.ExecContext(ctx, "SET SESSION innodb_lock_wait_timeout = 1"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer restoreCancel()
		if _, err := waiting.ExecContext(restoreCtx, fmt.Sprintf("SET SESSION innodb_lock_wait_timeout = %d", originalTimeout)); err != nil {
			t.Error(err)
		}
	}()
	blocker, err := f.connections[0].BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, "UPDATE "+f.table+" SET value = 9 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	previous := sqls.DB()
	sqls.SetDB(f.gormOn(t, 1).WithContext(ctx))
	defer sqls.SetDB(previous)
	var callbacks atomic.Int32
	started := time.Now()
	err = sqls.WithTransaction(func(tx *sqls.TxContext) error {
		tx.RegisterCallback(func() { callbacks.Add(1) })
		if err := tx.Tx.Exec("UPDATE " + f.table + " SET value = 7 WHERE id = 2").Error; err != nil {
			return err
		}
		return tx.Tx.Exec("UPDATE " + f.table + " SET value = 7 WHERE id = 1").Error
	})
	elapsed := time.Since(started)
	assertMySQLFailure(t, err, 1205, "HY000")
	if elapsed < 500*time.Millisecond || elapsed >= 8*time.Second {
		t.Fatalf("unbounded/unexpected lock timeout duration: %s", elapsed)
	}
	if callbacks.Load() != 0 {
		t.Fatal("callback ran for failed transaction")
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	f.assertValues(t, 0, 0)
	// A fresh whole operation on the same connection remains usable; this is a
	// test probe, not an automatic retry or continuation of the failed tx.
	if err := sqls.WithTransaction(func(tx *sqls.TxContext) error {
		tx.RegisterCallback(func() { callbacks.Add(1) })
		return tx.Tx.Exec("UPDATE " + f.table + " SET value = 3").Error
	}); err != nil {
		t.Fatal(err)
	}
	f.assertValues(t, 3, 3)
	if callbacks.Load() != 1 {
		t.Fatal("successful transaction callback did not run exactly once")
	}
}
