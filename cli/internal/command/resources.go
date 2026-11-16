package command

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/output"
)

// runResources lists and describes the first-class resource command registry.
func (r *Runner) runResources(format output.Format, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 0 {
			args = args[1:]
		}
		fs := flag.NewFlagSet("bifrost resources list", flag.ContinueOnError)
		fs.SetOutput(r.ErrOut)
		var search string
		fs.StringVar(&search, "search", "", "match a resource name or description")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if len(fs.Args()) > 0 {
			return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
		}
		needle := strings.ToLower(strings.TrimSpace(search))
		names := make([]string, 0, len(resourceRegistry))
		for name := range resourceRegistry {
			names = append(names, name)
		}
		sort.Strings(names)
		rows := make([]map[string]any, 0, len(names))
		for _, name := range names {
			descriptor := resourceRegistry[name]
			if needle != "" && !strings.Contains(strings.ToLower(name+" "+descriptor.Description), needle) {
				continue
			}
			rows = append(rows, map[string]any{"name": name, "description": descriptor.Description, "actions": len(descriptor.Actions)})
		}
		return printStructured(r.Out, format, map[string]any{"data": rows, "count": len(rows)})
	}
	if args[0] == "describe" {
		if len(args) != 2 {
			return fmt.Errorf("usage: bifrost resources describe <name>")
		}
		descriptor, ok := resourceRegistry[args[1]]
		if !ok {
			return fmt.Errorf("resource %q was not found", args[1])
		}
		return r.printResourceHelp(args[1], descriptor)
	}
	if args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(r.Out, "Usage: bifrost resources <list [--search TERM]|describe NAME>")
		return err
	}
	return fmt.Errorf("unknown resources action %q", args[0])
}

// printStructured marshals a local value and sends it through the common renderer.
func printStructured(writer io.Writer, format output.Format, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return output.Print(writer, body, format)
}
