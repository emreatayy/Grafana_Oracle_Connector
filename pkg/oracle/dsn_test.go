package oracle

import (
	"strings"
	"testing"

	"github.com/emreatayy/oracle-datasource/pkg/models"
)

func settings() *models.PluginSettings {
	return &models.PluginSettings{
		OracleSettings: models.OracleSettings{
			Hostname: "db.example.com",
			Port:     1521,
			Service:  "XEPDB1",
			User:     "SYSTEM",
		},
		Secrets: &models.SecretPluginSettings{O_Password: "p@ss/word"},
	}
}

func TestBuildDSN_ServiceMode(t *testing.T) {
	dsn, err := BuildDSN(settings())
	if err != nil {
		t.Fatalf("BuildDSN: %v", err)
	}
	// go-ora BuildUrl escapes user/password/service with url.PathEscape
	// (@ is left literal) and emits option keys verbatim (space unescaped).
	want := "oracle://SYSTEM:p@ss%2Fword@db.example.com:1521/XEPDB1?CONNECTION TIMEOUT=10"
	if dsn != want {
		t.Errorf("dsn = %q, want %q", dsn, want)
	}
}

func TestBuildDSN_SIDMode(t *testing.T) {
	s := settings()
	s.Service = ""
	s.SID = "ORCL"

	dsn, err := BuildDSN(s)
	if err != nil {
		t.Fatalf("BuildDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "oracle://SYSTEM:p@ss%2Fword@db.example.com:1521/ORCL?") {
		t.Errorf("dsn = %q, want ORCL path", dsn)
	}
	// The ?SID= query option selects SID resolution in go-ora.
	if !strings.Contains(dsn, "SID=ORCL") {
		t.Errorf("dsn = %q, want SID=ORCL option", dsn)
	}
}

func TestBuildDSN_ConnStringPriority(t *testing.T) {
	s := settings()
	s.ConnStr = "oracle://app:secret@other.example.com:1522/APP"
	dsn, err := BuildDSN(s)
	if err != nil {
		t.Fatalf("BuildDSN: %v", err)
	}
	if dsn != "oracle://app:secret@other.example.com:1522/APP" {
		t.Errorf("dsn = %q, want the explicit connection string", dsn)
	}
}

func TestBuildDSN_ServiceWinsOverSID(t *testing.T) {
	s := settings()
	s.SID = "ORCL" // service already set
	dsn, err := BuildDSN(s)
	if err != nil {
		t.Fatalf("BuildDSN: %v", err)
	}
	if !strings.Contains(dsn, "/XEPDB1") || strings.Contains(dsn, "SID=") {
		t.Errorf("dsn = %q, want service path without SID option", dsn)
	}
}

func TestBuildDSN_Validation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*models.PluginSettings)
		want   string
	}{
		{"missing host", func(s *models.PluginSettings) { s.Hostname = "" }, "host is required"},
		{"missing port", func(s *models.PluginSettings) { s.Port = 0 }, "port must be between"},
		{"bad port", func(s *models.PluginSettings) { s.Port = 70000 }, "port must be between"},
		{"missing user", func(s *models.PluginSettings) { s.User = "" }, "username is required"},
		{"missing password", func(s *models.PluginSettings) { s.Secrets.O_Password = "" }, "password is required"},
		{"missing service and sid", func(s *models.PluginSettings) { s.Service = ""; s.SID = "" }, "service name or a SID"},
		{"bad conn string", func(s *models.PluginSettings) { s.ConnStr = "host:1521/xe" }, "must start with oracle://"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := settings()
			tc.mutate(s)
			_, err := BuildDSN(s)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}
