package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// MCPUserDiscovery is one caller's discovery on one MCP client with per_user_discovery on: who
// holds it, keyed by MCPUserDiscoveryKey, and the discovery itself, which the store interns so
// callers holding the same discovery share one copy.
type MCPUserDiscovery struct {
	// ID is the binding's key, MCPUserDiscoveryKey of its auth mode, identity and client; Mode,
	// Identity and MCPClientID are its parts, held as substrings of ID.
	ID, Mode, Identity, MCPClientID string
	// Discovery is the discovery the caller holds, with bare tool and prompt names; nil when its
	// row went between the two reads that build the record.
	Discovery *MCPUserDiscoveryContent
}

// MCPUserDiscoveryContent is a per-user discovery as stored: tool and prompt names without the
// client prefix, which a reader adds from the client's current name. Hash is the rows'
// discovery_hash, a content hash, so callers with equal discoveries hold equal hashes.
type MCPUserDiscoveryContent struct {
	Hash              string
	Tools             map[string]schemas.ChatTool
	Instructions      string
	Resources         []mcp.Resource
	ResourceTemplates []mcp.ResourceTemplate
	Prompts           []mcp.Prompt
	DiscoveredAt      time.Time
}

// PoolKey is the discovery's key in the shared pool of discoveries: the first sixteen bytes of the
// SHA-256 hex digest in Hash, or the first sixteen bytes of Hash's own SHA-256 when it is not hex.
// Equal keys share one copy, so the key must be collision resistant.
func (d *MCPUserDiscoveryContent) PoolKey() [16]byte {
	var k [16]byte
	if len(d.Hash) >= 2*len(k) {
		if _, err := hex.Decode(k[:], []byte(d.Hash[:2*len(k)])); err == nil {
			return k
		}
	}
	sum := sha256.Sum256([]byte(d.Hash))
	copy(k[:], sum[:])
	return k
}

// MCPUserDiscoveryByCaller names the index of bindings by caller, keyed by MCPUserDiscoveryCaller.
const MCPUserDiscoveryByCaller = "caller"

// MCPUserDiscoveryKey is the key of a caller's binding on an MCP client: its parts length-prefixed,
// so no identity can run into the client ID.
func MCPUserDiscoveryKey(mode, identity, mcpClientID string) string {
	return string(AppendMCPUserDiscoveryKey(make([]byte, 0, len(mode)+len(identity)+len(mcpClientID)+12), mode, identity, mcpClientID))
}

// AppendMCPUserDiscoveryKey appends MCPUserDiscoveryKey's bytes to dst, for a lookup that builds
// the key in a buffer of its own.
func AppendMCPUserDiscoveryKey(dst []byte, mode, identity, mcpClientID string) []byte {
	for _, part := range [...]string{mode, identity, mcpClientID} {
		dst = strconv.AppendInt(dst, int64(len(part)), 10)
		dst = append(dst, ':')
		dst = append(dst, part...)
	}
	return dst
}

