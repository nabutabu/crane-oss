package creditmanager_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nabutabu/crane-oss/internal/creditmanager/poolstore"
)

// MockCreditManager implements the credit manager interface for testing
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

// TestPoolStoreIntegration tests the pool store with mocked database
func TestPoolStoreIntegration(t *testing.T) {
	tests := []struct {
		name        string
		poolID      string
		teamID      string
		role        string
		shouldFind  bool
		expectError bool
	}{
		{
			name:        "pool_found",
			poolID:      "pool-1",
			teamID:      "team-1",
			role:        "web",
			shouldFind:  true,
			expectError: false,
		},
		{
			name:        "pool_not_found",
			poolID:      "nonexistent",
			teamID:      "",
			role:        "",
			shouldFind:  false,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create mock database: %v", err)
			}
			defer db.Close()

			store := poolstore.NewPostgresPoolStore(db)

			if tt.shouldFind {
				rows := sqlmock.NewRows([]string{"id", "teamid", "role"}).
					AddRow(tt.poolID, tt.teamID, tt.role)
				mock.ExpectQuery("SELECT id, teamid, role FROM pool WHERE id = \\$1").
					WithArgs(tt.poolID).
					WillReturnRows(rows)
			} else {
				mock.ExpectQuery("SELECT id, teamid, role FROM pool WHERE id = \\$1").
					WithArgs(tt.poolID).
					WillReturnError(sql.ErrNoRows)
			}

			pool, err := store.GetPool(context.Background(), tt.poolID)

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if tt.shouldFind {
				if pool == nil {
					t.Error("Expected pool but got nil")
				} else {
					if pool.ID != tt.poolID {
						t.Errorf("Expected pool ID %s, got %s", tt.poolID, pool.ID)
					}
					if pool.TeamID != tt.teamID {
						t.Errorf("Expected team ID %s, got %s", tt.teamID, pool.TeamID)
					}
					if pool.Role.Name != tt.role {
						t.Errorf("Expected role %s, got %s", tt.role, pool.Role.Name)
					}
				}
			} else {
				if pool != nil {
					t.Error("Expected nil pool but got one")
				}
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unfulfilled expectations: %v", err)
			}
		})
	}
}

// TestCreditManagerFlow tests the credit management flow
func TestCreditManagerFlow(t *testing.T) {
	creditMgr := NewMockCreditManager()
	poolID := "test-pool"

	// Set initial credits
	creditMgr.SetCredit(poolID, 10)

	// Test CanReserve with sufficient credits
	canReserve, err := creditMgr.CanReserve(poolID, 5)
	if err != nil {
		t.Errorf("CanReserve failed: %v", err)
	}
	if !canReserve {
		t.Error("Expected CanReserve to return true with sufficient credits")
	}

	// Test Reserve
	err = creditMgr.Reserve(poolID, 5)
	if err != nil {
		t.Errorf("Reserve failed: %v", err)
	}

	// Test CanReserve after reservation
	canReserve, err = creditMgr.CanReserve(poolID, 6)
	if err != nil {
		t.Errorf("CanReserve failed: %v", err)
	}
	if canReserve {
		t.Error("Expected CanReserve to return false with insufficient credits")
	}

	// Test Release
	err = creditMgr.Release(poolID, 3)
	if err != nil {
		t.Errorf("Release failed: %v", err)
	}

	// Test CanReserve after release
	canReserve, err = creditMgr.CanReserve(poolID, 6)
	if err != nil {
		t.Errorf("CanReserve failed: %v", err)
	}
	if !canReserve {
		t.Error("Expected CanReserve to return true after release")
	}

	// Test Reserve with insufficient credits
	err = creditMgr.Reserve(poolID, 20)
	if err == nil {
		t.Error("Expected Reserve to fail with insufficient credits")
	}
}
