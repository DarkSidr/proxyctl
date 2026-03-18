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
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"proxyctl/internal/config"
	"proxyctl/internal/domain"
	"proxyctl/internal/engine"
	"proxyctl/internal/renderer"
	subscriptionservice "proxyctl/internal/subscription/service"
)

type panelUnitState struct {
	Unit    string
	Active  string
	Enabled string
}

type panelInboundView struct {
	ID        string
	Type      string
	Engine    string
	NodeID    string
	NodeName  string
	Domain    string
	Port      int
	TLS       bool
	Enabled   bool
	Transport string
	Path      string
	SNI       string
	Version   string
}

type panelUserView struct {
	ID        string
	Name      string
	Enabled   bool
	CreatedAt time.Time
	Version   string
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
	ID      string
	Name    string
	Host    string
	Role    string
	Enabled bool
	Version string
}

type panelSubscriptionState struct {
	Exists  bool
	Enabled bool
}

type panelSubscriptionView struct {
	UserID       string
	UserName     string
	ProfileName  string
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
	CPUCores        int
	MemUsedBytes    uint64
	MemTotalBytes   uint64
	DiskUsedBytes   uint64
	DiskTotalBytes  uint64
	UptimeSeconds   uint64
	TotalRXBytes    uint64
	TotalTXBytes    uint64
	TotalBytes      uint64
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
	SuggestedPorts      map[string]int
	SNIPresets          []string
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
      <h2>runtime actions</h2>
      <div class="pad line">
        <form method="post" action="{{.DashboardActionPath}}" class="inline">
          <input type="hidden" name="action" value="render">
          <button class="btn" type="submit">render configs</button>
        </form>
        <form method="post" action="{{.DashboardActionPath}}" class="inline">
          <input type="hidden" name="action" value="validate">
          <button class="btn warn" type="submit">validate</button>
        </form>
        <form method="post" action="{{.DashboardActionPath}}" class="inline">
          <input type="hidden" name="action" value="apply">
          <button class="btn err" type="submit">apply</button>
        </form>
      </div>
    </section>

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
	BasePath            string
	LegacyPath          string
	LogoutPath          string
	SnapshotPath        string
	DashboardActionPath string
	SettingsActionPath  string
	UsersActionPath     string
	NodesActionPath     string
	InboundsActionPath  string
	SubsActionPath      string
	ContactEmail        string
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
  </style>
</head>
<body>
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
      <button type="button" class="tab active" data-tab="runtime">runtime</button>
      <button type="button" class="tab" data-tab="nodes">nodes</button>
      <button type="button" class="tab" data-tab="inbounds">inbounds</button>
      <button type="button" class="tab" data-tab="users">users</button>
      <button type="button" class="tab" data-tab="credentials">credentials</button>
      <button type="button" class="tab" data-tab="subscriptions">subscriptions</button>
      <button type="button" class="tab" data-tab="settings">settings</button>
    </div>

    <section class="sec" data-tab-section="runtime">
      <h2>runtime actions</h2>
      <div class="pad row">
        <button class="btn" data-runtime="render">render</button>
        <button class="btn warn" data-runtime="validate">validate</button>
        <button class="btn err" data-runtime="apply">apply</button>
        <button id="askTrafficBtn" class="btn secondary">ask traffic now</button>
        <label class="row" style="gap:6px">
          <input id="liveMode" type="checkbox" checked>
          <span class="label">live</span>
        </label>
        <select id="liveInterval">
          <option value="3000">3s</option>
          <option value="5000" selected>5s</option>
          <option value="10000">10s</option>
        </select>
      </div>
      <div class="pad">
        <div class="grid" id="dashCards"></div>
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>user</th><th>rx</th><th>tx</th><th>total</th></tr></thead>
          <tbody id="userTrafficBody"></tbody>
        </table>
      </div>
      <div class="pad muted" id="trafficMeta"></div>
    </section>

    <section class="sec" data-tab-section="nodes">
      <h2>nodes</h2>
      <div class="pad row">
        <input id="nodeName" type="text" placeholder="node name">
        <input id="nodeHost" type="text" placeholder="node host/ip">
        <select id="nodeRole">
          <option value="node">node</option>
          <option value="primary">primary</option>
        </select>
        <button id="createNodeBtn" class="btn">create node</button>
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>name</th><th>host</th><th>role</th><th>enabled</th><th>actions</th></tr></thead>
          <tbody id="nodesBody"></tbody>
        </table>
      </div>
    </section>

    <section class="sec" data-tab-section="inbounds">
      <h2>inbounds</h2>
      <div class="pad row">
        <select id="inType">
          <option value="vless">vless</option>
          <option value="hysteria2">hysteria2</option>
          <option value="xhttp">xhttp</option>
        </select>
        <select id="inTransport">
          <option value="tcp">tcp</option>
          <option value="ws">ws</option>
          <option value="grpc">grpc</option>
          <option value="udp">udp</option>
          <option value="xhttp">xhttp</option>
        </select>
        <select id="inEngine">
          <option value="">auto</option>
          <option value="sing-box">sing-box</option>
          <option value="xray">xray</option>
        </select>
        <select id="inNode"></select>
        <input id="inDomain" type="text" placeholder="domain">
        <input id="inPort" type="number" min="1" max="65535" placeholder="port">
        <input id="inPath" type="text" placeholder="path (optional)">
        <select id="inSniMode">
          <option value="none">sni: none</option>
          <option value="list">sni: from list</option>
          <option value="domain">sni: same as domain</option>
          <option value="custom">sni: custom</option>
        </select>
        <input id="inSni" type="text" list="inSniList" placeholder="sni value">
        <datalist id="inSniList"></datalist>
        <button id="createInboundBtn" class="btn">create inbound</button>
        <button id="cancelInboundEditBtn" type="button" class="btn secondary hidden">cancel edit</button>
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
          <thead><tr><th>name</th><th>id</th><th>actions</th></tr></thead>
          <tbody id="usersBody"></tbody>
        </table>
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
      <h2>subscriptions</h2>
      <div class="pad row">
        <select id="subUser"></select>
        <button id="genSubBtn" class="btn">generate for user</button>
        <select id="subProfileSel"></select>
        <input id="subProfile" type="text" placeholder="profile for selected (default: panel)">
        <button id="genSelectedSubBtn" class="btn secondary">generate selected</button>
        <button id="detachSelectedCredsBtn" class="btn secondary">detach selected creds</button>
        <button id="refreshSubBtn" class="btn warn">refresh all</button>
        <button id="subEnableBtn" class="btn">enable</button>
        <button id="subDisableBtn" class="btn err">disable</button>
      </div>
      <div class="pad" id="subInboundPick"></div>
      <div class="pad" id="subsList"></div>
    </section>

    <section class="sec" data-tab-section="settings">
      <h2>settings</h2>
      <div class="pad row">
        <input id="acmeEmail" type="email" placeholder="acme contact email (optional)" value="{{.ContactEmail}}">
        <button id="saveAcmeBtn" class="btn">save ACME email</button>
      </div>
      <div class="pad muted">used by caddy tls/acme and new node bootstrap flows</div>
    </section>
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
      render();
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
      await getSnapshot();
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
    function getSuggestedInboundPort(type, transport) {
      const ports = (snapshot && snapshot.SuggestedPorts) ? snapshot.SuggestedPorts : {};
      const key = String(type || "").trim().toLowerCase() + "|" + String(transport || "").trim().toLowerCase();
      const v = ports[key];
      const n = Number(v);
      return Number.isInteger(n) && n > 0 ? n : 0;
    }
    function updateInboundPortSuggestion(force) {
      const type = (document.getElementById("inType").value || "").trim();
      const transport = (document.getElementById("inTransport").value || "").trim();
      const portEl = document.getElementById("inPort");
      if (!portEl) return;
      const suggested = getSuggestedInboundPort(type, transport);
      if (!suggested) return;
      if (force || !(portEl.value || "").trim()) {
        portEl.value = String(suggested);
      }
    }
    function updateInboundSniInputState() {
      const mode = (document.getElementById("inSniMode").value || "none").trim();
      const input = document.getElementById("inSni");
      const sniModeEl = document.getElementById("inSniMode");
      if (!input) return;
      if (sniModeEl && sniModeEl.disabled) {
        input.value = "";
        input.disabled = true;
        input.placeholder = "sni disabled";
        return;
      }
      if (mode === "none") {
        input.value = "";
        input.disabled = true;
        input.placeholder = "sni disabled";
      } else if (mode === "domain") {
        input.value = "";
        input.disabled = true;
        input.placeholder = "sni = domain";
      } else if (mode === "list") {
        input.disabled = false;
        input.placeholder = "pick sni from list";
      } else {
        input.disabled = false;
        input.placeholder = "custom sni";
      }
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
      editingInboundID = "";
      editingInboundVersion = "";
      editingInboundEnabled = true;
      const createBtn = document.getElementById("createInboundBtn");
      if (createBtn) createBtn.textContent = "create inbound";
      const cancelBtn = document.getElementById("cancelInboundEditBtn");
      if (cancelBtn) cancelBtn.classList.add("hidden");
      const sniMode = document.getElementById("inSniMode");
      if (sniMode) sniMode.value = "none";
      const sni = document.getElementById("inSni");
      if (sni) sni.value = "";
      const path = document.getElementById("inPath");
      if (path) path.value = "";
      updateInboundDomainFromNode(true);
      updateInboundCreateFieldVisibility(true);
      updateInboundSniInputState();
    }
    function beginInboundEdit(inbound) {
      if (!inbound || !inbound.ID) return;
      editingInboundID = String(inbound.ID || "").trim();
      editingInboundVersion = String(inbound.Version || "").trim();
      editingInboundEnabled = !!inbound.Enabled;
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
        const nextEngine = String(inbound.Engine || "").trim();
        if (nextEngine && Array.from(engineEl.options).some((o) => o.value === nextEngine)) {
          engineEl.value = nextEngine;
        } else {
          engineEl.value = "";
        }
      }
      const nodeEl = document.getElementById("inNode");
      if (nodeEl) {
        const nextNodeID = String(inbound.NodeID || "").trim();
        if (nextNodeID && Array.from(nodeEl.options).some((o) => o.value === nextNodeID)) {
          nodeEl.value = nextNodeID;
        }
      }
      document.getElementById("inDomain").value = String(inbound.Domain || "").trim();
      document.getElementById("inPort").value = String(inbound.Port || "").trim();
      document.getElementById("inPath").value = String(inbound.Path || "").trim();
      const sni = String(inbound.SNI || "").trim();
      const sniModeEl = document.getElementById("inSniMode");
      const sniEl = document.getElementById("inSni");
      if (sniModeEl && !sniModeEl.disabled) {
        const domain = String(inbound.Domain || "").trim();
        if (!sni) {
          sniModeEl.value = "none";
          if (sniEl) sniEl.value = "";
        } else if (domain && sni === domain) {
          sniModeEl.value = "domain";
          if (sniEl) sniEl.value = "";
        } else if (inboundSniOptions.includes(sni)) {
          sniModeEl.value = "list";
          if (sniEl) sniEl.value = sni;
        } else {
          sniModeEl.value = "custom";
          if (sniEl) sniEl.value = sni;
        }
      } else if (sniEl) {
        sniEl.value = "";
      }
      updateInboundCreateFieldVisibility(false);
      const createBtn = document.getElementById("createInboundBtn");
      if (createBtn) createBtn.textContent = "save inbound";
      const cancelBtn = document.getElementById("cancelInboundEditBtn");
      if (cancelBtn) cancelBtn.classList.remove("hidden");
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
      if (!list) return;
      list.innerHTML = inboundSniOptions.map((v) => '<option value="'+esc(v)+'"></option>').join("");
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
      const type = (document.getElementById("inType").value || "").trim().toLowerCase();
      const transportSel = document.getElementById("inTransport");
      const pathEl = document.getElementById("inPath");
      const sniModeEl = document.getElementById("inSniMode");
      const sniEl = document.getElementById("inSni");
      if (!transportSel || !pathEl || !sniModeEl || !sniEl) return;

      if (type === "hysteria2") {
        setTransportOptions(["udp"]);
        transportSel.disabled = true;
      } else if (type === "xhttp") {
        setTransportOptions(["xhttp"]);
        transportSel.disabled = true;
      } else {
        setTransportOptions(["tcp", "ws", "grpc"]);
        transportSel.disabled = false;
      }

      const transport = (transportSel.value || "").trim().toLowerCase();
      const pathNeeded = transport === "ws" || transport === "grpc" || transport === "xhttp";
      pathEl.classList.toggle("hidden", !pathNeeded);
      if (!pathNeeded) {
        pathEl.value = "";
      } else if (!(pathEl.value || "").trim()) {
        if (transport === "ws") pathEl.value = "/ws";
        if (transport === "grpc") pathEl.value = "grpc";
        if (transport === "xhttp") pathEl.value = "/xhttp";
      }
      const sniSupported = type === "vless" || type === "hysteria2";
      sniModeEl.classList.toggle("hidden", !sniSupported);
      sniEl.classList.toggle("hidden", !sniSupported);
      sniModeEl.disabled = !sniSupported;
      if (!sniSupported) {
        sniModeEl.value = "none";
        sniEl.value = "";
      }
      updateInboundPortSuggestion(!!forcePort);
      updateInboundSniInputState();
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
      pick.innerHTML = [
        '<div class="label" style="margin-bottom:8px">selected profile inbounds</div>',
        '<div class="table-wrap">',
        '<table>',
        '<thead><tr><th><input type="checkbox" id="subInboundAll"></th><th>label</th><th>node</th><th>domain</th><th>port</th><th>type</th><th>transport</th><th>path</th><th>sni</th><th>enabled</th></tr></thead>',
        '<tbody>',
        inbounds.map((i) => {
          const checked = selected.has(String(i.ID)) ? ' checked' : '';
          const label = [String(i.Type || "").trim(), String(i.Domain || "").trim() + ":" + String(i.Port || "")].join(" ").trim();
          return (
            '<tr>' +
              '<td><input type="checkbox" data-sub-inbound-id="'+esc(i.ID)+'"'+checked+'></td>' +
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
    function render() {
      if (!snapshot) return;
      syncOpFromSnapshot();

      const c = snapshot.Counts || {};
      const dash = snapshot.Dashboard || {};
      document.getElementById("counts").innerHTML = [
        ["users", c.UsersTotal],
        ["enabled users", c.UsersEnabled],
        ["inbounds", c.InboundsTotal],
        ["active inbounds", c.InboundsActive],
      ].map(([k, v]) => '<div class="card"><div class="label">'+esc(k)+'</div><div class="value">'+esc(v)+'</div></div>').join("");
      const dashCardsEl = document.getElementById("dashCards");
      if (dashCardsEl) dashCardsEl.innerHTML = [
        ["proxyctl", dash.ProxyctlVersion || "dev"],
        ["cpu load (1m)", dash.Load1 ?? 0],
        ["cpu cores", dash.CPUCores ?? 0],
        ["memory", fmtBytes(dash.MemUsedBytes) + " / " + fmtBytes(dash.MemTotalBytes)],
        ["disk", fmtBytes(dash.DiskUsedBytes) + " / " + fmtBytes(dash.DiskTotalBytes)],
        ["uptime", fmtUptime(dash.UptimeSeconds)],
        ["traffic total", fmtBytes(dash.TotalBytes)],
        ["traffic rx/tx", fmtBytes(dash.TotalRXBytes) + " / " + fmtBytes(dash.TotalTXBytes)],
      ].map(([k, v]) => '<div class="card"><div class="label">'+esc(k)+'</div><div class="value">'+esc(v)+'</div></div>').join("");
      const userTraffic = Array.isArray(dash.UserTraffic) ? dash.UserTraffic : [];
      document.getElementById("userTrafficBody").innerHTML = userTraffic.map((u) => (
        '<tr>' +
          '<td>'+esc(u.UserName || u.UserID)+'</td>' +
          '<td>'+esc(fmtBytes(u.RXBytes))+'</td>' +
          '<td>'+esc(fmtBytes(u.TXBytes))+'</td>' +
          '<td>'+esc(fmtBytes(u.TotalBytes))+'</td>' +
        '</tr>'
      )).join("");
      document.getElementById("trafficMeta").textContent = "traffic source: " + (dash.TrafficSource || "none");

      const users = Array.isArray(snapshot.Users) ? snapshot.Users : [];
      const nodes = Array.isArray(snapshot.Nodes) ? snapshot.Nodes : [];
      const inbounds = Array.isArray(snapshot.Inbounds) ? snapshot.Inbounds : [];
      const creds = Array.isArray(snapshot.Credentials) ? snapshot.Credentials : [];
      const subDetails = Array.isArray(snapshot.SubscriptionDetails) ? snapshot.SubscriptionDetails : [];
      refreshInboundSniList(nodes, inbounds);
      updateInboundCreateFieldVisibility(false);
      document.getElementById("usersBody").innerHTML = users.map((u) => (
        '<tr>' +
          '<td>'+esc(u.Name)+'</td>' +
          '<td class="mono muted">'+esc(u.ID)+'</td>' +
          '<td><button class="btn err" data-user-id="'+esc(u.ID)+'" data-user-version="'+esc(u.Version)+'">delete</button></td>' +
        '</tr>'
      )).join("");

      document.querySelectorAll("[data-user-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            await postForm(cfg.usersActionPath, {
              op: "delete",
              user_id: btn.getAttribute("data-user-id"),
              version: btn.getAttribute("data-user-version"),
            });
          } catch (e) {
            showOp("error", String(e));
          }
        });
      });

      document.getElementById("nodesBody").innerHTML = nodes.map((n) => (
        '<tr>' +
          '<td><input type="text" data-node-name="'+esc(n.ID)+'" value="'+esc(n.Name)+'"></td>' +
          '<td><input type="text" data-node-host="'+esc(n.ID)+'" value="'+esc(n.Host)+'"></td>' +
          '<td><select data-node-role="'+esc(n.ID)+'"><option value="primary"'+(String(n.Role) === "primary" ? " selected" : "")+'>primary</option><option value="node"'+(String(n.Role) === "node" ? " selected" : "")+'>node</option></select></td>' +
          '<td>'+ (n.Enabled ? "yes" : "no") +'</td>' +
          '<td class="row">' +
            '<button class="btn secondary" data-node-save-id="'+esc(n.ID)+'" data-node-save-version="'+esc(n.Version)+'">save</button>' +
            '<button class="btn secondary" data-node-test-id="'+esc(n.ID)+'" data-node-test-version="'+esc(n.Version)+'">test</button>' +
            '<button class="btn secondary" data-node-sshkey-id="'+esc(n.ID)+'" data-node-sshkey-version="'+esc(n.Version)+'">setup ssh key</button>' +
            '<button class="btn secondary" data-node-bootstrap-id="'+esc(n.ID)+'" data-node-bootstrap-version="'+esc(n.Version)+'">bootstrap</button>' +
            '<button class="btn '+(n.Enabled ? 'warn' : '')+'" data-node-toggle-id="'+esc(n.ID)+'" data-node-toggle-version="'+esc(n.Version)+'" data-node-enabled="'+(n.Enabled ? '1' : '0')+'">'+(n.Enabled ? 'disable' : 'enable')+'</button>' +
            '<button class="btn err" data-node-id="'+esc(n.ID)+'" data-node-version="'+esc(n.Version)+'">delete</button>' +
          '</td>' +
        '</tr>'
      )).join("");
      document.querySelectorAll("[data-node-save-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            const id = btn.getAttribute("data-node-save-id") || "";
            const nameEl = document.querySelector('[data-node-name="'+id+'"]');
            const hostEl = document.querySelector('[data-node-host="'+id+'"]');
            const roleEl = document.querySelector('[data-node-role="'+id+'"]');
            const name = (nameEl && nameEl.value ? nameEl.value : "").trim();
            const host = (hostEl && hostEl.value ? hostEl.value : "").trim();
            const role = (roleEl && roleEl.value ? roleEl.value : "").trim();
            if (!id || !name || !host || !role) return;
            await postForm(cfg.nodesActionPath, {
              op: "update",
              node_id: id,
              version: btn.getAttribute("data-node-save-version"),
              name,
              host,
              role,
            });
          } catch (e) {
            showOp("error", String(e));
          }
        });
      });
      document.querySelectorAll("[data-node-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            await postForm(cfg.nodesActionPath, {
              op: "delete",
              node_id: btn.getAttribute("data-node-id"),
              version: btn.getAttribute("data-node-version"),
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
      document.querySelectorAll("[data-node-bootstrap-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            const nodeID = (btn.getAttribute("data-node-bootstrap-id") || "").trim();
            const rowNode = nodes.find((item) => String(item?.ID || "").trim() === nodeID);
            const nodeName = String(rowNode?.Name || nodeID || "node").trim();
            const nodeHost = String(rowNode?.Host || "").trim();
            const nodeLabel = nodeHost ? (nodeName + " (" + nodeHost + ")") : nodeName;
            const sshPassword = window.prompt("Root password for node bootstrap: " + nodeLabel + " (optional if SSH key already works):", "") || "";
            await postForm(cfg.nodesActionPath, {
              op: "bootstrap",
              node_id: nodeID,
              version: btn.getAttribute("data-node-bootstrap-version"),
              ssh_password: sshPassword,
            });
          } catch (e) {
            showOp("error", String(e));
          }
        });
      });
      document.querySelectorAll("[data-node-sshkey-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            const nodeID = (btn.getAttribute("data-node-sshkey-id") || "").trim();
            const rowNode = nodes.find((item) => String(item?.ID || "").trim() === nodeID);
            const nodeName = String(rowNode?.Name || nodeID || "node").trim();
            const nodeHost = String(rowNode?.Host || "").trim();
            const nodeLabel = nodeHost ? (nodeName + " (" + nodeHost + ")") : nodeName;
            const sshPassword = window.prompt("Root password for SSH key setup on node: " + nodeLabel + " (optional if key auth already works):", "") || "";
            await postForm(cfg.nodesActionPath, {
              op: "install_ssh_key",
              node_id: nodeID,
              version: btn.getAttribute("data-node-sshkey-version"),
              ssh_password: sshPassword,
            });
          } catch (e) {
            showOp("error", String(e));
          }
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
      document.getElementById("inboundsBody").innerHTML = inbounds.map((i) => (
        '<tr>' +
          '<td class="mono muted">'+esc(i.ID)+'</td>' +
          '<td>'+esc(i.Type)+'</td>' +
          '<td>'+esc(i.NodeName)+'</td>' +
          '<td>'+esc(i.Domain)+'</td>' +
          '<td>'+esc(i.Port)+'</td>' +
          '<td class="row">' +
            '<button class="btn secondary" data-inbound-edit-id="'+esc(i.ID)+'">edit</button>' +
            '<button class="btn '+(i.Enabled ? 'warn' : '')+'" data-inbound-toggle-id="'+esc(i.ID)+'" data-inbound-toggle-version="'+esc(i.Version)+'" data-inbound-enabled="'+(i.Enabled ? '1' : '0')+'">'+(i.Enabled ? 'disable' : 'enable')+'</button>' +
            '<button class="btn err" data-inbound-id="'+esc(i.ID)+'" data-inbound-version="'+esc(i.Version)+'">delete</button>' +
          '</td>' +
        '</tr>'
      )).join("");
      document.querySelectorAll("[data-inbound-edit-id]").forEach((btn) => {
        btn.addEventListener("click", () => {
          const inboundID = (btn.getAttribute("data-inbound-edit-id") || "").trim();
          if (!inboundID) return;
          const selected = inbounds.find((item) => String(item.ID || "").trim() === inboundID);
          if (!selected) return;
          beginInboundEdit(selected);
        });
      });
      document.querySelectorAll("[data-inbound-toggle-id]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            const enabledNow = btn.getAttribute("data-inbound-enabled") === "1";
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
          '<td><button class="btn err" data-cred-id="'+esc(c.ID)+'" data-cred-user="'+esc(c.UserID)+'" data-cred-version="'+esc(c.Version)+'">detach</button></td>' +
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

      document.getElementById("subsList").innerHTML = subDetails.length === 0
        ? '<div class="muted">no subscriptions</div>'
        : [
            '<div class="table-wrap">',
            '<table>',
            '<thead><tr><th>user</th><th>profile</th><th>status</th><th>configs</th><th>inside configs</th><th>link</th><th>actions</th></tr></thead>',
            '<tbody>',
            subDetails.map((s) => {
              const link = String(s.URL || "").trim();
              const labels = Array.isArray(s.ConfigLabels) ? s.ConfigLabels : [];
              return (
                '<tr>' +
                  '<td>'+esc((s.UserName || "") + (s.UserID ? " (" + s.UserID + ")" : ""))+'</td>' +
                  '<td>'+esc(s.ProfileName || "default")+'</td>' +
                  '<td>'+ (s.Enabled ? 'enabled' : 'disabled') +'</td>' +
                  '<td>'+esc(Number(s.ConfigCount || 0))+'</td>' +
                  '<td>'+ (labels.length > 0 ? esc(labels.join(", ")) : '<span class="muted">-</span>') +'</td>' +
                  '<td>'+ (link ? '<a href="'+esc(link)+'" target="_blank" rel="noopener noreferrer">'+esc(link)+'</a>' : '<span class="muted">-</span>') +'</td>' +
                  '<td class="row">' +
                    (link ? '<button class="btn secondary" data-copy="'+esc(link)+'">copy</button>' : '') +
                    '<button class="btn err" data-sub-delete="'+esc(link || s.AccessToken || "")+'">delete</button>' +
                  '</td>' +
                '</tr>'
              );
            }).join(""),
            '</tbody>',
            '</table>',
            '</div>',
          ].join("");
      updateSubButtons();

      renderSubInboundPick(inbounds);

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
    }

    document.querySelectorAll("[data-runtime]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        try {
          await postForm(cfg.dashboardActionPath, { action: btn.getAttribute("data-runtime") || "" });
        } catch (e) {
          showOp("error", String(e));
        }
      });
    });
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
    document.getElementById("createNodeBtn").addEventListener("click", async () => {
      const name = (document.getElementById("nodeName").value || "").trim();
      const host = (document.getElementById("nodeHost").value || "").trim();
      const role = (document.getElementById("nodeRole").value || "").trim();
      if (!name || !host) return;
      try {
        await postForm(cfg.nodesActionPath, { op: "create", name, host, role });
        document.getElementById("nodeName").value = "";
        document.getElementById("nodeHost").value = "";
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.getElementById("createInboundBtn").addEventListener("click", async () => {
      updateInboundCreateFieldVisibility(false);
      const type = (document.getElementById("inType").value || "").trim();
      const transport = (document.getElementById("inTransport").value || "").trim();
      const engine = (document.getElementById("inEngine").value || "").trim();
      const nodeID = (document.getElementById("inNode").value || "").trim();
      const domain = (document.getElementById("inDomain").value || "").trim();
      let port = (document.getElementById("inPort").value || "").trim();
      const path = (document.getElementById("inPath").value || "").trim();
      const sniMode = (document.getElementById("inSniMode").value || "none").trim();
      const tls = String(type || "").trim().toLowerCase() === "hysteria2" ? "1" : "0";
      let sni = "";
      if (sniMode === "domain") {
        sni = domain;
      } else if (sniMode === "list" || sniMode === "custom") {
        sni = (document.getElementById("inSni").value || "").trim();
      }
      if (!port) {
        const suggested = getSuggestedInboundPort(type, transport);
        if (suggested > 0) {
          port = String(suggested);
          document.getElementById("inPort").value = port;
        }
      }
      if (sniMode === "list" && sni && !inboundSniOptions.includes(sni)) {
        showOp("error", "SNI from list mode: choose value from suggestions");
        return;
      }
      if (!type || !transport || !nodeID || !domain || !port) return;
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
          tls,
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
    document.getElementById("subEnableBtn").addEventListener("click", async () => {
      const userID = (document.getElementById("subUser").value || "").trim();
      const profile = currentSubProfileName();
      if (!userID) return;
      try {
        await postForm(cfg.subsActionPath, { op: "set_enabled", user_id: userID, profile, enabled: "1" });
      } catch (e) {
        showOp("error", String(e));
      }
    });
    document.getElementById("subDisableBtn").addEventListener("click", async () => {
      const userID = (document.getElementById("subUser").value || "").trim();
      const profile = currentSubProfileName();
      if (!userID) return;
      try {
        await postForm(cfg.subsActionPath, { op: "set_enabled", user_id: userID, profile, enabled: "0" });
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
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden && document.getElementById("liveMode")?.checked) {
        pollSnapshotSilently();
      }
    });
    document.getElementById("inType").addEventListener("change", () => updateInboundCreateFieldVisibility(true));
    document.getElementById("inTransport").addEventListener("change", () => updateInboundCreateFieldVisibility(true));
    document.getElementById("inSniMode").addEventListener("change", () => updateInboundSniInputState());
    document.getElementById("inNode").addEventListener("change", () => {
      updateInboundDomainFromNode(true);
      updateInboundSniInputState();
    });
    document.getElementById("inDomain").addEventListener("blur", () => {
      const mode = (document.getElementById("inSniMode").value || "none").trim();
      if (mode === "domain") {
        updateInboundSniInputState();
      }
    });
    document.getElementById("subUser").addEventListener("change", () => {
      render();
    });
    document.getElementById("subProfileSel").addEventListener("change", () => {
      const selected = (document.getElementById("subProfileSel").value || "").trim();
      const input = document.getElementById("subProfile");
      if (input) input.value = selected;
      updateSubButtons();
      const inbounds = (snapshot && Array.isArray(snapshot.Inbounds)) ? snapshot.Inbounds : [];
      renderSubInboundPick(inbounds);
    });
    document.getElementById("subProfile").addEventListener("change", () => {
      updateSubButtons();
      const inbounds = (snapshot && Array.isArray(snapshot.Inbounds)) ? snapshot.Inbounds : [];
      renderSubInboundPick(inbounds);
    });
    document.querySelectorAll("[data-tab]").forEach((btn) => {
      btn.addEventListener("click", () => setTab(btn.getAttribute("data-tab") || "runtime"));
    });
    lastOpSeenKey = loadLastOpSeenKey();
    setTab("runtime");
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
						Dashboard:           snapshot.dashboard,
						ContactEmail:        strings.TrimSpace(cfg.Public.ContactEmail),
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
					BasePath:            basePath,
					LegacyPath:          legacyDashboardPath,
					LogoutPath:          logoutPath,
					SnapshotPath:        apiSnapshotPath,
					DashboardActionPath: dashboardActionPath,
					SettingsActionPath:  settingsActionPath,
					UsersActionPath:     usersActionPath,
					NodesActionPath:     nodesActionPath,
					InboundsActionPath:  inboundsActionPath,
					SubsActionPath:      subsActionPath,
					ContactEmail:        strings.TrimSpace(cfg.Public.ContactEmail),
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
					Dashboard:           snapshot.dashboard,
					ContactEmail:        strings.TrimSpace(cfg.Public.ContactEmail),
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
				if err := panelHandleNodeAction(r.Context(), resolvedDB, r, &configPathValue, &dbPathValue, ops); err != nil {
					ops.set("error", err.Error())
				}
				panelRespondAction(w, r, legacyDashboardPath, ops)
			})
			panelMux.HandleFunc(inboundsActionPath, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				if err := panelHandleInboundAction(r.Context(), resolvedDB, r, &configPathValue, &dbPathValue, ops); err != nil {
					ops.set("error", err.Error())
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
				case "attach_credential", "delete_credential":
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
			}

			fmt.Fprintf(cmd.OutOrStdout(), "panel listen: %s\n", listenAddr)
			fmt.Fprintf(cmd.OutOrStdout(), "panel path: %s\n", basePath)
			if requireAuth {
				fmt.Fprintln(cmd.OutOrStdout(), "panel auth: enabled (login page)")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "terminate with Ctrl+C")
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
	suggestedPorts    map[string]int
	sniPresets        []string
	dashboard         panelDashboardView
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
		nodeRows = append(nodeRows, panelNodeView{
			ID:      node.ID,
			Name:    node.Name,
			Host:    node.Host,
			Role:    string(node.Role),
			Enabled: node.Enabled,
			Version: panelNodeVersion(node),
		})
	}
	sort.Slice(nodeRows, func(i, j int) bool { return nodeRows[i].ID < nodeRows[j].ID })

	inboundRows := make([]panelInboundView, 0, len(inbounds))
	inboundByID := make(map[string]domain.Inbound, len(inbounds))
	enabledInbounds := 0
	usedPorts := make(map[int]struct{}, len(inbounds))
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
		nodeName := inbound.NodeID
		if name := strings.TrimSpace(nodeNameByID[inbound.NodeID]); name != "" {
			nodeName = name
		}
		inboundRows = append(inboundRows, panelInboundView{
			ID:        inbound.ID,
			Type:      string(inbound.Type),
			Engine:    string(inbound.Engine),
			NodeID:    inbound.NodeID,
			NodeName:  nodeName,
			Domain:    strings.TrimSpace(inbound.Domain),
			Port:      inbound.Port,
			TLS:       inbound.TLSEnabled,
			Enabled:   inbound.Enabled,
			Transport: strings.TrimSpace(inbound.Transport),
			Path:      strings.TrimSpace(inbound.Path),
			SNI:       strings.TrimSpace(inbound.SNI),
			Version:   panelInboundVersion(inbound),
		})
		if inbound.Enabled {
			enabledInbounds++
			if inbound.Port > 0 {
				usedPorts[inbound.Port] = struct{}{}
			}
		}
	}
	sort.Slice(inboundRows, func(i, j int) bool { return inboundRows[i].ID < inboundRows[j].ID })
	suggestedPorts := map[string]int{}
	for _, item := range []struct {
		protocol  string
		transport string
	}{
		{protocol: "vless", transport: "tcp"},
		{protocol: "vless", transport: "ws"},
		{protocol: "vless", transport: "grpc"},
		{protocol: "hysteria2", transport: "udp"},
		{protocol: "xhttp", transport: "xhttp"},
	} {
		key := item.protocol + "|" + item.transport
		suggestedPorts[key] = suggestWizardPort(item.protocol, item.transport, usedPorts, hostPortBusy)
	}

	userRows := make([]panelUserView, 0, len(users))
	userByID := make(map[string]domain.User, len(users))
	enabledUsers := 0
	for _, user := range users {
		userByID[user.ID] = user
		userRows = append(userRows, panelUserView{
			ID:        user.ID,
			Name:      user.Name,
			Enabled:   user.Enabled,
			CreatedAt: user.CreatedAt,
			Version:   panelUserVersion(user),
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
	load1, _ := panelLoadAvg1()
	if math.IsNaN(load1) || math.IsInf(load1, 0) {
		load1 = 0
	}
	memUsed, memTotal, _ := panelMemoryUsage()
	diskUsed, diskTotal, _ := panelDiskUsage(cfg.Paths.StateDir)
	uptime, _ := panelUptimeSeconds()
	totalRX, totalTX, _ := panelNetTotals()
	userTraffic, source := panelUserTraffic(cfg.Paths.RuntimeDir, users)
	totalUserBytes := uint64(0)
	for _, item := range userTraffic {
		totalUserBytes += item.TotalBytes
	}
	totalBytes := totalRX + totalTX
	if totalUserBytes > 0 {
		totalBytes = totalUserBytes
	}
	return panelDashboardView{
		ProxyctlVersion: strings.TrimSpace(Version),
		Load1:           load1,
		CPUCores:        runtime.NumCPU(),
		MemUsedBytes:    memUsed,
		MemTotalBytes:   memTotal,
		DiskUsedBytes:   diskUsed,
		DiskTotalBytes:  diskTotal,
		UptimeSeconds:   uptime,
		TotalRXBytes:    totalRX,
		TotalTXBytes:    totalTX,
		TotalBytes:      totalBytes,
		UserTraffic:     userTraffic,
		TrafficSource:   source,
	}
}

func panelLoadAvg1() (float64, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("invalid /proc/loadavg")
	}
	return strconv.ParseFloat(fields[0], 64)
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
			// Fallback for service environments without HOME: continue without explicit key path.
			resolvedKeyPath = ""
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

func panelSyncWorkerNodesByIDs(ctx context.Context, dbPath, configPath string, nodeIDs []string) (int, int, error) {
	return panelSyncWorkerNodesByIDsWithPassword(ctx, dbPath, configPath, nodeIDs, "")
}

func panelSyncWorkerNodesByIDsWithPassword(ctx context.Context, dbPath, configPath string, nodeIDs []string, sshPassword string) (int, int, error) {
	settings, err := panelNodeSyncSettingsFromEnv()
	if err != nil {
		return 0, 0, err
	}
	if !settings.enabled {
		return 0, 0, nil
	}
	if strings.TrimSpace(sshPassword) != "" {
		settings.opts.sshPassword = strings.TrimSpace(sshPassword)
	}
	if _, err := lookPath("ssh"); err != nil {
		return 0, 0, fmt.Errorf("ssh client is required for panel node sync: %w", err)
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
	for _, node := range nodes {
		if !node.Enabled || node.Role != domain.NodeRoleNode {
			continue
		}
		if len(filter) > 0 {
			if _, ok := filter[node.ID]; !ok {
				continue
			}
		}
		targeted++

		nodeInbounds := append([]domain.Inbound(nil), inboundsByNode[node.ID]...)
		sort.Slice(nodeInbounds, func(i, j int) bool { return nodeInbounds[i].ID < nodeInbounds[j].ID })
		if len(nodeInbounds) == 0 {
			if _, err := cleanupSingleNodeRuntime(ctx, node, settings.opts, appCfg); err != nil {
				return synced, cleaned, err
			}
			cleaned++
			continue
		}
		nodeCredentials := make([]domain.Credential, 0)
		for _, inbound := range nodeInbounds {
			nodeCredentials = append(nodeCredentials, credentialsByInbound[inbound.ID]...)
		}
		sort.Slice(nodeCredentials, func(i, j int) bool { return nodeCredentials[i].ID < nodeCredentials[j].ID })
		if _, err := lookPath("scp"); err != nil {
			return synced, cleaned, fmt.Errorf("scp client is required for panel node sync: %w", err)
		}

		if _, err := syncSingleNode(ctx, renderer.BuildRequest{
			Node:        node,
			Inbounds:    nodeInbounds,
			Credentials: nodeCredentials,
		}, settings.opts, appCfg); err != nil {
			return synced, cleaned, err
		}
		synced++
	}
	if len(filter) > 0 && targeted == 0 {
		return synced, cleaned, fmt.Errorf("no enabled worker nodes found for requested IDs")
	}
	return synced, cleaned, nil
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
	installCmd := prefix + "bash -lc " + shellQuote(
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

func panelInstallSSHKeyOnNode(ctx context.Context, node domain.Node, sshPassword string) (bool, string, error) {
	settings, err := panelNodeSyncSettingsFromEnv()
	if err != nil {
		return false, "", err
	}
	if !settings.enabled {
		return false, "", fmt.Errorf("%s=0, auto-node-sync is disabled", panelEnvAutoNodeSyncEnabled)
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
		keyPath, generated, genErr = ensureWizardSSHKey(ctx, "~/.ssh/id_ed25519")
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

func panelHandleInboundAction(ctx context.Context, dbPath string, r *http.Request, configPath, dbPathFlag *string, ops *panelOperationFeed) error {
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("invalid inbound action request")
	}
	action := strings.TrimSpace(r.FormValue("op"))
	if action == "" {
		return fmt.Errorf("inbound action is required")
	}

	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	switch action {
	case "create":
		inbound, err := panelInboundFromForm(r, domain.Inbound{})
		if err != nil {
			return err
		}
		created, err := store.Inbounds().Create(ctx, inbound)
		if err != nil {
			return fmt.Errorf("create inbound: %w", err)
		}
		if synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{created.NodeID}); syncErr != nil {
			if panelSyncErrMissingCredentials(syncErr) {
				ops.set("ok", fmt.Sprintf("inbound created: %s (%s:%d) | warning: node sync skipped until at least one credential is attached", created.ID, created.Domain, created.Port))
				return nil
			}
			ops.set("error", fmt.Sprintf("inbound created (%s), but node sync failed: %v", created.ID, syncErr))
			return nil
		} else if synced > 0 || cleaned > 0 {
			ops.set("ok", fmt.Sprintf("inbound created: %s (%s:%d) | node sync: synced=%d cleaned=%d", created.ID, created.Domain, created.Port, synced, cleaned))
			return nil
		}
		ops.set("ok", fmt.Sprintf("inbound created: %s (%s:%d)", created.ID, created.Domain, created.Port))
		return nil
	case "set_enabled":
		inboundID := strings.TrimSpace(r.FormValue("inbound_id"))
		version := strings.TrimSpace(r.FormValue("version"))
		if inboundID == "" {
			return fmt.Errorf("inbound id is required")
		}
		inbounds, err := store.Inbounds().List(ctx)
		if err != nil {
			return fmt.Errorf("list inbounds: %w", err)
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
			return fmt.Errorf("inbound %q not found", inboundID)
		}
		if version != panelInboundVersion(current) {
			return fmt.Errorf("inbound %q changed since page load; refresh and retry", inboundID)
		}
		current.Enabled = panelFormBool(r.FormValue("enabled"))
		stored, err := store.Inbounds().Update(ctx, current)
		if err != nil {
			return fmt.Errorf("set inbound enabled: %w", err)
		}
		if synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{stored.NodeID}); syncErr != nil {
			ops.set("error", fmt.Sprintf("inbound state updated (%s), but node sync failed: %v", stored.ID, syncErr))
			return nil
		} else if synced > 0 || cleaned > 0 {
			state := "disabled"
			if stored.Enabled {
				state = "enabled"
			}
			ops.set("ok", fmt.Sprintf("inbound %s: %s | node sync: synced=%d cleaned=%d", state, stored.ID, synced, cleaned))
			return nil
		}
		state := "disabled"
		if stored.Enabled {
			state = "enabled"
		}
		ops.set("ok", fmt.Sprintf("inbound %s: %s", state, stored.ID))
		return nil
	case "update", "delete":
		inboundID := strings.TrimSpace(r.FormValue("inbound_id"))
		version := strings.TrimSpace(r.FormValue("version"))
		if inboundID == "" {
			return fmt.Errorf("inbound id is required")
		}
		inbounds, err := store.Inbounds().List(ctx)
		if err != nil {
			return fmt.Errorf("list inbounds: %w", err)
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
			return fmt.Errorf("inbound %q not found", inboundID)
		}
		if version != panelInboundVersion(current) {
			return fmt.Errorf("inbound %q changed since page load; refresh and retry", inboundID)
		}

		if action == "delete" {
			deleted, err := store.Inbounds().Delete(ctx, inboundID)
			if err != nil {
				return fmt.Errorf("delete inbound: %w", err)
			}
			if !deleted {
				return fmt.Errorf("inbound %q not found", inboundID)
			}
			synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{current.NodeID})
			out, refreshErr := panelRefreshAllSubscriptions(ctx, configPath, dbPathFlag)
			if syncErr != nil && refreshErr != nil {
				ops.set("error", fmt.Sprintf("inbound deleted (%s), but node sync failed: %v; subscription refresh failed: %s", inboundID, syncErr, panelErrWithOutput(refreshErr, out)))
				return nil
			}
			if syncErr != nil {
				ops.set("error", fmt.Sprintf("inbound deleted (%s), subscriptions refreshed: %s, but node sync failed: %v", inboundID, panelSummarizeOutput(out), syncErr))
				return nil
			}
			if refreshErr != nil {
				ops.set("error", fmt.Sprintf("inbound deleted (%s), but subscription refresh failed: %s", inboundID, panelErrWithOutput(refreshErr, out)))
				return nil
			}
			if synced > 0 || cleaned > 0 {
				ops.set("ok", fmt.Sprintf("inbound deleted: %s | subscriptions refreshed: %s | node sync: synced=%d cleaned=%d", inboundID, panelSummarizeOutput(out), synced, cleaned))
				return nil
			}
			ops.set("ok", fmt.Sprintf("inbound deleted: %s | subscriptions refreshed: %s", inboundID, panelSummarizeOutput(out)))
			return nil
		}

		updated, err := panelInboundFromForm(r, current)
		if err != nil {
			return err
		}
		updated.ID = current.ID
		updated.CreatedAt = current.CreatedAt
		updated.RealityEnabled = current.RealityEnabled
		updated.RealityPublicKey = current.RealityPublicKey
		updated.RealityPrivateKey = current.RealityPrivateKey
		updated.RealityShortID = current.RealityShortID
		updated.RealityFingerprint = current.RealityFingerprint
		updated.RealitySpiderX = current.RealitySpiderX
		updated.RealityServer = current.RealityServer
		updated.RealityServerPort = current.RealityServerPort
		updated.VLESSFlow = current.VLESSFlow
		updated.TLSCertPath = current.TLSCertPath
		updated.TLSKeyPath = current.TLSKeyPath
		if updated.RealityEnabled {
			if strings.ToLower(string(updated.Type)) != string(domain.ProtocolVLESS) || strings.ToLower(strings.TrimSpace(updated.Transport)) != "tcp" || strings.ToLower(string(updated.Engine)) != string(domain.EngineXray) {
				return fmt.Errorf("inbound has reality enabled; keep type=vless transport=tcp engine=xray")
			}
		}
		stored, err := store.Inbounds().Update(ctx, updated)
		if err != nil {
			return fmt.Errorf("update inbound: %w", err)
		}
		if synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{stored.NodeID}); syncErr != nil {
			ops.set("error", fmt.Sprintf("inbound updated (%s), but node sync failed: %v", stored.ID, syncErr))
			return nil
		} else if synced > 0 || cleaned > 0 {
			ops.set("ok", fmt.Sprintf("inbound updated: %s (%s:%d) | node sync: synced=%d cleaned=%d", stored.ID, stored.Domain, stored.Port, synced, cleaned))
			return nil
		}
		ops.set("ok", fmt.Sprintf("inbound updated: %s (%s:%d)", stored.ID, stored.Domain, stored.Port))
		return nil
	default:
		return fmt.Errorf("unknown inbound action")
	}
}

func panelSyncErrMissingCredentials(err error) bool {
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "requires at least one uuid credential") ||
		strings.Contains(msg, "requires at least one password credential")
}

func panelHandleNodeAction(ctx context.Context, dbPath string, r *http.Request, configPath, dbPathFlag *string, ops *panelOperationFeed) error {
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("invalid node action request")
	}
	action := strings.TrimSpace(r.FormValue("op"))
	if action == "" {
		return fmt.Errorf("node action is required")
	}

	store, err := openStoreWithInit(ctx, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	switch action {
	case "create":
		name := strings.TrimSpace(r.FormValue("name"))
		host := strings.TrimSpace(r.FormValue("host"))
		roleRaw := strings.ToLower(strings.TrimSpace(r.FormValue("role")))
		if name == "" {
			return fmt.Errorf("node name is required")
		}
		if host == "" {
			return fmt.Errorf("node host is required")
		}
		role := domain.NodeRoleNode
		if roleRaw == "" || roleRaw == string(domain.NodeRolePrimary) {
			role = domain.NodeRolePrimary
		}
		created, err := store.Nodes().Create(ctx, domain.Node{
			Name:    name,
			Host:    host,
			Role:    role,
			Enabled: true,
		})
		if err != nil {
			return fmt.Errorf("create node: %w", err)
		}
		if created.Role == domain.NodeRoleNode && created.Enabled {
			if bootErr := panelEnsureProxyctlOnNode(ctx, strings.TrimSpace(*configPath), created, ""); bootErr != nil {
				ops.set("error", fmt.Sprintf("node created (%s), but remote bootstrap failed: %v", created.Name, bootErr))
				return nil
			}
			if synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{created.ID}); syncErr != nil {
				ops.set("error", fmt.Sprintf("node created (%s), bootstrap ok, but node sync failed: %v", created.Name, syncErr))
				return nil
			} else if synced > 0 || cleaned > 0 {
				ops.set("ok", fmt.Sprintf("node created: %s (%s) | bootstrap ok | node sync: synced=%d cleaned=%d", created.Name, created.Host, synced, cleaned))
				return nil
			}
			ops.set("ok", fmt.Sprintf("node created: %s (%s) | bootstrap ok", created.Name, created.Host))
			return nil
		}
		ops.set("ok", fmt.Sprintf("node created: %s (%s)", created.Name, created.Host))
		return nil
	case "install_ssh_key", "bootstrap", "test", "set_enabled", "update", "delete":
		nodeID := strings.TrimSpace(r.FormValue("node_id"))
		version := strings.TrimSpace(r.FormValue("version"))
		if nodeID == "" {
			return fmt.Errorf("node id is required")
		}
		nodes, err := store.Nodes().List(ctx)
		if err != nil {
			return fmt.Errorf("list nodes: %w", err)
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
			return fmt.Errorf("node %q not found", nodeID)
		}
		if version != panelNodeVersion(current) {
			return fmt.Errorf("node %q changed since page load; refresh and retry", nodeID)
		}
		if action == "install_ssh_key" {
			if current.Role == domain.NodeRolePrimary {
				ops.set("ok", fmt.Sprintf("ssh key setup skipped: %s (%s) is primary", current.Name, current.Host))
				return nil
			}
			sshPassword := strings.TrimSpace(r.FormValue("ssh_password"))
			generated, keyPath, keyErr := panelInstallSSHKeyOnNode(ctx, current, sshPassword)
			if keyErr != nil {
				ops.set("error", fmt.Sprintf("ssh key setup failed: %s (%s) | %v", current.Name, current.Host, keyErr))
				return nil
			}
			extra := ""
			if generated {
				extra = " | ssh key generated: " + keyPath
			}
			ops.set("ok", fmt.Sprintf("ssh key setup ok: %s (%s)%s", current.Name, current.Host, extra))
			return nil
		}
		if action == "bootstrap" {
			if current.Role == domain.NodeRolePrimary {
				ops.set("ok", fmt.Sprintf("node bootstrap skipped: %s (%s) is primary", current.Name, current.Host))
				return nil
			}
			sshPassword := strings.TrimSpace(r.FormValue("ssh_password"))
			if bootErr := panelEnsureProxyctlOnNode(ctx, strings.TrimSpace(*configPath), current, sshPassword); bootErr != nil {
				ops.set("error", fmt.Sprintf("node bootstrap failed: %s (%s) | %v", current.Name, current.Host, bootErr))
				return nil
			}
			if testErr := panelTestNodeConnectivityWithPassword(ctx, current, sshPassword); testErr != nil {
				ops.set("error", fmt.Sprintf("node bootstrap completed, but test failed: %s (%s) | %v", current.Name, current.Host, testErr))
				return nil
			}
			if synced, cleaned, syncErr := panelSyncWorkerNodesByIDsWithPassword(ctx, dbPath, strings.TrimSpace(*configPath), []string{current.ID}, sshPassword); syncErr != nil {
				ops.set("error", fmt.Sprintf("node bootstrap completed (%s), but node sync failed: %v", current.ID, syncErr))
				return nil
			} else if synced > 0 || cleaned > 0 {
				ops.set("ok", fmt.Sprintf("node bootstrap ok: %s (%s) | node sync: synced=%d cleaned=%d", current.Name, current.Host, synced, cleaned))
				return nil
			}
			ops.set("ok", fmt.Sprintf("node bootstrap ok: %s (%s)", current.Name, current.Host))
			return nil
		}
		if action == "test" {
			if current.Role == domain.NodeRolePrimary {
				ops.set("ok", fmt.Sprintf("node test ok: %s (%s) | primary node: local control-plane check (ssh skipped)", current.Name, current.Host))
				return nil
			}
			if testErr := panelTestNodeConnectivityWithPassword(ctx, current, ""); testErr != nil {
				msg := strings.ToLower(strings.TrimSpace(testErr.Error()))
				if strings.Contains(msg, "proxyctl is not installed") {
					if bootErr := panelEnsureProxyctlOnNode(ctx, strings.TrimSpace(*configPath), current, ""); bootErr != nil {
						ops.set("error", fmt.Sprintf("node test failed: %s (%s) | proxyctl missing and bootstrap failed: %v", current.Name, current.Host, bootErr))
						return nil
					}
					if recheckErr := panelTestNodeConnectivityWithPassword(ctx, current, ""); recheckErr != nil {
						ops.set("error", fmt.Sprintf("node test failed after bootstrap: %s (%s) | %v", current.Name, current.Host, recheckErr))
						return nil
					}
					if synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{current.ID}); syncErr != nil {
						ops.set("error", fmt.Sprintf("node test ok after bootstrap (%s), but node sync failed: %v", current.ID, syncErr))
						return nil
					} else if synced > 0 || cleaned > 0 {
						ops.set("ok", fmt.Sprintf("node test ok: %s (%s) | proxyctl bootstrap completed | node sync: synced=%d cleaned=%d", current.Name, current.Host, synced, cleaned))
						return nil
					}
					ops.set("ok", fmt.Sprintf("node test ok: %s (%s) | proxyctl bootstrap completed", current.Name, current.Host))
					return nil
				}
				ops.set("error", fmt.Sprintf("node test failed: %s (%s) | %v", current.Name, current.Host, testErr))
				return nil
			}
			ops.set("ok", fmt.Sprintf("node test ok: %s (%s)", current.Name, current.Host))
			return nil
		}

		if action == "delete" {
			cleanupWarning := ""
			if current.Role == domain.NodeRoleNode {
				if cleanupErr := panelCleanupNodeRuntime(ctx, strings.TrimSpace(*configPath), current); cleanupErr != nil {
					cleanupWarning = cleanupErr.Error()
				}
			}
			deleted, err := store.Nodes().Delete(ctx, nodeID)
			if err != nil {
				return fmt.Errorf("delete node: %w", err)
			}
			if !deleted {
				return fmt.Errorf("node %q not found", nodeID)
			}
			out, refreshErr := panelRefreshAllSubscriptions(ctx, configPath, dbPathFlag)
			if refreshErr != nil {
				if cleanupWarning != "" {
					ops.set("error", fmt.Sprintf("node deleted (%s), but remote cleanup failed: %s; subscription refresh failed: %s", nodeID, cleanupWarning, panelErrWithOutput(refreshErr, out)))
					return nil
				}
				ops.set("error", fmt.Sprintf("node deleted (%s), but subscription refresh failed: %s", nodeID, panelErrWithOutput(refreshErr, out)))
				return nil
			}
			if current.Role == domain.NodeRoleNode {
				if cleanupWarning != "" {
					ops.set("ok", fmt.Sprintf("node deleted: %s | warning: remote runtime cleanup failed: %s | subscriptions refreshed: %s", nodeID, cleanupWarning, panelSummarizeOutput(out)))
					return nil
				}
				ops.set("ok", fmt.Sprintf("node deleted: %s | remote runtime cleaned | subscriptions refreshed: %s", nodeID, panelSummarizeOutput(out)))
				return nil
			}
			ops.set("ok", fmt.Sprintf("node deleted: %s | subscriptions refreshed: %s", nodeID, panelSummarizeOutput(out)))
			return nil
		}
		if action == "update" {
			prevRole := current.Role
			name := strings.TrimSpace(r.FormValue("name"))
			host := strings.TrimSpace(r.FormValue("host"))
			roleRaw := strings.ToLower(strings.TrimSpace(r.FormValue("role")))
			if name == "" {
				return fmt.Errorf("node name is required")
			}
			if host == "" {
				return fmt.Errorf("node host is required")
			}
			role := domain.NodeRoleNode
			if roleRaw == "" || roleRaw == string(domain.NodeRolePrimary) {
				role = domain.NodeRolePrimary
			}
			current.Name = name
			current.Host = host
			current.Role = role
			updated, err := store.Nodes().Update(ctx, current)
			if err != nil {
				return fmt.Errorf("update node: %w", err)
			}
			if updated.Role == domain.NodeRoleNode && updated.Enabled {
				if bootErr := panelEnsureProxyctlOnNode(ctx, strings.TrimSpace(*configPath), updated, ""); bootErr != nil {
					ops.set("error", fmt.Sprintf("node updated (%s), but remote bootstrap failed: %v", updated.ID, bootErr))
					return nil
				}
				if synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{updated.ID}); syncErr != nil {
					ops.set("error", fmt.Sprintf("node updated (%s), but node sync failed: %v", updated.ID, syncErr))
					return nil
				} else if synced > 0 || cleaned > 0 {
					ops.set("ok", fmt.Sprintf("node updated: %s (%s) | node sync: synced=%d cleaned=%d", updated.Name, updated.Host, synced, cleaned))
					return nil
				}
			}
			if prevRole == domain.NodeRoleNode && (updated.Role != domain.NodeRoleNode || !updated.Enabled) {
				if cleanupErr := panelCleanupNodeRuntime(ctx, strings.TrimSpace(*configPath), updated); cleanupErr != nil {
					ops.set("error", fmt.Sprintf("node updated (%s), but runtime cleanup failed: %v", updated.ID, cleanupErr))
					return nil
				}
				ops.set("ok", fmt.Sprintf("node updated: %s (%s) | remote runtime cleaned", updated.Name, updated.Host))
				return nil
			}
			ops.set("ok", fmt.Sprintf("node updated: %s (%s)", updated.Name, updated.Host))
			return nil
		}

		current.Enabled = panelFormBool(r.FormValue("enabled"))
		updated, err := store.Nodes().Update(ctx, current)
		if err != nil {
			return fmt.Errorf("set node enabled: %w", err)
		}
		if updated.Enabled && updated.Role == domain.NodeRoleNode {
			if bootErr := panelEnsureProxyctlOnNode(ctx, strings.TrimSpace(*configPath), updated, ""); bootErr != nil {
				ops.set("error", fmt.Sprintf("node enabled (%s), but remote bootstrap failed: %v", updated.ID, bootErr))
				return nil
			}
			if synced, cleaned, syncErr := panelSyncWorkerNodesByIDs(ctx, dbPath, strings.TrimSpace(*configPath), []string{updated.ID}); syncErr != nil {
				ops.set("error", fmt.Sprintf("node enabled (%s), but node sync failed: %v", updated.ID, syncErr))
				return nil
			} else if synced > 0 || cleaned > 0 {
				ops.set("ok", fmt.Sprintf("node enabled: %s | node sync: synced=%d cleaned=%d", updated.Name, synced, cleaned))
				return nil
			}
		}
		if !updated.Enabled && updated.Role == domain.NodeRoleNode {
			if cleanupErr := panelCleanupNodeRuntime(ctx, strings.TrimSpace(*configPath), updated); cleanupErr != nil {
				ops.set("error", fmt.Sprintf("node disabled (%s), but runtime cleanup failed: %v", updated.ID, cleanupErr))
				return nil
			}
			ops.set("ok", fmt.Sprintf("node disabled: %s | remote runtime cleaned", updated.Name))
			return nil
		}
		state := "disabled"
		if updated.Enabled {
			state = "enabled"
		}
		ops.set("ok", fmt.Sprintf("node %s: %s", state, updated.Name))
		return nil
	default:
		return fmt.Errorf("unknown node action")
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

		out, runErr := panelExecuteCommand(ctx, newSubscriptionGenerateCmd(configPath, dbPathFlag), []string{userID})
		if runErr != nil {
			ops.set("error", fmt.Sprintf("credential created (%s), but subscription generate failed: %s", created.ID, panelErrWithOutput(runErr, out)))
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

		if strings.TrimSpace(userID) == "" {
			ops.set("ok", fmt.Sprintf("credential detached: %s", credentialID))
			return nil
		}
		out, runErr := panelExecuteCommand(ctx, newSubscriptionGenerateCmd(configPath, dbPathFlag), []string{userID})
		if runErr != nil {
			ops.set("error", fmt.Sprintf("credential detached (%s), but subscription generate failed: %s", credentialID, panelErrWithOutput(runErr, out)))
			return nil
		}
		ops.set("ok", fmt.Sprintf("credential detached: %s | %s", credentialID, panelSummarizeOutput(out)))
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
	base.NodeID = nodeID
	base.Domain = domainName
	base.Port = port
	base.TLSEnabled = panelFormBool(r.FormValue("tls"))
	if base.Type == domain.ProtocolHysteria2 {
		base.TLSEnabled = true
	}
	base.Transport = transport
	base.Path = path
	base.SNI = sni
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
	s := strings.Join([]string{
		strings.TrimSpace(user.ID),
		strings.TrimSpace(user.Name),
		strconv.FormatBool(user.Enabled),
		user.CreatedAt.UTC().Format(time.RFC3339Nano),
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
		strconv.FormatBool(node.Enabled),
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
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
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
