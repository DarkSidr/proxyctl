package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"proxyctl/internal/domain"
)

func TestNodesCreateRejectsSecondPrimary(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "proxyctl.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer store.Close()
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	if _, err := store.Nodes().Create(context.Background(), domain.Node{
		Name:    "Primary-1",
		Host:    "one.example.com",
		Role:    domain.NodeRolePrimary,
		Enabled: true,
	}); err != nil {
		t.Fatalf("create first primary node: %v", err)
	}

	_, err = store.Nodes().Create(context.Background(), domain.Node{
		Name:    "Primary-2",
		Host:    "two.example.com",
		Role:    domain.NodeRolePrimary,
		Enabled: true,
	})
	if err == nil {
		t.Fatalf("create second primary node expected error, got nil")
	}
	if !strings.Contains(err.Error(), "only one primary node is allowed") {
		t.Fatalf("create second primary node error = %q", err)
	}
}

func TestNodesUpdateRejectsPromoteWhenPrimaryExists(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "proxyctl.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer store.Close()
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	primary, err := store.Nodes().Create(context.Background(), domain.Node{
		Name:    "Primary-1",
		Host:    "one.example.com",
		Role:    domain.NodeRolePrimary,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create primary node: %v", err)
	}
	worker, err := store.Nodes().Create(context.Background(), domain.Node{
		Name:    "Node-1",
		Host:    "two.example.com",
		Role:    domain.NodeRoleNode,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create worker node: %v", err)
	}

	_, err = store.Nodes().Update(context.Background(), domain.Node{
		ID:      worker.ID,
		Name:    worker.Name,
		Host:    worker.Host,
		Role:    domain.NodeRolePrimary,
		Enabled: worker.Enabled,
	})
	if err == nil {
		t.Fatalf("promote worker to primary expected error, got nil")
	}
	if !strings.Contains(err.Error(), "only one primary node is allowed") {
		t.Fatalf("promote worker to primary error = %q", err)
	}

	_, err = store.Nodes().Update(context.Background(), domain.Node{
		ID:      primary.ID,
		Name:    primary.Name,
		Host:    primary.Host,
		Role:    domain.NodeRolePrimary,
		Enabled: primary.Enabled,
	})
	if err != nil {
		t.Fatalf("update existing primary node should pass, got: %v", err)
	}
}
