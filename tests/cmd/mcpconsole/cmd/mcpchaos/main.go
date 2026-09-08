// Command mcpchaos runs a fleet of controllable MCP servers for exercising how
// an MCP client handles servers that change underneath it.
//
// Each virtual server speaks the protocol for real, so an ordinary client
// connects to it without knowing it is a mock, and each exposes a control API
// that mutates its tools, credentials and failure behaviour while that client
// stays connected. The counters every server keeps are the point: they record
// what was actually asked of it, which is the ground truth a test compares
// against whatever the client reports about itself.
//
//	mcpchaos -servers alpha,beta
//	curl localhost:8400/control
//	curl -XPOST localhost:8400/control/alpha/tools/add -d '{"tool":{"name":"new_tool"}}'
//	curl -XPOST localhost:8400/control/alpha/faults -d '{"faults":{"status":429}}'
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8400", "address to listen on")
	servers := flag.String("servers", "alpha", "comma-separated virtual server names")
	tools := flag.String("tools", "echo_tool,add_tool", "comma-separated tools to seed every server with")
	seed := flag.String("seed", "", "path to a JSON file describing the initial fleet, overriding -servers and -tools")
	stdio := flag.Bool("stdio", false, "serve one virtual server over stdin and stdout, bridging to a running fleet")
	fleetURL := flag.String("fleet", "", "fleet base URL to bridge to (with -stdio)")
	serverName := flag.String("server", "", "virtual server to bridge (with -stdio)")
	flag.Parse()

	// In stdio mode this process is spawned by whatever is under test, so it
	// serves one virtual server over pipes and holds no state of its own; the
	// fleet it bridges to remains the single place tools, faults and counters
	// live. Nothing may be written to stdout but protocol messages, so every
	// diagnostic here goes to stderr.
	if *stdio {
		if err := chaosmock.RunStdioBridge(context.Background(), chaosmock.StdioBridgeConfig{
			FleetURL:   *fleetURL,
			ServerName: *serverName,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "mcpchaos: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fleet := chaosmock.NewFleet()

	if *seed != "" {
		if err := seedFromFile(fleet, *seed); err != nil {
			fmt.Fprintf(os.Stderr, "mcpchaos: %v\n", err)
			os.Exit(1)
		}
	} else {
		toolNames := splitList(*tools)
		for _, name := range splitList(*servers) {
			srv := fleet.Add(name)
			srv.Tools().Set(toolsFromNames(toolNames))
		}
	}

	url, shutdown, err := fleet.Serve(*addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcpchaos: failed to listen on %s: %v\n", *addr, err)
		os.Exit(1)
	}

	fmt.Printf("mcpchaos listening on %s\n", url)
	fmt.Printf("  fleet control  %s/control\n", url)
	for _, name := range fleet.Names() {
		fmt.Printf("  %-12s mcp %s   control %s/control/%s\n", name, fleet.ServerURL(name), url, name)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	fmt.Println("\nmcpchaos shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "mcpchaos: shutdown: %v\n", err)
		os.Exit(1)
	}
}

// seedSpec is the file form of an initial fleet, so a scenario that needs more
// than names and tool lists does not have to be built through a sequence of
// control calls before it can start.
type seedSpec struct {
	Servers map[string]struct {
		Tools  []chaosmock.Tool            `json:"tools"`
		Auth   *chaosmock.Auth             `json:"auth,omitempty"`
		Faults *chaosmock.Faults           `json:"faults,omitempty"`
		Views  map[string][]chaosmock.Tool `json:"views,omitempty"`
	} `json:"servers"`
}

func seedFromFile(fleet *chaosmock.Fleet, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read seed file: %w", err)
	}
	var spec seedSpec
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return fmt.Errorf("invalid seed file: %w", err)
	}
	if len(spec.Servers) == 0 {
		return fmt.Errorf("seed file declares no servers")
	}
	for name, cfg := range spec.Servers {
		srv := fleet.Add(name)
		srv.Tools().Set(cfg.Tools)
		if cfg.Auth != nil {
			srv.SetAuth(*cfg.Auth)
		}
		if cfg.Faults != nil {
			srv.SetFaults(*cfg.Faults)
		}
		for identity, view := range cfg.Views {
			srv.Tools().SetOverlay(identity, view)
		}
	}
	return nil
}

func splitList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func toolsFromNames(names []string) []chaosmock.Tool {
	out := make([]chaosmock.Tool, 0, len(names))
	for _, n := range names {
		out = append(out, chaosmock.Tool{Name: n})
	}
	return out
}
