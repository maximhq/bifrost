package chaosmock

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// MCP methods for prompts, resources and completion. A server offers them only once a scenario
// gives it prompts or resources, so a server that has none keeps the handshake and method counts
// it always had.
//
// Prompts, resources and templates are served the way tools are: a base list every caller sees,
// and per-identity views that replace one kind's list wholesale for one caller. Capabilities
// describe the server, not the caller, so a kind any list holds is advertised to everyone, and a
// caller whose view of it is empty lists nothing.
const (
	MethodPromptsList           = "prompts/list"
	MethodPromptsGet            = "prompts/get"
	MethodResourcesList         = "resources/list"
	MethodResourcesRead         = "resources/read"
	MethodResourceTemplatesList = "resources/templates/list"
	MethodCompletionComplete    = "completion/complete"
	CodeResourceNotFound        = -32002
	defaultResourceMimeType     = "text/plain"
)

// Prompt is one prompt a virtual server offers. Text is the user message prompts/get answers with,
// each "{argument}" replaced by the value sent for it, and Completions lists per argument the values
// completion/complete offers when they start with what was typed.
type Prompt struct {
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Arguments   []PromptArgument    `json:"arguments,omitempty"`
	Text        string              `json:"text,omitempty"`
	Completions map[string][]string `json:"completions,omitempty"`
}

// PromptArgument is one argument a prompt takes.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// Resource is one resource a virtual server offers, read back as Text.
type Resource struct {
	URI      string `json:"uri"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

// ResourceTemplate is one URI template. A read of a URI it matches answers with Text, "{uri}"
// replaced by the URI read.
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name,omitempty"`
	Text        string `json:"text,omitempty"`
}

