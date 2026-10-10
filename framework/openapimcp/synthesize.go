package openapimcp

import (
	"fmt"
	"strings"
)

// Synthesize turns a Document into tools: one per supported operation, named,
// described, with a flattened input schema and the bindings the executor needs.
// Operations that cannot become tools are listed in Unsupported with a reason;
// the result is still usable as long as at least one tool was produced (the
// factory enforces that, the preview shows both lists).
func Synthesize(doc *Document, opts SynthesizeOptions) (*Synthesis, error) {
	if doc == nil {
		return nil, fmt.Errorf("document is nil")
	}
	budget := toolNameBudget(opts.ClientName)
	syn := &Synthesis{Document: doc, Warnings: append([]string(nil), doc.Warnings...)}

	for _, op := range doc.Operations {
		unsupported := func(reason string) {
			syn.Unsupported = append(syn.Unsupported, UnsupportedOperation{OperationID: op.ID, Method: op.Method, Path: op.Path, Reason: reason})
		}
		if op.Unsupported != "" {
			unsupported(op.Unsupported)
			continue
		}
		if op.Deprecated && !opts.IncludeDeprecated {
			unsupported("deprecated (set openapi_config.include_deprecated to expose it)")
			continue
		}
		schema, args, body, warnings, err := FlattenInputSchema(op)
		if err != nil {
			unsupported(err.Error())
			continue
		}
		tool := Tool{
			Name:        ToolName(op, budget),
			Operation:   op,
			Description: BuildDescription(op),
			InputSchema: schema,
			Args:        args,
			Body:        body,
			Annotations: annotationsFor(op.Method),
			Warnings:    warnings,
		}
		if w := securityWarning(op, doc); w != "" {
			tool.Warnings = append(tool.Warnings, w)
		}
		syn.Tools = append(syn.Tools, tool)
	}

	if len(syn.Tools) > MaxToolCount {
		return nil, fmt.Errorf("spec yields %d tools, above the limit of %d", len(syn.Tools), MaxToolCount)
	}
	syn.Warnings = append(syn.Warnings, DedupeToolNames(syn.Tools, budget)...)
	if len(syn.Tools) > ToolCountWarnThreshold {
		syn.Warnings = append(syn.Warnings, fmt.Sprintf("%d tools exceed the recommended %d; restrict tools_to_execute so the model is not offered every operation", len(syn.Tools), ToolCountWarnThreshold))
	}
	if len(syn.Tools) == 0 {
		syn.Warnings = append(syn.Warnings, "no operation in the spec can be exposed as a tool")
	}
	return syn, nil
}

// securityWarning flags an operation whose every security requirement depends
// on an unsupported scheme: it will be called unauthenticated unless the admin
// supplies static headers.
func securityWarning(op Operation, doc *Document) string {
	reqs := doc.GlobalSecurity
	if op.HasSecurity {
		reqs = op.Security
	}
	if len(reqs) == 0 {
		return ""
	}
	var unsupported []string
	for _, req := range reqs {
		if len(req) == 0 {
			return "" // an empty requirement means the operation may be called without credentials
		}
		satisfiable := true
		for name := range req {
			scheme, ok := doc.SecuritySchemes[name]
			if !ok || !scheme.Supported {
				satisfiable = false
				if ok {
					unsupported = append(unsupported, fmt.Sprintf("%s (%s)", name, scheme.Type))
				} else {
					unsupported = append(unsupported, name+" (undeclared)")
				}
			}
		}
		if satisfiable {
			return ""
		}
	}
	return fmt.Sprintf("requires security scheme %s which is not supported; calls are sent with static headers only", strings.Join(unsupported, ", "))
}
