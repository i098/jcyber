package db

import (
	"database/sql"
	"net/url"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestDBName is the dedicated database the test suite runs against. Tests must
// never touch a console database: profile and finding fixtures would poison a
// running instance. EnsureTestDB rewrites JCYBER_PG_DSN to point here.
const TestDBName = "jcyber_test"

// EnsureTestDB rewrites the configured DSN to the dedicated test database,
// creating it if missing. No-op when no DSN is configured (tests skip) or
// when the DSN already points at the test database. Call it first in TestMain.
func EnsureTestDB() {
	dsn, _, err := DSN()
	if err != nil {
		return
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return
	}
	if u.Path == "/"+TestDBName || u.Path == TestDBName {
		return
	}
	admin := *u
	admin.Path = "/postgres"
	conn, err := sql.Open("pgx", admin.String())
	if err != nil {
		return
	}
	defer conn.Close()
	if err := conn.Ping(); err != nil {
		return
	}
	var exists bool
	if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, TestDBName).Scan(&exists); err != nil {
		return
	}
	if !exists {
		if _, err := conn.Exec(`CREATE DATABASE ` + TestDBName); err != nil {
			return
		}
	}
	u.Path = "/" + TestDBName
	os.Setenv("JCYBER_PG_DSN", u.String())
}

// ResetTestSchema drops and recreates the public schema on the test database,
// giving each package's test run a clean start. Refuses to touch anything
// that is not the dedicated test database.
func ResetTestSchema() {
	dsn, _, err := DSN()
	if err != nil {
		return
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Path != "/"+TestDBName {
		return // never reset a console database
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return
	}
	defer conn.Close()
	if err := conn.Ping(); err != nil {
		return
	}
	_, _ = conn.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
}
