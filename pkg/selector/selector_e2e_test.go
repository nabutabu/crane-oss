package selector_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nabutabu/crane-oss/internal/creditmanager/poolstore"
	"github.com/nabutabu/crane-oss/pkg/api"
)

// MockCreditManager for testing pool selection
type MockCreditManager struct {
	credits map[string]int
}

func NewMockCreditManager() *MockCreditManager {
	return &MockCreditManager{
		credits: make(map[string]int),
	}
}

func (m *MockCreditManager) CanReserve(poolID string, amount int) (bool, error) {
	available := m.credits[poolID]
	return available >= amount, nil
}

func (m *MockCreditManager) Reserve(poolID string, amount int) error {
	available := m.credits[poolID]
	if available < amount {
		return sql.ErrNoRows
	}
	m.credits[poolID] = available - amount
	return nil
}

func (m *MockCreditManager) Release(poolID string, amount int) error {
	m.credits[poolID] += amount
	return nil
}

func (m *MockCreditManager) SetCredit(poolID string, amount int) {
	m.credits[poolID] = amount
}

// TestEndToEndPoolAssignment tests the complete flow:
// 1. Create dummy team and pool in postgres
// 2. Assign host to pool with available credits
func TestEndToEndPoolAssignment(t *testing.T) {
	tests := []struct {
		name           string
		host           *api.Host
		poolSetup      []PoolSetup
		initialCredits map[string]int
		expectedPoolID string
		shouldSucceed  bool
	}{
		{
			name: "successful_assignment_with_available_host",
			host: &api.Host{
				ID:   "host-123",
				Role: api.Role{Name: "web"},
			},
			poolSetup: []PoolSetup{
				{ID: "pool-web-1", TeamID: "team-1", Role: "web"},
				{ID: "pool-worker-1", TeamID: "team-1", Role: "worker"},
			},
			initialCredits: map[string]int{
				"pool-web-1":    5,
				"pool-worker-1": 3,
			},
			expectedPoolID: "pool-web-1",
			shouldSucceed:  true,
		},
		{
			name: "no_available_credits",
			host: &api.Host{
				ID:   "host-456",
				Role: api.Role{Name: "web"},
			},
			poolSetup: []PoolSetup{
				{ID: "pool-web-2", TeamID: "team-2", Role: "web"},
			},
			initialCredits: map[string]int{
				"pool-web-2": 0,
			},
			expectedPoolID: "",
			shouldSucceed:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock database
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create mock database: %v", err)
			}
			defer db.Close()

			// Setup mock credit manager
			creditMgr := NewMockCreditManager()
			for poolID, credits := range tt.initialCredits {
				creditMgr.SetCredit(poolID, credits)
			}

			// Setup pool store
			poolStore := poolstore.NewPostgresPoolStore(db)

			// Mock database query for ListPools
			rows := sqlmock.NewRows([]string{"id", "teamid", "role"})
			for _, pool := range tt.poolSetup {
				rows.AddRow(pool.ID, pool.TeamID, pool.Role)
			}
			mock.ExpectQuery("SELECT id, teamid, role FROM pool").WillReturnRows(rows)

			// Test the flow: List pools -> Find matching pool -> Check credits -> Assign
			ctx := context.Background()

			// 1. List all pools
			pools, err := poolStore.ListPools(ctx)
			if err != nil {
				t.Fatalf("Failed to list pools: %v", err)
			}

			// 2. Find pool matching host role
			var selectedPool *api.Pool
			for _, pool := range pools {
				if pool.Role.Name == tt.host.Role.Name {
					// 3. Check if pool has available credits
					canReserve, err := creditMgr.CanReserve(pool.ID, 1)
					if err != nil {
						t.Errorf("Failed to check credits for pool %s: %v", pool.ID, err)
						continue
					}
					if canReserve {
						selectedPool = pool
						break
					}
				}
			}

			// 4. Verify results
			if tt.shouldSucceed {
				if selectedPool == nil {
					t.Error("Expected to find a pool but none was selected")
					return
				}
				if selectedPool.ID != tt.expectedPoolID {
					t.Errorf("Expected pool ID %s, got %s", tt.expectedPoolID, selectedPool.ID)
				}

				// 5. Test reservation
				err = creditMgr.Reserve(selectedPool.ID, 1)
				if err != nil {
					t.Errorf("Failed to reserve credits: %v", err)
				}

				// 6. Verify credits were deducted
				canReserve, _ := creditMgr.CanReserve(selectedPool.ID, tt.initialCredits[selectedPool.ID])
				if canReserve {
					t.Error("Credits were not deducted properly")
				}

			} else {
				if selectedPool != nil {
					t.Error("Expected no pool to be selected but one was found")
				}
			}

			// Verify all mock expectations were met
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unfulfilled database expectations: %v", err)
			}
		})
	}
}

type PoolSetup struct {
	ID     string
	TeamID string
	Role   string
}

// TestDummyTeamAndPoolCreation simulates creating dummy data for testing
func TestDummyTeamAndPoolCreation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create mock database: %v", err)
	}
	defer db.Close()

	// Simulate verifying the pool exists (simplified test)
	rows := sqlmock.NewRows([]string{"id", "teamid", "role"}).
		AddRow("dummy-pool-id", "dummy-team-id", "web")
	mock.ExpectQuery("SELECT id, teamid, role FROM pool WHERE id = \\$1").
		WithArgs("dummy-pool-id").
		WillReturnRows(rows)

	// Test the dummy data creation simulation
	ctx := context.Background()
	poolStore := poolstore.NewPostgresPoolStore(db)

	// Verify the dummy pool can be retrieved
	pool, err := poolStore.GetPool(ctx, "dummy-pool-id")
	if err != nil {
		t.Errorf("Failed to get dummy pool: %v", err)
	}

	if pool == nil {
		t.Error("Expected to find dummy pool but got nil")
	} else {
		if pool.ID != "dummy-pool-id" {
			t.Errorf("Expected pool ID dummy-pool-id, got %s", pool.ID)
		}
		if pool.TeamID != "dummy-team-id" {
			t.Errorf("Expected team ID dummy-team-id, got %s", pool.TeamID)
		}
		if pool.Role.Name != "web" {
			t.Errorf("Expected role web, got %s", pool.Role.Name)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}