// parseMCPUserDiscoveryKey splits a binding key into its parts, substrings of key, and reports
// false for a string MCPUserDiscoveryKey did not build.
func parseMCPUserDiscoveryKey(key string) (mode, identity, mcpClientID string, ok bool) {
	var parts [3]string
	rest := key
	for i := range parts {
		colon := strings.IndexByte(rest, ':')
		if colon < 0 {
			return "", "", "", false
		}
		n, err := strconv.Atoi(rest[:colon])
		if err != nil || n < 0 || n > len(rest)-colon-1 {
			return "", "", "", false
		}
		parts[i] = rest[colon+1 : colon+1+n]
		rest = rest[colon+1+n:]
	}
	if rest != "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// MCPUserDiscoveryCaller is a caller's key in MCPUserDiscoveryByCaller.
func MCPUserDiscoveryCaller(mode, identity string) string { return mode + ":" + identity }

// MCPUserDiscoveryCallers is a binding's key in MCPUserDiscoveryByCaller.
func MCPUserDiscoveryCallers(d *MCPUserDiscovery) []string {
	return []string{MCPUserDiscoveryCaller(d.Mode, d.Identity)}
}

// MCPUserDiscoveryID is a binding record's primary key.
func MCPUserDiscoveryID(d *MCPUserDiscovery) string { return d.ID }

// MCPUserDiscoveryClientID is the MCP client a binding is on.
func MCPUserDiscoveryClientID(d *MCPUserDiscovery) string { return d.MCPClientID }

// callerModes are the auth modes that keep per-user discoveries.
var callerModes = []string{string(schemas.MCPAuthModeUser), string(schemas.MCPAuthModeVK)}

// bindingColumns are what a binding is read from; the discovery itself is read once per distinct
// hash (readDiscoveryContents), so a read of many callers holding few discoveries decodes each
// discovery once.
var bindingColumns = []string{"id", "auth_mode", "user_id", "virtual_key_id", "mcp_client_id", "discovery_hash"}

// MCPUserDiscoveriesPage reads up to limit bindings after the cursor, in row order, and returns
// the next cursor, "" after the last page. The cursor is the row's own ID, not the binding key.
func MCPUserDiscoveriesPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*MCPUserDiscovery, string, error) {
	var rows []tables.TableMCPPerUserDiscovery
	if err := db.WithContext(ctx).Select(bindingColumns).Where("id > ? AND auth_mode IN ?", after, callerModes).
		Order("id").Limit(limit).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	recs, err := discoveriesFromRows(ctx, db, rows)
	if err != nil || len(rows) < limit {
		return recs, "", err
	}
	return recs, rows[len(rows)-1].ID, nil
}

// MCPUserDiscoveriesByID reads the bindings with these keys; one with no row is left out. It reads
// by client and caller, then keeps the rows whose keys were asked for.
func MCPUserDiscoveriesByID(ctx context.Context, db *gorm.DB, ids []string) ([]*MCPUserDiscovery, error) {
	want := make(map[string]struct{}, len(ids))
	// Empty, not nil: an empty list binds as an empty set on every database.
	clients, users, keys := []string{}, []string{}, []string{}
	for _, id := range ids {
		mode, identity, client, ok := parseMCPUserDiscoveryKey(id)
		if !ok {
			continue
		}
		want[id] = struct{}{}
		clients = append(clients, client)
		switch schemas.MCPAuthMode(mode) {
		case schemas.MCPAuthModeUser:
			users = append(users, identity)
		case schemas.MCPAuthModeVK:
			keys = append(keys, identity)
		}
	}
	if len(want) == 0 {
		return nil, nil
	}
	q := db.WithContext(ctx).Select(bindingColumns).Where(queryscope.InStrings(db, "mcp_client_id", clients))
	userCol, userArg := queryscope.InStrings(db, "user_id", users)
	keyCol, keyArg := queryscope.InStrings(db, "virtual_key_id", keys)
	q = q.Where("(auth_mode = ? AND "+userCol+") OR (auth_mode = ? AND "+keyCol+")",
		string(schemas.MCPAuthModeUser), userArg, string(schemas.MCPAuthModeVK), keyArg)
	var rows []tables.TableMCPPerUserDiscovery
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	kept := rows[:0]
	for i := range rows {
		if _, ok := want[MCPUserDiscoveryKey(rows[i].AuthMode, rows[i].Identity(), rows[i].MCPClientID)]; ok {
			kept = append(kept, rows[i])
		}
	}
	return discoveriesFromRows(ctx, db, kept)
}

// MCPUserDiscoveryIDsByClients returns the keys of the bindings on these MCP clients, for a
// client's refresh, which reads the bindings themselves by key, in batches.
func MCPUserDiscoveryIDsByClients(ctx context.Context, db *gorm.DB, clientIDs []string) ([]string, error) {
	var rows []tables.TableMCPPerUserDiscovery
	if err := db.WithContext(ctx).Select("auth_mode", "user_id", "virtual_key_id", "mcp_client_id").
		Where(queryscope.InStrings(db, "mcp_client_id", clientIDs)).Where("auth_mode IN ?", callerModes).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for i := range rows {
		if identity := rows[i].Identity(); identity != "" {
			out = append(out, MCPUserDiscoveryKey(rows[i].AuthMode, identity, rows[i].MCPClientID))
		}
	}
	return out, nil
}

