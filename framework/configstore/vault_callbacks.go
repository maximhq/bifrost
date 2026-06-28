package configstore

import (
	"reflect"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
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

// RegisterVaultCallbacks installs global GORM callbacks that automatically
// store plaintext SecretVar fields into the vault before create/update and
// remove owned vault secrets after delete. Models opt in by implementing
// schemas.VaultPathKeyer; models that don't implement it are silently skipped.
//
// The store callback runs at Before("gorm:before_create") / Before("gorm:before_update"),
// so the vault ref replaces the plaintext before BeforeSave serializes/encrypts it.
// This is correct for models whose SecretVar fields are set by the caller
// (TableMCPClient.Headers, TableOauthConfig.ClientSecret). Models that populate their
// SecretVar columns inside BeforeSave implement vaultStoreSelfManaged and do their own
// store at the correct midpoint; the global callback skips them.
func RegisterVaultCallbacks(db *gorm.DB) {
	db.Callback().Create().Before("gorm:before_create").Register("bifrost:vault_store", vaultStoreCallback)
	db.Callback().Update().Before("gorm:before_update").Register("bifrost:vault_store", vaultStoreCallback)
	db.Callback().Delete().After("gorm:after_delete").Register("bifrost:vault_remove", vaultRemoveCallback)
}

func vaultStoreCallback(tx *gorm.DB) {
	if !schemas.VaultStoreWriteEnabled() {
		return
	}
	forEachModel(tx, func(model interface{}, keyer schemas.VaultPathKeyer) {
		if _, ok := model.(vaultStoreSelfManaged); ok {
			return // model stores its own vault secrets inside BeforeSave
		}
		tableName := tx.Statement.Table
		base := schemas.VaultBasePath(tableName, keyer.VaultPathKey())
		if err := schemas.StoreOwnedVaultSecretVars(tx.Statement.Context, base, model); err != nil {
			_ = tx.AddError(err)
		}
	})
}

func vaultRemoveCallback(tx *gorm.DB) {
	if !schemas.VaultStoreWriteEnabled() {
		return
	}
	forEachModel(tx, func(model interface{}, keyer schemas.VaultPathKeyer) {
		tableName := tx.Statement.Table
		base := schemas.VaultBasePath(tableName, keyer.VaultPathKey())
		schemas.RemoveOwnedVaultSecretVars(tx.Statement.Context, base, model)
	})
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
