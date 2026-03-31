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

// encryptEnvVar encrypts the Val field of an EnvVar in place using AES-256-GCM.
// It is a no-op if the field is nil, references an environment variable, or has an empty value.
func encryptEnvVar(field *schemas.EnvVar) error {
	if field == nil || field.IsFromEnv() || field.GetValue() == "" {
		return nil
	}
	encrypted, err := encrypt.Encrypt(field.Val)
	if err != nil {
		return err
	}
	field.Val = encrypted
	return nil
}

// decryptEnvVar decrypts the Val field of an EnvVar in place using AES-256-GCM.
// It is a no-op if the field is nil, references an environment variable, or has an empty value.
func decryptEnvVar(field *schemas.EnvVar) error {
	if field == nil || field.IsFromEnv() || field.GetValue() == "" {
		return nil
	}
	decrypted, err := encrypt.Decrypt(field.Val)
	if err != nil {
		return err
	}
	field.Val = decrypted
	return nil
}

// encryptEnvVarPtr encrypts the Val field of a pointer-to-EnvVar in place.
// It is a no-op if the pointer or the EnvVar it points to is nil.
func encryptEnvVarPtr(field **schemas.EnvVar) error {
	if field == nil || *field == nil {
		return nil
	}
	return encryptEnvVar(*field)
}

// decryptEnvVarPtr decrypts the Val field of a pointer-to-EnvVar in place.
// It is a no-op if the pointer or the EnvVar it points to is nil.
func decryptEnvVarPtr(field **schemas.EnvVar) error {
	if field == nil || *field == nil {
		return nil
	}
	return decryptEnvVar(*field)
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
