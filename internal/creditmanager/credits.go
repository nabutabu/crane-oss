package creditmanager

type CreditStore interface {
	Get(poolID string) int
	Add(poolID string, delta int)
}

type Manager interface {
	CanReserve(poolID string, amount int) (bool, error)
	Reserve(poolID string, amount int) error
	Release(poolID string, amount int) error
}
