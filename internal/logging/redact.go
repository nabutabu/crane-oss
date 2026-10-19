package logging

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// Redacted is the placeholder written in place of sensitive data.
const Redacted = "[REDACTED]"

// maxRedactDepth bounds recursion into slog.Any values to avoid cycles.
const maxRedactDepth = 12

// sensitiveKeySubstrings are matched (case-insensitively, ignoring separators)
// against attribute and group keys. A match redacts the whole attribute.
var sensitiveKeySubstrings = []string{
	"password", "passwd", "pwd",
	"secret", "token", "credential",
	"authorization", "apikey", "accesskey", "secretkey", "privatekey",
	"dsn", "conn", "connection",
	"body", "rawbody", "cookie", "session",
}

// secretKeyValueRe redacts "key=value" / "key: value" secrets embedded in free
// text such as DSNs and error messages. The boundary group keeps surrounding
// characters (for example the underscore in db_password).
var secretKeyValueRe = regexp.MustCompile(
	`(?i)(^|[^A-Za-z0-9])(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|secret[_-]?key|dsn|authorization|credential)\s*[=:]\s*("[^"]*"|'[^']*'|\S+)`,
)

// urlCredentialRe redacts the password portion of a URL-style DSN, for example
// postgres://user:password@host:5432/db.
var urlCredentialRe = regexp.MustCompile(
	`(?i)([a-z][a-z0-9+.\-]*://[^:@/\s]+:)([^@\s]+)(@)`,
)

var authorizationRe = regexp.MustCompile(`(?i)(authorization\s*[=:]\s*(?:bearer|basic)\s+)[^\s,;]+`)

// RedactText applies the same conservative redaction used by structured logs
// to operator-visible command output.
func RedactText(s string) string { return redactString(s) }

var jsonSecretRe = regexp.MustCompile(`(?i)("(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|secret[_-]?key|authorization|credential)"\s*:\s*)"(?:\\.|[^"\\])*"`)

// RedactHandler is a slog.Handler that removes secrets before delegating to an
// inner handler. It redacts attributes whose key looks sensitive, DSN-like
// strings in any value or message, error values, raw byte slices, and nested
// secrets inside slog.Any maps and structs. A WithGroup scope whose name looks
// sensitive suppresses every child value.
type RedactHandler struct {
	inner    slog.Handler
	suppress bool
}

// NewRedactHandler wraps inner with redaction.
func NewRedactHandler(inner slog.Handler) *RedactHandler {
	return &RedactHandler{inner: inner}
}

// Enabled reports whether the wrapped handler is enabled for level.
func (h *RedactHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle redacts the record and forwards it to the wrapped handler.
func (h *RedactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, redactString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

// WithAttrs returns a handler that redacts the supplied attributes.
func (h *RedactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		if h.suppress {
			redacted[i] = slog.String(a.Key, Redacted)
			continue
		}
		redacted[i] = h.redactAttr(a)
	}
	return &RedactHandler{inner: h.inner.WithAttrs(redacted), suppress: h.suppress}
}

// WithGroup returns a handler scoped to the named group. If the group name
// looks sensitive, all values logged within it are suppressed.
func (h *RedactHandler) WithGroup(name string) slog.Handler {
	return &RedactHandler{
		inner:    h.inner.WithGroup(name),
		suppress: h.suppress || isSensitiveKey(name),
	}
}

func (h *RedactHandler) redactAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return a
	}
	if h.suppress || isSensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}

	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, redactString(a.Value.String()))
	case slog.KindGroup:
		group := a.Value.Group()
		attrs := make([]slog.Attr, len(group))
		for i, ga := range group {
			attrs[i] = h.redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(attrs...)}
	case slog.KindAny:
		switch v := a.Value.Any().(type) {
		case error:
			return slog.String(a.Key, redactString(v.Error()))
		case []byte:
			return slog.String(a.Key, Redacted)
		default:
			return slog.Any(a.Key, redactAny(v))
		}
	}
	return a
}

// redactAny sanitizes arbitrary values passed through slog.Any so nested
// secrets in maps, slices, pointers, and structs cannot escape.
func redactAny(v any) any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case error:
		return redactString(t.Error())
	case []byte:
		return Redacted
	case time.Time:
		return t
	}
	return redactReflect(reflect.ValueOf(v), 0)
}

func redactReflect(rv reflect.Value, depth int) any {
	if depth > maxRedactDepth || !rv.IsValid() {
		return Redacted
	}

	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return redactReflect(rv.Elem(), depth+1)
	case reflect.String:
		return redactString(rv.String())
	case reflect.Bool:
		return rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return Redacted
		}
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = redactReflect(rv.Index(i), depth+1)
		}
		return out
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			name := fmt.Sprint(iter.Key().Interface())
			if isSensitiveKey(name) {
				out[name] = Redacted
				continue
			}
			out[name] = redactReflect(iter.Value(), depth+1)
		}
		return out
	case reflect.Struct:
		if _, ok := rv.Interface().(time.Time); ok {
			return rv.Interface()
		}
		out := make(map[string]any)
		rt := rv.Type()
		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			if field.PkgPath != "" {
				continue // unexported; encoding/json would skip it too
			}
			name := field.Name
			if tag, ok := field.Tag.Lookup("json"); ok {
				parts := strings.Split(tag, ",")
				if parts[0] == "-" {
					continue
				}
				if parts[0] != "" {
					name = parts[0]
				}
			}
			if isSensitiveKey(name) || isSensitiveKey(field.Name) {
				out[name] = Redacted
				continue
			}
			out[name] = redactReflect(rv.Field(i), depth+1)
		}
		return out
	default:
		return Redacted
	}
}

// isSensitiveKey reports whether a key (or group name) names sensitive data.
func isSensitiveKey(key string) bool {
	normalized := strings.ToLower(key)
	normalized = strings.NewReplacer("-", "", "_", "", " ", "", ".", "").Replace(normalized)
	if normalized == "" {
		return false
	}
	for _, s := range sensitiveKeySubstrings {
		if strings.Contains(normalized, s) {
			return true
		}
	}
	return false
}

// redactString removes DSN credentials and inline key/value secrets from s.
func redactString(s string) string {
	if s == "" {
		return s
	}
	s = authorizationRe.ReplaceAllString(s, "${1}"+Redacted)
	s = jsonSecretRe.ReplaceAllString(s, "${1}\""+Redacted+"\"")
	s = secretKeyValueRe.ReplaceAllString(s, "${1}${2}="+Redacted)
	s = urlCredentialRe.ReplaceAllString(s, "${1}"+Redacted+"${3}")
	return s
}
