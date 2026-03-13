package runtime

// UnitStatus represents a simplified systemd unit state.
type UnitStatus string

const (
	UnitStatusActive   UnitStatus = "active"
	UnitStatusInactive UnitStatus = "inactive"
	UnitStatusFailed   UnitStatus = "failed"
)

// ServiceStatus is the runtime status snapshot for one unit.
type ServiceStatus struct {
	Unit   string
	Status UnitStatus
	Detail string
}

// Manager defines runtime control operations delegated to systemd.
type Manager interface {
	Status(unit string) (ServiceStatus, error)
	Restart(unit string) error
	Reload(unit string) error
}
