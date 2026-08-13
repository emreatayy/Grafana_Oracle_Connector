package models

import (
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

func TestLoadPluginSettings_AppliesDefaults(t *testing.T) {
	source := backend.DataSourceInstanceSettings{
		JSONData:               []byte(`{}`),
		DecryptedSecureJSONData: map[string]string{"o_password": "secret"},
	}

	settings, err := LoadPluginSettings(source)
	if err != nil {
		t.Fatalf("LoadPluginSettings: %v", err)
	}

	if settings.MaxOpenConns != DefaultMaxOpenConns {
		t.Errorf("MaxOpenConns = %d, want %d", settings.MaxOpenConns, DefaultMaxOpenConns)
	}
	if settings.MaxIdleConns != DefaultMaxIdleConns {
		t.Errorf("MaxIdleConns = %d, want %d", settings.MaxIdleConns, DefaultMaxIdleConns)
	}
	if settings.ConnMaxLifetimeSec != DefaultConnMaxLifetimeSec {
		t.Errorf("ConnMaxLifetimeSec = %d, want %d", settings.ConnMaxLifetimeSec, DefaultConnMaxLifetimeSec)
	}
	if settings.QueryTimeoutSec != DefaultQueryTimeoutSec {
		t.Errorf("QueryTimeoutSec = %d, want %d", settings.QueryTimeoutSec, DefaultQueryTimeoutSec)
	}
	if settings.MaxRows != DefaultMaxRows {
		t.Errorf("MaxRows = %d, want %d", settings.MaxRows, DefaultMaxRows)
	}
	if settings.MaxSeries != DefaultMaxSeries {
		t.Errorf("MaxSeries = %d, want %d", settings.MaxSeries, DefaultMaxSeries)
	}
	if settings.SessionTimeZone != DefaultSessionTimeZone {
		t.Errorf("SessionTimeZone = %q, want %q", settings.SessionTimeZone, DefaultSessionTimeZone)
	}
	if got := settings.Password(); got != "secret" {
		t.Errorf("Password() = %q, want %q", got, "secret")
	}
}

func TestLoadPluginSettings_KeepsProvidedValues(t *testing.T) {
	source := backend.DataSourceInstanceSettings{
		JSONData: []byte(`{
			"o_hostname": "db.example.com",
			"o_port": 1521,
			"maxOpenConns": 25,
			"queryTimeoutSec": 5,
			"sessionTimeZone": "Europe/Istanbul"
		}`),
	}

	settings, err := LoadPluginSettings(source)
	if err != nil {
		t.Fatalf("LoadPluginSettings: %v", err)
	}

	if settings.Hostname != "db.example.com" {
		t.Errorf("Hostname = %q, want %q", settings.Hostname, "db.example.com")
	}
	if settings.Port != 1521 {
		t.Errorf("Port = %d, want 1521", settings.Port)
	}
	if settings.MaxOpenConns != 25 {
		t.Errorf("MaxOpenConns = %d, want 25", settings.MaxOpenConns)
	}
	if settings.QueryTimeoutSec != 5 {
		t.Errorf("QueryTimeoutSec = %d, want 5", settings.QueryTimeoutSec)
	}
	if settings.SessionTimeZone != "Europe/Istanbul" {
		t.Errorf("SessionTimeZone = %q, want %q", settings.SessionTimeZone, "Europe/Istanbul")
	}
	// MaxIdle is still zero -> default.
	if settings.MaxIdleConns != DefaultMaxIdleConns {
		t.Errorf("MaxIdleConns = %d, want default %d", settings.MaxIdleConns, DefaultMaxIdleConns)
	}
}

func TestLoadPluginSettings_InvalidJSON(t *testing.T) {
	source := backend.DataSourceInstanceSettings{JSONData: []byte(`{not json`)}
	if _, err := LoadPluginSettings(source); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParseColumnUnits(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]string
	}{
		{name: "blank", in: "", want: nil},
		{name: "comma separate", in: "LOAD=percent, MEM_BYTES=decbytes", want: map[string]string{"LOAD": "percent", "MEM_BYTES": "decbytes"}},
		{name: "newline separate", in: "LOAD=percent\nMEM_BYTES=decbytes", want: map[string]string{"LOAD": "percent", "MEM_BYTES": "decbytes"}},
		{name: "case insensitive key", in: "load=percent", want: map[string]string{"LOAD": "percent"}},
		{name: "mixed whitespace", in: "  load = percent  ,\n  name=bytes  ", want: map[string]string{"LOAD": "percent", "NAME": "bytes"}},
		{name: "malformed skipped", in: "LOAD=percent, plain, =x, NAME=, =", want: map[string]string{"LOAD": "percent"}},
		{name: "only junk", in: "plain\n=,\nNOPE=", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseColumnUnits(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseColumnUnits(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("ParseColumnUnits(%q)[%q] = %q, want %q", tc.in, k, got[k], v)
				}
			}
		})
	}
}
