package systemd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Manager struct{}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) Restart(ctx context.Context, unit string) error {
	return m.run(ctx, "restart", unit)
}

func (m *Manager) Reload(ctx context.Context, unit string) error {
	return m.run(ctx, "reload", unit)
}

func (m *Manager) run(ctx context.Context, action, unit string) error {
	action = strings.TrimSpace(action)
	unit = strings.TrimSpace(unit)
	if action == "" {
		return fmt.Errorf("systemctl action is required")
	}
	if unit == "" {
		return fmt.Errorf("systemd unit is required")
	}

	cmd := exec.CommandContext(ctx, "systemctl", action, unit)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("systemctl %s %s failed: %w: %s", action, unit, err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("systemctl %s %s failed: %w", action, unit, err)
	}
	return nil
}
