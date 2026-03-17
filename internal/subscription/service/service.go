package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"proxyctl/internal/domain"
	"proxyctl/internal/renderer"
	"proxyctl/internal/runtime/layout"
	"proxyctl/internal/storage"
)

const (
	FormatTXT    = "txt"
	FormatBase64 = "base64"
	FormatJSON   = "json"

	DefaultProfileName = "default"
)

type Service struct {
	store           storage.Store
	dataDir         string
	publicDir       string
	publicMirrorDir string
	singBoxRenderer renderer.Service
	xrayRenderer    renderer.Service
	now             func() time.Time
}

type Generated struct {
	User            domain.User
	ProfileName     string
	GeneratedAt     time.Time
	ClientArtifacts []renderer.ClientArtifact
	TXT             []byte
	Base64          []byte
	JSON            []byte
	TXTPath         string
	Base64Path      string
	JSONPath        string
	AccessToken     string
	PublicTXTPath   string
}

type ShowResult struct {
	User        domain.User
	ProfileName string
	Format      string
	Path        string
	Content     []byte
	AccessToken string
}

type profileStore struct {
	Profiles []profileEntry `json:"profiles"`
}

type profileEntry struct {
	Name        string    `json:"name"`
	InboundIDs  []string  `json:"inbound_ids"`
	AccessToken string    `json:"access_token"`
	Enabled     *bool     `json:"enabled,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type jsonExport struct {
	Version     string                 `json:"version"`
	User        jsonUser               `json:"user"`
	GeneratedAt time.Time              `json:"generated_at"`
	Protocols   []domain.Protocol      `json:"protocols"`
	Items       []jsonSubscriptionItem `json:"items"`
}

type jsonUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type jsonSubscriptionItem struct {
	Protocol     domain.Protocol `json:"protocol"`
	Engine       domain.Engine   `json:"engine"`
	NodeID       string          `json:"node_id"`
	NodeName     string          `json:"node_name"`
	NodeHost     string          `json:"node_host"`
	InboundID    string          `json:"inbound_id"`
	CredentialID string          `json:"credential_id"`
	Port         int             `json:"port"`
	Domain       string          `json:"domain,omitempty"`
	Transport    string          `json:"transport,omitempty"`
	TLSEnabled   bool            `json:"tls_enabled"`
	Path         string          `json:"path,omitempty"`
	SNI          string          `json:"sni,omitempty"`
	URI          string          `json:"uri"`
}

func New(store storage.Store, dataDir, publicDir, publicMirrorDir string, singBoxRenderer renderer.Service, xrayRenderer renderer.Service) *Service {
	publicDir = strings.TrimSpace(publicDir)
	if publicDir == "" {
		publicDir = filepath.Join(strings.TrimSpace(dataDir), "public")
	}
	return &Service{
		store:           store,
		dataDir:         dataDir,
		publicDir:       publicDir,
		publicMirrorDir: strings.TrimSpace(publicMirrorDir),
		singBoxRenderer: singBoxRenderer,
		xrayRenderer:    xrayRenderer,
		now:             func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) Build(ctx context.Context, userRef string) (Generated, error) {
	return s.BuildProfile(ctx, userRef, DefaultProfileName, nil)
}

func (s *Service) Generate(ctx context.Context, userRef string) (Generated, error) {
	return s.GenerateProfile(ctx, userRef, DefaultProfileName, nil)
}

func (s *Service) BuildProfile(ctx context.Context, userRef, profileName string, inboundIDs []string) (Generated, error) {
	profileKey, err := normalizeProfileName(profileName)
	if err != nil {
		return Generated{}, err
	}
	user, err := s.resolveUser(ctx, userRef)
	if err != nil {
		return Generated{}, err
	}
	filteredInboundIDs := compactUniqueStrings(inboundIDs)
	if profileKey == DefaultProfileName {
		filteredInboundIDs = nil
	}
	if profileKey != DefaultProfileName && len(filteredInboundIDs) == 0 {
		stored, exists, loadErr := s.loadProfile(user.ID, profileKey)
		if loadErr != nil {
			return Generated{}, loadErr
		}
		if !exists || len(stored.InboundIDs) == 0 {
			return Generated{}, fmt.Errorf("subscription profile %q is not configured; provide inbound ids", profileKey)
		}
		filteredInboundIDs = stored.InboundIDs
	}

	input, err := s.build(ctx, user.ID, toSet(filteredInboundIDs))
	if err != nil {
		return Generated{}, err
	}
	input.ProfileName = profileKey
	return input, nil
}

func (s *Service) GenerateProfile(ctx context.Context, userRef, profileName string, inboundIDs []string) (Generated, error) {
	input, err := s.BuildProfile(ctx, userRef, profileName, inboundIDs)
	if err != nil {
		return Generated{}, err
	}
	profileKey := input.ProfileName

	token := ""
	if profileKey == DefaultProfileName {
		token, err = s.resolveOrCreateAccessToken(ctx, input.User.ID)
		if err != nil {
			return Generated{}, err
		}
	} else {
		entry, _, loadErr := s.loadProfile(input.User.ID, profileKey)
		if loadErr != nil {
			return Generated{}, loadErr
		}
		entry.Name = profileKey
		if len(inboundIDs) > 0 {
			entry.InboundIDs = compactUniqueStrings(inboundIDs)
		}
		if strings.TrimSpace(entry.AccessToken) == "" {
			entry.AccessToken, err = generateAccessToken()
			if err != nil {
				return Generated{}, err
			}
		}
		entry.Enabled = boolPtr(true)
		entry.UpdatedAt = input.GeneratedAt
		if err := s.saveProfile(input.User.ID, entry); err != nil {
			return Generated{}, err
		}
		token = entry.AccessToken
	}

	writer := layout.New(layout.Directories{SubscriptionsDir: s.dataDir})
	var paths layout.SubscriptionPaths
	if profileKey == DefaultProfileName {
		paths, err = writer.WriteSubscriptionFiles(input.User.ID, layout.SubscriptionFiles{
			TXT:    input.TXT,
			Base64: input.Base64,
			JSON:   input.JSON,
		})
		if err != nil {
			return Generated{}, err
		}
	} else {
		paths, err = writer.WriteSubscriptionFilesWithSuffix(input.User.ID, layout.SubscriptionFiles{
			TXT:    input.TXT,
			Base64: input.Base64,
			JSON:   input.JSON,
		}, profileKey)
		if err != nil {
			return Generated{}, err
		}
	}
	publicTXTPath, err := s.writePublicTXT(token, input.TXT)
	if err != nil {
		return Generated{}, err
	}

	if profileKey == DefaultProfileName {
		if _, err := s.store.Subscriptions().Upsert(ctx, domain.Subscription{
			UserID:      input.User.ID,
			Format:      domain.SubscriptionFormat(FormatTXT),
			OutputPath:  paths.TXTPath,
			AccessToken: token,
			Enabled:     true,
			UpdatedAt:   input.GeneratedAt,
		}); err != nil {
			return Generated{}, fmt.Errorf("persist subscription metadata: %w", err)
		}
	}

	input.TXTPath = paths.TXTPath
	input.Base64Path = paths.Base64Path
	input.JSONPath = paths.JSONPath
	input.AccessToken = token
	input.PublicTXTPath = publicTXTPath
	return input, nil
}

func (s *Service) Export(ctx context.Context, userRef, format string) (ShowResult, error) {
	return s.ExportProfile(ctx, userRef, DefaultProfileName, nil, format)
}

func (s *Service) ExportProfile(ctx context.Context, userRef, profileName string, inboundIDs []string, format string) (ShowResult, error) {
	result, err := s.GenerateProfile(ctx, userRef, profileName, inboundIDs)
	if err != nil {
		return ShowResult{}, err
	}

	format = normalizeFormat(format)
	if format == "" {
		format = FormatJSON
	}

	show := ShowResult{User: result.User, ProfileName: result.ProfileName, Format: format, AccessToken: result.AccessToken}
	switch format {
	case FormatTXT:
		show.Path = result.TXTPath
		show.Content = result.TXT
	case FormatBase64:
		show.Path = result.Base64Path
		show.Content = result.Base64
	case FormatJSON:
		show.Path = result.JSONPath
		show.Content = result.JSON
	default:
		return ShowResult{}, fmt.Errorf("unsupported format %q", format)
	}

	if result.ProfileName == DefaultProfileName {
		if _, err := s.store.Subscriptions().Upsert(ctx, domain.Subscription{
			UserID:      result.User.ID,
			Format:      domain.SubscriptionFormat(show.Format),
			OutputPath:  show.Path,
			AccessToken: result.AccessToken,
			Enabled:     true,
			UpdatedAt:   result.GeneratedAt,
		}); err != nil {
			return ShowResult{}, fmt.Errorf("persist subscription metadata: %w", err)
		}
	}
	return show, nil
}

func (s *Service) Show(ctx context.Context, userRef string) (ShowResult, error) {
	return s.ShowProfile(ctx, userRef, DefaultProfileName)
}

func (s *Service) ShowProfile(ctx context.Context, userRef, profileName string) (ShowResult, error) {
	profileKey, err := normalizeProfileName(profileName)
	if err != nil {
		return ShowResult{}, err
	}
	user, err := s.resolveUser(ctx, userRef)
	if err != nil {
		return ShowResult{}, err
	}
	if profileKey != DefaultProfileName {
		entry, exists, loadErr := s.loadProfile(user.ID, profileKey)
		if loadErr != nil {
			return ShowResult{}, loadErr
		}
		if !exists {
			return ShowResult{}, fmt.Errorf("subscription profile %q not found for user %q", profileKey, userRef)
		}
		if entry.Enabled != nil && !*entry.Enabled {
			return ShowResult{}, fmt.Errorf("subscription profile %q is disabled", profileKey)
		}
		path := filepath.Join(strings.TrimSpace(s.dataDir), user.ID+"."+profileKey+".txt")
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return ShowResult{}, fmt.Errorf("read subscription file: %w", readErr)
		}
		return ShowResult{
			User:        user,
			ProfileName: profileKey,
			Format:      FormatTXT,
			Path:        path,
			Content:     content,
			AccessToken: strings.TrimSpace(entry.AccessToken),
		}, nil
	}

	sub, err := s.store.Subscriptions().GetByUserID(ctx, user.ID)
	if err != nil {
		if errorsIsNotFound(err) {
			path := filepath.Join(s.dataDir, user.ID+".txt")
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return ShowResult{}, fmt.Errorf("subscription for user %q is not generated", userRef)
			}
			return ShowResult{User: user, ProfileName: DefaultProfileName, Format: FormatTXT, Path: path, Content: content}, nil
		}
		return ShowResult{}, err
	}
	if !sub.Enabled {
		return ShowResult{}, fmt.Errorf("subscription profile %q is disabled", DefaultProfileName)
	}

	content, err := os.ReadFile(sub.OutputPath)
	if err != nil {
		return ShowResult{}, fmt.Errorf("read subscription file: %w", err)
	}
	return ShowResult{
		User:        user,
		ProfileName: DefaultProfileName,
		Format:      string(sub.Format),
		Path:        sub.OutputPath,
		Content:     content,
		AccessToken: strings.TrimSpace(sub.AccessToken),
	}, nil
}

func (s *Service) build(ctx context.Context, userRef string, allowedInboundIDs map[string]struct{}) (Generated, error) {
	user, err := s.resolveUser(ctx, userRef)
	if err != nil {
		return Generated{}, err
	}

	nodes, err := s.store.Nodes().List(ctx)
	if err != nil {
		return Generated{}, fmt.Errorf("list nodes: %w", err)
	}
	inbounds, err := s.store.Inbounds().List(ctx)
	if err != nil {
		return Generated{}, fmt.Errorf("list inbounds: %w", err)
	}
	credentials, err := s.store.Credentials().List(ctx)
	if err != nil {
		return Generated{}, fmt.Errorf("list credentials: %w", err)
	}

	nodeByID := make(map[string]domain.Node, len(nodes))
	for _, node := range nodes {
		nodeByID[node.ID] = node
	}
	inboundByID := make(map[string]domain.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}

	userCreds := make([]domain.Credential, 0)
	for _, cred := range credentials {
		if cred.UserID == user.ID {
			if len(allowedInboundIDs) > 0 {
				if _, ok := allowedInboundIDs[cred.InboundID]; !ok {
					continue
				}
			}
			if _, ok := inboundByID[cred.InboundID]; ok {
				userCreds = append(userCreds, cred)
			}
		}
	}
	if len(userCreds) == 0 {
		return Generated{}, fmt.Errorf("user %q has no credentials bound to inbounds", userRef)
	}

	inboundSet := map[string]struct{}{}
	for _, cred := range userCreds {
		inboundSet[cred.InboundID] = struct{}{}
	}

	nodeInbounds := map[string][]domain.Inbound{}
	for _, inbound := range inbounds {
		if _, ok := inboundSet[inbound.ID]; !ok {
			continue
		}
		if _, ok := nodeByID[inbound.NodeID]; !ok {
			continue
		}
		nodeInbounds[inbound.NodeID] = append(nodeInbounds[inbound.NodeID], inbound)
	}

	nodeIDs := make([]string, 0, len(nodeInbounds))
	for nodeID := range nodeInbounds {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)

	allArtifacts := make([]renderer.ClientArtifact, 0)
	for _, nodeID := range nodeIDs {
		node := nodeByID[nodeID]
		req := renderer.BuildRequest{
			Node:        node,
			Inbounds:    nodeInbounds[nodeID],
			Credentials: userCreds,
		}

		if s.singBoxRenderer != nil {
			result, err := s.singBoxRenderer.Render(ctx, req)
			if err != nil {
				return Generated{}, fmt.Errorf("render sing-box client artifacts for node %q: %w", nodeID, err)
			}
			allArtifacts = append(allArtifacts, result.ClientArtifacts...)
		}
		if s.xrayRenderer != nil {
			result, err := s.xrayRenderer.Render(ctx, req)
			if err != nil {
				return Generated{}, fmt.Errorf("render xray client artifacts for node %q: %w", nodeID, err)
			}
			allArtifacts = append(allArtifacts, result.ClientArtifacts...)
		}
	}

	if len(allArtifacts) == 0 {
		return Generated{}, fmt.Errorf("no client artifacts generated for user %q", userRef)
	}

	sort.Slice(allArtifacts, func(i, j int) bool {
		if allArtifacts[i].Protocol != allArtifacts[j].Protocol {
			return allArtifacts[i].Protocol < allArtifacts[j].Protocol
		}
		if allArtifacts[i].InboundID != allArtifacts[j].InboundID {
			return allArtifacts[i].InboundID < allArtifacts[j].InboundID
		}
		return allArtifacts[i].CredentialID < allArtifacts[j].CredentialID
	})

	uris := make([]string, 0, len(allArtifacts))
	for _, item := range allArtifacts {
		if strings.TrimSpace(item.URI) != "" {
			uris = append(uris, item.URI)
		}
	}
	if len(uris) == 0 {
		return Generated{}, fmt.Errorf("generated artifacts do not contain URIs for user %q", userRef)
	}

	txt := []byte(strings.Join(uris, "\n") + "\n")
	b64 := []byte(base64.StdEncoding.EncodeToString(txt))
	generatedAt := s.now()

	jsonData, err := buildJSONExport(user, generatedAt, allArtifacts, inboundByID, nodeByID)
	if err != nil {
		return Generated{}, err
	}

	return Generated{
		User:            user,
		GeneratedAt:     generatedAt,
		ClientArtifacts: allArtifacts,
		TXT:             txt,
		Base64:          b64,
		JSON:            jsonData,
	}, nil
}

func (s *Service) resolveUser(ctx context.Context, userRef string) (domain.User, error) {
	users, err := s.store.Users().List(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("list users: %w", err)
	}
	needle := strings.TrimSpace(userRef)
	if needle == "" {
		return domain.User{}, fmt.Errorf("user reference is required")
	}
	for _, user := range users {
		if user.ID == needle || user.Name == needle {
			return user, nil
		}
	}
	return domain.User{}, fmt.Errorf("user %q not found", userRef)
}

func buildJSONExport(user domain.User, generatedAt time.Time, artifacts []renderer.ClientArtifact, inbounds map[string]domain.Inbound, nodes map[string]domain.Node) ([]byte, error) {
	protocolSet := map[domain.Protocol]struct{}{}
	items := make([]jsonSubscriptionItem, 0, len(artifacts))

	for _, artifact := range artifacts {
		protocolSet[artifact.Protocol] = struct{}{}
		inbound, ok := inbounds[artifact.InboundID]
		if !ok {
			continue
		}
		node := nodes[inbound.NodeID]

		items = append(items, jsonSubscriptionItem{
			Protocol:     artifact.Protocol,
			Engine:       inbound.Engine,
			NodeID:       node.ID,
			NodeName:     node.Name,
			NodeHost:     node.Host,
			InboundID:    inbound.ID,
			CredentialID: artifact.CredentialID,
			Port:         inbound.Port,
			Domain:       inbound.Domain,
			Transport:    inbound.Transport,
			TLSEnabled:   inbound.TLSEnabled,
			Path:         inbound.Path,
			SNI:          inbound.SNI,
			URI:          artifact.URI,
		})
	}

	protocols := make([]domain.Protocol, 0, len(protocolSet))
	for protocol := range protocolSet {
		protocols = append(protocols, protocol)
	}
	sort.Slice(protocols, func(i, j int) bool { return protocols[i] < protocols[j] })

	payload := jsonExport{
		Version: "v1",
		User: jsonUser{
			ID:   user.ID,
			Name: user.Name,
		},
		GeneratedAt: generatedAt,
		Protocols:   protocols,
		Items:       items,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal json subscription export: %w", err)
	}
	return data, nil
}

func (s *Service) resolveOrCreateAccessToken(ctx context.Context, userID string) (string, error) {
	sub, err := s.store.Subscriptions().GetByUserID(ctx, userID)
	if err == nil {
		token := strings.TrimSpace(sub.AccessToken)
		if token != "" {
			return token, nil
		}
	} else if !errorsIsNotFound(err) {
		return "", fmt.Errorf("read existing subscription token: %w", err)
	}

	token, err := generateAccessToken()
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) writePublicTXT(accessToken string, content []byte) (string, error) {
	token := strings.TrimSpace(accessToken)
	if token == "" {
		return "", fmt.Errorf("subscription access token is required")
	}
	publicDir := strings.TrimSpace(s.publicDir)
	if publicDir == "" {
		return "", fmt.Errorf("public subscriptions directory is required")
	}
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		return "", fmt.Errorf("create public subscriptions directory: %w", err)
	}
	publicPath := filepath.Join(publicDir, token+".txt")
	if err := layout.WriteAtomicFile(publicPath, content, 0o644); err != nil {
		return "", fmt.Errorf("write public subscription txt: %w", err)
	}
	publicPathNoExt := filepath.Join(publicDir, token)
	if err := layout.WriteAtomicFile(publicPathNoExt, content, 0o644); err != nil {
		return "", fmt.Errorf("write public subscription token file: %w", err)
	}
	mirrorDir := strings.TrimSpace(s.publicMirrorDir)
	if mirrorDir != "" {
		if err := os.MkdirAll(mirrorDir, 0o755); err != nil {
			return "", fmt.Errorf("create mirror public subscriptions directory: %w", err)
		}
		mirrorPath := filepath.Join(mirrorDir, token+".txt")
		if err := layout.WriteAtomicFile(mirrorPath, content, 0o644); err != nil {
			return "", fmt.Errorf("write mirror public subscription txt: %w", err)
		}
		mirrorPathNoExt := filepath.Join(mirrorDir, token)
		if err := layout.WriteAtomicFile(mirrorPathNoExt, content, 0o644); err != nil {
			return "", fmt.Errorf("write mirror public subscription token file: %w", err)
		}
	}
	return publicPath, nil
}

func generateAccessToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate access token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func normalizeProfileName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return DefaultProfileName, nil
	}
	for _, ch := range name {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' {
			continue
		}
		return "", fmt.Errorf("invalid profile name %q: use [a-z0-9_-]", raw)
	}
	return name, nil
}

func compactUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		v := strings.TrimSpace(value)
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

func toSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		set[v] = struct{}{}
	}
	return set
}

func (s *Service) profilesPath(userID string) string {
	return filepath.Join(strings.TrimSpace(s.dataDir), "profiles", strings.TrimSpace(userID)+".json")
}

func (s *Service) loadProfile(userID, profileKey string) (profileEntry, bool, error) {
	path := s.profilesPath(userID)
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return profileEntry{}, false, nil
		}
		return profileEntry{}, false, fmt.Errorf("read profiles file: %w", err)
	}

	var file profileStore
	if err := json.Unmarshal(content, &file); err != nil {
		return profileEntry{}, false, fmt.Errorf("decode profiles file: %w", err)
	}
	for _, item := range file.Profiles {
		name, normalizeErr := normalizeProfileName(item.Name)
		if normalizeErr != nil {
			continue
		}
		if name == profileKey {
			item.Name = name
			item.InboundIDs = compactUniqueStrings(item.InboundIDs)
			item.AccessToken = strings.TrimSpace(item.AccessToken)
			if item.Enabled == nil {
				item.Enabled = boolPtr(true)
			}
			return item, true, nil
		}
	}
	return profileEntry{}, false, nil
}

func (s *Service) saveProfile(userID string, entry profileEntry) error {
	path := s.profilesPath(userID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create profiles directory: %w", err)
	}

	file := profileStore{}
	content, err := os.ReadFile(path)
	if err == nil {
		if unmarshalErr := json.Unmarshal(content, &file); unmarshalErr != nil {
			return fmt.Errorf("decode profiles file: %w", unmarshalErr)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read profiles file: %w", err)
	}

	entry.Name = strings.TrimSpace(entry.Name)
	entry.InboundIDs = compactUniqueStrings(entry.InboundIDs)
	entry.AccessToken = strings.TrimSpace(entry.AccessToken)
	replaced := false
	for i := range file.Profiles {
		name, normalizeErr := normalizeProfileName(file.Profiles[i].Name)
		if normalizeErr != nil {
			continue
		}
		if name == entry.Name {
			file.Profiles[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		file.Profiles = append(file.Profiles, entry)
	}
	sort.Slice(file.Profiles, func(i, j int) bool {
		return file.Profiles[i].Name < file.Profiles[j].Name
	})

	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode profiles file: %w", err)
	}
	if err := layout.WriteAtomicFile(path, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("write profiles file: %w", err)
	}
	return nil
}

func normalizeFormat(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", FormatTXT, "text":
		return FormatTXT
	case FormatBase64, "b64":
		return FormatBase64
	case FormatJSON:
		return FormatJSON
	default:
		return ""
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func errorsIsNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), sql.ErrNoRows.Error())
}
