package oracle

import (
	"fmt"
	"strconv"
	"strings"

	go_ora "github.com/sijms/go-ora/v2"

	"github.com/emreatayy/oracle-datasource/pkg/models"
)

// BuildDSN constructs a go-ora connection string from settings (report §5.1).
// Priority: explicit connString > host+port+service > host+port+SID.
//
// go-ora DSN shape: oracle://user:pass@host:port/service. A SID is requested
// with the ?SID=<sid> query option (verified in go-ora v2.9.0, which then uses
// (SID=...) in the TNS descriptor instead of (SERVICE_NAME=...)).
func BuildDSN(s *models.PluginSettings) (string, error) {
	if connStr := strings.TrimSpace(s.ConnStr); connStr != "" {
		if !strings.HasPrefix(connStr, "oracle://") {
			return "", fmt.Errorf("connection string must start with oracle://")
		}
		return connStr, nil
	}

	// Field-based connection.
	if strings.TrimSpace(s.Hostname) == "" {
		return "", fmt.Errorf("host is required (or provide a connection string)")
	}
	if s.Port < 1 || s.Port > 65535 {
		return "", fmt.Errorf("port must be between 1 and 65535, got %d", s.Port)
	}
	if strings.TrimSpace(s.User) == "" {
		return "", fmt.Errorf("username is required")
	}
	if s.Password() == "" {
		return "", fmt.Errorf("password is required")
	}
	if strings.TrimSpace(s.Service) == "" && strings.TrimSpace(s.SID) == "" {
		return "", fmt.Errorf("either a service name or a SID is required")
	}

	options := map[string]string{
		"CONNECTION TIMEOUT": strconv.Itoa(models.DSNConnectTimeoutSec),
	}

	service := strings.TrimSpace(s.Service)
	if service == "" {
		// SID mode: the service path carries the SID and ?SID= selects SID
		// resolution (connect_config.go: use (SID=...) when SID is set).
		service = strings.TrimSpace(s.SID)
		options["SID"] = service
	}

	return go_ora.BuildUrl(strings.TrimSpace(s.Hostname), s.Port, service,
		strings.TrimSpace(s.User), s.Password(), options), nil
}