// discoveriesFromRows builds the records of binding rows, reading each distinct discovery once. A
// row with no identity is left out. A row whose discovery is gone by the time discoveries are read
// moved to another one in between: it is read again whole and built from what it holds now, or
// left out when the row itself is gone.
func discoveriesFromRows(ctx context.Context, db *gorm.DB, rows []tables.TableMCPPerUserDiscovery) ([]*MCPUserDiscovery, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	hashes := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for i := range rows {
		if _, ok := seen[rows[i].DiscoveryHash]; !ok {
			seen[rows[i].DiscoveryHash] = struct{}{}
			hashes = append(hashes, rows[i].DiscoveryHash)
		}
	}
	contents, err := readDiscoveryContents(ctx, db, hashes)
	if err != nil {
		return nil, err
	}
	if rows, err = rereadMoved(ctx, db, rows, contents); err != nil {
		return nil, err
	}
	out := make([]*MCPUserDiscovery, 0, len(rows))
	for i := range rows {
		identity := rows[i].Identity()
		if identity == "" {
			continue
		}
		key := MCPUserDiscoveryKey(rows[i].AuthMode, identity, rows[i].MCPClientID)
		mode, identity, client, _ := parseMCPUserDiscoveryKey(key)
		out = append(out, &MCPUserDiscovery{
			ID: key, Mode: mode, Identity: identity, MCPClientID: client,
			Discovery: contents[rows[i].DiscoveryHash],
		})
	}
	return out, nil
}

// rereadMoved reads again, whole, every row whose hash contents does not hold, adds the discovery
// each holds now to contents, and returns rows with those rows as they are now; a row gone since
// is left out. A whole row carries its hash and its discovery together, so what it returns agrees.
// Which rows moved is fixed before the re-read: a moved row is taken only from its own re-read,
// even when another row's re-read brings content for its old hash.
func rereadMoved(ctx context.Context, db *gorm.DB, rows []tables.TableMCPPerUserDiscovery, contents map[string]*MCPUserDiscoveryContent) ([]tables.TableMCPPerUserDiscovery, error) {
	var moved []string
	wasMoved := make([]bool, len(rows))
	for i := range rows {
		if contents[rows[i].DiscoveryHash] == nil {
			moved = append(moved, rows[i].ID)
			wasMoved[i] = true
		}
	}
	if len(moved) == 0 {
		return rows, nil
	}
	var fresh []tables.TableMCPPerUserDiscovery
	if err := db.WithContext(ctx).Where(queryscope.InStrings(db, "id", moved)).Find(&fresh).Error; err != nil {
		return nil, err
	}
	now := make(map[string]*tables.TableMCPPerUserDiscovery, len(fresh))
	for i := range fresh {
		now[fresh[i].ID] = &fresh[i]
		if contents[fresh[i].DiscoveryHash] == nil {
			contents[fresh[i].DiscoveryHash] = contentOf(&fresh[i])
		}
	}
	out := rows[:0]
	for i := range rows {
		if !wasMoved[i] {
			out = append(out, rows[i])
		} else if row, ok := now[rows[i].ID]; ok {
			out = append(out, *row)
		}
	}
	return out, nil
}

// contentOf is the discovery a whole row holds.
func contentOf(r *tables.TableMCPPerUserDiscovery) *MCPUserDiscoveryContent {
	return &MCPUserDiscoveryContent{
		Hash: r.DiscoveryHash, Tools: r.Tools, Instructions: r.Instructions,
		Resources: r.Resources, ResourceTemplates: r.ResourceTemplates, Prompts: r.Prompts,
		DiscoveredAt: r.DiscoveredAt,
	}
}

// readDiscoveryContents reads one row for each hash, the one with the lowest ID, and returns the
// discovery each holds by hash. A hash whose rows are all gone is left out.
func readDiscoveryContents(ctx context.Context, db *gorm.DB, hashes []string) (map[string]*MCPUserDiscoveryContent, error) {
	first := db.Model(&tables.TableMCPPerUserDiscovery{}).Select("MIN(id)").
		Where(queryscope.InStrings(db, "discovery_hash", hashes)).Group("discovery_hash")
	var rows []tables.TableMCPPerUserDiscovery
	if err := db.WithContext(ctx).Where("id IN (?)", first).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]*MCPUserDiscoveryContent, len(rows))
	for i := range rows {
		out[rows[i].DiscoveryHash] = contentOf(&rows[i])
	}
	return out, nil
}