// ReadEntry records one prompts/get, resources/read or completion/complete the server answered, so
// a scenario can check what the client asked for rather than only that it asked.
type ReadEntry struct {
	Method    string            `json:"method"`
	Name      string            `json:"name,omitempty"`
	URI       string            `json:"uri,omitempty"`
	Argument  string            `json:"argument,omitempty"`
	Value     string            `json:"value,omitempty"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// promptSurface holds a server's prompts, resources and templates, each identity's own lists, and
// the reads it answered.
type promptSurface struct {
	mu            sync.Mutex
	prompts       []Prompt
	resources     []Resource
	templates     []ResourceTemplate
	promptViews   map[string][]Prompt           // replaces prompts for one identity
	resourceViews map[string][]Resource         // replaces resources for one identity
	templateViews map[string][]ResourceTemplate // replaces templates for one identity
	reads         []ReadEntry
}

// validatePrompts rejects a prompt a client could not address.
func validatePrompts(prompts []Prompt) error {
	for _, prompt := range prompts {
		if prompt.Name == "" {
			return errors.New("every prompt needs a name")
		}
	}
	return nil
}

// validateResources rejects a resource with no uri to read it by.
func validateResources(resources []Resource) error {
	for _, resource := range resources {
		if resource.URI == "" {
			return errors.New("every resource needs a uri")
		}
	}
	return nil
}

// validateTemplates rejects a template with no uriTemplate to match against.
func validateTemplates(templates []ResourceTemplate) error {
	for _, template := range templates {
		if template.URITemplate == "" {
			return errors.New("every resource template needs a uriTemplate")
		}
	}
	return nil
}

// setPrompts replaces the prompts.
func (p *promptSurface) setPrompts(prompts []Prompt) error {
	if err := validatePrompts(prompts); err != nil {
		return err
	}
	p.mu.Lock()
	p.prompts = append([]Prompt(nil), prompts...)
	p.mu.Unlock()
	return nil
}

// setResources replaces the resources and templates.
func (p *promptSurface) setResources(resources []Resource, templates []ResourceTemplate) error {
	if err := validateResources(resources); err != nil {
		return err
	}
	if err := validateTemplates(templates); err != nil {
		return err
	}
	p.mu.Lock()
	p.resources = append([]Resource(nil), resources...)
	p.templates = append([]ResourceTemplate(nil), templates...)
	p.mu.Unlock()
	return nil
}

// setTemplates replaces the templates alone.
func (p *promptSurface) setTemplates(templates []ResourceTemplate) error {
	if err := validateTemplates(templates); err != nil {
		return err
	}
	p.mu.Lock()
	p.templates = append([]ResourceTemplate(nil), templates...)
	p.mu.Unlock()
	return nil
}

// setView gives identity its own list in views, which may be empty.
func setView[T any](mu *sync.Mutex, views *map[string][]T, identity string, items []T) {
	mu.Lock()
	defer mu.Unlock()
	if *views == nil {
		*views = map[string][]T{}
	}
	(*views)[identity] = append([]T{}, items...)
}

// clearView returns identity to the base list, reporting whether it had a view to clear.
func clearView[T any](mu *sync.Mutex, views map[string][]T, identity string) bool {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := views[identity]; !ok {
		return false
	}
	delete(views, identity)
	return true
}

// viewOf is identity's list of one kind: its own view when it has one, else the base list.
func viewOf[T any](base []T, views map[string][]T, identity string) []T {
	if view, ok := views[identity]; ok && identity != "" {
		return append([]T(nil), view...)
	}
	return append([]T(nil), base...)
}

// copyViews copies every identity's list of one kind, nil when there are none.
func copyViews[T any](views map[string][]T) map[string][]T {
	if len(views) == 0 {
		return nil
	}
	out := make(map[string][]T, len(views))
	for identity, items := range views {
		out[identity] = append([]T{}, items...)
	}
	return out
}

// anyView reports whether any identity's list of one kind holds something.
func anyView[T any](views map[string][]T) bool {
	for _, items := range views {
		if len(items) > 0 {
			return true
		}
	}
	return false
}

// applyOverlay handles the <kind>/overlay verbs: give body.Identity its own list of kind, or with
// clear return it to the base list. Clearing a view that is not there is refused like every other
// verb that would do nothing.
func (p *promptSurface) applyOverlay(kind string, body controlBody) error {
	if body.Identity == "" {
		return errBadRequest(kind + "/overlay needs an identity")
	}
	if body.Clear {
		cleared := false
		switch kind {
		case "prompts":
			cleared = clearView(&p.mu, p.promptViews, body.Identity)
		case "resources":
			cleared = clearView(&p.mu, p.resourceViews, body.Identity)
		case "templates":
			cleared = clearView(&p.mu, p.templateViews, body.Identity)
		}
		if !cleared {
			return errBadRequest(fmt.Sprintf("%q has no %s view to clear", body.Identity, kind))
		}
		return nil
	}
	switch kind {
	case "prompts":
		if err := validatePrompts(body.Prompts); err != nil {
			return errBadRequest(err.Error())
		}
		setView(&p.mu, &p.promptViews, body.Identity, body.Prompts)
	case "resources":
		if err := validateResources(body.Resources); err != nil {
			return errBadRequest(err.Error())
		}
		setView(&p.mu, &p.resourceViews, body.Identity, body.Resources)
	case "templates":
		if err := validateTemplates(body.Templates); err != nil {
			return errBadRequest(err.Error())
		}
		setView(&p.mu, &p.templateViews, body.Identity, body.Templates)
	}
	return nil
}

// seed installs a seed's base lists and every identity's own, replacing what the surface held.
func (p *promptSurface) seed(prompts []Prompt, resources []Resource, templates []ResourceTemplate,
	promptViews map[string][]Prompt, resourceViews map[string][]Resource, templateViews map[string][]ResourceTemplate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompts = append([]Prompt(nil), prompts...)
	p.resources = append([]Resource(nil), resources...)
	p.templates = append([]ResourceTemplate(nil), templates...)
	p.promptViews = copyViews(promptViews)
	p.resourceViews = copyViews(resourceViews)
	p.templateViews = copyViews(templateViews)
}

// viewFor is what identity sees of each kind.
func (p *promptSurface) viewFor(identity string) ([]Prompt, []Resource, []ResourceTemplate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return viewOf(p.prompts, p.promptViews, identity), viewOf(p.resources, p.resourceViews, identity), viewOf(p.templates, p.templateViews, identity)
}

// views copies every identity's own lists, for State.
func (p *promptSurface) views() (map[string][]Prompt, map[string][]Resource, map[string][]ResourceTemplate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return copyViews(p.promptViews), copyViews(p.resourceViews), copyViews(p.templateViews)
}

// offers reports which of prompts and resources the server has, in its base lists or in any
// identity's own, which is what it advertises.
func (p *promptSurface) offers() (prompts, resources bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	prompts = len(p.prompts) > 0 || anyView(p.promptViews)
	resources = len(p.resources) > 0 || len(p.templates) > 0 || anyView(p.resourceViews) || anyView(p.templateViews)
	return prompts, resources
}

// record appends one answered read to the log.
func (p *promptSurface) record(entry ReadEntry) {
	p.mu.Lock()
	p.reads = append(p.reads, entry)
	p.mu.Unlock()
}

// clearReads empties the read log, as a counters reset does.
func (p *promptSurface) clearReads() {
	p.mu.Lock()
	p.reads = nil
	p.mu.Unlock()
}

// snapshot copies the surface for State, nil fields when the server offers nothing.
func (p *promptSurface) snapshot() ([]Prompt, []Resource, []ResourceTemplate, []ReadEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Prompt(nil), p.prompts...), append([]Resource(nil), p.resources...), append([]ResourceTemplate(nil), p.templates...), append([]ReadEntry(nil), p.reads...)
}

// SetPrompts replaces the server's prompts; a server with prompts advertises prompts and completion.
func (s *Server) SetPrompts(prompts []Prompt) error { return s.prompts.setPrompts(prompts) }

// SetResources replaces the server's resources and templates; a server with either advertises resources.
func (s *Server) SetResources(resources []Resource, templates []ResourceTemplate) error {
	return s.prompts.setResources(resources, templates)
}

// Reads returns the prompts/get, resources/read and completion/complete requests answered so far.
func (s *Server) Reads() []ReadEntry {
	_, _, _, reads := s.prompts.snapshot()
	return reads
}

// templatePattern turns a URI template into a regexp matching the URIs it covers, each
// "{variable}" standing for one path segment.
func templatePattern(template string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for {
		open := strings.Index(template, "{")
		if open < 0 {
			b.WriteString(regexp.QuoteMeta(template))
			break
		}
		closing := strings.Index(template[open:], "}")
		if closing < 0 {
			b.WriteString(regexp.QuoteMeta(template))
			break
		}
		b.WriteString(regexp.QuoteMeta(template[:open]))
		b.WriteString("[^/]+")
		template = template[open+closing+1:]
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// wirePrompt is a prompt as prompts/list lists it.
type wirePrompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// wireResource is a resource as resources/list lists it.
type wireResource struct {
	URI      string `json:"uri"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType,omitempty"`
}

