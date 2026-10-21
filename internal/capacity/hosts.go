package capacity

import (
	"context"
	"net/http"
	"strings"

	"github.com/nabutabu/crane-oss/pkg/api"
)

// ListHosts uses canonical capacity placement, falling back to the legacy zone
// column for protected bootstrap inventory. Region is topology, not a host tag.
func (s *Store) ListHosts(ctx context.Context, provider, region string) ([]api.HostSummary, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT h.id,h.provider,COALESCE(h.providerid,''),z.region,z.id,
COALESCE(h.role,''),h.state,COALESCE(h.health,'unknown'),h.protected,
COALESCE(h.node_group_id,''),COALESCE(h.node_pool_id,''),h.desired_state_version,h.observed_state_version
FROM host h JOIN zones z ON z.id=COALESCE(h.crane_zone,h.zone)
WHERE h.provider=$1 AND z.provider=$1 AND z.region=$2 ORDER BY h.id`, provider, region)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hosts := make([]api.HostSummary, 0)
	for rows.Next() {
		var host api.HostSummary
		if err := rows.Scan(&host.ID, &host.Provider, &host.ProviderID, &host.Region, &host.ZoneID,
			&host.Role, &host.State, &host.Health, &host.Protected, &host.NodeGroupID, &host.NodePoolID,
			&host.DesiredStateVersion, &host.ObservedStateVersion); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hosts, nil
}

func (a *API) listHosts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	provider, region := strings.TrimSpace(query.Get("provider")), strings.TrimSpace(query.Get("region"))
	if len(query["provider"]) != 1 || len(query["region"]) != 1 || provider == "" || region == "" {
		http.Error(w, "one provider and one region are required", http.StatusBadRequest)
		return
	}
	hosts, err := a.store.ListHosts(r.Context(), provider, region)
	if err != nil {
		http.Error(w, "host catalog unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, api.HostList{Hosts: hosts})
}
