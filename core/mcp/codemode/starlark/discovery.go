//go:build !tinygo && !wasm

package starlark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// Ranked discovery: listToolFiles(query=...) returns a short, ranked list of
// the tool files the caller can already see, instead of the complete listing.
// Ranking only orders what the caller's listing would contain; it never
// executes a tool and never adds a tool the caller cannot see.

const (
	discoveryDefaultLimit  = 8
	discoveryMaxLimit      = 20
	discoveryMaxQueryRunes = 2048

	// Bounds on what is sent to a ranker.
	discoveryMaxRankCandidates   = 32
	discoveryMaxDescriptionRunes = 512
	discoveryMaxParameters       = 32
	discoveryMaxParameterRunes   = 128
	discoveryMaxRequestBytes     = 256 << 10
	discoveryMaxResponseBytes    = 64 << 10
	discoveryMaxTokenBytes       = 4096

	// discoveryRankVersion is the version of the external ranker contract.
	discoveryRankVersion = 1
	// A ranker sufficiency below this adds a hint to widen the search.
	discoveryInsufficientBelow = 0.5

	// Lexical weights: a query term found in the server or tool name counts more
	// than one found in the description or parameter names; naming a tool
	// outright ("server.tool" or its file path) puts it first.
	discoveryNameWeight        = 4
	discoveryNameWordBonus     = 2
	discoveryDescriptionWeight = 1
	discoveryExplicitWeight    = 1000
)

