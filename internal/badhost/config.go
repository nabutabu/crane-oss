package badhost

import "time"

type Config struct {
	Zone         string
	ScanInterval time.Duration
	Checks       []string
}

func NewConfig(zone string, interval time.Duration, checks []string) *Config {
	return &Config{
		Zone:         zone,
		ScanInterval: interval,
		Checks: checks,
	}
}
