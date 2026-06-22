package tables

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
)

const (
	// EncryptionStatusPlainText indicates the row's sensitive fields are stored as plaintext.
	EncryptionStatusPlainText = "plain_text"
	// EncryptionStatusEncrypted indicates the row's sensitive fields have been encrypted.
	EncryptionStatusEncrypted = "encrypted"
)

// encryptSecretVar encrypts the Val field of an SecretVar in place using AES-256-GCM.
// It is a no-op if the field is nil, references an environment variable or vault, or has an empty value.
func encryptSecretVar(field *schemas.SecretVar) error {
	if field == nil || field.IsFromSecret() || field.GetValue() == "" {
		return nil
	}
	encrypted, err := encrypt.Encrypt(field.Val)
	if err != nil {
		return err
	}
	field.Val = encrypted
	return nil
}

// decryptSecretVar decrypts the Val field of an SecretVar in place using AES-256-GCM.
// It is a no-op if the field is nil, references an environment variable or vault, or has an empty value.
func decryptSecretVar(field *schemas.SecretVar) error {
	if field == nil || field.IsFromSecret() || field.GetValue() == "" {
		return nil
	}
	decrypted, err := encrypt.Decrypt(field.Val)
	if err != nil {
		return err
	}
	field.Val = decrypted
	return nil
}

// encryptSecretVarPtr encrypts the Val field of a pointer-to-SecretVar in place.
// It is a no-op if the pointer or the SecretVar it points to is nil.
func encryptSecretVarPtr(field **schemas.SecretVar) error {
	if field == nil || *field == nil {
		return nil
	}
	return encryptSecretVar(*field)
}

// decryptSecretVarPtr decrypts the Val field of a pointer-to-SecretVar in place.
// It is a no-op if the pointer or the SecretVar it points to is nil.
func decryptSecretVarPtr(field **schemas.SecretVar) error {
	if field == nil || *field == nil {
		return nil
	}
	return decryptSecretVar(*field)
}

// encryptString encrypts the string pointed to by value in place using AES-256-GCM.
// It is a no-op if the pointer is nil or the string is empty.
func encryptString(value *string) error {
	if value == nil || *value == "" {
		return nil
	}
	encrypted, err := encrypt.Encrypt(*value)
	if err != nil {
		return err
	}
	*value = encrypted
	return nil
}

// decryptString decrypts the string pointed to by value in place using AES-256-GCM.
// It is a no-op if the pointer is nil or the string is empty.
func decryptString(value *string) error {
	if value == nil || *value == "" {
		return nil
	}
	decrypted, err := encrypt.Decrypt(*value)
	if err != nil {
		return err
	}
	*value = decrypted
	return nil
}
