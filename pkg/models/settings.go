package models

import (
	"encoding/json"
	"fmt"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// Defaults (report §5.1 / ADR-11, §21). Safe values applied when a setting is
// empty/zero so an unset field never disables a limit accidentally.
const (
	DefaultMaxOpenConns       = 10
	DefaultMaxIdleConns       = 5
	DefaultConnMaxLifetimeSec = 1800 // 30m
	DefaultQueryTimeoutSec    = 30
	DefaultCommandTimeoutSec  = 10
	DefaultMaxRows            = 100000
	DefaultMaxSeries          = 1000
	DefaultSessionTimeZone    = "UTC"

	// DSNConnectTimeoutSec is the go-ora "CONNECTION TIMEOUT" backstop for a
	// single connect attempt (config.SessionInfo.ConnectTimeout). The health
	// check and query timeouts bound the operation from the caller side.
	DSNConnectTimeoutSec = 10
)

// OracleSettings is the datasource configuration (report §4.3, §5.1). JSON tags
// match the frontend MyDataSourceOptions keys.
type OracleSettings struct {
	ConnStr            string `json:"o_connStr"`
	Hostname           string `json:"o_hostname"`
	Port               int    `json:"o_port"`
	Service            string `json:"o_service"`
	SID                string `json:"o_sid"`
	User               string `json:"o_user"`
	MaxOpenConns       int    `json:"maxOpenConns"`
	MaxIdleConns       int    `json:"maxIdleConns"`
	ConnMaxLifetimeSec int    `json:"connMaxLifetimeSec"`
	QueryTimeoutSec    int    `json:"queryTimeoutSec"`
	CommandTimeoutSec  int    `json:"commandTimeoutSec"`
	MaxRows            int    `json:"maxRows"`
	MaxSeries          int    `json:"maxSeries"`
	SessionTimeZone    string `json:"sessionTimeZone"`
	// LargeNumberMode is the ADR-8 policy for big NUMBER columns (scale=0,
	// precision>15): "" = per-format default (string in table, float in
	// time_series), "string", or "float".
	LargeNumberMode string `json:"largeNumberMode"`
}

// SecretPluginSettings holds decrypted secure fields (never sent to the
// browser). o_password is the Oracle password (report §4.3).
type SecretPluginSettings struct {
	O_Password string `json:"o_password"`
}

// PluginSettings is the fully-loaded datasource configuration. OracleSettings
// is embedded so its fields flatten onto the top-level JSON keys.
type PluginSettings struct {
	OracleSettings
	Secrets *SecretPluginSettings `json:"-"`
}

// Password returns the decrypted Oracle password, or "" when none is set.
func (s *PluginSettings) Password() string {
	if s.Secrets == nil {
		return ""
	}
	return s.Secrets.O_Password
}

// LoadPluginSettings parses a DataSourceInstanceSettings and applies defaults.
func LoadPluginSettings(source backend.DataSourceInstanceSettings) (*PluginSettings, error) {
	settings := PluginSettings{}
	if err := json.Unmarshal(source.JSONData, &settings); err != nil {
		return nil, fmt.Errorf("could not unmarshal plugin settings json: %w", err)
	}
	settings.Secrets = loadSecretPluginSettings(source.DecryptedSecureJSONData)
	settings.applyDefaults()
	return &settings, nil
}

func loadSecretPluginSettings(source map[string]string) *SecretPluginSettings {
	return &SecretPluginSettings{
		O_Password: source["o_password"],
	}
}

// applyDefaults fills safe defaults for any zero-valued limit (§21, ADR-11).
// A zero here is treated as "unset", never as an intentional unlimited value.
func (s *PluginSettings) applyDefaults() {
	if s.MaxOpenConns == 0 {
		s.MaxOpenConns = DefaultMaxOpenConns
	}
	if s.MaxIdleConns == 0 {
		s.MaxIdleConns = DefaultMaxIdleConns
	}
	if s.ConnMaxLifetimeSec == 0 {
		s.ConnMaxLifetimeSec = DefaultConnMaxLifetimeSec
	}
	if s.QueryTimeoutSec == 0 {
		s.QueryTimeoutSec = DefaultQueryTimeoutSec
	}
	if s.CommandTimeoutSec == 0 {
		s.CommandTimeoutSec = DefaultCommandTimeoutSec
	}
	if s.MaxRows == 0 {
		s.MaxRows = DefaultMaxRows
	}
	if s.MaxSeries == 0 {
		s.MaxSeries = DefaultMaxSeries
	}
	if s.SessionTimeZone == "" {
		s.SessionTimeZone = DefaultSessionTimeZone
	}
}
