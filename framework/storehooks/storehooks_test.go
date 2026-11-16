package storehooks

import (
	"testing"

	"gorm.io/gorm"
)

// TestPoolOpenedRunsRegisteredHooks checks hooks see the role and pool, and a nil pool is ignored.
func TestPoolOpenedRunsRegisteredHooks(t *testing.T) {
	var got []Role
	RegisterPoolOpened(func(role Role, db *gorm.DB) { got = append(got, role) })
	RegisterPoolOpened(nil)

	PoolOpened(RoleConfig, &gorm.DB{})
	PoolOpened(RoleLogs, nil)
	PoolOpened(RoleLogs, &gorm.DB{})

	if len(got) != 2 || got[0] != RoleConfig || got[1] != RoleLogs {
		t.Fatalf("hooks saw %v, want [config logs]", got)
	}
}
