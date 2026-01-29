# CreditManager Testing Guide

This document explains how to test the CreditManager part of your codebase using end-to-end tests that simulate dummy teams and pools in PostgreSQL.

## Overview

The CreditManager tests verify that:
1. Pools can be created and retrieved from PostgreSQL
2. Credit management works correctly (CanReserve, Reserve, Release)
3. Hosts are correctly assigned to pools with available credits
4. Pool selection follows the role-matching logic

## Test Files

### 1. `internal/creditmanager/creditmanager_test.go`

Tests the core credit management functionality:
- `TestPoolStoreIntegration`: Tests PostgreSQL pool store with mocked database
- `TestCreditManagerFlow`: Tests credit reservation/release flow

### 2. `pkg/selector/selector_e2e_test.go`

Tests the end-to-end pool assignment flow:
- `TestEndToEndPoolAssignment`: Complete flow from dummy team/pool creation to host assignment
- `TestDummyTeamAndPoolCreation`: Simulates dummy data creation

## Running Tests

### Run All CreditManager Tests
```bash
go test ./internal/creditmanager/... -v
```

### Run All Selector Tests
```bash
go test ./pkg/selector/... -v
```

### Run Specific Test
```bash
go test ./pkg/selector -v -run TestEndToEndPoolAssignment
```

### Run Tests with Coverage
```bash
go test ./... -cover -v
```

## Test Structure

### Dummy Data Setup

The tests create mock data using these structures:

```go
type PoolSetup struct {
    ID     string
    TeamID string
    Role   string
}

type MockCreditManager struct {
    credits map[string]int
}
```

### Test Scenarios

1. **Successful Assignment**: 
   - Creates dummy team and pool with credits
   - Host with matching role gets assigned to pool
   - Credits are properly reserved

2. **No Available Credits**:
   - Pool exists but has zero credits
   - Host should not be assigned
   - Credits reservation should fail

3. **Pool Store Operations**:
   - Test pool creation and retrieval
   - Test error handling for missing pools

## Integration with Real Database

For testing with a real PostgreSQL database:

1. Set up a test database
2. Use environment variables for connection
3. Create tables with proper schema
4. Insert test data
5. Run tests against real DB

Example integration test structure:
```go
func TestRealDatabaseIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }
    
    // Connect to real test database
    db, err := sql.Open("postgres", testConnectionString)
    // ... rest of test
}
```

## Key Test Assertions

The tests verify:

1. **Pool Matching**: Host role matches pool role
2. **Credit Availability**: Pool has sufficient credits before assignment
3. **Credit Deduction**: Credits are properly deducted after reservation
4. **Error Handling**: Proper error responses for edge cases
5. **Data Integrity**: Database queries return expected results

## Mocking Strategy

The tests use `sqlmock` for database operations:
- Mock SQL queries and responses
- Verify query expectations are met
- Avoid dependency on real database during unit tests

## Best Practices

1. **Isolation**: Each test runs independently
2. **Cleanup**: Mock expectations are verified
3. **Edge Cases**: Test both success and failure scenarios
4. **Realistic Data**: Use meaningful pool/role combinations

## Future Enhancements

Consider adding:

1. **Concurrent Tests**: Test multiple hosts competing for same pool
2. **Credit Expiration**: Test time-based credit management
3. **Pool Capacity**: Test maximum host limits per pool
4. **Integration Tests**: Real database integration testing