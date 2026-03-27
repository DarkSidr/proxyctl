package caddy

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"proxyctl/internal/config"
	"proxyctl/internal/domain"
)

const caddyTemplateName = "Caddyfile.tmpl"

const caddyTemplateFallback = `{
  acme_ca https://acme-v02.api.letsencrypt.org/directory
{{- if .ContactEmail }}
  email {{ .ContactEmail }}
{{- end }}
}


{{- range .Sites }}
{{ .Address }} {
  root * {{ .DecoyRoot }}

{{- if and .PanelPath .PanelPort }}
  handle {{ .PanelPath }}* {
    reverse_proxy 127.0.0.1:{{ .PanelPort }}
  }
{{- end }}

  handle_path /sub/* {
    root * {{ .SubscriptionRoot }}
    try_files {path} {path}.txt =404
    header Content-Type "text/plain; charset=utf-8"
    file_server
  }

{{- range .Routes }}
  @{{ .MatcherName }} {
    path {{ .Path }} {{ .PrefixPath }}
  }
  reverse_proxy @{{ .MatcherName }} {{ .Backend }}{{ if .UseH2C }} {
    transport http {
      versions h2c 2
    }
  }{{ end }}

{{- end }}
  file_server
}

{{- end }}

{{- range .SelfStealSites }}
{{ .Address }} {
  bind 127.0.0.1
  root * {{ .DecoyRoot }}
  file_server
}

{{- end }}
`

// Asset is one static decoy-site file loaded from templates.
type Asset struct {
	RelativePath string
	Content      []byte
}

// Route describes one public path routed to a backend endpoint.
type Route struct {
	Domain    string
	InboundID string
	Path      string
	Backend   string
	Transport string
	UseH2C    bool
}

// BuildRequest contains node and inbound data needed for Caddyfile generation.
type BuildRequest struct {
	Node          domain.Node
	Inbounds      []domain.Inbound
	PanelPath     string // if non-empty, inject panel reverse-proxy route (e.g. "/mi0a34mkrs3akogd")
	PanelPort     string // panel listen port on 127.0.0.1 (e.g. "20443")
	SelfStealPort int    // internal port for Reality self-steal (default 8443)
}

// BuildResult contains generated Caddyfile and derived route metadata.
type BuildResult struct {
	Caddyfile []byte
	Domains   []string
	Routes    []Route
}

// Builder generates Caddy config from app config + runtime request.
type Builder struct {
	cfg config.AppConfig
}

// New creates a new Caddy builder.
func New(cfg config.AppConfig) *Builder {
	return &Builder{cfg: cfg}
}

