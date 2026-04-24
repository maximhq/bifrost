// schemasync validates that Bifrost Go config types stay in sync with
// transports/config.schema.json.
//
// Starting from a configured entry-point type (default: ConfigData in
// transports/bifrost-http/lib), it recursively walks every nested struct
// field via go/types. For each field it verifies:
//
//  1. The json:"X" tag has a corresponding property in config.schema.json at
//     the propagated schema path (handling $ref, allOf, oneOf, if/then/else).
//  2. If the field's Go type is a named string type with const declarations,
//     the set of Go constant values matches the schema's enum array.
//
// Exit 0 on full agreement, 1 on any mismatch.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/constant"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

type entrypoint struct {
	pkg        string // Go import path
	typeName   string // exported type name
	schemaPath string // JSON pointer path in config.schema.json (e.g. "/properties")
	moduleDir  string // directory (relative to --pkg-root) that contains the go.mod
}

var entrypoints = []entrypoint{
	{
		pkg:        "github.com/maximhq/bifrost/transports/bifrost-http/lib",
		typeName:   "ConfigData",
		schemaPath: "", // root schema node — collectProperties will find .properties
		moduleDir:  "transports",
	},
}

// Schema properties that intentionally have no Go counterpart and vice versa.
// Key is a JSON pointer path; value is a short reason.
var ignoreSchemaProps = map[string]string{
	"/properties/$schema": "JSON schema self-reference",
	// GORM foreignKey slice relations that ARE user-submittable config input.
	// Go-side: schemasync skips them via the gorm-tag filter; schema-side:
	// these entries prevent the missing-in-go warning for them.
	"/properties/governance/properties/virtual_keys/items/properties/provider_configs": "gorm fk slice; user-submittable",
	"/properties/governance/properties/virtual_keys/items/properties/mcp_configs":      "gorm fk slice; user-submittable",
	"/properties/governance/properties/routing_rules/items/properties/targets":         "gorm fk slice; user-submittable",
	// MCP headers map<string, EnvVar> — documented escape hatch is envFrom:
	// plus env.X references in values; no chart-native secretRef.
	"/properties/mcp/properties/client_configs/items/properties/headers/additionalProperties": "documented envFrom pattern",
	// Object-storage identity fields (bucket/region/endpoint/project_id) are
	// EnvVar-typed for flexibility but are not inherently secret. Operators
	// can write `env.MY_VAR` in values and use envFrom to inject. Access
	// keys, session tokens, and credentials DO have chart-native secret
	// support via `storage.logsStore.objectStorage.existingSecret`.
	"/properties/logs_store/properties/object_storage/properties/bucket":     "not a secret; env.X + envFrom pattern",
	"/properties/logs_store/properties/object_storage/properties/region":     "not a secret; env.X + envFrom pattern",
	"/properties/logs_store/properties/object_storage/properties/endpoint":   "not a secret; env.X + envFrom pattern",
	"/properties/logs_store/properties/object_storage/properties/project_id": "not a secret; env.X + envFrom pattern",
	// Enterprise-only top-level fields: schema documents them for enterprise
	// deployments; OSS ConfigData struct does not carry these fields.
	"/properties/access_profiles":            "enterprise-only; defined in bifrost-enterprise/lib/config.go",
	"/properties/audit_logs":                 "enterprise-only; defined in bifrost-enterprise/lib/config.go",
	"/properties/cluster_config":             "enterprise-only; defined in bifrost-enterprise/lib/config.go",
	"/properties/guardrails_config":          "enterprise-only; defined in bifrost-enterprise/lib/config.go",
	"/properties/large_payload_optimization": "enterprise-only; defined in bifrost-enterprise/lib/config.go",
	"/properties/load_balancer_config":       "enterprise-only; defined in bifrost-enterprise/lib/config.go",
	"/properties/scim_config":                "enterprise-only; defined in bifrost-enterprise/lib/config.go",
	// Enterprise governance extensions not yet in OSS structs.
	"/properties/governance/properties/business_units":                          "enterprise-only; business unit governance",
	"/properties/governance/properties/teams/items/properties/business_unit_id": "enterprise-only; team→business unit association",
	// Stale schema field: TableTeam uses a budgets[] relationship (team_id FK on
	// TableBudget), never a singular budget_id foreign key.
	"/properties/governance/properties/teams/items/properties/budget_id": "stale; teams use budgets[] relation not budget_id",
	// MCP tool groups are an enterprise governance feature; OSS MCPConfig has no tool_groups field.
	"/properties/mcp/properties/tool_groups": "enterprise-only; MCP tool group governance",
}

