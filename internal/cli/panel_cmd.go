package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"proxyctl/internal/config"
	"proxyctl/internal/domain"
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
	NodeName  string
	Domain    string
	Port      int
	TLS       bool
	Enabled   bool
	Transport string
	Path      string
}

type panelCounts struct {
	UsersTotal     int
	UsersEnabled   int
	InboundsTotal  int
	InboundsActive int
}

type panelPageData struct {
	Title         string
	ActiveTab     string
	BasePath      string
	LogoutPath    string
	ListenAddr    string
	GeneratedAt   string
	Counts        panelCounts
	Units         []panelUnitState
	Users         []domain.User
	Inbounds      []panelInboundView
	Subscriptions []string
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
    .wrap { max-width: 1200px; margin: 0 auto; padding: 24px 16px 44px; }
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
    .section h2 { margin: 0; padding: 12px 14px; font-size: 1rem; border-bottom: 1px solid var(--line); }
    table { width: 100%; border-collapse: collapse; }
    th, td { text-align: left; padding: 9px 12px; border-bottom: 1px solid rgba(148, 163, 184, 0.15); font-size: 0.88rem; }
    th { color: var(--muted); font-weight: 600; }
    tbody tr:hover { background: rgba(15, 23, 42, 0.45); }
    .ok { color: var(--ok); }
    .muted { color: var(--muted); }
    .links { padding: 10px 14px; }
    .links a { color: #a5f3fc; word-break: break-all; }
    .links li { margin: 8px 0; }
    @media (max-width: 720px) {
      .top { flex-direction: column; align-items: flex-start; }
      th, td { padding: 8px; font-size: 0.82rem; }
    }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="top">
      <h1 class="title">proxyctl visual panel (phase 0)</h1>
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
    </nav>

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
      <h2>users</h2>
      <table>
        <thead><tr><th>id</th><th>name</th><th>enabled</th><th>created at</th></tr></thead>
        <tbody>
          {{range .Users}}
          <tr>
            <td>{{.ID}}</td>
            <td>{{.Name}}</td>
            <td class="{{stateClass .Enabled}}">{{yesNo .Enabled}}</td>
            <td>{{timefmt .CreatedAt}}</td>
          </tr>
          {{else}}
          <tr><td colspan="4" class="muted">no users</td></tr>
          {{end}}
        </tbody>
      </table>
    </section>
    {{end}}

    {{if eq .ActiveTab "inbounds"}}
    <section class="section">
      <h2>inbounds</h2>
      <table>
        <thead><tr><th>id</th><th>type</th><th>engine</th><th>node</th><th>domain</th><th>port</th><th>tls</th><th>enabled</th></tr></thead>
        <tbody>
          {{range .Inbounds}}
          <tr>
            <td>{{.ID}}</td>
            <td>{{.Type}}</td>
            <td>{{.Engine}}</td>
            <td>{{.NodeName}}</td>
            <td>{{.Domain}}</td>
            <td>{{.Port}}</td>
            <td class="{{stateClass .TLS}}">{{yesNo .TLS}}</td>
            <td class="{{stateClass .Enabled}}">{{yesNo .Enabled}}</td>
          </tr>
          {{else}}
          <tr><td colspan="8" class="muted">no inbounds</td></tr>
          {{end}}
        </tbody>
      </table>
    </section>
    {{end}}

    {{if eq .ActiveTab "subscriptions"}}
    <section class="section">
      <h2>generated subscription links</h2>
      <div class="links">
        <ul>
          {{range .Subscriptions}}
          <li><a href="{{.}}" target="_blank" rel="noopener noreferrer">{{.}}</a></li>
          {{else}}
          <li class="muted">no public subscription links found</li>
          {{end}}
        </ul>
      </div>
    </section>
    {{end}}
  </div>
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
		Short: "Serve read-only visual panel",
		Long:  "Starts phase-0 read-only visual panel bound to localhost by default.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadAppConfig(*configPath)
			if err != nil {
				return err
			}
			resolvedDB := resolveDBPath(cmd, cfg, *dbPath)

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
			dashboardPath := panelJoin(basePath, "")
			usersPath := panelJoin(basePath, "users")
			inboundsPath := panelJoin(basePath, "inbounds")
			subsPath := panelJoin(basePath, "subscriptions")
			logoutPath := panelJoin(basePath, "logout")

			handlePage := func(tab string) http.HandlerFunc {
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
						Title:         "proxyctl panel",
						ActiveTab:     tab,
						BasePath:      basePath,
						LogoutPath:    logoutPath,
						ListenAddr:    listenAddr,
						GeneratedAt:   time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
						Counts:        snapshot.counts,
						Units:         snapshot.units,
						Users:         snapshot.users,
						Inbounds:      snapshot.inbounds,
						Subscriptions: snapshot.subscriptionLinks,
					}
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					if execErr := panelPageTmpl.Execute(w, data); execErr != nil {
						http.Error(w, "template render failed", http.StatusInternalServerError)
						return
					}
				}
			}

			panelMux.HandleFunc(dashboardPath, handlePage("dashboard"))
			if dashboardPath != "/" {
				panelMux.HandleFunc(panelJoin(basePath, "")+"/", func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, dashboardPath, http.StatusMovedPermanently)
				})
			}
			panelMux.HandleFunc(usersPath, handlePage("users"))
			panelMux.HandleFunc(inboundsPath, handlePage("inbounds"))
			panelMux.HandleFunc(subsPath, handlePage("subscriptions"))
			if dashboardPath != "/" {
				panelMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/" {
						http.Redirect(w, r, dashboardPath, http.StatusFound)
						return
					}
					http.NotFound(w, r)
				})
			}

			var handler http.Handler = panelMux
			if requireAuth {
				auth := newPanelCookieAuth(panelInfo.Login, panelInfo.Password, basePath, dashboardPath, logoutPath)
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
	users             []domain.User
	inbounds          []panelInboundView
	subscriptionLinks []string
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

	nodeNameByID := make(map[string]string, len(nodes))
	for _, node := range nodes {
		nodeNameByID[node.ID] = strings.TrimSpace(node.Name)
	}

	inboundRows := make([]panelInboundView, 0, len(inbounds))
	enabledInbounds := 0
	for _, inbound := range inbounds {
		nodeName := inbound.NodeID
		if name := strings.TrimSpace(nodeNameByID[inbound.NodeID]); name != "" {
			nodeName = name
		}
		inboundRows = append(inboundRows, panelInboundView{
			ID:        inbound.ID,
			Type:      string(inbound.Type),
			Engine:    string(inbound.Engine),
			NodeName:  nodeName,
			Domain:    strings.TrimSpace(inbound.Domain),
			Port:      inbound.Port,
			TLS:       inbound.TLSEnabled,
			Enabled:   inbound.Enabled,
			Transport: strings.TrimSpace(inbound.Transport),
			Path:      strings.TrimSpace(inbound.Path),
		})
		if inbound.Enabled {
			enabledInbounds++
		}
	}
	sort.Slice(inboundRows, func(i, j int) bool { return inboundRows[i].ID < inboundRows[j].ID })

	enabledUsers := 0
	for _, user := range users {
		if user.Enabled {
			enabledUsers++
		}
	}

	subLinks, err := listPanelSubscriptionLinks(cfg)
	if err != nil {
		return panelSnapshot{}, err
	}

	return panelSnapshot{
		counts: panelCounts{
			UsersTotal:     len(users),
			UsersEnabled:   enabledUsers,
			InboundsTotal:  len(inboundRows),
			InboundsActive: enabledInbounds,
		},
		units:             runtimeUnitStates(ctx, cfg),
		users:             users,
		inbounds:          inboundRows,
		subscriptionLinks: subLinks,
	}, nil
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
