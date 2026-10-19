package subd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestExplicitChangesOnlyAndPartialFailure(t *testing.T) {
	var calls [][]string
	h := LocalHost{run: func(_ context.Context, cmd string, args ...string) error {
		calls = append(calls, append([]string{cmd}, args...))
		return nil
	}}
	if err := h.Apply(context.Background(), api.ExpectedHostState{Revision: 1}); err != nil || len(calls) != 0 {
		t.Fatal("empty state must not change inventory", err, calls)
	}
	in := api.ExpectedHostState{Revision: 2, Services: map[string]bool{"z.service": false, "a.service": true}, Packages: []api.HostPackageChange{{Name: "curl", Action: "install"}, {Name: "example", Action: "remove"}}}
	if err := h.Apply(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"systemctl", "start", "--", "a.service"}, {"systemctl", "stop", "--", "z.service"}, {"apt-get", "-y", "install", "--", "curl"}, {"apt-get", "-y", "remove", "--", "example"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatal(calls)
	}
	calls = nil
	h.run = func(_ context.Context, cmd string, args ...string) error {
		calls = append(calls, append([]string{cmd}, args...))
		return errors.New("private command output")
	}
	if err := h.Apply(context.Background(), in); err == nil || err.Error() != "service update failed" || len(calls) != 1 {
		t.Fatal("failure did not stop application", err, calls)
	}
}