// ignoreGoFields keys are "schemaPath|fieldName"; value is the reason.
var ignoreGoFields = map[string]string{
	"|auth_config": "deprecated; moved to governance.auth_config",
	// provider_key_id is the internal DB column resolved from provider_key_name at config load time;
	// schema documents only the human-readable provider_key_name alias.
	"/properties/governance/properties/pricing_overrides/items|provider_key_id": "internal DB column; config uses provider_key_name alias instead",
}

// ignoreGoFieldNames are field names (regardless of parent path) that are
// DB bookkeeping or runtime-derived — never part of user-submitted config.
var ignoreGoFieldNames = map[string]string{
	"created_at":  "DB bookkeeping",
	"updated_at":  "DB bookkeeping",
	"config_hash": "internal hash",
	"status":      "runtime-derived",
	"state":       "runtime-derived",
}

// opaqueLeafTypes are named Go types that have custom JSON marshalling and
// should be treated as leaves. The walker does NOT recurse into their fields,
// and they are collected for downstream checks (e.g., EnvVar → helm secret).
var opaqueLeafTypes = map[string]string{
	"github.com/maximhq/bifrost/core/schemas.EnvVar": "env-aware string; custom JSON",
}

// envVarLocation records where an EnvVar-typed field appears in config.json
// so a downstream pass can confirm the helm chart supports Secret-backed
// injection (existingSecret / secretRef / env.BIFROST_*) for that path.
type envVarLocation struct {
	schemaPath string
	goPath     string
}

// Finding categorises every issue the tool surfaces so the final report can
// group by category and render as a table.
type Finding struct {
	Category string // e.g. "missing-in-schema", "missing-in-go", "enum-drift", "envvar-no-secret"
	Severity string // "ERROR" or "WARN"
	Path     string // schema path or enum path
	Detail   string // field name, Go path, missing/extra values, etc.
	Go       string // Go-side location (package.Type.Field)
}

type checker struct {
	schema map[string]any
	pkgs   map[string]*packages.Package // path → pkg
	// enumConsts[namedType] -> list of string values found in any loaded package
	enumConsts map[string][]string
	// visited type names to break cycles
	visited map[string]bool
	// envVarFields records where EnvVar types occur, for downstream checks
	envVarFields []envVarLocation
	findings     []Finding
}

