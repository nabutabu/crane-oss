package dominator

func Resolve(hostID string) DesiredState {
	return DesiredState{
		ImageID: "ami-stable-001",
		Track:   "stable",
		Version: "2026.02.01",
	}
}
