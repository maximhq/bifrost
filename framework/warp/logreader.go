package warp

import (
	"context"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/mcptools"
)

// LogReader is the deployment's telemetry read surface. The tools that read it
// live on Bifrost's MCP server (framework/mcptools), which owns the interface;
// Warp keeps the same name because its own indexer, backfill and semantic
// searcher read through the same seam, and because the transport builds one
// adapter and hands it to both sides.
type LogReader = mcptools.LogReader

// KeyPair is an id paired with the name it is known by.
type KeyPair = mcptools.KeyPair

// GovernanceReader is the slice of the config store describe_virtual_key
// reads. Owned by mcptools for the same reason LogReader is; Warp only carries
// it from the config store to the server's Deps.
type GovernanceReader = mcptools.GovernanceReader

// SemanticHydrator reads whole log rows for a set of ids.
//
// Kept out of LogReader deliberately. LogReader is exported and accepted by
// exported APIs - WithLogReader, NewAgent - so adding a method to it breaks
// every reader outside this repo at compile time, including ones that never
// touch semantic search. Semantic search and the backfill ask for this
// separately and are enabled only when the supplied reader satisfies it, so an
// older reader keeps working with the rest of Warp.
type SemanticHydrator interface {
	GetLogsByIDs(ctx context.Context, ids []string) ([]logstore.Log, error)
}