func main() {
	schemaFlag := flag.String("schema", "transports/config.schema.json", "path to config.schema.json")
	pkgDir := flag.String("pkg-root", ".", "repo root used as packages.Load dir")
	helmValuesFlag := flag.String("helm-values", "helm-charts/bifrost/values.schema.json", "path to helm values.schema.json (for EnvVar secret-support check)")
	helmHelpersFlag := flag.String("helm-helpers", "helm-charts/bifrost/templates/_helpers.tpl", "path to helm _helpers.tpl (for env.BIFROST_* emission detection)")
	flag.Parse()

	schemaBytes, err := os.ReadFile(*schemaFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read schema: %v\n", err)
		os.Exit(2)
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		fmt.Fprintf(os.Stderr, "parse schema: %v\n", err)
		os.Exit(2)
	}

	// Group entrypoints by moduleDir so we load each module's package graph once.
	byModule := map[string][]entrypoint{}
	orderedMods := []string{}
	for _, e := range entrypoints {
		if _, seen := byModule[e.moduleDir]; !seen {
			orderedMods = append(orderedMods, e.moduleDir)
		}
		byModule[e.moduleDir] = append(byModule[e.moduleDir], e)
	}
	absRoot, err := filepath.Abs(*pkgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "abs pkg-root: %v\n", err)
		os.Exit(2)
	}
	// Always use the repo's go.work so local modules resolve against each
	// other (not against registry tarballs). The tool refuses to run without
	// go.work — that's the only configuration bifrost is tested against.
	goworkPath := filepath.Join(absRoot, "go.work")
	if _, err := os.Stat(goworkPath); err != nil {
		fmt.Fprintf(os.Stderr, "schemasync requires go.work at %s: %v\n", goworkPath, err)
		os.Exit(2)
	}

	allPkgs := map[string]*packages.Package{}
	for _, mod := range orderedMods {
		modDir := filepath.Join(absRoot, mod)
		env := append([]string{}, os.Environ()...)
		env = append(env, "GOWORK="+goworkPath)
		cfg := &packages.Config{
			Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports | packages.NeedFiles,
			Dir:  modDir,
			Env:  env,
		}
		imports := []string{}
		for _, e := range byModule[mod] {
			imports = append(imports, e.pkg)
		}
		pkgs, err := packages.Load(cfg, imports...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load %s: %v\n", mod, err)
			os.Exit(2)
		}
		hadLoadErr := false
		packages.Visit(pkgs, nil, func(p *packages.Package) {
			for _, e := range p.Errors {
				fmt.Fprintln(os.Stderr, e)
				hadLoadErr = true
			}
		})
		if hadLoadErr {
			os.Exit(2)
		}
		for k, v := range collectPkgs(pkgs) {
			allPkgs[k] = v
		}
	}

	c := &checker{
		schema:     schema,
		pkgs:       allPkgs,
		enumConsts: map[string][]string{},
		visited:    map[string]bool{},
	}
	c.collectConsts()

	for _, e := range entrypoints {
		p := c.pkgs[e.pkg]
		if p == nil {
			c.add(Finding{Category: "entrypoint", Severity: "ERROR", Detail: "package not loaded: " + e.pkg})
			continue
		}
		obj := p.Types.Scope().Lookup(e.typeName)
		if obj == nil {
			c.add(Finding{Category: "entrypoint", Severity: "ERROR", Detail: fmt.Sprintf("type %s not found in %s", e.typeName, e.pkg)})
			continue
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			c.add(Finding{Category: "entrypoint", Severity: "ERROR", Detail: fmt.Sprintf("%s.%s is not a named type", e.pkg, e.typeName)})
			continue
		}
		c.walkType(named, e.schemaPath, fmt.Sprintf("%s.%s", e.pkg, e.typeName))
	}

	// EnvVar → helm-chart secret-support pass. For each Go field typed as
	// schemas.EnvVar, the helm chart must either (a) emit an env.BIFROST_*
	// placeholder for that JSON path via _helpers.tpl, or (b) expose a
	// secretRef/existingSecret knob in values.schema.json at the equivalent
	// camelCase location. If neither, warn.
	c.checkEnvVarHelmSupport(*helmValuesFlag, *helmHelpersFlag)

	printReport(os.Stderr, c.findings)
	errCount := c.countErrs()
	warnCount := c.countWarns()
	if errCount > 0 {
		fmt.Fprintf(os.Stderr, "\nschemasync: %d errors, %d warnings\n", errCount, warnCount)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "\nschemasync: OK (%d warnings)\n", warnCount)
}

// printReport groups findings by category and prints a markdown-style table
// for each non-empty group.
func printReport(w interface{ Write([]byte) (int, error) }, findings []Finding) {
	if len(findings) == 0 {
		return
	}
	groups := map[string][]Finding{}
	order := []string{}
	for _, f := range findings {
		if _, ok := groups[f.Category]; !ok {
			order = append(order, f.Category)
		}
		groups[f.Category] = append(groups[f.Category], f)
	}
	titles := map[string]string{
		"missing-in-schema":      "Missing in config.schema.json (Go has field, schema doesn't) — ERRORS",
		"missing-in-go":          "Missing in Go (schema has property, ConfigData doesn't) — WARNINGS",
		"enum-drift":             "Enum drift (Go constants vs schema enum array)",
		"enum-no-schema":         "Go enum types with no schema `enum` constraint — WARNINGS",
		"envvar-no-secret":       "EnvVar fields lacking chart-native Secret support — WARNINGS",
		"schema-path-not-found":  "Schema path not found for a walked Go type — ERRORS",
		"entrypoint":             "Entrypoint problems — ERRORS",
	}
	for _, cat := range order {
		items := groups[cat]
		title := titles[cat]
		if title == "" {
			title = cat
		}
		fmt.Fprintf(w.(interface{ Write([]byte) (int, error) }), "\n### %s (%d)\n\n", title, len(items))
		// Pick columns based on category for readability.
		switch cat {
		case "missing-in-schema", "schema-path-not-found":
			renderTable(w, []string{"severity", "schema path", "Go location"}, func() [][]string {
				out := [][]string{}
				for _, f := range items {
					out = append(out, []string{f.Severity, f.Path, f.Go})
				}
				return out
			}())
		case "missing-in-go":
			renderTable(w, []string{"severity", "schema path", "property", "Go parent"}, func() [][]string {
				out := [][]string{}
				for _, f := range items {
					out = append(out, []string{f.Severity, f.Path, f.Detail, f.Go})
				}
				return out
			}())
		case "enum-drift", "enum-no-schema":
			renderTable(w, []string{"severity", "enum path", "drift", "Go location"}, func() [][]string {
				out := [][]string{}
				for _, f := range items {
					out = append(out, []string{f.Severity, f.Path, f.Detail, f.Go})
				}
				return out
			}())
		case "envvar-no-secret":
			renderTable(w, []string{"severity", "config path", "Go location", "note"}, func() [][]string {
				out := [][]string{}
				for _, f := range items {
					out = append(out, []string{f.Severity, f.Path, f.Go, f.Detail})
				}
				return out
			}())
		default:
			renderTable(w, []string{"severity", "detail"}, func() [][]string {
				out := [][]string{}
				for _, f := range items {
					out = append(out, []string{f.Severity, f.Detail})
				}
				return out
			}())
		}
	}
}

