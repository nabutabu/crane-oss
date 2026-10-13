package logging

import (
	"bytes"
	"errors"
	stdlog "log"
	"log/slog"
	"strings"
	"testing"
)

func capture(t *testing.T, fn func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	handler := NewRedactHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fn(slog.New(handler))
	return buf.String()
}

func assertRedacted(t *testing.T, out string, secrets ...string) {
	t.Helper()
	if !strings.Contains(out, Redacted) {
		t.Fatalf("expected %q in output, got: %s", Redacted, out)
	}
	for _, secret := range secrets {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked into output: %s", secret, out)
		}
	}
}

func TestRedactHandlerSensitiveKeys(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("connected",
			"password", "hunter2",
			"db_password", "correct-horse",
			"secret", "shh",
			"api_key", "ak-123",
			"token", "tok-abc",
			"authorization", "Bearer xyz",
		)
	})
	assertRedacted(t, out, "hunter2", "correct-horse", "shh", "ak-123", "tok-abc", "Bearer xyz")
}

func TestRedactionCannotBeBypassedByTagsOrErrorText(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Error("request failed", "error", errors.New(`Authorization: Bearer bearer-secret, response={"password":"json-secret"}`),
			"config", struct {
				Password string `json:"value"`
			}{Password: "tag-secret"})
	})
	assertRedacted(t, out, "bearer-secret", "json-secret", "tag-secret")
}

func TestRedactHandlerNestedGroups(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("config",
			slog.Group("config",
				slog.String("host", "db.internal"),
				slog.String("password", "nested-secret"),
				slog.Group("tls",
					slog.String("token", "nested-token"),
					slog.String("ca", "ca.pem"),
				),
			),
		)
	})
	assertRedacted(t, out, "nested-secret", "nested-token")
	if !strings.Contains(out, "db.internal") || !strings.Contains(out, "ca.pem") {
		t.Fatalf("non-sensitive nested values were removed: %s", out)
	}
}

func TestRedactHandlerSensitiveGroupName(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("x", slog.Group("credentials",
			slog.String("user", "u"),
			slog.String("value", "group-secret"),
		))
	})
	assertRedacted(t, out, "group-secret")
	if strings.Contains(out, "\"user\"") {
		t.Fatalf("whole sensitive group should be redacted: %s", out)
	}
}

func TestRedactHandlerDSNs(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("connecting",
			"target", "postgres://crane:s3cr3t@db.internal:5432/crane",
			"note", "host=localhost user=postgres password=kv-secret dbname=crane sslmode=disable",
		)
	})
	assertRedacted(t, out, "s3cr3t", "kv-secret")
	if !strings.Contains(out, "db.internal") {
		t.Fatalf("non-secret DSN parts should remain: %s", out)
	}
}

func TestRedactHandlerMessageDSN(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Warn("dial failed host=localhost password=msg-secret sslmode=disable")
	})
	assertRedacted(t, out, "msg-secret")
}

func TestRedactHandlerErrors(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Error("connection failed",
			"error", errors.New("dial postgres://crane:err-secret@db:5432/crane: connection refused"),
		)
	})
	assertRedacted(t, out, "err-secret")
	if !strings.Contains(out, "connection refused") {
		t.Fatalf("error context should remain: %s", out)
	}
}

func TestRedactHandlerRawBody(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("request",
			"payload", []byte("raw-body-bytes"),
			"raw_body", "raw-body-string",
		)
	})
	assertRedacted(t, out, "raw-body-bytes", "raw-body-string")
}

type testCreds struct {
	User     string            `json:"user"`
	Password string            `json:"password"`
	Meta     map[string]any    `json:"meta"`
	Labels   map[string]string `json:"labels"`
}

func TestRedactHandlerAnyMap(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("map",
			"labels", map[string]any{
				"env":      "prod",
				"password": "map-secret",
				"nested": map[string]any{
					"token": "nested-map-secret",
					"zone":  "us-west-2",
				},
			},
		)
	})
	assertRedacted(t, out, "map-secret", "nested-map-secret")
	for _, want := range []string{"prod", "us-west-2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected non-secret %q in output: %s", want, out)
		}
	}
}

func TestRedactHandlerAnyStruct(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("struct",
			"account", testCreds{
				User:     "alice",
				Password: "struct-secret",
				Meta:     map[string]any{"api_key": "struct-api-key", "region": "eu"},
				Labels:   map[string]string{"role": "spire", "token": "struct-map-token"},
			},
		)
	})
	assertRedacted(t, out, "struct-secret", "struct-api-key", "struct-map-token")
	for _, want := range []string{"alice", "spire", "eu"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected non-secret %q in output: %s", want, out)
		}
	}
}

func TestRedactHandlerAnyPointerStruct(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("ptr", "account", &testCreds{User: "bob", Password: "ptr-secret"})
	})
	assertRedacted(t, out, "ptr-secret")
	if !strings.Contains(out, "bob") {
		t.Fatalf("expected non-secret field in output: %s", out)
	}
}

func TestRedactHandlerSensitiveWithGroup(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.WithGroup("credentials").Info("auth",
			"user", "secret-user",
			"token", "secret-token",
			slog.Group("nested", slog.String("value", "secret-nested")),
		)
	})
	assertRedacted(t, out, "secret-user", "secret-token", "secret-nested")
}

func TestRedactHandlerSafeWithGroup(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.WithGroup("request").Info("handled", "path", "/v1/health", "password", "still-secret")
	})
	assertRedacted(t, out, "still-secret")
	for _, want := range []string{"request", "/v1/health"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output: %s", want, out)
		}
	}
}

func TestRedactHandlerAttachedAttrs(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.With(
			"password", "attached-secret",
			slog.Group("db", slog.String("token", "attached-token"), slog.String("host", "h")),
		).Info("hello")
	})
	assertRedacted(t, out, "attached-secret", "attached-token")
	if !strings.Contains(out, "\"host\":\"h\"") {
		t.Fatalf("non-sensitive attached value missing: %s", out)
	}
}

func TestRedactHandlerPreservesNonSensitive(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("serving", "host", "db.internal", "port", 5432, "healthy", true)
	})
	for _, want := range []string{"db.internal", "5432", "true", "serving"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output: %s", want, out)
		}
	}
	if strings.Contains(out, Redacted) {
		t.Fatalf("unexpected redaction: %s", out)
	}
}

func TestSetDefaultRoutesStdlibLog(t *testing.T) {
	prevSlog := slog.Default()
	prevOut := stdlog.Writer()
	prevFlags := stdlog.Flags()
	prevPrefix := stdlog.Prefix()
	defer func() {
		slog.SetDefault(prevSlog)
		stdlog.SetOutput(prevOut)
		stdlog.SetFlags(prevFlags)
		stdlog.SetPrefix(prevPrefix)
	}()

	var buf bytes.Buffer
	logger := &Logger{Logger: slog.New(NewRedactHandler(slog.NewJSONHandler(&buf, nil)))}
	logger.SetDefault()

	stdlog.Print("legacy logger password=stdlib-secret")

	out := buf.String()
	assertRedacted(t, out, "stdlib-secret")
	if !strings.Contains(out, "legacy logger") {
		t.Fatalf("stdlib log message not routed: %s", out)
	}
}
