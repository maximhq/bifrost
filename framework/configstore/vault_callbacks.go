package configstore

import (
	"reflect"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// vaultStoreSelfManaged is implemented by models that store their own SecretVar
// fields into the vault from within their BeforeSave hook, instead of relying on
// the global store callback. This is required when a model's SecretVar columns are
// only populated inside BeforeSave (e.g. TableKey copies AzureKeyConfig/BedrockKeyConfig
// etc. into flat columns) AND that same hook encrypts them: the vault store must run
// at the midpoint — after populate, before encrypt — which only the hook itself can
// reach. The global callback skips these models to avoid storing empty values (if it
// ran before BeforeSave) or ciphertext (if it ran after).
type vaultStoreSelfManaged interface {
	VaultStoreSelfManaged()
}

// vaultPathKeyColumner names the column that holds a model's VaultPathKey, so the vault
// callbacks can load the stored row and see which secrets it referenced before a save.
// Models without it still store and remove secrets, but a secret a save replaces is not
// removed.
type vaultPathKeyColumner interface {
	VaultPathKeyColumn() string
}

// RegisterVaultCallbacks installs global GORM callbacks that automatically store
// plaintext SecretVar fields into the vault before create/update and remove the owned
// vault secrets a write or delete leaves unreferenced. Models opt in by implementing
// schemas.VaultPathKeyer; models that don't implement it are silently skipped.
//
// The store callback runs at Before("gorm:before_create") / Before("gorm:before_update"),
// so the vault ref replaces the plaintext before BeforeSave serializes/encrypts it.
// This is correct for models whose SecretVar fields are set by the caller
// (TableMCPClient.Headers, TableOauthConfig.ClientSecret). Models that populate their
// SecretVar columns inside BeforeSave implement vaultStoreSelfManaged and do their own
// store at the correct midpoint; the global callback skips them.
//
// Removals never run inside the transaction. The callbacks queue them on the vaultTx the
// statement runs in (see vaulttx.go), which runs them once the transaction commits or
// rolls back, so a rolled-back row never references a removed secret.
func RegisterVaultCallbacks(db *gorm.DB) {
	installVaultTxPool(db)
	db.Callback().Create().Before("gorm:before_create").Register("bifrost:vault_store", vaultStoreCallback(true))
	db.Callback().Update().Before("gorm:before_update").Register("bifrost:vault_store", vaultStoreCallback(false))
	db.Callback().Create().After("gorm:after_create").Before("gorm:commit_or_rollback_transaction").Register("bifrost:vault_reconcile", vaultReconcileCallback)
	db.Callback().Update().After("gorm:after_update").Before("gorm:commit_or_rollback_transaction").Register("bifrost:vault_reconcile", vaultReconcileCallback)
	db.Callback().Delete().After("gorm:after_delete").Before("gorm:commit_or_rollback_transaction").Register("bifrost:vault_remove", vaultRemoveCallback)
}

const vaultSnapshotsKey = "bifrost:vault_snapshots"

// vaultSnapshot is a model's vault state captured before the store runs: the vault paths
// the caller passed in, and the owned paths the stored row referenced (nil when unknown,
// which skips removal of replaced secrets).
type vaultSnapshot struct {
	incoming map[string]bool
	stored   map[string]bool
}

// vaultStoreCallback snapshots each model's vault state, then stores its plaintext
// SecretVar fields into the vault.
func vaultStoreCallback(isCreate bool) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		if !schemas.VaultStoreWriteEnabled() {
			return
		}
		loadStored := replacesStoredRow(tx, isCreate)
		snapshots := make(map[any]*vaultSnapshot)
		forEachModel(tx, func(model interface{}, keyer schemas.VaultPathKeyer) {
			snapshot := &vaultSnapshot{incoming: pathSet(schemas.VaultSecretPaths(model))}
			if loadStored {
				snapshot.stored = storedVaultPaths(tx, model, keyer)
			}
			snapshots[model] = snapshot
			if _, ok := model.(vaultStoreSelfManaged); ok {
				return // model stores its own vault secrets inside BeforeSave
			}
			tableName := tx.Statement.Table
			base := schemas.VaultBasePath(tableName, keyer.VaultPathKey())
			if err := schemas.StoreOwnedVaultSecretVars(tx.Statement.Context, base, model); err != nil {
				_ = tx.AddError(err)
			}
		})
		tx.InstanceSet(vaultSnapshotsKey, snapshots)
	}
}

