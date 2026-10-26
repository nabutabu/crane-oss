package hostrpc

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeadlineCannotBeExtended(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := httptest.NewRequest("PUT", "/", nil).WithContext(parent)
	r.Header.Set(DeadlineHeader, time.Now().Add(time.Hour).Format(time.RFC3339Nano))
	ctx, cleanup, err := Context(r)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, _ := ctx.Deadline()
	want, _ := parent.Deadline()
	if !got.Equal(want) {
		t.Fatal("caller deadline extended")
	}
	earlier := time.Now().Add(100 * time.Millisecond)
	r.Header.Set(DeadlineHeader, earlier.Format(time.RFC3339Nano))
	ctx, cleanup, err = Context(r)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, _ = ctx.Deadline()
	if !got.Equal(earlier) {
		t.Fatal("downstream deadline lost")
	}
	r.Header.Set(DeadlineHeader, "invalid")
	if _, _, err := Context(r); err == nil {
		t.Fatal("invalid deadline accepted")
	}
}