// Build renders a Caddyfile for reverse proxy and decoy site routing.
func (b *Builder) Build(req BuildRequest) (BuildResult, error) {
	siteMap := map[string][]Route{}
	for _, inbound := range req.Inbounds {
		route, ok := buildRoute(b.cfg, req.Node, inbound)
		if !ok {
			continue
		}
		siteMap[route.Domain] = append(siteMap[route.Domain], route)
	}

	// Include domains from TLS-enabled non-HTTP inbounds (e.g. Hysteria2) so
	// caddy triggers ACME cert acquisition for them even without HTTP routes.
	for _, inbound := range req.Inbounds {
		if !inbound.Enabled {
			continue
		}
		if !needsCaddyCert(inbound) {
			continue
		}
		domainName := publicDomain(b.cfg, req.Node, inbound)
		if domainName == "" {
			continue
		}
		if _, exists := siteMap[domainName]; !exists {
			siteMap[domainName] = []Route{}
		}
	}

	// If a panel route is requested, ensure the public domain (or node host)
	// is present in siteMap even when no inbounds produce caddy routes.
	if strings.TrimSpace(req.PanelPath) != "" {
		panelDomain := strings.TrimSpace(b.cfg.Public.Domain)
		if panelDomain == "" {
			panelDomain = strings.TrimSpace(req.Node.Host)
		}
		if panelDomain != "" {
			if _, exists := siteMap[panelDomain]; !exists {
				siteMap[panelDomain] = []Route{}
			}
		}
	}

	domains := make([]string, 0, len(siteMap))
	for domainName := range siteMap {
		domains = append(domains, domainName)
	}
	sort.Strings(domains)
	if len(domains) == 0 {
		return BuildResult{}, fmt.Errorf("no inbounds require caddy: no HTTP reverse-proxy routes and no TLS-only inbounds found")
	}

	tplData := caddyTemplateData{}
	if strings.TrimSpace(b.cfg.Public.ContactEmail) != "" {
		tplData.ContactEmail = strings.TrimSpace(b.cfg.Public.ContactEmail)
	}
	for _, domainName := range domains {
		routes := append([]Route(nil), siteMap[domainName]...)
		sort.Slice(routes, func(i, j int) bool {
			if routes[i].Path != routes[j].Path {
				return routes[i].Path < routes[j].Path
			}
			return routes[i].InboundID < routes[j].InboundID
		})

		site := caddySiteData{
			Address:          siteAddress(domainName, b.cfg.Public.HTTPS),
			DecoyRoot:        b.cfg.Paths.DecoySiteDir,
			SubscriptionRoot: filepath.Join(b.cfg.Paths.Subscription, "public"),
			Routes:           make([]caddyRouteData, 0, len(routes)),
			PanelPath:        strings.TrimSpace(req.PanelPath),
			PanelPort:        strings.TrimSpace(req.PanelPort),
		}
		for idx, route := range routes {
			site.Routes = append(site.Routes, caddyRouteData{
				MatcherName: fmt.Sprintf("route_%d", idx+1),
				Path:        route.Path,
				PrefixPath:  ensurePrefixPath(route.Path),
				Backend:     route.Backend,
				UseH2C:      route.UseH2C,
			})
			tplData.Routes = append(tplData.Routes, route)
		}
		tplData.Sites = append(tplData.Sites, site)
	}

	// Build self-steal internal listener blocks (bind 127.0.0.1:selfStealPort).
	selfStealPort := req.SelfStealPort
	if selfStealPort <= 0 {
		selfStealPort = 8443
	}
	selfStealDomains := map[string]struct{}{}
	for _, inbound := range req.Inbounds {
		if !inbound.Enabled || !inbound.RealityEnabled || !inbound.SelfSteal {
			continue
		}
		d := publicDomain(b.cfg, req.Node, inbound)
		if d == "" {
			continue
		}
		if err := rejectConfigInjection("domain", d); err != nil {
			continue
		}
		selfStealDomains[d] = struct{}{}
	}
	selfStealDomainsSorted := make([]string, 0, len(selfStealDomains))
	for d := range selfStealDomains {
		selfStealDomainsSorted = append(selfStealDomainsSorted, d)
	}
	sort.Strings(selfStealDomainsSorted)
	for _, d := range selfStealDomainsSorted {
		tplData.SelfStealSites = append(tplData.SelfStealSites, caddySelfStealSiteData{
			Address:   fmt.Sprintf("%s:%d", d, selfStealPort),
			DecoyRoot: b.cfg.Paths.DecoySiteDir,
		})
	}

	tpl, err := loadTemplate(b.cfg.Paths.TemplatesDir)
	if err != nil {
		return BuildResult{}, err
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, tplData); err != nil {
		return BuildResult{}, fmt.Errorf("execute caddy template: %w", err)
	}

	return BuildResult{
		Caddyfile: buf.Bytes(),
		Domains:   domains,
		Routes:    tplData.Routes,
	}, nil
}

// LoadDecoyAssets reads decoy-site static files from templates.
func LoadDecoyAssets(cfg config.AppConfig) ([]Asset, error) {
	baseDir, err := resolveExistingPath(
		filepath.Join(cfg.Paths.TemplatesDir, "decoy-site"),
		filepath.Join("templates", "decoy-site"),
	)
	if err != nil {
		return nil, err
	}

	entries := make([]Asset, 0)
	walkErr := filepath.WalkDir(baseDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, Asset{RelativePath: filepath.ToSlash(rel), Content: content})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("load decoy assets: %w", walkErr)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].RelativePath < entries[j].RelativePath
	})
	return entries, nil
}