// renderTable writes a markdown table. Truncates long cells to keep width sane.
func renderTable(w interface{ Write([]byte) (int, error) }, headers []string, rows [][]string) {
	const maxCol = 80
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	truncate := func(s string) string {
		if len(s) <= maxCol {
			return s
		}
		return s[:maxCol-1] + "…"
	}
	trimmed := make([][]string, len(rows))
	for i, r := range rows {
		trimmed[i] = make([]string, len(r))
		for j, cell := range r {
			trimmed[i][j] = truncate(cell)
			if j < len(widths) && len(trimmed[i][j]) > widths[j] {
				widths[j] = len(trimmed[i][j])
			}
		}
	}
	writeRow := func(cells []string) {
		var sb strings.Builder
		sb.WriteString("| ")
		for i, c := range cells {
			sb.WriteString(c)
			if pad := widths[i] - len(c); pad > 0 {
				sb.WriteString(strings.Repeat(" ", pad))
			}
			sb.WriteString(" | ")
		}
		sb.WriteString("\n")
		_, _ = w.Write([]byte(sb.String()))
	}
	writeRow(headers)
	sep := make([]string, len(headers))
	for i := range headers {
		sep[i] = strings.Repeat("-", widths[i])
	}
	writeRow(sep)
	for _, r := range trimmed {
		writeRow(r)
	}
}

// checkEnvVarHelmSupport verifies that every Go field of type schemas.EnvVar
// has a way to be sourced from a Kubernetes secret via the helm chart. Proof
// of support is any of:
//
//  1. An `env.BIFROST_*` string literal appears in _helpers.tpl (indicating
//     a rewrite is wired up for the corresponding config path), OR
//  2. values.schema.json declares a `secretRef` or `existingSecret` object
//     at the camelCase equivalent of the schema path.
//
// Neither heuristic is perfect — this is a structural review aid, not a
// proof. Treat misses as warnings so they don't block CI on borderline cases.
func (c *checker) checkEnvVarHelmSupport(valuesPath, helpersPath string) {
	helpersBytes, err := os.ReadFile(helpersPath)
	if err != nil {
		c.add(Finding{Category: "envvar-no-secret", Severity: "WARN", Detail: fmt.Sprintf("could not read helm helpers %s: %v — skipping EnvVar helm-support check", helpersPath, err)})
		return
	}
	helpers := string(helpersBytes)
	// Extract every env.BIFROST_* token mentioned in _helpers.tpl.
	envBifrostMentions := map[string]bool{}
	for _, line := range strings.Split(helpers, "\n") {
		// crude extraction: look for "env.BIFROST_X" substrings
		idx := 0
		for idx < len(line) {
			k := strings.Index(line[idx:], "env.BIFROST_")
			if k < 0 {
				break
			}
			start := idx + k
			end := start
			for end < len(line) {
				ch := line[end]
				if ch == '"' || ch == ' ' || ch == '\t' || ch == '}' || ch == ')' {
					break
				}
				end++
			}
			envBifrostMentions[line[start:end]] = true
			idx = end
		}
	}

	valuesBytes, err := os.ReadFile(valuesPath)
	hasValues := err == nil
	var valuesSchema map[string]any
	if hasValues {
		_ = json.Unmarshal(valuesBytes, &valuesSchema)
	}

	for _, loc := range c.envVarFields {
		// Heuristic 1: any env.BIFROST_* is present in helpers — broad acceptance.
		// We can't easily map a specific EnvVar field to a specific env var
		// without per-field config, so we just check that the helpers file
		// has AT LEAST ONE envBifrost mention that maps to this field's path.
		// To make this stricter, we look for a helpers line mentioning either
		// the camelCase field's parent path or an env var matching it.
		camel := schemaPathToCamelCase(loc.schemaPath)
		matched := false
		// Heuristic 2: values.schema.json declares secretRef under the parent path.
		if hasValues && valuesSchema != nil {
			if hasSecretRefAt(valuesSchema, camel) {
				matched = true
			}
		}
		if !matched && len(envBifrostMentions) > 0 {
			// Fall back to "some envBifrost wiring exists somewhere" — we flag it
			// as a weaker hit so maintainers know to verify the mapping manually.
			// Do not accept purely from presence; require a name-similarity match.
			tail := lastSchemaComponent(loc.schemaPath)
			for mention := range envBifrostMentions {
				up := strings.ToUpper(tail)
				if strings.Contains(mention, "_"+up) || strings.HasSuffix(mention, up) {
					matched = true
					break
				}
			}
		}
		if !matched {
			if _, ignored := ignoreSchemaProps[loc.schemaPath]; ignored {
				continue
			}
			c.add(Finding{
				Category: "envvar-no-secret",
				Severity: "WARN",
				Path:     loc.schemaPath,
				Detail:   "helm has no secretRef/existingSecret at " + camel + " or parent",
				Go:       loc.goPath,
			})
		}
	}
}

