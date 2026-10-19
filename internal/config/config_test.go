package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validEnv() map[string]string {
	return map[string]string{
		EnvConfluenceBaseURL: "https://example.atlassian.net/wiki",
		EnvConfluenceToken:   "super-secret-token",
		EnvOnyxBaseURL:       "https://onyx.example.com",
		EnvOnyxAPIKey:        "onyx-secret-key",
		EnvOnyxCCPairID:      "12",
	}
}

func getenvFrom(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(getenvFrom(validEnv()))
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, cfg.HTTPTimeout)
	assert.Equal(t, 12, cfg.OnyxCCPairID)
	assert.False(t, strings.HasSuffix(cfg.ConfluenceBaseURL, "/"), "base URL keeps trailing slash")
	assert.Empty(t, cfg.ConfluenceAuthType)
}

func TestLoadMissingRequired(t *testing.T) {
	env := validEnv()
	delete(env, EnvOnyxAPIKey)
	delete(env, EnvConfluenceToken)
	delete(env, EnvOnyxCCPairID)

	_, err := Load(getenvFrom(env))
	require.Error(t, err)
	for _, name := range []string{EnvOnyxAPIKey, EnvConfluenceToken, EnvOnyxCCPairID} {
		assert.ErrorContains(t, err, name)
	}
	for _, secret := range []string{"super-secret-token", "onyx-secret-key"} {
		assert.NotContains(t, err.Error(), secret, "error must not leak secret values")
	}
}

func TestLoadRejectsBadURL(t *testing.T) {
	env := validEnv()
	env[EnvConfluenceBaseURL] = "not-a-url"
	_, err := Load(getenvFrom(env))
	require.Error(t, err)
	assert.ErrorContains(t, err, EnvConfluenceBaseURL)
}

func TestLoadRejectsBadCCPairID(t *testing.T) {
	for _, bad := range []string{"abc", "0", "-3"} {
		env := validEnv()
		env[EnvOnyxCCPairID] = bad
		_, err := Load(getenvFrom(env))
		assert.Error(t, err, "cc pair id %q should be rejected", bad)
	}
}

func TestLoadTimeout(t *testing.T) {
	env := validEnv()
	env[EnvHTTPTimeout] = "5s"
	cfg, err := Load(getenvFrom(env))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, cfg.HTTPTimeout)

	env[EnvHTTPTimeout] = "soon"
	_, err = Load(getenvFrom(env))
	assert.Error(t, err)
}

func TestLoadAuthType(t *testing.T) {
	env := validEnv()
	env[EnvConfluenceAuthType] = "Scoped"
	env[EnvConfluenceCloudID] = "cloud-abc"
	cfg, err := Load(getenvFrom(env))
	require.NoError(t, err)
	assert.Equal(t, "scoped", cfg.ConfluenceAuthType)
	assert.Equal(t, "cloud-abc", cfg.ConfluenceCloudID)
}

func TestLoadScopedRequiresCloudID(t *testing.T) {
	env := validEnv()
	env[EnvConfluenceAuthType] = "scoped"
	_, err := Load(getenvFrom(env))
	require.Error(t, err)
	assert.ErrorContains(t, err, EnvConfluenceCloudID)
}

func TestLoadRejectsUnknownAuthType(t *testing.T) {
	env := validEnv()
	env[EnvConfluenceAuthType] = "oauth"
	_, err := Load(getenvFrom(env))
	require.Error(t, err)
	assert.ErrorContains(t, err, EnvConfluenceAuthType)
}

func TestLoadOnyxIngestionPath(t *testing.T) {
	env := validEnv()
	env[EnvOnyxIngestionPath] = "/api/onyx-api/ingestion"
	cfg, err := Load(getenvFrom(env))
	require.NoError(t, err)
	assert.Equal(t, "/api/onyx-api/ingestion", cfg.OnyxIngestionPath)

	cfg, err = Load(getenvFrom(validEnv()))
	require.NoError(t, err)
	assert.Empty(t, cfg.OnyxIngestionPath, "unset means client default")
}
