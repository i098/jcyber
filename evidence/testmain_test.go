package evidence

import (
	"database/sql"
	"os"
	"testing"

	"pta/db"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Initialize an explicitly configured fresh database before taking the same
// suite lock as db, agent and server. Hold it on one pinned connection.
func TestMain(m *testing.M) {
	db.EnsureTestDB() // never run against a console DB
	dsn, _, err := db.DSN()
	if err != nil {
		os.Exit(m.Run())
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil || conn.Ping() != nil {
		os.Exit(m.Run())
	}
	defer conn.Close()
	if _, err := conn.Exec(`SELECT pg_advisory_lock(7337741002)`); err != nil {
		os.Exit(m.Run())
	}
	defer conn.Exec(`SELECT pg_advisory_unlock(7337741002)`) //nolint:errcheck
	db.ResetTestSchema() // clean start per package run — after the lock
	os.Exit(m.Run())
}