// discoveryCandidate is one tool as the external ranker sees it.
type discoveryCandidate struct {
	ID          string   `json:"id"`
	Server      string   `json:"server"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Parameters  []string `json:"parameters"`
}

// discoveryRankRequest is the body POSTed to the external ranker.
type discoveryRankRequest struct {
	Version    int                  `json:"version"`
	Revision   string               `json:"revision"`
	Query      string               `json:"query"`
	Candidates []discoveryCandidate `json:"candidates"`
}

// discoveryRankScore is one scored candidate in the ranker response. Relevance
// is a pointer so a missing value is told apart from an explicit 0.
type discoveryRankScore struct {
	ID        string   `json:"id"`
	Relevance *float64 `json:"relevance"`
}

// discoveryRankResponse is the external ranker's answer.
type discoveryRankResponse struct {
	Version    int                  `json:"version"`
	Revision   string               `json:"revision"`
	Candidates []discoveryRankScore `json:"candidates"`
	Sufficient *float64             `json:"sufficient,omitempty"`
}

// discoveryTool is one visible tool with its lexical score.
type discoveryTool struct {
	candidate discoveryCandidate
	lexical   float64
}

// parseListToolFilesArgs reads the optional query and limit. An empty query
// means the complete listing, and limit is then ignored, not validated.
func parseListToolFilesArgs(raw string) (string, int, error) {
	limit := discoveryDefaultLimit
	if strings.TrimSpace(raw) == "" {
		return "", limit, nil
	}
	var args map[string]interface{}
	if err := sonic.Unmarshal([]byte(raw), &args); err != nil {
		return "", limit, fmt.Errorf("failed to parse tool arguments: %v", err)
	}
	query := ""
	if value, ok := args["query"]; ok && value != nil {
		text, isString := value.(string)
		if !isString {
			return "", limit, fmt.Errorf("query must be a string")
		}
		if utf8.RuneCountInString(text) > discoveryMaxQueryRunes {
			return "", limit, fmt.Errorf("query must be at most %d characters", discoveryMaxQueryRunes)
		}
		query = strings.TrimSpace(text)
	}
	if query == "" {
		return "", limit, nil
	}
	if value, ok := args["limit"]; ok && value != nil {
		n, isNumber := value.(float64)
		if !isNumber || n != math.Trunc(n) || n < 1 || n > discoveryMaxLimit {
			return "", limit, fmt.Errorf("limit must be an integer from 1 to %d", discoveryMaxLimit)
		}
		limit = int(n)
	}
	return query, limit, nil
}

// discoveryCatalogue collects the code mode tools visible to this caller, the
// same set the complete listing shows, sorted by ID. The revision changes when
// any visible tool's definition or the binding level changes.
func (s *StarlarkCodeMode) discoveryCatalogue(ctx context.Context) ([]discoveryTool, string) {
	toolsPerClient := s.clientManager.GetToolPerClient(codemcp.WithModelToolsOnly(ctx))
	type entry struct {
		tool       discoveryTool
		definition []byte
	}
	entries := make([]entry, 0)
	for clientName, tools := range toolsPerClient {
		client := s.clientManager.GetClientByName(clientName)
		if client == nil || client.ExecutionConfig == nil || !client.ExecutionConfig.IsCodeModeClient {
			continue
		}
		for _, tool := range tools {
			if tool.Function == nil || tool.Function.Name == "" {
				continue
			}
			name := getCanonicalToolName(clientName, tool.Function.Name)
			if validateNormalizedToolName(name) != nil {
				continue
			}
			description := ""
			if tool.Function.Description != nil {
				description = boundedDiscoveryText(*tool.Function.Description, discoveryMaxDescriptionRunes)
			}
			parameters := []string{}
			if tool.Function.Parameters != nil {
				for _, key := range tool.Function.Parameters.Properties.Keys() {
					if len(parameters) == discoveryMaxParameters {
						break
					}
					parameters = append(parameters, boundedDiscoveryText(key, discoveryMaxParameterRunes))
				}
			}
			// Sorted keys: a revision must not change between calls for the same tools.
			definition, err := schemas.MarshalSorted(tool.Function)
			if err != nil {
				definition = nil
			}
			entries = append(entries, entry{
				tool: discoveryTool{candidate: discoveryCandidate{
					ID:          fmt.Sprintf("servers/%s/%s.pyi", clientName, name),
					Server:      clientName,
					Name:        name,
					Description: description,
					Parameters:  parameters,
				}},
				definition: definition,
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].tool.candidate.ID < entries[j].tool.candidate.ID })

	hash := sha256.New()
	hash.Write([]byte(s.GetBindingLevel()))
	items := make([]discoveryTool, len(entries))
	for i, e := range entries {
		items[i] = e.tool
		hash.Write([]byte{0})
		hash.Write([]byte(e.tool.candidate.ID))
		hash.Write([]byte{0})
		hash.Write(e.definition)
	}
	return items, hex.EncodeToString(hash.Sum(nil))
}

// boundedDiscoveryText collapses whitespace and keeps at most limit runes.
func boundedDiscoveryText(text string, limit int) string {
	clean := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(clean) <= limit {
		return clean
	}
	return string([]rune(clean)[:limit])
}

// discoveryWords splits text into lowercase letter/digit words.
func discoveryWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// scoreDiscovery sets each tool's lexical score for query and returns the tools
// ordered best first; equal scores keep ID order.
func scoreDiscovery(items []discoveryTool, query string) []discoveryTool {
	ranked := append([]discoveryTool(nil), items...)
	lowerQuery := strings.ToLower(query)
	seen := map[string]bool{}
	terms := make([]string, 0)
	for _, word := range discoveryWords(query) {
		if utf8.RuneCountInString(word) < 2 || seen[word] {
			continue
		}
		seen[word] = true
		terms = append(terms, word)
	}
	for i := range ranked {
		item := &ranked[i]
		c := item.candidate
		nameText := strings.ToLower(c.Server + " " + c.Name)
		nameWords := map[string]bool{}
		for _, word := range discoveryWords(nameText) {
			nameWords[word] = true
		}
		otherText := strings.ToLower(c.Description + " " + strings.Join(c.Parameters, " "))
		score := 0.0
		for _, term := range terms {
			if strings.Contains(nameText, term) {
				score += discoveryNameWeight
				if nameWords[term] {
					score += discoveryNameWordBonus
				}
			}
			if strings.Contains(otherText, term) {
				score += discoveryDescriptionWeight
			}
		}
		explicit := strings.Contains(lowerQuery, strings.ToLower(c.ID)) ||
			strings.Contains(lowerQuery, strings.ToLower(c.Server+"."+c.Name))
		if explicit {
			score += discoveryExplicitWeight
		}
		item.lexical = score
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].lexical > ranked[j].lexical })
	return ranked
}

// rankCandidates picks what is sent to a ranker that takes at most
// maxCandidates: everything when the catalogue is small, otherwise the best
// lexical matches in three quarters of the slots plus one tool from each server
// not yet represented, so a match with no shared words can still be found.
func rankCandidates(ranked []discoveryTool, maxCandidates int) []discoveryTool {
	if len(ranked) <= maxCandidates {
		return ranked
	}
	lexicalSlots := maxCandidates - maxCandidates/4
	selected := append(make([]discoveryTool, 0, maxCandidates), ranked[:lexicalSlots]...)
	picked := map[string]bool{}
	servers := map[string]bool{}
	for _, item := range selected {
		picked[item.candidate.ID] = true
		servers[item.candidate.Server] = true
	}
	for _, item := range ranked[lexicalSlots:] {
		if len(selected) == maxCandidates {
			break
		}
		if !servers[item.candidate.Server] {
			selected = append(selected, item)
			picked[item.candidate.ID] = true
			servers[item.candidate.Server] = true
		}
	}
	for _, item := range ranked[lexicalSlots:] {
		if len(selected) == maxCandidates {
			break
		}
		if !picked[item.candidate.ID] {
			selected = append(selected, item)
			picked[item.candidate.ID] = true
		}
	}
	return selected
}

// validateDiscoveryRankResponse checks a ranker answer against the request it
// answers and returns the relevance of each scored candidate. Candidates the
// ranker leaves out count as not relevant.
func validateDiscoveryRankResponse(req discoveryRankRequest, resp discoveryRankResponse) (map[string]float64, error) {
	if resp.Version != discoveryRankVersion {
		return nil, fmt.Errorf("unsupported response version %d", resp.Version)
	}
	if resp.Revision != req.Revision {
		return nil, errors.New("response revision does not match the request")
	}
	if resp.Sufficient != nil && !validDiscoveryProbability(*resp.Sufficient) {
		return nil, errors.New("sufficient must be between 0 and 1")
	}
	allowed := make(map[string]bool, len(req.Candidates))
	for _, c := range req.Candidates {
		allowed[c.ID] = true
	}
	scores := make(map[string]float64, len(resp.Candidates))
	for _, c := range resp.Candidates {
		if !allowed[c.ID] {
			return nil, fmt.Errorf("response names unknown candidate %q", c.ID)
		}
		if _, dup := scores[c.ID]; dup {
			return nil, fmt.Errorf("response scores candidate %q twice", c.ID)
		}
		if c.Relevance == nil || !validDiscoveryProbability(*c.Relevance) {
			return nil, fmt.Errorf("relevance of candidate %q must be between 0 and 1", c.ID)
		}
		scores[c.ID] = *c.Relevance
	}
	return scores, nil
}

func validDiscoveryProbability(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1
}

// handleRankedToolFiles answers listToolFiles(query=...).
func (s *StarlarkCodeMode) handleRankedToolFiles(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall, query string, limit int) (*schemas.ChatMessage, error) {
	catalogue, revision := s.discoveryCatalogue(ctx)
	if len(catalogue) == 0 {
		// Nothing to rank: the complete listing explains why.
		return s.handleCompleteToolFiles(ctx, toolCall)
	}

	// A query that is exactly a tool reference needs no ranking.
	for _, item := range catalogue {
		c := item.candidate
		if strings.EqualFold(query, c.ID) || strings.EqualFold(query, c.Server+"."+c.Name) {
			return createToolResponseMessage(toolCall, s.discoveryShortlist(ctx, []discoveryTool{item}, nil, limit,
				fmt.Sprintf("# Exact match for this query: 1 of %d tools visible to this caller.", len(catalogue))), false), nil
		}
	}

	ranked := scoreDiscovery(catalogue, query)
	lexical := func(reason string) *schemas.ChatMessage {
		matched := make([]discoveryTool, 0, limit)
		for _, item := range ranked {
			if item.lexical <= 0 {
				break
			}
			matched = append(matched, item)
		}
		if len(matched) == 0 {
			return createToolResponseMessage(toolCall, fmt.Sprintf(
				"No tool names or descriptions matched this query among the %d tools visible to this caller. "+
					"Try other words, or call listToolFiles without query for the complete listing.", len(catalogue)), false)
		}
		header := fmt.Sprintf("# Ranked shortlist for this query (lexical match): %d of %d tools visible to this caller matched.", len(matched), len(catalogue))
		if reason != "" {
			header += "\n# The discovery ranker was not used (" + reason + ")."
		}
		return createToolResponseMessage(toolCall, s.discoveryShortlist(ctx, matched, nil, limit, header), false)
	}

	rankerRef := s.discoveryRanker.Load()
	if rankerRef == nil {
		return lexical(""), nil
	}
	ranker := *rankerRef

	candidates := rankCandidates(ranked, ranker.maxCandidates())
	req := discoveryRankRequest{
		Version:    discoveryRankVersion,
		Revision:   revision,
		Query:      query,
		Candidates: make([]discoveryCandidate, len(candidates)),
	}
	for i, item := range candidates {
		req.Candidates[i] = item.candidate
	}
	resp, err := ranker.rank(ctx, req)
	var scores map[string]float64
	if err == nil {
		scores, err = validateDiscoveryRankResponse(req, resp)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		s.logger.Warn("%s discovery ranker failed, using lexical ranking: %v", codemcp.CodeModeLogPrefix, err)
		return lexical("it did not return a usable answer"), nil
	}

	selected := make([]discoveryTool, 0, len(candidates))
	for _, item := range candidates {
		if scores[item.candidate.ID] > 0 {
			selected = append(selected, item)
		}
	}
	if len(selected) == 0 {
		return lexical("it found no relevant tool"), nil
	}
	sort.SliceStable(selected, func(i, j int) bool {
		ri, rj := scores[selected[i].candidate.ID], scores[selected[j].candidate.ID]
		if ri != rj {
			return ri > rj
		}
		return selected[i].lexical > selected[j].lexical
	})
	header := fmt.Sprintf("# Ranked shortlist for this query (discovery ranker): %d of %d tools visible to this caller were ranked.", len(candidates), len(catalogue))
	if resp.Sufficient != nil && *resp.Sufficient < discoveryInsufficientBelow {
		header += "\n# The ranker reports these tools may not cover the request; call listToolFiles without query for the complete listing."
	}
	return createToolResponseMessage(toolCall, s.discoveryShortlist(ctx, selected, scores, limit, header), false), nil
}

// discoveryShortlist renders up to limit tool files in rank order. At server
// binding several tools share one file, which is listed once with the matching
// tool names. scores, when set, annotates each file with its best relevance.
func (s *StarlarkCodeMode) discoveryShortlist(ctx context.Context, items []discoveryTool, scores map[string]float64, limit int, header string) string {
	bindingLevel := s.GetBindingLevel()
	type file struct {
		path      string
		server    string
		tools     []string
		relevance float64
	}
	files := make([]*file, 0, limit)
	byPath := map[string]*file{}
	for _, item := range items {
		c := item.candidate
		path := c.ID
		if bindingLevel == schemas.CodeModeBindingLevelServer {
			path = fmt.Sprintf("servers/%s.pyi", c.Server)
		}
		f, ok := byPath[path]
		if !ok {
			if len(files) == limit {
				continue
			}
			f = &file{path: path, server: c.Server, relevance: scores[c.ID]}
			byPath[path] = f
			files = append(files, f)
		}
		f.tools = append(f.tools, c.Name)
	}

	var serversWithInstructions []string
	seenServers := map[string]bool{}
	for _, f := range files {
		if seenServers[f.server] {
			continue
		}
		seenServers[f.server] = true
		if client := s.clientManager.GetClientByName(f.server); client != nil && client.ExecutionConfig != nil &&
			s.clientManager.GetInstructionsForClient(ctx, client.ExecutionConfig.ID) != "" {
			serversWithInstructions = append(serversWithInstructions, f.server)
		}
	}
	sort.Strings(serversWithInstructions)

	lines := []string{
		header,
		"# Not exhaustive: call listToolFiles without query for the complete listing.",
		"# Workflow: listToolFiles -> readToolFile -> (optional) getToolDocs -> executeToolCode",
	}
	if bindingLevel == schemas.CodeModeBindingLevelServer {
		lines = append(lines, "# Read the server .pyi file before executeToolCode to confirm exact tool names and parameters.")
	} else {
		lines = append(lines, "# Call readToolFile before executeToolCode to confirm parameters and return shape.")
	}
	if len(serversWithInstructions) > 0 {
		lines = append(lines, fmt.Sprintf("# Servers with their own usage instructions: %s. Read their .pyi before calling their tools.",
			strings.Join(serversWithInstructions, ", ")))
	}
	lines = append(lines, "")
	for _, f := range files {
		var notes []string
		if bindingLevel == schemas.CodeModeBindingLevelServer {
			notes = append(notes, "tools: "+strings.Join(f.tools, ", "))
		}
		if scores != nil {
			notes = append(notes, fmt.Sprintf("relevance=%.2f", f.relevance))
		}
		if len(notes) > 0 {
			lines = append(lines, f.path+"  # "+strings.Join(notes, "; "))
		} else {
			lines = append(lines, f.path)
		}
	}
	return strings.Join(lines, "\n")
}

// toolRanker ranks listToolFiles candidates: an operator-configured HTTP
// ranker or a decision model. Both answer in the external ranker contract, so
// one validation and one shortlist serve both.
type toolRanker interface {
	rank(ctx context.Context, input discoveryRankRequest) (discoveryRankResponse, error)
	// maxCandidates bounds the candidates one request may carry.
	maxCandidates() int
}

// httpDiscoveryRanker calls an operator-configured HTTP ranker. The endpoint
// and credentials come only from configuration, never from the query.
type httpDiscoveryRanker struct {
	endpoint  string
	tokenFile string
	timeout   time.Duration
	client    *http.Client
}

func (r *httpDiscoveryRanker) maxCandidates() int { return discoveryMaxRankCandidates }

// storeDiscoveryRanker publishes the ranker. nil keeps the current one, a
// config with neither a URL nor a decision model removes it, and an invalid
// config is ignored.
func (s *StarlarkCodeMode) storeDiscoveryRanker(config *schemas.MCPCodeModeDiscoveryRanker) {
	if config == nil {
		return
	}
	if !config.Enabled() {
		s.discoveryRanker.Store(nil)
		return
	}
	if err := config.Validate(); err != nil {
		s.logger.Warn("%s ignoring invalid code mode discovery ranker: %v", codemcp.CodeModeLogPrefix, err)
		return
	}
	var ranker toolRanker
	if config.DecisionModel != "" {
		ranker = newDecisionDiscoveryRanker(*config, &s.decisionRequest)
	} else {
		ranker = &httpDiscoveryRanker{
			endpoint:  config.URL,
			tokenFile: config.TokenFile,
			timeout:   config.Timeout(),
			client: &http.Client{
				// A redirect is answered as a failure rather than followed, so the
				// catalogue and token only ever reach the configured endpoint.
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
		}
	}
	s.discoveryRanker.Store(&ranker)
}

// rank sends one ranking request, bounded by the configured timeout.
func (r *httpDiscoveryRanker) rank(ctx context.Context, input discoveryRankRequest) (discoveryRankResponse, error) {
	var result discoveryRankResponse
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	body, err := sonic.Marshal(input)
	if err != nil {
		return result, fmt.Errorf("encode request: %w", err)
	}
	if len(body) > discoveryMaxRequestBytes {
		return result, fmt.Errorf("request is %d bytes, over the %d byte limit", len(body), discoveryMaxRequestBytes)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if r.tokenFile != "" {
		token, err := readDiscoveryToken(r.tokenFile)
		if err != nil {
			return result, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return result, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("ranker returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, discoveryMaxResponseBytes+1))
	if err != nil {
		return result, fmt.Errorf("read response: %w", err)
	}
	if len(data) > discoveryMaxResponseBytes {
		return result, fmt.Errorf("response is over the %d byte limit", discoveryMaxResponseBytes)
	}
	if err := sonic.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}

// readDiscoveryToken reads the bearer token on every call, so a rotated token
// is picked up without a restart.
func readDiscoveryToken(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, discoveryMaxTokenBytes+1))
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if len(data) > discoveryMaxTokenBytes || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("token file must hold one non-empty token")
	}
	return token, nil
}