type caddyTemplateData struct {
	ContactEmail   string
	Sites          []caddySiteData
	SelfStealSites []caddySelfStealSiteData
	Routes         []Route
}

type caddySelfStealSiteData struct {
	Address   string // e.g. "example.com:8443"
	DecoyRoot string
}

type caddySiteData struct {
	Address          string
	DecoyRoot        string
	SubscriptionRoot string
	Routes           []caddyRouteData
	PanelPath        string
	PanelPort        string
}

type caddyRouteData struct {
	MatcherName string
	Path        string
	PrefixPath  string
	Backend     string
	UseH2C      bool
}

func loadTemplate(templatesDir string) (*template.Template, error) {
	path, err := resolveExistingPath(
		filepath.Join(templatesDir, "caddy", caddyTemplateName),
		filepath.Join("templates", "caddy", caddyTemplateName),
	)
	if err != nil {
		return template.New("caddy").Parse(caddyTemplateFallback)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read caddy template %q: %w", path, err)
	}
	return template.New("caddy").Parse(string(content))
}

// rejectConfigInjection returns an error if s contains characters that could
// break out of a config file block (newlines, carriage returns).
func rejectConfigInjection(field, s string) error {
	if strings.ContainsAny(s, "\n\r") {
		return fmt.Errorf("invalid %s: must not contain newline characters", field)
	}
	return nil
}

func resolveExistingPath(candidates ...string) (string, error) {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("template path not found; checked: %s", strings.Join(candidates, ", "))
}

func buildRoute(cfg config.AppConfig, node domain.Node, inbound domain.Inbound) (Route, bool) {
	if !inbound.Enabled {
		return Route{}, false
	}
	transport := strings.ToLower(strings.TrimSpace(inbound.Transport))
	if transport != "ws" && transport != "grpc" && transport != "xhttp" {
		return Route{}, false
	}
	// xhttp with TLS: xray handles TLS directly using caddy-managed certs.
	// Caddy must not reverse-proxy to it — a bare domain entry for ACME cert
	// acquisition is added separately via needsCaddyCert.
	if transport == "xhttp" && inbound.TLSEnabled {
		return Route{}, false
	}
	if inbound.Port < 1 || inbound.Port > 65535 {
		return Route{}, false
	}

	domainName := publicDomain(cfg, node, inbound)
	if domainName == "" {
		return Route{}, false
	}
	if err := rejectConfigInjection("domain", domainName); err != nil {
		return Route{}, false
	}
	path := normalizePath(inbound.Path)
	if err := rejectConfigInjection("path", path); err != nil {
		return Route{}, false
	}

	return Route{
		Domain:    domainName,
		InboundID: inbound.ID,
		Path:      path,
		Backend:   fmt.Sprintf("127.0.0.1:%d", inbound.Port),
		Transport: transport,
		UseH2C:    transport == "grpc",
	}, true
}

func publicDomain(cfg config.AppConfig, node domain.Node, inbound domain.Inbound) string {
	if strings.TrimSpace(inbound.Domain) != "" {
		return strings.TrimSpace(inbound.Domain)
	}
	if strings.TrimSpace(node.Host) != "" {
		return strings.TrimSpace(node.Host)
	}
	return strings.TrimSpace(cfg.Public.Domain)
}

func siteAddress(domainName string, https bool) string {
	if !https {
		return "http://" + domainName
	}
	return domainName
}

func normalizePath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return "/"
	}
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

func ensurePrefixPath(path string) string {
	if path == "/" {
		return "/*"
	}
	if strings.HasSuffix(path, "/*") {
		return path
	}
	if strings.HasSuffix(path, "/") {
		return path + "*"
	}
	return path + "/*"
}

// needsCaddyCert reports whether an inbound relies on caddy-managed ACME certs.
// This is true when TLS is enabled, Reality is not used (Reality has its own
// key-pair), and no explicit cert path is configured (the renderer falls back
// to the caddy cert path at /caddy/certificates/...).
func needsCaddyCert(inbound domain.Inbound) bool {
	return inbound.TLSEnabled &&
		!inbound.RealityEnabled &&
		strings.TrimSpace(inbound.TLSCertPath) == ""
}
