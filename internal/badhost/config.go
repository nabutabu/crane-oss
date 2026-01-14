package badhost

import "time"

type Config struct {
	Zone         string
	ScanInterval time.Duration
}