// wireTemplate is a template as resources/templates/list lists it.
type wireTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
}

// listFault answers a list method with list_fail or malformed, when either is set, and reports
// whether it did. Every list method goes through it, so a discovery that lists tools, prompts and
// resources fails the same way on each.
func (s *Server) listFault(req *request, f Faults) (handleResult, bool) {
	switch {
	case f.ListFail:
		s.counters.faulted()
		return handleResult{Response: newError(req.ID, CodeInternalError, f.text(req.Method+" failed (injected)"))}, true
	case f.Malformed:
		s.counters.faulted()
		return handleResult{Malformed: true}, true
	}
	return handleResult{}, false
}

// handlePromptsList answers prompts/list with identity's prompts.
func (s *Server) handlePromptsList(req *request, identity string, f Faults) handleResult {
	if res, failed := s.listFault(req, f); failed {
		return res
	}
	prompts, _, _ := s.prompts.viewFor(identity)
	wire := make([]wirePrompt, len(prompts))
	for i, prompt := range prompts {
		wire[i] = wirePrompt{Name: prompt.Name, Description: prompt.Description, Arguments: prompt.Arguments}
	}
	return handleResult{Response: newResult(req.ID, map[string]any{"prompts": wire})}
}

// handlePromptsGet answers prompts/get with the text of identity's prompt, its arguments filled in.
func (s *Server) handlePromptsGet(req *request, identity string) handleResult {
	var params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return handleResult{Response: newError(req.ID, CodeInvalidParams, "invalid prompts/get params")}
	}
	prompts, _, _ := s.prompts.viewFor(identity)
	for _, prompt := range prompts {
		if prompt.Name != params.Name {
			continue
		}
		s.prompts.record(ReadEntry{Method: MethodPromptsGet, Name: params.Name, Arguments: params.Arguments})
		text := prompt.Text
		for name, value := range params.Arguments {
			text = strings.ReplaceAll(text, "{"+name+"}", value)
		}
		message := map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": text}}
		return handleResult{Response: newResult(req.ID, map[string]any{"description": prompt.Description, "messages": []any{message}})}
	}
	return handleResult{Response: newError(req.ID, CodeInvalidParams, "prompt not found: "+params.Name)}
}

