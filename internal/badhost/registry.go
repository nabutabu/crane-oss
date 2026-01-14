package badhost

type Registry struct {
	checks []Check
}

func NewRegistry(checks ...Check) *Registry {
	return &Registry{
		checks: checks,
	}
}

func (r *Registry) Detectors() []Check {
	return r.checks
}
