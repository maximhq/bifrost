package handlers

import (
	"context"
	"sync"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// VirtualKeyCreateGovernor decides what governs a virtual key, inside the transaction that creates
// it. A build that puts keys under something - an access profile, a quota, anything a key must
// answer to - registers one; a build with nothing to answer to registers none.
//
// It runs after the key and its own rows are written and before the transaction commits, so the key
// and whatever governs it land together. Returning an error rolls the whole create back: the key is
// never created rather than created ungoverned. Return a *ForbiddenError to refuse the request with
// 403 - "you may not create a key that nothing would govern" - and any other error is a 500.
type VirtualKeyCreateGovernor func(ctx context.Context, tx *gorm.DB, vk *configstoreTables.TableVirtualKey) error

// VirtualKeyCreatedNotifier is told about a created key once its transaction has committed, for the
// caches and peers that have to see it. It cannot refuse anything: the key exists by then.
type VirtualKeyCreatedNotifier func(ctx context.Context, vk *configstoreTables.TableVirtualKey)

var (
	virtualKeyCreateMu       sync.RWMutex
	virtualKeyCreateGovernor VirtualKeyCreateGovernor
	virtualKeyCreatedNotify  VirtualKeyCreatedNotifier
)

// RegisterVirtualKeyCreateGovernor installs the governor for this process. Passing nil clears it,
// which is how a test puts the process back as it found it.
func RegisterVirtualKeyCreateGovernor(fn VirtualKeyCreateGovernor) {
	virtualKeyCreateMu.Lock()
	virtualKeyCreateGovernor = fn
	virtualKeyCreateMu.Unlock()
}

// RegisterVirtualKeyCreatedNotifier installs the post-commit notifier, the same way.
func RegisterVirtualKeyCreatedNotifier(fn VirtualKeyCreatedNotifier) {
	virtualKeyCreateMu.Lock()
	virtualKeyCreatedNotify = fn
	virtualKeyCreateMu.Unlock()
}

// governCreatedVirtualKey runs the registered governor, and nothing when none is registered.
func governCreatedVirtualKey(ctx context.Context, tx *gorm.DB, vk *configstoreTables.TableVirtualKey) error {
	virtualKeyCreateMu.RLock()
	govern := virtualKeyCreateGovernor
	virtualKeyCreateMu.RUnlock()
	if govern == nil || vk == nil {
		return nil
	}
	return govern(ctx, tx, vk)
}

// notifyVirtualKeyCreated tells the registered notifier about a committed key.
func notifyVirtualKeyCreated(ctx context.Context, vk *configstoreTables.TableVirtualKey) {
	virtualKeyCreateMu.RLock()
	notify := virtualKeyCreatedNotify
	virtualKeyCreateMu.RUnlock()
	if notify == nil || vk == nil {
		return
	}
	notify(ctx, vk)
}