// schemaPathToCamelCase converts a JSON pointer like
// "/properties/governance/properties/auth_config/properties/admin_username"
// into a best-effort camelCase helm values path like
// "properties.bifrost.properties.governance.properties.authConfig.properties.adminUsername".
func schemaPathToCamelCase(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	out := []string{"properties", "bifrost"}
	for _, part := range parts {
		if part == "" {
			continue
		}
		if part == "properties" {
			out = append(out, "properties")
			continue
		}
		out = append(out, snakeToCamel(part))
	}
	return strings.Join(out, ".")
}

func snakeToCamel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func lastSchemaComponent(p string) string {
	parts := strings.Split(p, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" && parts[i] != "properties" {
			return parts[i]
		}
	}
	return ""
}

// hasSecretRefAt returns true if EITHER (a) the target subtree declares a
// secretRef/existingSecret/*Secret knob inside its own "properties", OR
// (b) a SIBLING of the target (at the same properties-map level) is named
// "<target>Secret" / "secretRef" / "existingSecret" / has "Secret" suffix.
// Sibling match is how the helm chart's encryptionKey + encryptionKeySecret
// pattern works: the Secret-source knob is a sibling of the field itself.
func hasSecretRefAt(schema map[string]any, dotted string) bool {
	parts := strings.Split(dotted, ".")
	var cur any = schema
	var propsAtTarget map[string]any // map in which the last non-"properties" part lives
	var targetName string
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		// Resolve $ref at this node before descending.
		if ref, ok := m["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
			resolved := jsonPointerGet(schema, strings.TrimPrefix(ref, "#/"))
			if rm, ok := resolved.(map[string]any); ok {
				m = rm
			}
		}
		if p != "properties" {
			propsAtTarget = m
			targetName = p
		}
		next, present := m[p]
		if !present {
			break
		}
		cur = next
	}
	// (a) target itself declares a Secret knob in its own properties.
	if m, ok := cur.(map[string]any); ok && secretRefPresent(m) {
		return true
	}
	// (b) a sibling of target matches <target>Secret or a generic Secret knob.
	if propsAtTarget != nil && targetName != "" {
		for k := range propsAtTarget {
			if k == targetName {
				continue
			}
			if k == "secretRef" || k == "existingSecret" {
				return true
			}
			if strings.HasSuffix(k, "Secret") || strings.HasSuffix(k, "SecretRef") {
				return true
			}
		}
	}
	return false
}

