package domain

import "time"

type NodeRole string

const (
	NodeRolePrimary NodeRole = "primary"
	NodeRoleNode    NodeRole = "node"
)

type Protocol string

const (
	ProtocolVLESS     Protocol = "vless"
	ProtocolHysteria2 Protocol = "hysteria2"
	ProtocolXHTTP     Protocol = "xhttp"
)

type Engine string

const (
	EngineSingBox Engine = "sing-box"
	EngineXray    Engine = "xray"
	EngineCaddy   Engine = "caddy"
	EngineNginx   Engine = "nginx"
)

type RevisionStatus string

const (
	RevisionDraft      RevisionStatus = "draft"
	RevisionValidated  RevisionStatus = "validated"
	RevisionApplied    RevisionStatus = "applied"
	RevisionFailed     RevisionStatus = "failed"
	RevisionRolledBack RevisionStatus = "rolled_back"
)

type CredentialKind string

const (
	CredentialKindUUID      CredentialKind = "uuid"
	CredentialKindPassword  CredentialKind = "password"
	CredentialKindAuthToken CredentialKind = "auth_token"
)

type SubscriptionFormat string

const (
	SubscriptionFormatClash   SubscriptionFormat = "clash"
	SubscriptionFormatSingBox SubscriptionFormat = "sing-box"
	SubscriptionFormatJSON    SubscriptionFormat = "json"
)

// User describes one managed account.
type User struct {
	ID                string
	Name              string
	Enabled           bool
	CreatedAt         time.Time
	ExpiresAt         *time.Time // nil = no expiry
	TrafficLimitBytes int64      // 0 = unlimited
}

// UserTrafficRecord holds accumulated traffic counters for a user.
type UserTrafficRecord struct {
	UserID    string
	RXBytes   int64
	TXBytes   int64
	UpdatedAt time.Time
}

// Node describes a managed node in proxyctl.
type Node struct {
	ID          string
	Name        string
	Host        string
	Role        NodeRole
	SSHUser     string
	SSHPort     int
	Enabled     bool
	DisableIPv6 bool
	BlockPing   bool
	CreatedAt   time.Time
	LastSyncOK  *bool // nil = never synced; persisted across panel restarts
	LastSyncMsg string
}

// Inbound configures one inbound listener.
type Inbound struct {
	ID                 string
	Type               Protocol
	Engine             Engine
	NodeID             string
	Domain             string
	Port               int
	TLSEnabled         bool
	TLSCertPath        string
	TLSKeyPath         string
	Transport          string
	Path               string
	SNI                string
	RealityEnabled     bool
	RealityPublicKey   string
	RealityPrivateKey  string
	RealityShortID     string
	RealityFingerprint string
	RealitySpiderX     string
	RealityServer      string
	RealityServerPort  int
	SelfSteal          bool
	VLESSFlow          string
	SniffingEnabled    bool
	SniffingHTTP       bool
	SniffingTLS        bool
	SniffingQUIC       bool
	SniffingFakeDNS    bool
	Enabled            bool
	CreatedAt          time.Time
}

// Credential stores one user/inbound access secret.
type Credential struct {
	ID        string
	UserID    string
	InboundID string
	Kind      CredentialKind
	Secret    string
	Metadata  string
	CreatedAt time.Time
}

// Subscription stores user subscription output metadata.
type Subscription struct {
	ID          string
	UserID      string
	Format      SubscriptionFormat
	OutputPath  string
	AccessToken string
	Label       string
	Enabled     bool
	UpdatedAt   time.Time
}

// Compatibility structs for upcoming renderer/runtime stages.
// Keep these as separate types to avoid coupling current storage model with render model.
type InboundProfile struct {
	ID              string
	Protocol        Protocol
	ListenAddr      string
	ListenPort      int
	TLSMode         string
	TransportParams string
	Enabled         bool
}

type EngineBinding struct {
	Protocol        Protocol
	Engine          Engine
	TemplateVersion string
}

type ReverseProxyProfile struct {
	ID            string
	Engine        Engine
	Domain        string
	TLSSource     string
	DecoySiteRoot string
	Enabled       bool
}
