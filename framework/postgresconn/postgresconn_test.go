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

func TestRunPasswordCommand(t *testing.T) {
	password, err := RunPasswordCommand(context.Background(), &PasswordCommandConfig{
		Command: "printf",
		Args:    []string{"secret\n"},
	})

	require.NoError(t, err)
	require.Equal(t, "secret", password)
}

func TestRunPasswordCommandRejectsEmptyOutput(t *testing.T) {
	_, err := RunPasswordCommand(context.Background(), &PasswordCommandConfig{
		Command: "printf",
		Args:    []string{""},
	})

	require.ErrorContains(t, err, "empty stdout")
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

type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}