// vaultReconcileCallback queues removal of the owned secrets a create or update left
// unreferenced. Secrets the statement wrote are removed if the transaction rolls back,
// or right away if the statement itself failed. Secrets the stored row referenced and the
// written row does not are removed once the transaction commits.
func vaultReconcileCallback(tx *gorm.DB) {
	if !schemas.VaultStoreWriteEnabled() {
		return
	}
	value, ok := tx.InstanceGet(vaultSnapshotsKey)
	if !ok {
		return
	}
	snapshots, _ := value.(map[any]*vaultSnapshot)
	var onCommit, onRollback []string
	forEachModel(tx, func(model interface{}, keyer schemas.VaultPathKeyer) {
		snapshot := snapshots[model]
		if snapshot == nil {
			return
		}
		referenced := ownedPaths(schemas.VaultBasePath(tx.Statement.Table, keyer.VaultPathKey()), model)
		var written []string
		for path := range referenced {
			if !snapshot.incoming[path] {
				written = append(written, path)
			}
		}
		onRollback = append(onRollback, written...)
		if tx.Error != nil {
			onCommit = append(onCommit, written...)
			return
		}
		for path := range snapshot.stored {
			if !referenced[path] {
				onCommit = append(onCommit, path)
			}
		}
	})
	queueVaultRemovals(tx, onCommit, onRollback)
}

// vaultRemoveCallback queues removal of the owned secrets a deleted row referenced, to run
// once the transaction commits.
func vaultRemoveCallback(tx *gorm.DB) {
	if !schemas.VaultStoreWriteEnabled() || tx.Error != nil {
		return
	}
	var onCommit []string
	forEachModel(tx, func(model interface{}, keyer schemas.VaultPathKeyer) {
		base := schemas.VaultBasePath(tx.Statement.Table, keyer.VaultPathKey())
		onCommit = appendPaths(onCommit, ownedPaths(base, model))
	})
	queueVaultRemovals(tx, onCommit, nil)
}

// replacesStoredRow reports whether the statement overwrites every column of an existing
// row, the only case where a vault ref missing from the model was really dropped: Save
// (an update selecting "*") and an upsert that updates all columns. A plain insert has no
// prior row, and a partial update (Update, Updates, Select, Omit) leaves unwritten columns
// holding refs the model may not carry, so neither may remove anything.
func replacesStoredRow(tx *gorm.DB, isCreate bool) bool {
	stmt := tx.Statement
	if len(stmt.Omits) > 0 {
		return false
	}
	if isCreate {
		conflict, ok := stmt.Clauses["ON CONFLICT"].Expression.(clause.OnConflict)
		return ok && conflict.UpdateAll && len(stmt.Selects) == 0
	}
	return len(stmt.Selects) == 1 && stmt.Selects[0] == "*"
}

// storedVaultPaths loads the row the statement is about to overwrite and returns the
// owned vault paths it references. It returns nil when the row cannot be identified or
// read, which leaves replaced secrets in place rather than risk removing a live one.
func storedVaultPaths(tx *gorm.DB, model any, keyer schemas.VaultPathKeyer) map[string]bool {
	columner, ok := model.(vaultPathKeyColumner)
	key := keyer.VaultPathKey()
	if !ok || key == "" {
		return nil
	}
	stored := reflect.New(reflect.TypeOf(model).Elem()).Interface()
	result := tx.Session(&gorm.Session{NewDB: true, Context: tx.Statement.Context}).
		Table(tx.Statement.Table).
		Where(columner.VaultPathKeyColumn()+" = ?", key).
		Limit(1).
		Find(stored)
	if result.Error != nil {
		return nil
	}
	if result.RowsAffected == 0 {
		return map[string]bool{}
	}
	return ownedPaths(schemas.VaultBasePath(tx.Statement.Table, key), stored)
}

// ownedPaths returns the vault paths model references that are owned under base.
func ownedPaths(base string, model any) map[string]bool {
	owned := make(map[string]bool)
	for _, path := range schemas.VaultSecretPaths(model) {
		if schemas.OwnsVaultPath(base, path) {
			owned[path] = true
		}
	}
	return owned
}

func pathSet(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, path := range paths {
		set[path] = true
	}
	return set
}

func appendPaths(dst []string, set map[string]bool) []string {
	for path := range set {
		dst = append(dst, path)
	}
	return dst
}

// forEachModel extracts the model(s) from the GORM statement and calls fn for
// each one that implements VaultPathKeyer. Handles both single structs and
// slices (batch operations).
func forEachModel(tx *gorm.DB, fn func(model interface{}, keyer schemas.VaultPathKeyer)) {
	if tx.Statement == nil {
		return
	}
	rv := tx.Statement.ReflectValue
	switch rv.Kind() {
	case reflect.Struct:
		if !rv.CanAddr() {
			return
		}
		model := rv.Addr().Interface()
		if keyer, ok := model.(schemas.VaultPathKeyer); ok {
			fn(model, keyer)
		}
	case reflect.Slice:
		for i := 0; i < rv.Len(); i++ {
			elem := rv.Index(i)
			if elem.Kind() == reflect.Ptr {
				elem = elem.Elem()
			}
			if !elem.CanAddr() {
				continue
			}
			model := elem.Addr().Interface()
			if keyer, ok := model.(schemas.VaultPathKeyer); ok {
				fn(model, keyer)
			}
		}
	}
}
