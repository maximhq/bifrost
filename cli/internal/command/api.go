package command

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/client"
	"github.com/maximhq/bifrost/cli/internal/operations"
	"github.com/maximhq/bifrost/cli/internal/output"
)

var pathParameterPattern = regexp.MustCompile(`\{([^{}]+)\}`)

// runAPI discovers and invokes operations from the bundled OpenAPI catalog.
func (r *Runner) runAPI(ctx context.Context, env *environment, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(r.Out, `Usage:
  bifrost api list [--search TERM] [--tag TAG] [--include-deprecated]
  bifrost api describe <operation-id>
  bifrost api call <operation-id> [--param name=value] [--query key=value] [--body JSON|--file PATH]
`)
		return err
	}
	switch args[0] {
	case "list":
		return r.listOperations(env, args[1:])
	case "describe":
		return r.describeOperation(env, args[1:])
	case "call":
		return r.callOperation(ctx, env, args[1:])
	default:
		return fmt.Errorf("unknown api action %q", args[0])
	}
}

// listOperations prints searchable operation-level coverage from OpenAPI.
func (r *Runner) listOperations(env *environment, args []string) error {
	fs := flag.NewFlagSet("bifrost api list", flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var search string
	var tag string
	var includeDeprecated bool
	fs.StringVar(&search, "search", "", "match operation ID, method, path, or summary")
	fs.StringVar(&tag, "tag", "", "match an exact OpenAPI tag")
	fs.BoolVar(&includeDeprecated, "include-deprecated", false, "include legacy operations")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected api list arguments: %s", strings.Join(fs.Args(), " "))
	}
	matched := operations.Search(search, tag)
	rows := make([]operations.Operation, 0, len(matched))
	for _, operation := range matched {
		if operation.Deprecated && !includeDeprecated {
			continue
		}
		rows = append(rows, operation)
	}
	body, err := json.Marshal(map[string]any{"data": rows, "count": len(rows)})
	if err != nil {
		return err
	}
	return output.Print(r.Out, body, env.Output)
}

// describeOperation prints one operation's method, path, tags, and summary.
func (r *Runner) describeOperation(env *environment, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bifrost api describe <operation-id>")
	}
	operation, ok := operations.Find(args[0])
	if !ok {
		return fmt.Errorf("OpenAPI operation %q was not found", args[0])
	}
	body, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	return output.Print(r.Out, body, env.Output)
}

// callOperation resolves path parameters and invokes one OpenAPI operation.
func (r *Runner) callOperation(ctx context.Context, env *environment, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bifrost api call <operation-id> [flags]")
	}
	operation, ok := operations.Find(args[0])
	if !ok {
		return fmt.Errorf("OpenAPI operation %q was not found", args[0])
	}
	fs := flag.NewFlagSet("bifrost api call "+operation.ID, flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var parameterFlags repeatedValue
	var queryFlags repeatedValue
	var headerFlags repeatedValue
	var bodyValue string
	var bodyFile string
	var authValue string
	var dryRun bool
	var yes bool
	var stream bool
	fs.Var(&parameterFlags, "param", "path parameter as name=value; repeatable")
	fs.Var(&queryFlags, "query", "query parameter as key=value; repeatable")
	fs.Var(&headerFlags, "header", "request header as Name: value; repeatable")
	fs.StringVar(&bodyValue, "body", "", "inline request body")
	fs.StringVar(&bodyFile, "file", "", "request body file, or - for stdin")
	fs.StringVar(&authValue, "auth", "auto", "credential mode: auto, none, management, inference, agent")
	fs.BoolVar(&dryRun, "dry-run", false, "print the request plan without applying it")
	fs.BoolVar(&yes, "yes", false, "confirm destructive operations")
	fs.BoolVar(&stream, "stream", false, "copy the response incrementally")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected api call arguments: %s", strings.Join(fs.Args(), " "))
	}
	parameters, err := parseAssignments(parameterFlags, "path parameter")
	if err != nil {
		return err
	}
	path, err := resolveOperationPath(operation.Path, parameters)
	if err != nil {
		return err
	}
	query, err := parseQuery(queryFlags)
	if err != nil {
		return err
	}
	headers, err := parseHeaders(headerFlags)
	if err != nil {
		return err
	}
	body, err := r.readBody(bodyValue, bodyFile)
	if err != nil {
		return err
	}
	authMode, err := parseAuthMode(authValue)
	if err != nil {
		return err
	}
	request := client.Request{Method: operation.Method, Path: path, Query: query, Headers: headers, Body: body, Auth: authMode}
	if operation.Destructive && !yes && !dryRun {
		return fmt.Errorf("refusing destructive operation without --yes; inspect it first with --dry-run")
	}
	if dryRun {
		return r.printRequestPlan(env, request)
	}
	if operation.Deprecated {
		if _, err := fmt.Fprintf(r.ErrOut, "warning: %s is a deprecated compatibility operation\n", operation.ID); err != nil {
			return err
		}
	}
	if stream {
		writer := r.Out
		if env.Quiet {
			writer = io.Discard
		}
		return authErrorHint(env, request, env.Client.Stream(ctx, request, writer))
	}
	return r.executeAndPrint(ctx, env, request)
}

// parseAssignments parses unique key=value flags into a map.
func parseAssignments(entries []string, kind string) (map[string]string, error) {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid %s %q; expected name=value", kind, entry)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("duplicate %s %q", kind, name)
		}
		values[name] = value
	}
	return values, nil
}

// resolveOperationPath replaces every OpenAPI path parameter with an escaped value.
func resolveOperationPath(template string, parameters map[string]string) (string, error) {
	missing := []string{}
	path := pathParameterPattern.ReplaceAllStringFunc(template, func(match string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(match, "{"), "}")
		value, ok := parameters[name]
		if !ok {
			missing = append(missing, name)
			return match
		}
		delete(parameters, name)
		return url.PathEscape(value)
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing path parameter(s): %s", strings.Join(missing, ", "))
	}
	if len(parameters) > 0 {
		extra := make([]string, 0, len(parameters))
		for name := range parameters {
			extra = append(extra, name)
		}
		sort.Strings(extra)
		return "", fmt.Errorf("unknown path parameter(s): %s", strings.Join(extra, ", "))
	}
	return path, nil
}
