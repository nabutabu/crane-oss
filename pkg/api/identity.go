package api

import (
	"errors"
	"strings"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

// DominatorID returns the exact workload identity for a zone's Dominator.
func DominatorID(trustDomain, zoneID string) (spiffeid.ID, error) {
	if strings.TrimSpace(trustDomain) == "" || strings.TrimSpace(zoneID) == "" || zoneID != strings.TrimSpace(zoneID) {
		return spiffeid.ID{}, errors.New("trust domain and zone ID are required")
	}
	td, err := spiffeid.TrustDomainFromString(trustDomain)
	if err != nil {
		return spiffeid.ID{}, errors.New("invalid SPIFFE trust domain")
	}
	id, err := spiffeid.FromPath(td, "/dominator/"+zoneID)
	if err != nil {
		return spiffeid.ID{}, errors.New("invalid zone-scoped Dominator identity")
	}
	return id, nil
}
