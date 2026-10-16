package config

import (
	"net/url"
	"testing"
)

func TestDatabaseURLPreservesTLSAndEscapesPassword(t *testing.T) {
	t.Setenv("CRANE_DATABASE_URL", "")
	t.Setenv("DB_PASSWORD", "a'b@ /%")
	t.Setenv("DB_SSLMODE", "verify-full")
	t.Setenv("DB_SSLROOTCERT", "/etc/crane/rds-ca.pem")
	u, err := url.Parse(DatabaseURL(5432))
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if password != "a'b@ /%" || u.Query().Get("sslmode") != "verify-full" || u.Query().Get("sslrootcert") != "/etc/crane/rds-ca.pem" {
		t.Fatal("DSN lost credentials or TLS configuration")
	}
}

func TestDatabaseURLOverride(t *testing.T) {
	t.Setenv("CRANE_DATABASE_URL", "postgres://configured/db?sslmode=verify-full")
	if got := DatabaseURL(5432); got != "postgres://configured/db?sslmode=verify-full" {
		t.Fatal(got)
	}
}
