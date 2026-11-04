package capacity

import (
	"context"
	"errors"
	"github.com/nabutabu/crane-oss/pkg/api"
	"testing"
)

func TestPostgresCanonicalRegionMapping(t *testing.T) {
	s := NewStore(database(t))
	ctx := context.Background()
	zone := api.ZoneInput{ID: "mapping-zone", Provider: "fixture-provider", AccountID: "account", Region: "native-region", ProviderZoneID: "native-az-1", KubernetesRegion: "canonical-region", KubernetesZone: "canonical-zone-1"}
	if err := s.CreateZone(ctx, zone); err != nil {
		t.Fatal(err)
	}
	zone.ID = "mapping-zone-2"
	zone.ProviderZoneID = "native-az-2"
	zone.KubernetesZone = "canonical-zone-2"
	zone.KubernetesRegion = "different-region-label"
	if err := s.CreateZone(ctx, zone); !errors.Is(err, ErrConflict) {
		t.Fatalf("one native region advertised with different labels: %v", err)
	}
	zone.KubernetesRegion = "canonical-region"
	if err := s.CreateZone(ctx, zone); err != nil {
		t.Fatal(err)
	}
	zone.ID = "mapping-zone-3"
	zone.ProviderZoneID = "native-az-3"
	zone.KubernetesZone = "canonical-zone-3"
	zone.AccountID = "another-account"
	if err := s.CreateZone(ctx, zone); !errors.Is(err, ErrConflict) {
		t.Fatalf("region label collision across accounts: %v", err)
	}
}
