package scheduler_test

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/xichan96/cortex/pkg/xcron"
)

// newTestDB returns a SQLite database that is private to the calling test.
//
// Every call gets its own file under t.TempDir(); the file and its connection
// pool are closed automatically via t.Cleanup. This is deliberate: the previous
// `file::memory:?cache=shared` DSN is process-wide, so jobs created by one test
// (and resumed by Scheduler.Start) leaked into every other test in the package,
// producing cross-test execution and "database table is locked" errors.
//
// WAL plus busy_timeout allow concurrent reads and let writers wait for the
// lock; MaxOpenConns(1) keeps the connection pool from racing with itself on
// the same database file (SQLite is a single-writer engine).
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	// pure-Go driver so the package also builds/tests with CGO_ENABLED=0.
	dsn := filepath.Join(t.TempDir(), "scheduler-test.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})

	if err := db.AutoMigrate(&xcron.Job{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return db
}
