package capacity

import (
	"context"
	"fmt"
	"net"
	"regexp"

	"github.com/nabutabu/crane-oss/pkg/api"
)

var providerInstanceID = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,200}$`)

// RecordBootstrapHost records the provider-owned protected host after the
// infrastructure step. The record is deliberately outside node groups, so
// capacity admission cannot provision or terminate it as worker capacity.
func (s *Store) RecordBootstrapHost(ctx context.Context, op api.Operation, result api.BootstrapStepResult) error {
	if result.ProviderHostID == "" || !providerInstanceID.MatchString(result.ProviderHostID) {
		return ErrInvalid
	}
	ip := net.ParseIP(result.HostPrivateIP)
	if ip == nil || !ip.IsPrivate() || result.ImageID == "" {
		return ErrInvalid
	}
	role := "cluster-control-plane"
	if op.Kind == "attach_zone" {
		role = "zone-dominator-host"
	}
	hostID := "bootstrap/" + op.ID
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var clusterID string
	if err = tx.QueryRowContext(ctx, `SELECT cluster_id FROM zone_bootstrap_operations WHERE operation_id=$1`, op.ID).Scan(&clusterID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO host(id,role,zone,imageid,state,health,createdat,provider,providerid,protected,subd_endpoint,crane_zone)
 VALUES($1,$2,$3,$4,'CONVERGING','unknown',clock_timestamp(),'aws',$5,true,$6,$3)
 ON CONFLICT(id) DO UPDATE SET role=EXCLUDED.role,zone=EXCLUDED.zone,imageid=EXCLUDED.imageid,provider=EXCLUDED.provider,providerid=EXCLUDED.providerid,protected=true,subd_endpoint=EXCLUDED.subd_endpoint
 WHERE host.provider=EXCLUDED.provider AND host.providerid=EXCLUDED.providerid AND host.protected`, hostID, role, op.ZoneID, result.ImageID, result.ProviderHostID, "https://"+result.HostPrivateIP+":44001")
	if err != nil {
		return err
	}
	var stored string
	if err = tx.QueryRowContext(ctx, `SELECT providerid FROM host WHERE id=$1`, hostID).Scan(&stored); err != nil {
		return err
	}
	if stored != result.ProviderHostID {
		return fmt.Errorf("bootstrap host identity conflict")
	}
	_ = clusterID // ownership is carried by the operation's zone membership.
	return tx.Commit()
}
