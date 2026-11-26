// Package sbsimport (SBS fork) migrates a MetaMCP installation into Bifrost.
//
// It runs as an operator command inside the Bifrost container, against the same config store the
// server uses, and is idempotent: anything that already exists (by name) is left as it is and
// reported. Restart Bifrost afterwards so it connects the new MCP servers.
//
//	/app/main sbs-import-metamcp -dsn postgres://user:pass@metamcp-postgres:5432/metamcp_db [flags]
//
// Mapping:
//   - MCP server -> MCP client. Names lose characters Bifrost forbids ("kali-mcp" -> "kali_mcp").
//     A bearer token becomes an Authorization header; STDIO env becomes KEY=value entries.
//     Servers that point back at a MetaMCP endpoint (nested aggregation) are not imported: the
//     namespaces that used them get the nested namespace's servers instead.
//   - Namespace -> Virtual MCP ("namespace"), slug "ns-<name>" so it never collides with an
//     endpoint name. Inactive servers keep an empty tool list; a server with some inactive
//     tools gets the explicit list of active ones; MetaMCP tool overrides are carried over.
//   - Endpoint -> SBS endpoint of the same name. API-key endpoints require a virtual key (and
//     -grant-vk assigns their namespace to those keys); open endpoints become public endpoints
//     acting as one MCP-only virtual key created for them.
package sbsimport

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/handlers"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Options control one import run.
type Options struct {
	DSN           string            // MetaMCP Postgres
	AppDir        string            // Bifrost app dir (config.db)
	GrantVKs      []string          // virtual key names or IDs given access to API-key endpoints' namespaces
	PublicVKName  string            // name of the virtual key public endpoints act as
	CAFiles       map[string]string // host -> PEM file to trust for that host
	InsecureHosts map[string]bool   // hosts whose TLS certificate is not verified
	DryRun        bool
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// RunCLI parses args and runs the import; it returns the process exit code.
func RunCLI(args []string) int {
	fs := flag.NewFlagSet("sbs-import-metamcp", flag.ContinueOnError)
	opts := Options{CAFiles: map[string]string{}, InsecureHosts: map[string]bool{}}
	var grants, cas, insecure listFlag
	fs.StringVar(&opts.DSN, "dsn", os.Getenv("SBS_METAMCP_DSN"), "MetaMCP Postgres DSN (or SBS_METAMCP_DSN)")
	fs.StringVar(&opts.AppDir, "app-dir", envOr("APP_DIR", "/app/data"), "Bifrost app dir holding config.db")
	fs.StringVar(&opts.PublicVKName, "public-vk", "mcp-public", "virtual key that public endpoints act as (created when missing)")
	fs.Var(&grants, "grant-vk", "virtual key (name or ID) to give API-key endpoints' namespaces; repeatable")
	fs.Var(&cas, "ca", "host=/path/to/ca.pem: trust this CA for that host; repeatable")
	fs.Var(&insecure, "insecure-host", "host whose TLS certificate is not verified; repeatable")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "print the plan without writing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if opts.DSN == "" {
		fmt.Fprintln(os.Stderr, "sbs-import-metamcp: -dsn is required")
		return 2
	}
	opts.GrantVKs = grants
	for _, c := range cas {
		host, path, ok := strings.Cut(c, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "sbs-import-metamcp: -ca %q: want host=/path\n", c)
			return 2
		}
		opts.CAFiles[host] = path
	}
	for _, h := range insecure {
		opts.InsecureHosts[h] = true
	}
	report, err := Run(context.Background(), opts)
	for _, line := range report {
		fmt.Println(line)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sbs-import-metamcp:", err)
		return 1
	}
	return 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ---------------------------------------------------------------------------------------------
// MetaMCP model

type mmServer struct {
	UUID        string `gorm:"column:uuid"`
	Name        string `gorm:"column:name"`
	Type        string `gorm:"column:type"`
	Command     *string
	Args        string // postgres text[] rendered by array_to_json
	Env         string
	URL         *string `gorm:"column:url"`
	BearerToken *string `gorm:"column:bearer_token"`
	Headers     string
}

