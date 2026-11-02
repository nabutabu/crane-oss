package capacity

import (
	"context"
	"testing"

	"github.com/nabutabu/crane-oss/pkg/api"
)

func TestSetDominatorRouteRejectsPublicOrWrongIdentityBeforeDatabase(t *testing.T) {
	store := &Store{}
	for _, result := range []api.BootstrapStepResult{
		{DominatorEndpoint: "http://10.80.1.20:44000", DominatorIdentity: "spiffe://crane.internal/dominator/zone-b"},
		{DominatorEndpoint: "https://8.8.8.8:44000", DominatorIdentity: "spiffe://crane.internal/dominator/zone-b"},
		{DominatorEndpoint: "https://10.80.1.20:44000", DominatorIdentity: "spiffe://crane.internal/dominator/zone-c"},
	} {
		if err := store.SetDominatorRoute(context.Background(), api.Operation{ID: "op", ZoneID: "zone-b"}, result); err != ErrInvalid {
			t.Fatalf("expected invalid route, got %v", err)
		}
	}
}
