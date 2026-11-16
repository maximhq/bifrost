package rules

import (
	"context"

	"github.com/google/cel-go/cel"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// GetProgram returns the compiled CEL program for a rule, compiling it when the rule's expression
// has no program yet. The tests use it to see what compile caches.
func (rs *Store) GetProgram(ctx context.Context, rule *configstoreTables.TableRoutingRule) (cel.Program, error) {
	compiled, err := rs.compile(rule)
	if err != nil {
		return nil, err
	}
	return compiled.program, nil
}