type mmNamespace struct {
	UUID        string `gorm:"column:uuid"`
	Name        string
	Description *string
}

type mmNSServer struct {
	NamespaceUUID string `gorm:"column:namespace_uuid"`
	ServerUUID    string `gorm:"column:mcp_server_uuid"`
	Status        string
}

type mmNSTool struct {
	NamespaceUUID       string `gorm:"column:namespace_uuid"`
	ServerUUID          string `gorm:"column:mcp_server_uuid"`
	ToolName            string `gorm:"column:tool_name"`
	Status              string
	OverrideName        *string `gorm:"column:override_name"`
	OverrideTitle       *string `gorm:"column:override_title"`
	OverrideDescription *string `gorm:"column:override_description"`
	OverrideAnnotations *string `gorm:"column:override_annotations"`
}

type mmEndpoint struct {
	Name          string
	Description   *string
	NamespaceUUID string `gorm:"column:namespace_uuid"`
	APIKeyAuth    bool   `gorm:"column:enable_api_key_auth"`
}

type metamcp struct {
	servers    []mmServer
	namespaces []mmNamespace
	nsServers  []mmNSServer
	nsTools    []mmNSTool
	endpoints  []mmEndpoint
	toolsBySrv map[string][]string // server uuid -> tool names MetaMCP knows
}

func loadMetaMCP(ctx context.Context, dsn string) (*metamcp, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("connect to MetaMCP: %w", err)
	}
	m := &metamcp{toolsBySrv: map[string][]string{}}
	q := func(dst any, sql string) error { return db.WithContext(ctx).Raw(sql).Scan(dst).Error }
	if err := q(&m.servers, `SELECT uuid, name, type::text AS type, command, array_to_json(args)::text AS args, env::text AS env, url, bearer_token, headers::text AS headers FROM mcp_servers ORDER BY name`); err != nil {
		return nil, fmt.Errorf("read mcp_servers: %w", err)
	}
	if err := q(&m.namespaces, `SELECT uuid, name, description FROM namespaces ORDER BY name`); err != nil {
		return nil, fmt.Errorf("read namespaces: %w", err)
	}
	if err := q(&m.nsServers, `SELECT namespace_uuid, mcp_server_uuid, status::text AS status FROM namespace_server_mappings`); err != nil {
		return nil, fmt.Errorf("read namespace_server_mappings: %w", err)
	}
	if err := q(&m.nsTools, `SELECT m.namespace_uuid, m.mcp_server_uuid, t.name AS tool_name, m.status::text AS status, m.override_name, m.override_title, m.override_description, m.override_annotations::text AS override_annotations FROM namespace_tool_mappings m JOIN tools t ON t.uuid = m.tool_uuid`); err != nil {
		return nil, fmt.Errorf("read namespace_tool_mappings: %w", err)
	}
	if err := q(&m.endpoints, `SELECT name, description, namespace_uuid, enable_api_key_auth FROM endpoints ORDER BY name`); err != nil {
		return nil, fmt.Errorf("read endpoints: %w", err)
	}
	var tools []struct {
		ServerUUID string `gorm:"column:mcp_server_uuid"`
		Name       string
	}
	if err := q(&tools, `SELECT mcp_server_uuid, name FROM tools`); err != nil {
		return nil, fmt.Errorf("read tools: %w", err)
	}
	for _, t := range tools {
		m.toolsBySrv[t.ServerUUID] = append(m.toolsBySrv[t.ServerUUID], t.Name)
	}
	return m, nil
}

