package config

import (
	"net/url"
	"os"
	"strconv"
)

// DatabaseURL permits a TLS-verified bootstrap DSN while retaining local defaults.
func DatabaseURL(defaultPort int) string {
	if dsn := os.Getenv("CRANE_DATABASE_URL"); dsn != "" {
		return dsn
	}
	u := &url.URL{Scheme: "postgres", Host: GetEnv("DB_HOST", "localhost") + ":" + strconv.Itoa(GetEnvInt("DB_PORT", defaultPort)), Path: "/" + GetEnv("DB_NAME", "crane"), User: url.UserPassword(GetEnv("DB_USER", "postgres"), GetEnv("DB_PASSWORD", "REDACTED_DB_PASSWORD"))}
	q := u.Query()
	q.Set("sslmode", GetEnv("DB_SSLMODE", "disable"))
	if ca := os.Getenv("DB_SSLROOTCERT"); ca != "" {
		q.Set("sslrootcert", ca)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
