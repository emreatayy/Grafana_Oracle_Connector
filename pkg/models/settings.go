package models

import (
	"encoding/json"
	"fmt"
	"strings"

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
	DefaultPort               = 1521 // placeholder for TNS descriptor DSNs (report §16)

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
	// TLSMode selects the Oracle server TLS behavior (report §16): "" = plain
	// connection (the default, unchanged behaviour), "skip-verify" = TLS
	// without certificate validation (operator opt-in), "custom-ca" = TLS where
	// the server is verified against the TLSCAPEM bundle.
	TLSMode string `json:"tlsMode"`
	// TLSCAPEM holds a PEM bundle used to verify the Oracle server when TLSMode
	// is "custom-ca". It is a public certificate, not a credential, so it lives
	// in jsonData — never in secureJsonData.
	TLSCAPEM string `json:"tlsCaPem"`
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
	// ColumnUnits is the column→Grafana-unit mapping (report §16) as a
	// "NAME=unit" list; entries are separated by commas or newlines and the
	// column name is matched case-insensitively (Oracle returns uppercase
	// unquoted names). ParseColumnUnits turns it into a lookup map applied as
	// data.FieldConfig.Unit by the frame builder. Blank means no unit mapping.
	ColumnUnits string `json:"columnUnits"`
	// NumericVariables lists dashboard variable names that may expand unquoted
	// in SQL (report §8.2). It is a hint, not a trust grant: the backend still
	// requires the value to match a strict numeric literal before emitting it
	// bare.
	NumericVariables []string `json:"numericVariables"`
	// Prometheus proxy (report §9.1). promEnabled gates the whole proxy; when
	// false a Prometheus-mode query fails cleanly before any HTTP call. The
	// credentials for promAuthType live in SecretPluginSettings.
	PromEnabled       bool   `json:"promEnabled"`
	PromURL           string `json:"promUrl"`
	PromAuthType      string `json:"promAuthType"` // none | basic | token
	PromTlsSkipVerify bool   `json:"promTlsSkipVerify"`
}

// NumericVariableSet returns the configured numeric variable names as a set for
// O(1) lookup during interpolation (§8.2).
func (s *OracleSettings) NumericVariableSet() map[string]bool {
	set := make(map[string]bool, len(s.NumericVariables))
	for _, n := range s.NumericVariables {
		n = strings.TrimSpace(n)
		if n != "" {
			set[n] = true
		}
	}
	return set
}

// ParseColumnUnits turns the columnUnits text ("NAME=unit", comma- or
// newline-separated) into a map keyed by the column name in UPPERCASE so it can
// be matched against Oracle's unquoted identifier case. A blank or all-empty
// input returns nil. Entries without '=' are skipped; surrounding whitespace is
// trimmed from both sides.
func ParseColumnUnits(s string) map[string]string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	if len(fields) == 0 {
		return nil
	}
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		name, unit, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		name = strings.ToUpper(strings.TrimSpace(name))
		unit = strings.TrimSpace(unit)
		if name == "" || unit == "" {
			continue
		}
		m[name] = unit
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// SecretPluginSettings holds decrypted secure fields (never sent to the
// browser). o_password is the Oracle password (report §4.3); promUser,
// promPassword and promToken are the Prometheus proxy credentials used when
// promAuthType is "basic"/"token" (§9.1) — all three are sent to Prometheus
// over the proxy connection and are never logged.
type SecretPluginSettings struct {
	O_Password   string `json:"o_password"`
	PromUser     string `json:"promUser"`
	PromPassword string `json:"promPassword"`
	PromToken    string `json:"promToken"`
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

// PromCreds returns the Prometheus proxy credentials for the configured
// promAuthType (report §9.1). For "basic" it returns the user/password; for
// "token" it returns ("", token) so the caller adds the Bearer header.
func (s *PluginSettings) PromCreds() (user, password, token string) {
	if s.Secrets == nil {
		return "", "", ""
	}
	return s.Secrets.PromUser, s.Secrets.PromPassword, s.Secrets.PromToken
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
		O_Password:   source["o_password"],
		PromUser:     source["promUser"],
		PromPassword: source["promPassword"],
		PromToken:    source["promToken"],
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
