package postgresconn

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestValidatePasswordCommandAllowsEmptyPassword(t *testing.T) {
	cfg := validConfig()
	cfg.Password = schemas.NewEnvVar("")
	cfg.PasswordCommand = &PasswordCommandConfig{Command: "printf", Args: []string{"secret"}}

	require.NoError(t, Validate(cfg, true))
}

func TestValidatePasswordAndPasswordCommandAreExclusive(t *testing.T) {
	cfg := validConfig()
	cfg.PasswordCommand = &PasswordCommandConfig{Command: "printf", Args: []string{"secret"}}

	require.ErrorContains(t, Validate(cfg, true), "mutually exclusive")
}

func TestValidateRequiresStaticPasswordWhenConfigured(t *testing.T) {
	cfg := validConfig()
	cfg.Password = schemas.NewEnvVar("")

	require.ErrorContains(t, Validate(cfg, true), "postgres password is required")
}

func TestValidateRejectsInvalidConnMaxLifetime(t *testing.T) {
	cfg := validConfig()
	cfg.ConnMaxLifetime = "sometimes"

	require.ErrorContains(t, Validate(cfg, true), "invalid postgres conn_max_lifetime")
}

func TestValidateRejectsNonPositiveConnMaxLifetime(t *testing.T) {
	cfg := validConfig()
	cfg.ConnMaxLifetime = "0s"

	require.ErrorContains(t, Validate(cfg, true), "postgres conn_max_lifetime must be positive")
}

func TestCloseNilDBDoesNotPanic(t *testing.T) {
	require.NotPanics(t, func() {
		Close(nil, nil)
	})
}

func TestRunPasswordCommand(t *testing.T) {
	password, err := RunPasswordCommand(context.Background(), &PasswordCommandConfig{
		Command: "printf",
		Args:    []string{"secret\n"},
	})

	require.NoError(t, err)
	require.Equal(t, "secret", password)
}

func TestRunPasswordCommandRequiresConfig(t *testing.T) {
	_, err := RunPasswordCommand(context.Background(), nil)

	require.ErrorContains(t, err, "postgres password_command config is required")
}

func TestRunPasswordCommandRequiresCommand(t *testing.T) {
	_, err := RunPasswordCommand(context.Background(), &PasswordCommandConfig{
		Command: " ",
	})

	require.ErrorContains(t, err, "postgres password_command.command is required")
}

func TestRunPasswordCommandRejectsCommandWithInlineArgs(t *testing.T) {
	_, err := RunPasswordCommand(context.Background(), &PasswordCommandConfig{
		Command: "printf secret",
	})

	require.ErrorContains(t, err, "single executable")
}

func TestRunPasswordCommandRejectsShellInterpreter(t *testing.T) {
	_, err := RunPasswordCommand(context.Background(), &PasswordCommandConfig{
		Command: "sh",
		Args:    []string{"-c", "printf secret"},
	})

	require.ErrorContains(t, err, "must not invoke a shell interpreter")
}

func TestRunPasswordCommandRejectsEmptyOutput(t *testing.T) {
	_, err := RunPasswordCommand(context.Background(), &PasswordCommandConfig{
		Command: "printf",
		Args:    []string{""},
	})

	require.ErrorContains(t, err, "empty stdout")
}

func TestRunPasswordCommandIncludesStderr(t *testing.T) {
	_, err := RunPasswordCommand(context.Background(), &PasswordCommandConfig{
		Command: "ls",
		Args:    []string{"/definitely/not/a/real/postgres/password/file"},
	})

	require.ErrorContains(t, err, "exit status")
	require.ErrorContains(t, err, "definitely/not/a/real/postgres/password/file")
}

func TestRunPasswordCommandTimeout(t *testing.T) {
	_, err := RunPasswordCommand(context.Background(), &PasswordCommandConfig{
		Command: "sleep",
		Args:    []string{"1"},
		Timeout: "1ms",
	})

	require.ErrorContains(t, err, "timed out")
}

func TestRunPasswordCommandStartErrorIsNotTimeout(t *testing.T) {
	_, err := RunPasswordCommand(context.Background(), &PasswordCommandConfig{
		Command: "/definitely/not/a/real/postgres/password/command",
		Timeout: "1ns",
	})

	require.ErrorContains(t, err, "failed to start")
	require.NotContains(t, err.Error(), "timed out")
}

func validConfig() *Config {
	return &Config{
		Host:     schemas.NewEnvVar("localhost"),
		Port:     schemas.NewEnvVar("5432"),
		User:     schemas.NewEnvVar("bifrost"),
		Password: schemas.NewEnvVar("password"),
		DBName:   schemas.NewEnvVar("bifrost"),
		SSLMode:  schemas.NewEnvVar("disable"),
	}
}
