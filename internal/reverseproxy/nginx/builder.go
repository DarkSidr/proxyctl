package nginx

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

const nginxTemplateName = "nginx.conf.tmpl"

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
	Prefix    string
	Backend   string
	Transport string
}

// BuildRequest contains node and inbound data needed for nginx config generation.
type BuildRequest struct {
	Node     domain.Node
	Inbounds []domain.Inbound
}

// BuildResult contains generated nginx config and derived route metadata.
type BuildResult struct {
	NginxConfig []byte
	Domains     []string
	Routes      []Route
}

// Builder generates nginx config from app config + runtime request.
type Builder struct {
	cfg config.AppConfig
}

// New creates a new nginx builder.
func New(cfg config.AppConfig) *Builder {
	return &Builder{cfg: cfg}
}

// Build renders nginx config for reverse proxy and decoy site routing.
func (b *Builder) Build(req BuildRequest) (BuildResult, error) {
	siteMap := map[string][]Route{}
	for _, inbound := range req.Inbounds {
		route, ok := buildRoute(b.cfg, req.Node, inbound)
		if !ok {
			continue
		}
		siteMap[route.Domain] = append(siteMap[route.Domain], route)
	}

	domains := make([]string, 0, len(siteMap))
	for domainName := range siteMap {
		domains = append(domains, domainName)
	}
	sort.Strings(domains)
	if len(domains) == 0 {
		return BuildResult{}, fmt.Errorf("no HTTP reverse-proxy inbounds found for nginx (supported transports: ws|grpc|xhttp)")
	}

	tplData := nginxTemplateData{
		Sites: make([]nginxSiteData, 0, len(domains)),
	}
	for _, domainName := range domains {
		routes := append([]Route(nil), siteMap[domainName]...)
		sort.Slice(routes, func(i, j int) bool {
			if routes[i].Path != routes[j].Path {
				return routes[i].Path < routes[j].Path
			}
			return routes[i].InboundID < routes[j].InboundID
		})

		site := nginxSiteData{
			Domain:           domainName,
			DecoyRoot:        b.cfg.Paths.DecoySiteDir,
			SubscriptionRoot: filepath.Join(b.cfg.Paths.Subscription, "public"),
			Routes:           make([]nginxRouteData, 0, len(routes)),
		}
		for _, route := range routes {
			site.Routes = append(site.Routes, nginxRouteData{
				Path:      route.Path,
				Prefix:    route.Prefix,
				Backend:   route.Backend,
				Transport: route.Transport,
			})
			tplData.Routes = append(tplData.Routes, route)
		}
		tplData.Sites = append(tplData.Sites, site)
	}

	tpl, err := loadTemplate(b.cfg.Paths.TemplatesDir)
	if err != nil {
		return BuildResult{}, err
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, tplData); err != nil {
		return BuildResult{}, fmt.Errorf("execute nginx template: %w", err)
	}

	return BuildResult{
		NginxConfig: buf.Bytes(),
		Domains:     domains,
		Routes:      tplData.Routes,
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

type nginxTemplateData struct {
	Sites  []nginxSiteData
	Routes []Route
}

type nginxSiteData struct {
	Domain           string
	DecoyRoot        string
	SubscriptionRoot string
	Routes           []nginxRouteData
}

type nginxRouteData struct {
	Path      string
	Prefix    string
	Backend   string
	Transport string
}

func loadTemplate(templatesDir string) (*template.Template, error) {
	path, err := resolveExistingPath(
		filepath.Join(templatesDir, "nginx", nginxTemplateName),
		filepath.Join("templates", "nginx", nginxTemplateName),
	)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read nginx template %q: %w", path, err)
	}
	return template.New("nginx").Parse(string(content))
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
	if inbound.Port < 1 || inbound.Port > 65535 {
		return Route{}, false
	}

	domainName := publicDomain(cfg, node, inbound)
	if domainName == "" {
		return Route{}, false
	}

	path := normalizePath(inbound.Path)
	return Route{
		Domain:    domainName,
		InboundID: inbound.ID,
		Path:      path,
		Prefix:    ensurePrefix(path),
		Backend:   fmt.Sprintf("127.0.0.1:%d", inbound.Port),
		Transport: transport,
	}, true
}

func publicDomain(cfg config.AppConfig, node domain.Node, inbound domain.Inbound) string {
	if strings.TrimSpace(cfg.Public.Domain) != "" {
		return strings.TrimSpace(cfg.Public.Domain)
	}
	if strings.TrimSpace(inbound.Domain) != "" {
		return strings.TrimSpace(inbound.Domain)
	}
	return strings.TrimSpace(node.Host)
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

func ensurePrefix(path string) string {
	if path == "/" {
		return "/"
	}
	if strings.HasSuffix(path, "/") {
		return path
	}
	return path + "/"
}