// handleResourcesList answers resources/list with identity's resources.
func (s *Server) handleResourcesList(req *request, identity string, f Faults) handleResult {
	if res, failed := s.listFault(req, f); failed {
		return res
	}
	_, resources, _ := s.prompts.viewFor(identity)
	wire := make([]wireResource, len(resources))
	for i, resource := range resources {
		name := resource.Name
		if name == "" {
			name = resource.URI
		}
		wire[i] = wireResource{URI: resource.URI, Name: name, MimeType: resource.MimeType}
	}
	return handleResult{Response: newResult(req.ID, map[string]any{"resources": wire})}
}

// handleResourceTemplatesList answers resources/templates/list with identity's templates.
func (s *Server) handleResourceTemplatesList(req *request, identity string, f Faults) handleResult {
	if res, failed := s.listFault(req, f); failed {
		return res
	}
	_, _, templates := s.prompts.viewFor(identity)
	wire := make([]wireTemplate, len(templates))
	for i, template := range templates {
		name := template.Name
		if name == "" {
			name = template.URITemplate
		}
		wire[i] = wireTemplate{URITemplate: template.URITemplate, Name: name}
	}
	return handleResult{Response: newResult(req.ID, map[string]any{"resourceTemplates": wire})}
}

// handleResourcesRead answers resources/read from one of identity's resources, else from the first
// of its templates that matches.
func (s *Server) handleResourcesRead(req *request, identity string) handleResult {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return handleResult{Response: newError(req.ID, CodeInvalidParams, "invalid resources/read params")}
	}
	_, resources, templates := s.prompts.viewFor(identity)
	answer := func(mimeType, text string) handleResult {
		s.prompts.record(ReadEntry{Method: MethodResourcesRead, URI: params.URI})
		if mimeType == "" {
			mimeType = defaultResourceMimeType
		}
		contents := []any{map[string]any{"uri": params.URI, "mimeType": mimeType, "text": text}}
		return handleResult{Response: newResult(req.ID, map[string]any{"contents": contents})}
	}
	for _, resource := range resources {
		if resource.URI == params.URI {
			return answer(resource.MimeType, resource.Text)
		}
	}
	for _, template := range templates {
		if templatePattern(template.URITemplate).MatchString(params.URI) {
			return answer("", strings.ReplaceAll(template.Text, "{uri}", params.URI))
		}
	}
	return handleResult{Response: newError(req.ID, CodeResourceNotFound, "resource not found: "+params.URI)}
}

// handleCompletionComplete answers completion/complete for an argument of one of identity's prompts
// with the prompt's values that start with what was typed, and with no values for anything else.
func (s *Server) handleCompletionComplete(req *request, identity string) handleResult {
	var params struct {
		Ref struct {
			Type string `json:"type"`
			Name string `json:"name,omitempty"`
			URI  string `json:"uri,omitempty"`
		} `json:"ref"`
		Argument struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"argument"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return handleResult{Response: newError(req.ID, CodeInvalidParams, "invalid completion/complete params")}
	}
	s.prompts.record(ReadEntry{Method: MethodCompletionComplete, Name: params.Ref.Name, URI: params.Ref.URI, Argument: params.Argument.Name, Value: params.Argument.Value})
	values := []string{}
	prompts, _, _ := s.prompts.viewFor(identity)
	for _, prompt := range prompts {
		if params.Ref.Type == "ref/prompt" && prompt.Name == params.Ref.Name {
			for _, value := range prompt.Completions[params.Argument.Name] {
				if strings.HasPrefix(value, params.Argument.Value) {
					values = append(values, value)
				}
			}
		}
	}
	completion := map[string]any{"values": values, "total": len(values), "hasMore": false}
	return handleResult{Response: newResult(req.ID, map[string]any{"completion": completion})}
}