// jsonPointerGet resolves a /-delimited JSON Pointer into a schema root.
// Used by hasSecretRefAt to follow $ref entries in helm values.schema.json.
func jsonPointerGet(root any, pointer string) any {
	if pointer == "" {
		return root
	}
	parts := strings.Split(pointer, "/")
	cur := root
	for _, p := range parts {
		p = strings.ReplaceAll(p, "~1", "/")
		p = strings.ReplaceAll(p, "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
		if cur == nil {
			return nil
		}
	}
	return cur
}

func secretRefPresent(m map[string]any) bool {
	if m == nil {
		return false
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return false
	}
	for k := range props {
		if k == "secretRef" || k == "existingSecret" || k == "encryptionKeySecret" {
			return true
		}
		if strings.HasSuffix(k, "Secret") || strings.HasSuffix(k, "SecretRef") {
			return true
		}
	}
	return false
}

func collectPkgs(roots []*packages.Package) map[string]*packages.Package {
	out := map[string]*packages.Package{}
	packages.Visit(roots, nil, func(p *packages.Package) {
		out[p.PkgPath] = p
	})
	return out
}

func (c *checker) add(f Finding) { c.findings = append(c.findings, f) }

// countErrs returns the number of ERROR-severity findings.
func (c *checker) countErrs() int {
	n := 0
	for _, f := range c.findings {
		if f.Severity == "ERROR" {
			n++
		}
	}
	return n
}

// countWarns returns the number of WARN-severity findings.
func (c *checker) countWarns() int {
	n := 0
	for _, f := range c.findings {
		if f.Severity == "WARN" {
			n++
		}
	}
	return n
}

// collectConsts scans all loaded packages for `const X NamedStringType = "v"`
// and indexes them by namedType key "pkgpath.TypeName".
func (c *checker) collectConsts() {
	for _, p := range c.pkgs {
		if p.Types == nil {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			cnst, ok := obj.(*types.Const)
			if !ok {
				continue
			}
			named, ok := cnst.Type().(*types.Named)
			if !ok {
				continue
			}
			// Only named string types
			basic, ok := named.Underlying().(*types.Basic)
			if !ok || basic.Kind() != types.String {
				continue
			}
			key := named.Obj().Pkg().Path() + "." + named.Obj().Name()
			v := cnst.Val()
			if v.Kind() != constant.String {
				continue
			}
			c.enumConsts[key] = append(c.enumConsts[key], constant.StringVal(v))
		}
	}
	for k := range c.enumConsts {
		sort.Strings(c.enumConsts[k])
	}
}

// walkType recursively walks a struct type, verifying each json-tagged field
// has a schema counterpart at the propagated schemaPath.
func (c *checker) walkType(t types.Type, schemaPath, goPath string) {
	t = deref(t)
	named, _ := t.(*types.Named)
	if named != nil {
		key := named.Obj().Pkg().Path() + "." + named.Obj().Name()
		// Treat opaque types (like schemas.EnvVar) as leaves.
		if _, isOpaque := opaqueLeafTypes[key]; isOpaque {
			if key == "github.com/maximhq/bifrost/core/schemas.EnvVar" {
				c.envVarFields = append(c.envVarFields, envVarLocation{schemaPath, goPath})
			}
			return
		}
		if c.visited[key+"@"+schemaPath] {
			return
		}
		c.visited[key+"@"+schemaPath] = true
	}
	structType, ok := t.Underlying().(*types.Struct)
	if !ok {
		return
	}

	schemaNode := c.resolveSchema(schemaPath)
	if schemaNode == nil {
		c.add(Finding{Category: "schema-path-not-found", Severity: "ERROR", Path: schemaPath, Go: goPath})
		return
	}

	// Collect every property key reachable from this schema node across
	// properties/allOf/oneOf/anyOf/if-then-else branches.
	schemaProps := c.collectProperties(schemaNode, schemaPath)

	goFieldTags := map[string]*types.Var{}
	for i := 0; i < structType.NumFields(); i++ {
		f := structType.Field(i)
		if !f.Exported() {
			continue
		}
		tag := reflectTag(structType.Tag(i), "json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		// Skip GORM relational fields (populated from joins; never user-submitted).
		// GORM relational fields (`foreignKey`, `many2many`) are populated
		// from DB joins, not user-submitted config. Skip them from the walk so
		// schemasync only compares user-input config against config.schema.json.
		// Schema properties for these relations may still exist (validated at
		// the missing-in-go layer); add the schema path to ignoreSchemaProps
		// for deliberate exceptions (see below for `provider_configs`, etc.).
		gormTag := reflectTag(structType.Tag(i), "gorm")
		if strings.Contains(gormTag, "foreignKey") || strings.Contains(gormTag, "many2many") {
			continue
		}
		goFieldTags[name] = f
	}

	// Go-field → schema check
	for name, f := range goFieldTags {
		childPath := schemaPath + "/properties/" + name
		if _, ignored := ignoreGoFields[schemaPath+"|"+name]; ignored {
			continue
		}
		if _, ignored := ignoreGoFieldNames[name]; ignored {
			continue
		}
		childSchema := schemaProps[name]
		if childSchema == nil {
			c.add(Finding{
				Category: "missing-in-schema",
				Severity: "ERROR",
				Path:     schemaPath + "/properties/" + name,
				Detail:   name,
				Go:       goPath + "." + f.Name(),
			})
			continue
		}
		// Recurse into field type
		c.walkField(f.Type(), childSchema, childPath, goPath+"."+f.Name())
	}

	// Schema-key → Go field check (warnings; schema may legitimately be broader)
	for name := range schemaProps {
		if _, ignored := ignoreSchemaProps[schemaPath+"/properties/"+name]; ignored {
			continue
		}
		if _, ignored := ignoreGoFields[schemaPath+"|"+name]; ignored {
			continue
		}
		if _, ok := goFieldTags[name]; !ok {
			c.add(Finding{
				Category: "missing-in-go",
				Severity: "WARN",
				Path:     schemaPath + "/properties/" + name,
				Detail:   name,
				Go:       goPath,
			})
		}
	}
}

// walkField dispatches based on the field's Go type.
func (c *checker) walkField(t types.Type, schemaNode map[string]any, schemaPath, goPath string) {
	t = deref(t)

	// Named type → opaque-leaf check + enum check (if string const type)
	if named, ok := t.(*types.Named); ok {
		key := named.Obj().Pkg().Path() + "." + named.Obj().Name()
		if _, isOpaque := opaqueLeafTypes[key]; isOpaque {
			if key == "github.com/maximhq/bifrost/core/schemas.EnvVar" {
				c.envVarFields = append(c.envVarFields, envVarLocation{schemaPath, goPath})
			}
			return // do not recurse into opaque types
		}
		if goVals, hasConsts := c.enumConsts[key]; hasConsts && len(goVals) > 0 {
			c.checkEnum(goVals, schemaNode, schemaPath, goPath, key)
		}
	}

	switch u := t.Underlying().(type) {
	case *types.Struct:
		// Recurse into named struct (anonymous structs are inlined below)
		if _, ok := t.(*types.Named); ok {
			c.walkType(t, schemaPath, goPath)
		} else {
			// Anonymous inline struct — rare but handle by walking tags
			c.walkAnonymous(u, schemaPath, goPath)
		}
	case *types.Slice:
		elem := u.Elem()
		if _, isStruct := deref(elem).Underlying().(*types.Struct); isStruct {
			itemsNode := c.resolveRef(schemaNode)
			if _, ok := itemsNode["items"].(map[string]any); ok {
				c.walkType(elem, schemaPath+"/items", goPath+"[]")
			}
		}
	case *types.Array:
		elem := u.Elem()
		if _, isStruct := deref(elem).Underlying().(*types.Struct); isStruct {
			itemsNode := c.resolveRef(schemaNode)
			if _, ok := itemsNode["items"].(map[string]any); ok {
				c.walkType(elem, schemaPath+"/items", goPath+"[]")
			}
		}
	case *types.Map:
		elem := u.Elem()
		if _, isStruct := deref(elem).Underlying().(*types.Struct); isStruct {
			node := c.resolveRef(schemaNode)
			if _, ok := node["additionalProperties"].(map[string]any); ok {
				c.walkType(elem, schemaPath+"/additionalProperties", goPath+"[]")
			}
			// If no additionalProperties/patternProperties, silently skip — schemas
			// often describe provider-keyed maps via oneOf branches.
		}
	case *types.Basic, *types.Interface:
		// Leaf — nothing to recurse into.
	}
}

// walkAnonymous handles anonymous (inline) struct fields — rare; we treat
// them as a struct walk at the same schemaPath.
func (c *checker) walkAnonymous(st *types.Struct, schemaPath, goPath string) {
	// Not common in this codebase; fall back to flat tag-check.
	schemaNode := c.resolveSchema(schemaPath)
	if schemaNode == nil {
		return
	}
	props := c.collectProperties(schemaNode, schemaPath)
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if !f.Exported() {
			continue
		}
		tag := reflectTag(st.Tag(i), "json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if _, ok := props[name]; !ok {
			c.add(Finding{
				Category: "missing-in-schema",
				Severity: "ERROR",
				Path:     schemaPath + "/properties/" + name,
				Detail:   name,
				Go:       goPath + "." + f.Name(),
			})
		}
	}
}

// checkEnum diffs Go string-const values against schema enum array.
func (c *checker) checkEnum(goVals []string, schemaNode map[string]any, schemaPath, goPath, typeKey string) {
	node := c.resolveRef(schemaNode)
	rawEnum, ok := node["enum"]
	if !ok {
		c.add(Finding{
			Category: "enum-no-schema",
			Severity: "WARN",
			Path:     schemaPath,
			Detail:   fmt.Sprintf("%v (Go consts)", goVals),
			Go:       typeKey,
		})
		return
	}
	enumArr, ok := rawEnum.([]any)
	if !ok {
		c.add(Finding{Category: "enum-drift", Severity: "ERROR", Path: schemaPath, Detail: "schema enum is not an array"})
		return
	}
	schemaSet := map[string]bool{}
	for _, v := range enumArr {
		if s, ok := v.(string); ok {
			schemaSet[s] = true
		}
	}
	goSet := map[string]bool{}
	for _, v := range goVals {
		goSet[v] = true
	}
	var missingInSchema, extraInSchema []string
	for v := range goSet {
		if !schemaSet[v] {
			missingInSchema = append(missingInSchema, v)
		}
	}
	for v := range schemaSet {
		if !goSet[v] {
			extraInSchema = append(extraInSchema, v)
		}
	}
	sort.Strings(missingInSchema)
	sort.Strings(extraInSchema)
	if len(missingInSchema) > 0 {
		c.add(Finding{
			Category: "enum-drift",
			Severity: "ERROR",
			Path:     schemaPath,
			Detail:   fmt.Sprintf("schema missing Go consts %v", missingInSchema),
			Go:       goPath + " (" + typeKey + ")",
		})
	}
	if len(extraInSchema) > 0 {
		c.add(Finding{
			Category: "enum-drift",
			Severity: "WARN",
			Path:     schemaPath,
			Detail:   fmt.Sprintf("schema has %v with no Go const", extraInSchema),
			Go:       typeKey,
		})
	}
}

// collectProperties walks the schema subtree rooted at `node`, unioning
// property keys from the direct `properties`, and recursively from `allOf`,
// `oneOf`, `anyOf`, `then`, `else`. Handles $ref.
// Returns map of propertyName → subschema.
func (c *checker) collectProperties(node map[string]any, atPath string) map[string]map[string]any {
	out := map[string]map[string]any{}
	c.mergeProperties(out, node, atPath, map[string]bool{})
	return out
}

func (c *checker) mergeProperties(out map[string]map[string]any, node map[string]any, atPath string, seen map[string]bool) {
	if node == nil {
		return
	}
	node = c.resolveRef(node)
	if ref, ok := node["$ref"].(string); ok && seen[ref] {
		return
	}
	if ref, ok := node["$ref"].(string); ok {
		seen[ref] = true
	}
	if props, ok := node["properties"].(map[string]any); ok {
		for k, v := range props {
			if m, ok := v.(map[string]any); ok {
				if _, already := out[k]; !already {
					out[k] = m
				}
			}
		}
	}
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		if arr, ok := node[key].([]any); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					c.mergeProperties(out, m, atPath+"/"+key, seen)
				}
			}
		}
	}
	for _, key := range []string{"then", "else"} {
		if m, ok := node[key].(map[string]any); ok {
			c.mergeProperties(out, m, atPath+"/"+key, seen)
		}
	}
}

