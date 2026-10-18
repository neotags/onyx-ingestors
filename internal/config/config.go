// Package config loads validated runtime configuration for onyx-ingestor.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Environment variable names read by config.Load.
const (
	EnvConfluenceBaseURL  = "CONFLUENCE_BASE_URL"
	EnvConfluenceAuthType = "CONFLUENCE_AUTH_TYPE" // auto | basic | bearer | scoped
	EnvConfluenceCloudID  = "CONFLUENCE_CLOUD_ID"
	EnvConfluenceUsername = "CONFLUENCE_USERNAME"
	EnvConfluenceToken    = "CONFLUENCE_TOKEN"
	EnvConfluenceCQL      = "CONFLUENCE_CQL"
	EnvOnyxBaseURL        = "ONYX_BASE_URL"
	EnvOnyxAPIKey         = "ONYX_API_KEY"
	EnvOnyxCCPairID       = "ONYX_CC_PAIR_ID"
	EnvHTTPTimeout        = "HTTP_TIMEOUT"
)

// Config is the typed, validated runtime configuration.
type Config struct {
	ConfluenceBaseURL string
	// ConfluenceAuthType selects the auth mode: auto (default), basic
	// (classic API token / username+token), bearer (Data Center PAT),
	// or scoped (Atlassian scoped API token via the API gateway).
	ConfluenceAuthType string
	// ConfluenceCloudID is the Atlassian cloud ID, required for scoped
	// API tokens in cloud mode.
	ConfluenceCloudID string
	// ConfluenceUsername is optional: set it together with ConfluenceToken
	// for basic auth (Confluence Cloud classic token and Data Center).
	// With no username, the token is sent as a bearer token.
	ConfluenceUsername string
	ConfluenceToken    string
	ConfluenceCQL      string
	OnyxBaseURL        string
	OnyxAPIKey         string
	OnyxCCPairID       int
	HTTPTimeout        time.Duration
}

// Load reads configuration from getenv and validates it. Missing or invalid
// values are reported by variable name only; secrets are never echoed back.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		ConfluenceBaseURL:  strings.TrimRight(getenv(EnvConfluenceBaseURL), "/"),
		ConfluenceAuthType: strings.ToLower(strings.TrimSpace(getenv(EnvConfluenceAuthType))),
		ConfluenceCloudID:  strings.TrimSpace(getenv(EnvConfluenceCloudID)),
		ConfluenceUsername: getenv(EnvConfluenceUsername),
		ConfluenceToken:    getenv(EnvConfluenceToken),
		ConfluenceCQL:      getenv(EnvConfluenceCQL),
		OnyxBaseURL:        strings.TrimRight(getenv(EnvOnyxBaseURL), "/"),
		OnyxAPIKey:         getenv(EnvOnyxAPIKey),
		HTTPTimeout:        30 * time.Second,
	}

	if raw := getenv(EnvHTTPTimeout); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration (e.g. 30s)", EnvHTTPTimeout)
		}
		cfg.HTTPTimeout = d
	}

	if raw := getenv(EnvOnyxCCPairID); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive integer", EnvOnyxCCPairID)
		}
		cfg.OnyxCCPairID = id
	}

	if cfg.ConfluenceAuthType != "" {
		switch cfg.ConfluenceAuthType {
		case "auto", "basic", "bearer", "scoped":
		default:
			return Config{}, fmt.Errorf("%s must be auto, basic, bearer, or scoped", EnvConfluenceAuthType)
		}
	}
	if cfg.ConfluenceAuthType == "scoped" && cfg.ConfluenceCloudID == "" {
		return Config{}, fmt.Errorf("%s must be set when %s=scoped", EnvConfluenceCloudID, EnvConfluenceAuthType)
	}

	var missing []string
	if cfg.ConfluenceBaseURL == "" {
		missing = append(missing, EnvConfluenceBaseURL)
	} else if err := validBaseURL(cfg.ConfluenceBaseURL); err != nil {
		return Config{}, fmt.Errorf("%s %w", EnvConfluenceBaseURL, err)
	}
	if cfg.OnyxBaseURL == "" {
		missing = append(missing, EnvOnyxBaseURL)
	} else if err := validBaseURL(cfg.OnyxBaseURL); err != nil {
		return Config{}, fmt.Errorf("%s %w", EnvOnyxBaseURL, err)
	}
	if cfg.ConfluenceToken == "" {
		missing = append(missing, EnvConfluenceToken)
	}
	if cfg.OnyxAPIKey == "" {
		missing = append(missing, EnvOnyxAPIKey)
	}
	if cfg.OnyxCCPairID == 0 {
		missing = append(missing, EnvOnyxCCPairID)
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

func validBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("must be an absolute http(s) URL")
	}
	return nil
}
