package oracle

import (
	"strings"
	"testing"

	go_ora "github.com/sijms/go-ora/v2"

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

func TestBuildDSN_DescriptorHost(t *testing.T) {
	// A full TNS connect descriptor in the host field (report §16) is passed
	// through go-ora's CONNSTR parameter: no port/service/SID is required and
	// the address list comes from the descriptor itself.
	desc := "(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=db.example.com)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=XEPDB1)))"
	s := settings()
	s.Hostname = desc
	s.Service = ""
	s.SID = ""

	dsn, err := BuildDSN(s)
	if err != nil {
		t.Fatalf("BuildDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "oracle://SYSTEM:p@ss%2Fword@:1521/?") {
		t.Errorf("dsn = %q, want empty-host placeholder + query options", dsn)
	}
	if !strings.Contains(dsn, "connStr=") {
		t.Errorf("dsn = %q, want a connStr option carrying the descriptor", dsn)
	}

	// ParseConfig must turn the DSN back into the descriptor's address and
	// service, proving go-ora accepts the built string without connecting.
	cfg, err := go_ora.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("servers = %+v, want exactly the descriptor's address", cfg.Servers)
	}
	if cfg.Servers[0].Addr != "db.example.com" || cfg.Servers[0].Port != 1521 {
		t.Errorf("server = %+v, want db.example.com:1521 from the descriptor", cfg.Servers[0])
	}
	if cfg.ServiceName != "XEPDB1" {
		t.Errorf("ServiceName = %q, want XEPDB1 from CONNECT_DATA", cfg.ServiceName)
	}
}

func TestBuildDSN_DescriptorValidation(t *testing.T) {
	// Descriptor mode still requires a user and password.
	s := settings()
	s.Hostname = "(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=db)(PORT=1521)))"
	s.User = ""
	if _, err := BuildDSN(s); err == nil {
		t.Fatal("descriptor mode without a user: expected error, got nil")
	}
}
