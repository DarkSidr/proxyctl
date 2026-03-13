package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"proxyctl/internal/domain"
	"proxyctl/internal/renderer/singbox"
	"proxyctl/internal/renderer/xray"
	"proxyctl/internal/storage/sqlite"
)

func TestGenerateBuildsSubscriptionFromMultipleInbounds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	user := mustCreateUser(t, ctx, store, "alice")
	_ = mustCreateUser(t, ctx, store, "bob")

	node1 := mustCreateNode(t, ctx, store, domain.Node{ID: "node-1", Name: "eu-1", Host: "eu.example.com", Role: domain.NodeRolePrimary, Enabled: true})
	node2 := mustCreateNode(t, ctx, store, domain.Node{ID: "node-2", Name: "us-1", Host: "us.example.com", Role: domain.NodeRolePrimary, Enabled: true})

	inVLESS := mustCreateInbound(t, ctx, store, domain.Inbound{ID: "in-vless", Type: domain.ProtocolVLESS, Engine: domain.EngineSingBox, NodeID: node1.ID, Domain: "eu.example.com", Port: 443, TLSEnabled: true, Transport: "ws", Path: "/ws", Enabled: true})
	inHY2 := mustCreateInbound(t, ctx, store, domain.Inbound{ID: "in-hy2", Type: domain.ProtocolHysteria2, Engine: domain.EngineSingBox, NodeID: node1.ID, Domain: "hy2.example.com", Port: 8443, TLSEnabled: true, Transport: "udp", Enabled: true})
	inXHTTP := mustCreateInbound(t, ctx, store, domain.Inbound{ID: "in-xhttp", Type: domain.ProtocolXHTTP, Engine: domain.EngineXray, NodeID: node2.ID, Domain: "edge.example.com", Port: 443, TLSEnabled: true, Transport: "xhttp", Path: "/x", Enabled: true})

	mustCreateCredential(t, ctx, store, domain.Credential{ID: "cred-vless", UserID: user.ID, InboundID: inVLESS.ID, Kind: domain.CredentialKindUUID, Secret: "11111111-1111-1111-1111-111111111111"})
	mustCreateCredential(t, ctx, store, domain.Credential{ID: "cred-hy2", UserID: user.ID, InboundID: inHY2.ID, Kind: domain.CredentialKindPassword, Secret: "hy2-secret"})
	mustCreateCredential(t, ctx, store, domain.Credential{ID: "cred-xhttp", UserID: user.ID, InboundID: inXHTTP.ID, Kind: domain.CredentialKindUUID, Secret: "22222222-2222-2222-2222-222222222222"})

	dataDir := t.TempDir()
	svc := New(store, dataDir, singbox.New(nil), xray.New(nil))

	got, err := svc.Generate(ctx, user.Name)
	if err != nil {
		t.Fatalf("Generate() unexpected error: %v", err)
	}

	if got.TXTPath != filepath.Join(dataDir, user.ID+".txt") {
		t.Fatalf("TXTPath = %q, want %q", got.TXTPath, filepath.Join(dataDir, user.ID+".txt"))
	}
	if got.Base64Path != filepath.Join(dataDir, user.ID+".base64") {
		t.Fatalf("Base64Path = %q, want %q", got.Base64Path, filepath.Join(dataDir, user.ID+".base64"))
	}
	if got.JSONPath != filepath.Join(dataDir, user.ID+".json") {
		t.Fatalf("JSONPath = %q, want %q", got.JSONPath, filepath.Join(dataDir, user.ID+".json"))
	}

	lines := strings.Split(strings.TrimSpace(string(got.TXT)), "\n")
	if len(lines) != 3 {
		t.Fatalf("txt line count = %d, want 3", len(lines))
	}
	assertContainsPrefix(t, lines, "vless://")
	assertContainsPrefix(t, lines, "hysteria2://")
	assertContainsSubstring(t, lines, "type=xhttp")

	decoded, err := base64.StdEncoding.DecodeString(string(got.Base64))
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if string(decoded) != string(got.TXT) {
		t.Fatalf("decoded base64 content does not match txt")
	}

	var payload struct {
		Version   string            `json:"version"`
		Protocols []domain.Protocol `json:"protocols"`
		Items     []struct {
			Protocol domain.Protocol `json:"protocol"`
			NodeID   string          `json:"node_id"`
			URI      string          `json:"uri"`
		} `json:"items"`
	}
	if err := json.Unmarshal(got.JSON, &payload); err != nil {
		t.Fatalf("unmarshal json export: %v", err)
	}
	if payload.Version != "v1" {
		t.Fatalf("json version = %q, want v1", payload.Version)
	}
	if len(payload.Items) != 3 {
		t.Fatalf("json items count = %d, want 3", len(payload.Items))
	}

	sub, err := store.Subscriptions().GetByUserID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByUserID() unexpected error: %v", err)
	}
	if sub.Format != domain.SubscriptionFormat(FormatTXT) {
		t.Fatalf("subscription format = %q, want %q", sub.Format, FormatTXT)
	}
	if sub.OutputPath != got.TXTPath {
		t.Fatalf("subscription output path = %q, want %q", sub.OutputPath, got.TXTPath)
	}
}

