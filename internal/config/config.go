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
	EnvConfluenceBaseURL         = "CONFLUENCE_BASE_URL"
	EnvConfluenceAuthType        = "CONFLUENCE_AUTH_TYPE" // auto | basic | bearer | scoped
	EnvConfluenceCloudID         = "CONFLUENCE_CLOUD_ID"
	EnvConfluenceUsername        = "CONFLUENCE_USERNAME"
	EnvConfluenceToken           = "CONFLUENCE_TOKEN"
	EnvConfluenceCQL             = "CONFLUENCE_CQL"
	EnvConfluenceAttachments     = "CONFLUENCE_ATTACHMENTS"      // on | off (default on)
	EnvConfluenceAttachmentTypes = "CONFLUENCE_ATTACHMENT_TYPES" // CSV allow-list of extensions
	EnvConfluenceAttachmentMaxB  = "CONFLUENCE_ATTACHMENT_MAX_BYTES"
	EnvConfluenceAttachmentMaxCh = "CONFLUENCE_ATTACHMENT_MAX_CHARS"
	EnvOnyxBaseURL               = "ONYX_BASE_URL"
	EnvOnyxAPIKey                = "ONYX_API_KEY"
	EnvOnyxCCPairID              = "ONYX_CC_PAIR_ID"
	EnvOnyxIngestionPath         = "ONYX_INGESTION_PATH"
	EnvHTTPTimeout               = "HTTP_TIMEOUT"
	EnvOnyxHTTPTimeout           = "ONYX_HTTP_TIMEOUT"
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
	// ConfluenceAttachments enables the attachment second stream
	// (DefaultTypes allow-list unless ConfluenceAttachmentTypes is set).
	ConfluenceAttachments bool
	// ConfluenceAttachmentTypes holds the allowed extensions
	// (lowercase, dot-less). Empty = package default.
	ConfluenceAttachmentTypes []string
	// ConfluenceAttachmentMaxBytes caps a single download (default 25 MiB).
	ConfluenceAttachmentMaxBytes int
	// ConfluenceAttachmentMaxChars caps converted text (default 100k).
	ConfluenceAttachmentMaxChars int
	OnyxBaseURL                  string
	OnyxAPIKey                   string
	OnyxCCPairID                 int
	// OnyxIngestionPath overrides the ingestion endpoint path for
	// deployments behind the chart nginx (e.g. "/api/onyx-api/ingestion").
	// Empty means the client's default.
	OnyxIngestionPath string
	HTTPTimeout       time.Duration
	// OnyxHTTPTimeout bounds each ingestion POST. Ingestion is
	// server-side pipeline work (extraction, chunking, embedding) and
	// can legitimately take tens of seconds, so it defaults higher than
	// HTTPTimeout and can be tuned separately.
	OnyxHTTPTimeout time.Duration
}

// Load reads configuration from getenv and validates it. Missing or invalid
// values are reported by variable name only; secrets are never echoed back.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		ConfluenceAttachments:        true,
		ConfluenceAttachmentMaxBytes: 25 << 20,
		ConfluenceAttachmentMaxChars: 100_000,
		ConfluenceBaseURL:            strings.TrimRight(getenv(EnvConfluenceBaseURL), "/"),
		ConfluenceAuthType:           strings.ToLower(strings.TrimSpace(getenv(EnvConfluenceAuthType))),
		ConfluenceCloudID:            strings.TrimSpace(getenv(EnvConfluenceCloudID)),
		ConfluenceUsername:           getenv(EnvConfluenceUsername),
		ConfluenceToken:              getenv(EnvConfluenceToken),
		ConfluenceCQL:                getenv(EnvConfluenceCQL),
		OnyxBaseURL:                  strings.TrimRight(getenv(EnvOnyxBaseURL), "/"),
		OnyxAPIKey:                   getenv(EnvOnyxAPIKey),
		OnyxIngestionPath:            getenv(EnvOnyxIngestionPath),
		HTTPTimeout:                  30 * time.Second,
	}

	switch strings.ToLower(strings.TrimSpace(getenv(EnvConfluenceAttachments))) {
	case "", "on", "true", "1", "yes":
		cfg.ConfluenceAttachments = true
	case "off", "false", "0", "no":
		cfg.ConfluenceAttachments = false
	default:
		return Config{}, fmt.Errorf("%s must be on or off", EnvConfluenceAttachments)
	}
	if raw := getenv(EnvConfluenceAttachmentTypes); raw != "" {
		var types []string
		for t := range strings.SplitSeq(raw, ",") {
			if t = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(t, "."))); t != "" {
				types = append(types, t)
			}
		}
		if len(types) == 0 {
			return Config{}, fmt.Errorf("%s must name at least one extension", EnvConfluenceAttachmentTypes)
		}
		cfg.ConfluenceAttachmentTypes = types
	}
	if raw := getenv(EnvConfluenceAttachmentMaxB); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive integer (bytes)", EnvConfluenceAttachmentMaxB)
		}
		cfg.ConfluenceAttachmentMaxBytes = n
	}
	if raw := getenv(EnvConfluenceAttachmentMaxCh); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive integer", EnvConfluenceAttachmentMaxCh)
		}
		cfg.ConfluenceAttachmentMaxChars = n
	}

	if raw := getenv(EnvHTTPTimeout); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration (e.g. 30s)", EnvHTTPTimeout)
		}
		cfg.HTTPTimeout = d
	}

	cfg.OnyxHTTPTimeout = 2 * time.Minute
	if cfg.HTTPTimeout != 30*time.Second {
		// Preserve pre-split behavior: an explicit HTTP_TIMEOUT previously
		// governed every request, including ingestion.
		cfg.OnyxHTTPTimeout = cfg.HTTPTimeout
	}
	if raw := getenv(EnvOnyxHTTPTimeout); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration (e.g. 120s)", EnvOnyxHTTPTimeout)
		}
		cfg.OnyxHTTPTimeout = d
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
