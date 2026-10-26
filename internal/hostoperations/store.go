package hostoperations

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"time"
)

type Target struct{ HostID, ZoneID, ProviderID, Endpoint, DominatorEndpoint, DominatorIdentity string }
type Store struct{ DB *sql.DB }
type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadTarget(ctx context.Context, q queryer, host string) (Target, error) {
	t := Target{HostID: host}
	var state string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(crane_zone,zone,''),COALESCE(providerid,''),subd_endpoint,state FROM host WHERE id=$1`, host).Scan(&t.ZoneID, &t.ProviderID, &t.Endpoint, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return t, fail(http.StatusNotFound, "host not found")
	}
	if err != nil {
		return t, fail(503, "host catalog unavailable")
	}
	if state == "DRAINING" || state == "TERMINATING" || state == "TERMINATED" || state == "FAILED" || t.ProviderID == "" || t.Endpoint == "" {
		return t, fail(409, "host unavailable for expected-state changes")
	}
	err = q.QueryRowContext(ctx, `SELECT endpoint,identity FROM zone_dominators WHERE zone_id=$1`, t.ZoneID).Scan(&t.DominatorEndpoint, &t.DominatorIdentity)
	if err != nil {
		return t, fail(503, "zone Dominator unavailable")
	}
	return t, nil
}

func (s Store) Target(ctx context.Context, host string) (Target, error) {
	return loadTarget(ctx, s.DB, host)
}

// A dedicated connection holds the host lock across committed allocation and
// remote execution. A process crash releases ownership without losing revision
// history. Subd's own serialization/revision fence protects in-flight delivery.
func (s Store) lock(ctx context.Context, host string) (*sql.Conn, func(), error) {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, fail(504, "host operation deadline exceeded")
		}
		return nil, nil, fail(503, "operation store unavailable")
	}
	for {
		var locked bool
		err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,724163012))`, host).Scan(&locked)
		if err != nil {
			// A cancelled query may have acquired the session lock before its
			// result was lost. Never return that connection to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			conn.Close()
			if ctx.Err() != nil {
				return nil, nil, fail(504, "host operation deadline exceeded")
			}
			return nil, nil, fail(503, "cannot acquire host operation ownership")
		}
		if locked {
			break
		}
		select {
		case <-ctx.Done():
			conn.Close()
			return nil, nil, fail(504, "host operation deadline exceeded")
		case <-time.After(25 * time.Millisecond):
		}
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRowContext(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,724163012))`, host).Scan(&unlocked); err != nil || !unlocked {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}
	return conn, release, nil
}

type operationError struct {
	status  int
	message string
}

func (e *operationError) Error() string     { return e.message }
func fail(status int, message string) error { return &operationError{status, message} }
func errorStatus(err error) int {
	var e *operationError
	if errors.As(err, &e) {
		return e.status
	}
	return 503
}