// selfEndpoint returns the MetaMCP endpoint a server URL points at, or "" for a real server.
func selfEndpoint(rawURL *string) string {
	if rawURL == nil {
		return ""
	}
	u, err := url.Parse(*rawURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "metamcp" && (parts[2] == "mcp" || parts[2] == "sse") {
		return parts[1]
	}
	return ""
}

var invalidNameChars = regexp.MustCompile(`[^A-Za-z0-9_]`)

// clientName turns a MetaMCP server name into a valid Bifrost MCP client name.
func clientName(name string) string {
	n := invalidNameChars.ReplaceAllString(name, "_")
	if n == "" || (n[0] >= '0' && n[0] <= '9') {
		n = "s_" + n
	}
	return n
}

var slugChars = regexp.MustCompile(`[^a-z0-9]+`)

func namespaceSlug(name string) string {
	return "ns-" + strings.Trim(slugChars.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// ---------------------------------------------------------------------------------------------
// Import

type importer struct {
	ctx    context.Context
	opts   Options
	store  configstore.ConfigStore
	mm     *metamcp
	report []string
	// MetaMCP server uuid -> Bifrost client ID (real servers only)
	clientIDs map[string]string
}

func (im *importer) logf(format string, args ...any) {
	im.report = append(im.report, fmt.Sprintf(format, args...))
}

// Run performs the import and returns a human-readable report.
func Run(ctx context.Context, opts Options) ([]string, error) {
	mm, err := loadMetaMCP(ctx, opts.DSN)
	if err != nil {
		return nil, err
	}
	logger := bifrost.NewDefaultLogger(schemas.LogLevelWarn)
	if key := os.Getenv("BIFROST_ENCRYPTION_KEY"); key != "" {
		encrypt.Init(key, logger)
	}
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(opts.AppDir, "config.db")},
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("open Bifrost config store: %w", err)
	}
	if db := store.DB(); db != nil {
		if err := db.WithContext(ctx).AutoMigrate(&handlers.TableSBSMCPEndpoint{}); err != nil {
			return nil, fmt.Errorf("prepare sbs_mcp_endpoints: %w", err)
		}
	}
	im := &importer{ctx: ctx, opts: opts, store: store, mm: mm, clientIDs: map[string]string{}}
	if opts.DryRun {
		im.logf("DRY RUN: nothing is written")
	}
	if err := im.importServers(); err != nil {
		return im.report, err
	}
	nsIDs, err := im.importNamespaces()
	if err != nil {
		return im.report, err
	}
	if err := im.importEndpoints(nsIDs); err != nil {
		return im.report, err
	}
	im.logf("done. Restart Bifrost so it connects the imported MCP servers.")
	return im.report, nil
}

func (im *importer) importServers() error {
	existing, err := im.store.GetMCPConfig(im.ctx)
	if err != nil {
		return fmt.Errorf("read Bifrost MCP clients: %w", err)
	}
	byName := map[string]string{}
	if existing != nil {
		for _, c := range existing.ClientConfigs {
			if c != nil {
				byName[c.Name] = c.ID
			}
		}
	}
	for _, s := range im.mm.servers {
		if ep := selfEndpoint(s.URL); ep != "" {
			im.logf("server %-30s skipped: points back at MetaMCP endpoint %q (its servers are imported directly)", s.Name, ep)
			continue
		}
		name := clientName(s.Name)
		if id, ok := byName[name]; ok {
			im.clientIDs[s.UUID] = id
			im.logf("server %-30s exists in Bifrost as %s: kept", s.Name, name)
			continue
		}
		cfg, err := im.clientConfig(s, name)
		if err != nil {
			im.logf("server %-30s NOT imported: %v", s.Name, err)
			continue
		}
		if !im.opts.DryRun {
			if err := im.store.CreateMCPClientConfig(im.ctx, cfg); err != nil {
				im.logf("server %-30s NOT imported: %v", s.Name, err)
				continue
			}
		}
		im.clientIDs[s.UUID] = cfg.ID
		im.logf("server %-30s -> MCP server %s (%s)", s.Name, name, cfg.ConnectionType)
	}
	return nil
}

func (im *importer) clientConfig(s mmServer, name string) (*schemas.MCPClientConfig, error) {
	ping := true
	cfg := &schemas.MCPClientConfig{
		ID:              uuid.NewString(),
		Name:            name,
		ToolsToExecute:  schemas.WhiteList{"*"},
		IsPingAvailable: &ping,
		AuthType:        schemas.MCPAuthTypeNone,
	}
	headers := map[string]string{}
	if strings.TrimSpace(s.Headers) != "" {
		_ = json.Unmarshal([]byte(s.Headers), &headers)
	}
	if s.BearerToken != nil && *s.BearerToken != "" {
		headers["Authorization"] = "Bearer " + *s.BearerToken
	}
	switch s.Type {
	case "STDIO":
		if s.Command == nil || *s.Command == "" {
			return nil, fmt.Errorf("STDIO server without a command")
		}
		var args []string
		_ = json.Unmarshal([]byte(s.Args), &args)
		env := map[string]string{}
		_ = json.Unmarshal([]byte(s.Env), &env)
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		envs := make([]string, 0, len(keys))
		for _, k := range keys {
			envs = append(envs, k+"="+env[k])
		}
		cfg.ConnectionType = schemas.MCPConnectionTypeSTDIO
		cfg.StdioConfig = &schemas.MCPStdioConfig{Command: *s.Command, Args: args, Envs: envs}
	case "STREAMABLE_HTTP", "SSE":
		if s.URL == nil || *s.URL == "" {
			return nil, fmt.Errorf("%s server without a URL", s.Type)
		}
		cfg.ConnectionType = schemas.MCPConnectionTypeHTTP
		if s.Type == "SSE" {
			cfg.ConnectionType = schemas.MCPConnectionTypeSSE
		}
		cfg.ConnectionString = schemas.NewSecretVar(*s.URL)
		if u, err := url.Parse(*s.URL); err == nil && u.Scheme == "https" {
			host := u.Hostname()
			if im.opts.InsecureHosts[host] {
				cfg.TLSConfig = &schemas.MCPTLSConfig{InsecureSkipVerify: true}
			} else if path, ok := im.opts.CAFiles[host]; ok {
				pem, err := os.ReadFile(path)
				if err != nil {
					return nil, fmt.Errorf("read CA for %s: %w", host, err)
				}
				cfg.TLSConfig = &schemas.MCPTLSConfig{CACertPEM: schemas.NewSecretVar(string(pem))}
			}
		}
	default:
		return nil, fmt.Errorf("unsupported server type %q", s.Type)
	}
	if len(headers) > 0 {
		cfg.Headers = make(map[string]schemas.SecretVar, len(headers))
		for k, v := range headers {
			cfg.Headers[k] = *schemas.NewSecretVar(v)
		}
		cfg.AuthType = schemas.MCPAuthTypeHeaders
	}
	return cfg, nil
}

// nsSpecs builds a namespace's tool specs, flattening servers that point at another MetaMCP endpoint.
func (im *importer) nsSpecs(nsUUID string, seen map[string]bool) map[string]tables.MCPToolSpec {
	specs := map[string]tables.MCPToolSpec{}
	if seen[nsUUID] {
		return specs
	}
	seen[nsUUID] = true
	serverByUUID := map[string]mmServer{}
	for _, s := range im.mm.servers {
		serverByUUID[s.UUID] = s
	}
	nsByEndpoint := map[string]string{}
	for _, e := range im.mm.endpoints {
		nsByEndpoint[e.Name] = e.NamespaceUUID
	}
	for _, link := range im.mm.nsServers {
		if link.NamespaceUUID != nsUUID {
			continue
		}
		srv := serverByUUID[link.ServerUUID]
		if ep := selfEndpoint(srv.URL); ep != "" {
			if link.Status != "ACTIVE" {
				continue
			}
			for id, spec := range im.nsSpecs(nsByEndpoint[ep], seen) {
				if _, own := specs[id]; !own {
					specs[id] = spec
				}
			}
			continue
		}
		clientID := im.clientIDs[link.ServerUUID]
		if clientID == "" {
			continue
		}
		spec := tables.MCPToolSpec{MCPClientID: clientID, ToolNames: []string{}}
		if link.Status == "ACTIVE" {
			spec.ToolNames = im.activeTools(nsUUID, link.ServerUUID)
		}
		spec.Overrides = im.overrides(nsUUID, link.ServerUUID)
		specs[clientID] = spec
	}
	return specs
}

// activeTools is ["*"] when no tool of the server is switched off in the namespace, else the active ones.
func (im *importer) activeTools(nsUUID, serverUUID string) []string {
	inactive := map[string]bool{}
	for _, t := range im.mm.nsTools {
		if t.NamespaceUUID == nsUUID && t.ServerUUID == serverUUID && t.Status != "ACTIVE" {
			inactive[t.ToolName] = true
		}
	}
	if len(inactive) == 0 {
		return []string{"*"}
	}
	active := []string{}
	for _, name := range im.mm.toolsBySrv[serverUUID] {
		if !inactive[name] {
			active = append(active, name)
		}
	}
	sort.Strings(active)
	return active
}

func (im *importer) overrides(nsUUID, serverUUID string) map[string]tables.MCPToolOverride {
	out := map[string]tables.MCPToolOverride{}
	for _, t := range im.mm.nsTools {
		if t.NamespaceUUID != nsUUID || t.ServerUUID != serverUUID {
			continue
		}
		ov := tables.MCPToolOverride{Name: t.OverrideName, Title: t.OverrideTitle, Description: t.OverrideDescription}
		if t.OverrideAnnotations != nil && *t.OverrideAnnotations != "" && *t.OverrideAnnotations != "null" {
			var ann map[string]any
			if json.Unmarshal([]byte(*t.OverrideAnnotations), &ann) == nil {
				if inner, ok := ann["annotations"].(map[string]any); ok {
					ann = inner
				}
				ov.Annotations = ann
			}
		}
		if ov.Name == nil && ov.Title == nil && ov.Description == nil && len(ov.Annotations) == 0 {
			continue
		}
		out[t.ToolName] = ov
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (im *importer) importNamespaces() (map[string]uint, error) {
	ids := map[string]uint{} // MetaMCP namespace uuid -> Virtual MCP id
	existing, err := im.store.GetVirtualMCPs(im.ctx)
	if err != nil {
		return nil, fmt.Errorf("read Bifrost namespaces: %w", err)
	}
	bySlug, byName := map[string]tables.TableVirtualMCP{}, map[string]bool{}
	for _, def := range existing {
		bySlug[def.EndpointSlug] = def
		byName[def.Name] = true
	}
	for _, ns := range im.mm.namespaces {
		// An earlier run's import is recognised by its slug; a namespace of the same name that
		// Bifrost already had for something else is kept apart under a suffixed name.
		slug := namespaceSlug(ns.Name)
		if def, ok := bySlug[slug]; ok {
			ids[ns.UUID] = def.ID
			im.logf("namespace %-27s already imported as %q (id %d): kept", ns.Name, def.Name, def.ID)
			continue
		}
		name := ns.Name
		if byName[name] {
			name = ns.Name + " (MetaMCP)"
		}
		specMap := im.nsSpecs(ns.UUID, map[string]bool{})
		clientIDs := make([]string, 0, len(specMap))
		for id := range specMap {
			clientIDs = append(clientIDs, id)
		}
		sort.Strings(clientIDs)
		specs := make([]tables.MCPToolSpec, 0, len(specMap))
		for _, id := range clientIDs {
			specs = append(specs, specMap[id])
		}
		def := &tables.TableVirtualMCP{Name: name, EndpointSlug: slug, Description: ns.Description, Enabled: true, ParsedTools: specs}
		if !im.opts.DryRun {
			if err := im.store.CreateVirtualMCP(im.ctx, def); err != nil {
				im.logf("namespace %-27s NOT imported: %v", ns.Name, err)
				continue
			}
		}
		ids[ns.UUID] = def.ID
		im.logf("namespace %-27s -> namespace %q at /mcp/%s with %d server(s)", ns.Name, name, def.EndpointSlug, len(specs))
	}
	return ids, nil
}

// virtualKey finds a virtual key by ID or name.
func (im *importer) virtualKey(ref string) (*tables.TableVirtualKey, error) {
	if vk, err := im.store.GetVirtualKey(im.ctx, ref); err == nil && vk != nil {
		return vk, nil
	}
	db := im.store.DB()
	var vk tables.TableVirtualKey
	if err := db.WithContext(im.ctx).Where("name = ?", ref).First(&vk).Error; err != nil {
		return nil, fmt.Errorf("virtual key %q not found", ref)
	}
	return &vk, nil
}

// publicKey returns the virtual key public endpoints act as, creating an MCP-only one when missing.
func (im *importer) publicKey() (*tables.TableVirtualKey, error) {
	if vk, err := im.virtualKey(im.opts.PublicVKName); err == nil {
		return vk, nil
	}
	active := true
	description := "SBS: public MCP endpoints imported from MetaMCP act as this key. No model providers."
	vk := &tables.TableVirtualKey{
		ID:          uuid.NewString(),
		Name:        im.opts.PublicVKName,
		Value:       *schemas.NewSecretVar(governance.GenerateVirtualKey()),
		Description: description,
		IsActive:    &active,
	}
	if im.opts.DryRun {
		return vk, nil
	}
	if err := im.store.CreateVirtualKey(im.ctx, vk); err != nil {
		return nil, fmt.Errorf("create virtual key %q: %w", im.opts.PublicVKName, err)
	}
	im.logf("virtual key %q created for public endpoints", im.opts.PublicVKName)
	return vk, nil
}

func (im *importer) attach(vmcpID uint, vkID string) error {
	if im.opts.DryRun {
		return nil
	}
	ids, err := im.store.GetVirtualKeyIDsForVirtualMCP(im.ctx, vmcpID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if id == vkID {
			return nil
		}
	}
	return im.store.AttachVirtualMCPToVirtualKey(im.ctx, vmcpID, vkID)
}

func (im *importer) importEndpoints(nsIDs map[string]uint) error {
	db := im.store.DB()
	var grantKeys []*tables.TableVirtualKey
	for _, ref := range im.opts.GrantVKs {
		vk, err := im.virtualKey(ref)
		if err != nil {
			return err
		}
		grantKeys = append(grantKeys, vk)
	}
	for _, e := range im.mm.endpoints {
		vmcpID, ok := nsIDs[e.NamespaceUUID]
		if !ok {
			im.logf("endpoint %-28s NOT imported: its namespace was not imported", e.Name)
			continue
		}
		var count int64
		db.WithContext(im.ctx).Model(&handlers.TableSBSMCPEndpoint{}).Where("name = ?", e.Name).Count(&count)
		if count > 0 {
			im.logf("endpoint %-28s exists in Bifrost: kept", e.Name)
			continue
		}
		if taken, _ := im.store.MCPEndpointSlugTaken(im.ctx, e.Name); taken {
			im.logf("endpoint %-28s NOT imported: /mcp/%s is already the URL of a Bifrost MCP server or namespace; rename one of them and re-run", e.Name, e.Name)
			continue
		}
		ep := handlers.TableSBSMCPEndpoint{
			Name:           e.Name,
			Description:    e.Description,
			VirtualMCPID:   vmcpID,
			Enabled:        true,
			AuthMode:       handlers.SBSEndpointAuthVirtualKey,
			OpenAPIEnabled: true,
		}
		access := "virtual key required"
		if e.APIKeyAuth {
			for _, vk := range grantKeys {
				if err := im.attach(vmcpID, vk.ID); err != nil {
					return fmt.Errorf("give %q access: %w", vk.Name, err)
				}
				access += ", granted to " + vk.Name
			}
		} else {
			vk, err := im.publicKey()
			if err != nil {
				return err
			}
			if err := im.attach(vmcpID, vk.ID); err != nil {
				return fmt.Errorf("give %q access: %w", vk.Name, err)
			}
			ep.AuthMode = handlers.SBSEndpointAuthPublic
			ep.PublicVirtualKeyID = &vk.ID
			ep.PublicGrantAdded = true
			access = "public (acts as " + vk.Name + ")"
		}
		if !im.opts.DryRun {
			if err := db.WithContext(im.ctx).Create(&ep).Error; err != nil {
				im.logf("endpoint %-28s NOT imported: %v", e.Name, err)
				continue
			}
		}
		im.logf("endpoint %-28s -> /mcp/%s, %s", e.Name, e.Name, access)
	}
	return nil
}
