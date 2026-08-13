package oracle

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	go_ora "github.com/sijms/go-ora/v2"

	"github.com/emreatayy/oracle-datasource/pkg/models"
)

// BuildDSN constructs a go-ora connection string from settings (report §5.1).
// Priority: explicit connString > TNS descriptor host > host+port+service >
// host+port+SID.
//
// go-ora DSN shape: oracle://user:pass@host:port/service. A SID is requested
// with the ?SID=<sid> query option (verified in go-ora v2.9.0, which then uses
// (SID=...) in the TNS descriptor instead of (SERVICE_NAME=...)). A host that
// starts with '(' is treated as a full TNS connect descriptor and passed to
// go-ora through its CONNSTR parameter (report §16): go-ora extracts the
// ADDRESS servers and SERVICE_NAME/SID from the descriptor, so the port/service
// fields are ignored in this mode.
func BuildDSN(s *models.PluginSettings) (string, error) {
	if connStr := strings.TrimSpace(s.ConnStr); connStr != "" {
		if !strings.HasPrefix(connStr, "oracle://") {
			return "", fmt.Errorf("connection string must start with oracle://")
		}
		return connStr, nil
	}

	host := strings.TrimSpace(s.Hostname)
	if host == "" {
		return "", fmt.Errorf("host is required (or provide a connection string)")
	}
	if strings.TrimSpace(s.User) == "" {
		return "", fmt.Errorf("username is required")
	}
	if s.Password() == "" {
		return "", fmt.Errorf("password is required")
	}

	// TNS descriptor host. The descriptor carries its own ADDRESS list and
	// CONNECT_DATA, so neither port nor service/SID is required or consulted.
	// The DSN is built by hand because go-ora's ParseConfig reads the descriptor
	// through the lowercase ?connStr= key (connect_config.go), while BuildUrl
	// emits option keys verbatim — an uppercase CONNSTR= would be dropped.
	if strings.HasPrefix(host, "(") {
		dsn := fmt.Sprintf("oracle://%s:%s@%s/?CONNECTION TIMEOUT=%d&connStr=%s",
			url.PathEscape(strings.TrimSpace(s.User)), url.PathEscape(s.Password()),
			net.JoinHostPort("", strconv.Itoa(models.DefaultPort)),
			models.DSNConnectTimeoutSec, url.QueryEscape(host))
		return dsn, nil
	}

	if s.Port < 1 || s.Port > 65535 {
		return "", fmt.Errorf("port must be between 1 and 65535, got %d", s.Port)
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

	return go_ora.BuildUrl(host, s.Port, service, strings.TrimSpace(s.User), s.Password(), options), nil
}