// resolveSchema walks a JSON-pointer path into c.schema, resolving $ref at
// each intermediate node.
func (c *checker) resolveSchema(path string) map[string]any {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var cur any = c.schema
	for _, p := range parts {
		if p == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		m = c.resolveRef(m)
		cur = m[unescapeJSONPointer(p)]
		if cur == nil {
			return nil
		}
	}
	if m, ok := cur.(map[string]any); ok {
		return c.resolveRef(m)
	}
	return nil
}

// resolveRef follows a $ref pointer (recursively) to the final target node.
// $ref values are expected as "#/$defs/xxx" style JSON pointers.
func (c *checker) resolveRef(node map[string]any) map[string]any {
	for i := 0; i < 16; i++ {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		if !strings.HasPrefix(ref, "#/") {
			return node // external refs unsupported
		}
		parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
		var cur any = c.schema
		ok2 := true
		for _, p := range parts {
			m, isMap := cur.(map[string]any)
			if !isMap {
				ok2 = false
				break
			}
			cur = m[unescapeJSONPointer(p)]
			if cur == nil {
				ok2 = false
				break
			}
		}
		if !ok2 {
			return node
		}
		next, ok := cur.(map[string]any)
		if !ok {
			return node
		}
		node = next
	}
	return node
}

func unescapeJSONPointer(s string) string {
	s = strings.ReplaceAll(s, "~1", "/")
	s = strings.ReplaceAll(s, "~0", "~")
	return s
}

// deref strips pointer wrappers to get the underlying type.
func deref(t types.Type) types.Type {
	for {
		p, ok := t.(*types.Pointer)
		if !ok {
			return t
		}
		t = p.Elem()
	}
}

// reflectTag parses a single struct-tag key; mirrors reflect.StructTag.Get.
func reflectTag(tag, key string) string {
	for tag != "" {
		for tag != "" && tag[0] == ' ' {
			tag = tag[1:]
		}
		i := 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			break
		}
		name := tag[:i]
		tag = tag[i+1:]
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		val := tag[1:i]
		tag = tag[i+1:]
		if name == key {
			return val
		}
	}
	return ""
}
