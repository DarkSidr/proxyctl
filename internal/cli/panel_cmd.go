package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"proxyctl/internal/config"
	"proxyctl/internal/domain"
	"proxyctl/internal/engine"
	"proxyctl/internal/renderer"
	subscriptionservice "proxyctl/internal/subscription/service"

	"proxyctl/internal/storage"
)

type panelUnitState struct {
	Unit    string
	Active  string
	Enabled string
}

type panelInboundView struct {
	ID                 string
	Type               string
	Engine             string
	NodeID             string
	NodeName           string
	Domain             string
	Port               int
	TLS                bool
	Enabled            bool
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
	TLSCertPath        string
	TLSKeyPath         string
	SniffingEnabled    bool
	SniffingHTTP       bool
	SniffingTLS        bool
	SniffingQUIC       bool
	SniffingFakeDNS    bool
	PortConflict       bool
	Version            string
}

type panelUserView struct {
	ID                string
	Name              string
	Enabled           bool
	CreatedAt         time.Time
	ExpiresAt         *time.Time
	TrafficLimitBytes int64
	UsedRXBytes       int64
	UsedTXBytes       int64
	Version           string
}

type panelCredentialView struct {
	ID          string
	UserID      string
	UserName    string
	ClientLabel string
	InboundID   string
	InboundType string
	InboundAddr string
	Kind        string
	SecretMask  string
	ClientURI   string
	ClientError string
	Version     string
}

type panelNodeView struct {
	ID            string
	Name          string
	Host          string
	Role          string
	SSHUser       string
	SSHPort       int
	Enabled       bool
	DisableIPv6   bool
	BlockPing     bool
	Version       string
	SyncOK        *bool // nil = never synced, true = last OK, false = last failed
	SyncMsg       string
	JobID         string // non-empty = background job in progress
	RemoteVersion string // proxyctl version on the remote node (empty = not checked)
}

type panelSubscriptionState struct {
	Exists  bool
	Enabled bool
}

type panelSubscriptionView struct {
	UserID       string
	UserName     string
	ProfileName  string
	Label        string
	Enabled      bool
	AccessToken  string
	URL          string
	ConfigCount  int
	ConfigLabels []string
	InboundIDs   []string
}

type panelCounts struct {
	UsersTotal     int
	UsersEnabled   int
	InboundsTotal  int
	InboundsActive int
}

type panelUserTrafficView struct {
	UserID     string
	UserName   string
	RXBytes    uint64
	TXBytes    uint64
	TotalBytes uint64
}

type panelDashboardView struct {
	ProxyctlVersion string
	Load1           float64
	Load5           float64
	Load15          float64
	CPUCores        int
	CPUPercent      float64
	MemUsedBytes    uint64
	MemTotalBytes   uint64
	SwapUsedBytes   uint64
	SwapTotalBytes  uint64
	DiskUsedBytes   uint64
	DiskTotalBytes  uint64
	UptimeSeconds   uint64
	TotalRXBytes    uint64
	TotalTXBytes    uint64
	TotalBytes      uint64
	NetRXSpeed      uint64
	NetTXSpeed      uint64
	TCPConns        int
	UDPConns        int
	UserTraffic     []panelUserTrafficView
	TrafficSource   string
}

type panelPageData struct {
	Title               string
	ActiveTab           string
	BasePath            string
	LogoutPath          string
	ListenAddr          string
	GeneratedAt         string
	Counts              panelCounts
	Units               []panelUnitState
	Users               []panelUserView
	Nodes               []panelNodeView
	Inbounds            []panelInboundView
	Credentials         []panelCredentialView
	Subscriptions       []string
	SubscriptionDetails []panelSubscriptionView
	SubscriptionState   map[string]panelSubscriptionState
	SuggestedPorts      map[string]map[string]int // nodeID → "proto|transport" → port
	SNIPresets          []string
	SSHKeyWarning       string
	DefaultSelfSteal    bool
	Dashboard           panelDashboardView
	ContactEmail        string
	OperationStatus     string
	OperationMessage    string
	OperationAt         string
	DashboardActionPath string
	SettingsActionPath  string
	UsersActionPath     string
	NodesActionPath     string
	InboundsActionPath  string
	SubsActionPath      string
	AppPath             string
	XrayUnit            string
	SingBoxUnit         string
	CaddyUnit           string
}

type panelOperationFeed struct {
	mu      sync.RWMutex
	status  string
	message string
	at      string
}

func (f *panelOperationFeed) set(status, message string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = strings.TrimSpace(status)
	f.message = strings.TrimSpace(message)
	f.at = time.Now().UTC().Format("2006-01-02 15:04:05 UTC")
}

func (f *panelOperationFeed) snapshot() (status, message, at string) {
	if f == nil {
		return "", "", ""
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.status, f.message, f.at
}

// nodeSyncRecord caches the last sync result per node (in-memory, resets on restart).
type nodeSyncRecord struct {
	ok  bool
	msg string
	at  time.Time
}

var panelNodeSyncCache sync.Map // key: nodeID string → value: nodeSyncRecord

var panelNodeVersionCache sync.Map // key: nodeID string → value: string (remote proxyctl version)

// panelGlobalDBPath is set once at panel startup and used by setNodeSyncStatus
// to persist sync results across panel restarts.
var panelGlobalDBPath string

// panelTrafficCollection controls whether traffic stats goroutines are active.
// Default true; toggled via settings.
var panelTrafficCollection atomic.Bool

func init() { panelTrafficCollection.Store(true) }

// dashMu guards delta-metric state used to compute per-second speeds
// and traffic reset baselines.
var (
	dashMu       sync.Mutex
	prevNetRX    uint64
	prevNetTX    uint64
	prevNetAt    time.Time
	prevCPUIdle  uint64
	prevCPUTotal uint64
	// Traffic display reset baseline: display = raw - baseline.
	trafResetRX uint64
	trafResetTX uint64
	// Self-update version cache.
	panelUpdMu  sync.Mutex
	panelUpdTag string
	panelUpdURL string
	panelUpdAt  time.Time
)

// nodeJob tracks a background SSH operation per node.
type nodeJob struct {
	id        string
	nodeID    string
	op        string
	done      bool
	ok        bool
	msg       string
	startedAt time.Time
	mu        sync.RWMutex
}

func (j *nodeJob) finish(ok bool, msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.done = true
	j.ok = ok
	j.msg = strings.TrimSpace(msg)
}

func (j *nodeJob) jobStatus() (done, ok bool, msg string) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.done, j.ok, j.msg
}

var panelNodeJobs sync.Map // key: jobID (string) → *nodeJob

func panelNewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newNodeJob(nodeID, op string) *nodeJob {
	j := &nodeJob{
		id:        panelNewID(),
		nodeID:    strings.TrimSpace(nodeID),
		op:        op,
		startedAt: time.Now().UTC(),
	}
	panelNodeJobs.Store(j.id, j)
	return j
}

func getActiveJobForNode(nodeID string) *nodeJob {
	nodeID = strings.TrimSpace(nodeID)
	var found *nodeJob
	panelNodeJobs.Range(func(k, v interface{}) bool {
		j := v.(*nodeJob)
		if j.nodeID == nodeID {
			done, _, _ := j.jobStatus()
			if !done {
				found = j
				return false
			}
		}
		return true
	})
	return found
}

func setNodeSyncStatus(nodeID string, ok bool, msg string) {
	nodeID = strings.TrimSpace(nodeID)
	panelNodeSyncCache.Store(nodeID, nodeSyncRecord{ok: ok, msg: strings.TrimSpace(msg), at: time.Now()})
	// Persist to DB so status survives panel restarts.
	if dbPath := panelGlobalDBPath; dbPath != "" {
		go func() {
			store, err := openStoreWithInit(context.Background(), dbPath)
			if err != nil {
				return
			}
			defer store.Close()
			_ = store.Nodes().UpdateSyncStatus(context.Background(), nodeID, ok, msg)
		}()
	}
}

func getNodeSyncStatus(nodeID string) (ok bool, msg string, found bool) {
	v, exists := panelNodeSyncCache.Load(strings.TrimSpace(nodeID))
	if !exists {
		return false, "", false
	}
	r := v.(nodeSyncRecord)
	return r.ok, r.msg, true
}

var panelPageTmpl = template.Must(template.New("panel").Funcs(template.FuncMap{
	"eq": func(a, b string) bool { return a == b },
	"yesNo": func(v bool) string {
		if v {
			return "yes"
		}
		return "no"
	},
	"stateClass": func(v bool) string {
		if v {
			return "ok"
		}
		return "muted"
	},
	"timefmt": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.UTC().Format("2006-01-02 15:04:05 UTC")
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    :root {
      --bg-a: #0f172a;
      --bg-b: #111827;
      --card: rgba(17, 24, 39, 0.72);
      --line: rgba(148, 163, 184, 0.3);
      --text: #e5e7eb;
      --muted: #94a3b8;
      --ok: #34d399;
      --warn: #f59e0b;
      --err: #fb7185;
      --brand: #22d3ee;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      color: var(--text);
      font-family: "IBM Plex Sans", "Segoe UI", sans-serif;
      background:
        radial-gradient(circle at 5% 0%, #0ea5e930 0%, transparent 40%),
        radial-gradient(circle at 100% 10%, #22c55e22 0%, transparent 35%),
        linear-gradient(160deg, var(--bg-a), var(--bg-b));
      min-height: 100vh;
    }
    .wrap { max-width: 1280px; margin: 0 auto; padding: 24px 16px 44px; }
    .top {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      align-items: center;
      margin-bottom: 18px;
    }
    .top-right {
      display: flex;
      gap: 10px;
      align-items: center;
      flex-wrap: wrap;
      justify-content: flex-end;
    }
    .title { margin: 0; font-size: 1.45rem; letter-spacing: 0.02em; }
    .meta { color: var(--muted); font-size: 0.9rem; }
    .logout {
      appearance: none;
      border: 1px solid #0ea5e9;
      color: #e0f2fe;
      background: linear-gradient(180deg, #075985, #0c4a6e);
      border-radius: 999px;
      padding: 6px 12px;
      font-size: 0.82rem;
      cursor: pointer;
    }
    .logout:hover { filter: brightness(1.08); }
    .nav {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      margin-bottom: 14px;
    }
    .nav a {
      text-decoration: none;
      color: var(--text);
      border: 1px solid var(--line);
      background: var(--card);
      border-radius: 999px;
      padding: 7px 12px;
      font-size: 0.9rem;
    }
    .nav a.active {
      border-color: #06b6d4;
      box-shadow: inset 0 0 0 1px #06b6d4;
      color: #cffafe;
    }
    .cards {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
      gap: 10px;
      margin-bottom: 12px;
    }
    .card {
      border: 1px solid var(--line);
      background: var(--card);
      border-radius: 14px;
      padding: 12px;
      backdrop-filter: blur(8px);
    }
    .card .label { color: var(--muted); font-size: 0.85rem; margin-bottom: 3px; }
    .card .value { font-size: 1.3rem; font-weight: 600; }
    .section {
      border: 1px solid var(--line);
      background: var(--card);
      border-radius: 14px;
      overflow: hidden;
      margin-top: 10px;
    }
    .section h2 {
      margin: 0;
      padding: 12px 14px;
      font-size: 1rem;
      border-bottom: 1px solid var(--line);
    }
    .pad { padding: 10px 14px; }
    .line {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
      margin-bottom: 8px;
    }
    .btn {
      appearance: none;
      border: 1px solid #0ea5e9;
      color: #e0f2fe;
      background: linear-gradient(180deg, #0ea5e9, #0369a1);
      border-radius: 8px;
      padding: 6px 10px;
      cursor: pointer;
      font-size: 0.82rem;
    }
    .btn.warn { border-color: #f59e0b; background: linear-gradient(180deg, #d97706, #92400e); }
    .btn.err { border-color: #e11d48; background: linear-gradient(180deg, #be123c, #881337); }
    input, select {
      border: 1px solid var(--line);
      background: rgba(15, 23, 42, 0.62);
      border-radius: 8px;
      color: var(--text);
      padding: 6px 8px;
      font-size: 0.82rem;
      min-width: 0;
    }
    label.slim { color: var(--muted); font-size: 0.78rem; }
    .op {
      border: 1px solid var(--line);
      background: rgba(15, 23, 42, 0.45);
      border-radius: 10px;
      padding: 10px 12px;
      margin-bottom: 12px;
      font-size: 0.86rem;
    }
    .op.ok { border-color: #065f46; }
    .op.error { border-color: #9f1239; }
    table { width: 100%; border-collapse: collapse; }
    th, td {
      text-align: left;
      padding: 9px 12px;
      border-bottom: 1px solid rgba(148, 163, 184, 0.15);
      font-size: 0.82rem;
      vertical-align: top;
    }
    th { color: var(--muted); font-weight: 600; }
    tbody tr:hover { background: rgba(15, 23, 42, 0.45); }
    .ok { color: var(--ok); }
    .muted { color: var(--muted); }
    .links { padding: 10px 14px; }
    .links a { color: #a5f3fc; word-break: break-all; }
    .links li { margin: 8px 0; }
    .inline { display: inline-flex; gap: 8px; align-items: center; flex-wrap: wrap; }
    .table-wrap { overflow-x: auto; }
    .uri {
      min-width: 320px;
      max-width: 520px;
      width: 100%;
      border: 1px solid var(--line);
      background: rgba(15, 23, 42, 0.62);
      border-radius: 8px;
      color: var(--text);
      padding: 6px 8px;
      font-size: 0.78rem;
    }
    .copy-row {
      display: flex;
      gap: 8px;
      align-items: center;
      flex-wrap: wrap;
    }
    @media (max-width: 960px) {
      .top { flex-direction: column; align-items: flex-start; }
      th, td { padding: 8px; font-size: 0.8rem; }
    }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="top">
      <h1 class="title">proxyctl visual panel (phase 1)</h1>
      <div class="top-right">
        <div class="meta">{{.GeneratedAt}} | listen {{.ListenAddr}}</div>
        {{if .LogoutPath}}
        <form method="post" action="{{.LogoutPath}}">
          <button type="submit" class="logout">logout</button>
        </form>
        {{end}}
      </div>
    </div>

    <nav class="nav">
      <a href="{{.BasePath}}" class="{{if eq .ActiveTab "dashboard"}}active{{end}}">dashboard</a>
      <a href="{{.BasePath}}/users" class="{{if eq .ActiveTab "users"}}active{{end}}">users</a>
      <a href="{{.BasePath}}/inbounds" class="{{if eq .ActiveTab "inbounds"}}active{{end}}">inbounds</a>
      <a href="{{.BasePath}}/subscriptions" class="{{if eq .ActiveTab "subscriptions"}}active{{end}}">subscriptions</a>
      {{if .AppPath}}<a href="{{.AppPath}}">app (react beta)</a>{{end}}
    </nav>

    {{if .OperationMessage}}
    <section class="op {{if eq .OperationStatus "ok"}}ok{{else}}error{{end}}">
      <strong>last operation:</strong> {{.OperationMessage}}
      {{if .OperationAt}}<span class="muted">({{.OperationAt}})</span>{{end}}
    </section>
    {{end}}

    <div class="cards">
      <div class="card"><div class="label">users</div><div class="value">{{.Counts.UsersTotal}}</div></div>
      <div class="card"><div class="label">enabled users</div><div class="value">{{.Counts.UsersEnabled}}</div></div>
      <div class="card"><div class="label">inbounds</div><div class="value">{{.Counts.InboundsTotal}}</div></div>
      <div class="card"><div class="label">active inbounds</div><div class="value">{{.Counts.InboundsActive}}</div></div>
    </div>

    {{if eq .ActiveTab "dashboard"}}
    <section class="section">
      <h2>runtime units</h2>
      <table>
        <thead><tr><th>unit</th><th>active</th><th>enabled</th></tr></thead>
        <tbody>
          {{range .Units}}
          <tr>
            <td>{{.Unit}}</td>
            <td class="{{if eq .Active "active"}}ok{{else}}muted{{end}}">{{.Active}}</td>
            <td class="{{if eq .Enabled "enabled"}}ok{{else}}muted{{end}}">{{.Enabled}}</td>
          </tr>
          {{end}}
        </tbody>
      </table>
    </section>
    {{end}}

    {{if eq .ActiveTab "users"}}
    <section class="section">
      <h2>create user</h2>
      <div class="pad">
        <form method="post" action="{{.UsersActionPath}}" class="line">
          <input type="hidden" name="op" value="create">
          <input name="name" type="text" placeholder="username" required>
          <label class="slim"><input type="checkbox" name="enabled" value="1" checked> enabled</label>
          <button class="btn" type="submit">create</button>
        </form>
      </div>
    </section>

    <section class="section">
      <h2>users</h2>
      <table>
        <thead><tr><th>id</th><th>name</th><th>enabled</th><th>created at</th><th>actions</th></tr></thead>
        <tbody>
          {{range .Users}}
          <tr>
            <td>{{.ID}}</td>
            <td>{{.Name}}</td>
            <td class="{{stateClass .Enabled}}">{{yesNo .Enabled}}</td>
            <td>{{timefmt .CreatedAt}}</td>
            <td>
              <form method="post" action="{{$.UsersActionPath}}" class="inline">
                <input type="hidden" name="op" value="delete">
                <input type="hidden" name="user_id" value="{{.ID}}">
                <input type="hidden" name="version" value="{{.Version}}">
                <button class="btn err" type="submit">delete</button>
              </form>
            </td>
          </tr>
          {{else}}
          <tr><td colspan="5" class="muted">no users</td></tr>
          {{end}}
        </tbody>
      </table>
    </section>
    {{end}}

    {{if eq .ActiveTab "inbounds"}}
    <section class="section">
      <h2>create inbound</h2>
      <div class="pad">
        <form method="post" action="{{.InboundsActionPath}}" class="line">
          <input type="hidden" name="op" value="create">
          <select name="type">
            <option value="vless">vless</option>
            <option value="hysteria2">hysteria2</option>
            <option value="xhttp">xhttp</option>
          </select>
          <select name="transport">
            <option value="tcp">tcp</option>
            <option value="ws">ws</option>
            <option value="grpc">grpc</option>
            <option value="udp">udp</option>
            <option value="xhttp">xhttp</option>
          </select>
          <select name="engine">
            <option value="">auto</option>
            <option value="sing-box">sing-box</option>
            <option value="xray">xray</option>
          </select>
          <select name="node_id" required>
            {{range .Nodes}}
            <option value="{{.ID}}">{{.Name}} ({{.Host}})</option>
            {{end}}
          </select>
          <input name="domain" type="text" placeholder="domain" required>
          <input name="port" type="number" min="1" max="65535" placeholder="port" required>
          <input name="path" type="text" placeholder="path (optional)">
          <input name="sni" type="text" placeholder="sni (optional)">
          <label class="slim"><input type="checkbox" name="tls" value="1"> tls</label>
          <label class="slim"><input type="checkbox" name="enabled" value="1" checked> enabled</label>
          <button class="btn" type="submit">create inbound</button>
        </form>
      </div>
    </section>

    <section class="section">
      <h2>inbounds</h2>
      <div class="table-wrap">
      <table>
        <thead><tr><th>id</th><th>type</th><th>engine</th><th>node</th><th>domain</th><th>port</th><th>transport</th><th>path</th><th>sni</th><th>flags</th><th>actions</th></tr></thead>
        <tbody>
          {{range .Inbounds}}
          {{$row := .}}
          {{$formID := printf "inbound-form-%s" .ID}}
          <tr>
            <td>{{.ID}}</td>
            <td>
              <select name="type" form="{{$formID}}">
                <option value="vless" {{if eq .Type "vless"}}selected{{end}}>vless</option>
                <option value="hysteria2" {{if eq .Type "hysteria2"}}selected{{end}}>hysteria2</option>
                <option value="xhttp" {{if eq .Type "xhttp"}}selected{{end}}>xhttp</option>
              </select>
            </td>
            <td>
              <select name="engine" form="{{$formID}}">
                <option value="sing-box" {{if eq .Engine "sing-box"}}selected{{end}}>sing-box</option>
                <option value="xray" {{if eq .Engine "xray"}}selected{{end}}>xray</option>
              </select>
            </td>
            <td>
              <select name="node_id" form="{{$formID}}">
                {{range $.Nodes}}
                <option value="{{.ID}}" {{if eq $row.NodeID .ID}}selected{{end}}>{{.Name}}</option>
                {{end}}
              </select>
            </td>
            <td><input name="domain" value="{{.Domain}}" type="text" required form="{{$formID}}"></td>
            <td><input name="port" value="{{.Port}}" type="number" min="1" max="65535" required form="{{$formID}}"></td>
            <td>
              <select name="transport" form="{{$formID}}">
                <option value="tcp" {{if eq .Transport "tcp"}}selected{{end}}>tcp</option>
                <option value="ws" {{if eq .Transport "ws"}}selected{{end}}>ws</option>
                <option value="grpc" {{if eq .Transport "grpc"}}selected{{end}}>grpc</option>
                <option value="udp" {{if eq .Transport "udp"}}selected{{end}}>udp</option>
                <option value="xhttp" {{if eq .Transport "xhttp"}}selected{{end}}>xhttp</option>
              </select>
            </td>
            <td><input name="path" value="{{.Path}}" type="text" form="{{$formID}}"></td>
            <td><input name="sni" value="{{.SNI}}" type="text" form="{{$formID}}"></td>
            <td>
              <label class="slim"><input type="checkbox" name="tls" value="1" {{if .TLS}}checked{{end}} form="{{$formID}}"> tls</label>
              <label class="slim"><input type="checkbox" name="enabled" value="1" {{if .Enabled}}checked{{end}} form="{{$formID}}"> enabled</label>
            </td>
            <td>
              <form id="{{$formID}}" method="post" action="{{$.InboundsActionPath}}" class="inline">
                <input type="hidden" name="op" value="update">
                <input type="hidden" name="inbound_id" value="{{.ID}}">
                <input type="hidden" name="version" value="{{.Version}}">
                <button class="btn" type="submit">save</button>
              </form>
              <form method="post" action="{{$.InboundsActionPath}}" class="inline">
                <input type="hidden" name="op" value="delete">
                <input type="hidden" name="inbound_id" value="{{.ID}}">
                <input type="hidden" name="version" value="{{.Version}}">
                <button class="btn err" type="submit">delete</button>
              </form>
            </td>
          </tr>
          {{else}}
          <tr><td colspan="11" class="muted">no inbounds</td></tr>
          {{end}}
        </tbody>
      </table>
      </div>
    </section>
    {{end}}

    {{if eq .ActiveTab "subscriptions"}}
    <section class="section">
      <h2>subscription actions</h2>
      <div class="pad line">
        <form method="post" action="{{.SubsActionPath}}" class="inline">
          <input type="hidden" name="op" value="generate_user">
          <select name="user_id">
            {{range .Users}}
            <option value="{{.ID}}">{{.Name}} ({{.ID}})</option>
            {{end}}
          </select>
          <button class="btn" type="submit">generate for user</button>
        </form>
        <form method="post" action="{{.SubsActionPath}}" class="inline">
          <input type="hidden" name="op" value="refresh_all">
          <button class="btn warn" type="submit">refresh all</button>
        </form>
      </div>
    </section>

    <section class="section">
      <h2>attach credential (user ↔ inbound)</h2>
      <div class="pad">
        <form method="post" action="{{.SubsActionPath}}" class="line">
          <input type="hidden" name="op" value="attach_credential">
          <select name="user_id" required>
            {{range .Users}}
            <option value="{{.ID}}">{{.Name}} ({{.ID}})</option>
            {{end}}
          </select>
          <select name="inbound_id" required>
            {{range .Inbounds}}
            <option value="{{.ID}}">{{.ID}} {{.Type}} {{.Domain}}:{{.Port}}</option>
            {{end}}
          </select>
          <input name="label" type="text" placeholder="client label (optional)">
          <button class="btn" type="submit">attach</button>
        </form>
      </div>
    </section>

    <section class="section">
      <h2>credentials</h2>
      <div class="table-wrap">
      <table>
        <thead><tr><th>user</th><th>client label</th><th>inbound</th><th>ready config (uri)</th><th>actions</th></tr></thead>
        <tbody>
          {{range .Credentials}}
          <tr>
            <td>{{.UserName}}<br><span class="muted">{{.UserID}}</span></td>
            <td>{{if .ClientLabel}}{{.ClientLabel}}{{else}}<span class="muted">-</span>{{end}}</td>
            <td>{{.InboundType}} {{.InboundAddr}}<br><span class="muted">{{.InboundID}}</span></td>
            <td>
              {{if .ClientURI}}
              <div class="copy-row">
                <input class="uri" type="text" readonly value="{{.ClientURI}}">
                <button class="btn" type="button" data-copy-text="{{.ClientURI}}" onclick="copyPanelText(this)">copy</button>
              </div>
              {{else}}
              <span class="muted">unavailable: {{.ClientError}}</span>
              {{end}}
            </td>
            <td>
              <form method="post" action="{{$.SubsActionPath}}" class="inline">
                <input type="hidden" name="op" value="delete_credential">
                <input type="hidden" name="credential_id" value="{{.ID}}">
                <input type="hidden" name="user_id" value="{{.UserID}}">
                <input type="hidden" name="version" value="{{.Version}}">
                <button class="btn err" type="submit">detach</button>
              </form>
            </td>
          </tr>
          {{else}}
          <tr><td colspan="5" class="muted">no credentials</td></tr>
          {{end}}
        </tbody>
      </table>
      </div>
    </section>

    <section class="section">
      <h2>generated subscription links</h2>
      <div class="links">
        <ul>
          {{range .Subscriptions}}
          <li>
            <div class="copy-row">
              <a href="{{.}}" target="_blank" rel="noopener noreferrer">{{.}}</a>
              <button class="btn" type="button" data-copy-text="{{.}}" onclick="copyPanelText(this)">copy</button>
            </div>
          </li>
          {{else}}
          <li class="muted">no public subscription links found</li>
          {{end}}
        </ul>
      </div>
    </section>
    {{end}}
  </div>
  <script>
    function copyPanelText(btn) {
      if (!btn) return;
      var text = (btn.getAttribute('data-copy-text') || '').trim();
      if (!text) return;
      var done = function() {
        var prev = btn.textContent;
        btn.textContent = 'copied';
        setTimeout(function() {
          btn.textContent = prev;
        }, 900);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done).catch(function() {
          fallbackCopy(text, done);
        });
        return;
      }
      fallbackCopy(text, done);
    }
    function fallbackCopy(text, onDone) {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.setAttribute('readonly', '');
      ta.style.position = 'absolute';
      ta.style.left = '-9999px';
      document.body.appendChild(ta);
      ta.select();
      try {
        document.execCommand('copy');
        if (typeof onDone === 'function') onDone();
      } catch (e) {
      }
      document.body.removeChild(ta);
    }
  </script>
</body>
</html>`))

var panelLoginTmpl = template.Must(template.New("panel-login").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>proxyctl panel login</title>
  <style>
    :root {
      --bg-a: #0f172a;
      --bg-b: #111827;
      --card: rgba(17, 24, 39, 0.85);
      --line: rgba(148, 163, 184, 0.26);
      --text: #e5e7eb;
      --muted: #94a3b8;
      --brand: #22d3ee;
      --err: #fb7185;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      color: var(--text);
      font-family: "IBM Plex Sans", "Segoe UI", sans-serif;
      background:
        radial-gradient(circle at 5% 0%, #0ea5e930 0%, transparent 40%),
        radial-gradient(circle at 100% 10%, #22c55e22 0%, transparent 35%),
        linear-gradient(160deg, var(--bg-a), var(--bg-b));
      min-height: 100vh;
      display: grid;
      place-items: center;
      padding: 20px;
    }
    .card {
      width: 100%;
      max-width: 380px;
      border: 1px solid var(--line);
      border-radius: 16px;
      background: var(--card);
      backdrop-filter: blur(8px);
      padding: 18px;
    }
    h1 { margin: 0 0 6px; font-size: 1.25rem; letter-spacing: 0.01em; }
    .sub { color: var(--muted); margin-bottom: 14px; font-size: 0.92rem; }
    label { display: block; margin: 10px 0 6px; color: var(--muted); font-size: 0.85rem; }
    input {
      width: 100%;
      border: 1px solid var(--line);
      background: rgba(15, 23, 42, 0.62);
      border-radius: 10px;
      color: var(--text);
      padding: 10px 11px;
      outline: none;
    }
    input:focus { border-color: var(--brand); box-shadow: 0 0 0 1px var(--brand); }
    .btn {
      width: 100%;
      margin-top: 14px;
      border: 1px solid #0891b2;
      color: #e0f2fe;
      background: linear-gradient(180deg, #0891b2, #155e75);
      border-radius: 10px;
      padding: 10px 12px;
      cursor: pointer;
      font-weight: 600;
    }
    .err {
      margin-top: 10px;
      color: var(--err);
      border: 1px solid #be123c66;
      background: #88133733;
      border-radius: 10px;
      padding: 8px 10px;
      font-size: 0.88rem;
    }
  </style>
</head>
<body>
  <main class="card">
    <h1>proxyctl panel</h1>
    <div class="sub">sign in to continue</div>
    <form method="post" action="{{.LoginPath}}">
      <label for="login">username</label>
      <input id="login" name="login" type="text" autocomplete="username" required>
      <label for="password">password</label>
      <input id="password" name="password" type="password" autocomplete="current-password" required>
      <button class="btn" type="submit">Sign In</button>
      {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
    </form>
  </main>
</body>
</html>`))

type panelAppData struct {
	BasePath                 string
	LegacyPath               string
	LogoutPath               string
	SnapshotPath             string
	DashboardActionPath      string
	SettingsActionPath       string
	UsersActionPath          string
	NodesActionPath          string
	InboundsActionPath       string
	SubsActionPath           string
	ContactEmail             string
	TrafficCollectionEnabled bool
}

var panelAppTmpl = template.Must(template.New("panel-app").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>proxyctl app</title>
  <style>
    :root {
      --bg-a: #020617;
      --bg-b: #0f172a;
      --card: rgba(15, 23, 42, 0.7);
      --line: rgba(148, 163, 184, 0.25);
      --text: #e2e8f0;
      --muted: #94a3b8;
      --ok: #34d399;
      --err: #fb7185;
      --brand: #22d3ee;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      color: var(--text);
      font-family: "IBM Plex Sans", "Segoe UI", sans-serif;
      background:
        radial-gradient(circle at 3% 0%, #06b6d420 0%, transparent 35%),
        radial-gradient(circle at 100% 0%, #22c55e16 0%, transparent 28%),
        linear-gradient(160deg, var(--bg-a), var(--bg-b));
      min-height: 100vh;
    }
    .wrap { max-width: 1280px; margin: 0 auto; padding: 18px; }
    .top { display: flex; flex-wrap: wrap; gap: 8px; justify-content: space-between; align-items: center; }
    .title { margin: 0; font-size: 1.2rem; }
    .actions { display: flex; gap: 8px; flex-wrap: wrap; }
    .tabs { margin-top: 10px; display: flex; gap: 8px; flex-wrap: wrap; }
    .tab { border: 1px solid var(--line); background: rgba(15, 23, 42, 0.55); color: var(--text); border-radius: 999px; padding: 6px 10px; cursor: pointer; font-size: 0.8rem; }
    .tab.active { border-color: #06b6d4; box-shadow: inset 0 0 0 1px #06b6d4; color: #cffafe; }
    .btn {
      border: 1px solid #0891b2;
      color: #e0f2fe;
      background: linear-gradient(180deg, #0891b2, #155e75);
      border-radius: 10px;
      padding: 7px 10px;
      cursor: pointer;
      font-size: 0.82rem;
    }
    .btn.warn { border-color: #d97706; background: linear-gradient(180deg, #b45309, #78350f); }
    .btn.err { border-color: #be123c; background: linear-gradient(180deg, #be123c, #881337); }
    .btn.secondary { border-color: var(--line); background: rgba(15, 23, 42, 0.55); color: var(--text); }
    .grid { margin-top: 12px; display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 8px; }
    .card, .sec { border: 1px solid var(--line); background: var(--card); border-radius: 12px; }
    .card { padding: 10px 12px; }
    .label { color: var(--muted); font-size: 0.78rem; }
    .value { font-size: 1.2rem; font-weight: 600; }
    .sec { margin-top: 10px; overflow: hidden; }
    .sec h2 { margin: 0; padding: 10px 12px; border-bottom: 1px solid var(--line); font-size: 0.95rem; }
    .pad { padding: 10px 12px; }
    .row { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
    input, select {
      border: 1px solid var(--line);
      background: rgba(15, 23, 42, 0.62);
      border-radius: 8px;
      color: var(--text);
      padding: 6px 8px;
      font-size: 0.82rem;
    }
    .table-wrap { overflow-x: auto; }
    table { width: 100%; border-collapse: collapse; }
    th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid rgba(148, 163, 184, 0.15); font-size: 0.82rem; }
    th { color: var(--muted); }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.75rem; }
    .op { margin-top: 8px; padding: 8px 10px; border: 1px solid var(--line); border-radius: 10px; background: rgba(15, 23, 42, 0.45); }
    .op.ok { border-color: #065f46; }
    .op.error { border-color: #9f1239; }
    .muted { color: var(--muted); }
    .hidden { display: none !important; }
    a { color: #67e8f9; }
    /* Modal */
    .modal-overlay { position: fixed; inset: 0; background: rgba(2,6,23,0.75); z-index: 200; display: flex; align-items: center; justify-content: center; padding: 16px; backdrop-filter: blur(4px); }
    .modal { background: #0f172a; border: 1px solid var(--line); border-radius: 14px; width: 100%; max-width: 520px; max-height: 90vh; display: flex; flex-direction: column; box-shadow: 0 24px 64px rgba(0,0,0,0.6); }
    .modal-hdr { display: flex; align-items: center; justify-content: space-between; padding: 14px 16px; border-bottom: 1px solid var(--line); flex-shrink: 0; }
    .modal-hdr h3 { margin: 0; font-size: 0.95rem; font-weight: 600; }
    .modal-close { background: none; border: none; color: var(--muted); font-size: 1.4rem; cursor: pointer; padding: 2px 6px; line-height: 1; border-radius: 6px; }
    .modal-close:hover { color: var(--text); background: rgba(148,163,184,0.1); }
    .modal-body { padding: 16px; display: flex; flex-direction: column; gap: 12px; overflow-y: auto; flex: 1; }
    .modal-ftr { padding: 12px 16px; border-top: 1px solid var(--line); display: flex; gap: 8px; justify-content: flex-end; flex-shrink: 0; }
    .frow { display: flex; flex-direction: column; gap: 4px; }
    .frow .flabel { font-size: 0.78rem; color: var(--muted); }
    .frow input, .frow select { width: 100%; }
    .frow-inline { display: flex; gap: 8px; align-items: center; }
    .modal-box { background: #0f172a; border: 1px solid var(--line); border-radius: 14px; padding: 20px; max-width: 460px; margin: 20px auto; }
    .modal-box h3 { margin: 0 0 14px; font-size: 0.95rem; font-weight: 600; }
    .form-grid { display: grid; grid-template-columns: 130px 1fr; gap: 8px 12px; align-items: center; }
    .err-msg { color: #fb7185; font-size: 0.8rem; margin-top: 6px; padding: 6px; background: rgba(251,113,133,0.08); border-radius: 6px; }
    .sec-tabs { display: flex; gap: 4px; }
    .sec-tab { border: 1px solid var(--line); background: rgba(15,23,42,0.55); color: var(--muted); border-radius: 8px; padding: 5px 16px; cursor: pointer; font-size: 0.8rem; transition: all 0.15s; }
    .sec-tab.active { border-color: var(--brand); color: var(--brand); background: rgba(34,211,238,0.08); }
    .modal-block { display: flex; flex-direction: column; gap: 12px; padding: 12px 14px; border: 1px solid var(--line); border-radius: 10px; background: rgba(15,23,42,0.5); }
    .modal-block-hdr { font-size: 0.78rem; color: var(--muted); font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; margin-bottom: 2px; }
    .sec-hdr { display: flex; align-items: center; justify-content: space-between; padding: 10px 12px; border-bottom: 1px solid var(--line); }
    .sec-hdr h2 { margin: 0; font-size: 0.95rem; }
    @keyframes pulse { 0%,100% { opacity:1; } 50% { opacity:0.35; } }
    /* Custom checkbox (for tables) */
    input[type="checkbox"].cb { appearance: none; -webkit-appearance: none; width: 16px; height: 16px; border: 1.5px solid rgba(148,163,184,0.25); border-radius: 4px; background: rgba(15,23,42,0.5); cursor: pointer; transition: all 0.15s; position: relative; vertical-align: middle; flex-shrink: 0; }
    input[type="checkbox"].cb:checked { background: rgba(34,211,238,0.15); border-color: #22d3ee; }
    input[type="checkbox"].cb:checked::after { content: ""; position: absolute; left: 4px; top: 1px; width: 5px; height: 9px; border: 2px solid #22d3ee; border-top: none; border-left: none; transform: rotate(45deg); }
    input[type="checkbox"].cb:indeterminate { background: rgba(34,211,238,0.1); border-color: #22d3ee; }
    input[type="checkbox"].cb:indeterminate::after { content: ""; position: absolute; left: 3px; top: 6px; width: 8px; height: 2px; background: #22d3ee; }
    /* Toggle switch */
    .toggle { position: relative; display: inline-flex; align-items: center; gap: 8px; cursor: pointer; user-select: none; vertical-align: middle; }
    .toggle input[type="checkbox"] { position: absolute; opacity: 0; width: 0; height: 0; pointer-events: none; }
    .toggle-track { position: relative; width: 34px; height: 18px; flex-shrink: 0; background: rgba(148,163,184,0.12); border: 1px solid rgba(148,163,184,0.2); border-radius: 18px; transition: background 0.18s, border-color 0.18s; }
    .toggle-track::after { content: ""; position: absolute; top: 2px; left: 2px; width: 12px; height: 12px; border-radius: 50%; background: var(--muted); transition: transform 0.18s, background 0.18s; }
    .toggle input[type="checkbox"]:checked ~ .toggle-track { background: rgba(34,211,238,0.18); border-color: #22d3ee; }
    .toggle input[type="checkbox"]:checked ~ .toggle-track::after { transform: translateX(16px); background: #22d3ee; }
    .toggle-label { font-size: 0.82rem; color: var(--muted); }
    /* Dashboard gauges */
    .dash-gauges { display: flex; flex-wrap: wrap; gap: 12px; margin-top: 12px; }
    .gauge { flex: 1 1 140px; min-width: 130px; max-width: 200px; display: flex; flex-direction: column; align-items: center; gap: 6px; }
    .gauge-ring { position: relative; width: 96px; height: 96px; }
    .gauge-ring svg { width: 96px; height: 96px; transform: rotate(-90deg); }
    .gauge-ring circle { fill: none; stroke-width: 10; }
    .gauge-track { stroke: rgba(148,163,184,0.12); }
    .gauge-fill { stroke-linecap: round; transition: stroke-dashoffset 0.6s ease; }
    .gauge-center { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; }
    .gauge-pct { font-size: 1.1rem; font-weight: 700; }
    .gauge-name { font-size: 0.72rem; color: var(--muted); text-transform: uppercase; letter-spacing: 0.04em; }
    .gauge-sub { font-size: 0.7rem; color: var(--muted); }
    /* Dashboard grid */
    .dash-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 10px; margin-top: 10px; }
    .dash-card { border: 1px solid var(--line); background: var(--card); border-radius: 12px; padding: 12px 14px; }
    .dash-card h3 { margin: 0 0 10px; font-size: 0.85rem; color: var(--muted); font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; }
    .service-row { display: flex; align-items: center; gap: 8px; padding: 5px 0; border-bottom: 1px solid rgba(148,163,184,0.1); }
    .service-row:last-child { border-bottom: none; }
    .service-name { flex: 1; font-size: 0.82rem; }
    .badge-ok { background: rgba(52,211,153,0.15); color: #34d399; border-radius: 6px; padding: 2px 7px; font-size: 0.72rem; font-weight: 600; }
    .badge-err { background: rgba(251,113,133,0.15); color: #fb7185; border-radius: 6px; padding: 2px 7px; font-size: 0.72rem; font-weight: 600; }
    .badge-warn { background: rgba(251,191,36,0.15); color: #fbbf24; border-radius: 6px; padding: 2px 7px; font-size: 0.72rem; font-weight: 600; }
    .btn-xs { border: 1px solid var(--line); background: rgba(15,23,42,0.55); color: var(--muted); border-radius: 6px; padding: 3px 8px; cursor: pointer; font-size: 0.72rem; }
    .btn-xs:hover { color: var(--text); border-color: var(--brand); }
    .btn-xs.danger:hover { border-color: var(--err); color: var(--err); }
    .stat-row { display: flex; justify-content: space-between; align-items: baseline; padding: 4px 0; font-size: 0.82rem; border-bottom: 1px solid rgba(148,163,184,0.08); }
    .stat-row:last-child { border-bottom: none; }
    .stat-label { color: var(--muted); }
    .stat-value { font-weight: 600; }
    /* Loading overlay */
    #loadOverlay {
      position: fixed; inset: 0; z-index: 9999;
      background:
        radial-gradient(circle at 3% 0%, #06b6d420 0%, transparent 35%),
        radial-gradient(circle at 100% 0%, #22c55e16 0%, transparent 28%),
        linear-gradient(160deg, #020617, #0f172a);
      display: flex; flex-direction: column;
      align-items: center; justify-content: center; gap: 18px;
      transition: opacity 0.45s;
    }
    #loadOverlay.done { opacity: 0; pointer-events: none; }
    .load-brand { font-size: 2.2rem; font-weight: 700; color: #22d3ee; letter-spacing: -0.02em; }
    .load-sub { font-size: 0.82rem; color: #94a3b8; }
    .load-bar-wrap { width: 220px; height: 2px; background: rgba(148,163,184,0.18); border-radius: 2px; overflow: hidden; }
    .load-bar { height: 100%; background: linear-gradient(90deg, #22d3ee, #34d399); border-radius: 2px; width: 0%; animation: ldProg 1.6s cubic-bezier(0.4,0,0.2,1) forwards; transition: width 0.25s ease; }
    @keyframes ldProg { to { width: 72%; } }
  </style>
</head>
<body>
  <div id="loadOverlay">
    <div class="load-brand">proxyctl</div>
    <div class="load-sub" id="loadStatus">loading…</div>
    <div class="load-bar-wrap"><div class="load-bar" id="loadBar"></div></div>
  </div>
  <div class="wrap">
    <div class="top">
      <h1 class="title">proxyctl app</h1>
      <div class="actions">
        <a class="btn secondary" href="{{.LegacyPath}}">legacy panel</a>
        <form method="post" action="{{.LogoutPath}}"><button class="btn" type="submit">logout</button></form>
      </div>
    </div>
    <div id="op" class="op" style="display:none"></div>
    <div id="counts" class="grid"></div>
    <div class="tabs">
      <button type="button" class="tab active" data-tab="dashboard">dashboard</button>
      <button type="button" class="tab" data-tab="nodes">nodes</button>
      <button type="button" class="tab" data-tab="inbounds">inbounds</button>
      <button type="button" class="tab" data-tab="users">users</button>
      <button type="button" class="tab" data-tab="credentials">credentials</button>
      <button type="button" class="tab" data-tab="subscriptions">subscriptions</button>
      <button type="button" class="tab" data-tab="settings">settings</button>
    </div>

    <section class="sec" data-tab-section="dashboard">
      <div class="sec-hdr">
        <h2>dashboard</h2>
        <div class="row" style="gap:8px">
          <label class="toggle">
            <input id="liveMode" type="checkbox" checked>
            <span class="toggle-track"></span>
            <span class="toggle-label">live</span>
          </label>
          <select id="liveInterval">
            <option value="3000">3s</option>
            <option value="5000" selected>5s</option>
            <option value="10000">10s</option>
          </select>
          <button id="askTrafficBtn" class="btn secondary">refresh now</button>
        </div>
      </div>
      <div class="pad">
        <!-- Gauges row -->
        <div class="dash-gauges" id="dashGauges">
          <div class="gauge">
            <div class="gauge-ring">
              <svg viewBox="0 0 96 96"><circle class="gauge-track" cx="48" cy="48" r="43"/><circle class="gauge-fill" id="gaugeCPUArc" cx="48" cy="48" r="43" stroke="#22d3ee" stroke-dasharray="270.2 270.2" stroke-dashoffset="270.2"/></svg>
              <div class="gauge-center"><span class="gauge-pct" id="gaugeCPUPct">0%</span></div>
            </div>
            <span class="gauge-name">CPU</span>
            <span class="gauge-sub" id="gaugeCPUSub">0 cores</span>
          </div>
          <div class="gauge">
            <div class="gauge-ring">
              <svg viewBox="0 0 96 96"><circle class="gauge-track" cx="48" cy="48" r="43"/><circle class="gauge-fill" id="gaugeRAMArc" cx="48" cy="48" r="43" stroke="#34d399" stroke-dasharray="270.2 270.2" stroke-dashoffset="270.2"/></svg>
              <div class="gauge-center"><span class="gauge-pct" id="gaugeRAMPct">0%</span></div>
            </div>
            <span class="gauge-name">RAM</span>
            <span class="gauge-sub" id="gaugeRAMSub">0 / 0</span>
          </div>
          <div class="gauge">
            <div class="gauge-ring">
              <svg viewBox="0 0 96 96"><circle class="gauge-track" cx="48" cy="48" r="43"/><circle class="gauge-fill" id="gaugeSwapArc" cx="48" cy="48" r="43" stroke="#a78bfa" stroke-dasharray="270.2 270.2" stroke-dashoffset="270.2"/></svg>
              <div class="gauge-center"><span class="gauge-pct" id="gaugeSwapPct">0%</span></div>
            </div>
            <span class="gauge-name">Swap</span>
            <span class="gauge-sub" id="gaugeSwapSub">0 / 0</span>
          </div>
          <div class="gauge">
            <div class="gauge-ring">
              <svg viewBox="0 0 96 96"><circle class="gauge-track" cx="48" cy="48" r="43"/><circle class="gauge-fill" id="gaugeDiskArc" cx="48" cy="48" r="43" stroke="#fb923c" stroke-dasharray="270.2 270.2" stroke-dashoffset="270.2"/></svg>
              <div class="gauge-center"><span class="gauge-pct" id="gaugeDiskPct">0%</span></div>
            </div>
            <span class="gauge-name">Disk</span>
            <span class="gauge-sub" id="gaugeDiskSub">0 / 0</span>
          </div>
        </div>
        <!-- Grid -->
        <div class="dash-grid">
          <!-- Services -->
          <div class="dash-card">
            <h3>services</h3>
            <div id="dashServices"></div>
          </div>
          <!-- System info -->
          <div class="dash-card">
            <h3>system</h3>
            <div id="dashSystem"></div>
          </div>
          <!-- Network speed -->
          <div class="dash-card">
            <h3>network speed</h3>
            <div id="dashSpeed"></div>
          </div>
          <!-- Traffic & connections -->
          <div class="dash-card">
            <h3>traffic &amp; connections</h3>
            <div id="dashTraffic"></div>
          </div>
          <!-- Version & update -->
          <div class="dash-card">
            <h3>proxyctl</h3>
            <div id="dashVersion"></div>
          </div>
        </div>
        <!-- User traffic table -->
        <div class="table-wrap" style="margin-top:10px">
          <table>
            <thead><tr><th>user</th><th>↓ downloaded</th><th>↑ uploaded</th><th>total</th><th></th></tr></thead>
            <tbody id="userTrafficBody"></tbody>
          </table>
        </div>
        <div class="pad muted" id="trafficMeta"></div>
      </div>
    </section>

    <section class="sec" data-tab-section="nodes">
      <div class="sec-hdr">
        <h2>nodes</h2>
        <button class="btn" id="openAddNodeBtn">+ add node</button>
      </div>
      <div id="nodesSshWarning" class="hidden" style="margin:4px 0 10px;padding:8px 12px;background:#7a4a00;color:#ffd680;border-radius:6px;font-size:0.85rem"></div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>name</th><th>host</th><th>role</th><th>status</th><th>actions</th></tr></thead>
          <tbody id="nodesBody"></tbody>
        </table>
      </div>
    </section>

    <section class="sec" data-tab-section="inbounds">
      <div class="sec-hdr">
        <h2>inbounds</h2>
        <button class="btn" id="openCreateInboundBtn">+ create inbound</button>
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>id</th><th>type</th><th>node</th><th>domain</th><th>port</th><th>actions</th></tr></thead>
          <tbody id="inboundsBody"></tbody>
        </table>
      </div>
    </section>

    <section class="sec" data-tab-section="users">
      <h2>users</h2>
      <div class="pad row">
        <input id="newUserName" type="text" placeholder="user name">
        <button id="createUserBtn" class="btn">create</button>
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>name</th><th>expiry</th><th>limit</th><th>used</th><th>status</th><th>actions</th></tr></thead>
          <tbody id="usersBody"></tbody>
        </table>
      </div>
      <div id="userEditModal" class="modal hidden">
        <div class="modal-box">
          <h3>edit user</h3>
          <div class="form-grid">
            <label>name</label><input id="ueModalName" type="text">
            <label>expires (YYYY-MM-DD)</label>
            <div class="row"><input id="ueModalExpires" type="date"><button class="btn secondary" id="ueModalExpiresClear">clear</button></div>
            <label>traffic limit</label>
            <div class="row"><input id="ueModalTrafficVal" type="number" min="0" style="width:100px">
              <select id="ueModalTrafficUnit">
                <option value="1">B</option>
                <option value="1048576">MB</option>
                <option value="1073741824" selected>GB</option>
                <option value="1099511627776">TB</option>
              </select>
              <span class="muted" style="align-self:center">(0 = unlimited)</span>
            </div>
            <label>enabled</label><input id="ueModalEnabled" type="checkbox" checked>
          </div>
          <div class="row" style="margin-top:12px;gap:8px">
            <button id="ueModalSave" class="btn">save</button>
            <button id="ueModalResetTraffic" class="btn warn">reset traffic</button>
            <button id="ueModalDelete" class="btn err">delete</button>
            <button id="ueModalCancel" class="btn secondary">cancel</button>
          </div>
          <div id="ueModalErr" class="err-msg hidden"></div>
        </div>
      </div>
    </section>

    <section class="sec" data-tab-section="credentials">
      <h2>credentials</h2>
      <div class="pad row">
        <select id="credUser"></select>
        <select id="credInbound"></select>
        <input id="credLabel" type="text" placeholder="client label (optional)">
        <button id="attachCredBtn" class="btn">attach credential</button>
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>user</th><th>label</th><th>inbound</th><th>uri</th><th>actions</th></tr></thead>
          <tbody id="credsBody"></tbody>
        </table>
      </div>
    </section>

    <section class="sec" data-tab-section="subscriptions">
      <div class="sec-hdr">
        <h2>subscriptions</h2>
        <div class="row" style="gap:8px;align-items:center">
          <select id="subUser"></select>
          <button id="refreshSubBtn" class="btn warn">refresh all</button>
          <button id="openSubModalBtn" class="btn">+ subscription</button>
        </div>
      </div>
      <div class="pad" id="subsList"></div>
    </section>

    <section class="sec" data-tab-section="settings">
      <h2>settings</h2>
      <div class="pad row">
        <input id="acmeEmail" type="email" placeholder="acme contact email (optional)" value="{{.ContactEmail}}">
        <button id="saveAcmeBtn" class="btn">save ACME email</button>
      </div>
      <div class="pad muted">used by caddy tls/acme and new node bootstrap flows</div>
      <div class="pad row" style="align-items:center;gap:10px;margin-top:8px">
        <label style="display:flex;align-items:center;gap:8px;cursor:pointer">
          <input type="checkbox" id="trafficCollectionChk" class="cb"{{if .TrafficCollectionEnabled}} checked{{end}}>
          collect traffic stats from primary node (xray + sing-box APIs, every 30s)
        </label>
        <button id="saveTrafficCollectionBtn" class="btn secondary">save</button>
      </div>
      <div class="pad muted">when enabled, proxyctl polls local xray/sing-box stats API and accumulates per-user RX/TX counters in the database</div>
    </section>
  </div>

  <!-- Inbound modal -->
  <div id="inboundModal" class="modal-overlay hidden">
    <div class="modal">
      <div class="modal-hdr">
        <h3 id="inboundModalTitle">Create Inbound</h3>
        <button class="modal-close" id="closeInboundModalBtn">&#215;</button>
      </div>
      <div class="modal-body">
        <div class="frow">
          <span class="flabel">Protocol</span>
          <select id="inType">
            <option value="vless">VLESS &mdash; xray</option>
            <option value="xhttp">XHTTP &mdash; xray</option>
            <option value="hysteria2">Hysteria2 &mdash; sing-box</option>
          </select>
        </div>
        <div class="frow">
          <span class="flabel">Node</span>
          <select id="inNode"></select>
        </div>
        <div class="frow">
          <span class="flabel">Domain</span>
          <input id="inDomain" type="text" placeholder="e.g. swe.darksidr.icu">
        </div>
        <div class="frow">
          <span class="flabel">Port</span>
          <input id="inPort" type="number" min="1" max="65535" placeholder="e.g. 8443">
        </div>
        <div class="frow" id="mTransportRow">
          <span class="flabel">Transport</span>
          <select id="inTransport">
            <option value="tcp">TCP (RAW)</option>
            <option value="ws">WebSocket</option>
            <option value="grpc">gRPC</option>
            <option value="xhttp" class="hidden">xhttp</option>
            <option value="udp" class="hidden">udp</option>
          </select>
        </div>
        <div class="frow hidden" id="mPathRow">
          <span class="flabel">Path</span>
          <input id="inPath" type="text" placeholder="/path">
        </div>
        <div class="frow" id="mSecurityRow">
          <span class="flabel">Security</span>
          <div class="sec-tabs" id="mSecTabs">
            <button type="button" class="sec-tab active" data-sec="none">None</button>
            <button type="button" class="sec-tab" data-sec="reality" id="mSecTabReality">Reality</button>
            <button type="button" class="sec-tab" data-sec="tls">TLS</button>
          </div>
        </div>
        <select id="inSecurityMode" class="hidden">
          <option value="none">none</option>
          <option value="tls">tls</option>
          <option value="reality">reality</option>
        </select>
        <input type="hidden" id="inEngine" value="xray">
        <select id="inMode" class="hidden">
          <option value="basic">basic</option>
          <option value="advanced">advanced</option>
        </select>
        <select id="inBrowserPreset" class="hidden">
          <option value="">custom</option>
          <option value="chrome">chrome</option>
          <option value="firefox">firefox</option>
          <option value="safari">safari</option>
        </select>
        <datalist id="inTargetList"></datalist>
        <datalist id="inSniList"></datalist>
        <div id="inLinkWrap" class="hidden"></div>
        <div id="mRealityBlock" class="modal-block hidden">
          <div class="modal-block-hdr">Reality Settings</div>
          <div class="frow">
            <span class="flabel">Fingerprint (uTLS)</span>
            <select id="inRealityFingerprint">
              <option value="chrome">Chrome</option>
              <option value="firefox">Firefox</option>
              <option value="safari">Safari</option>
              <option value="edge">Edge</option>
              <option value="ios">iOS</option>
              <option value="random">Random</option>
            </select>
          </div>
          <div class="frow" id="grpRealityTarget">
            <span class="flabel">Target (dest)</span>
            <div class="frow-inline">
              <input id="inTarget" type="text" list="inTargetList" placeholder="www.example.com:443" style="flex:1">
              <button type="button" id="inTargetRegen" title="Pick random preset" style="padding:0 10px;font-size:1rem;background:var(--bg2);border:1px solid var(--border);border-radius:4px;cursor:pointer;color:var(--text)">↻</button>
            </div>
          </div>
          <div class="frow" id="grpRealitySNI">
            <span class="flabel">SNI</span>
            <div class="frow-inline">
              <input id="inSni" type="text" list="inSniList" placeholder="auto from target" style="flex:1">
              <label class="toggle" style="white-space:nowrap">
                <input id="inLinkTargetSni" type="checkbox" checked>
                <span class="toggle-track"></span>
                <span class="toggle-label">= Target</span>
              </label>
            </div>
          </div>
          <div class="frow" id="grpSelfSteal">
            <span class="flabel">Self Steal</span>
            <label style="display:flex;align-items:center;gap:8px;cursor:pointer">
              <input type="checkbox" id="inSelfSteal" name="self_steal" value="1" class="cb">
              <span style="font-size:0.85rem">использовать собственный домен как цель Reality</span>
            </label>
          </div>
          <div class="frow" id="grpRealityServer">
            <span class="flabel">Dest Server</span>
            <div class="frow-inline">
              <input id="inRealityServer" type="text" placeholder="www.example.com" style="flex:1">
              <input id="inRealityServerPort" type="number" min="1" max="65535" value="443" style="width:80px">
            </div>
          </div>
          <div class="frow">
            <span class="flabel">Short ID <span style="font-weight:normal">(auto if empty)</span></span>
            <input id="inRealityShortID" type="text" placeholder="e.g. 797e">
          </div>
          <div class="frow">
            <span class="flabel">SpiderX</span>
            <input id="inRealitySpiderX" type="text" placeholder="/">
          </div>
          <div class="frow">
            <span class="flabel"></span>
            <div class="frow-inline" style="gap:8px">
              <button type="button" id="inRealityGenKeys" class="btn secondary" style="font-size:0.82rem;padding:4px 12px">⚙ Generate Keys</button>
              <button type="button" id="inRealityClearKeys" class="btn secondary" style="font-size:0.82rem;padding:4px 12px">✕ Clear</button>
            </div>
          </div>
          <div class="frow">
            <span class="flabel">Public Key</span>
            <input id="inRealityPublicKey" type="text" placeholder="reality public key">
          </div>
          <div class="frow">
            <span class="flabel">Private Key</span>
            <input id="inRealityPrivateKey" type="text" placeholder="reality private key">
          </div>
          <div class="frow">
            <span class="flabel">Flow</span>
            <select id="inVlessFlow">
              <option value="xtls-rprx-vision">xtls-rprx-vision</option>
              <option value="">(none)</option>
            </select>
          </div>
        </div>
      </div>
      <div class="modal-block">
        <div style="display:flex;align-items:center;justify-content:space-between">
          <span class="modal-block-hdr" style="margin:0">Sniffing</span>
          <label class="toggle">
            <input type="checkbox" id="inSniffingEnabled">
            <span class="toggle-track"></span>
            <span class="toggle-label">detect protocol &amp; redirect</span>
          </label>
        </div>
        <div id="mSniffingRow" class="hidden" style="display:flex;gap:16px;flex-wrap:wrap;padding-top:8px;border-top:1px solid var(--line)">
          <label class="toggle"><input type="checkbox" id="inSniffingHTTP" checked><span class="toggle-track"></span><span class="toggle-label">HTTP</span></label>
          <label class="toggle"><input type="checkbox" id="inSniffingTLS" checked><span class="toggle-track"></span><span class="toggle-label">TLS</span></label>
          <label class="toggle"><input type="checkbox" id="inSniffingQUIC"><span class="toggle-track"></span><span class="toggle-label">QUIC</span></label>
          <label class="toggle"><input type="checkbox" id="inSniffingFakeDNS"><span class="toggle-track"></span><span class="toggle-label">FakeDNS</span></label>
        </div>
      </div>
      <div class="modal-ftr">
        <button type="button" class="btn secondary" id="cancelInboundEditBtn">Cancel</button>
        <button type="button" class="btn" id="createInboundBtn">Create</button>
      </div>
    </div>
  </div>

  <!-- Node modal -->
  <div id="nodeModal" class="modal-overlay hidden">
    <div class="modal">
      <div class="modal-hdr">
        <h3 id="nodeModalTitle">Add Node</h3>
        <button class="modal-close" id="closeNodeModalBtn">&#215;</button>
      </div>
      <div class="modal-body">
        <div class="frow">
          <span class="flabel">Name</span>
          <input id="ndName" type="text" placeholder="e.g. node-01">
        </div>
        <div class="frow">
          <span class="flabel">Host / IP</span>
          <input id="ndHost" type="text" placeholder="e.g. 1.2.3.4">
        </div>
        <div class="frow">
          <span class="flabel">Role</span>
          <select id="ndRole">
            <option value="node">node</option>
            <option value="primary">primary</option>
          </select>
        </div>
        <div class="frow">
          <span class="flabel">SSH User</span>
          <input id="ndSSHUser" type="text" placeholder="root">
        </div>
        <div class="frow">
          <span class="flabel">SSH Port</span>
          <input id="ndSSHPort" type="number" value="22" min="1" max="65535" style="width:100px">
        </div>
        <div class="frow">
          <span class="flabel">Hardening</span>
          <div style="display:flex;flex-direction:column;gap:8px">
            <label style="display:flex;align-items:center;gap:8px;cursor:pointer">
              <input id="ndDisableIPv6" type="checkbox" class="cb">
              <span style="font-size:0.85rem">Disable IPv6</span>
            </label>
            <label style="display:flex;align-items:center;gap:8px;cursor:pointer">
              <input id="ndBlockPing" type="checkbox" class="cb">
              <span style="font-size:0.85rem">Block ICMP ping</span>
            </label>
          </div>
        </div>
        <div id="ndPasswordRow" class="frow">
          <span class="flabel">Password</span>
          <input id="ndPassword" type="password" placeholder="for first-time bootstrap (optional, not stored)">
        </div>
        <div id="ndPasswordHint" class="muted" style="font-size:0.78rem;margin:-8px 0 6px 0;padding-left:100px;line-height:1.4">
          Provide SSH root password to automatically install SSH key + bootstrap the node.<br>
          Leave empty to do it manually from the edit modal after saving.
        </div>
        <div id="ndMaintenanceSection" class="modal-block hidden">
          <div class="modal-block-hdr">maintenance</div>
          <div style="display:flex;align-items:center;gap:10px;margin-top:6px;flex-wrap:wrap">
            <span class="muted" style="font-size:0.8rem">remote version:</span>
            <span id="ndRemoteVersion" class="mono" style="font-size:0.82rem">—</span>
            <button type="button" class="btn secondary" id="ndFetchVersionBtn" style="padding:3px 10px;font-size:0.78rem">check</button>
          </div>
          <div id="ndSshOpsGroup" style="display:flex;gap:8px;flex-wrap:wrap;margin-top:8px">
            <button type="button" class="btn secondary" id="ndSetupSshKeyBtn">setup ssh key</button>
            <button type="button" class="btn secondary" id="ndBootstrapBtn">bootstrap</button>
            <button type="button" class="btn warn" id="ndUpdateProxyctlBtn">update proxyctl</button>
          </div>
          <div id="ndHardeningOpsGroup" style="display:flex;gap:8px;flex-wrap:wrap;margin-top:8px">
            <button type="button" class="btn secondary" id="ndApplyHardeningBtn">apply hardening</button>
          </div>
          <div id="ndSyncGroup" style="display:flex;gap:8px;flex-wrap:wrap;margin-top:8px">
            <button type="button" class="btn secondary" id="ndSyncPrimaryBtn">sync now</button>
          </div>
          <div class="muted" style="font-size:0.75rem;margin-top:8px;line-height:1.5">
            <b>setup ssh key</b> — installs panel public key on node for passwordless sync<br>
            <b>bootstrap</b> — (re)installs xray / sing-box / caddy on node<br>
            <b>apply hardening</b> — writes sysctl rules for selected hardening options (IPv6 / ping) without full reinstall<br>
            <b>update proxyctl</b> — runs <code>proxyctl update --force</code> on the remote node<br>
            <b>sync now</b> — regenerates configs and restarts proxy services on this node
          </div>
        </div>
        <div class="modal-ftr">
          <button type="button" class="btn secondary" id="cancelNodeModalBtn">Cancel</button>
          <button type="button" class="btn" id="saveNodeBtn">Save</button>
        </div>
      </div>
    </div>
  </div>

  <!-- Node SSH password prompt modal -->
  <div id="nodePassModal" class="modal-overlay hidden">
    <div class="modal" style="max-width:420px">
      <div class="modal-hdr">
        <h3 id="nodePassModalTitle">SSH Password</h3>
        <button class="modal-close" id="closeNodePassModalBtn">&#215;</button>
      </div>
      <div class="modal-body">
        <div id="nodePassModalDesc" class="muted" style="margin-bottom:12px;font-size:0.9rem"></div>
        <div class="frow">
          <span class="flabel">Password</span>
          <input id="nodePassInput" type="password" placeholder="leave empty if SSH key already works">
        </div>
        <div class="modal-ftr">
          <button type="button" class="btn secondary" id="cancelNodePassModalBtn">Cancel</button>
          <button type="button" class="btn" id="confirmNodePassBtn">Confirm</button>
        </div>
      </div>
    </div>
  </div>

  <div id="nodeDeleteModal" class="modal-overlay hidden">
    <div class="modal" style="max-width:500px">
      <div class="modal-hdr">
        <h3>Delete Node</h3>
        <button class="modal-close" id="closeNodeDeleteModalBtn">&#215;</button>
      </div>
      <div class="modal-body">
        <div style="margin-bottom:16px;padding:12px 16px;background:var(--bg2,#1e1e1e);border-radius:6px;border:1px solid var(--border,#333)">
          <div id="nodeDeleteModalName" style="font-weight:600;font-size:1.05rem;margin-bottom:2px"></div>
          <div id="nodeDeleteModalHost" style="font-size:0.85rem;opacity:0.6;font-family:monospace"></div>
        </div>
        <div style="margin-bottom:20px;padding:12px 16px;background:rgba(220,50,50,0.08);border:1px solid rgba(220,50,50,0.3);border-radius:6px;font-size:0.88rem;line-height:1.6">
          <div style="font-weight:600;margin-bottom:8px;color:#e05555">This action cannot be undone.</div>
          The following will be permanently removed from the server:
          <ul style="margin:8px 0 0 0;padding-left:18px;opacity:0.85">
            <li>All proxy services (xray, sing-box, caddy, nginx) — stopped and disabled</li>
            <li>All configuration files <span style="font-family:monospace;font-size:0.85em">/etc/proxy-orchestrator/</span></li>
            <li>Database and subscriptions <span style="font-family:monospace;font-size:0.85em">/var/lib/proxy-orchestrator/</span></li>
            <li>SSL certificates <span style="font-family:monospace;font-size:0.85em">/caddy/</span></li>
            <li>Binaries: proxyctl, xray, sing-box</li>
          </ul>
        </div>
        <div class="modal-ftr">
          <button type="button" class="btn secondary" id="cancelNodeDeleteModalBtn">Cancel</button>
          <button type="button" class="btn err" id="confirmNodeDeleteBtn">Delete Node</button>
        </div>
      </div>
    </div>
  </div>

  <div id="credEditModal" class="modal-overlay hidden">
    <div class="modal" style="max-width:420px">
      <div class="modal-hdr">
        <h3>Edit Credential</h3>
        <button class="modal-close" id="closeCredEditModalBtn">&#215;</button>
      </div>
      <div class="modal-body">
        <input type="hidden" id="credEditID">
        <input type="hidden" id="credEditVersion">
        <div class="form-group">
          <label>Label</label>
          <input type="text" id="credEditLabel" placeholder="e.g. Kamil iPhone" style="width:100%">
        </div>
        <div class="modal-ftr">
          <button type="button" class="btn secondary" id="cancelCredEditBtn">Cancel</button>
          <button type="button" class="btn primary" id="saveCredEditBtn">Save</button>
        </div>
      </div>
    </div>
  </div>

  <div id="nodeInfoModal" class="modal-overlay hidden">
    <div class="modal" style="max-width:540px">
      <div class="modal-hdr">
        <h3 id="nodeInfoModalTitle">Node Diagnostics</h3>
        <button class="modal-close" id="closeNodeInfoModalBtn">&#215;</button>
      </div>
      <div class="modal-body" id="nodeInfoModalBody">
        <div id="nodeInfoLoading" style="text-align:center;padding:20px;color:var(--muted,#888)">Loading…</div>
        <div id="nodeInfoContent" class="hidden"></div>
      </div>
      <div class="modal-ftr">
        <button type="button" class="btn secondary" id="nodeInfoRefreshBtn">Refresh</button>
        <button type="button" class="btn secondary" id="closeNodeInfoBtn">Close</button>
      </div>
    </div>
  </div>

  <!-- Subscription edit label modal -->
  <div id="subEditModal" class="modal-overlay hidden">
    <div class="modal" style="max-width:420px">
      <div class="modal-hdr">
        <h3>Edit Subscription</h3>
        <button class="modal-close" id="closeSubEditModalBtn">&#215;</button>
      </div>
      <div class="modal-body">
        <input type="hidden" id="subEditUser">
        <input type="hidden" id="subEditProfile">
        <div class="form-group">
          <label>Label</label>
          <input type="text" id="subEditLabel" placeholder="e.g. Kamil — all nodes" style="width:100%">
        </div>
      </div>
      <div class="modal-ftr">
        <button type="button" class="btn secondary" id="cancelSubEditBtn">Cancel</button>
        <button type="button" class="btn" id="saveSubEditBtn">Save</button>
      </div>
    </div>
  </div>

  <!-- Subscription modal -->
  <div id="subModal" class="modal-overlay hidden">
    <div class="modal" style="max-width:860px;width:95vw">
      <div class="modal-hdr">
        <h3>Manage Subscription</h3>
        <button class="modal-close" id="closeSubModalBtn">&#215;</button>
      </div>
      <div class="modal-body">
        <div class="frow">
          <span class="flabel">Profile</span>
          <div class="frow-inline">
            <select id="subProfileSel" style="width:160px"></select>
            <input id="subProfile" type="text" placeholder="new profile name" style="flex:1">
          </div>
        </div>
        <div id="subInboundPick"></div>
      </div>
      <div class="modal-ftr">
        <button type="button" class="btn secondary" id="closeSubModalBtn2">Cancel</button>
        <button type="button" class="btn secondary" id="detachSelectedCredsBtn">Detach selected creds</button>
        <button type="button" class="btn secondary" id="genSelectedSubBtn">Generate selected</button>
        <button type="button" class="btn" id="genSubBtn">Generate for user</button>
      </div>
    </div>
  </div>

  <script>
    const cfgRaw = {
      basePath: {{printf "%q" .BasePath}},
      logoutPath: {{printf "%q" .LogoutPath}},
      snapshotPath: {{printf "%q" .SnapshotPath}},
      dashboardActionPath: {{printf "%q" .DashboardActionPath}},
      settingsActionPath: {{printf "%q" .SettingsActionPath}},
      usersActionPath: {{printf "%q" .UsersActionPath}},
      nodesActionPath: {{printf "%q" .NodesActionPath}},
      inboundsActionPath: {{printf "%q" .InboundsActionPath}},
      subsActionPath: {{printf "%q" .SubsActionPath}},
    };
    function normalizeBasePath(raw) {
      let s = String(raw || "/").trim();
      s = s.replace(/^"+|"+$/g, "").replace(/^'+|'+$/g, "");
      if (!s) return "/";
      if (!s.startsWith("/")) s = "/" + s;
      s = s.replace(/\/{2,}/g, "/").replace(/\/+$/, "");
      return s || "/";
    }
    function joinPath(base, suffix) {
      const b = normalizeBasePath(base);
      const s = String(suffix || "").replace(/^\/+/, "");
      if (!s) return b;
      return b === "/" ? "/" + s : b + "/" + s;
    }
    function detectBasePath() {
      const path = normalizeBasePath(window.location.pathname || "/");
      if (path.endsWith("/app")) {
        const b = path.slice(0, -4);
        return normalizeBasePath(b || "/");
      }
      if (path !== "/") {
        return normalizeBasePath(path);
      }
      return normalizeBasePath(cfgRaw.basePath || "/");
    }
    const cfg = (() => {
      const basePath = detectBasePath();
      return {
        basePath,
        logoutPath: joinPath(basePath, "logout"),
        snapshotPath: joinPath(basePath, "api/snapshot"),
        dashboardActionPath: joinPath(basePath, "actions"),
        settingsActionPath: joinPath(basePath, "settings/action"),
        usersActionPath: joinPath(basePath, "users/action"),
        nodesActionPath: joinPath(basePath, "nodes/action"),
        nodeJobsPath: joinPath(basePath, "api/node-jobs"),
        inboundsActionPath: joinPath(basePath, "inbounds/action"),
        subsActionPath: joinPath(basePath, "subscriptions/action"),
      };
    })();

    let snapshot = null;
    let opTimer = null;
    const STORAGE_KEY_LAST_OP_PREFIX = "proxyctl.app.lastOpSeenKey";
    let lastOpSeenKey = "";
    let selectedSubInboundBySelection = {};
    let inboundSniOptions = [];
    let editingInboundID = "";
    let editingInboundVersion = "";
    let editingInboundEnabled = true;
    // activeNodeJobs: nodeID → jobID (for showing spinner in the node table)
    let activeNodeJobs = {};
    let inboundSniManualOverride = false;
    const REALITY_PRESETS = [
      { target: "www.amd.com:443", fingerprint: "chrome" },
      { target: "www.intel.com:443", fingerprint: "chrome" },
      { target: "www.microsoft.com:443", fingerprint: "edge" },
      { target: "www.apple.com:443", fingerprint: "safari" },
      { target: "addons.mozilla.org:443", fingerprint: "firefox" },
      { target: "www.logitech.com:443", fingerprint: "chrome" },
      { target: "www.asus.com:443", fingerprint: "chrome" },
      { target: "www.samsung.com:443", fingerprint: "chrome" },
      { target: "aws.amazon.com:443", fingerprint: "chrome" },
      { target: "github.com:443", fingerprint: "chrome" },
    ];
    const browserPresetDefaults = {
      chrome: { target: "www.google.com:443", fingerprint: "chrome" },
      firefox: { target: "addons.mozilla.org:443", fingerprint: "firefox" },
      safari: { target: "www.apple.com:443", fingerprint: "safari" },
    };
    let liveTimer = null;
    let liveBusy = false;
    const STORAGE_KEY_LIVE_INTERVAL = "proxyctl.app.liveIntervalMs";
    const STORAGE_KEY_LIVE_ENABLED = "proxyctl.app.liveEnabled";
    function lastOpStorageKey() {
      return STORAGE_KEY_LAST_OP_PREFIX + ":" + String(cfg.basePath || "/");
    }
    function loadLastOpSeenKey() {
      try {
        const v = localStorage.getItem(lastOpStorageKey());
        return String(v || "");
      } catch (e) {
        return "";
      }
    }
    function saveLastOpSeenKey(v) {
      try {
        localStorage.setItem(lastOpStorageKey(), String(v || ""));
      } catch (e) {
      }
    }

    function esc(v) {
      return String(v ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
    }
    function fmtBytes(bytes) {
      const n = Number(bytes || 0);
      if (!Number.isFinite(n) || n <= 0) return "0 B";
      const units = ["B", "KB", "MB", "GB", "TB", "PB"];
      let value = n;
      let i = 0;
      while (value >= 1024 && i < units.length - 1) {
        value /= 1024;
        i++;
      }
      const fixed = value >= 100 || i === 0 ? 0 : (value >= 10 ? 1 : 2);
      return value.toFixed(fixed) + " " + units[i];
    }
    function fmtUptime(seconds) {
      const s = Math.max(0, Number(seconds || 0) | 0);
      const d = Math.floor(s / 86400);
      const h = Math.floor((s % 86400) / 3600);
      const m = Math.floor((s % 3600) / 60);
      if (d > 0) return d + "d " + h + "h";
      if (h > 0) return h + "h " + m + "m";
      return m + "m";
    }
    function setTab(name) {
      document.querySelectorAll("[data-tab]").forEach((btn) => {
        const active = btn.getAttribute("data-tab") === name;
        btn.classList.toggle("active", active);
      });
      document.querySelectorAll("[data-tab-section]").forEach((sec) => {
        const show = sec.getAttribute("data-tab-section") === name;
        sec.style.display = show ? "" : "none";
      });
    }
    function showOp(status, message) {
      const op = document.getElementById("op");
      if (!op) return;
      if (opTimer) {
        clearTimeout(opTimer);
        opTimer = null;
      }
      if (!message) {
        op.style.display = "none";
        op.textContent = "";
        op.className = "op";
        return;
      }
      op.style.display = "block";
      op.textContent = message;
      op.className = "op " + (status === "ok" ? "ok" : "error");
      opTimer = setTimeout(() => {
        op.style.display = "none";
      }, 4500);
    }
    async function getSnapshot() {
      const res = await fetch(cfg.snapshotPath, { headers: { "Accept": "application/json" } });
      if (!res.ok) {
        const body = await res.text();
        const msg = (body || "").trim();
        throw new Error("snapshot request failed" + (msg ? ": " + msg : ""));
      }
      snapshot = await res.json();
      // Hide loading overlay on first successful load.
      const overlay = document.getElementById("loadOverlay");
      if (overlay && overlay.style.display !== "none") {
        const bar = document.getElementById("loadBar");
        if (bar) { bar.style.animation = "none"; bar.style.width = "100%"; }
        overlay.classList.add("done");
        setTimeout(() => { overlay.style.display = "none"; }, 460);
      }
      render();
    }
    // fetchRaw: like postForm but returns raw JSON without reloading the snapshot.
    async function fetchRaw(path, form) {
      const res = await fetch(path, {
        method: "POST",
        headers: { "Accept": "application/json" },
        body: new URLSearchParams(form),
      });
      if (!res.ok) {
        const body = await res.text();
        throw new Error((body || "").trim() || "request failed (" + res.status + ")");
      }
      return res.json();
    }
    async function pollSnapshotSilently() {
      if (liveBusy) return;
      liveBusy = true;
      try {
        await getSnapshot();
      } catch (e) {
        showOp("error", String(e));
      } finally {
        liveBusy = false;
      }
    }
    function stopLivePolling() {
      if (liveTimer) {
        clearInterval(liveTimer);
        liveTimer = null;
      }
    }
    function startLivePolling() {
      stopLivePolling();
      const enabled = !!document.getElementById("liveMode")?.checked;
      if (!enabled) return;
      const intervalRaw = Number(document.getElementById("liveInterval")?.value || 5000);
      const intervalMs = Number.isFinite(intervalRaw) && intervalRaw >= 1000 ? intervalRaw : 5000;
      liveTimer = setInterval(() => {
        if (document.hidden) return;
        pollSnapshotSilently();
      }, intervalMs);
    }
    function restoreLivePrefs() {
      const modeEl = document.getElementById("liveMode");
      const intEl = document.getElementById("liveInterval");
      if (!modeEl || !intEl) return;
      const storedMode = localStorage.getItem(STORAGE_KEY_LIVE_ENABLED);
      if (storedMode === "1" || storedMode === "0") {
        modeEl.checked = storedMode === "1";
      }
      const storedInt = localStorage.getItem(STORAGE_KEY_LIVE_INTERVAL);
      if (storedInt && Array.from(intEl.options).some((o) => o.value === storedInt)) {
        intEl.value = storedInt;
      }
    }
    async function postForm(path, form) {
      const res = await fetch(path, {
        method: "POST",
        headers: { "Accept": "application/json" },
        body: new URLSearchParams(form),
      });
      if (!res.ok) {
        const body = await res.text();
        const msg = (body || "").trim();
        throw new Error("action request failed (" + res.status + ")" + (msg ? ": " + msg : ""));
      }
      const out = await res.json();
      lastOpSeenKey = [String(out?.status || ""), String(out?.message || ""), String(out?.at || "")].join("|");
      saveLastOpSeenKey(lastOpSeenKey);
      showOp(out.status, out.message);
      if (out.job_id) {
        const nid = String(out.node_id || "").trim();
        pollNodeJob(out.job_id, nid);
      } else {
        await getSnapshot();
      }
    }
    function pollNodeJob(jobID, nodeID) {
      if (nodeID) activeNodeJobs[nodeID] = jobID;
      const poll = async () => {
        try {
          const res = await fetch(cfg.nodeJobsPath + "?id=" + encodeURIComponent(jobID), { headers: { "Accept": "application/json" } });
          if (!res.ok) throw new Error("poll failed: " + res.status);
          const job = await res.json();
          if (job.done) {
            if (job.nodeID && activeNodeJobs[job.nodeID] === jobID) delete activeNodeJobs[job.nodeID];
            showOp(job.ok ? "ok" : "error", job.msg || "");
            await getSnapshot();
          } else {
            setTimeout(poll, 2000);
          }
        } catch (e) {
          if (nodeID && activeNodeJobs[nodeID] === jobID) delete activeNodeJobs[nodeID];
          showOp("error", String(e));
        }
      };
      setTimeout(poll, 2000);
    }
    // Node Info Modal
    let nodeInfoCurrentID = null;
    let nodeInfoPollTimer = null;
    function openNodeInfoModal(nodeID) {
      nodeInfoCurrentID = nodeID;
      const modal = document.getElementById("nodeInfoModal");
      const node = (snapshot && snapshot.nodes) ? snapshot.nodes.find((n) => n.ID === nodeID) : null;
      document.getElementById("nodeInfoModalTitle").textContent = "Diagnostics" + (node ? ": " + node.Name : "");
      document.getElementById("nodeInfoLoading").classList.remove("hidden");
      document.getElementById("nodeInfoContent").classList.add("hidden");
      document.getElementById("nodeInfoContent").innerHTML = "";
      modal.classList.remove("hidden");
      fetchNodeInfo(nodeID);
    }
    async function fetchNodeInfo(nodeID) {
      document.getElementById("nodeInfoLoading").classList.remove("hidden");
      document.getElementById("nodeInfoContent").classList.add("hidden");
      try {
        const res = await fetch(cfg.nodesActionPath, {
          method: "POST",
          headers: { "Content-Type": "application/x-www-form-urlencoded", "Accept": "application/json" },
          body: new URLSearchParams({ op: "fetch_node_info", node_id: nodeID }),
        });
        if (!res.ok) throw new Error("request failed: " + res.status);
        const out = await res.json();
        if (out.job_id) {
          pollNodeInfoJob(out.job_id, nodeID);
        } else {
          renderNodeInfoError(out.message || "unknown error");
        }
      } catch (e) {
        renderNodeInfoError(String(e));
      }
    }
    function pollNodeInfoJob(jobID, nodeID) {
      if (nodeInfoPollTimer) { clearTimeout(nodeInfoPollTimer); nodeInfoPollTimer = null; }
      const poll = async () => {
        if (nodeInfoCurrentID !== nodeID) return;
        try {
          const res = await fetch(cfg.nodeJobsPath + "?id=" + encodeURIComponent(jobID), { headers: { "Accept": "application/json" } });
          if (!res.ok) throw new Error("poll failed: " + res.status);
          const job = await res.json();
          if (job.done) {
            if (job.ok) {
              try {
                const data = JSON.parse(job.msg);
                renderNodeInfoData(data);
              } catch (_) {
                renderNodeInfoError(job.msg || "parse error");
              }
            } else {
              renderNodeInfoError(job.msg || "diagnostics failed");
            }
          } else {
            nodeInfoPollTimer = setTimeout(poll, 2000);
          }
        } catch (e) {
          renderNodeInfoError(String(e));
        }
      };
      nodeInfoPollTimer = setTimeout(poll, 2000);
    }
    function renderNodeInfoError(msg) {
      document.getElementById("nodeInfoLoading").classList.add("hidden");
      const el = document.getElementById("nodeInfoContent");
      el.innerHTML = '<div style="color:var(--err,#e05555);padding:8px 0">' + esc(msg) + '</div>';
      el.classList.remove("hidden");
    }
    function renderNodeInfoData(data) {
      document.getElementById("nodeInfoLoading").classList.add("hidden");
      const el = document.getElementById("nodeInfoContent");
      const statusColor = (s) => {
        if (s === "active") return "var(--ok,#4caf50)";
        if (s === "inactive") return "var(--muted,#888)";
        return "var(--err,#e05555)";
      };
      let html = '<table style="width:100%;border-collapse:collapse;font-size:0.9rem">';
      html += '<thead><tr><th style="text-align:left;padding:4px 8px;border-bottom:1px solid var(--border,#333)">Service</th><th style="text-align:left;padding:4px 8px;border-bottom:1px solid var(--border,#333)">Status</th></tr></thead><tbody>';
      (data.services || []).forEach((svc) => {
        html += '<tr><td style="padding:4px 8px;font-family:monospace">' + esc(svc.name) + '</td><td style="padding:4px 8px;color:' + statusColor(svc.status) + '">' + esc(svc.status) + '</td></tr>';
      });
      html += '</tbody></table>';
      if ((data.certs || []).length > 0) {
        html += '<div style="margin-top:14px"><table style="width:100%;border-collapse:collapse;font-size:0.9rem">';
        html += '<thead><tr><th style="text-align:left;padding:4px 8px;border-bottom:1px solid var(--border,#333)">Domain</th><th style="text-align:left;padding:4px 8px;border-bottom:1px solid var(--border,#333)">Certificate</th></tr></thead><tbody>';
        (data.certs || []).forEach((c) => {
          const certText = c.missing ? "MISSING" : esc(c.expiry);
          const certColor = c.missing ? "var(--err,#e05555)" : "var(--ok,#4caf50)";
          html += '<tr><td style="padding:4px 8px;font-family:monospace">' + esc(c.domain) + '</td><td style="padding:4px 8px;color:' + certColor + '">' + certText + '</td></tr>';
        });
        html += '</tbody></table></div>';
      }
      if (data.version) {
        html += '<div style="margin-top:12px;font-size:0.85rem;color:var(--muted,#888)">proxyctl: <span style="font-family:monospace;color:var(--fg,#eee)">' + esc(data.version) + '</span></div>';
      }
      el.innerHTML = html;
      el.classList.remove("hidden");
    }
    document.getElementById("closeNodeInfoModalBtn").addEventListener("click", () => {
      nodeInfoCurrentID = null;
      if (nodeInfoPollTimer) { clearTimeout(nodeInfoPollTimer); nodeInfoPollTimer = null; }
      document.getElementById("nodeInfoModal").classList.add("hidden");
    });
    document.getElementById("closeNodeInfoBtn").addEventListener("click", () => {
      nodeInfoCurrentID = null;
      if (nodeInfoPollTimer) { clearTimeout(nodeInfoPollTimer); nodeInfoPollTimer = null; }
      document.getElementById("nodeInfoModal").classList.add("hidden");
    });
    document.getElementById("nodeInfoRefreshBtn").addEventListener("click", () => {
      if (nodeInfoCurrentID) fetchNodeInfo(nodeInfoCurrentID);
    });
    // Prompts for SSH password via a modal. Returns a Promise<string>.
    function promptNodePass(title, desc) {
      return new Promise((resolve, reject) => {
        const modal = document.getElementById("nodePassModal");
        document.getElementById("nodePassModalTitle").textContent = title || "SSH Password";
        document.getElementById("nodePassModalDesc").textContent = desc || "";
        document.getElementById("nodePassInput").value = "";
        modal.classList.remove("hidden");
        setTimeout(() => { const el = document.getElementById("nodePassInput"); if (el) el.focus(); }, 50);
        const confirmBtn = document.getElementById("confirmNodePassBtn");
        const cancelBtn = document.getElementById("cancelNodePassModalBtn");
        const closeBtn = document.getElementById("closeNodePassModalBtn");
        function cleanup() {
          modal.classList.add("hidden");
          confirmBtn.removeEventListener("click", onConfirm);
          cancelBtn.removeEventListener("click", onCancel);
          closeBtn.removeEventListener("click", onCancel);
          modal.removeEventListener("keydown", onKey);
        }
        function onConfirm() { const p = (document.getElementById("nodePassInput").value || "").trim(); cleanup(); resolve(p); }
        function onCancel() { cleanup(); reject(new Error("cancelled")); }
        function onKey(e) { if (e.key === "Enter") onConfirm(); if (e.key === "Escape") onCancel(); }
        confirmBtn.addEventListener("click", onConfirm);
        cancelBtn.addEventListener("click", onCancel);
        closeBtn.addEventListener("click", onCancel);
        modal.addEventListener("keydown", onKey);
      });
    }
    function promptNodeDelete(name, host) {
      return new Promise((resolve, reject) => {
        const modal = document.getElementById("nodeDeleteModal");
        document.getElementById("nodeDeleteModalName").textContent = name || "Unknown Node";
        document.getElementById("nodeDeleteModalHost").textContent = host || "";
        modal.classList.remove("hidden");
        setTimeout(() => { const el = document.getElementById("confirmNodeDeleteBtn"); if (el) el.focus(); }, 50);
        const confirmBtn = document.getElementById("confirmNodeDeleteBtn");
        const cancelBtn = document.getElementById("cancelNodeDeleteModalBtn");
        const closeBtn = document.getElementById("closeNodeDeleteModalBtn");
        function cleanup() {
          modal.classList.add("hidden");
          confirmBtn.removeEventListener("click", onConfirm);
          cancelBtn.removeEventListener("click", onCancel);
          closeBtn.removeEventListener("click", onCancel);
          modal.removeEventListener("keydown", onKey);
        }
        function onConfirm() { cleanup(); resolve(); }
        function onCancel() { cleanup(); reject(new Error("cancelled")); }
        function onKey(e) { if (e.key === "Escape") onCancel(); }
        confirmBtn.addEventListener("click", onConfirm);
        cancelBtn.addEventListener("click", onCancel);
        closeBtn.addEventListener("click", onCancel);
        modal.addEventListener("keydown", onKey);
      });
    }

    // Credential edit modal
    document.getElementById("closeSubEditModalBtn").addEventListener("click", () => {
      document.getElementById("subEditModal").classList.add("hidden");
    });
    document.getElementById("cancelSubEditBtn").addEventListener("click", () => {
      document.getElementById("subEditModal").classList.add("hidden");
    });
    document.getElementById("subEditModal").addEventListener("click", (e) => {
      if (e.target === document.getElementById("subEditModal")) document.getElementById("subEditModal").classList.add("hidden");
    });
    document.getElementById("saveSubEditBtn").addEventListener("click", async () => {
      const userID = document.getElementById("subEditUser").value.trim();
      const profile = document.getElementById("subEditProfile").value.trim();
      const label = document.getElementById("subEditLabel").value.trim();
      try {
        await postForm(cfg.subsActionPath, { op: "set_label", user_id: userID, profile, label });
        document.getElementById("subEditModal").classList.add("hidden");
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.getElementById("closeCredEditModalBtn").addEventListener("click", () => {
      document.getElementById("credEditModal").classList.add("hidden");
    });
    document.getElementById("cancelCredEditBtn").addEventListener("click", () => {
      document.getElementById("credEditModal").classList.add("hidden");
    });
    document.getElementById("credEditModal").addEventListener("keydown", (e) => {
      if (e.key === "Escape") document.getElementById("credEditModal").classList.add("hidden");
    });
    document.getElementById("saveCredEditBtn").addEventListener("click", async () => {
      const credID = document.getElementById("credEditID").value;
      const version = document.getElementById("credEditVersion").value;
      const label = document.getElementById("credEditLabel").value.trim();
      try {
        await postForm(cfg.subsActionPath, {
          op: "update_credential",
          credential_id: credID,
          label: label,
          version: version,
        });
        document.getElementById("credEditModal").classList.add("hidden");
      } catch (e) {
        showOp("error", String(e));
      }
    });

    function openNodeModal(node) {
      const isEdit = !!node;
      const role = node ? (node.Role || "node") : "node";
      document.getElementById("nodeModalTitle").textContent = isEdit ? "Edit Node" : "Add Node";
      document.getElementById("ndName").value = node ? (node.Name || "") : "";
      document.getElementById("ndHost").value = node ? (node.Host || "") : "";
      document.getElementById("ndRole").value = role;
      document.getElementById("ndSSHUser").value = node ? (node.SSHUser || "") : "";
      document.getElementById("ndSSHPort").value = node ? (node.SSHPort || 22) : 22;
      document.getElementById("ndDisableIPv6").checked = node ? !!node.DisableIPv6 : false;
      document.getElementById("ndBlockPing").checked = node ? !!node.BlockPing : false;
      document.getElementById("ndPassword").value = "";
      const pwRow = document.getElementById("ndPasswordRow");
      if (pwRow) pwRow.classList.toggle("hidden", isEdit);
      const remVerEl = document.getElementById("ndRemoteVersion");
      if (remVerEl) remVerEl.textContent = (node && node.RemoteVersion) ? node.RemoteVersion : "—";
      const maintenanceSection = document.getElementById("ndMaintenanceSection");
      if (maintenanceSection) maintenanceSection.classList.toggle("hidden", !isEdit);
      const ndSshOps = document.getElementById("ndSshOpsGroup");
      if (ndSshOps) ndSshOps.classList.toggle("hidden", role === "primary");
      const ndHardeningOps = document.getElementById("ndHardeningOpsGroup");
      if (ndHardeningOps) ndHardeningOps.classList.toggle("hidden", !isEdit);
      const ndSyncGroup = document.getElementById("ndSyncGroup");
      if (ndSyncGroup) ndSyncGroup.classList.toggle("hidden", role !== "primary");
      const modal = document.getElementById("nodeModal");
      modal.setAttribute("data-node-id", node ? (node.ID || "") : "");
      modal.setAttribute("data-node-version", node ? (node.Version || "") : "");
      modal.setAttribute("data-node-name", node ? (node.Name || "") : "");
      modal.setAttribute("data-node-host", node ? (node.Host || "") : "");
      modal.classList.remove("hidden");
      setTimeout(() => { const el = document.getElementById("ndName"); if (el) el.focus(); }, 50);
    }
    function closeNodeModal() {
      document.getElementById("nodeModal").classList.add("hidden");
    }
    function syncOpFromSnapshot() {
      const status = String(snapshot?.OperationStatus || "");
      const message = String(snapshot?.OperationMessage || "");
      const at = String(snapshot?.OperationAt || "");
      if (!message) {
        showOp("", "");
        return;
      }
      const key = [status, message, at].join("|");
      if (key === lastOpSeenKey) {
        return;
      }
      lastOpSeenKey = key;
      saveLastOpSeenKey(lastOpSeenKey);
      showOp(status, message);
    }
    function updateSubButtons() {
      if (!snapshot) return;
      const subUserSel = document.getElementById("subUser");
      if (!subUserSel) return;
      const selected = selectedSubscriptionDetail();
      const enableBtn = document.getElementById("subEnableBtn");
      const disableBtn = document.getElementById("subDisableBtn");
      if (enableBtn) {
        enableBtn.disabled = !!(selected && selected.Enabled);
      }
      if (disableBtn) {
        disableBtn.disabled = !!(!selected || !selected.Enabled);
      }
    }
    function getSuggestedInboundPort(type, transport, nodeID) {
      const allPorts = (snapshot && snapshot.SuggestedPorts) ? snapshot.SuggestedPorts : {};
      const key = String(type || "").trim().toLowerCase() + "|" + String(transport || "").trim().toLowerCase();
      const nid = String(nodeID || "").trim();
      // look up per-node first
      if (nid && allPorts[nid]) {
        const v = allPorts[nid][key];
        const n = Number(v);
        if (Number.isInteger(n) && n > 0) return n;
      }
      return 0;
    }
    function updateInboundPortSuggestion(force) {
      const type = (document.getElementById("inType").value || "").trim();
      const transport = (document.getElementById("inTransport").value || "").trim();
      const nodeID = (document.getElementById("inNode")?.value || "").trim();
      const portEl = document.getElementById("inPort");
      if (!portEl) return;
      const suggested = getSuggestedInboundPort(type, transport, nodeID);
      if (!suggested) return;
      if (force || !(portEl.value || "").trim()) {
        portEl.value = String(suggested);
      }
    }
    function updateInboundTargetToSni(force) {
      const targetEl = document.getElementById("inTarget");
      const sniEl = document.getElementById("inSni");
      const linkEl = document.getElementById("inLinkTargetSni");
      if (!targetEl || !sniEl || !linkEl) return;
      const linked = !!linkEl.checked;
      const target = (targetEl.value || "").trim();
      if (!linked || (!force && inboundSniManualOverride)) return;
      const host = target.includes(":") ? target.split(":")[0] : target;
      sniEl.value = host;
      if (force) inboundSniManualOverride = false;
    }
    function updateDestServerFromTarget() {
      const targetEl = document.getElementById("inTarget");
      const serverEl = document.getElementById("inRealityServer");
      const portEl = document.getElementById("inRealityServerPort");
      if (!targetEl || !serverEl || !portEl) return;
      const target = (targetEl.value || "").trim();
      if (!target) return;
      if (target.includes(":")) {
        const colonIdx = target.lastIndexOf(":");
        const host = target.slice(0, colonIdx);
        const port = target.slice(colonIdx + 1);
        if (host) serverEl.value = host;
        if (/^\d+$/.test(port)) portEl.value = port;
      } else {
        serverEl.value = target;
      }
    }
    function updateSelfStealVisibility() {
      const selfStealEl = document.getElementById("inSelfSteal");
      if (!selfStealEl) return;
      const checked = !!selfStealEl.checked;
      const hide = checked ? "none" : "";
      const grpRealityServer = document.getElementById("grpRealityServer");
      if (grpRealityServer) grpRealityServer.style.display = hide;
      const grpRealityTarget = document.getElementById("grpRealityTarget");
      if (grpRealityTarget) grpRealityTarget.style.display = hide;
      const grpRealitySNI = document.getElementById("grpRealitySNI");
      if (grpRealitySNI) grpRealitySNI.style.display = hide;
    }
    function pickRandomRealityPreset() {
      return REALITY_PRESETS[Math.floor(Math.random() * REALITY_PRESETS.length)];
    }
    function applyRealityPreset(preset) {
      const targetEl = document.getElementById("inTarget");
      if (targetEl) targetEl.value = preset.target;
      const fpEl = document.getElementById("inRealityFingerprint");
      if (fpEl) fpEl.value = preset.fingerprint;
      updateDestServerFromTarget();
      updateInboundTargetToSni(false);
    }
    async function generateRealityKeyPair() {
      const toBase64url = (bytes) =>
        btoa(String.fromCharCode(...bytes))
          .replace(/\+/g, "-").replace(/\//g, "_").replace(/=/g, "");
      const keyPair = await crypto.subtle.generateKey(
        { name: "X25519" }, true, ["deriveKey", "deriveBits"]
      );
      const pubRaw = new Uint8Array(await crypto.subtle.exportKey("raw", keyPair.publicKey));
      const privPkcs8 = new Uint8Array(await crypto.subtle.exportKey("pkcs8", keyPair.privateKey));
      return {
        publicKey: toBase64url(pubRaw),
        privateKey: toBase64url(privPkcs8.slice(-32)),
      };
    }
    function updateInboundAdvancedVisibility() {
      // no-op: advanced block replaced by modal layout
    }
    function applyBrowserPreset() {
      const preset = (document.getElementById("inBrowserPreset")?.value || "").trim().toLowerCase();
      if (!preset || !browserPresetDefaults[preset]) return;
      const data = browserPresetDefaults[preset];
      const targetEl = document.getElementById("inTarget");
      if (targetEl && !(targetEl.value || "").trim()) targetEl.value = data.target;
      const fpEl = document.getElementById("inRealityFingerprint");
      if (fpEl) fpEl.value = data.fingerprint;
      updateDestServerFromTarget();
      updateInboundTargetToSni(false);
    }
    function updateInboundSniInputState() {
      const sniEl = document.getElementById("inSni");
      const targetEl = document.getElementById("inTarget");
      const linkEl = document.getElementById("inLinkTargetSni");
      if (!sniEl || !targetEl || !linkEl) return;
      const sniSupported = !sniEl.disabled;
      if (!sniSupported) {
        targetEl.value = "";
        sniEl.value = "";
        linkEl.checked = true;
        inboundSniManualOverride = false;
      }
      targetEl.disabled = !sniSupported;
      linkEl.disabled = !sniSupported;
      sniEl.placeholder = sniSupported ? "sni value" : "sni disabled";
      targetEl.placeholder = sniSupported ? "www.example.com:443" : "target disabled";
    }
    function selectedInboundNodeHost() {
      const nodeID = (document.getElementById("inNode")?.value || "").trim();
      const nodes = Array.isArray(snapshot?.Nodes) ? snapshot.Nodes : [];
      const node = nodes.find((n) => String(n?.ID || "").trim() === nodeID);
      return String(node?.Host || "").trim();
    }
    function updateInboundDomainFromNode(force) {
      const domainEl = document.getElementById("inDomain");
      if (!domainEl) return;
      const host = selectedInboundNodeHost();
      if (!host) return;
      const current = (domainEl.value || "").trim();
      if (force || !current) {
        domainEl.value = host;
      }
    }
    function resetInboundCreateDefaults() {
      closeInboundModal();
      editingInboundID = "";
      editingInboundVersion = "";
      editingInboundEnabled = true;
      const createBtn = document.getElementById("createInboundBtn");
      if (createBtn) createBtn.textContent = "Create";
      const cancelBtn = document.getElementById("cancelInboundEditBtn");
      if (cancelBtn) cancelBtn.textContent = "Cancel";
      const target = document.getElementById("inTarget");
      if (target) target.value = "";
      const link = document.getElementById("inLinkTargetSni");
      if (link) link.checked = true;
      inboundSniManualOverride = false;
      const sni = document.getElementById("inSni");
      if (sni) sni.value = "";
      const mode = document.getElementById("inMode");
      if (mode) mode.value = "basic";
      setModalSecTab("none");
      const preset = document.getElementById("inBrowserPreset");
      if (preset) preset.value = "";
      const selfStealEl = document.getElementById("inSelfSteal");
      if (selfStealEl) selfStealEl.checked = !!(snapshot && snapshot.DefaultSelfSteal);
      updateSelfStealVisibility();
      const realityServer = document.getElementById("inRealityServer");
      if (realityServer) realityServer.value = "";
      const realityServerPort = document.getElementById("inRealityServerPort");
      if (realityServerPort) realityServerPort.value = "443";
      const realityFp = document.getElementById("inRealityFingerprint");
      if (realityFp) {
        const fps = ["chrome", "firefox", "safari", "edge", "ios", "random"];
        realityFp.value = fps[Math.floor(Math.random() * fps.length)];
      }
      const realityPb = document.getElementById("inRealityPublicKey");
      if (realityPb) realityPb.value = "";
      const realityPr = document.getElementById("inRealityPrivateKey");
      if (realityPr) realityPr.value = "";
      const realitySid = document.getElementById("inRealityShortID");
      if (realitySid) realitySid.value = "";
      const realitySpider = document.getElementById("inRealitySpiderX");
      if (realitySpider) realitySpider.value = "";
      const vlessFlow = document.getElementById("inVlessFlow");
      if (vlessFlow) vlessFlow.value = "xtls-rprx-vision";
      const sniffEn = document.getElementById("inSniffingEnabled");
      if (sniffEn) sniffEn.checked = false;
      document.getElementById("mSniffingRow")?.classList.add("hidden");
      const sniffHTTP = document.getElementById("inSniffingHTTP");
      if (sniffHTTP) sniffHTTP.checked = true;
      const sniffTLS = document.getElementById("inSniffingTLS");
      if (sniffTLS) sniffTLS.checked = true;
      const sniffQUIC = document.getElementById("inSniffingQUIC");
      if (sniffQUIC) sniffQUIC.checked = false;
      const sniffFake = document.getElementById("inSniffingFakeDNS");
      if (sniffFake) sniffFake.checked = false;
      const path = document.getElementById("inPath");
      if (path) path.value = "";
      updateInboundDomainFromNode(true);
      updateInboundCreateFieldVisibility(true);
      updateInboundTargetToSni(true);
      updateInboundAdvancedVisibility();
    }
    function beginInboundEdit(inbound) {
      console.log('[beginInboundEdit] called with:', JSON.stringify({ID: inbound?.ID, Domain: inbound?.Domain, Port: inbound?.Port, NodeID: inbound?.NodeID, Transport: inbound?.Transport, RealityEnabled: inbound?.RealityEnabled, TLS: inbound?.TLS}));
      if (!inbound || !inbound.ID) { console.warn('[beginInboundEdit] no inbound or no ID, aborting'); return; }
      editingInboundID = String(inbound.ID || "").trim();
      editingInboundVersion = String(inbound.Version || "").trim();
      editingInboundEnabled = !!inbound.Enabled;
      const modalTitle = document.getElementById("inboundModalTitle");
      if (modalTitle) modalTitle.textContent = "Edit Inbound";
      const createBtn = document.getElementById("createInboundBtn");
      if (createBtn) createBtn.textContent = "Save";
      openInboundModal();
      document.getElementById("inType").value = String(inbound.Type || "vless").trim() || "vless";
      updateInboundCreateFieldVisibility(true);
      const transportEl = document.getElementById("inTransport");
      if (transportEl) {
        const nextTransport = String(inbound.Transport || "").trim().toLowerCase();
        if (nextTransport && Array.from(transportEl.options).some((o) => o.value === nextTransport)) {
          transportEl.value = nextTransport;
        }
      }
      const engineEl = document.getElementById("inEngine");
      if (engineEl) {
        engineEl.value = String(inbound.Engine || "").trim();
      }
      const nodeEl = document.getElementById("inNode");
      if (nodeEl) {
        const nextNodeID = String(inbound.NodeID || "").trim();
        const nodeInOptions = Array.from(nodeEl.options).some((o) => o.value === nextNodeID);
        console.log('[beginInboundEdit] setting node:', nextNodeID, 'found in options:', nodeInOptions, 'available options:', Array.from(nodeEl.options).map(o => o.value));
        if (nextNodeID && nodeInOptions) {
          nodeEl.value = nextNodeID;
        }
      }
      document.getElementById("inDomain").value = String(inbound.Domain || "").trim();
      document.getElementById("inPort").value = String(inbound.Port || "").trim();
      console.log('[beginInboundEdit] after setting: node=', document.getElementById("inNode")?.value, 'domain=', document.getElementById("inDomain")?.value, 'port=', document.getElementById("inPort")?.value);
      document.getElementById("inPath").value = String(inbound.Path || "").trim();
      const sni = String(inbound.SNI || "").trim();
      const targetEl = document.getElementById("inTarget");
      const linkEl = document.getElementById("inLinkTargetSni");
      const sniEl = document.getElementById("inSni");
      const realityHost = String(inbound.RealityServer || "").trim();
      const realityPort = inbound.RealityServerPort || 443;
      if (targetEl) targetEl.value = realityHost ? realityHost + ":" + realityPort : sni;
      if (linkEl) linkEl.checked = true;
      inboundSniManualOverride = false;
      if (sniEl) {
        sniEl.value = sni;
      }
      const modeEl = document.getElementById("inMode");
      if (modeEl) modeEl.value = inbound.RealityEnabled ? "advanced" : "basic";
      {
        const activeSec = inbound.RealityEnabled ? "reality" : (inbound.TLS ? "tls" : "none");
        setModalSecTab(activeSec);
      }
      const selfStealEl = document.getElementById("inSelfSteal");
      if (selfStealEl) selfStealEl.checked = !!inbound.SelfSteal;
      updateSelfStealVisibility();
      const realityServer = document.getElementById("inRealityServer");
      if (realityServer) realityServer.value = String(inbound.RealityServer || "").trim();
      const realityServerPort = document.getElementById("inRealityServerPort");
      if (realityServerPort) realityServerPort.value = String(inbound.RealityServerPort || 443);
      const realityFp = document.getElementById("inRealityFingerprint");
      if (realityFp) realityFp.value = String(inbound.RealityFingerprint || "chrome").trim() || "chrome";
      const realityPb = document.getElementById("inRealityPublicKey");
      if (realityPb) realityPb.value = String(inbound.RealityPublicKey || "").trim();
      const realityPr = document.getElementById("inRealityPrivateKey");
      if (realityPr) realityPr.value = String(inbound.RealityPrivateKey || "").trim();
      const realitySid = document.getElementById("inRealityShortID");
      if (realitySid) realitySid.value = String(inbound.RealityShortID || "").trim();
      const realitySpider = document.getElementById("inRealitySpiderX");
      if (realitySpider) realitySpider.value = String(inbound.RealitySpiderX || "").trim();
      const vlessFlow = document.getElementById("inVlessFlow");
      if (vlessFlow) vlessFlow.value = String(inbound.VLESSFlow || "xtls-rprx-vision").trim() || "xtls-rprx-vision";
      const sniffEn = document.getElementById("inSniffingEnabled");
      if (sniffEn) sniffEn.checked = !!inbound.SniffingEnabled;
      const sniffHTTP = document.getElementById("inSniffingHTTP");
      if (sniffHTTP) sniffHTTP.checked = !!inbound.SniffingHTTP;
      const sniffTLS = document.getElementById("inSniffingTLS");
      if (sniffTLS) sniffTLS.checked = !!inbound.SniffingTLS;
      const sniffQUIC = document.getElementById("inSniffingQUIC");
      if (sniffQUIC) sniffQUIC.checked = !!inbound.SniffingQUIC;
      const sniffFake = document.getElementById("inSniffingFakeDNS");
      if (sniffFake) sniffFake.checked = !!inbound.SniffingFakeDNS;
      document.getElementById("mSniffingRow")?.classList.toggle("hidden", !inbound.SniffingEnabled);
      updateInboundCreateFieldVisibility(false);
      updateInboundTargetToSni(true);
      updateInboundAdvancedVisibility();
      console.log('[beginInboundEdit] FINAL DOM values - node:', document.getElementById("inNode")?.value, 'domain:', document.getElementById("inDomain")?.value, 'port:', document.getElementById("inPort")?.value, 'transport:', document.getElementById("inTransport")?.value, 'security:', document.getElementById("inSecurityMode")?.value);
    }
    function refreshInboundSniList(nodes, inbounds) {
      const presets = (snapshot && Array.isArray(snapshot.SNIPresets)) ? snapshot.SNIPresets : [];
      const set = new Set((Array.isArray(presets) ? presets : []).map((v) => String(v || "").trim()).filter(Boolean));
      if (set.size === 0) {
        (Array.isArray(nodes) ? nodes : []).forEach((n) => {
          const host = String((n && n.Host) || "").trim();
          if (host) set.add(host);
        });
        (Array.isArray(inbounds) ? inbounds : []).forEach((i) => {
          const domain = String((i && i.Domain) || "").trim();
          if (domain) set.add(domain);
        });
      }
      inboundSniOptions = Array.from(set).sort((a, b) => a.localeCompare(b));
      const list = document.getElementById("inSniList");
      const targetList = document.getElementById("inTargetList");
      const html = inboundSniOptions.map((v) => '<option value="'+esc(v)+'"></option>').join("");
      if (list) list.innerHTML = html;
      if (targetList) targetList.innerHTML = html;
    }
    function setTransportOptions(options) {
      const sel = document.getElementById("inTransport");
      if (!sel) return;
      const prev = (sel.value || "").trim();
      sel.innerHTML = options.map((v) => '<option value="'+esc(v)+'">'+esc(v)+'</option>').join("");
      if (prev && options.includes(prev)) {
        sel.value = prev;
      } else if (options.length > 0) {
        sel.value = options[0];
      }
    }
    function updateInboundCreateFieldVisibility(forcePort) {
      const type = (document.getElementById("inType")?.value || "").trim().toLowerCase();
      const secEl = document.getElementById("inSecurityMode");
      const engineEl = document.getElementById("inEngine");
      const transportSel = document.getElementById("inTransport");
      const pathEl = document.getElementById("inPath");
      const mTransportRow = document.getElementById("mTransportRow");
      const mPathRow = document.getElementById("mPathRow");
      const mSecurityRow = document.getElementById("mSecurityRow");
      const mSecTabReality = document.getElementById("mSecTabReality");
      const mRealityBlock = document.getElementById("mRealityBlock");

      const sec = (secEl?.value || "none").trim().toLowerCase();

      if (type === "hysteria2") {
        if (engineEl) engineEl.value = "sing-box";
        setTransportOptions(["udp"]);
        if (transportSel) transportSel.value = "udp";
        if (secEl) secEl.value = "tls";
        if (mTransportRow) mTransportRow.classList.add("hidden");
        if (mPathRow) mPathRow.classList.add("hidden");
        if (mSecurityRow) mSecurityRow.classList.add("hidden");
        if (mRealityBlock) mRealityBlock.classList.add("hidden");
        setModalSecTab("tls");
      } else if (type === "xhttp") {
        if (engineEl) engineEl.value = "xray";
        setTransportOptions(["xhttp"]);
        if (transportSel) transportSel.value = "xhttp";
        if (mTransportRow) mTransportRow.classList.add("hidden");
        if (mPathRow) mPathRow.classList.remove("hidden");
        if (mSecurityRow) mSecurityRow.classList.remove("hidden");
        if (mSecTabReality) mSecTabReality.classList.add("hidden");
        if (sec === "reality") {
          if (secEl) secEl.value = "tls";
          setModalSecTab("tls");
        }
        if (mRealityBlock) mRealityBlock.classList.add("hidden");
        if (!pathEl?.value) { if (pathEl) pathEl.value = "/xhttp"; }
      } else {
        // vless
        if (engineEl) engineEl.value = "xray";
        if (mTransportRow) mTransportRow.classList.remove("hidden");
        if (mSecurityRow) mSecurityRow.classList.remove("hidden");
        if (mSecTabReality) mSecTabReality.classList.remove("hidden");
        if (sec === "reality") {
          setTransportOptions(["tcp"]);
          if (transportSel) transportSel.disabled = true;
          if (mRealityBlock) mRealityBlock.classList.remove("hidden");
          if (mPathRow) mPathRow.classList.add("hidden");
        } else {
          setTransportOptions(["tcp", "ws", "grpc"]);
          if (transportSel) transportSel.disabled = false;
          if (mRealityBlock) mRealityBlock.classList.add("hidden");
          const transport = (transportSel?.value || "").trim().toLowerCase();
          const pathNeeded = transport === "ws" || transport === "grpc";
          if (mPathRow) mPathRow.classList.toggle("hidden", !pathNeeded);
          if (pathNeeded && pathEl && !pathEl.value) {
            if (transport === "ws") pathEl.value = "/ws";
            if (transport === "grpc") pathEl.value = "grpc";
          }
        }
      }

      updateInboundPortSuggestion(!!forcePort);
      updateInboundTargetToSni(false);
    }
    function openInboundModal() {
      const modal = document.getElementById("inboundModal");
      if (modal) modal.classList.remove("hidden");
    }
    function closeInboundModal() {
      const modal = document.getElementById("inboundModal");
      if (modal) modal.classList.add("hidden");
    }
    function setModalSecTab(sec) {
      const secEl = document.getElementById("inSecurityMode");
      if (secEl) secEl.value = sec;
      document.querySelectorAll(".sec-tab").forEach((btn) => {
        btn.classList.toggle("active", btn.getAttribute("data-sec") === sec);
      });
      const mRealityBlock = document.getElementById("mRealityBlock");
      if (mRealityBlock) mRealityBlock.classList.toggle("hidden", sec !== "reality");
      if (sec === "reality") {
        const targetEl = document.getElementById("inTarget");
        if (!(targetEl?.value || "").trim()) applyRealityPreset(pickRandomRealityPreset());
        const sidEl = document.getElementById("inRealityShortID");
        if (sidEl && !(sidEl.value || "").trim()) {
          sidEl.value = Array.from(crypto.getRandomValues(new Uint8Array(8)))
            .map(b => b.toString(16).padStart(2, "0")).join("").slice(0, 8);
        }
        const spiderEl = document.getElementById("inRealitySpiderX");
        if (spiderEl && !(spiderEl.value || "").trim()) spiderEl.value = "/";
      }
    }
    function currentSubUserID() {
      const subUserSel = document.getElementById("subUser");
      return subUserSel ? (subUserSel.value || "").trim() : "";
    }
    function currentSubProfileName() {
      const sel = document.getElementById("subProfileSel");
      if (sel && (sel.value || "").trim()) return (sel.value || "").trim();
      const input = document.getElementById("subProfile");
      return input ? (input.value || "").trim() : "";
    }
    function currentSubSelectionKey() {
      const userID = currentSubUserID();
      const profile = currentSubProfileName() || "default";
      return userID + "::" + profile;
    }
    function selectedSubscriptionDetail() {
      const subDetails = Array.isArray(snapshot?.SubscriptionDetails) ? snapshot.SubscriptionDetails : [];
      const userID = currentSubUserID();
      const profile = currentSubProfileName() || "default";
      return subDetails.find((s) => String(s.UserID || "").trim() === userID && String(s.ProfileName || "").trim() === profile) || null;
    }
    function renderSubInboundPick(inbounds) {
      const pick = document.getElementById("subInboundPick");
      if (!pick) return;
      const key = currentSubSelectionKey();
      const selected = new Set(Array.isArray(selectedSubInboundBySelection[key]) ? selectedSubInboundBySelection[key] : []);
      if (selected.size === 0) {
        const selectedDetail = selectedSubscriptionDetail();
        const defaults = Array.isArray(selectedDetail?.InboundIDs) ? selectedDetail.InboundIDs : [];
        for (const id of defaults) selected.add(String(id || "").trim());
      }
      // Build inboundID → credential label map for the currently selected user.
      const subUserID = (document.getElementById("subUser")?.value || "").trim();
      const credLabelByInbound = {};
      const creds = Array.isArray(snapshot?.Credentials) ? snapshot.Credentials : [];
      for (const c of creds) {
        if (String(c.UserID || "").trim() !== subUserID) continue;
        const iid = String(c.InboundID || "").trim();
        if (!iid) continue;
        const lbl = String(c.ClientLabel || "").trim();
        if (lbl && !credLabelByInbound[iid]) credLabelByInbound[iid] = lbl;
      }
      pick.innerHTML = [
        '<div class="label" style="margin-bottom:8px">selected profile inbounds</div>',
        '<div class="table-wrap">',
        '<table>',
        '<thead><tr><th><input type="checkbox" class="cb" id="subInboundAll"></th><th>label</th><th>node</th><th>domain</th><th>port</th><th>type</th><th>transport</th><th>path</th><th>sni</th><th>enabled</th></tr></thead>',
        '<tbody>',
        inbounds.map((i) => {
          const checked = selected.has(String(i.ID)) ? ' checked' : '';
          const label = credLabelByInbound[String(i.ID)] ||
            [String(i.Type || "").trim(), String(i.Domain || "").trim() + ":" + String(i.Port || "")].join(" ").trim();
          return (
            '<tr>' +
              '<td><input type="checkbox" class="cb" data-sub-inbound-id="'+esc(i.ID)+'"'+checked+'></td>' +
              '<td>'+esc(label)+'</td>' +
              '<td>'+esc(i.NodeName || i.NodeID || "")+'</td>' +
              '<td>'+esc(i.Domain || "")+'</td>' +
              '<td>'+esc(i.Port)+'</td>' +
              '<td>'+esc(i.Type || "")+'</td>' +
              '<td>'+esc(i.Transport || "")+'</td>' +
              '<td>'+esc(i.Path || "")+'</td>' +
              '<td>'+esc(i.SNI || "")+'</td>' +
              '<td>'+ (i.Enabled ? "yes" : "no") +'</td>' +
            '</tr>'
          );
        }).join(""),
        '</tbody>',
        '</table>',
        '</div>',
      ].join("");
      const allBox = pick.querySelector("#subInboundAll");
      const syncAllBox = () => {
        const boxes = Array.from(pick.querySelectorAll("[data-sub-inbound-id]"));
        const checkedCount = boxes.filter((el) => el.checked).length;
        if (allBox) {
          allBox.checked = boxes.length > 0 && checkedCount === boxes.length;
          allBox.indeterminate = checkedCount > 0 && checkedCount < boxes.length;
        }
      };
      pick.querySelectorAll("[data-sub-inbound-id]").forEach((box) => {
        box.addEventListener("change", () => {
          const ids = Array.from(pick.querySelectorAll("[data-sub-inbound-id]:checked"))
            .map((el) => (el.getAttribute("data-sub-inbound-id") || "").trim())
            .filter(Boolean);
          if (key) {
            selectedSubInboundBySelection[key] = ids;
          }
          syncAllBox();
        });
      });
      if (allBox) {
        allBox.addEventListener("change", () => {
          const checked = !!allBox.checked;
          pick.querySelectorAll("[data-sub-inbound-id]").forEach((el) => {
            el.checked = checked;
          });
          const ids = checked
            ? Array.from(pick.querySelectorAll("[data-sub-inbound-id]"))
                .map((el) => (el.getAttribute("data-sub-inbound-id") || "").trim())
                .filter(Boolean)
            : [];
          if (key) {
            selectedSubInboundBySelection[key] = ids;
          }
          syncAllBox();
        });
      }
      syncAllBox();
    }
    function selectedSubInboundIDs() {
      return Array.from(document.querySelectorAll("[data-sub-inbound-id]:checked"))
        .map((el) => (el.getAttribute("data-sub-inbound-id") || "").trim())
        .filter(Boolean);
    }

    // ── Dashboard rendering ──────────────────────────────────────────────────
    const CIRC = 2 * Math.PI * 43; // circumference for r=43
    function setGauge(arcID, pctID, subID, pct, label, sub) {
      const arc = document.getElementById(arcID);
      const pctEl = document.getElementById(pctID);
      const subEl = document.getElementById(subID);
      if (!arc || !pctEl) return;
      const clamped = Math.max(0, Math.min(100, pct));
      const offset = CIRC * (1 - clamped / 100);
      arc.style.strokeDasharray = CIRC.toFixed(1) + " " + CIRC.toFixed(1);
      arc.style.strokeDashoffset = offset.toFixed(1);
      pctEl.textContent = label;
      if (subEl) subEl.textContent = sub;
    }
    function statRows(pairs) {
      return pairs.map(([k, v]) =>
        '<div class="stat-row"><span class="stat-label">'+esc(k)+'</span><span class="stat-value">'+esc(v)+'</span></div>'
      ).join("");
    }
    function renderDashboard(snap) {
      const dash = snap.Dashboard || {};
      const units = Array.isArray(snap.Units) ? snap.Units : [];

      // CPU gauge
      const cpuPct = Number(dash.CPUPercent || 0);
      setGauge("gaugeCPUArc", "gaugeCPUPct", "gaugeCPUSub",
        cpuPct, cpuPct.toFixed(1)+"%", (dash.CPUCores || 0)+" cores · load "+((dash.Load1||0).toFixed(2)));

      // RAM gauge
      const ramTotal = Number(dash.MemTotalBytes || 0);
      const ramUsed = Number(dash.MemUsedBytes || 0);
      const ramPct = ramTotal > 0 ? (ramUsed / ramTotal * 100) : 0;
      setGauge("gaugeRAMArc", "gaugeRAMPct", "gaugeRAMSub",
        ramPct, ramPct.toFixed(1)+"%", fmtBytes(ramUsed)+" / "+fmtBytes(ramTotal));

      // Swap gauge
      const swapTotal = Number(dash.SwapTotalBytes || 0);
      const swapUsed = Number(dash.SwapUsedBytes || 0);
      const swapPct = swapTotal > 0 ? (swapUsed / swapTotal * 100) : 0;
      setGauge("gaugeSwapArc", "gaugeSwapPct", "gaugeSwapSub",
        swapPct, swapTotal > 0 ? swapPct.toFixed(1)+"%" : "—",
        swapTotal > 0 ? fmtBytes(swapUsed)+" / "+fmtBytes(swapTotal) : "no swap");

      // Disk gauge
      const diskTotal = Number(dash.DiskTotalBytes || 0);
      const diskUsed = Number(dash.DiskUsedBytes || 0);
      const diskPct = diskTotal > 0 ? (diskUsed / diskTotal * 100) : 0;
      setGauge("gaugeDiskArc", "gaugeDiskPct", "gaugeDiskSub",
        diskPct, diskPct.toFixed(1)+"%", fmtBytes(diskUsed)+" / "+fmtBytes(diskTotal));

      // Services card
      const svcEl = document.getElementById("dashServices");
      if (svcEl) {
        const svcUnits = [
          { name: "xray",     unit: snap.XrayUnit || "proxyctl-xray.service" },
          { name: "sing-box", unit: snap.SingBoxUnit || "proxyctl-sing-box.service" },
          { name: "caddy",    unit: snap.CaddyUnit || "proxyctl-caddy.service" },
        ];
        svcEl.innerHTML = svcUnits.map(({name, unit}) => {
          const st = units.find((u) => u.Unit === unit) || {};
          const active = (st.Active || "").toLowerCase();
          let badge = '<span class="badge-warn">'+esc(active||"unknown")+'</span>';
          if (active === "active") badge = '<span class="badge-ok">active</span>';
          else if (active === "inactive" || active === "failed") badge = '<span class="badge-err">'+esc(active)+'</span>';
          return '<div class="service-row">' +
            '<span class="service-name">'+esc(name)+'</span>' +
            badge +
            '<button class="btn-xs" data-svc-action="restart_unit" data-svc-unit="'+esc(unit)+'">restart</button>' +
            '<button class="btn-xs danger" data-svc-action="stop_unit" data-svc-unit="'+esc(unit)+'">stop</button>' +
            '</div>';
        }).join("");
        svcEl.querySelectorAll("[data-svc-action]").forEach((btn) => {
          btn.addEventListener("click", async () => {
            try {
              await postForm(cfg.dashboardActionPath, {
                action: btn.getAttribute("data-svc-action"),
                unit: btn.getAttribute("data-svc-unit"),
              });
            } catch(e) { showOp("error", String(e)); }
          });
        });
      }

      // System card
      const sysEl = document.getElementById("dashSystem");
      if (sysEl) sysEl.innerHTML = statRows([
        ["uptime",    fmtUptime(dash.UptimeSeconds)],
        ["load 1m",   (dash.Load1||0).toFixed(2)],
        ["load 5m",   (dash.Load5||0).toFixed(2)],
        ["load 15m",  (dash.Load15||0).toFixed(2)],
      ]);

      // Speed card
      const speedEl = document.getElementById("dashSpeed");
      if (speedEl) speedEl.innerHTML = statRows([
        ["↓ download", fmtBytes(dash.NetRXSpeed||0) + "/s"],
        ["↑ upload",   fmtBytes(dash.NetTXSpeed||0) + "/s"],
      ]);

      // Traffic & connections card
      const trafEl = document.getElementById("dashTraffic");
      if (trafEl) {
        trafEl.innerHTML =
          '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">' +
            '<span class="stat-label" style="font-size:0.72rem;text-transform:uppercase;letter-spacing:0.04em">interface totals</span>' +
            '<button class="btn-xs danger" id="resetTotalTrafficBtn">reset</button>' +
          '</div>' +
          statRows([
            ["↓ downloaded",     fmtBytes(dash.TotalRXBytes||0)],
            ["↑ uploaded",       fmtBytes(dash.TotalTXBytes||0)],
            ["total",            fmtBytes(dash.TotalBytes||0)],
            ["TCP connections",  String(dash.TCPConns||0)],
            ["UDP connections",  String(dash.UDPConns||0)],
          ]);
        document.getElementById("resetTotalTrafficBtn")?.addEventListener("click", async () => {
          try { await postForm(cfg.dashboardActionPath, { action: "reset_total_traffic" }); }
          catch(e) { showOp("error", String(e)); }
        });
      }

      // Version & update card — initialize once, no re-render on poll.
      const verEl = document.getElementById("dashVersion");
      if (verEl && !verEl.dataset.init) {
        verEl.dataset.init = "1";
        const current = dash.ProxyctlVersion || "dev";
        verEl.innerHTML =
          statRows([["version", current]]) +
          '<div style="margin-top:10px;display:flex;gap:8px;align-items:center;flex-wrap:wrap">' +
            '<button class="btn-xs" id="checkUpdateBtn">check for update</button>' +
            '<span id="updateStatus" class="muted" style="font-size:0.75rem"></span>' +
          '</div>' +
          '<div id="updateAction" style="margin-top:8px"></div>';
        document.getElementById("checkUpdateBtn")?.addEventListener("click", async () => {
          const statusEl = document.getElementById("updateStatus");
          const actionEl = document.getElementById("updateAction");
          const btn = document.getElementById("checkUpdateBtn");
          if (btn) { btn.disabled = true; btn.textContent = "checking…"; }
          if (statusEl) statusEl.textContent = "";
          if (actionEl) actionEl.innerHTML = "";
          try {
            const out = await fetchRaw(cfg.dashboardActionPath, { action: "check_update" });
            if (btn) { btn.disabled = false; btn.textContent = "check for update"; }
            if (out.error) {
              if (statusEl) statusEl.textContent = "error: " + out.error;
              return;
            }
            const latest = out.latest_version || "";
            const dlURL = out.download_url || "";
            const curVer = out.current_version || current;
            if (!latest) {
              if (statusEl) statusEl.textContent = "no releases found";
            } else if (latest === curVer) {
              if (statusEl) statusEl.textContent = "up to date (" + latest + ")";
            } else {
              if (statusEl) statusEl.textContent = "latest: " + latest;
              if (actionEl) actionEl.innerHTML =
                '<button class="btn warn" style="font-size:0.8rem;padding:5px 10px" id="doUpdateBtn">update to ' + esc(latest) + '</button>';
              document.getElementById("doUpdateBtn")?.addEventListener("click", async () => {
                if (!confirm("Update proxyctl to " + latest + "?\nThe panel must be restarted to apply the new binary.")) return;
                const updateBtn = document.getElementById("doUpdateBtn");
                if (updateBtn) { updateBtn.disabled = true; updateBtn.textContent = "updating…"; }
                try {
                  const res = await fetchRaw(cfg.dashboardActionPath, { action: "update_proxyctl", download_url: dlURL });
                  if (res.error) {
                    if (updateBtn) { updateBtn.disabled = false; updateBtn.textContent = "retry"; }
                    if (statusEl) statusEl.textContent = "error: " + res.error;
                  } else {
                    if (actionEl) actionEl.innerHTML =
                      '<span style="color:var(--ok);font-size:0.8rem">' + esc(res.message || "updated — restart panel") + '</span>';
                    if (statusEl) statusEl.textContent = "";
                  }
                } catch(e) {
                  if (updateBtn) { updateBtn.disabled = false; updateBtn.textContent = "retry"; }
                  if (statusEl) statusEl.textContent = String(e);
                }
              });
            }
          } catch(e) {
            if (btn) { btn.disabled = false; btn.textContent = "check for update"; }
            if (statusEl) statusEl.textContent = String(e);
          }
        });
      }

      // User traffic table
      const userTraffic = Array.isArray(dash.UserTraffic) ? dash.UserTraffic : [];
      const utEl = document.getElementById("userTrafficBody");
      if (utEl) {
        utEl.innerHTML = userTraffic.map((u) =>
          '<tr>' +
            '<td>'+esc(u.UserName || u.UserID)+'</td>' +
            '<td>'+esc(fmtBytes(u.RXBytes))+'</td>' +
            '<td>'+esc(fmtBytes(u.TXBytes))+'</td>' +
            '<td>'+esc(fmtBytes(u.TotalBytes))+'</td>' +
            '<td><button class="btn-xs danger" data-reset-uid="'+esc(u.UserID)+'">reset</button></td>' +
          '</tr>'
        ).join("");
        utEl.querySelectorAll("[data-reset-uid]").forEach((btn) => {
          btn.addEventListener("click", async () => {
            try { await postForm(cfg.dashboardActionPath, { action: "reset_user_traffic", user_id: btn.getAttribute("data-reset-uid") }); }
            catch(e) { showOp("error", String(e)); }
          });
        });
      }
      const metaEl = document.getElementById("trafficMeta");
      if (metaEl) {
        const src = dash.TrafficSource || "none";
        if (src === "none") {
          metaEl.innerHTML = '<span style="color:var(--muted)">per-user traffic stats are not available — requires xray stats API integration</span>';
        } else {
          metaEl.textContent = "per-user stats source: " + src;
        }
      }
    }

    function render() {
      if (!snapshot) return;
      syncOpFromSnapshot();

      const c = snapshot.Counts || {};
      document.getElementById("counts").innerHTML = [
        ["users", c.UsersTotal],
        ["enabled users", c.UsersEnabled],
        ["inbounds", c.InboundsTotal],
        ["active inbounds", c.InboundsActive],
      ].map(([k, v]) => '<div class="card"><div class="label">'+esc(k)+'</div><div class="value">'+esc(v)+'</div></div>').join("");

      renderDashboard(snapshot);

      const users = Array.isArray(snapshot.Users) ? snapshot.Users : [];
      const nodes = Array.isArray(snapshot.Nodes) ? snapshot.Nodes : [];
      const inbounds = Array.isArray(snapshot.Inbounds) ? snapshot.Inbounds : [];
      const creds = Array.isArray(snapshot.Credentials) ? snapshot.Credentials : [];
      const subDetails = Array.isArray(snapshot.SubscriptionDetails) ? snapshot.SubscriptionDetails : [];
      refreshInboundSniList(nodes, inbounds);
      updateInboundCreateFieldVisibility(false);
      function fmtBytes(b) {
        if (!b || b === 0) return "0";
        const units = ["B","KB","MB","GB","TB"];
        let i = 0; let v = b;
        while (v >= 1024 && i < units.length-1) { v /= 1024; i++; }
        return v.toFixed(1)+units[i];
      }
      function userStatus(u) {
        if (!u.Enabled) return '<span class="badge-err">disabled</span>';
        if (u.ExpiresAt && new Date(u.ExpiresAt) < new Date())
          return '<span class="badge-err">expired</span>';
        if (u.TrafficLimitBytes > 0 && (u.UsedRXBytes + u.UsedTXBytes) >= u.TrafficLimitBytes)
          return '<span class="badge-err">over limit</span>';
        return '<span class="badge-ok">active</span>';
      }
      document.getElementById("usersBody").innerHTML = users.map((u) => {
        const expiry = u.ExpiresAt ? esc(u.ExpiresAt.slice(0,10)) : '<span class="muted">∞</span>';
        const limit = u.TrafficLimitBytes > 0 ? esc(fmtBytes(u.TrafficLimitBytes)) : '<span class="muted">∞</span>';
        const usedTotal = (u.UsedRXBytes||0) + (u.UsedTXBytes||0);
        const used = usedTotal > 0
          ? esc(fmtBytes(usedTotal))+' <span class="muted">(↓'+esc(fmtBytes(u.UsedRXBytes||0))+' ↑'+esc(fmtBytes(u.UsedTXBytes||0))+')</span>'
          : '<span class="muted">—</span>';
        return '<tr>' +
          '<td>'+esc(u.Name)+'</td>' +
          '<td>'+expiry+'</td>' +
          '<td>'+limit+'</td>' +
          '<td>'+used+'</td>' +
          '<td>'+userStatus(u)+'</td>' +
          '<td><button class="btn secondary" data-user-edit-id="'+esc(u.ID)+'">edit</button></td>' +
        '</tr>';
      }).join("");

      // User edit modal logic
      let ueCurrentUser = null;
      document.querySelectorAll("[data-user-edit-id]").forEach((btn) => {
        btn.addEventListener("click", () => {
          const uid = btn.getAttribute("data-user-edit-id");
          ueCurrentUser = users.find((u) => u.ID === uid) || null;
          if (!ueCurrentUser) return;
          const u = ueCurrentUser;
          document.getElementById("ueModalName").value = u.Name || "";
          document.getElementById("ueModalExpires").value = u.ExpiresAt ? u.ExpiresAt.slice(0,10) : "";
          document.getElementById("ueModalEnabled").checked = !!u.Enabled;
          // Try to detect unit for traffic limit
          const tlb = u.TrafficLimitBytes || 0;
          let unitEl = document.getElementById("ueModalTrafficUnit");
          let valEl = document.getElementById("ueModalTrafficVal");
          if (tlb === 0) {
            valEl.value = 0;
            unitEl.value = "1073741824";
          } else if (tlb % 1099511627776 === 0) {
            unitEl.value = "1099511627776"; valEl.value = tlb / 1099511627776;
          } else if (tlb % 1073741824 === 0) {
            unitEl.value = "1073741824"; valEl.value = tlb / 1073741824;
          } else if (tlb % 1048576 === 0) {
            unitEl.value = "1048576"; valEl.value = tlb / 1048576;
          } else {
            unitEl.value = "1"; valEl.value = tlb;
          }
          document.getElementById("ueModalErr").classList.add("hidden");
          document.getElementById("userEditModal").classList.remove("hidden");
        });
      });
      document.getElementById("ueModalCancel").onclick = () => {
        document.getElementById("userEditModal").classList.add("hidden");
        ueCurrentUser = null;
      };
      document.getElementById("ueModalExpiresClear").onclick = () => {
        document.getElementById("ueModalExpires").value = "";
      };
      document.getElementById("ueModalSave").onclick = async () => {
        if (!ueCurrentUser) return;
        const errEl = document.getElementById("ueModalErr");
        errEl.classList.add("hidden");
        const trafficVal = parseFloat(document.getElementById("ueModalTrafficVal").value) || 0;
        const trafficUnit = parseInt(document.getElementById("ueModalTrafficUnit").value) || 1;
        const trafficBytes = Math.floor(trafficVal * trafficUnit);
        try {
          await postForm(cfg.usersActionPath, {
            op: "update",
            user_id: ueCurrentUser.ID,
            version: ueCurrentUser.Version,
            name: document.getElementById("ueModalName").value,
            enabled: document.getElementById("ueModalEnabled").checked ? "1" : "0",
            expires_at: document.getElementById("ueModalExpires").value,
            traffic_limit_bytes: String(trafficBytes),
          });
          document.getElementById("userEditModal").classList.add("hidden");
          ueCurrentUser = null;
        } catch (e) {
          errEl.textContent = String(e);
          errEl.classList.remove("hidden");
        }
      };
      document.getElementById("ueModalResetTraffic").onclick = async () => {
        if (!ueCurrentUser) return;
        const errEl = document.getElementById("ueModalErr");
        errEl.classList.add("hidden");
        try {
          await postForm(cfg.usersActionPath, {
            op: "reset_traffic",
            user_id: ueCurrentUser.ID,
          });
          document.getElementById("userEditModal").classList.add("hidden");
          ueCurrentUser = null;
        } catch (e) {
          errEl.textContent = String(e);
          errEl.classList.remove("hidden");
        }
      };
      document.getElementById("ueModalDelete").onclick = async () => {
        if (!ueCurrentUser) return;
        const errEl = document.getElementById("ueModalErr");
        errEl.classList.add("hidden");
        if (!confirm("Delete user "+ueCurrentUser.Name+"?")) return;
        try {
          await postForm(cfg.usersActionPath, {
            op: "delete",
            user_id: ueCurrentUser.ID,
            version: ueCurrentUser.Version,
          });
          document.getElementById("userEditModal").classList.add("hidden");
          ueCurrentUser = null;
        } catch (e) {
          errEl.textContent = String(e);
          errEl.classList.remove("hidden");
        }
      };

      const sshWarn = (snapshot && snapshot.SSHKeyWarning) ? snapshot.SSHKeyWarning : "";
      const sshWarnEl = document.getElementById("nodesSshWarning");
      if (sshWarnEl) {
        if (sshWarn) {
          sshWarnEl.textContent = "⚠ " + sshWarn;
          sshWarnEl.classList.remove("hidden");
        } else {
          sshWarnEl.classList.add("hidden");
        }
      }
      // Merge server-reported active jobs with client-side tracking.
      nodes.forEach((n) => { if (n.JobID && !activeNodeJobs[n.ID]) activeNodeJobs[n.ID] = n.JobID; });
      document.getElementById("nodesBody").innerHTML = nodes.map((n) => {
        const running = !!activeNodeJobs[n.ID];
        let syncBadge = '<span style="color:var(--muted)">—</span>';
        if (running) {
          syncBadge = '<span style="display:inline-flex;align-items:center;gap:5px;color:var(--ok)">' +
            '<span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--ok);animation:pulse 1.2s ease-in-out infinite"></span>running…</span>';
        } else if (n.SyncOK === true) {
          syncBadge = '<span style="color:var(--ok)" title="last sync OK">✓ ready</span>';
        } else if (n.SyncOK === false) {
          syncBadge = '<span style="color:var(--err)" title="'+esc(n.SyncMsg || "")+'">✗ error</span>';
        } else if (n.SyncOK === null && n.Role === "node") {
          syncBadge = '<span style="color:#f0a500" title="Install SSH key then run Bootstrap to activate this node">⚙ setup needed</span>';
        }
        const dis = running ? ' disabled' : '';
        return '<tr id="nrow-'+esc(n.ID)+'">' +
          '<td>'+esc(n.Name)+'</td>' +
          '<td>'+esc(n.Host)+'</td>' +
          '<td><span class="muted">'+esc(n.Role)+'</span></td>' +
          '<td>'+syncBadge+'</td>' +
          '<td class="row">' +
            '<button class="btn secondary" data-node-edit-id="'+esc(n.ID)+'">edit</button>' +
            '<button class="btn secondary"'+dis+' data-node-test-id="'+esc(n.ID)+'" data-node-test-version="'+esc(n.Version)+'">test</button>' +
            '<button class="btn secondary"'+dis+' data-node-info-id="'+esc(n.ID)+'">info</button>' +
            '<button class="btn '+(n.Enabled ? 'warn' : '')+'"'+dis+' data-node-toggle-id="'+esc(n.ID)+'" data-node-toggle-version="'+esc(n.Version)+'" data-node-enabled="'+(n.Enabled ? '1' : '0')+'">'+(n.Enabled ? 'disable' : 'enable')+'</button>' +
            '<button class="btn err"'+dis+' data-node-delete-id="'+esc(n.ID)+'" data-node-delete-version="'+esc(n.Version)+'" data-node-delete-name="'+esc(n.Name)+'" data-node-delete-host="'+esc(n.Host)+'">delete</button>' +
          '</td>' +
        '</tr>';
      }).join("");
      document.querySelectorAll("[data-node-edit-id]").forEach((btn) => {
        btn.addEventListener("click", () => {
          const nodeID = (btn.getAttribute("data-node-edit-id") || "").trim();
          const node = nodes.find((n) => String(n.ID || "").trim() === nodeID);
          if (node) openNodeModal(node);
        });
      });
      document.querySelectorAll("[data-node-delete-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const nodeID = btn.getAttribute("data-node-delete-id");
          const version = btn.getAttribute("data-node-delete-version");
          const nodeName = btn.getAttribute("data-node-delete-name") || nodeID;
          const nodeHost = btn.getAttribute("data-node-delete-host") || "";
          try {
            await promptNodeDelete(nodeName, nodeHost);
          } catch (e) {
            return;
          }
          try {
            await postForm(cfg.nodesActionPath, {
              op: "delete",
              node_id: nodeID,
              version: version,
            });
          } catch (e) {
            showOp("error", String(e));
          }
        });
      });
      document.querySelectorAll("[data-node-test-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            await postForm(cfg.nodesActionPath, {
              op: "test",
              node_id: btn.getAttribute("data-node-test-id"),
              version: btn.getAttribute("data-node-test-version"),
            });
          } catch (e) {
            showOp("error", String(e));
          }
        });
      });
      document.querySelectorAll("[data-node-info-id]").forEach((btn) => {
        btn.addEventListener("click", () => {
          const nodeID = btn.getAttribute("data-node-info-id");
          openNodeInfoModal(nodeID);
        });
      });
      document.querySelectorAll("[data-node-toggle-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            const enabledNow = btn.getAttribute("data-node-enabled") === "1";
            await postForm(cfg.nodesActionPath, {
              op: "set_enabled",
              node_id: btn.getAttribute("data-node-toggle-id"),
              version: btn.getAttribute("data-node-toggle-version"),
              enabled: enabledNow ? "0" : "1",
            });
          } catch (e) {
            showOp("error", String(e));
          }
        });
      });

      const nodeSel = document.getElementById("inNode");
      if (nodeSel) {
        const prev = nodeSel.value || "";
        nodeSel.innerHTML = nodes.map((n) => '<option value="'+esc(n.ID)+'">'+esc(n.Name)+' ('+esc(n.Host)+')</option>').join("");
        if (prev && Array.from(nodeSel.options).some((o) => o.value === prev)) {
          nodeSel.value = prev;
        }
        updateInboundDomainFromNode(false);
      }
      document.getElementById("inboundsBody").innerHTML = inbounds.map((i) => {
        const portCell = i.PortConflict
          ? '<td><span style="color:var(--err);font-weight:600" title="Port conflict: another inbound on this node uses the same port">⚠ '+esc(i.Port)+'</span></td>'
          : '<td>'+esc(i.Port)+'</td>';
        const rowStyle = i.PortConflict ? ' style="background:rgba(251,113,133,0.06)"' : '';
        return '<tr'+rowStyle+'>' +
          '<td class="mono muted">'+esc(i.ID)+'</td>' +
          '<td>'+esc(i.Type)+(i.SelfSteal ? ' <span class="badge-ok" title="Self Steal">SS</span>' : '')+'</td>' +
          '<td>'+esc(i.NodeName)+'</td>' +
          '<td>'+esc(i.Domain)+'</td>' +
          portCell +
          '<td class="row">' +
            '<button class="btn secondary" data-inbound-edit-id="'+esc(i.ID)+'">edit</button>' +
            '<button class="btn '+(i.Enabled ? 'warn' : '')+'" data-inbound-toggle-id="'+esc(i.ID)+'" data-inbound-toggle-version="'+esc(i.Version)+'" data-inbound-enabled="'+(i.Enabled ? '1' : '0')+'">'+(i.Enabled ? 'disable' : 'enable')+'</button>' +
            '<button class="btn err" data-inbound-id="'+esc(i.ID)+'" data-inbound-version="'+esc(i.Version)+'">delete</button>' +
          '</td>' +
        '</tr>';
      }).join("");
      document.querySelectorAll("[data-inbound-toggle-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const enabledNow = btn.getAttribute("data-inbound-enabled") === "1";
          console.log('[inbound toggle] id:', btn.getAttribute("data-inbound-toggle-id"), 'enabledNow:', enabledNow, '→ setting to:', enabledNow ? "0" : "1");
          try {
            await postForm(cfg.inboundsActionPath, {
              op: "set_enabled",
              inbound_id: btn.getAttribute("data-inbound-toggle-id"),
              version: btn.getAttribute("data-inbound-toggle-version"),
              enabled: enabledNow ? "0" : "1",
            });
          } catch (e) {
            showOp("error", String(e));
          }
        });
      });
      document.querySelectorAll("[data-inbound-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          console.log('[inbound delete] id:', btn.getAttribute("data-inbound-id"));
          try {
            await postForm(cfg.inboundsActionPath, {
              op: "delete",
              inbound_id: btn.getAttribute("data-inbound-id"),
              version: btn.getAttribute("data-inbound-version"),
            });
          } catch (e) {
            showOp("error", String(e));
          }
        });
      });

      const subUserSel = document.getElementById("subUser");
      const subProfileSel = document.getElementById("subProfileSel");
      const credUserSel = document.getElementById("credUser");
      if (subUserSel) {
        const prev = subUserSel.value || "";
        subUserSel.innerHTML = users.map((u) => '<option value="'+esc(u.ID)+'">'+esc(u.Name)+' ('+esc(u.ID)+')</option>').join("");
        if (prev && Array.from(subUserSel.options).some((o) => o.value === prev)) {
          subUserSel.value = prev;
        }
      }
      if (subProfileSel) {
        const selectedUser = subUserSel ? (subUserSel.value || "").trim() : "";
        const userProfiles = subDetails
          .filter((s) => String(s.UserID || "").trim() === selectedUser)
          .map((s) => String(s.ProfileName || "").trim())
          .filter((v) => !!v);
        const uniqueProfiles = Array.from(new Set(userProfiles));
        uniqueProfiles.sort((a, b) => (a === "default" ? -1 : b === "default" ? 1 : a.localeCompare(b)));
        const prevProfile = subProfileSel.value || "";
        subProfileSel.innerHTML = uniqueProfiles.map((p) => '<option value="'+esc(p)+'">'+esc(p)+'</option>').join("");
        if (prevProfile && Array.from(subProfileSel.options).some((o) => o.value === prevProfile)) {
          subProfileSel.value = prevProfile;
        } else if (uniqueProfiles.length > 0) {
          subProfileSel.value = uniqueProfiles[0];
        }
      }
      const selectedProfileName = currentSubProfileName();
      const subProfileInput = document.getElementById("subProfile");
      if (subProfileInput) {
        subProfileInput.value = selectedProfileName;
      }
      if (credUserSel) {
        const prev = credUserSel.value || "";
        credUserSel.innerHTML = users.map((u) => '<option value="'+esc(u.ID)+'">'+esc(u.Name)+' ('+esc(u.ID)+')</option>').join("");
        if (prev && Array.from(credUserSel.options).some((o) => o.value === prev)) {
          credUserSel.value = prev;
        }
      }
      const credInboundSel = document.getElementById("credInbound");
      if (credInboundSel) {
        const prev = credInboundSel.value || "";
        credInboundSel.innerHTML = inbounds.map((i) => '<option value="'+esc(i.ID)+'">'+esc(i.ID)+' '+esc(i.Type)+' '+esc(i.Domain)+':'+esc(i.Port)+'</option>').join("");
        if (prev && Array.from(credInboundSel.options).some((o) => o.value === prev)) {
          credInboundSel.value = prev;
        }
      }
      document.getElementById("credsBody").innerHTML = creds.map((c) => (
        '<tr>' +
          '<td>'+esc(c.UserName)+'</td>' +
          '<td>'+ (c.ClientLabel ? esc(c.ClientLabel) : '<span class="muted">-</span>') +'</td>' +
          '<td>'+esc(c.InboundType)+' '+esc(c.InboundAddr)+'</td>' +
          '<td>'+ (c.ClientURI
            ? '<div class="row"><input class="mono" style="min-width:320px;width:100%" readonly value="'+esc(c.ClientURI)+'"><button class="btn secondary" data-copy="'+esc(c.ClientURI)+'">copy</button></div>'
            : '<span class="muted">unavailable</span>') +'</td>' +
          '<td style="white-space:nowrap">' +
            '<button class="btn secondary" style="margin-right:4px" data-cred-edit-id="'+esc(c.ID)+'" data-cred-edit-label="'+esc(c.ClientLabel||'')+'" data-cred-edit-version="'+esc(c.Version)+'">edit</button>' +
            '<button class="btn err" data-cred-id="'+esc(c.ID)+'" data-cred-user="'+esc(c.UserID)+'" data-cred-version="'+esc(c.Version)+'">detach</button>' +
          '</td>' +
        '</tr>'
      )).join("");
      document.querySelectorAll("[data-cred-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            await postForm(cfg.subsActionPath, {
              op: "delete_credential",
              credential_id: btn.getAttribute("data-cred-id"),
              user_id: btn.getAttribute("data-cred-user"),
              version: btn.getAttribute("data-cred-version"),
            });
          } catch (e) {
            showOp("error", String(e));
          }
        });
      });
      document.querySelectorAll("[data-cred-edit-id]").forEach((btn) => {
        btn.addEventListener("click", () => {
          document.getElementById("credEditID").value = btn.getAttribute("data-cred-edit-id");
          document.getElementById("credEditVersion").value = btn.getAttribute("data-cred-edit-version");
          document.getElementById("credEditLabel").value = btn.getAttribute("data-cred-edit-label") || "";
          document.getElementById("credEditModal").classList.remove("hidden");
          document.getElementById("credEditLabel").focus();
        });
      });

      const selectedSubUserID = (document.getElementById("subUser")?.value || "").trim();
      const filteredSubs = subDetails.filter((s) => !selectedSubUserID || String(s.UserID || "").trim() === selectedSubUserID);
      document.getElementById("subsList").innerHTML = filteredSubs.length === 0
        ? '<div class="muted" style="padding:12px">no subscriptions for this user</div>'
        : [
            '<div class="table-wrap">',
            '<table>',
            '<thead><tr><th>user</th><th>label</th><th>profile</th><th>status</th><th>configs</th><th>link</th><th>actions</th></tr></thead>',
            '<tbody>',
            filteredSubs.map((s) => {
              const link = String(s.URL || "").trim();
              const profileKey = esc(s.ProfileName || "default");
              const uid = esc(s.UserID || "");
              const lbl = String(s.Label || "").trim();
              return (
                '<tr>' +
                  '<td>'+esc(s.UserName || s.UserID || "")+'</td>' +
                  '<td>'+ (lbl ? esc(lbl) : '<span class="muted">-</span>') +'</td>' +
                  '<td>'+profileKey+'</td>' +
                  '<td>'+ (s.Enabled ? '<span class="badge-ok">enabled</span>' : '<span class="badge-err">disabled</span>') +'</td>' +
                  '<td>'+esc(Number(s.ConfigCount || 0))+'</td>' +
                  '<td style="max-width:220px;overflow:hidden;text-overflow:ellipsis">'+ (link ? '<a href="'+esc(link)+'" target="_blank" rel="noopener noreferrer" style="word-break:break-all">'+esc(link)+'</a>' : '<span class="muted">-</span>') +'</td>' +
                  '<td class="row" style="gap:4px;white-space:nowrap">' +
                    (link ? '<button class="btn secondary" data-copy="'+esc(link)+'">copy</button>' : '') +
                    '<button class="btn secondary" data-sub-edit-user="'+uid+'" data-sub-edit-profile="'+profileKey+'" data-sub-edit-label="'+esc(lbl)+'">edit</button>' +
                    (s.Enabled
                      ? '<button class="btn err" data-sub-toggle="0" data-sub-user="'+uid+'" data-sub-profile="'+profileKey+'">disable</button>'
                      : '<button class="btn" data-sub-toggle="1" data-sub-user="'+uid+'" data-sub-profile="'+profileKey+'">enable</button>') +
                    '<button class="btn err" data-sub-delete="'+esc(link || s.AccessToken || "")+'">delete</button>' +
                  '</td>' +
                '</tr>'
              );
            }).join(""),
            '</tbody>',
            '</table>',
            '</div>',
          ].join("");
      document.querySelectorAll("[data-copy]").forEach((btn) => {
        btn.addEventListener("click", () => {
          const txt = btn.getAttribute("data-copy") || "";
          navigator.clipboard?.writeText(txt);
        });
      });
      document.querySelectorAll("[data-sub-delete]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const link = (btn.getAttribute("data-sub-delete") || "").trim();
          if (!link) return;
          try {
            await postForm(cfg.subsActionPath, { op: "delete_link", subscription: link });
          } catch (e) {
            showOp("error", String(e));
          }
        });
      });
      document.querySelectorAll("[data-sub-toggle]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const enabled = btn.getAttribute("data-sub-toggle") || "0";
          const uid = (btn.getAttribute("data-sub-user") || "").trim();
          const profile = (btn.getAttribute("data-sub-profile") || "").trim();
          if (!uid) return;
          try {
            await postForm(cfg.subsActionPath, { op: "set_enabled", user_id: uid, profile, enabled });
          } catch (e) {
            showOp("error", String(e));
          }
        });
      });
      bindSubEditBtns();
    }

    // Subscription edit label — delegated via render()
    function bindSubEditBtns() {
      document.querySelectorAll("[data-sub-edit-user]").forEach((btn) => {
        btn.addEventListener("click", () => {
          document.getElementById("subEditUser").value = btn.getAttribute("data-sub-edit-user") || "";
          document.getElementById("subEditProfile").value = btn.getAttribute("data-sub-edit-profile") || "";
          document.getElementById("subEditLabel").value = btn.getAttribute("data-sub-edit-label") || "";
          document.getElementById("subEditModal").classList.remove("hidden");
          document.getElementById("subEditLabel").focus();
        });
      });
    }

    document.getElementById("createUserBtn").addEventListener("click", async () => {
      const field = document.getElementById("newUserName");
      const name = (field.value || "").trim();
      if (!name) return;
      try {
        await postForm(cfg.usersActionPath, { op: "create", name, enabled: "1" });
        field.value = "";
      } catch (e) {
        showOp("error", String(e));
      }
    });
    // Maintenance modal buttons — read node from modal data attributes.
    function getModalNode() {
      const modal = document.getElementById("nodeModal");
      return {
        id:      (modal.getAttribute("data-node-id")      || "").trim(),
        version: (modal.getAttribute("data-node-version") || "").trim(),
        name:    (modal.getAttribute("data-node-name")    || "").trim(),
        host:    (modal.getAttribute("data-node-host")    || "").trim(),
      };
    }
    document.getElementById("ndSetupSshKeyBtn").addEventListener("click", async () => {
      const n = getModalNode();
      if (!n.id) return;
      const label = n.host ? (n.name + " (" + n.host + ")") : n.name;
      try {
        const sshPassword = await promptNodePass("Setup SSH Key", "SSH password for " + label + " (leave empty if key auth already works):");
        closeNodeModal();
        await postForm(cfg.nodesActionPath, { op: "install_ssh_key", node_id: n.id, version: n.version, ssh_password: sshPassword });
      } catch (e) {
        if (String(e).includes("cancelled")) return;
        showOp("error", String(e));
      }
    });
    document.getElementById("ndBootstrapBtn").addEventListener("click", async () => {
      const n = getModalNode();
      if (!n.id) return;
      const label = n.host ? (n.name + " (" + n.host + ")") : n.name;
      try {
        const sshPassword = await promptNodePass("Bootstrap Node", "SSH password for " + label + " (leave empty if key auth works):");
        closeNodeModal();
        await postForm(cfg.nodesActionPath, { op: "bootstrap", node_id: n.id, version: n.version, ssh_password: sshPassword });
      } catch (e) {
        if (String(e).includes("cancelled")) return;
        showOp("error", String(e));
      }
    });
    document.getElementById("ndApplyHardeningBtn").addEventListener("click", async () => {
      const n = getModalNode();
      if (!n.id) return;
      const disableIPv6 = document.getElementById("ndDisableIPv6").checked ? "1" : "0";
      const blockPing   = document.getElementById("ndBlockPing").checked   ? "1" : "0";
      if (disableIPv6 === "0" && blockPing === "0") {
        showOp("error", "No hardening options enabled — enable Disable IPv6 or Block Ping in node settings first");
        return;
      }
      const label = n.host ? (n.name + " (" + n.host + ")") : n.name;
      try {
        const sshPassword = await promptNodePass("Apply Hardening", "SSH password for " + label + " (leave empty if key auth works):");
        closeNodeModal();
        await postForm(cfg.nodesActionPath, { op: "apply_hardening", node_id: n.id, version: n.version, ssh_password: sshPassword, disable_ipv6: disableIPv6, block_ping: blockPing });
      } catch (e) {
        if (String(e).includes("cancelled")) return;
        showOp("error", String(e));
      }
    });
    document.getElementById("ndFetchVersionBtn").addEventListener("click", async () => {
      const n = getModalNode();
      if (!n.id) return;
      const btn = document.getElementById("ndFetchVersionBtn");
      const vEl = document.getElementById("ndRemoteVersion");
      btn.disabled = true;
      vEl.textContent = "checking…";
      try {
        await postForm(cfg.nodesActionPath, { op: "fetch_version", node_id: n.id });
      } catch (e) {
        vEl.textContent = "error";
        showOp("error", String(e));
      } finally {
        btn.disabled = false;
      }
    });
    document.getElementById("ndUpdateProxyctlBtn").addEventListener("click", async () => {
      const n = getModalNode();
      if (!n.id) return;
      const label = n.host ? (n.name + " (" + n.host + ")") : n.name;
      if (!confirm("Update proxyctl on " + label + "?\nThis will run proxyctl update --force and restart services.")) return;
      try {
        const sshPassword = await promptNodePass("Update proxyctl", "SSH password for " + label + " (leave empty if key auth works):");
        closeNodeModal();
        await postForm(cfg.nodesActionPath, { op: "update_proxyctl", node_id: n.id, ssh_password: sshPassword });
      } catch (e) {
        if (String(e).includes("cancelled")) return;
        showOp("error", String(e));
      }
    });
    document.getElementById("ndSyncPrimaryBtn").addEventListener("click", async () => {
      const n = getModalNode();
      if (!n.id) return;
      closeNodeModal();
      await postForm(cfg.nodesActionPath, { op: "sync", node_id: n.id, version: n.version });
    });
    document.getElementById("openAddNodeBtn").addEventListener("click", () => openNodeModal(null));
    document.getElementById("closeNodeModalBtn").addEventListener("click", closeNodeModal);
    document.getElementById("cancelNodeModalBtn").addEventListener("click", closeNodeModal);
    document.getElementById("nodeModal").addEventListener("click", (e) => {
      if (e.target === document.getElementById("nodeModal")) closeNodeModal();
    });
    document.getElementById("saveNodeBtn").addEventListener("click", async () => {
      const modal = document.getElementById("nodeModal");
      const nodeID = (modal.getAttribute("data-node-id") || "").trim();
      const version = (modal.getAttribute("data-node-version") || "").trim();
      const name = (document.getElementById("ndName").value || "").trim();
      const host = (document.getElementById("ndHost").value || "").trim();
      const role = (document.getElementById("ndRole").value || "node").trim();
      const sshUser = (document.getElementById("ndSSHUser").value || "").trim();
      const sshPort = (document.getElementById("ndSSHPort").value || "22").trim();
      const disableIPv6 = document.getElementById("ndDisableIPv6").checked ? "1" : "0";
      const blockPing = document.getElementById("ndBlockPing").checked ? "1" : "0";
      const password = (document.getElementById("ndPassword").value || "").trim();
      if (!name || !host) { showOp("error", "name and host are required"); return; }
      closeNodeModal();
      try {
        if (nodeID) {
          await postForm(cfg.nodesActionPath, { op: "update", node_id: nodeID, version, name, host, role, ssh_user: sshUser, ssh_port: sshPort, disable_ipv6: disableIPv6, block_ping: blockPing });
        } else {
          await postForm(cfg.nodesActionPath, { op: "create", name, host, role, ssh_user: sshUser, ssh_port: sshPort, disable_ipv6: disableIPv6, block_ping: blockPing, ssh_password: password });
        }
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.getElementById("createInboundBtn").addEventListener("click", async () => {
      console.log('[createInboundBtn] click - editingInboundID:', editingInboundID);
      updateInboundCreateFieldVisibility(false);
      const type = (document.getElementById("inType").value || "").trim();
      const transport = (document.getElementById("inTransport").value || "").trim();
      const engine = (document.getElementById("inEngine").value || "").trim();
      const nodeID = (document.getElementById("inNode").value || "").trim();
      const domain = (document.getElementById("inDomain").value || "").trim();
      let port = (document.getElementById("inPort").value || "").trim();
      const path = (document.getElementById("inPath").value || "").trim();
      const target = (document.getElementById("inTarget").value || "").trim();
      const linkTargetSni = !!document.getElementById("inLinkTargetSni")?.checked;
      const securityMode = (document.getElementById("inSecurityMode").value || "none").trim().toLowerCase();
      let sni = (document.getElementById("inSni").value || "").trim();
      if (!sni && linkTargetSni && target) {
        sni = target.includes(":") ? target.split(":")[0] : target;
      }
      const selfSteal = !!document.getElementById("inSelfSteal")?.checked;
      const realityServer = (document.getElementById("inRealityServer").value || "").trim();
      const realityServerPort = (document.getElementById("inRealityServerPort").value || "").trim();
      const realityFingerprint = (document.getElementById("inRealityFingerprint").value || "").trim();
      const realityPublicKey = (document.getElementById("inRealityPublicKey").value || "").trim();
      const realityPrivateKey = (document.getElementById("inRealityPrivateKey").value || "").trim();
      const realityShortID = (document.getElementById("inRealityShortID").value || "").trim();
      const realitySpiderX = (document.getElementById("inRealitySpiderX").value || "").trim();
      const vlessFlow = (document.getElementById("inVlessFlow").value || "").trim();
      const sniffingEnabled = !!document.getElementById("inSniffingEnabled")?.checked;
      const sniffingHTTP = !!document.getElementById("inSniffingHTTP")?.checked;
      const sniffingTLS = !!document.getElementById("inSniffingTLS")?.checked;
      const sniffingQUIC = !!document.getElementById("inSniffingQUIC")?.checked;
      const sniffingFakeDNS = !!document.getElementById("inSniffingFakeDNS")?.checked;
      if (!port) {
        const suggested = getSuggestedInboundPort(type, transport, nodeID);
        if (suggested > 0) {
          port = String(suggested);
          document.getElementById("inPort").value = port;
        }
      }
      console.log('[createInboundBtn] validation - type:', type, 'transport:', transport, 'nodeID:', nodeID, 'domain:', domain, 'port:', port);
      if (!type || !transport || !nodeID || !domain || !port) { console.warn('[createInboundBtn] validation FAILED, returning'); return; }
      try {
        const isEdit = !!editingInboundID;
        const payload = {
          op: isEdit ? "update" : "create",
          type,
          transport,
          engine,
          node_id: nodeID,
          domain,
          port,
          path,
          sni,
          security_mode: securityMode,
          self_steal: selfSteal ? "1" : "0",
          reality_server: realityServer,
          reality_server_port: realityServerPort,
          reality_fingerprint: realityFingerprint,
          reality_public_key: realityPublicKey,
          reality_private_key: realityPrivateKey,
          reality_short_id: realityShortID,
          reality_spider_x: realitySpiderX,
          vless_flow: vlessFlow,
          sniffing_enabled: sniffingEnabled ? "1" : "0",
          sniffing_http: sniffingHTTP ? "1" : "0",
          sniffing_tls: sniffingTLS ? "1" : "0",
          sniffing_quic: sniffingQUIC ? "1" : "0",
          sniffing_fakedns: sniffingFakeDNS ? "1" : "0",
          enabled: editingInboundID ? (editingInboundEnabled ? "1" : "0") : "1",
        };
        if (isEdit) {
          payload.inbound_id = editingInboundID;
          payload.version = editingInboundVersion;
        }
        await postForm(cfg.inboundsActionPath, payload);
        resetInboundCreateDefaults();
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.getElementById("cancelInboundEditBtn").addEventListener("click", () => {
      resetInboundCreateDefaults();
    });
    document.getElementById("attachCredBtn").addEventListener("click", async () => {
      const userID = (document.getElementById("credUser").value || "").trim();
      const inboundID = (document.getElementById("credInbound").value || "").trim();
      const label = (document.getElementById("credLabel").value || "").trim();
      if (!userID || !inboundID) return;
      try {
        await postForm(cfg.subsActionPath, {
          op: "attach_credential",
          user_id: userID,
          inbound_id: inboundID,
          label,
        });
      } catch (e) {
        showOp("error", String(e));
      }
    });
    // Subscription modal open/close
    function openSubModal() {
      const modal = document.getElementById("subModal");
      if (modal) modal.classList.remove("hidden");
      const inbounds = (snapshot && Array.isArray(snapshot.Inbounds)) ? snapshot.Inbounds : [];
      renderSubInboundPick(inbounds);
    }
    function closeSubModal() {
      const modal = document.getElementById("subModal");
      if (modal) modal.classList.add("hidden");
    }
    document.getElementById("openSubModalBtn").addEventListener("click", openSubModal);
    document.getElementById("closeSubModalBtn").addEventListener("click", closeSubModal);
    document.getElementById("closeSubModalBtn2").addEventListener("click", closeSubModal);
    document.getElementById("subModal").addEventListener("click", (e) => {
      if (e.target === document.getElementById("subModal")) closeSubModal();
    });
    document.getElementById("genSubBtn").addEventListener("click", async () => {
      const userID = (document.getElementById("subUser").value || "").trim();
      if (!userID) return;
      try {
        await postForm(cfg.subsActionPath, { op: "generate_user", user_id: userID });
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.getElementById("genSelectedSubBtn").addEventListener("click", async () => {
      const userID = (document.getElementById("subUser").value || "").trim();
      const profile = (document.getElementById("subProfile").value || "").trim();
      if (!userID) return;
      const ids = selectedSubInboundIDs();
      if (ids.length === 0) {
        showOp("error", "select at least one inbound");
        return;
      }
      try {
        await postForm(cfg.subsActionPath, {
          op: "generate_user_selected",
          user_id: userID,
          profile,
          inbounds: ids.join(","),
        });
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.getElementById("detachSelectedCredsBtn").addEventListener("click", async () => {
      const userID = (document.getElementById("subUser").value || "").trim();
      if (!userID) return;
      const ids = selectedSubInboundIDs();
      if (ids.length === 0) {
        showOp("error", "select at least one inbound");
        return;
      }
      try {
        await postForm(cfg.subsActionPath, {
          op: "delete_selected_credentials",
          user_id: userID,
          inbounds: ids.join(","),
        });
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.getElementById("refreshSubBtn").addEventListener("click", async () => {
      try {
        await postForm(cfg.subsActionPath, { op: "refresh_all" });
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.getElementById("askTrafficBtn").addEventListener("click", async () => {
      try {
        await getSnapshot();
        showOp("ok", "traffic and runtime stats refreshed");
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.getElementById("liveMode").addEventListener("change", () => {
      localStorage.setItem(STORAGE_KEY_LIVE_ENABLED, document.getElementById("liveMode").checked ? "1" : "0");
      if (document.getElementById("liveMode").checked) {
        pollSnapshotSilently();
      }
      startLivePolling();
    });
    document.getElementById("liveInterval").addEventListener("change", () => {
      localStorage.setItem(STORAGE_KEY_LIVE_INTERVAL, String(document.getElementById("liveInterval").value || "5000"));
      startLivePolling();
    });
    document.getElementById("saveAcmeBtn").addEventListener("click", async () => {
      const email = (document.getElementById("acmeEmail").value || "").trim();
      try {
        await postForm(cfg.settingsActionPath, { op: "set_acme_email", email });
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.getElementById("saveTrafficCollectionBtn").addEventListener("click", async () => {
      const enabled = document.getElementById("trafficCollectionChk").checked ? "1" : "0";
      try {
        await postForm(cfg.settingsActionPath, { op: "set_traffic_collection", enabled });
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden && document.getElementById("liveMode")?.checked) {
        pollSnapshotSilently();
      }
    });
    document.getElementById("inboundsBody").addEventListener("click", (e) => {
      const btn = e.target.closest("[data-inbound-edit-id]");
      if (!btn) return;
      const inboundID = (btn.getAttribute("data-inbound-edit-id") || "").trim();
      console.log('[inboundsBody] edit click - inboundID:', inboundID);
      if (!inboundID) return;
      const currentInbounds = Array.isArray(snapshot?.Inbounds) ? snapshot.Inbounds : [];
      const selected = currentInbounds.find((item) => String(item.ID || "").trim() === inboundID);
      console.log('[inboundsBody] found inbound:', selected ? JSON.stringify({ID: selected.ID, Domain: selected.Domain, Port: selected.Port, NodeID: selected.NodeID}) : 'NOT FOUND');
      if (!selected) return;
      beginInboundEdit(selected);
    });
    document.getElementById("inType").addEventListener("change", () => updateInboundCreateFieldVisibility(true));
    document.getElementById("inTransport").addEventListener("change", () => updateInboundCreateFieldVisibility(true));
    document.getElementById("inTarget").addEventListener("input", () => {
      updateInboundTargetToSni(false);
      updateDestServerFromTarget();
    });
    document.getElementById("inTargetRegen").addEventListener("click", () => {
      applyRealityPreset(pickRandomRealityPreset());
    });
    document.getElementById("inRealityGenKeys").addEventListener("click", async () => {
      const btn = document.getElementById("inRealityGenKeys");
      btn.disabled = true;
      btn.textContent = "Generating…";
      try {
        const { publicKey, privateKey } = await generateRealityKeyPair();
        document.getElementById("inRealityPublicKey").value = publicKey;
        document.getElementById("inRealityPrivateKey").value = privateKey;
      } catch (e) {
        showOp("error", "Key generation failed: " + String(e));
      } finally {
        btn.disabled = false;
        btn.textContent = "⚙ Generate Keys";
      }
    });
    document.getElementById("inRealityClearKeys").addEventListener("click", () => {
      document.getElementById("inRealityPublicKey").value = "";
      document.getElementById("inRealityPrivateKey").value = "";
    });
    document.getElementById("inSelfSteal").addEventListener("change", () => {
      updateSelfStealVisibility();
    });
    document.getElementById("inSniffingEnabled").addEventListener("change", () => {
      const on = !!document.getElementById("inSniffingEnabled").checked;
      document.getElementById("mSniffingRow")?.classList.toggle("hidden", !on);
    });
    document.getElementById("inLinkTargetSni").addEventListener("change", () => {
      if (document.getElementById("inLinkTargetSni").checked) {
        inboundSniManualOverride = false;
        updateInboundTargetToSni(true);
      }
    });
    document.getElementById("inSni").addEventListener("input", () => {
      const linked = !!document.getElementById("inLinkTargetSni")?.checked;
      const target = (document.getElementById("inTarget")?.value || "").trim();
      const host = target.includes(":") ? target.split(":")[0] : target;
      const sni = (document.getElementById("inSni")?.value || "").trim();
      inboundSniManualOverride = linked && sni !== host;
    });
    document.getElementById("inNode").addEventListener("change", () => {
      updateInboundDomainFromNode(true);
      // re-suggest port only when creating (not editing)
      if (!editingInboundID) {
        updateInboundPortSuggestion(true);
      }
    });
    document.getElementById("openCreateInboundBtn").addEventListener("click", () => {
      console.log('[openCreateInboundBtn] opening create modal');
      resetInboundCreateDefaults();
      const modalTitle = document.getElementById("inboundModalTitle");
      if (modalTitle) modalTitle.textContent = "Create Inbound";
      const createBtn = document.getElementById("createInboundBtn");
      if (createBtn) createBtn.textContent = "Create";
      openInboundModal();
      updateInboundCreateFieldVisibility(true);
    });
    document.getElementById("closeInboundModalBtn").addEventListener("click", () => {
      console.log('[closeInboundModalBtn] closing modal');
      resetInboundCreateDefaults();
    });
    document.getElementById("inboundModal").addEventListener("click", (e) => {
      if (e.target === document.getElementById("inboundModal")) resetInboundCreateDefaults();
    });
    document.getElementById("mSecTabs").addEventListener("click", (e) => {
      const btn = e.target.closest(".sec-tab");
      if (!btn) return;
      const sec = btn.getAttribute("data-sec") || "none";
      setModalSecTab(sec);
      updateInboundCreateFieldVisibility(false);
    });
    document.getElementById("subUser").addEventListener("change", () => {
      render();
    });
    document.getElementById("subProfileSel").addEventListener("change", () => {
      const selected = (document.getElementById("subProfileSel").value || "").trim();
      const input = document.getElementById("subProfile");
      if (input) input.value = selected;
      const inbounds = (snapshot && Array.isArray(snapshot.Inbounds)) ? snapshot.Inbounds : [];
      renderSubInboundPick(inbounds);
    });
    document.getElementById("subProfile").addEventListener("change", () => {
      const inbounds = (snapshot && Array.isArray(snapshot.Inbounds)) ? snapshot.Inbounds : [];
      renderSubInboundPick(inbounds);
    });
    document.querySelectorAll("[data-tab]").forEach((btn) => {
      btn.addEventListener("click", () => setTab(btn.getAttribute("data-tab") || "dashboard"));
    });
    lastOpSeenKey = loadLastOpSeenKey();
    setTab("dashboard");
    restoreLivePrefs();
    startLivePolling();

    getSnapshot().catch((e) => showOp("error", String(e)));
  </script>
</body>
</html>`))

func newPanelCmd(configPath, dbPath *string) *cobra.Command {
	cmd := newGroupCmd(
		"panel",
		"Panel web UI commands",
		"Runs local web panel endpoints for visual control-plane workflows.",
	)
	cmd.AddCommand(newPanelServeCmd(configPath, dbPath))
	return cmd
}

func newPanelServeCmd(configPath, dbPath *string) *cobra.Command {
	listenAddr := ""
	basePath := ""
	requireAuth := true

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve visual panel",
		Long:  "Starts phase-1 visual panel with safe write operations and explicit runtime actions.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadAppConfig(*configPath)
			if err != nil {
				return err
			}
			resolvedDB := resolveDBPath(cmd, cfg, *dbPath)
			configPathValue := strings.TrimSpace(*configPath)
			dbPathValue := strings.TrimSpace(*dbPath)
			panelGlobalDBPath = resolvedDB

			panelInfo, err := readPanelAccessInfo(panelCredentialsPathFromConfig(*configPath))
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("read panel credentials: %w", err)
			}

			if strings.TrimSpace(listenAddr) == "" {
				if p := strings.TrimSpace(panelInfo.Port); isValidPanelPortString(p) {
					listenAddr = "127.0.0.1:" + p
				} else {
					listenAddr = "127.0.0.1:20443"
				}
			}
			if strings.TrimSpace(basePath) == "" {
				basePath = strings.TrimSpace(panelInfo.Path)
			}
			basePath = normalizePanelBasePath(basePath)

			if requireAuth {
				if strings.TrimSpace(panelInfo.Login) == "" || strings.TrimSpace(panelInfo.Password) == "" {
					return fmt.Errorf("panel auth is enabled but login/password are missing in %s", panelCredentialsPathFromConfig(*configPath))
				}
			}

			panelMux := http.NewServeMux()
			appPath := panelJoin(basePath, "")
			appAliasPath := panelJoin(basePath, "app")
			legacyDashboardPath := panelJoin(basePath, "legacy")
			usersPath := panelJoin(basePath, "legacy/users")
			inboundsPath := panelJoin(basePath, "legacy/inbounds")
			subsPath := panelJoin(basePath, "legacy/subscriptions")
			apiSnapshotPath := panelJoin(basePath, "api/snapshot")
			nodeJobsPath := panelJoin(basePath, "api/node-jobs")
			dashboardActionPath := panelJoin(basePath, "actions")
			settingsActionPath := panelJoin(basePath, "settings/action")
			usersActionPath := panelJoin(basePath, "users/action")
			nodesActionPath := panelJoin(basePath, "nodes/action")
			inboundsActionPath := panelJoin(basePath, "inbounds/action")
			subsActionPath := panelJoin(basePath, "subscriptions/action")
			logoutPath := panelJoin(basePath, "logout")
			ops := &panelOperationFeed{}

			handlePage := func(tab, navBasePath string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet {
						http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
						return
					}
					snapshot, snapErr := buildPanelSnapshot(r.Context(), resolvedDB, cfg)
					if snapErr != nil {
						http.Error(w, "failed to build panel data: "+snapErr.Error(), http.StatusInternalServerError)
						return
					}
					data := panelPageData{
						Title:               "proxyctl panel",
						ActiveTab:           tab,
						BasePath:            navBasePath,
						LogoutPath:          logoutPath,
						ListenAddr:          listenAddr,
						GeneratedAt:         time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
						Counts:              snapshot.counts,
						Units:               snapshot.units,
						Users:               snapshot.users,
						Nodes:               snapshot.nodes,
						Inbounds:            snapshot.inbounds,
						Credentials:         snapshot.credentials,
						Subscriptions:       snapshot.subscriptionLinks,
						SubscriptionDetails: snapshot.subscriptionViews,
						DashboardActionPath: dashboardActionPath,
						UsersActionPath:     usersActionPath,
						NodesActionPath:     nodesActionPath,
						InboundsActionPath:  inboundsActionPath,
						SubsActionPath:      subsActionPath,
						AppPath:             appPath,
						SubscriptionState:   snapshot.subscriptionState,
						SuggestedPorts:      snapshot.suggestedPorts,
						SNIPresets:          snapshot.sniPresets,
						DefaultSelfSteal:    cfg.Public.DefaultSelfSteal,
						Dashboard:           snapshot.dashboard,
						ContactEmail:        strings.TrimSpace(cfg.Public.ContactEmail),
						XrayUnit:            cfg.Runtime.XrayUnit,
						SingBoxUnit:         cfg.Runtime.SingBoxUnit,
						CaddyUnit:           cfg.Runtime.CaddyUnit,
					}
					data.OperationStatus, data.OperationMessage, data.OperationAt = ops.snapshot()
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					if execErr := panelPageTmpl.Execute(w, data); execErr != nil {
						http.Error(w, "template render failed", http.StatusInternalServerError)
						return
					}
				}
			}

			panelMux.HandleFunc(legacyDashboardPath, handlePage("dashboard", legacyDashboardPath))
			panelMux.HandleFunc(usersPath, handlePage("users", legacyDashboardPath))
			panelMux.HandleFunc(inboundsPath, handlePage("inbounds", legacyDashboardPath))
			panelMux.HandleFunc(subsPath, handlePage("subscriptions", legacyDashboardPath))
			panelMux.HandleFunc(appPath, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_ = panelAppTmpl.Execute(w, panelAppData{
					BasePath:                 basePath,
					LegacyPath:               legacyDashboardPath,
					LogoutPath:               logoutPath,
					SnapshotPath:             apiSnapshotPath,
					DashboardActionPath:      dashboardActionPath,
					SettingsActionPath:       settingsActionPath,
					UsersActionPath:          usersActionPath,
					NodesActionPath:          nodesActionPath,
					InboundsActionPath:       inboundsActionPath,
					SubsActionPath:           subsActionPath,
					ContactEmail:             strings.TrimSpace(cfg.Public.ContactEmail),
					TrafficCollectionEnabled: panelTrafficCollection.Load(),
				})
			})
			if appAliasPath != appPath {
				panelMux.HandleFunc(appAliasPath, func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, appPath, http.StatusMovedPermanently)
				})
			}
			panelMux.HandleFunc(apiSnapshotPath, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				snapshot, snapErr := buildPanelSnapshot(r.Context(), resolvedDB, cfg)
				if snapErr != nil {
					http.Error(w, "failed to build panel data: "+snapErr.Error(), http.StatusInternalServerError)
					return
				}
				data := panelPageData{
					Title:               "proxyctl panel",
					ActiveTab:           "dashboard",
					BasePath:            basePath,
					LogoutPath:          logoutPath,
					ListenAddr:          listenAddr,
					GeneratedAt:         time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
					Counts:              snapshot.counts,
					Units:               snapshot.units,
					Users:               snapshot.users,
					Nodes:               snapshot.nodes,
					Inbounds:            snapshot.inbounds,
					Credentials:         snapshot.credentials,
					Subscriptions:       snapshot.subscriptionLinks,
					SubscriptionDetails: snapshot.subscriptionViews,
					DashboardActionPath: dashboardActionPath,
					SettingsActionPath:  settingsActionPath,
					UsersActionPath:     usersActionPath,
					NodesActionPath:     nodesActionPath,
					InboundsActionPath:  inboundsActionPath,
					SubsActionPath:      subsActionPath,
					AppPath:             appPath,
					SubscriptionState:   snapshot.subscriptionState,
					SuggestedPorts:      snapshot.suggestedPorts,
					SNIPresets:          snapshot.sniPresets,
					SSHKeyWarning:       snapshot.sshKeyWarning,
					DefaultSelfSteal:    cfg.Public.DefaultSelfSteal,
					Dashboard:           snapshot.dashboard,
					ContactEmail:        strings.TrimSpace(cfg.Public.ContactEmail),
					XrayUnit:            cfg.Runtime.XrayUnit,
					SingBoxUnit:         cfg.Runtime.SingBoxUnit,
					CaddyUnit:           cfg.Runtime.CaddyUnit,
				}
				data.OperationStatus, data.OperationMessage, data.OperationAt = ops.snapshot()
				panelWriteJSON(w, http.StatusOK, data)
			})
			panelMux.HandleFunc(dashboardActionPath, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				if err := r.ParseForm(); err != nil {
					ops.set("error", "invalid action request")
					http.Redirect(w, r, legacyDashboardPath, http.StatusSeeOther)
					return
				}
				action := strings.TrimSpace(r.FormValue("action"))
				switch action {
				case "render":
					out, runErr := panelExecuteCommand(r.Context(), newRenderCmd(&configPathValue, &dbPathValue), nil)
					if runErr != nil {
						ops.set("error", "render failed: "+panelErrWithOutput(runErr, out))
					} else {
						ops.set("ok", "render completed: "+panelSummarizeOutput(out))
					}
				case "validate":
					out, runErr := panelExecuteCommand(r.Context(), newValidateCmd(&configPathValue, &dbPathValue), nil)
					if runErr != nil {
						ops.set("error", "validate failed: "+panelErrWithOutput(runErr, out))
					} else {
						ops.set("ok", "validate completed: "+panelSummarizeOutput(out))
					}
				case "apply":
					out, runErr := panelExecuteCommand(r.Context(), newApplyCmd(&configPathValue, &dbPathValue), nil)
					if runErr != nil {
						ops.set("error", "apply failed: "+panelErrWithOutput(runErr, out))
					} else {
						ops.set("ok", "apply completed: "+panelSummarizeOutput(out))
					}
				case "reset_total_traffic":
					rx, tx, err := panelNetTotals()
					if err != nil {
						ops.set("error", "reset traffic: "+err.Error())
					} else {
						dashMu.Lock()
						trafResetRX = rx
						trafResetTX = tx
						dashMu.Unlock()
						ops.set("ok", "traffic counters reset")
					}
				case "reset_user_traffic":
					userID := strings.TrimSpace(r.FormValue("user_id"))
					if err := panelResetUserTraffic(cfg.Paths.RuntimeDir, userID); err != nil {
						ops.set("error", "reset user traffic: "+err.Error())
					} else {
						if userID == "" {
							ops.set("ok", "all user traffic reset")
						} else {
							ops.set("ok", "user traffic reset: "+userID)
						}
					}
				case "restart_unit", "stop_unit", "start_unit":
					unit := strings.TrimSpace(r.FormValue("unit"))
					allowed := map[string]bool{
						cfg.Runtime.XrayUnit:    true,
						cfg.Runtime.SingBoxUnit: true,
						cfg.Runtime.CaddyUnit:   true,
						cfg.Runtime.NginxUnit:   true,
					}
					if unit == "" || !allowed[unit] {
						ops.set("error", "unit not allowed: "+unit)
						break
					}
					var subcmd string
					switch action {
					case "restart_unit":
						subcmd = "restart"
					case "stop_unit":
						subcmd = "stop"
					case "start_unit":
						subcmd = "start"
					}
					out, runErr := panelRunSystemctl(r.Context(), subcmd, unit)
					if runErr != nil {
						ops.set("error", subcmd+" "+unit+" failed: "+panelErrWithOutput(runErr, out))
					} else {
						ops.set("ok", subcmd+" "+unit+" ok")
					}
				case "check_update":
					tag, dlURL, fetchErr := panelFetchLatestVersion(r.Context())
					if fetchErr != nil {
						panelWriteJSON(w, http.StatusOK, map[string]string{"error": fetchErr.Error()})
						return
					}
					panelWriteJSON(w, http.StatusOK, map[string]string{
						"latest_version":  tag,
						"download_url":    dlURL,
						"current_version": strings.TrimSpace(Version),
					})
					return
				case "update_proxyctl":
					dlURL := strings.TrimSpace(r.FormValue("download_url"))
					if dlURL == "" {
						_, dlURL, _ = panelFetchLatestVersion(r.Context())
					}
					if dlURL == "" {
						panelWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "could not resolve download URL"})
						return
					}
					if updErr := panelSelfUpdate(dlURL); updErr != nil {
						panelWriteJSON(w, http.StatusOK, map[string]string{"error": updErr.Error()})
						return
					}
					panelUpdMu.Lock()
					panelUpdAt = time.Time{}
					panelUpdMu.Unlock()
					panelWriteJSON(w, http.StatusOK, map[string]string{
						"ok":      "true",
						"message": "binary updated — restart proxyctl panel to apply",
					})
					return
				default:
					ops.set("error", "unknown dashboard action")
				}
				panelRespondAction(w, r, legacyDashboardPath, ops)
			})
			panelMux.HandleFunc(settingsActionPath, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				if err := panelHandleSettingsAction(r, &configPathValue, ops); err != nil {
					ops.set("error", err.Error())
				}
				panelRespondAction(w, r, appPath, ops)
			})
			panelMux.HandleFunc(usersActionPath, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				if err := panelHandleUserAction(r.Context(), resolvedDB, r, ops); err != nil {
					ops.set("error", err.Error())
				}
				panelRespondAction(w, r, usersPath, ops)
			})
			panelMux.HandleFunc(nodesActionPath, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				jobID, nodeID, nodeErr := panelHandleNodeAction(r.Context(), resolvedDB, r, &configPathValue, &dbPathValue, ops)
				if nodeErr != nil {
					ops.set("error", nodeErr.Error())
				}
				if panelWantsJSON(r) {
					status, message, at := ops.snapshot()
					resp := map[string]interface{}{
						"status":  status,
						"message": message,
						"at":      at,
					}
					if jobID != "" {
						resp["job_id"] = jobID
					}
					if nodeID != "" {
						resp["node_id"] = nodeID
					}
					panelWriteJSON(w, http.StatusOK, resp)
					return
				}
				http.Redirect(w, r, legacyDashboardPath, http.StatusSeeOther)
			})
			panelMux.HandleFunc(nodeJobsPath, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				id := strings.TrimSpace(r.URL.Query().Get("id"))
				if id == "" {
					http.Error(w, "id required", http.StatusBadRequest)
					return
				}
				v, ok := panelNodeJobs.Load(id)
				if !ok {
					panelWriteJSON(w, http.StatusNotFound, map[string]interface{}{"error": "job not found"})
					return
				}
				j := v.(*nodeJob)
				done, jok, msg := j.jobStatus()
				panelWriteJSON(w, http.StatusOK, map[string]interface{}{
					"id":     j.id,
					"nodeID": j.nodeID,
					"op":     j.op,
					"done":   done,
					"ok":     jok,
					"msg":    msg,
				})
			})
			panelMux.HandleFunc(inboundsActionPath, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				jobID, inboundNodeID, inboundErr := panelHandleInboundAction(r.Context(), resolvedDB, r, &configPathValue, &dbPathValue, ops)
				if inboundErr != nil {
					ops.set("error", inboundErr.Error())
				}
				if panelWantsJSON(r) {
					status, message, at := ops.snapshot()
					resp := map[string]interface{}{
						"status":  status,
						"message": message,
						"at":      at,
					}
					if jobID != "" {
						resp["job_id"] = jobID
					}
					if inboundNodeID != "" {
						resp["node_id"] = inboundNodeID
					}
					panelWriteJSON(w, http.StatusOK, resp)
					return
				}
				panelRespondAction(w, r, inboundsPath, ops)
			})
			panelMux.HandleFunc(subsActionPath, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				if err := r.ParseForm(); err != nil {
					ops.set("error", "invalid subscription action request")
					http.Redirect(w, r, subsPath, http.StatusSeeOther)
					return
				}
				switch strings.TrimSpace(r.FormValue("op")) {
				case "generate_user":
					userID := strings.TrimSpace(r.FormValue("user_id"))
					if userID == "" {
						ops.set("error", "user id is required")
						break
					}
					autoCreated := 0
					createdCount, ensureErr := panelEnsureCredentialsForUserInbounds(r.Context(), resolvedDB, userID, nil)
					if ensureErr != nil {
						ops.set("error", fmt.Sprintf("subscription generate failed: ensure credentials: %v", ensureErr))
						break
					}
					autoCreated = createdCount
					if autoCreated > 0 {
						synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(r.Context(), resolvedDB, strings.TrimSpace(configPathValue), nil)
						if syncErr != nil {
							ops.set("error", fmt.Sprintf("subscription generate failed: credentials auto-created=%d, but node sync failed: %v", autoCreated, syncErr))
							break
						}
						ops.set("ok", fmt.Sprintf("credentials auto-created: %d | node sync: synced=%d cleaned=%d", autoCreated, synced, cleaned))
					}
					out, runErr := panelExecuteCommand(r.Context(), newSubscriptionGenerateCmd(&configPathValue, &dbPathValue), []string{userID})
					if runErr != nil {
						ops.set("error", "subscription generate failed: "+panelErrWithOutput(runErr, out))
					} else {
						if autoCreated > 0 {
							ops.set("ok", fmt.Sprintf("subscription generated: %s | credentials auto-created: %d", panelSummarizeOutput(out), autoCreated))
						} else {
							ops.set("ok", "subscription generated: "+panelSummarizeOutput(out))
						}
					}
				case "generate_user_selected":
					userID := strings.TrimSpace(r.FormValue("user_id"))
					inboundsCSV := strings.TrimSpace(r.FormValue("inbounds"))
					profile := strings.TrimSpace(r.FormValue("profile"))
					if userID == "" {
						ops.set("error", "user id is required")
						break
					}
					if inboundsCSV == "" {
						ops.set("error", "select at least one inbound")
						break
					}
					if profile == "" {
						profile = "panel"
					}
					autoCreated := 0
					createdCount, ensureErr := panelEnsureCredentialsForUserInbounds(r.Context(), resolvedDB, userID, parseCSV(inboundsCSV))
					if ensureErr != nil {
						ops.set("error", fmt.Sprintf("subscription generate failed: ensure credentials: %v", ensureErr))
						break
					}
					autoCreated = createdCount
					if autoCreated > 0 {
						synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(r.Context(), resolvedDB, strings.TrimSpace(configPathValue), nil)
						if syncErr != nil {
							ops.set("error", fmt.Sprintf("subscription generate failed: credentials auto-created=%d, but node sync failed: %v", autoCreated, syncErr))
							break
						}
						ops.set("ok", fmt.Sprintf("credentials auto-created: %d | node sync: synced=%d cleaned=%d", autoCreated, synced, cleaned))
					}
					out, runErr := panelExecuteCommandWithSetup(r.Context(), newSubscriptionGenerateCmd(&configPathValue, &dbPathValue), []string{userID}, func(cmd *cobra.Command) error {
						if err := cmd.Flags().Set("profile", profile); err != nil {
							return err
						}
						return cmd.Flags().Set("inbounds", inboundsCSV)
					})
					if runErr != nil {
						ops.set("error", "subscription generate failed: "+panelErrWithOutput(runErr, out))
					} else {
						if autoCreated > 0 {
							ops.set("ok", fmt.Sprintf("subscription generated: %s | credentials auto-created: %d", panelSummarizeOutput(out), autoCreated))
						} else {
							ops.set("ok", "subscription generated: "+panelSummarizeOutput(out))
						}
					}
				case "set_enabled":
					userID := strings.TrimSpace(r.FormValue("user_id"))
					if userID == "" {
						ops.set("error", "user id is required")
						break
					}
					profile := wizardNormalizeProfileName(r.FormValue("profile"))
					if profile == "" {
						profile = subscriptionservice.DefaultProfileName
					}
					enabled := panelFormBool(r.FormValue("enabled"))
					store, storeErr := openStoreWithInit(r.Context(), resolvedDB)
					if storeErr != nil {
						ops.set("error", storeErr.Error())
						break
					}
					if profile != subscriptionservice.DefaultProfileName {
						subscriptionDir, dirErr := resolveSubscriptionDir(configPathValue)
						if dirErr != nil {
							_ = store.Close()
							ops.set("error", fmt.Sprintf("resolve subscription dir: %v", dirErr))
							break
						}
						entry, setErr := setNamedSubscriptionProfileEnabled(userID, subscriptionDir, profile, enabled)
						_ = store.Close()
						if setErr != nil {
							ops.set("error", fmt.Sprintf("update named subscription profile: %v", setErr))
							break
						}
						if enabled {
							inboundIDs := compactUnique(entry.InboundIDs)
							if len(inboundIDs) == 0 {
								ops.set("error", fmt.Sprintf("named subscription %s enabled, but profile has no inbounds", profile))
								break
							}
							out, runErr := panelExecuteCommandWithSetup(r.Context(), newSubscriptionGenerateCmd(&configPathValue, &dbPathValue), []string{userID}, func(cmd *cobra.Command) error {
								if err := cmd.Flags().Set("profile", profile); err != nil {
									return err
								}
								return cmd.Flags().Set("inbounds", strings.Join(inboundIDs, ","))
							})
							if runErr != nil {
								ops.set("error", fmt.Sprintf("named subscription enabled (%s), but generate failed: %s", profile, panelErrWithOutput(runErr, out)))
								break
							}
							ops.set("ok", fmt.Sprintf("named subscription enabled: profile=%s | %s", profile, panelSummarizeOutput(out)))
							break
						}
						appCfg, _ := loadAppConfig(configPathValue)
						decoySiteDir := strings.TrimSpace(appCfg.Paths.DecoySiteDir)
						removed, rmErr := cleanupNamedSubscriptionFilesWithMirror(userID, subscriptionDir, decoySiteDir, profile, strings.TrimSpace(entry.AccessToken))
						if rmErr != nil {
							ops.set("error", fmt.Sprintf("named subscription disabled (%s), but cleanup failed: %v", profile, rmErr))
							break
						}
						inboundSet := make(map[string]struct{}, len(entry.InboundIDs))
						for _, inboundID := range compactUnique(entry.InboundIDs) {
							id := strings.TrimSpace(inboundID)
							if id == "" {
								continue
							}
							inboundSet[id] = struct{}{}
						}
						revoked, revokeErr := panelRevokeCredentialsForUser(r.Context(), resolvedDB, userID, inboundSet)
						if revokeErr != nil {
							ops.set("error", fmt.Sprintf("named subscription disabled (%s), removed files=%d, but revoke credentials failed: %v", profile, removed, revokeErr))
							break
						}
						synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(r.Context(), resolvedDB, strings.TrimSpace(configPathValue), nil)
						if syncErr != nil {
							ops.set("error", fmt.Sprintf("named subscription disabled (%s), removed files=%d, credentials revoked=%d, but node sync failed: %v", profile, removed, revoked, syncErr))
							break
						}
						ops.set("ok", fmt.Sprintf("named subscription disabled: profile=%s (removed files: %d, credentials revoked=%d) | node sync: synced=%d cleaned=%d", profile, removed, revoked, synced, cleaned))
						break
					}
					sub, subErr := store.Subscriptions().GetByUserID(r.Context(), userID)
					if subErr != nil {
						if errors.Is(subErr, sql.ErrNoRows) {
							if enabled {
								out, runErr := panelExecuteCommand(r.Context(), newSubscriptionGenerateCmd(&configPathValue, &dbPathValue), []string{userID})
								if runErr != nil {
									ops.set("error", "subscription enable failed: "+panelErrWithOutput(runErr, out))
								} else {
									ops.set("ok", "subscription enabled: "+panelSummarizeOutput(out))
								}
							} else {
								revoked, revokeErr := panelRevokeCredentialsForUser(r.Context(), resolvedDB, userID, nil)
								if revokeErr != nil {
									ops.set("error", fmt.Sprintf("subscription already disabled, but revoke credentials failed: %v", revokeErr))
									_ = store.Close()
									break
								}
								synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(r.Context(), resolvedDB, strings.TrimSpace(configPathValue), nil)
								if syncErr != nil {
									ops.set("error", fmt.Sprintf("subscription already disabled for user %s, credentials revoked=%d, but node sync failed: %v", userID, revoked, syncErr))
									_ = store.Close()
									break
								}
								ops.set("ok", fmt.Sprintf("subscription already disabled for user %s | credentials revoked=%d | node sync: synced=%d cleaned=%d", userID, revoked, synced, cleaned))
							}
							_ = store.Close()
							break
						}
						_ = store.Close()
						ops.set("error", fmt.Sprintf("read subscription: %v", subErr))
						break
					}
					sub.Enabled = enabled
					sub.UpdatedAt = time.Now().UTC()
					if _, upsertErr := store.Subscriptions().Upsert(r.Context(), sub); upsertErr != nil {
						_ = store.Close()
						ops.set("error", fmt.Sprintf("update subscription: %v", upsertErr))
						break
					}
					_ = store.Close()
					if enabled {
						out, runErr := panelExecuteCommand(r.Context(), newSubscriptionGenerateCmd(&configPathValue, &dbPathValue), []string{userID})
						if runErr != nil {
							ops.set("error", "subscription enabled, but generate failed: "+panelErrWithOutput(runErr, out))
						} else {
							ops.set("ok", "subscription enabled: "+panelSummarizeOutput(out))
						}
					} else {
						subscriptionDir, dirErr := resolveSubscriptionDir(configPathValue)
						if dirErr != nil {
							ops.set("error", fmt.Sprintf("subscription disabled, but cleanup failed: %v", dirErr))
							break
						}
						appCfg, _ := loadAppConfig(configPathValue)
						decoySiteDir := strings.TrimSpace(appCfg.Paths.DecoySiteDir)
						removed, rmErr := cleanupUserSubscriptionFilesWithMirror(userID, subscriptionDir, decoySiteDir, strings.TrimSpace(sub.OutputPath), strings.TrimSpace(sub.AccessToken))
						if rmErr != nil {
							ops.set("error", fmt.Sprintf("subscription disabled, but cleanup failed: %v", rmErr))
						} else {
							revoked, revokeErr := panelRevokeCredentialsForUser(r.Context(), resolvedDB, userID, nil)
							if revokeErr != nil {
								ops.set("error", fmt.Sprintf("subscription disabled for user %s (removed files: %d), but revoke credentials failed: %v", userID, removed, revokeErr))
								break
							}
							synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(r.Context(), resolvedDB, strings.TrimSpace(configPathValue), nil)
							if syncErr != nil {
								ops.set("error", fmt.Sprintf("subscription disabled for user %s (removed files: %d, credentials revoked=%d), but node sync failed: %v", userID, removed, revoked, syncErr))
								break
							}
							ops.set("ok", fmt.Sprintf("subscription disabled for user %s (removed files: %d, credentials revoked=%d) | node sync: synced=%d cleaned=%d", userID, removed, revoked, synced, cleaned))
						}
					}
				case "set_label":
					userID := strings.TrimSpace(r.FormValue("user_id"))
					if userID == "" {
						ops.set("error", "user id is required")
						break
					}
					profile := wizardNormalizeProfileName(r.FormValue("profile"))
					if profile == "" {
						profile = subscriptionservice.DefaultProfileName
					}
					label := strings.TrimSpace(r.FormValue("label"))
					if profile != subscriptionservice.DefaultProfileName {
						// Named profile — update JSON file.
						subscriptionDir, dirErr := resolveSubscriptionDir(configPathValue)
						if dirErr != nil {
							ops.set("error", fmt.Sprintf("resolve subscription dir: %v", dirErr))
							break
						}
						profilesPath := filepath.Join(subscriptionDir, "profiles", userID+".json")
						content, readErr := os.ReadFile(profilesPath)
						if readErr != nil {
							ops.set("error", fmt.Sprintf("read profiles file: %v", readErr))
							break
						}
						var file wizardSubscriptionProfilesFile
						if err := json.Unmarshal(content, &file); err != nil {
							ops.set("error", fmt.Sprintf("decode profiles file: %v", err))
							break
						}
						found := false
						for i := range file.Profiles {
							if wizardNormalizeProfileName(file.Profiles[i].Name) == profile {
								file.Profiles[i].Label = label
								found = true
								break
							}
						}
						if !found {
							ops.set("error", fmt.Sprintf("subscription profile %q not found", profile))
							break
						}
						encoded, encErr := json.MarshalIndent(file, "", "  ")
						if encErr != nil {
							ops.set("error", fmt.Sprintf("encode profiles file: %v", encErr))
							break
						}
						if writeErr := os.WriteFile(profilesPath, append(encoded, '\n'), 0o644); writeErr != nil {
							ops.set("error", fmt.Sprintf("write profiles file: %v", writeErr))
							break
						}
						ops.set("ok", fmt.Sprintf("subscription label updated: profile=%s", profile))
						break
					}
					// Default subscription — update SQLite.
					store, storeErr := openStoreWithInit(r.Context(), resolvedDB)
					if storeErr != nil {
						ops.set("error", storeErr.Error())
						break
					}
					sub, subErr := store.Subscriptions().GetByUserID(r.Context(), userID)
					if subErr != nil {
						_ = store.Close()
						ops.set("error", fmt.Sprintf("read subscription: %v", subErr))
						break
					}
					sub.Label = label
					if _, upsertErr := store.Subscriptions().Upsert(r.Context(), sub); upsertErr != nil {
						_ = store.Close()
						ops.set("error", fmt.Sprintf("update subscription label: %v", upsertErr))
						break
					}
					_ = store.Close()
					ops.set("ok", "subscription label updated")
				case "refresh_all":
					out, runErr := panelRefreshAllSubscriptions(r.Context(), &configPathValue, &dbPathValue)
					if runErr != nil {
						ops.set("error", "subscription refresh failed: "+panelErrWithOutput(runErr, out))
					} else {
						ops.set("ok", "subscriptions refreshed: "+panelSummarizeOutput(out))
					}
				case "delete_link":
					rawLink := strings.TrimSpace(r.FormValue("subscription"))
					token := panelSubscriptionTokenFromLink(rawLink)
					if token == "" {
						ops.set("error", "subscription link is invalid")
						break
					}
					store, storeErr := openStoreWithInit(r.Context(), resolvedDB)
					if storeErr != nil {
						ops.set("error", storeErr.Error())
						break
					}
					users, usersErr := store.Users().List(r.Context())
					if usersErr != nil {
						_ = store.Close()
						ops.set("error", fmt.Sprintf("list users: %v", usersErr))
						break
					}
					subscriptionDir, dirErr := resolveSubscriptionDir(configPathValue)
					if dirErr != nil {
						_ = store.Close()
						ops.set("error", fmt.Sprintf("resolve subscription dir: %v", dirErr))
						break
					}
					appCfg, _ := loadAppConfig(configPathValue)
					decoySiteDir := strings.TrimSpace(appCfg.Paths.DecoySiteDir)
					foundUserID := ""
					var foundSub domain.Subscription
					for _, user := range users {
						sub, subErr := store.Subscriptions().GetByUserID(r.Context(), user.ID)
						if subErr != nil {
							continue
						}
						if strings.TrimSpace(sub.AccessToken) == token {
							foundUserID = user.ID
							foundSub = sub
							break
						}
					}
					removed := 0
					if foundUserID != "" {
						rmCount, rmErr := cleanupUserSubscriptionFilesWithMirror(foundUserID, subscriptionDir, decoySiteDir, strings.TrimSpace(foundSub.OutputPath), strings.TrimSpace(foundSub.AccessToken))
						if rmErr != nil {
							_ = store.Close()
							ops.set("error", fmt.Sprintf("cleanup subscription files: %v", rmErr))
							break
						}
						removed = rmCount
						revoked, revokeErr := panelRevokeCredentialsForUser(r.Context(), resolvedDB, foundUserID, nil)
						if revokeErr != nil {
							_ = store.Close()
							ops.set("error", fmt.Sprintf("subscription files cleaned (%s), but revoke credentials failed: %v", token, revokeErr))
							break
						}
						deleted, delErr := store.Subscriptions().DeleteByUserID(r.Context(), foundUserID)
						_ = store.Close()
						if delErr != nil {
							ops.set("error", fmt.Sprintf("delete subscription: %v", delErr))
							break
						}
						synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(r.Context(), resolvedDB, strings.TrimSpace(configPathValue), nil)
						if syncErr != nil {
							ops.set("error", fmt.Sprintf("subscription deleted (%s), credentials revoked=%d, but node sync failed: %v", token, revoked, syncErr))
							break
						}
						if deleted {
							ops.set("ok", fmt.Sprintf("subscription deleted (%s), removed files: %d, credentials revoked=%d | node sync: synced=%d cleaned=%d", token, removed, revoked, synced, cleaned))
						} else {
							ops.set("ok", fmt.Sprintf("subscription files cleaned (%s), removed files: %d, credentials revoked=%d | node sync: synced=%d cleaned=%d", token, removed, revoked, synced, cleaned))
						}
						break
					}
					foundNamed := false
					for _, user := range users {
						profilesPath := filepath.Join(strings.TrimSpace(subscriptionDir), "profiles", strings.TrimSpace(user.ID)+".json")
						content, readErr := os.ReadFile(profilesPath)
						if readErr != nil {
							if os.IsNotExist(readErr) {
								continue
							}
							_ = store.Close()
							ops.set("error", fmt.Sprintf("read profiles file: %v", readErr))
							foundNamed = true
							break
						}
						var file wizardSubscriptionProfilesFile
						if unmarshalErr := json.Unmarshal(content, &file); unmarshalErr != nil {
							_ = store.Close()
							ops.set("error", fmt.Sprintf("decode profiles file: %v", unmarshalErr))
							foundNamed = true
							break
						}
						for _, entry := range file.Profiles {
							if strings.TrimSpace(entry.AccessToken) != token {
								continue
							}
							inboundSet := make(map[string]struct{}, len(entry.InboundIDs))
							for _, inboundID := range compactUnique(entry.InboundIDs) {
								id := strings.TrimSpace(inboundID)
								if id == "" {
									continue
								}
								inboundSet[id] = struct{}{}
							}
							revoked, revokeErr := panelRevokeCredentialsForUser(r.Context(), resolvedDB, user.ID, inboundSet)
							if revokeErr != nil {
								_ = store.Close()
								ops.set("error", fmt.Sprintf("delete named profile: revoke credentials failed: %v", revokeErr))
								foundNamed = true
								break
							}
							deleted, rmCount, delErr := deleteNamedWizardSubscriptionProfileWithMirror(user.ID, subscriptionDir, decoySiteDir, entry.Name)
							_ = store.Close()
							if delErr != nil {
								ops.set("error", fmt.Sprintf("delete named profile: %v", delErr))
								foundNamed = true
								break
							}
							synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(r.Context(), resolvedDB, strings.TrimSpace(configPathValue), nil)
							if syncErr != nil {
								ops.set("error", fmt.Sprintf("named subscription deleted (%s): profile=%s user=%s metadata=%t removed_files=%d credentials_revoked=%d, but node sync failed: %v", token, wizardNormalizeProfileName(entry.Name), user.ID, deleted, rmCount, revoked, syncErr))
								foundNamed = true
								break
							}
							ops.set("ok", fmt.Sprintf("named subscription deleted (%s): profile=%s user=%s metadata=%t removed_files=%d credentials_revoked=%d | node sync: synced=%d cleaned=%d", token, wizardNormalizeProfileName(entry.Name), user.ID, deleted, rmCount, revoked, synced, cleaned))
							foundNamed = true
							break
						}
						if foundNamed {
							break
						}
					}
					if foundNamed {
						break
					}
					rmCount, rmErr := cleanupSubscriptionTokenFilesWithMirror(subscriptionDir, decoySiteDir, token)
					_ = store.Close()
					if rmErr != nil {
						ops.set("error", fmt.Sprintf("cleanup subscription token files: %v", rmErr))
						break
					}
					ops.set("ok", fmt.Sprintf("subscription token files removed (%s): %d", token, rmCount))
				case "delete_user":
					userID := strings.TrimSpace(r.FormValue("user_id"))
					if userID == "" {
						ops.set("error", "user id is required")
						break
					}
					store, storeErr := openStoreWithInit(r.Context(), resolvedDB)
					if storeErr != nil {
						ops.set("error", storeErr.Error())
						break
					}
					subscriptionDir, dirErr := resolveSubscriptionDir(configPathValue)
					if dirErr != nil {
						_ = store.Close()
						ops.set("error", fmt.Sprintf("resolve subscription dir: %v", dirErr))
						break
					}
					appCfg, _ := loadAppConfig(configPathValue)
					decoySiteDir := strings.TrimSpace(appCfg.Paths.DecoySiteDir)
					sub, subErr := store.Subscriptions().GetByUserID(r.Context(), userID)
					removedFiles := 0
					if subErr == nil {
						rmCount, rmErr := cleanupUserSubscriptionFilesWithMirror(userID, subscriptionDir, decoySiteDir, strings.TrimSpace(sub.OutputPath), strings.TrimSpace(sub.AccessToken))
						if rmErr != nil {
							_ = store.Close()
							ops.set("error", fmt.Sprintf("cleanup subscription files: %v", rmErr))
							break
						}
						removedFiles = rmCount
					} else {
						rmCount, rmErr := cleanupUserSubscriptionFilesWithMirror(userID, subscriptionDir, decoySiteDir, "", "")
						if rmErr != nil {
							_ = store.Close()
							ops.set("error", fmt.Sprintf("cleanup subscription files: %v", rmErr))
							break
						}
						removedFiles = rmCount
					}
					deleted, delErr := store.Subscriptions().DeleteByUserID(r.Context(), userID)
					_ = store.Close()
					if delErr != nil {
						ops.set("error", fmt.Sprintf("delete subscription: %v", delErr))
						break
					}
					revoked, revokeErr := panelRevokeCredentialsForUser(r.Context(), resolvedDB, userID, nil)
					if revokeErr != nil {
						ops.set("error", fmt.Sprintf("subscription deleted for user %s (removed files: %d), but revoke credentials failed: %v", userID, removedFiles, revokeErr))
						break
					}
					synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(r.Context(), resolvedDB, strings.TrimSpace(configPathValue), nil)
					if syncErr != nil {
						ops.set("error", fmt.Sprintf("subscription deleted for user %s (removed files: %d, credentials revoked=%d), but node sync failed: %v", userID, removedFiles, revoked, syncErr))
						break
					}
					if deleted {
						ops.set("ok", fmt.Sprintf("subscription deleted for user %s (removed files: %d, credentials revoked=%d) | node sync: synced=%d cleaned=%d", userID, removedFiles, revoked, synced, cleaned))
					} else {
						ops.set("ok", fmt.Sprintf("subscription files cleaned for user %s (removed files: %d, credentials revoked=%d) | node sync: synced=%d cleaned=%d", userID, removedFiles, revoked, synced, cleaned))
					}
				case "delete_selected_credentials":
					userID := strings.TrimSpace(r.FormValue("user_id"))
					if userID == "" {
						ops.set("error", "user id is required")
						break
					}
					inboundIDs := parseCSV(r.FormValue("inbounds"))
					if len(inboundIDs) == 0 {
						ops.set("error", "select at least one inbound")
						break
					}
					inboundSet := make(map[string]struct{}, len(inboundIDs))
					for _, id := range inboundIDs {
						inboundSet[id] = struct{}{}
					}
					store, storeErr := openStoreWithInit(r.Context(), resolvedDB)
					if storeErr != nil {
						ops.set("error", storeErr.Error())
						break
					}
					credentials, listErr := store.Credentials().List(r.Context())
					if listErr != nil {
						_ = store.Close()
						ops.set("error", fmt.Sprintf("list credentials: %v", listErr))
						break
					}
					toDelete := make([]domain.Credential, 0, len(credentials))
					remainingForUser := 0
					for _, cred := range credentials {
						if strings.TrimSpace(cred.UserID) != userID {
							continue
						}
						if _, ok := inboundSet[strings.TrimSpace(cred.InboundID)]; ok {
							toDelete = append(toDelete, cred)
							continue
						}
						remainingForUser++
					}
					deleteFailed := false
					for _, cred := range toDelete {
						if _, delErr := store.Credentials().Delete(r.Context(), cred.ID); delErr != nil {
							_ = store.Close()
							ops.set("error", fmt.Sprintf("detach credential %s: %v", cred.ID, delErr))
							deleteFailed = true
							break
						}
					}
					if deleteFailed {
						break
					}
					deletedCount := len(toDelete)
					if deletedCount == 0 {
						_ = store.Close()
						ops.set("ok", "no matching credentials to detach")
						break
					}
					if remainingForUser == 0 {
						removedFiles := 0
						if sub, subErr := store.Subscriptions().GetByUserID(r.Context(), userID); subErr == nil {
							if subscriptionDir, dirErr := resolveSubscriptionDir(configPathValue); dirErr == nil {
								appCfg, _ := loadAppConfig(configPathValue)
								decoySiteDir := strings.TrimSpace(appCfg.Paths.DecoySiteDir)
								if rmCount, rmErr := cleanupUserSubscriptionFilesWithMirror(userID, subscriptionDir, decoySiteDir, strings.TrimSpace(sub.OutputPath), strings.TrimSpace(sub.AccessToken)); rmErr == nil {
									removedFiles = rmCount
								}
							}
						}
						_, _ = store.Subscriptions().DeleteByUserID(r.Context(), userID)
						_ = store.Close()
						ops.set("ok", fmt.Sprintf("credentials detached: %d, subscription disabled (removed files: %d)", deletedCount, removedFiles))
						break
					}
					_ = store.Close()
					out, runErr := panelExecuteCommand(r.Context(), newSubscriptionGenerateCmd(&configPathValue, &dbPathValue), []string{userID})
					if runErr != nil {
						ops.set("error", fmt.Sprintf("credentials detached: %d, but subscription generate failed: %s", deletedCount, panelErrWithOutput(runErr, out)))
					} else {
						ops.set("ok", fmt.Sprintf("credentials detached: %d | %s", deletedCount, panelSummarizeOutput(out)))
					}
				case "attach_credential", "delete_credential", "update_credential":
					if err := panelHandleCredentialAction(r.Context(), resolvedDB, r, &configPathValue, &dbPathValue, ops); err != nil {
						ops.set("error", err.Error())
					}
				default:
					ops.set("error", "unknown subscription action")
				}
				panelRespondAction(w, r, subsPath, ops)
			})
			if appPath != "/" {
				panelMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/" {
						http.Redirect(w, r, appPath, http.StatusFound)
						return
					}
					http.NotFound(w, r)
				})
			}

			var handler http.Handler = panelMux
			if requireAuth {
				auth := newPanelCookieAuth(panelInfo.Login, panelInfo.Password, basePath, appPath, logoutPath)
				panelMux.HandleFunc(auth.loginPath, auth.handleLogin)
				panelMux.HandleFunc(logoutPath, auth.handleLogout)
				handler = auth.middleware(panelMux)
			}

			httpServer := &http.Server{
				Addr:              listenAddr,
				Handler:           handler,
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      30 * time.Second,
				IdleTimeout:       60 * time.Second,
			}

			fmt.Fprintf(cmd.OutOrStdout(), "panel listen: %s\n", listenAddr)
			fmt.Fprintf(cmd.OutOrStdout(), "panel path: %s\n", basePath)
			if requireAuth {
				fmt.Fprintln(cmd.OutOrStdout(), "panel auth: enabled (login page)")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "terminate with Ctrl+C")

			// Load traffic collection setting from config.
			if strings.TrimSpace(configPathValue) != "" {
				panelTrafficCollection.Store(loadTrafficCollectionEnabled(strings.TrimSpace(configPathValue)))
			}

			// Background: collect traffic from xray and sing-box every 30s.
			go func() {
				t := time.NewTicker(30 * time.Second)
				defer t.Stop()
				for {
					select {
					case <-t.C:
						if panelTrafficCollection.Load() {
							_ = panelCollectXrayTraffic(context.Background(), resolvedDB, "127.0.0.1:10090")
							_ = panelCollectSingboxTraffic(context.Background(), resolvedDB, "127.0.0.1:10091")
						}
					}
				}
			}()

			// Background: enforce user expiry and traffic limits every 60s.
			go func() {
				t := time.NewTicker(60 * time.Second)
				defer t.Stop()
				for {
					select {
					case <-t.C:
						panelEnforceUserPolicy(context.Background(), resolvedDB, ops)
					}
				}
			}()

			return httpServer.ListenAndServe()
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", "", "HTTP listen address, default: 127.0.0.1:<panel-port>")
	cmd.Flags().StringVar(&basePath, "base-path", "", "Panel base path, default from panel-admin.env or /panel")
	cmd.Flags().BoolVar(&requireAuth, "auth", true, "Require HTTP basic auth from panel credentials file")
	return cmd
}

type panelSnapshot struct {
	counts            panelCounts
	units             []panelUnitState
	users             []panelUserView
	nodes             []panelNodeView
	inbounds          []panelInboundView
	credentials       []panelCredentialView
	subscriptionLinks []string
	subscriptionViews []panelSubscriptionView
	subscriptionState map[string]panelSubscriptionState
	suggestedPorts    map[string]map[string]int // nodeID → "proto|transport" → port
	sniPresets        []string
	dashboard         panelDashboardView
	sshKeyWarning     string // non-empty if SSH key is not configured/found
}

func buildPanelSnapshot(ctx context.Context, dbPath string, cfg config.AppConfig) (panelSnapshot, error) {
	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return panelSnapshot{}, err
	}
	defer store.Close()

	users, err := store.Users().List(ctx)
	if err != nil {
		return panelSnapshot{}, fmt.Errorf("list users: %w", err)
	}
	nodes, err := store.Nodes().List(ctx)
	if err != nil {
		return panelSnapshot{}, fmt.Errorf("list nodes: %w", err)
	}
	inbounds, err := store.Inbounds().List(ctx)
	if err != nil {
		return panelSnapshot{}, fmt.Errorf("list inbounds: %w", err)
	}
	credentials, err := store.Credentials().List(ctx)
	if err != nil {
		return panelSnapshot{}, fmt.Errorf("list credentials: %w", err)
	}
	subscriptions := make(map[string]panelSubscriptionState, len(users))

	nodeNameByID := make(map[string]string, len(nodes))
	nodeByID := make(map[string]domain.Node, len(nodes))
	nodeRows := make([]panelNodeView, 0, len(nodes))
	for _, node := range nodes {
		nodeNameByID[node.ID] = strings.TrimSpace(node.Name)
		nodeByID[node.ID] = node
		nv := panelNodeView{
			ID:          node.ID,
			Name:        node.Name,
			Host:        node.Host,
			Role:        string(node.Role),
			SSHUser:     node.SSHUser,
			SSHPort:     node.SSHPort,
			Enabled:     node.Enabled,
			DisableIPv6: node.DisableIPv6,
			BlockPing:   node.BlockPing,
			Version:     panelNodeVersion(node),
		}
		if ok, msg, found := getNodeSyncStatus(node.ID); found {
			okVal := ok
			nv.SyncOK = &okVal
			nv.SyncMsg = msg
		} else if node.LastSyncOK != nil {
			// Fall back to DB-persisted status (survives panel restarts).
			nv.SyncOK = node.LastSyncOK
			nv.SyncMsg = node.LastSyncMsg
		}
		if j := getActiveJobForNode(node.ID); j != nil {
			nv.JobID = j.id
		}
		if rv, ok := panelNodeVersionCache.Load(node.ID); ok {
			nv.RemoteVersion = rv.(string)
		}
		nodeRows = append(nodeRows, nv)
	}
	sort.Slice(nodeRows, func(i, j int) bool { return nodeRows[i].ID < nodeRows[j].ID })

	// Detect port conflicts: count inbounds per (nodeID, port).
	portCountPerNode := make(map[string]int, len(inbounds)) // "nodeID:port" → count
	for _, ib := range inbounds {
		if ib.Port > 0 {
			portCountPerNode[ib.NodeID+":"+strconv.Itoa(ib.Port)]++
		}
	}

	inboundRows := make([]panelInboundView, 0, len(inbounds))
	inboundByID := make(map[string]domain.Inbound, len(inbounds))
	enabledInbounds := 0
	// per-node used ports: nodeID → set of ports
	usedPortsByNode := make(map[string]map[int]struct{}, len(nodes))
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
		nodeName := inbound.NodeID
		if name := strings.TrimSpace(nodeNameByID[inbound.NodeID]); name != "" {
			nodeName = name
		}
		conflict := inbound.Port > 0 && portCountPerNode[inbound.NodeID+":"+strconv.Itoa(inbound.Port)] > 1
		inboundRows = append(inboundRows, panelInboundView{
			ID:                 inbound.ID,
			Type:               string(inbound.Type),
			Engine:             string(inbound.Engine),
			NodeID:             inbound.NodeID,
			NodeName:           nodeName,
			Domain:             strings.TrimSpace(inbound.Domain),
			Port:               inbound.Port,
			TLS:                inbound.TLSEnabled,
			Enabled:            inbound.Enabled,
			Transport:          strings.TrimSpace(inbound.Transport),
			Path:               strings.TrimSpace(inbound.Path),
			SNI:                strings.TrimSpace(inbound.SNI),
			RealityEnabled:     inbound.RealityEnabled,
			RealityPublicKey:   strings.TrimSpace(inbound.RealityPublicKey),
			RealityPrivateKey:  strings.TrimSpace(inbound.RealityPrivateKey),
			RealityShortID:     strings.TrimSpace(inbound.RealityShortID),
			RealityFingerprint: strings.TrimSpace(inbound.RealityFingerprint),
			RealitySpiderX:     strings.TrimSpace(inbound.RealitySpiderX),
			RealityServer:      strings.TrimSpace(inbound.RealityServer),
			RealityServerPort:  inbound.RealityServerPort,
			SelfSteal:          inbound.SelfSteal,
			VLESSFlow:          strings.TrimSpace(inbound.VLESSFlow),
			SniffingEnabled:    inbound.SniffingEnabled,
			SniffingHTTP:       inbound.SniffingHTTP,
			SniffingTLS:        inbound.SniffingTLS,
			SniffingQUIC:       inbound.SniffingQUIC,
			SniffingFakeDNS:    inbound.SniffingFakeDNS,
			PortConflict:       conflict,
			Version:            panelInboundVersion(inbound),
		})
		// Track ALL ports (enabled and disabled) per-node so suggestions avoid them.
		if inbound.Port > 0 && inbound.NodeID != "" {
			if usedPortsByNode[inbound.NodeID] == nil {
				usedPortsByNode[inbound.NodeID] = make(map[int]struct{})
			}
			usedPortsByNode[inbound.NodeID][inbound.Port] = struct{}{}
		}
		if inbound.Enabled {
			enabledInbounds++
		}
	}
	sort.Slice(inboundRows, func(i, j int) bool { return inboundRows[i].ID < inboundRows[j].ID })

	protoTransportItems := []struct{ protocol, transport string }{
		{protocol: "vless", transport: "tcp"},
		{protocol: "vless", transport: "ws"},
		{protocol: "vless", transport: "grpc"},
		{protocol: "hysteria2", transport: "udp"},
		{protocol: "xhttp", transport: "xhttp"},
	}
	suggestedPorts := make(map[string]map[string]int, len(nodes)+1)
	// Per-node suggestions.
	for _, node := range nodes {
		nodeUsed := usedPortsByNode[node.ID]
		if nodeUsed == nil {
			nodeUsed = make(map[int]struct{})
		}
		m := make(map[string]int, len(protoTransportItems))
		for _, item := range protoTransportItems {
			key := item.protocol + "|" + item.transport
			m[key] = suggestWizardPort(item.protocol, item.transport, nodeUsed, nil)
		}
		suggestedPorts[node.ID] = m
	}

	trafficRows, _ := store.UserTraffic().List(ctx)
	trafficByUser := make(map[string]domain.UserTrafficRecord, len(trafficRows))
	for _, t := range trafficRows {
		trafficByUser[t.UserID] = t
	}

	userRows := make([]panelUserView, 0, len(users))
	userByID := make(map[string]domain.User, len(users))
	enabledUsers := 0
	for _, user := range users {
		userByID[user.ID] = user
		t := trafficByUser[user.ID]
		userRows = append(userRows, panelUserView{
			ID:                user.ID,
			Name:              user.Name,
			Enabled:           user.Enabled,
			CreatedAt:         user.CreatedAt,
			ExpiresAt:         user.ExpiresAt,
			TrafficLimitBytes: user.TrafficLimitBytes,
			UsedRXBytes:       t.RXBytes,
			UsedTXBytes:       t.TXBytes,
			Version:           panelUserVersion(user),
		})
		if user.Enabled {
			enabledUsers++
		}
		if sub, subErr := store.Subscriptions().GetByUserID(ctx, user.ID); subErr == nil {
			subscriptions[user.ID] = panelSubscriptionState{Exists: true, Enabled: sub.Enabled}
		} else if errors.Is(subErr, sql.ErrNoRows) {
			subscriptions[user.ID] = panelSubscriptionState{Exists: false, Enabled: false}
		} else {
			subscriptions[user.ID] = panelSubscriptionState{Exists: false, Enabled: false}
		}
	}

	credentialRows := make([]panelCredentialView, 0, len(credentials))
	configLabelsByUser := make(map[string][]string, len(users))
	configLabelsByUserInbound := make(map[string]map[string][]string, len(users))
	inboundLabelByID := make(map[string]string, len(inbounds))
	for _, inbound := range inbounds {
		nodeName := strings.TrimSpace(nodeNameByID[inbound.NodeID])
		if nodeName == "" {
			nodeName = strings.TrimSpace(inbound.NodeID)
		}
		label := strings.TrimSpace(strings.Join([]string{
			string(inbound.Type),
			strings.TrimSpace(inbound.Domain) + ":" + strconv.Itoa(inbound.Port),
			"|",
			nodeName,
		}, " "))
		inboundLabelByID[inbound.ID] = label
	}
	for _, cred := range credentials {
		userName := cred.UserID
		if u, ok := userByID[cred.UserID]; ok && strings.TrimSpace(u.Name) != "" {
			userName = strings.TrimSpace(u.Name)
		}
		inboundAddr := cred.InboundID
		inboundType := ""
		if in, ok := inboundByID[cred.InboundID]; ok {
			inboundType = string(in.Type)
			addr := strings.TrimSpace(in.Domain)
			if addr == "" {
				addr = "<no-domain>"
			}
			inboundAddr = fmt.Sprintf("%s:%d", addr, in.Port)
		}
		clientURI, clientErr := panelBuildClientURI(ctx, nodeByID, inboundByID, cred)
		credentialRows = append(credentialRows, panelCredentialView{
			ID:          cred.ID,
			UserID:      cred.UserID,
			UserName:    userName,
			ClientLabel: credentialLabel(cred),
			InboundID:   cred.InboundID,
			InboundType: inboundType,
			InboundAddr: inboundAddr,
			Kind:        string(cred.Kind),
			SecretMask:  maskSecret(cred.Secret),
			ClientURI:   clientURI,
			ClientError: clientErr,
			Version:     panelCredentialVersion(cred),
		})
		configLabel := strings.TrimSpace(credentialLabel(cred))
		if configLabel == "" {
			configLabel = strings.TrimSpace(inboundType + " " + inboundAddr)
		}
		if configLabel != "" {
			configLabelsByUser[cred.UserID] = append(configLabelsByUser[cred.UserID], configLabel)
			userInbound := configLabelsByUserInbound[cred.UserID]
			if userInbound == nil {
				userInbound = make(map[string][]string)
				configLabelsByUserInbound[cred.UserID] = userInbound
			}
			userInbound[cred.InboundID] = append(userInbound[cred.InboundID], configLabel)
		}
	}
	sort.Slice(credentialRows, func(i, j int) bool { return credentialRows[i].ID < credentialRows[j].ID })
	for userID, labels := range configLabelsByUser {
		configLabelsByUser[userID] = compactUnique(labels)
		sort.Strings(configLabelsByUser[userID])
	}
	for userID, byInbound := range configLabelsByUserInbound {
		for inboundID, labels := range byInbound {
			configLabelsByUserInbound[userID][inboundID] = compactUnique(labels)
			sort.Strings(configLabelsByUserInbound[userID][inboundID])
		}
	}

	subViews := make([]panelSubscriptionView, 0, len(users)*2)
	makeURL := func(token string) string {
		token = strings.TrimSpace(token)
		if token == "" {
			return ""
		}
		publicURL := buildSubscriptionPublicURL(cfg, token)
		if publicURL == "" {
			publicURL = "http://<server-ip-or-domain>/sub/" + token
		}
		return publicURL
	}
	for _, user := range users {
		sub, subErr := store.Subscriptions().GetByUserID(ctx, user.ID)
		if subErr == nil {
			token := strings.TrimSpace(sub.AccessToken)
			labels := configLabelsByUser[user.ID]
			inboundIDs := make([]string, 0, len(configLabelsByUserInbound[user.ID]))
			for inboundID := range configLabelsByUserInbound[user.ID] {
				if strings.TrimSpace(inboundID) == "" {
					continue
				}
				inboundIDs = append(inboundIDs, strings.TrimSpace(inboundID))
			}
			sort.Strings(inboundIDs)
			subViews = append(subViews, panelSubscriptionView{
				UserID:       user.ID,
				UserName:     strings.TrimSpace(user.Name),
				ProfileName:  subscriptionservice.DefaultProfileName,
				Label:        strings.TrimSpace(sub.Label),
				Enabled:      sub.Enabled,
				AccessToken:  token,
				URL:          makeURL(token),
				ConfigCount:  len(labels),
				ConfigLabels: labels,
				InboundIDs:   inboundIDs,
			})
		}
		profilesPath := filepath.Join(strings.TrimSpace(cfg.Paths.Subscription), "profiles", strings.TrimSpace(user.ID)+".json")
		content, readErr := os.ReadFile(profilesPath)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return panelSnapshot{}, fmt.Errorf("read profiles file: %w", readErr)
		}
		var file wizardSubscriptionProfilesFile
		if err := json.Unmarshal(content, &file); err != nil {
			return panelSnapshot{}, fmt.Errorf("decode profiles file: %w", err)
		}
		for _, entry := range file.Profiles {
			profileName := wizardNormalizeProfileName(entry.Name)
			if profileName == "" || profileName == subscriptionservice.DefaultProfileName {
				continue
			}
			labels := make([]string, 0, len(entry.InboundIDs))
			for _, inboundID := range compactUnique(entry.InboundIDs) {
				candidates := configLabelsByUserInbound[user.ID][inboundID]
				if len(candidates) > 0 {
					labels = append(labels, candidates...)
					continue
				}
				if fallback := strings.TrimSpace(inboundLabelByID[inboundID]); fallback != "" {
					labels = append(labels, fallback)
				}
			}
			labels = compactUnique(labels)
			sort.Strings(labels)
			token := strings.TrimSpace(entry.AccessToken)
			inboundIDs := compactUnique(entry.InboundIDs)
			sort.Strings(inboundIDs)
			subViews = append(subViews, panelSubscriptionView{
				UserID:       user.ID,
				UserName:     strings.TrimSpace(user.Name),
				ProfileName:  profileName,
				Label:        strings.TrimSpace(entry.Label),
				Enabled:      wizardProfileEntryEnabled(entry),
				AccessToken:  token,
				URL:          makeURL(token),
				ConfigCount:  len(labels),
				ConfigLabels: labels,
				InboundIDs:   inboundIDs,
			})
		}
	}
	sort.Slice(subViews, func(i, j int) bool {
		left := strings.TrimSpace(subViews[i].UserName)
		right := strings.TrimSpace(subViews[j].UserName)
		if left == right {
			if subViews[i].ProfileName != subViews[j].ProfileName {
				if subViews[i].ProfileName == subscriptionservice.DefaultProfileName {
					return true
				}
				if subViews[j].ProfileName == subscriptionservice.DefaultProfileName {
					return false
				}
				return subViews[i].ProfileName < subViews[j].ProfileName
			}
			return subViews[i].UserID < subViews[j].UserID
		}
		return left < right
	})

	subLinks, err := listPanelSubscriptionLinks(cfg)
	if err != nil {
		return panelSnapshot{}, err
	}
	dashboard := buildPanelDashboard(cfg, userRows)

	sshKeyWarning := ""
	hasRemoteNodes := false
	for _, n := range nodeRows {
		if n.Enabled && n.Role != string(domain.NodeRolePrimary) {
			hasRemoteNodes = true
			break
		}
	}
	if hasRemoteNodes {
		if syncSettings, settingsErr := panelNodeSyncSettingsFromEnv(); settingsErr == nil && syncSettings.enabled {
			if syncSettings.opts.sshKeyPath == "" {
				sshKeyWarning = "SSH key not configured — node sync may require a password. Use 'setup ssh key' on each node."
			} else if _, statErr := os.Stat(syncSettings.opts.sshKeyPath); statErr != nil {
				sshKeyWarning = fmt.Sprintf("SSH key not found at %s — node sync may require a password. Use 'setup ssh key' on each node.", syncSettings.opts.sshKeyPath)
			}
		}
	}

	return panelSnapshot{
		counts: panelCounts{
			UsersTotal:     len(users),
			UsersEnabled:   enabledUsers,
			InboundsTotal:  len(inboundRows),
			InboundsActive: enabledInbounds,
		},
		units:             runtimeUnitStates(ctx, cfg),
		users:             userRows,
		nodes:             nodeRows,
		inbounds:          inboundRows,
		credentials:       credentialRows,
		subscriptionLinks: subLinks,
		subscriptionViews: subViews,
		subscriptionState: subscriptions,
		suggestedPorts:    suggestedPorts,
		sniPresets:        panelWizardSNIPresets(),
		dashboard:         dashboard,
		sshKeyWarning:     sshKeyWarning,
	}, nil
}

func panelWizardSNIPresets() []string {
	if len(realityServerPresets) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(realityServerPresets))
	out := make([]string, 0, len(realityServerPresets))
	for _, raw := range realityServerPresets {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func buildPanelDashboard(cfg config.AppConfig, users []panelUserView) panelDashboardView {
	load1, load5, load15, _ := panelLoadAvg()
	if math.IsNaN(load1) || math.IsInf(load1, 0) {
		load1 = 0
	}
	cpuPct := panelCPUPercent()
	memUsed, memTotal, _ := panelMemoryUsage()
	swapUsed, swapTotal, _ := panelSwapUsage()
	diskUsed, diskTotal, _ := panelDiskUsage(cfg.Paths.StateDir)
	uptime, _ := panelUptimeSeconds()
	rawRX, rawTX, _ := panelNetTotals()
	rxSpeed, txSpeed := panelNetSpeed()
	tcpConns, udpConns := panelNetConnections()
	userTraffic, source := panelUserTrafficFromDB(users)
	totalUserBytes := uint64(0)
	for _, item := range userTraffic {
		totalUserBytes += item.TotalBytes
	}
	// Apply traffic reset baseline.
	dashMu.Lock()
	baseRX := trafResetRX
	baseTX := trafResetTX
	dashMu.Unlock()
	displayRX, displayTX := rawRX, rawTX
	if displayRX > baseRX {
		displayRX -= baseRX
	} else {
		displayRX = 0
	}
	if displayTX > baseTX {
		displayTX -= baseTX
	} else {
		displayTX = 0
	}
	totalBytes := displayRX + displayTX
	if totalUserBytes > 0 {
		totalBytes = totalUserBytes
	}
	return panelDashboardView{
		ProxyctlVersion: strings.TrimSpace(Version),
		Load1:           load1,
		Load5:           load5,
		Load15:          load15,
		CPUCores:        runtime.NumCPU(),
		CPUPercent:      cpuPct,
		MemUsedBytes:    memUsed,
		MemTotalBytes:   memTotal,
		SwapUsedBytes:   swapUsed,
		SwapTotalBytes:  swapTotal,
		DiskUsedBytes:   diskUsed,
		DiskTotalBytes:  diskTotal,
		UptimeSeconds:   uptime,
		TotalRXBytes:    displayRX,
		TotalTXBytes:    displayTX,
		TotalBytes:      totalBytes,
		NetRXSpeed:      rxSpeed,
		NetTXSpeed:      txSpeed,
		TCPConns:        tcpConns,
		UDPConns:        udpConns,
		UserTraffic:     userTraffic,
		TrafficSource:   source,
	}
}

func panelMemoryUsage() (used uint64, total uint64, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	var memTotalKB, memAvailKB uint64
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				memTotalKB, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				memAvailKB, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		}
	}
	total = memTotalKB * 1024
	if memTotalKB > memAvailKB {
		used = (memTotalKB - memAvailKB) * 1024
	}
	return used, total, nil
}

func panelDiskUsage(path string) (used uint64, total uint64, err error) {
	target := strings.TrimSpace(path)
	if target == "" {
		target = "/"
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(target, &stat); err != nil {
		return 0, 0, err
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	if total > free {
		used = total - free
	}
	return used, total, nil
}

func panelUptimeSeconds() (uint64, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("invalid /proc/uptime")
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	if f < 0 {
		return 0, nil
	}
	return uint64(f), nil
}

func panelNetTotals() (rx uint64, tx uint64, err error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[2:] {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		rxBytes, rxErr := strconv.ParseUint(fields[0], 10, 64)
		txBytes, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr == nil {
			rx += rxBytes
		}
		if txErr == nil {
			tx += txBytes
		}
	}
	return rx, tx, nil
}

// panelLoadAvg returns 1m, 5m, 15m load averages.
func panelLoadAvg() (load1, load5, load15 float64, err error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, fmt.Errorf("invalid /proc/loadavg")
	}
	load1, _ = strconv.ParseFloat(fields[0], 64)
	load5, _ = strconv.ParseFloat(fields[1], 64)
	load15, _ = strconv.ParseFloat(fields[2], 64)
	return load1, load5, load15, nil
}

// panelSwapUsage reads SwapTotal/SwapFree from /proc/meminfo.
func panelSwapUsage() (used, total uint64, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	var swapTotalKB, swapFreeKB uint64
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SwapTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				swapTotalKB, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		}
		if strings.HasPrefix(line, "SwapFree:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				swapFreeKB, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		}
	}
	total = swapTotalKB * 1024
	if swapTotalKB > swapFreeKB {
		used = (swapTotalKB - swapFreeKB) * 1024
	}
	return used, total, nil
}

// panelCPUPercent computes CPU usage percent via delta from /proc/stat.
func panelCPUPercent() float64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	var fields []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "cpu ") {
			fields = strings.Fields(line)
			break
		}
	}
	// cpu user nice system idle iowait irq softirq steal
	if len(fields) < 5 {
		return 0
	}
	vals := make([]uint64, len(fields)-1)
	for i, f := range fields[1:] {
		vals[i], _ = strconv.ParseUint(f, 10, 64)
	}
	// total = sum of all, idle = vals[3]
	var total uint64
	for _, v := range vals {
		total += v
	}
	idle := vals[3]

	dashMu.Lock()
	pIdle := prevCPUIdle
	pTotal := prevCPUTotal
	prevCPUIdle = idle
	prevCPUTotal = total
	dashMu.Unlock()

	if pTotal == 0 || total <= pTotal {
		return 0
	}
	dTotal := total - pTotal
	dIdle := idle - pIdle
	if dTotal == 0 {
		return 0
	}
	pct := 100.0 * float64(dTotal-dIdle) / float64(dTotal)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return math.Round(pct*10) / 10
}

// panelNetSpeed computes RX/TX bytes-per-second via delta from /proc/net/dev.
func panelNetSpeed() (rxSpeed, txSpeed uint64) {
	rx, tx, err := panelNetTotals()
	if err != nil {
		return 0, 0
	}
	now := time.Now()

	dashMu.Lock()
	pRX := prevNetRX
	pTX := prevNetTX
	pAt := prevNetAt
	prevNetRX = rx
	prevNetTX = tx
	prevNetAt = now
	dashMu.Unlock()

	if pAt.IsZero() {
		return 0, 0
	}
	elapsed := now.Sub(pAt).Seconds()
	if elapsed <= 0 {
		return 0, 0
	}
	if rx >= pRX {
		rxSpeed = uint64(float64(rx-pRX) / elapsed)
	}
	if tx >= pTX {
		txSpeed = uint64(float64(tx-pTX) / elapsed)
	}
	return rxSpeed, txSpeed
}

// panelNetConnections counts rows in /proc/net/tcp*, /proc/net/udp*.
func panelNetConnections() (tcp, udp int) {
	for _, p := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		// first line is header
		for _, l := range lines[1:] {
			if strings.TrimSpace(l) != "" {
				tcp++
			}
		}
	}
	for _, p := range []string{"/proc/net/udp", "/proc/net/udp6"} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for _, l := range lines[1:] {
			if strings.TrimSpace(l) != "" {
				udp++
			}
		}
	}
	return tcp, udp
}

func panelUserTrafficFromDB(users []panelUserView) ([]panelUserTrafficView, string) {
	rows := make([]panelUserTrafficView, 0, len(users))
	hasData := false
	for _, u := range users {
		var rx, tx uint64
		if u.UsedRXBytes > 0 {
			rx = uint64(u.UsedRXBytes)
		}
		if u.UsedTXBytes > 0 {
			tx = uint64(u.UsedTXBytes)
		}
		total := rx + tx
		if total > 0 {
			hasData = true
		}
		rows = append(rows, panelUserTrafficView{
			UserID:     u.ID,
			UserName:   u.Name,
			RXBytes:    rx,
			TXBytes:    tx,
			TotalBytes: total,
		})
	}
	if !hasData {
		return rows, "none"
	}
	return rows, "db"
}

func panelUserTraffic(runtimeDir string, users []panelUserView) ([]panelUserTrafficView, string) {
	rows := make([]panelUserTrafficView, 0, len(users))
	for _, u := range users {
		rows = append(rows, panelUserTrafficView{UserID: u.ID, UserName: u.Name})
	}
	path := filepath.Join(strings.TrimSpace(runtimeDir), "user-traffic.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return rows, "none"
	}
	type trafficItem struct {
		UserID     string `json:"user_id"`
		UserName   string `json:"user_name"`
		RXBytes    uint64 `json:"rx_bytes"`
		TXBytes    uint64 `json:"tx_bytes"`
		TotalBytes uint64 `json:"total_bytes"`
	}
	var payload struct {
		UsersMap  map[string]trafficItem `json:"users"`
		UsersList []trafficItem          `json:"user_traffic"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return rows, "none"
	}
	byID := make(map[string]trafficItem, len(payload.UsersMap)+len(payload.UsersList))
	for key, item := range payload.UsersMap {
		id := strings.TrimSpace(item.UserID)
		if id == "" {
			id = strings.TrimSpace(key)
		}
		if id == "" {
			continue
		}
		byID[id] = item
	}
	for _, item := range payload.UsersList {
		id := strings.TrimSpace(item.UserID)
		if id == "" {
			continue
		}
		byID[id] = item
	}
	for i := range rows {
		if item, ok := byID[rows[i].UserID]; ok {
			rows[i].RXBytes = item.RXBytes
			rows[i].TXBytes = item.TXBytes
			rows[i].TotalBytes = item.TotalBytes
			if rows[i].TotalBytes == 0 {
				rows[i].TotalBytes = rows[i].RXBytes + rows[i].TXBytes
			}
		}
	}
	return rows, "user-traffic.json"
}

// panelResetUserTraffic zeros out traffic bytes in user-traffic.json.
// If userID is empty, all users are reset.
func panelResetUserTraffic(runtimeDir, userID string) error {
	path := filepath.Join(strings.TrimSpace(runtimeDir), "user-traffic.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	type trafficItem struct {
		UserID     string `json:"user_id"`
		UserName   string `json:"user_name"`
		RXBytes    uint64 `json:"rx_bytes"`
		TXBytes    uint64 `json:"tx_bytes"`
		TotalBytes uint64 `json:"total_bytes"`
	}
	var payload struct {
		UsersMap  map[string]trafficItem `json:"users,omitempty"`
		UsersList []trafficItem          `json:"user_traffic,omitempty"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	zero := func(item trafficItem) trafficItem {
		item.RXBytes = 0
		item.TXBytes = 0
		item.TotalBytes = 0
		return item
	}
	for k, v := range payload.UsersMap {
		if userID == "" || v.UserID == userID || k == userID {
			payload.UsersMap[k] = zero(v)
		}
	}
	for i, v := range payload.UsersList {
		if userID == "" || v.UserID == userID {
			payload.UsersList[i] = zero(v)
		}
	}
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// panelCollectXrayTraffic queries xray stats API and upserts per-user traffic into DB.
func panelCollectXrayTraffic(ctx context.Context, dbPath, addr string) error {
	cmd := exec.CommandContext(ctx, "xray", "api", "statsquery",
		"--server="+addr, "--pattern=user>>>", "--reset")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("xray stats query: %w", err)
	}
	return panelParseAndUpsertStats(ctx, dbPath, out)
}

// panelCollectSingboxTraffic queries sing-box v2ray-compatible stats API.
func panelCollectSingboxTraffic(ctx context.Context, dbPath, addr string) error {
	cmd := exec.CommandContext(ctx, "xray", "api", "statsquery",
		"--server="+addr, "--pattern=user>>>", "--reset")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("sing-box stats query: %w", err)
	}
	return panelParseAndUpsertStats(ctx, dbPath, out)
}

// panelParseAndUpsertStats parses xray/sing-box API stat JSON and upserts traffic.
func panelParseAndUpsertStats(ctx context.Context, dbPath string, data []byte) error {
	var payload struct {
		Stat []struct {
			Name  string `json:"name"`
			Value int64  `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("parse stats: %w", err)
	}
	if len(payload.Stat) == 0 {
		return nil
	}

	// Accumulate: name format = "user>>>userID>>>traffic>>>uplink|downlink"
	type userTraffic struct{ rx, tx int64 }
	traffic := make(map[string]*userTraffic)
	for _, stat := range payload.Stat {
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) != 4 {
			continue
		}
		userID := parts[1]
		direction := parts[3]
		if _, ok := traffic[userID]; !ok {
			traffic[userID] = &userTraffic{}
		}
		switch direction {
		case "downlink":
			traffic[userID].rx += stat.Value
		case "uplink":
			traffic[userID].tx += stat.Value
		}
	}

	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	for userID, t := range traffic {
		if t.rx == 0 && t.tx == 0 {
			continue
		}
		if err := store.UserTraffic().Upsert(ctx, userID, t.rx, t.tx); err != nil {
			return err
		}
	}
	return nil
}

// panelEnforceUserPolicy disables users that have exceeded their expiry or traffic limit.
func panelEnforceUserPolicy(ctx context.Context, dbPath string, ops *panelOperationFeed) {
	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return
	}
	defer store.Close()

	users, err := store.Users().List(ctx)
	if err != nil {
		return
	}
	trafficRows, _ := store.UserTraffic().List(ctx)
	trafficByUser := make(map[string]domain.UserTrafficRecord, len(trafficRows))
	for _, t := range trafficRows {
		trafficByUser[t.UserID] = t
	}

	now := time.Now().UTC()
	var changedNodeIDs []string
	for _, user := range users {
		if !user.Enabled {
			continue
		}
		disabled := false
		reason := ""
		if user.ExpiresAt != nil && now.After(*user.ExpiresAt) {
			disabled = true
			reason = "expired"
		}
		if !disabled && user.TrafficLimitBytes > 0 {
			t := trafficByUser[user.ID]
			if t.RXBytes+t.TXBytes >= user.TrafficLimitBytes {
				disabled = true
				reason = "traffic limit exceeded"
			}
		}
		if disabled {
			user.Enabled = false
			if _, updateErr := store.Users().Update(ctx, user); updateErr == nil {
				ops.set("ok", fmt.Sprintf("user %s disabled: %s", user.Name, reason))
				changedNodeIDs = append(changedNodeIDs, "all")
			}
		}
	}
	if len(changedNodeIDs) > 0 {
		// Sync all nodes since we don't know which ones have credentials for the affected users.
		go func() {
			_, _, _ = panelSyncWorkerNodesByIDs(context.Background(), dbPath, "", nil)
		}()
	}
}

func panelRunSystemctl(ctx context.Context, subcmd, unit string) (string, error) {
	cmd := exec.CommandContext(ctx, "systemctl", subcmd, unit)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// panelFetchLatestVersion queries GitHub API for the latest proxyctl release.
// Results are cached for 1 hour.
func panelFetchLatestVersion(ctx context.Context) (tag, downloadURL string, err error) {
	panelUpdMu.Lock()
	if !panelUpdAt.IsZero() && time.Since(panelUpdAt) < time.Hour {
		tag, downloadURL = panelUpdTag, panelUpdURL
		panelUpdMu.Unlock()
		return
	}
	panelUpdMu.Unlock()

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet,
		"https://api.github.com/repos/DarkSidr/proxyctl/releases/latest", nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "proxyctl/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return
	}
	tag = rel.TagName
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	for _, a := range rel.Assets {
		n := strings.ToLower(a.Name)
		if strings.Contains(n, goos) && strings.Contains(n, goarch) {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	panelUpdMu.Lock()
	panelUpdTag, panelUpdURL, panelUpdAt = tag, downloadURL, time.Now()
	panelUpdMu.Unlock()
	return
}

// panelSelfUpdate downloads a new proxyctl binary and atomically replaces the current one.
func panelSelfUpdate(downloadURL string) error {
	if !strings.HasPrefix(downloadURL, "https://github.com/DarkSidr/proxyctl/") {
		return fmt.Errorf("invalid download URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "proxyctl/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %s", resp.Status)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolve symlink: %w", err)
	}
	tmpPath := self + ".new"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	_, copyErr := io.Copy(f, resp.Body)
	f.Close()
	if copyErr != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write binary: %w", copyErr)
	}
	if err := os.Rename(tmpPath, self); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}

func panelExecuteCommand(ctx context.Context, cmd *cobra.Command, args []string) (string, error) {
	return panelExecuteCommandWithSetup(ctx, cmd, args, nil)
}

func panelRespondAction(w http.ResponseWriter, r *http.Request, redirectPath string, ops *panelOperationFeed) {
	if panelWantsJSON(r) {
		status, message, at := ops.snapshot()
		panelWriteJSON(w, http.StatusOK, map[string]string{
			"status":  status,
			"message": message,
			"at":      at,
		})
		return
	}
	http.Redirect(w, r, redirectPath, http.StatusSeeOther)
}

func panelWantsJSON(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("api")), "1") {
		return true
	}
	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))
	return strings.Contains(accept, "application/json")
}

func panelWriteJSON(w http.ResponseWriter, status int, payload any) {
	if w == nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "json marshal failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func panelExecuteCommandWithSetup(ctx context.Context, cmd *cobra.Command, args []string, setup func(*cobra.Command) error) (string, error) {
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(ctx)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if setup != nil {
		if err := setup(cmd); err != nil {
			return "", err
		}
	}
	if args == nil {
		args = []string{}
	}
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return strings.TrimSpace(out.String()), err
}

func panelRefreshAllSubscriptions(ctx context.Context, configPath, dbPath *string) (string, error) {
	return panelExecuteCommandWithSetup(ctx, newSubscriptionRefreshCmd(configPath, dbPath), nil, func(cmd *cobra.Command) error {
		return cmd.Flags().Set("all", "true")
	})
}

func panelErrWithOutput(err error, out string) string {
	base := strings.TrimSpace(err.Error())
	if strings.TrimSpace(out) == "" {
		return base
	}
	return base + " | " + panelSummarizeOutput(out)
}

func panelSummarizeOutput(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	parts := make([]string, 0, 2)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts = append(parts, line)
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) == 0 {
		return "ok"
	}
	return strings.Join(parts, " | ")
}

type panelNodeSyncSettings struct {
	enabled bool
	opts    nodeSyncOptions
}

const (
	panelEnvAutoNodeSyncEnabled = "PROXYCTL_PANEL_AUTO_NODE_SYNC"
	panelEnvAutoNodeSyncUser    = "PROXYCTL_PANEL_NODE_SYNC_SSH_USER"
	panelEnvAutoNodeSyncPort    = "PROXYCTL_PANEL_NODE_SYNC_SSH_PORT"
	panelEnvAutoNodeSyncKey     = "PROXYCTL_PANEL_NODE_SYNC_SSH_KEY"
	panelEnvAutoNodeSyncPass    = "PROXYCTL_PANEL_NODE_SYNC_SSH_PASSWORD"
	panelEnvAutoNodeSyncRuntime = "PROXYCTL_PANEL_NODE_SYNC_RUNTIME_DIR"
	panelEnvAutoNodeSyncStrict  = "PROXYCTL_PANEL_NODE_SYNC_STRICT_HOST_KEY"
	panelEnvAutoNodeSyncSudo    = "PROXYCTL_PANEL_NODE_SYNC_REMOTE_SUDO"
	panelEnvAutoNodeSyncRestart = "PROXYCTL_PANEL_NODE_SYNC_RESTART"
)

func panelNodeSyncSettingsFromEnv() (panelNodeSyncSettings, error) {
	enabled, err := panelEnvBool(panelEnvAutoNodeSyncEnabled, true)
	if err != nil {
		return panelNodeSyncSettings{}, err
	}
	sshUser := strings.TrimSpace(os.Getenv(panelEnvAutoNodeSyncUser))
	if sshUser == "" {
		sshUser = "root"
	}
	sshPort, err := panelEnvInt(panelEnvAutoNodeSyncPort, 22)
	if err != nil {
		return panelNodeSyncSettings{}, err
	}
	if sshPort < 1 || sshPort > 65535 {
		return panelNodeSyncSettings{}, fmt.Errorf("%s must be in range 1..65535", panelEnvAutoNodeSyncPort)
	}
	runtimeDir := strings.TrimSpace(os.Getenv(panelEnvAutoNodeSyncRuntime))
	if runtimeDir == "" {
		runtimeDir = "/etc/proxy-orchestrator/runtime"
	}
	strictHostKey, err := panelEnvBool(panelEnvAutoNodeSyncStrict, false)
	if err != nil {
		return panelNodeSyncSettings{}, err
	}
	restart, err := panelEnvBool(panelEnvAutoNodeSyncRestart, true)
	if err != nil {
		return panelNodeSyncSettings{}, err
	}
	defaultUseSudo := sshUser != "root"
	remoteUseSudo, err := panelEnvBool(panelEnvAutoNodeSyncSudo, defaultUseSudo)
	if err != nil {
		return panelNodeSyncSettings{}, err
	}

	sshKeyPath := ""
	rawKeyPath, keyConfigured := os.LookupEnv(panelEnvAutoNodeSyncKey)
	if !keyConfigured {
		rawKeyPath = "~/.ssh/id_ed25519"
	}
	rawKeyPath = strings.TrimSpace(rawKeyPath)
	if rawKeyPath != "" {
		resolvedKeyPath, resolveErr := expandHomePath(rawKeyPath)
		if resolveErr != nil {
			if keyConfigured {
				return panelNodeSyncSettings{}, fmt.Errorf("resolve %s: %w", panelEnvAutoNodeSyncKey, resolveErr)
			}
			// Fallback for service environments without HOME.
			resolvedKeyPath = panelDefaultSSHKeyPath(sshUser)
		}
		if keyConfigured {
			sshKeyPath = resolvedKeyPath
		} else if _, statErr := os.Stat(resolvedKeyPath); statErr == nil {
			sshKeyPath = resolvedKeyPath
		}
	}

	return panelNodeSyncSettings{
		enabled: enabled,
		opts: nodeSyncOptions{
			sshUser:       sshUser,
			sshPort:       sshPort,
			sshKeyPath:    sshKeyPath,
			sshPassword:   strings.TrimSpace(os.Getenv(panelEnvAutoNodeSyncPass)),
			runtimeDir:    runtimeDir,
			restart:       restart,
			strictHostKey: strictHostKey,
			remoteUseSudo: remoteUseSudo,
		},
	}, nil
}

func panelEnvBool(key string, fallback bool) (bool, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s has invalid boolean value %q", key, raw)
	}
}

func panelEnvInt(key string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer: %w", key, err)
	}
	return value, nil
}

func panelDefaultSSHKeyPath(sshUser string) string {
	userName := strings.TrimSpace(sshUser)
	if userName == "" || strings.EqualFold(userName, "root") {
		return "/root/.ssh/id_ed25519"
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return filepath.Join(home, ".ssh", "id_ed25519")
	}
	if current, err := user.Current(); err == nil {
		if home := strings.TrimSpace(current.HomeDir); home != "" {
			return filepath.Join(home, ".ssh", "id_ed25519")
		}
	}
	return "/tmp/proxyctl-panel-ssh/id_ed25519"
}

func panelSyncWorkerNodesByIDs(ctx context.Context, dbPath, configPath string, nodeIDs []string) (int, int, error) {
	return panelSyncWorkerNodesByIDsWithPassword(ctx, dbPath, configPath, nodeIDs, "")
}

func panelSyncWorkerNodesByIDsWithPassword(ctx context.Context, dbPath, configPath string, nodeIDs []string, sshPassword string) (int, int, error) {
	settings, err := panelNodeSyncSettingsFromEnv()
	if err != nil {
		return 0, 0, err
	}
	if strings.TrimSpace(sshPassword) != "" {
		settings.opts.sshPassword = strings.TrimSpace(sshPassword)
	}

	appCfg, err := config.Load(configPath)
	if err != nil {
		return 0, 0, err
	}
	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return 0, 0, err
	}
	defer store.Close()

	nodes, err := store.Nodes().List(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list nodes: %w", err)
	}
	inbounds, err := store.Inbounds().List(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list inbounds: %w", err)
	}
	credentials, err := store.Credentials().List(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list credentials: %w", err)
	}

	filter := make(map[string]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		id := strings.TrimSpace(nodeID)
		if id == "" {
			continue
		}
		filter[id] = struct{}{}
	}

	inboundsByNode := make(map[string][]domain.Inbound, len(nodes))
	for _, inbound := range inbounds {
		if !inbound.Enabled {
			continue
		}
		inboundsByNode[inbound.NodeID] = append(inboundsByNode[inbound.NodeID], inbound)
	}
	credentialsByInbound := make(map[string][]domain.Credential, len(inbounds))
	for _, cred := range credentials {
		credentialsByInbound[strings.TrimSpace(cred.InboundID)] = append(credentialsByInbound[strings.TrimSpace(cred.InboundID)], cred)
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	synced := 0
	cleaned := 0
	targeted := 0
	skippedPrimary := 0
	var primaryNodeForCaddy *domain.Node
	for _, node := range nodes {
		if len(filter) > 0 {
			if _, ok := filter[node.ID]; !ok {
				continue
			}
		}
		if !node.Enabled || node.Role != domain.NodeRoleNode {
			// Primary nodes manage their own config locally — SSH sync is not applicable.
			if node.Role == domain.NodeRolePrimary && node.Enabled {
				n := node
				primaryNodeForCaddy = &n
				skippedPrimary++
			}
			continue
		}
		targeted++

		// Remote node sync requires SSH to be enabled and available.
		if !settings.enabled {
			continue
		}
		if _, err := lookPath("ssh"); err != nil {
			setNodeSyncStatus(node.ID, false, "ssh client not available")
			continue
		}

		// Per-node SSH overrides take priority over env-based defaults.
		nodeOpts := settings.opts
		if node.SSHUser != "" {
			nodeOpts.sshUser = node.SSHUser
		}
		if node.SSHPort > 0 {
			nodeOpts.sshPort = node.SSHPort
		}

		nodeInbounds := append([]domain.Inbound(nil), inboundsByNode[node.ID]...)
		sort.Slice(nodeInbounds, func(i, j int) bool { return nodeInbounds[i].ID < nodeInbounds[j].ID })
		if len(nodeInbounds) == 0 {
			if _, err := cleanupSingleNodeRuntime(ctx, node, nodeOpts, appCfg); err != nil {
				setNodeSyncStatus(node.ID, false, err.Error())
				return synced, cleaned, err
			}
			setNodeSyncStatus(node.ID, true, "no inbounds — config cleaned")
			cleaned++
			continue
		}
		nodeCredentials := make([]domain.Credential, 0)
		for _, inbound := range nodeInbounds {
			nodeCredentials = append(nodeCredentials, credentialsByInbound[inbound.ID]...)
		}
		sort.Slice(nodeCredentials, func(i, j int) bool { return nodeCredentials[i].ID < nodeCredentials[j].ID })
		if _, err := lookPath("scp"); err != nil {
			setNodeSyncStatus(node.ID, false, "scp client not available")
			return synced, cleaned, fmt.Errorf("scp client is required for panel node sync: %w", err)
		}

		if _, err := syncSingleNode(ctx, renderer.BuildRequest{
			Node:        node,
			Inbounds:    nodeInbounds,
			Credentials: nodeCredentials,
		}, nodeOpts, appCfg); err != nil {
			setNodeSyncStatus(node.ID, false, err.Error())
			return synced, cleaned, err
		}
		setNodeSyncStatus(node.ID, true, "")
		synced++
	}
	if len(filter) > 0 && targeted == 0 && skippedPrimary == 0 {
		return synced, cleaned, fmt.Errorf("no enabled worker nodes found for requested IDs")
	}
	if skippedPrimary > 0 {
		// Primary node apply runs unconditionally — no SSH required.
		// Reload caddy first so ACME cert acquisition starts before
		// sing-box/xray are restarted by the apply pipeline.
		if primaryNodeForCaddy != nil {
			nodeInbounds := append([]domain.Inbound(nil), inboundsByNode[primaryNodeForCaddy.ID]...)
			sort.Slice(nodeInbounds, func(i, j int) bool { return nodeInbounds[i].ID < nodeInbounds[j].ID })
			// Non-fatal: caddy errors should not block the apply pipeline.
			_ = syncPrimaryNodeCaddy(ctx, *primaryNodeForCaddy, nodeInbounds, appCfg)
		}
		if applyErr := panelApplyPrimary(ctx, configPath, dbPath); applyErr != nil {
			return synced, cleaned, fmt.Errorf("primary node apply failed: %w", applyErr)
		}
	}
	return synced, cleaned, nil
}

func panelApplyPrimary(ctx context.Context, configPath, dbPath string) error {
	_, err := panelExecuteCommand(ctx, newApplyCmd(&configPath, &dbPath), nil)
	return err
}

func panelCleanupNodeRuntime(ctx context.Context, configPath string, node domain.Node) error {
	settings, err := panelNodeSyncSettingsFromEnv()
	if err != nil {
		return err
	}
	if !settings.enabled {
		return nil
	}
	if _, err := lookPath("ssh"); err != nil {
		return fmt.Errorf("ssh client is required for node cleanup: %w", err)
	}
	appCfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	_, err = cleanupSingleNodeRuntime(ctx, node, settings.opts, appCfg)
	return err
}

// panelUninstallNodeRuntime fully uninstalls proxyctl and all its data from a remote node.
// Called only on node delete.
func panelUninstallNodeRuntime(ctx context.Context, configPath string, node domain.Node) error {
	settings, err := panelNodeSyncSettingsFromEnv()
	if err != nil {
		return err
	}
	if !settings.enabled {
		return nil
	}
	if _, err := lookPath("ssh"); err != nil {
		return fmt.Errorf("ssh client is required for node uninstall: %w", err)
	}
	if node.SSHUser != "" {
		settings.opts.sshUser = node.SSHUser
	}
	if node.SSHPort > 0 {
		settings.opts.sshPort = node.SSHPort
	}
	appCfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	return uninstallSingleNode(ctx, node, settings.opts, appCfg)
}

// panelFetchNodeRemoteVersion SSHes to the node, runs "proxyctl version", and caches the result.
func panelFetchNodeRemoteVersion(ctx context.Context, node domain.Node) (string, error) {
	settings, err := panelNodeSyncSettingsFromEnv()
	if err != nil {
		return "", err
	}
	if !settings.enabled {
		return "", fmt.Errorf("auto-node-sync disabled")
	}
	if node.SSHUser != "" {
		settings.opts.sshUser = node.SSHUser
	}
	if node.SSHPort > 0 {
		settings.opts.sshPort = node.SSHPort
	}
	host := strings.TrimSpace(node.Host)
	if host == "" {
		return "", fmt.Errorf("node %q has empty host", node.ID)
	}
	target := fmt.Sprintf("%s@%s", settings.opts.sshUser, host)
	sshArgs := buildSSHArgs(settings.opts.sshPort, settings.opts.sshKeyPath, settings.opts.strictHostKey)
	sshArgs = append(sshArgs, target, "proxyctl version 2>/dev/null || proxyctl --version 2>/dev/null || echo unknown")
	out, err := runRemoteExecCombined(ctx, "ssh", sshArgs, settings.opts.sshPassword)
	if err != nil {
		return "", fmt.Errorf("ssh to %s: %w", host, err)
	}
	ver := strings.TrimSpace(string(out))
	if ver == "" {
		ver = "unknown"
	}
	panelNodeVersionCache.Store(node.ID, ver)
	return ver, nil
}

// panelUpdateProxyctlOnNode SSHes to the node and runs "proxyctl update --force --restart-services".
func panelUpdateProxyctlOnNode(ctx context.Context, configPath string, node domain.Node, sshPassword string) error {
	settings, err := panelNodeSyncSettingsFromEnv()
	if err != nil {
		return err
	}
	if !settings.enabled {
		return fmt.Errorf("auto-node-sync disabled")
	}
	if strings.TrimSpace(sshPassword) != "" {
		settings.opts.sshPassword = strings.TrimSpace(sshPassword)
	}
	if node.SSHUser != "" {
		settings.opts.sshUser = node.SSHUser
	}
	if node.SSHPort > 0 {
		settings.opts.sshPort = node.SSHPort
	}
	host := strings.TrimSpace(node.Host)
	if host == "" {
		return fmt.Errorf("node %q has empty host", node.ID)
	}
	prefix := ""
	if settings.opts.remoteUseSudo {
		prefix = "sudo "
	}
	updateCmd := prefix + "proxyctl update --force --restart-services --ensure-caddy"
	target := fmt.Sprintf("%s@%s", settings.opts.sshUser, host)
	sshArgs := buildSSHArgs(settings.opts.sshPort, settings.opts.sshKeyPath, settings.opts.strictHostKey)
	sshArgs = append(sshArgs, target, updateCmd)
	if out, runErr := runRemoteExecCombined(ctx, "ssh", sshArgs, settings.opts.sshPassword); runErr != nil {
		return fmt.Errorf("update proxyctl on %s: %w | %s", host, runErr, strings.TrimSpace(string(out)))
	}
	// Refresh cached version after update.
	_, _ = panelFetchNodeRemoteVersion(ctx, node)
	return nil
}

func panelTestNodeConnectivity(ctx context.Context, node domain.Node) error {
	return panelTestNodeConnectivityWithPassword(ctx, node, "")
}

func panelTestNodeConnectivityWithPassword(ctx context.Context, node domain.Node, sshPassword string) error {
	settings, err := panelNodeSyncSettingsFromEnv()
	if err != nil {
		return err
	}
	if !settings.enabled {
		return fmt.Errorf("%s=0, auto-node-sync is disabled", panelEnvAutoNodeSyncEnabled)
	}
	if strings.TrimSpace(sshPassword) != "" {
		settings.opts.sshPassword = strings.TrimSpace(sshPassword)
	}
	if node.SSHUser != "" {
		settings.opts.sshUser = node.SSHUser
	}
	if node.SSHPort > 0 {
		settings.opts.sshPort = node.SSHPort
	}
	if _, err := lookPath("ssh"); err != nil {
		return fmt.Errorf("ssh client is required: %w", err)
	}
	host := strings.TrimSpace(node.Host)
	if host == "" {
		return fmt.Errorf("node %q has empty host", node.ID)
	}
	target := fmt.Sprintf("%s@%s", settings.opts.sshUser, host)
	sshArgs := buildSSHArgs(settings.opts.sshPort, settings.opts.sshKeyPath, settings.opts.strictHostKey)
	sshArgs = append(sshArgs, target, "echo proxyctl-panel-node-test-ok")
	if out, err := runRemoteExecCombined(ctx, "ssh", sshArgs, settings.opts.sshPassword); err != nil {
		return fmt.Errorf("ssh connectivity check failed for node %q (%s): %w | %s", node.ID, host, err, strings.TrimSpace(string(out)))
	}
	sshCheckArgs := buildSSHArgs(settings.opts.sshPort, settings.opts.sshKeyPath, settings.opts.strictHostKey)
	sshCheckArgs = append(sshCheckArgs, target, "command -v proxyctl >/dev/null 2>&1")
	if out, err := runRemoteExecCombined(ctx, "ssh", sshCheckArgs, settings.opts.sshPassword); err != nil {
		return fmt.Errorf("proxyctl is not installed on node %q (%s): %w | %s", node.ID, host, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func panelEnsureProxyctlOnNode(ctx context.Context, configPath string, node domain.Node, sshPassword string) error {
	settings, err := panelNodeSyncSettingsFromEnv()
	if err != nil {
		return err
	}
	if !settings.enabled {
		return nil
	}
	if strings.TrimSpace(sshPassword) != "" {
		settings.opts.sshPassword = strings.TrimSpace(sshPassword)
	}
	if node.SSHUser != "" {
		settings.opts.sshUser = node.SSHUser
	}
	if node.SSHPort > 0 {
		settings.opts.sshPort = node.SSHPort
	}
	if node.Role != domain.NodeRoleNode || !node.Enabled {
		return nil
	}
	if _, err := lookPath("ssh"); err != nil {
		return fmt.Errorf("ssh client is required for node bootstrap: %w", err)
	}
	host := strings.TrimSpace(node.Host)
	if host == "" {
		return fmt.Errorf("node %q has empty host", node.ID)
	}

	contactEmail := ""
	if cfg, cfgErr := config.Load(configPath); cfgErr == nil {
		contactEmail = strings.TrimSpace(cfg.Public.ContactEmail)
	}
	prefix := ""
	if settings.opts.remoteUseSudo {
		prefix = "sudo "
	}
	installEnv := "PROXYCTL_PROMPT_CONFIG=0 PROXYCTL_DEPLOYMENT_MODE=node PROXYCTL_REVERSE_PROXY=caddy PROXYCTL_PUBLIC_DOMAIN=" + shellQuote(host)
	if contactEmail != "" {
		installEnv += " PROXYCTL_CONTACT_EMAIL=" + shellQuote(contactEmail)
	}
	if node.DisableIPv6 {
		installEnv += " PROXYCTL_DISABLE_IPV6=1"
	}
	if node.BlockPing {
		installEnv += " PROXYCTL_BLOCK_PING=1"
	}
	installCmd := prefix + "bash -lc " + shellQuote(
		"command -v curl >/dev/null 2>&1 || (apt-get update -qq && apt-get install -y -qq curl); "+
			"curl -fsSL "+defaultUpdateInstallURL+
			" | "+installEnv+" bash",
	)
	updateCmd := prefix + "proxyctl update --force --restart-services --ensure-caddy"
	remoteCmd := strings.Join([]string{
		"set -e",
		"if command -v proxyctl >/dev/null 2>&1; then " + updateCmd + "; else " + installCmd + "; fi",
		"command -v proxyctl >/dev/null 2>&1",
	}, "; ")

	target := fmt.Sprintf("%s@%s", settings.opts.sshUser, host)
	sshArgs := buildSSHArgs(settings.opts.sshPort, settings.opts.sshKeyPath, settings.opts.strictHostKey)
	sshArgs = append(sshArgs, target, remoteCmd)
	if out, err := runRemoteExecCombined(ctx, "ssh", sshArgs, settings.opts.sshPassword); err != nil {
		return fmt.Errorf("bootstrap proxyctl on node %q (%s): %w | %s", node.ID, host, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// panelApplyNodeHardening applies network hardening (disable IPv6, block ping)
// on an already-installed node without re-running the full bootstrap.
func panelApplyNodeHardening(ctx context.Context, node domain.Node, sshPassword string) error {
	settings, err := panelNodeSyncSettingsFromEnv()
	if err != nil {
		return err
	}
	if !settings.enabled {
		return fmt.Errorf("%s=0, auto-node-sync is disabled", panelEnvAutoNodeSyncEnabled)
	}
	if sshPassword != "" {
		settings.opts.sshPassword = strings.TrimSpace(sshPassword)
	}
	if node.SSHUser != "" {
		settings.opts.sshUser = node.SSHUser
	}
	if node.SSHPort > 0 {
		settings.opts.sshPort = node.SSHPort
	}
	host := strings.TrimSpace(node.Host)
	if host == "" {
		return fmt.Errorf("node %q has empty host", node.ID)
	}

	conf := "/etc/sysctl.d/99-proxyctl-hardening.conf"
	prefix := ""
	if settings.opts.remoteUseSudo {
		prefix = "sudo "
	}

	var cmds []string
	cmds = append(cmds, "set -e")
	if node.DisableIPv6 {
		cmds = append(cmds,
			prefix+"grep -qxF 'net.ipv6.conf.all.disable_ipv6 = 1' "+conf+" 2>/dev/null || "+prefix+"printf 'net.ipv6.conf.all.disable_ipv6 = 1\\nnet.ipv6.conf.default.disable_ipv6 = 1\\nnet.ipv6.conf.lo.disable_ipv6 = 1\\n' >> "+conf,
		)
	}
	if node.BlockPing {
		cmds = append(cmds,
			prefix+"grep -qxF 'net.ipv4.icmp_echo_ignore_all = 1' "+conf+" 2>/dev/null || "+prefix+"printf 'net.ipv4.icmp_echo_ignore_all = 1\\n' >> "+conf,
			// Extra firewall rule: persist via nft in /etc/nftables.conf, or fall back to iptables.
			"("+prefix+"which nft >/dev/null 2>&1 && "+
				"(grep -q proxyctl-block /etc/nftables.conf 2>/dev/null || "+
				"printf '\\ntable inet proxyctl-block {\\n\\tchain input {\\n\\t\\ttype filter hook input priority filter; policy accept;\\n\\t\\ticmp type echo-request drop\\n\\t}\\n}\\n' >> /etc/nftables.conf) && "+
				prefix+"nft delete table inet proxyctl-block 2>/dev/null; "+prefix+"nft -f /etc/nftables.conf) || "+
				"("+prefix+"iptables -C INPUT -p icmp --icmp-type echo-request -j DROP 2>/dev/null || "+
				prefix+"iptables -I INPUT -p icmp --icmp-type echo-request -j DROP 2>/dev/null) || true",
		)
	}
	cmds = append(cmds, prefix+"sysctl -p "+conf)

	target := fmt.Sprintf("%s@%s", settings.opts.sshUser, host)
	sshArgs := buildSSHArgs(settings.opts.sshPort, settings.opts.sshKeyPath, settings.opts.strictHostKey)
	sshArgs = append(sshArgs, target, strings.Join(cmds, "; "))
	if out, err := runRemoteExecCombined(ctx, "ssh", sshArgs, settings.opts.sshPassword); err != nil {
		return fmt.Errorf("apply hardening on node %q (%s): %w | %s", node.ID, host, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func panelApplyLocalHardening(_ context.Context, node domain.Node) error {
	conf := "/etc/sysctl.d/99-proxyctl-hardening.conf"

	existing, _ := os.ReadFile(conf)
	content := string(existing)

	if node.DisableIPv6 {
		lines := []string{
			"net.ipv6.conf.all.disable_ipv6 = 1",
			"net.ipv6.conf.default.disable_ipv6 = 1",
			"net.ipv6.conf.lo.disable_ipv6 = 1",
		}
		for _, l := range lines {
			if !strings.Contains(content, l) {
				content += l + "\n"
			}
		}
	}
	if node.BlockPing {
		line := "net.ipv4.icmp_echo_ignore_all = 1"
		if !strings.Contains(content, line) {
			content += line + "\n"
		}
	}

	if err := os.WriteFile(conf, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", conf, err)
	}
	if out, err := runExecCombined(context.Background(), "sysctl", "-p", conf); err != nil {
		return fmt.Errorf("sysctl -p: %w | %s", err, strings.TrimSpace(string(out)))
	}

	// Firewall rule — persist in /etc/nftables.conf if nft is available,
	// fall back to iptables, apply immediately. Non-fatal if neither available.
	if node.BlockPing {
		applyBlockPingFirewall()
	}
	return nil
}

const nftablesConf = "/etc/nftables.conf"
const nftProxyctlTable = `
table inet proxyctl-block {
	chain input {
		type filter hook input priority filter; policy accept;
		icmp type echo-request drop
	}
}
`
const nftProxyctlMarker = "proxyctl-block"

// applyBlockPingFirewall adds an ICMP echo-request drop rule via nft or iptables
// and persists the nft rule in /etc/nftables.conf so it survives reboots.
func applyBlockPingFirewall() {
	if nft := resolveBinaryPath("nft"); nft != "nft" {
		// Persist in /etc/nftables.conf if not already there.
		existing, _ := os.ReadFile(nftablesConf)
		if !strings.Contains(string(existing), nftProxyctlMarker) {
			updated := strings.TrimRight(string(existing), "\n") + "\n" + nftProxyctlTable
			os.WriteFile(nftablesConf, []byte(updated), 0o644) //nolint:errcheck
		}
		// Apply immediately (idempotent: flush+recreate table).
		runExecCombined(context.Background(), nft, "delete", "table", "inet", "proxyctl-block") //nolint:errcheck
		runExecCombined(context.Background(), nft, "-f", nftablesConf)                          //nolint:errcheck
		return
	}
	if ipt := resolveBinaryPath("iptables"); ipt != "iptables" {
		if _, err := runExecCombined(context.Background(), ipt, "-C", "INPUT", "-p", "icmp", "--icmp-type", "echo-request", "-j", "DROP"); err != nil {
			runExecCombined(context.Background(), ipt, "-I", "INPUT", "-p", "icmp", "--icmp-type", "echo-request", "-j", "DROP") //nolint:errcheck
		}
	}
}

func panelInstallSSHKeyOnNode(ctx context.Context, node domain.Node, sshPassword string) (bool, string, error) {
	settings, err := panelNodeSyncSettingsFromEnv()
	if err != nil {
		return false, "", err
	}
	if !settings.enabled {
		return false, "", fmt.Errorf("%s=0, auto-node-sync is disabled", panelEnvAutoNodeSyncEnabled)
	}
	if node.SSHUser != "" {
		settings.opts.sshUser = node.SSHUser
	}
	if node.SSHPort > 0 {
		settings.opts.sshPort = node.SSHPort
	}
	if _, err := lookPath("ssh"); err != nil {
		return false, "", fmt.Errorf("ssh client is required for ssh key setup: %w", err)
	}
	host := strings.TrimSpace(node.Host)
	if host == "" {
		return false, "", fmt.Errorf("node %q has empty host", node.ID)
	}

	keyPath := strings.TrimSpace(settings.opts.sshKeyPath)
	generated := false
	if keyPath == "" {
		var genErr error
		keyPath, generated, genErr = ensureWizardSSHKey(ctx, panelDefaultSSHKeyPath(settings.opts.sshUser))
		if genErr != nil {
			return false, "", fmt.Errorf("prepare ssh key: %w", genErr)
		}
	} else {
		resolved, resolveErr := expandHomePath(keyPath)
		if resolveErr != nil {
			return false, "", fmt.Errorf("resolve ssh key path: %w", resolveErr)
		}
		keyPath = strings.TrimSpace(resolved)
		if keyPath == "" {
			return false, "", fmt.Errorf("ssh key path is empty")
		}
		if _, statErr := os.Stat(keyPath); statErr != nil {
			var genErr error
			keyPath, generated, genErr = ensureWizardSSHKey(ctx, keyPath)
			if genErr != nil {
				return false, "", fmt.Errorf("prepare configured ssh key %s: %w", keyPath, genErr)
			}
		} else if _, pubErr := os.Stat(keyPath + ".pub"); pubErr != nil {
			var genErr error
			keyPath, generated, genErr = ensureWizardSSHKey(ctx, keyPath)
			if genErr != nil {
				return false, "", fmt.Errorf("prepare ssh public key %s.pub: %w", keyPath, genErr)
			}
		}
	}

	pubPath := strings.TrimSpace(keyPath) + ".pub"
	pubRaw, readErr := os.ReadFile(pubPath)
	if readErr != nil {
		return generated, keyPath, fmt.Errorf("read ssh public key %s: %w", pubPath, readErr)
	}
	pubKey := strings.TrimSpace(string(pubRaw))
	if pubKey == "" {
		return generated, keyPath, fmt.Errorf("ssh public key %s is empty", pubPath)
	}

	target := fmt.Sprintf("%s@%s", settings.opts.sshUser, host)
	installKeyCmd := strings.Join([]string{
		"set -e",
		"umask 077",
		"mkdir -p ~/.ssh",
		"touch ~/.ssh/authorized_keys",
		"grep -qxF " + shellQuote(pubKey) + " ~/.ssh/authorized_keys || echo " + shellQuote(pubKey) + " >> ~/.ssh/authorized_keys",
		"chmod 700 ~/.ssh",
		"chmod 600 ~/.ssh/authorized_keys",
	}, "; ")
	sshArgs := buildSSHArgs(settings.opts.sshPort, keyPath, settings.opts.strictHostKey)
	sshArgs = append(sshArgs, target, installKeyCmd)
	if out, err := runRemoteExecCombined(ctx, "ssh", sshArgs, strings.TrimSpace(sshPassword)); err != nil {
		if strings.TrimSpace(sshPassword) == "" {
			return generated, keyPath, fmt.Errorf("install ssh key on node %q (%s): %w | %s | hint: pass root password in 'setup ssh key' modal for first-time setup", node.ID, host, err, strings.TrimSpace(string(out)))
		}
		return generated, keyPath, fmt.Errorf("install ssh key on node %q (%s): %w | %s", node.ID, host, err, strings.TrimSpace(string(out)))
	}
	return generated, keyPath, nil
}

func panelRevokeCredentialsForUser(ctx context.Context, dbPath, userID string, inboundFilter map[string]struct{}) (int, error) {
	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return 0, err
	}
	defer store.Close()

	credentials, err := store.Credentials().List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list credentials: %w", err)
	}
	toDelete := make([]string, 0)
	for _, cred := range credentials {
		if strings.TrimSpace(cred.UserID) != strings.TrimSpace(userID) {
			continue
		}
		if len(inboundFilter) > 0 {
			if _, ok := inboundFilter[strings.TrimSpace(cred.InboundID)]; !ok {
				continue
			}
		}
		toDelete = append(toDelete, cred.ID)
	}
	deleted := 0
	for _, credID := range toDelete {
		ok, delErr := store.Credentials().Delete(ctx, credID)
		if delErr != nil {
			return deleted, fmt.Errorf("delete credential %s: %w", credID, delErr)
		}
		if ok {
			deleted++
		}
	}
	return deleted, nil
}

func panelEnsureCredentialsForUserInbounds(ctx context.Context, dbPath, userID string, inboundIDs []string) (int, error) {
	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return 0, err
	}
	defer store.Close()

	users, err := store.Users().List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list users: %w", err)
	}
	userFound := false
	for _, user := range users {
		if strings.TrimSpace(user.ID) == strings.TrimSpace(userID) {
			userFound = true
			break
		}
	}
	if !userFound {
		return 0, fmt.Errorf("user %q not found", userID)
	}

	inbounds, err := store.Inbounds().List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list inbounds: %w", err)
	}
	inboundByID := make(map[string]domain.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundByID[strings.TrimSpace(inbound.ID)] = inbound
	}

	targetInboundIDs := compactUnique(inboundIDs)
	if len(targetInboundIDs) == 0 {
		for _, inbound := range inbounds {
			if !inbound.Enabled {
				continue
			}
			targetInboundIDs = append(targetInboundIDs, strings.TrimSpace(inbound.ID))
		}
		sort.Strings(targetInboundIDs)
	}

	credentials, err := store.Credentials().List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list credentials: %w", err)
	}
	existing := make(map[string]struct{}, len(credentials))
	for _, cred := range credentials {
		if strings.TrimSpace(cred.UserID) != strings.TrimSpace(userID) {
			continue
		}
		existing[strings.TrimSpace(cred.InboundID)] = struct{}{}
	}

	created := 0
	for _, inboundID := range targetInboundIDs {
		id := strings.TrimSpace(inboundID)
		if id == "" {
			continue
		}
		if _, ok := existing[id]; ok {
			continue
		}
		inbound, ok := inboundByID[id]
		if !ok {
			continue
		}
		if !inbound.Enabled {
			continue
		}
		credential, credErr := createCredentialForInbound(inbound, userID, "")
		if credErr != nil {
			return created, fmt.Errorf("create credential draft for inbound %s: %w", id, credErr)
		}
		if _, createErr := store.Credentials().Create(ctx, credential); createErr != nil {
			return created, fmt.Errorf("create credential for inbound %s: %w", id, createErr)
		}
		existing[id] = struct{}{}
		created++
	}
	return created, nil
}

func panelHandleUserAction(ctx context.Context, dbPath string, r *http.Request, ops *panelOperationFeed) error {
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("invalid user action request")
	}
	action := strings.TrimSpace(r.FormValue("op"))
	if action == "" {
		return fmt.Errorf("user action is required")
	}

	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	switch action {
	case "create":
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			return fmt.Errorf("user name is required")
		}
		created, err := store.Users().Create(ctx, domain.User{
			Name:    name,
			Enabled: panelFormBool(r.FormValue("enabled")),
		})
		if err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		ops.set("ok", fmt.Sprintf("user created: %s (%s)", created.Name, created.ID))
		return nil
	case "update":
		userID := strings.TrimSpace(r.FormValue("user_id"))
		version := strings.TrimSpace(r.FormValue("version"))
		if userID == "" {
			return fmt.Errorf("user id is required")
		}
		users, err := store.Users().List(ctx)
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		var current domain.User
		found := false
		for _, user := range users {
			if user.ID == userID {
				current = user
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("user %q not found", userID)
		}
		if version != panelUserVersion(current) {
			return fmt.Errorf("user %q changed since page load; refresh and retry", userID)
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			name = current.Name
		}
		var expiresAt *time.Time
		if expiresStr := strings.TrimSpace(r.FormValue("expires_at")); expiresStr != "" {
			t, parseErr := time.Parse("2006-01-02", expiresStr)
			if parseErr != nil {
				t, parseErr = time.Parse(time.RFC3339, expiresStr)
			}
			if parseErr != nil {
				return fmt.Errorf("invalid expires_at format: %s", expiresStr)
			}
			t = t.UTC()
			expiresAt = &t
		}
		trafficLimitBytes := int64(0)
		if tlStr := strings.TrimSpace(r.FormValue("traffic_limit_bytes")); tlStr != "" {
			v, parseErr := strconv.ParseInt(tlStr, 10, 64)
			if parseErr != nil {
				return fmt.Errorf("invalid traffic_limit_bytes: %s", tlStr)
			}
			trafficLimitBytes = v
		}
		updated := domain.User{
			ID:                userID,
			Name:              name,
			Enabled:           panelFormBool(r.FormValue("enabled")),
			CreatedAt:         current.CreatedAt,
			ExpiresAt:         expiresAt,
			TrafficLimitBytes: trafficLimitBytes,
		}
		if _, err := store.Users().Update(ctx, updated); err != nil {
			return fmt.Errorf("update user: %w", err)
		}
		ops.set("ok", fmt.Sprintf("user updated: %s (%s)", updated.Name, updated.ID))
		return nil
	case "reset_traffic":
		userID := strings.TrimSpace(r.FormValue("user_id"))
		if userID == "" {
			return fmt.Errorf("user id is required")
		}
		if err := store.UserTraffic().ResetUser(ctx, userID); err != nil {
			return fmt.Errorf("reset user traffic: %w", err)
		}
		ops.set("ok", fmt.Sprintf("user traffic reset: %s", userID))
		return nil
	case "delete":
		userID := strings.TrimSpace(r.FormValue("user_id"))
		version := strings.TrimSpace(r.FormValue("version"))
		if userID == "" {
			return fmt.Errorf("user id is required")
		}
		users, err := store.Users().List(ctx)
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		var current domain.User
		found := false
		for _, user := range users {
			if user.ID == userID {
				current = user
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("user %q not found", userID)
		}
		if version != panelUserVersion(current) {
			return fmt.Errorf("user %q changed since page load; refresh and retry", userID)
		}
		deleted, err := store.Users().Delete(ctx, userID)
		if err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
		if !deleted {
			return fmt.Errorf("user %q not found", userID)
		}
		ops.set("ok", fmt.Sprintf("user deleted: %s (%s)", current.Name, current.ID))
		return nil
	default:
		return fmt.Errorf("unknown user action")
	}
}

// panelCheckPortConflict returns an error if another inbound on the same node
// already uses the given port. excludeID is the current inbound ID when editing
// (so we don't flag the inbound against itself).
func panelCheckPortConflict(ctx context.Context, repo storage.InboundRepository, nodeID string, port int, excludeID string) error {
	all, err := repo.List(ctx)
	if err != nil {
		return fmt.Errorf("port conflict check: %w", err)
	}
	for _, ib := range all {
		if ib.NodeID != nodeID {
			continue
		}
		if excludeID != "" && ib.ID == excludeID {
			continue
		}
		if ib.Port == port {
			return fmt.Errorf("port %d is already used by inbound %s (type: %s) on this node", port, ib.ID, ib.Type)
		}
	}
	return nil
}

// panelHandleInboundAction handles inbound CRUD actions.
// Returns (jobID, nodeID, error): jobID is non-empty when a background sync job was started
// (remote node). The caller should include job_id/node_id in the JSON response so the
// frontend can poll for completion.
func panelHandleInboundAction(ctx context.Context, dbPath string, r *http.Request, configPath, dbPathFlag *string, ops *panelOperationFeed) (jobID, nodeID string, err error) {
	if err := r.ParseForm(); err != nil {
		return "", "", fmt.Errorf("invalid inbound action request")
	}
	action := strings.TrimSpace(r.FormValue("op"))
	if action == "" {
		return "", "", fmt.Errorf("inbound action is required")
	}

	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return "", "", err
	}
	defer store.Close()

	// panelInboundSyncJob starts a background sync goroutine for remote nodes and
	// returns a non-empty jobID. For primary nodes it syncs synchronously and
	// returns "".
	panelInboundSyncJob := func(nid, opDesc string) string {
		isPrimary := false
		if nodes, listErr := store.Nodes().List(ctx); listErr == nil {
			for _, n := range nodes {
				if n.ID == nid {
					isPrimary = n.Role == "primary"
					break
				}
			}
		}
		if isPrimary {
			synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{nid})
			if syncErr != nil {
				if panelSyncErrMissingCredentials(syncErr) {
					ops.set("ok", opDesc+" | warning: node sync skipped until at least one credential is attached")
				} else {
					ops.set("error", opDesc+" | node sync failed: "+syncErr.Error())
				}
			} else if synced > 0 || cleaned > 0 {
				ops.set("ok", fmt.Sprintf("%s | node sync: synced=%d cleaned=%d", opDesc, synced, cleaned))
			} else {
				ops.set("ok", opDesc)
			}
			return ""
		}
		// Remote node — run sync in background.
		job := newNodeJob(nid, opDesc)
		go func() {
			_, _, syncErr := panelSyncWorkerNodesByIDs(context.Background(), dbPath, strings.TrimSpace(*configPath), []string{nid})
			if syncErr != nil {
				job.finish(false, opDesc+" | sync failed: "+syncErr.Error())
			} else {
				job.finish(true, opDesc+" | sync ok")
			}
		}()
		ops.set("ok", opDesc+" — syncing node in background")
		return job.id
	}

	switch action {
	case "create":
		inbound, err := panelInboundFromForm(r, domain.Inbound{})
		if err != nil {
			return "", "", err
		}
		if err := panelCheckPortConflict(ctx, store.Inbounds(), inbound.NodeID, inbound.Port, ""); err != nil {
			return "", "", err
		}
		created, err := store.Inbounds().Create(ctx, inbound)
		if err != nil {
			return "", "", fmt.Errorf("create inbound: %w", err)
		}
		desc := fmt.Sprintf("inbound created: %s (%s:%d)", created.ID, created.Domain, created.Port)
		jid := panelInboundSyncJob(created.NodeID, desc)
		return jid, created.NodeID, nil
	case "set_enabled":
		inboundID := strings.TrimSpace(r.FormValue("inbound_id"))
		version := strings.TrimSpace(r.FormValue("version"))
		if inboundID == "" {
			return "", "", fmt.Errorf("inbound id is required")
		}
		inbounds, err := store.Inbounds().List(ctx)
		if err != nil {
			return "", "", fmt.Errorf("list inbounds: %w", err)
		}
		var current domain.Inbound
		found := false
		for _, item := range inbounds {
			if item.ID == inboundID {
				current = item
				found = true
				break
			}
		}
		if !found {
			return "", "", fmt.Errorf("inbound %q not found", inboundID)
		}
		if version != panelInboundVersion(current) {
			return "", "", fmt.Errorf("inbound %q changed since page load; refresh and retry", inboundID)
		}
		current.Enabled = panelFormBool(r.FormValue("enabled"))
		stored, err := store.Inbounds().Update(ctx, current)
		if err != nil {
			return "", "", fmt.Errorf("set inbound enabled: %w", err)
		}
		state := "disabled"
		if stored.Enabled {
			state = "enabled"
		}
		desc := fmt.Sprintf("inbound %s: %s", state, stored.ID)
		jid := panelInboundSyncJob(stored.NodeID, desc)
		return jid, stored.NodeID, nil

	case "update", "delete":
		inboundID := strings.TrimSpace(r.FormValue("inbound_id"))
		version := strings.TrimSpace(r.FormValue("version"))
		if inboundID == "" {
			return "", "", fmt.Errorf("inbound id is required")
		}
		inbounds, err := store.Inbounds().List(ctx)
		if err != nil {
			return "", "", fmt.Errorf("list inbounds: %w", err)
		}
		var current domain.Inbound
		found := false
		for _, item := range inbounds {
			if item.ID == inboundID {
				current = item
				found = true
				break
			}
		}
		if !found {
			return "", "", fmt.Errorf("inbound %q not found", inboundID)
		}
		if version != panelInboundVersion(current) {
			return "", "", fmt.Errorf("inbound %q changed since page load; refresh and retry", inboundID)
		}

		if action == "delete" {
			deleted, err := store.Inbounds().Delete(ctx, inboundID)
			if err != nil {
				return "", "", fmt.Errorf("delete inbound: %w", err)
			}
			if !deleted {
				return "", "", fmt.Errorf("inbound %q not found", inboundID)
			}
			// Refresh subscriptions synchronously (fast, local).
			out, refreshErr := panelRefreshAllSubscriptions(ctx, configPath, dbPathFlag)
			subMsg := ""
			if refreshErr != nil {
				subMsg = " | subscription refresh failed: " + panelErrWithOutput(refreshErr, out)
			} else {
				subMsg = " | " + panelSummarizeOutput(out)
			}
			desc := fmt.Sprintf("inbound deleted: %s%s", inboundID, subMsg)
			jid := panelInboundSyncJob(current.NodeID, desc)
			return jid, current.NodeID, nil
		}

		updated, err := panelInboundFromForm(r, current)
		if err != nil {
			return "", "", err
		}
		updated.ID = current.ID
		updated.CreatedAt = current.CreatedAt
		if strings.TrimSpace(updated.TLSCertPath) == "" {
			updated.TLSCertPath = current.TLSCertPath
		}
		if strings.TrimSpace(updated.TLSKeyPath) == "" {
			updated.TLSKeyPath = current.TLSKeyPath
		}
		if updated.RealityEnabled {
			if strings.ToLower(string(updated.Type)) != string(domain.ProtocolVLESS) || strings.ToLower(strings.TrimSpace(updated.Transport)) != "tcp" || strings.ToLower(string(updated.Engine)) != string(domain.EngineXray) {
				return "", "", fmt.Errorf("inbound has reality enabled; keep type=vless transport=tcp engine=xray")
			}
		}
		if err := panelCheckPortConflict(ctx, store.Inbounds(), updated.NodeID, updated.Port, updated.ID); err != nil {
			return "", "", err
		}
		stored, err := store.Inbounds().Update(ctx, updated)
		if err != nil {
			return "", "", fmt.Errorf("update inbound: %w", err)
		}
		desc := fmt.Sprintf("inbound updated: %s (%s:%d)", stored.ID, stored.Domain, stored.Port)
		jid := panelInboundSyncJob(stored.NodeID, desc)
		return jid, stored.NodeID, nil

	default:
		return "", "", fmt.Errorf("unknown inbound action")
	}
}

func panelSyncErrMissingCredentials(err error) bool {
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "requires at least one uuid credential") ||
		strings.Contains(msg, "requires at least one password credential")
}

type nodeDiagServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type nodeDiagCertStatus struct {
	Domain  string `json:"domain"`
	Expiry  string `json:"expiry"`
	Missing bool   `json:"missing"`
}

type nodeDiagResult struct {
	NodeName string                  `json:"node_name"`
	Services []nodeDiagServiceStatus `json:"services"`
	Certs    []nodeDiagCertStatus    `json:"certs"`
	Version  string                  `json:"version"`
}

// panelFetchNodeDiag collects service status, TLS certificate status and proxyctl version
// from the node (via SSH for remote nodes, locally for primary).
func panelFetchNodeDiag(ctx context.Context, dbPath string, node domain.Node) (*nodeDiagResult, error) {
	// Get inbounds to derive cert domains.
	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	allInbounds, err := store.Inbounds().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list inbounds: %w", err)
	}

	// Collect cert domains using the same logic as caddy builder needsCaddyCert.
	certDomains := []string{}
	seenDomains := map[string]bool{}
	for _, ib := range allInbounds {
		if ib.NodeID != node.ID {
			continue
		}
		if !ib.TLSEnabled || ib.RealityEnabled || strings.TrimSpace(ib.TLSCertPath) != "" {
			continue
		}
		dom := strings.TrimSpace(ib.Domain)
		if dom == "" {
			dom = strings.TrimSpace(node.Host)
		}
		if dom != "" && !seenDomains[dom] {
			seenDomains[dom] = true
			certDomains = append(certDomains, dom)
		}
	}

	// Build a bash command that outputs tab-delimited lines.
	var sb strings.Builder
	sb.WriteString(`printf 'svc\tproxyctl-xray\t%s\n' "$(systemctl is-active proxyctl-xray 2>/dev/null || echo unknown)"; `)
	sb.WriteString(`printf 'svc\tproxyctl-sing-box\t%s\n' "$(systemctl is-active proxyctl-sing-box 2>/dev/null || echo unknown)"; `)
	sb.WriteString(`printf 'svc\tproxyctl-caddy\t%s\n' "$(systemctl is-active proxyctl-caddy 2>/dev/null || echo unknown)"; `)
	sb.WriteString(`printf 'ver\t%s\n' "$(proxyctl version 2>/dev/null || proxyctl --version 2>/dev/null || echo unknown)"; `)
	for _, dom := range certDomains {
		certPath := fmt.Sprintf("/caddy/certificates/acme-v02.api.letsencrypt.org-directory/%s/%s.crt", dom, dom)
		sb.WriteString(fmt.Sprintf(
			`printf 'cert\t%s\t%%s\n' "$(openssl x509 -noout -enddate -in %s 2>/dev/null | sed 's/notAfter=//' || echo MISSING)"; `,
			dom, certPath,
		))
	}
	bashCmd := sb.String()

	var out []byte
	if node.Role == domain.NodeRolePrimary {
		cmd := exec.CommandContext(ctx, "bash", "-c", bashCmd)
		out, _ = cmd.CombinedOutput()
	} else {
		settings, settErr := panelNodeSyncSettingsFromEnv()
		if settErr != nil {
			return nil, settErr
		}
		if node.SSHUser != "" {
			settings.opts.sshUser = node.SSHUser
		}
		if node.SSHPort > 0 {
			settings.opts.sshPort = node.SSHPort
		}
		host := strings.TrimSpace(node.Host)
		if host == "" {
			return nil, fmt.Errorf("node %q has empty host", node.ID)
		}
		target := fmt.Sprintf("%s@%s", settings.opts.sshUser, host)
		sshArgs := buildSSHArgs(settings.opts.sshPort, settings.opts.sshKeyPath, settings.opts.strictHostKey)
		sshArgs = append(sshArgs, target, bashCmd)
		var sshErr error
		out, sshErr = runRemoteExecCombined(ctx, "ssh", sshArgs, settings.opts.sshPassword)
		if sshErr != nil && len(out) == 0 {
			return nil, fmt.Errorf("ssh to %s: %w", host, sshErr)
		}
	}

	result := &nodeDiagResult{
		NodeName: node.Name,
		Services: []nodeDiagServiceStatus{},
		Certs:    []nodeDiagCertStatus{},
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 3)
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "svc":
			if len(parts) == 3 {
				result.Services = append(result.Services, nodeDiagServiceStatus{
					Name:   parts[1],
					Status: strings.TrimSpace(parts[2]),
				})
			}
		case "ver":
			result.Version = strings.TrimSpace(parts[1])
		case "cert":
			if len(parts) == 3 {
				expiry := strings.TrimSpace(parts[2])
				missing := expiry == "MISSING" || expiry == ""
				result.Certs = append(result.Certs, nodeDiagCertStatus{
					Domain:  parts[1],
					Expiry:  expiry,
					Missing: missing,
				})
			}
		}
	}
	return result, nil
}

// panelHandleNodeAction handles node CRUD and SSH operations.
// Returns (jobID, nodeID, error): jobID is non-empty when a background job was started.
func panelHandleNodeAction(ctx context.Context, dbPath string, r *http.Request, configPath, dbPathFlag *string, ops *panelOperationFeed) (string, string, error) {
	if err := r.ParseForm(); err != nil {
		return "", "", fmt.Errorf("invalid node action request")
	}
	action := strings.TrimSpace(r.FormValue("op"))
	if action == "" {
		return "", "", fmt.Errorf("node action is required")
	}

	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return "", "", err
	}
	defer store.Close()

	switch action {
	case "create":
		name := strings.TrimSpace(r.FormValue("name"))
		host := strings.TrimSpace(r.FormValue("host"))
		roleRaw := strings.ToLower(strings.TrimSpace(r.FormValue("role")))
		sshUser := strings.TrimSpace(r.FormValue("ssh_user"))
		sshPort := 22
		if p, pErr := strconv.Atoi(strings.TrimSpace(r.FormValue("ssh_port"))); pErr == nil && p > 0 && p < 65536 {
			sshPort = p
		}
		sshPassword := strings.TrimSpace(r.FormValue("ssh_password"))
		disableIPv6 := panelFormBool(r.FormValue("disable_ipv6"))
		blockPing := panelFormBool(r.FormValue("block_ping"))
		if name == "" {
			return "", "", fmt.Errorf("node name is required")
		}
		if host == "" {
			return "", "", fmt.Errorf("node host is required")
		}
		role := domain.NodeRoleNode
		if roleRaw == "" || roleRaw == string(domain.NodeRolePrimary) {
			role = domain.NodeRolePrimary
		}
		created, err := store.Nodes().Create(ctx, domain.Node{
			Name:        name,
			Host:        host,
			Role:        role,
			SSHUser:     sshUser,
			SSHPort:     sshPort,
			Enabled:     true,
			DisableIPv6: disableIPv6,
			BlockPing:   blockPing,
		})
		if err != nil {
			return "", "", fmt.Errorf("create node: %w", err)
		}
		if created.Role == domain.NodeRoleNode && created.Enabled {
			job := newNodeJob(created.ID, "bootstrap")
			cp := strings.TrimSpace(*configPath)
			go func() {
				// Install SSH key first (enables passwordless sync after bootstrap).
				if sshPassword != "" {
					if _, _, keyErr := panelInstallSSHKeyOnNode(context.Background(), created, sshPassword); keyErr != nil {
						job.finish(false, fmt.Sprintf("node created (%s), ssh key setup failed: %v", created.Name, keyErr))
						return
					}
				}
				if bootErr := panelEnsureProxyctlOnNode(context.Background(), cp, created, sshPassword); bootErr != nil {
					job.finish(false, fmt.Sprintf("node created (%s), bootstrap failed: %v", created.Name, bootErr))
					return
				}
				if synced, cleaned, syncErr := panelSyncWorkerNodesByIDsWithPassword(context.Background(), dbPath, cp, []string{created.ID}, sshPassword); syncErr != nil {
					job.finish(false, fmt.Sprintf("node created (%s), bootstrap ok, sync failed: %v", created.Name, syncErr))
					return
				} else if synced > 0 || cleaned > 0 {
					job.finish(true, fmt.Sprintf("node created: %s (%s) | ssh key ok | bootstrap ok | synced=%d cleaned=%d", created.Name, created.Host, synced, cleaned))
					return
				}
				job.finish(true, fmt.Sprintf("node created: %s (%s) | ssh key ok | bootstrap ok", created.Name, created.Host))
			}()
			ops.set("ok", fmt.Sprintf("node created: %s — bootstrap running in background", created.Name))
			return job.id, created.ID, nil
		}
		ops.set("ok", fmt.Sprintf("node created: %s (%s)", created.Name, created.Host))
		return "", created.ID, nil

	case "install_ssh_key", "bootstrap", "apply_hardening", "test", "sync", "set_enabled", "update", "delete":
		nodeID := strings.TrimSpace(r.FormValue("node_id"))
		version := strings.TrimSpace(r.FormValue("version"))
		if nodeID == "" {
			return "", "", fmt.Errorf("node id is required")
		}
		nodes, err := store.Nodes().List(ctx)
		if err != nil {
			return "", "", fmt.Errorf("list nodes: %w", err)
		}
		var current domain.Node
		found := false
		for _, node := range nodes {
			if node.ID == nodeID {
				current = node
				found = true
				break
			}
		}
		if !found {
			return "", "", fmt.Errorf("node %q not found", nodeID)
		}
		if version != panelNodeVersion(current) {
			return "", "", fmt.Errorf("node %q changed since page load; refresh and retry", nodeID)
		}
		if action == "install_ssh_key" {
			if current.Role == domain.NodeRolePrimary {
				ops.set("ok", fmt.Sprintf("ssh key setup skipped: %s (%s) is primary", current.Name, current.Host))
				return "", "", nil
			}
			sshPassword := strings.TrimSpace(r.FormValue("ssh_password"))
			job := newNodeJob(current.ID, "install_ssh_key")
			snapNode := current
			go func() {
				generated, keyPath, keyErr := panelInstallSSHKeyOnNode(context.Background(), snapNode, sshPassword)
				if keyErr != nil {
					job.finish(false, fmt.Sprintf("ssh key setup failed: %s (%s) | %v", snapNode.Name, snapNode.Host, keyErr))
					return
				}
				extra := ""
				if generated {
					extra = " | key generated: " + keyPath
				}
				job.finish(true, fmt.Sprintf("ssh key setup ok: %s (%s)%s", snapNode.Name, snapNode.Host, extra))
			}()
			ops.set("ok", fmt.Sprintf("ssh key setup started for %s — running in background", current.Name))
			return job.id, "", nil
		}
		if action == "apply_hardening" {
			// Prefer form-supplied flags (from current checkbox state) so the
			// user doesn't need to Save before applying hardening.
			snapNode := current
			if r.FormValue("disable_ipv6") != "" {
				snapNode.DisableIPv6 = panelFormBool(r.FormValue("disable_ipv6"))
			}
			if r.FormValue("block_ping") != "" {
				snapNode.BlockPing = panelFormBool(r.FormValue("block_ping"))
			}
			if !snapNode.DisableIPv6 && !snapNode.BlockPing {
				ops.set("ok", fmt.Sprintf("hardening skipped: no options enabled for %s — enable Disable IPv6 or Block Ping in node settings first", current.Name))
				return "", "", nil
			}
			sshPassword := strings.TrimSpace(r.FormValue("ssh_password"))
			job := newNodeJob(current.ID, "apply_hardening")
			go func() {
				var applyErr error
				if snapNode.Role == domain.NodeRolePrimary {
					applyErr = panelApplyLocalHardening(context.Background(), snapNode)
				} else {
					applyErr = panelApplyNodeHardening(context.Background(), snapNode, sshPassword)
				}
				if applyErr != nil {
					job.finish(false, fmt.Sprintf("hardening failed: %s (%s) | %v", snapNode.Name, snapNode.Host, applyErr))
					return
				}
				job.finish(true, fmt.Sprintf("hardening applied: %s (%s)", snapNode.Name, snapNode.Host))
			}()
			ops.set("ok", fmt.Sprintf("hardening started for %s — running in background", current.Name))
			return job.id, "", nil
		}
		if action == "bootstrap" {
			if current.Role == domain.NodeRolePrimary {
				ops.set("ok", fmt.Sprintf("node bootstrap skipped: %s (%s) is primary", current.Name, current.Host))
				return "", "", nil
			}
			sshPassword := strings.TrimSpace(r.FormValue("ssh_password"))
			job := newNodeJob(current.ID, "bootstrap")
			snapNode := current
			cp := strings.TrimSpace(*configPath)
			go func() {
				if bootErr := panelEnsureProxyctlOnNode(context.Background(), cp, snapNode, sshPassword); bootErr != nil {
					job.finish(false, fmt.Sprintf("bootstrap failed: %s (%s) | %v", snapNode.Name, snapNode.Host, bootErr))
					return
				}
				if testErr := panelTestNodeConnectivityWithPassword(context.Background(), snapNode, sshPassword); testErr != nil {
					job.finish(false, fmt.Sprintf("bootstrap ok, but test failed: %s (%s) | %v", snapNode.Name, snapNode.Host, testErr))
					return
				}
				if synced, cleaned, syncErr := panelSyncWorkerNodesByIDsWithPassword(context.Background(), dbPath, cp, []string{snapNode.ID}, sshPassword); syncErr != nil {
					job.finish(false, fmt.Sprintf("bootstrap ok (%s), sync failed: %v", snapNode.Name, syncErr))
					return
				} else if synced > 0 || cleaned > 0 {
					job.finish(true, fmt.Sprintf("bootstrap ok: %s (%s) | synced=%d cleaned=%d", snapNode.Name, snapNode.Host, synced, cleaned))
					return
				}
				job.finish(true, fmt.Sprintf("bootstrap ok: %s (%s)", snapNode.Name, snapNode.Host))
			}()
			ops.set("ok", fmt.Sprintf("bootstrap started for %s — running in background", current.Name))
			return job.id, "", nil
		}
		if action == "test" {
			if current.Role == domain.NodeRolePrimary {
				setNodeSyncStatus(current.ID, true, "primary: local check ok")
				ops.set("ok", fmt.Sprintf("node test ok: %s (%s) | primary node: local control-plane check (ssh skipped)", current.Name, current.Host))
				return "", "", nil
			}
			if testErr := panelTestNodeConnectivityWithPassword(ctx, current, ""); testErr != nil {
				msg := strings.ToLower(strings.TrimSpace(testErr.Error()))
				if strings.Contains(msg, "proxyctl is not installed") {
					if bootErr := panelEnsureProxyctlOnNode(ctx, strings.TrimSpace(*configPath), current, ""); bootErr != nil {
						setNodeSyncStatus(current.ID, false, fmt.Sprintf("proxyctl missing, bootstrap failed: %v", bootErr))
						ops.set("error", fmt.Sprintf("node test failed: %s (%s) | proxyctl missing and bootstrap failed: %v", current.Name, current.Host, bootErr))
						return "", "", nil
					}
					if recheckErr := panelTestNodeConnectivityWithPassword(ctx, current, ""); recheckErr != nil {
						setNodeSyncStatus(current.ID, false, fmt.Sprintf("recheck after bootstrap failed: %v", recheckErr))
						ops.set("error", fmt.Sprintf("node test failed after bootstrap: %s (%s) | %v", current.Name, current.Host, recheckErr))
						return "", "", nil
					}
					if synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{current.ID}); syncErr != nil {
						setNodeSyncStatus(current.ID, false, fmt.Sprintf("bootstrap ok, sync failed: %v", syncErr))
						ops.set("error", fmt.Sprintf("node test ok after bootstrap (%s), but node sync failed: %v", current.ID, syncErr))
						return "", "", nil
					} else if synced > 0 || cleaned > 0 {
						setNodeSyncStatus(current.ID, true, fmt.Sprintf("bootstrap completed, sync: synced=%d cleaned=%d", synced, cleaned))
						ops.set("ok", fmt.Sprintf("node test ok: %s (%s) | proxyctl bootstrap completed | node sync: synced=%d cleaned=%d", current.Name, current.Host, synced, cleaned))
						return "", "", nil
					}
					setNodeSyncStatus(current.ID, true, "bootstrap completed")
					ops.set("ok", fmt.Sprintf("node test ok: %s (%s) | proxyctl bootstrap completed", current.Name, current.Host))
					return "", "", nil
				}
				setNodeSyncStatus(current.ID, false, testErr.Error())
				ops.set("error", fmt.Sprintf("node test failed: %s (%s) | %v", current.Name, current.Host, testErr))
				return "", "", nil
			}
			setNodeSyncStatus(current.ID, true, "connectivity ok")
			ops.set("ok", fmt.Sprintf("node test ok: %s (%s)", current.Name, current.Host))
			return "", "", nil
		}

		if action == "sync" {
			cp := strings.TrimSpace(*configPath)
			synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, cp, []string{current.ID})
			if syncErr != nil {
				ops.set("error", fmt.Sprintf("sync failed: %s (%s) | %v", current.Name, current.Host, syncErr))
				return "", "", nil
			}
			ops.set("ok", fmt.Sprintf("sync ok: %s (%s) | synced=%d cleaned=%d", current.Name, current.Host, synced, cleaned))
			return "", "", nil
		}

		if action == "delete" {
			cleanupWarning := ""
			if current.Role == domain.NodeRoleNode {
				if cleanupErr := panelUninstallNodeRuntime(ctx, strings.TrimSpace(*configPath), current); cleanupErr != nil {
					cleanupWarning = cleanupErr.Error()
				}
			}
			deleted, err := store.Nodes().Delete(ctx, nodeID)
			if err != nil {
				return "", "", fmt.Errorf("delete node: %w", err)
			}
			if !deleted {
				return "", "", fmt.Errorf("node %q not found", nodeID)
			}
			out, refreshErr := panelRefreshAllSubscriptions(ctx, configPath, dbPathFlag)
			if refreshErr != nil {
				if cleanupWarning != "" {
					ops.set("error", fmt.Sprintf("node deleted (%s), but remote cleanup failed: %s; subscription refresh failed: %s", nodeID, cleanupWarning, panelErrWithOutput(refreshErr, out)))
					return "", "", nil
				}
				ops.set("error", fmt.Sprintf("node deleted (%s), but subscription refresh failed: %s", nodeID, panelErrWithOutput(refreshErr, out)))
				return "", "", nil
			}
			if current.Role == domain.NodeRoleNode {
				if cleanupWarning != "" {
					ops.set("ok", fmt.Sprintf("node deleted: %s | warning: remote runtime cleanup failed: %s | subscriptions refreshed: %s", nodeID, cleanupWarning, panelSummarizeOutput(out)))
					return "", "", nil
				}
				ops.set("ok", fmt.Sprintf("node deleted: %s | remote runtime cleaned | subscriptions refreshed: %s", nodeID, panelSummarizeOutput(out)))
				return "", "", nil
			}
			ops.set("ok", fmt.Sprintf("node deleted: %s | subscriptions refreshed: %s", nodeID, panelSummarizeOutput(out)))
			return "", "", nil
		}
		if action == "update" {
			prevRole := current.Role
			name := strings.TrimSpace(r.FormValue("name"))
			host := strings.TrimSpace(r.FormValue("host"))
			roleRaw := strings.ToLower(strings.TrimSpace(r.FormValue("role")))
			sshUser := strings.TrimSpace(r.FormValue("ssh_user"))
			sshPort := current.SSHPort
			if p, pErr := strconv.Atoi(strings.TrimSpace(r.FormValue("ssh_port"))); pErr == nil && p > 0 && p < 65536 {
				sshPort = p
			}
			if name == "" {
				return "", "", fmt.Errorf("node name is required")
			}
			if host == "" {
				return "", "", fmt.Errorf("node host is required")
			}
			role := domain.NodeRoleNode
			if roleRaw == "" || roleRaw == string(domain.NodeRolePrimary) {
				role = domain.NodeRolePrimary
			}
			current.Name = name
			current.Host = host
			current.Role = role
			current.SSHUser = sshUser
			current.SSHPort = sshPort
			current.DisableIPv6 = panelFormBool(r.FormValue("disable_ipv6"))
			current.BlockPing = panelFormBool(r.FormValue("block_ping"))
			updated, err := store.Nodes().Update(ctx, current)
			if err != nil {
				return "", "", fmt.Errorf("update node: %w", err)
			}
			if updated.Role == domain.NodeRoleNode && updated.Enabled {
				if bootErr := panelEnsureProxyctlOnNode(ctx, strings.TrimSpace(*configPath), updated, ""); bootErr != nil {
					ops.set("error", fmt.Sprintf("node updated (%s), but remote bootstrap failed: %v", updated.ID, bootErr))
					return "", "", nil
				}
				if synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{updated.ID}); syncErr != nil {
					ops.set("error", fmt.Sprintf("node updated (%s), but node sync failed: %v", updated.ID, syncErr))
					return "", "", nil
				} else if synced > 0 || cleaned > 0 {
					ops.set("ok", fmt.Sprintf("node updated: %s (%s) | node sync: synced=%d cleaned=%d", updated.Name, updated.Host, synced, cleaned))
					return "", "", nil
				}
			}
			if prevRole == domain.NodeRoleNode && (updated.Role != domain.NodeRoleNode || !updated.Enabled) {
				if cleanupErr := panelCleanupNodeRuntime(ctx, strings.TrimSpace(*configPath), updated); cleanupErr != nil {
					ops.set("error", fmt.Sprintf("node updated (%s), but runtime cleanup failed: %v", updated.ID, cleanupErr))
					return "", "", nil
				}
				ops.set("ok", fmt.Sprintf("node updated: %s (%s) | remote runtime cleaned", updated.Name, updated.Host))
				return "", "", nil
			}
			ops.set("ok", fmt.Sprintf("node updated: %s (%s)", updated.Name, updated.Host))
			return "", "", nil
		}

		current.Enabled = panelFormBool(r.FormValue("enabled"))
		updated, err := store.Nodes().Update(ctx, current)
		if err != nil {
			return "", "", fmt.Errorf("set node enabled: %w", err)
		}
		if updated.Enabled && updated.Role == domain.NodeRoleNode {
			if bootErr := panelEnsureProxyctlOnNode(ctx, strings.TrimSpace(*configPath), updated, ""); bootErr != nil {
				ops.set("error", fmt.Sprintf("node enabled (%s), but remote bootstrap failed: %v", updated.ID, bootErr))
				return "", "", nil
			}
			if synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{updated.ID}); syncErr != nil {
				ops.set("error", fmt.Sprintf("node enabled (%s), but node sync failed: %v", updated.ID, syncErr))
				return "", "", nil
			} else if synced > 0 || cleaned > 0 {
				ops.set("ok", fmt.Sprintf("node enabled: %s | node sync: synced=%d cleaned=%d", updated.Name, synced, cleaned))
				return "", "", nil
			}
		}
		if !updated.Enabled && updated.Role == domain.NodeRoleNode {
			if cleanupErr := panelCleanupNodeRuntime(ctx, strings.TrimSpace(*configPath), updated); cleanupErr != nil {
				ops.set("error", fmt.Sprintf("node disabled (%s), but runtime cleanup failed: %v", updated.ID, cleanupErr))
				return "", "", nil
			}
			ops.set("ok", fmt.Sprintf("node disabled: %s | remote runtime cleaned", updated.Name))
			return "", "", nil
		}
		state := "disabled"
		if updated.Enabled {
			state = "enabled"
		}
		ops.set("ok", fmt.Sprintf("node %s: %s", state, updated.Name))
		return "", "", nil
	case "fetch_version":
		nodeID := strings.TrimSpace(r.FormValue("node_id"))
		if nodeID == "" {
			return "", "", fmt.Errorf("node id is required")
		}
		nodes, err := store.Nodes().List(ctx)
		if err != nil {
			return "", "", fmt.Errorf("list nodes: %w", err)
		}
		var current domain.Node
		found := false
		for _, node := range nodes {
			if node.ID == nodeID {
				current = node
				found = true
				break
			}
		}
		if !found {
			return "", "", fmt.Errorf("node %q not found", nodeID)
		}
		if current.Role == domain.NodeRolePrimary {
			panelNodeVersionCache.Store(current.ID, Version)
			ops.set("ok", fmt.Sprintf("version on %s: %s (primary, local)", current.Name, Version))
			return "", "", nil
		}
		job := newNodeJob(current.ID, "fetch_version")
		snapNode := current
		go func() {
			ver, fetchErr := panelFetchNodeRemoteVersion(context.Background(), snapNode)
			if fetchErr != nil {
				job.finish(false, fmt.Sprintf("version check failed: %s (%s) | %v", snapNode.Name, snapNode.Host, fetchErr))
				return
			}
			job.finish(true, fmt.Sprintf("version on %s: %s", snapNode.Name, ver))
		}()
		ops.set("ok", fmt.Sprintf("fetching version from %s…", current.Name))
		return job.id, "", nil

	case "update_proxyctl":
		nodeID := strings.TrimSpace(r.FormValue("node_id"))
		if nodeID == "" {
			return "", "", fmt.Errorf("node id is required")
		}
		nodes, err := store.Nodes().List(ctx)
		if err != nil {
			return "", "", fmt.Errorf("list nodes: %w", err)
		}
		var current domain.Node
		found := false
		for _, node := range nodes {
			if node.ID == nodeID {
				current = node
				found = true
				break
			}
		}
		if !found {
			return "", "", fmt.Errorf("node %q not found", nodeID)
		}
		if current.Role == domain.NodeRolePrimary {
			return "", "", fmt.Errorf("use panel self-update for the primary node")
		}
		sshPassword := strings.TrimSpace(r.FormValue("ssh_password"))
		job := newNodeJob(current.ID, "update_proxyctl")
		snapNode := current
		cp := strings.TrimSpace(*configPath)
		go func() {
			if updateErr := panelUpdateProxyctlOnNode(context.Background(), cp, snapNode, sshPassword); updateErr != nil {
				job.finish(false, fmt.Sprintf("update failed: %s (%s) | %v", snapNode.Name, snapNode.Host, updateErr))
				return
			}
			// After update, sync the node.
			if synced, _, syncErr := panelSyncWorkerNodesByIDsWithPassword(context.Background(), dbPath, cp, []string{snapNode.ID}, sshPassword); syncErr != nil {
				job.finish(true, fmt.Sprintf("proxyctl updated on %s | sync warning: %v", snapNode.Name, syncErr))
				return
			} else if synced > 0 {
				job.finish(true, fmt.Sprintf("proxyctl updated on %s | synced", snapNode.Name))
				return
			}
			job.finish(true, fmt.Sprintf("proxyctl updated on %s", snapNode.Name))
		}()
		ops.set("ok", fmt.Sprintf("updating proxyctl on %s — running in background", current.Name))
		return job.id, "", nil

	case "fetch_node_info":
		nodeID := strings.TrimSpace(r.FormValue("node_id"))
		if nodeID == "" {
			return "", "", fmt.Errorf("node id is required")
		}
		nodes, err := store.Nodes().List(ctx)
		if err != nil {
			return "", "", fmt.Errorf("list nodes: %w", err)
		}
		var current domain.Node
		found := false
		for _, node := range nodes {
			if node.ID == nodeID {
				current = node
				found = true
				break
			}
		}
		if !found {
			return "", "", fmt.Errorf("node %q not found", nodeID)
		}
		job := newNodeJob(current.ID, "fetch_node_info")
		snapNode := current
		go func() {
			diagResult, diagErr := panelFetchNodeDiag(context.Background(), dbPath, snapNode)
			if diagErr != nil {
				job.finish(false, fmt.Sprintf("diagnostics failed: %s (%s) | %v", snapNode.Name, snapNode.Host, diagErr))
				return
			}
			jsonBytes, jsonErr := json.Marshal(diagResult)
			if jsonErr != nil {
				job.finish(false, fmt.Sprintf("marshal diagnostics: %v", jsonErr))
				return
			}
			job.finish(true, string(jsonBytes))
		}()
		ops.set("ok", fmt.Sprintf("fetching diagnostics for %s…", current.Name))
		return job.id, "", nil

	default:
		return "", "", fmt.Errorf("unknown node action")
	}
}

func panelHandleCredentialAction(ctx context.Context, dbPath string, r *http.Request, configPath, dbPathFlag *string, ops *panelOperationFeed) error {
	action := strings.TrimSpace(r.FormValue("op"))
	if action == "" {
		return fmt.Errorf("credential action is required")
	}

	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	switch action {
	case "attach_credential":
		userID := strings.TrimSpace(r.FormValue("user_id"))
		inboundID := strings.TrimSpace(r.FormValue("inbound_id"))
		label := strings.TrimSpace(r.FormValue("label"))
		if userID == "" || inboundID == "" {
			return fmt.Errorf("user and inbound are required")
		}

		users, err := store.Users().List(ctx)
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		var user domain.User
		foundUser := false
		for _, u := range users {
			if u.ID == userID {
				user = u
				foundUser = true
				break
			}
		}
		if !foundUser {
			return fmt.Errorf("user %q not found", userID)
		}

		inbounds, err := store.Inbounds().List(ctx)
		if err != nil {
			return fmt.Errorf("list inbounds: %w", err)
		}
		var inbound domain.Inbound
		foundInbound := false
		for _, in := range inbounds {
			if in.ID == inboundID {
				inbound = in
				foundInbound = true
				break
			}
		}
		if !foundInbound {
			return fmt.Errorf("inbound %q not found", inboundID)
		}

		credentials, err := store.Credentials().List(ctx)
		if err != nil {
			return fmt.Errorf("list credentials: %w", err)
		}
		if existing, ok := findCredentialByUserAndInbound(credentials, userID, inboundID); ok {
			if label != "" {
				updated, updateErr := store.Credentials().Update(ctx, domain.Credential{
					ID:        existing.ID,
					UserID:    existing.UserID,
					InboundID: existing.InboundID,
					Kind:      existing.Kind,
					Secret:    existing.Secret,
					Metadata:  setCredentialLabelMetadata(existing.Metadata, label),
				})
				if updateErr != nil {
					return fmt.Errorf("update credential label: %w", updateErr)
				}
				out, runErr := panelExecuteCommand(ctx, newSubscriptionGenerateCmd(configPath, dbPathFlag), []string{userID})
				if runErr != nil {
					ops.set("error", fmt.Sprintf("credential label updated (%s), but subscription generate failed: %s", updated.ID, panelErrWithOutput(runErr, out)))
					return nil
				}
				ops.set("ok", fmt.Sprintf("credential label updated: %s | %s", updated.ID, panelSummarizeOutput(out)))
				return nil
			}
			ops.set("ok", fmt.Sprintf("credential already attached: %s (%s)", existing.ID, existing.Kind))
			return nil
		}

		if label == "" {
			label = user.Name
		}
		credential, err := createCredentialForInbound(inbound, userID, label)
		if err != nil {
			return err
		}
		created, err := store.Credentials().Create(ctx, credential)
		if err != nil {
			return fmt.Errorf("create credential: %w", err)
		}

		synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{inbound.NodeID})
		out, runErr := panelExecuteCommand(ctx, newSubscriptionGenerateCmd(configPath, dbPathFlag), []string{userID})
		if syncErr != nil && runErr != nil {
			ops.set("error", fmt.Sprintf("credential created (%s), but node sync failed: %v; subscription generate failed: %s", created.ID, syncErr, panelErrWithOutput(runErr, out)))
			return nil
		}
		if syncErr != nil {
			ops.set("error", fmt.Sprintf("credential attached: %s (%s) | %s | node sync failed: %v", created.ID, created.Kind, panelSummarizeOutput(out), syncErr))
			return nil
		}
		if runErr != nil {
			ops.set("error", fmt.Sprintf("credential created (%s), but subscription generate failed: %s", created.ID, panelErrWithOutput(runErr, out)))
			return nil
		}
		if synced > 0 || cleaned > 0 {
			ops.set("ok", fmt.Sprintf("credential attached: %s (%s) | node sync: synced=%d cleaned=%d | %s", created.ID, created.Kind, synced, cleaned, panelSummarizeOutput(out)))
			return nil
		}
		ops.set("ok", fmt.Sprintf("credential attached: %s (%s) | %s", created.ID, created.Kind, panelSummarizeOutput(out)))
		return nil

	case "delete_credential":
		credentialID := strings.TrimSpace(r.FormValue("credential_id"))
		userID := strings.TrimSpace(r.FormValue("user_id"))
		version := strings.TrimSpace(r.FormValue("version"))
		if credentialID == "" {
			return fmt.Errorf("credential id is required")
		}

		credentials, err := store.Credentials().List(ctx)
		if err != nil {
			return fmt.Errorf("list credentials: %w", err)
		}
		var current domain.Credential
		found := false
		for _, cred := range credentials {
			if cred.ID == credentialID {
				current = cred
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("credential %q not found", credentialID)
		}
		if userID == "" {
			userID = current.UserID
		}
		if version != panelCredentialVersion(current) {
			return fmt.Errorf("credential %q changed since page load; refresh and retry", credentialID)
		}

		deleted, err := store.Credentials().Delete(ctx, credentialID)
		if err != nil {
			return fmt.Errorf("delete credential: %w", err)
		}
		if !deleted {
			return fmt.Errorf("credential %q not found", credentialID)
		}

		// Look up nodeID via the inbound (store is still open, inbound was not deleted).
		nodeIDForSync := ""
		if inbounds2, listErr := store.Inbounds().List(ctx); listErr == nil {
			for _, ib := range inbounds2 {
				if ib.ID == current.InboundID {
					nodeIDForSync = ib.NodeID
					break
				}
			}
		}
		synced, cleaned, syncErr := 0, 0, error(nil)
		if nodeIDForSync != "" {
			synced, cleaned, syncErr = panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{nodeIDForSync})
		}
		if strings.TrimSpace(userID) == "" {
			if syncErr != nil {
				ops.set("error", fmt.Sprintf("credential detached: %s | node sync failed: %v", credentialID, syncErr))
			} else {
				ops.set("ok", fmt.Sprintf("credential detached: %s | node sync: synced=%d cleaned=%d", credentialID, synced, cleaned))
			}
			return nil
		}
		out, runErr := panelExecuteCommand(ctx, newSubscriptionGenerateCmd(configPath, dbPathFlag), []string{userID})
		if syncErr != nil && runErr != nil {
			ops.set("error", fmt.Sprintf("credential detached (%s), but node sync failed: %v; subscription generate failed: %s", credentialID, syncErr, panelErrWithOutput(runErr, out)))
			return nil
		}
		if syncErr != nil {
			ops.set("error", fmt.Sprintf("credential detached: %s | %s | node sync failed: %v", credentialID, panelSummarizeOutput(out), syncErr))
			return nil
		}
		if runErr != nil {
			ops.set("error", fmt.Sprintf("credential detached (%s), but subscription generate failed: %s", credentialID, panelErrWithOutput(runErr, out)))
			return nil
		}
		if synced > 0 || cleaned > 0 {
			ops.set("ok", fmt.Sprintf("credential detached: %s | node sync: synced=%d cleaned=%d | %s", credentialID, synced, cleaned, panelSummarizeOutput(out)))
			return nil
		}
		ops.set("ok", fmt.Sprintf("credential detached: %s | %s", credentialID, panelSummarizeOutput(out)))
		return nil
	case "update_credential":
		credentialID := strings.TrimSpace(r.FormValue("credential_id"))
		label := strings.TrimSpace(r.FormValue("label"))
		version := strings.TrimSpace(r.FormValue("version"))
		if credentialID == "" {
			return fmt.Errorf("credential id is required")
		}

		credentials, err := store.Credentials().List(ctx)
		if err != nil {
			return fmt.Errorf("list credentials: %w", err)
		}
		var current domain.Credential
		found := false
		for _, cred := range credentials {
			if cred.ID == credentialID {
				current = cred
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("credential %q not found", credentialID)
		}
		if version != panelCredentialVersion(current) {
			return fmt.Errorf("credential %q changed since page load; refresh and retry", credentialID)
		}

		// Build updated metadata with new label.
		var meta struct {
			Label string `json:"label"`
		}
		if strings.TrimSpace(current.Metadata) != "" {
			_ = json.Unmarshal([]byte(current.Metadata), &meta)
		}
		meta.Label = label
		metaBytes, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("marshal metadata: %w", err)
		}
		current.Metadata = string(metaBytes)

		if _, err := store.Credentials().Update(ctx, current); err != nil {
			return fmt.Errorf("update credential: %w", err)
		}

		// Refresh subscription for the credential owner.
		userID := strings.TrimSpace(current.UserID)
		if userID != "" {
			out, runErr := panelExecuteCommand(ctx, newSubscriptionGenerateCmd(configPath, dbPathFlag), []string{userID})
			if runErr != nil {
				ops.set("error", fmt.Sprintf("label updated, but subscription generate failed: %s", panelErrWithOutput(runErr, out)))
				return nil
			}
			ops.set("ok", fmt.Sprintf("label updated | %s", panelSummarizeOutput(out)))
		} else {
			ops.set("ok", "label updated")
		}
		return nil

	default:
		return fmt.Errorf("unknown credential action")
	}
}

func panelHandleSettingsAction(r *http.Request, configPath *string, ops *panelOperationFeed) error {
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("invalid settings action request")
	}
	action := strings.TrimSpace(r.FormValue("op"))
	if action == "" {
		return fmt.Errorf("settings action is required")
	}
	switch action {
	case "set_acme_email":
		if configPath == nil || strings.TrimSpace(*configPath) == "" {
			return fmt.Errorf("config path is required")
		}
		email := strings.TrimSpace(r.FormValue("email"))
		if err := setConfigContactEmail(strings.TrimSpace(*configPath), email); err != nil {
			return err
		}
		if email == "" {
			ops.set("ok", "acme contact email cleared")
		} else {
			ops.set("ok", "acme contact email saved: "+email)
		}
		return nil
	case "set_traffic_collection":
		enabled := panelFormBool(r.FormValue("enabled"))
		panelTrafficCollection.Store(enabled)
		if configPath != nil && strings.TrimSpace(*configPath) != "" {
			if err := setConfigTrafficCollectionEnabled(strings.TrimSpace(*configPath), enabled); err != nil {
				return err
			}
		}
		if enabled {
			ops.set("ok", "traffic stats collection enabled")
		} else {
			ops.set("ok", "traffic stats collection disabled")
		}
		return nil
	default:
		return fmt.Errorf("unknown settings action")
	}
}

func panelInboundFromForm(r *http.Request, base domain.Inbound) (domain.Inbound, error) {
	typeRaw := strings.ToLower(strings.TrimSpace(r.FormValue("type")))
	transport := strings.ToLower(strings.TrimSpace(r.FormValue("transport")))
	nodeID := strings.TrimSpace(r.FormValue("node_id"))
	domainName := strings.TrimSpace(r.FormValue("domain"))
	sni := strings.TrimSpace(r.FormValue("sni"))
	path := strings.TrimSpace(r.FormValue("path"))
	engineRaw := strings.ToLower(strings.TrimSpace(r.FormValue("engine")))
	securityMode := strings.ToLower(strings.TrimSpace(r.FormValue("security_mode")))
	if securityMode == "" {
		securityMode = "none"
	}

	if typeRaw == "" {
		return domain.Inbound{}, fmt.Errorf("inbound type is required")
	}
	if transport == "" {
		return domain.Inbound{}, fmt.Errorf("transport is required")
	}
	if nodeID == "" {
		return domain.Inbound{}, fmt.Errorf("node is required")
	}
	if domainName == "" {
		return domain.Inbound{}, fmt.Errorf("domain is required")
	}

	port, err := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	if err != nil || port < 1 || port > 65535 {
		return domain.Inbound{}, fmt.Errorf("port must be in range 1..65535")
	}
	if port == 443 && !panelFormBool(r.FormValue("allow_port_443")) {
		return domain.Inbound{}, fmt.Errorf("port 443 is reserved by default")
	}

	resolvedEngine, err := engine.Resolve(engine.ResolutionRequest{
		Protocol:        domain.Protocol(typeRaw),
		Transport:       transport,
		PreferredEngine: domain.Engine(engineRaw),
	})
	if err != nil {
		return domain.Inbound{}, err
	}

	base.Type = domain.Protocol(typeRaw)
	base.Engine = resolvedEngine
	base.Transport = transport
	base.NodeID = nodeID
	base.Domain = domainName
	base.Port = port
	base.TLSEnabled = panelFormBool(r.FormValue("tls"))
	base.RealityEnabled = false
	base.RealityPublicKey = ""
	base.RealityPrivateKey = ""
	base.RealityShortID = ""
	base.RealityFingerprint = ""
	base.RealitySpiderX = ""
	base.RealityServer = ""
	base.RealityServerPort = 0
	base.VLESSFlow = ""
	switch securityMode {
	case "none":
		base.TLSEnabled = false
	case "tls":
		base.TLSEnabled = true
	case "reality":
		if base.Type != domain.ProtocolVLESS || strings.ToLower(strings.TrimSpace(base.Transport)) != "tcp" || strings.ToLower(string(base.Engine)) != string(domain.EngineXray) {
			return domain.Inbound{}, fmt.Errorf("reality requires type=vless transport=tcp engine=xray")
		}
		base.SelfSteal = r.FormValue("self_steal") == "1"
		base.TLSEnabled = false
		base.RealityEnabled = true
		base.RealityPublicKey = strings.TrimSpace(r.FormValue("reality_public_key"))
		base.RealityPrivateKey = strings.TrimSpace(r.FormValue("reality_private_key"))
		base.RealityShortID = strings.TrimSpace(r.FormValue("reality_short_id"))
		base.RealityFingerprint = strings.TrimSpace(r.FormValue("reality_fingerprint"))
		base.RealitySpiderX = strings.TrimSpace(r.FormValue("reality_spider_x"))
		base.VLESSFlow = strings.TrimSpace(r.FormValue("vless_flow"))
		if base.SelfSteal {
			base.RealityServer = ""
			base.RealityServerPort = 0
		} else {
			serverPort, portErr := strconv.Atoi(strings.TrimSpace(r.FormValue("reality_server_port")))
			if portErr != nil || serverPort < 1 || serverPort > 65535 {
				return domain.Inbound{}, fmt.Errorf("reality server port must be in range 1..65535")
			}
			base.RealityServer = strings.TrimSpace(r.FormValue("reality_server"))
			base.RealityServerPort = serverPort
			if base.RealityServer == "" {
				return domain.Inbound{}, fmt.Errorf("reality requires target host when self steal is disabled")
			}
		}
		if base.RealityPublicKey == "" || base.RealityPrivateKey == "" {
			return domain.Inbound{}, fmt.Errorf("reality requires public key and private key")
		}
		if base.RealityFingerprint == "" {
			base.RealityFingerprint = "chrome"
		}
		if base.VLESSFlow == "" {
			base.VLESSFlow = "xtls-rprx-vision"
		}
	default:
		return domain.Inbound{}, fmt.Errorf("unknown security mode %q", securityMode)
	}
	if base.Type == domain.ProtocolHysteria2 {
		base.TLSEnabled = true
	}
	base.Path = path
	base.SNI = sni
	base.SniffingEnabled = panelFormBool(r.FormValue("sniffing_enabled"))
	base.SniffingHTTP = panelFormBool(r.FormValue("sniffing_http"))
	base.SniffingTLS = panelFormBool(r.FormValue("sniffing_tls"))
	base.SniffingQUIC = panelFormBool(r.FormValue("sniffing_quic"))
	base.SniffingFakeDNS = panelFormBool(r.FormValue("sniffing_fakedns"))
	base.Enabled = panelFormBool(r.FormValue("enabled"))
	return base, nil
}

func panelFormBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

func panelUserVersion(user domain.User) string {
	expiresStr := ""
	if user.ExpiresAt != nil {
		expiresStr = user.ExpiresAt.UTC().Format(time.RFC3339)
	}
	s := strings.Join([]string{
		strings.TrimSpace(user.ID),
		strings.TrimSpace(user.Name),
		strconv.FormatBool(user.Enabled),
		user.CreatedAt.UTC().Format(time.RFC3339Nano),
		expiresStr,
		strconv.FormatInt(user.TrafficLimitBytes, 10),
	}, "|")
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func panelNodeVersion(node domain.Node) string {
	s := strings.Join([]string{
		strings.TrimSpace(node.ID),
		strings.TrimSpace(node.Name),
		strings.TrimSpace(node.Host),
		strings.TrimSpace(string(node.Role)),
		strings.TrimSpace(node.SSHUser),
		strconv.Itoa(node.SSHPort),
		strconv.FormatBool(node.Enabled),
		strconv.FormatBool(node.DisableIPv6),
		strconv.FormatBool(node.BlockPing),
		node.CreatedAt.UTC().Format(time.RFC3339Nano),
	}, "|")
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func panelInboundVersion(inbound domain.Inbound) string {
	s := strings.Join([]string{
		strings.TrimSpace(inbound.ID),
		strings.TrimSpace(string(inbound.Type)),
		strings.TrimSpace(string(inbound.Engine)),
		strings.TrimSpace(inbound.NodeID),
		strings.TrimSpace(inbound.Domain),
		strconv.Itoa(inbound.Port),
		strconv.FormatBool(inbound.TLSEnabled),
		strings.TrimSpace(inbound.Transport),
		strings.TrimSpace(inbound.Path),
		strings.TrimSpace(inbound.SNI),
		strconv.FormatBool(inbound.Enabled),
		strconv.FormatBool(inbound.RealityEnabled),
		strings.TrimSpace(inbound.RealityPublicKey),
		strings.TrimSpace(inbound.RealityPrivateKey),
		strings.TrimSpace(inbound.RealityShortID),
		strings.TrimSpace(inbound.RealityFingerprint),
		strings.TrimSpace(inbound.RealitySpiderX),
		strings.TrimSpace(inbound.RealityServer),
		strconv.Itoa(inbound.RealityServerPort),
		strings.TrimSpace(inbound.VLESSFlow),
		strconv.FormatBool(inbound.SniffingEnabled),
		strconv.FormatBool(inbound.SniffingHTTP),
		strconv.FormatBool(inbound.SniffingTLS),
		strconv.FormatBool(inbound.SniffingQUIC),
		strconv.FormatBool(inbound.SniffingFakeDNS),
		strings.TrimSpace(inbound.TLSCertPath),
		strings.TrimSpace(inbound.TLSKeyPath),
		inbound.CreatedAt.UTC().Format(time.RFC3339Nano),
	}, "|")
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func panelCredentialVersion(credential domain.Credential) string {
	s := strings.Join([]string{
		strings.TrimSpace(credential.ID),
		strings.TrimSpace(credential.UserID),
		strings.TrimSpace(credential.InboundID),
		strings.TrimSpace(string(credential.Kind)),
		strings.TrimSpace(credential.Secret),
		strings.TrimSpace(credential.Metadata),
		credential.CreatedAt.UTC().Format(time.RFC3339Nano),
	}, "|")
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func maskSecret(secret string) string {
	s := strings.TrimSpace(secret)
	if s == "" {
		return "-"
	}
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "..." + s[len(s)-4:]
}

func panelBuildClientURI(ctx context.Context, nodeByID map[string]domain.Node, inboundByID map[string]domain.Inbound, credential domain.Credential) (string, string) {
	inbound, ok := inboundByID[credential.InboundID]
	if !ok {
		return "", "inbound not found"
	}
	node, ok := nodeByID[inbound.NodeID]
	if !ok {
		return "", "node not found"
	}
	uri, err := renderSingleClientURI(ctx, node, inbound, credential)
	if err != nil {
		return "", strings.TrimSpace(err.Error())
	}
	return strings.TrimSpace(uri), ""
}

func runtimeUnitStates(ctx context.Context, cfg config.AppConfig) []panelUnitState {
	units := []string{
		strings.TrimSpace(cfg.Runtime.SingBoxUnit),
		strings.TrimSpace(cfg.Runtime.XrayUnit),
		strings.TrimSpace(cfg.Runtime.CaddyUnit),
		strings.TrimSpace(cfg.Runtime.NginxUnit),
	}
	seen := map[string]struct{}{}
	out := make([]panelUnitState, 0, len(units))
	for _, unit := range units {
		if unit == "" {
			continue
		}
		if _, ok := seen[unit]; ok {
			continue
		}
		seen[unit] = struct{}{}

		active := "unknown"
		enabled := "unknown"
		if _, err := lookPath("systemctl"); err == nil {
			if v, runErr := runCommandOutput(ctx, "systemctl", "is-active", unit); runErr == nil {
				active = strings.TrimSpace(v)
			} else if strings.TrimSpace(v) != "" {
				active = strings.TrimSpace(v)
			} else {
				active = "inactive"
			}
			if v, runErr := runCommandOutput(ctx, "systemctl", "is-enabled", unit); runErr == nil {
				enabled = strings.TrimSpace(v)
			} else if strings.TrimSpace(v) != "" {
				enabled = strings.TrimSpace(v)
			} else {
				enabled = "disabled"
			}
		}
		out = append(out, panelUnitState{Unit: unit, Active: active, Enabled: enabled})
	}
	return out
}

func listPanelSubscriptionLinks(cfg config.AppConfig) ([]string, error) {
	publicDir := subscriptionPublicDir(cfg.Paths.Subscription)
	entries, err := os.ReadDir(publicDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read subscriptions public directory: %w", err)
	}

	tokens := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		token := strings.TrimSuffix(name, filepath.Ext(name))
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		tokens[token] = struct{}{}
	}

	links := make([]string, 0, len(tokens))
	for token := range tokens {
		link := buildSubscriptionPublicURL(cfg, token)
		if link == "" {
			link = "http://<server-ip-or-domain>/sub/" + token
		}
		links = append(links, link)
	}
	sort.Strings(links)
	return links, nil
}

func panelSubscriptionTokenFromLink(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err == nil && strings.TrimSpace(parsed.Path) != "" {
		parts := strings.Split(strings.Trim(strings.TrimSpace(parsed.Path), "/"), "/")
		if len(parts) > 0 {
			last := strings.TrimSpace(parts[len(parts)-1])
			if last != "" {
				last = strings.TrimSuffix(last, filepath.Ext(last))
				if last != "" {
					return last
				}
			}
		}
	}
	value = strings.TrimSuffix(value, filepath.Ext(value))
	value = strings.Trim(value, "/")
	if value == "" || strings.Contains(value, " ") {
		return ""
	}
	return value
}

func cleanupSubscriptionTokenFiles(subscriptionDir, token string) (int, error) {
	return cleanupSubscriptionTokenFilesWithMirror(subscriptionDir, "", token)
}

func cleanupSubscriptionTokenFilesWithMirror(subscriptionDir, decoySiteDir, token string) (int, error) {
	t := strings.TrimSpace(token)
	if t == "" {
		return 0, nil
	}
	publicDir := subscriptionPublicDir(subscriptionDir)
	mirrorDir := subscriptionDecoyDir(decoySiteDir)
	paths := []string{
		filepath.Join(publicDir, t),
		filepath.Join(publicDir, t+".txt"),
	}
	if strings.TrimSpace(mirrorDir) != "" {
		paths = append(paths,
			filepath.Join(mirrorDir, t),
			filepath.Join(mirrorDir, t+".txt"),
		)
	}
	paths = compactUnique(paths)
	removed := 0
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		err := os.Remove(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return removed, fmt.Errorf("remove subscription token file %q: %w", p, err)
		}
		removed++
	}
	return removed, nil
}

func normalizePanelBasePath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "/panel"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "/"
	}
	return path
}

func panelJoin(basePath, suffix string) string {
	base := normalizePanelBasePath(basePath)
	s := strings.TrimPrefix(strings.TrimSpace(suffix), "/")
	if s == "" {
		return base
	}
	if base == "/" {
		return "/" + s
	}
	return base + "/" + s
}

func isValidPanelPortString(raw string) bool {
	v := strings.TrimSpace(raw)
	if v == "" {
		return false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return false
	}
	return n >= 1 && n <= 65535
}

type panelCookieAuth struct {
	expectedUser  []byte
	expectedPass  []byte
	cookieName    string
	sessionValue  []byte
	basePath      string
	dashboardPath string
	loginPath     string
	logoutPath    string
}

type panelLoginData struct {
	LoginPath string
	Error     string
}

func newPanelCookieAuth(login, password, basePath, dashboardPath, logoutPath string) panelCookieAuth {
	return panelCookieAuth{
		expectedUser:  []byte(strings.TrimSpace(login)),
		expectedPass:  []byte(strings.TrimSpace(password)),
		cookieName:    "proxyctl_panel_session",
		sessionValue:  []byte(newPanelSessionToken()),
		basePath:      normalizePanelBasePath(basePath),
		dashboardPath: dashboardPath,
		loginPath:     panelJoin(basePath, "login"),
		logoutPath:    logoutPath,
	}
}

func newPanelSessionToken() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(buf)
}

func (a panelCookieAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == a.loginPath || r.URL.Path == a.logoutPath {
			next.ServeHTTP(w, r)
			return
		}
		if a.isAuthenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, a.loginPath, http.StatusFound)
	})
}

func (a panelCookieAuth) isAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie(a.cookieName)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), a.sessionValue) == 1
}

func (a panelCookieAuth) setSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName,
		Value:    string(a.sessionValue),
		Path:     a.basePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})
}

func (a panelCookieAuth) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName,
		Value:    "",
		Path:     a.basePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (a panelCookieAuth) renderLogin(w http.ResponseWriter, status int, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = panelLoginTmpl.Execute(w, panelLoginData{
		LoginPath: a.loginPath,
		Error:     strings.TrimSpace(errMsg),
	})
}

func (a panelCookieAuth) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if a.isAuthenticated(r) {
			http.Redirect(w, r, a.dashboardPath, http.StatusFound)
			return
		}
		a.renderLogin(w, http.StatusOK, "")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		a.renderLogin(w, http.StatusBadRequest, "invalid request")
		return
	}
	login := strings.TrimSpace(r.FormValue("login"))
	password := strings.TrimSpace(r.FormValue("password"))
	if subtle.ConstantTimeCompare([]byte(login), a.expectedUser) != 1 || subtle.ConstantTimeCompare([]byte(password), a.expectedPass) != 1 {
		a.renderLogin(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	a.setSessionCookie(w)
	http.Redirect(w, r, a.dashboardPath, http.StatusFound)
}

func (a panelCookieAuth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.clearSessionCookie(w)
	http.Redirect(w, r, a.loginPath, http.StatusFound)
}
