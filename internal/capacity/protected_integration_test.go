package capacity

import (
	"context"
	"errors"
	"testing"

	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/store"
)

func TestProtectedBootstrapInventory(t *testing.T) {
	db := database(t)
	mustExec(t, db, `INSERT INTO host(id,role,zone,imageid,state,health,createdat,provider,providerid,protected,subd_endpoint) VALUES('bootstrap','platform','zone','ami-bootstrap','CONVERGING','unknown',clock_timestamp(),'aws','provider-bootstrap',true,'https://10.0.0.10:44001')`)
	catalog := service.NewHostCatalogService(store.NewPostgresHostStore(db))
	for _, err := range []error{catalog.EnsureLegacyLifecycle(context.Background(), "bootstrap"), catalog.DeleteHost(context.Background(), "bootstrap"), catalog.UpdateState(context.Background(), "bootstrap", "TERMINATED"), catalog.UpdateHostProviderID(context.Background(), "bootstrap", "other")} {
		if !errors.Is(err, service.ErrProtectedHost) {
			t.Fatal(err)
		}
	}
	h, err := NewStore(db).Host(context.Background(), "bootstrap")
	if err != nil || !h.Protected || h.SubdEndpoint != "https://10.0.0.10:44001" || h.NodeGroupID != "" || h.Health != "unknown" || !h.LastSeenHeartbeat.IsZero() {
		t.Fatalf("unexpected bootstrap inventory: %+v %v", h, err)
	}
	var credits int
	if err := db.QueryRow(`SELECT count(*) FROM credit_reservations WHERE host_id='bootstrap'`).Scan(&credits); err != nil || credits != 0 {
		t.Fatal("bootstrap charged worker credits", err)
	}
}
