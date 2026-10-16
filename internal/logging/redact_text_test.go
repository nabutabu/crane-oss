package logging

import "testing"

func TestRedactText(t *testing.T) {
	got := RedactText(`password=secret postgres://user:pw@example/db token:abc`)
	for _, secret := range []string{"secret", "postgres://user:pw", "abc"} {
		if contains(got, secret) {
			t.Fatalf("output contains %q: %s", secret, got)
		}
	}
}

func contains(s, part string) bool {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