func TestExportJSONUpdatesStoredSubscriptionFormat(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	user := mustCreateUser(t, ctx, store, "carol")
	node := mustCreateNode(t, ctx, store, domain.Node{ID: "node-1", Name: "eu-1", Host: "eu.example.com", Role: domain.NodeRolePrimary, Enabled: true})
	inbound := mustCreateInbound(t, ctx, store, domain.Inbound{ID: "in-vless", Type: domain.ProtocolVLESS, Engine: domain.EngineSingBox, NodeID: node.ID, Domain: "eu.example.com", Port: 443, TLSEnabled: true, Transport: "tcp", Enabled: true})
	mustCreateCredential(t, ctx, store, domain.Credential{ID: "cred-vless", UserID: user.ID, InboundID: inbound.ID, Kind: domain.CredentialKindUUID, Secret: "11111111-1111-1111-1111-111111111111"})

	dataDir := t.TempDir()
	svc := New(store, dataDir, singbox.New(nil), xray.New(nil))

	exported, err := svc.Export(ctx, user.ID, FormatJSON)
	if err != nil {
		t.Fatalf("Export() unexpected error: %v", err)
	}
	if exported.Format != FormatJSON {
		t.Fatalf("exported format = %q, want %q", exported.Format, FormatJSON)
	}
	if !json.Valid(exported.Content) {
		t.Fatalf("exported json is invalid")
	}

	shown, err := svc.Show(ctx, user.ID)
	if err != nil {
		t.Fatalf("Show() unexpected error: %v", err)
	}
	if shown.Format != FormatJSON {
		t.Fatalf("show format = %q, want %q", shown.Format, FormatJSON)
	}
}

func openTestStore(t *testing.T) *sqlite.Store {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "proxyctl.db")
	store, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open(): %v", err)
	}
	if err := store.Init(context.Background()); err != nil {
		_ = store.Close()
		t.Fatalf("store.Init(): %v", err)
	}
	return store
}

func mustCreateUser(t *testing.T, ctx context.Context, store *sqlite.Store, name string) domain.User {
	t.Helper()
	user, err := store.Users().Create(ctx, domain.User{Name: name, Enabled: true})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func mustCreateNode(t *testing.T, ctx context.Context, store *sqlite.Store, node domain.Node) domain.Node {
	t.Helper()
	created, err := store.Nodes().Create(ctx, node)
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	return created
}

func mustCreateInbound(t *testing.T, ctx context.Context, store *sqlite.Store, inbound domain.Inbound) domain.Inbound {
	t.Helper()
	created, err := store.Inbounds().Create(ctx, inbound)
	if err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	return created
}

func mustCreateCredential(t *testing.T, ctx context.Context, store *sqlite.Store, credential domain.Credential) domain.Credential {
	t.Helper()
	created, err := store.Credentials().Create(ctx, credential)
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	return created
}

func assertContainsPrefix(t *testing.T, values []string, prefix string) {
	t.Helper()
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return
		}
	}
	t.Fatalf("no values with prefix %q found: %v", prefix, values)
}

func assertContainsSubstring(t *testing.T, values []string, needle string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(value, needle) {
			return
		}
	}
	t.Fatalf("no values containing %q found: %v", needle, values)
}
