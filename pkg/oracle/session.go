package oracle

import (
	"database/sql"
	"fmt"
	"strings"

	go_ora "github.com/sijms/go-ora/v2"
)

// SessionParam is an ALTER SESSION setting applied to every connection of a
// datasource (report §5.4, §6.4). go-ora concatenates the value verbatim into
// "ALTER SESSION SET <Key>=<Value>", so string literals must carry their own
// quotes (e.g. Value: "'UTC'").
type SessionParam struct {
	Key   string
	Value string
}

// SessionParams builds the session settings for a datasource. F1 pins the
// session TIME_ZONE: go-ora otherwise sets it from the plugin host's local
// offset at connect (auth_object.go), which would leak the host locale into
// every TIMESTAMP result (report §5.4 "pin UTC"). NLS params arrive in F2.
func SessionParams(timeZone string) []SessionParam {
	if strings.TrimSpace(timeZone) == "" {
		timeZone = "UTC"
	}
	return []SessionParam{
		{Key: "TIME_ZONE", Value: "'" + timeZone + "'"},
	}
}

// ApplySession registers the session params on db. Each is executed on the
// current connection and stored on the driver so every future pooled
// connection gets the same ALTER SESSION (go-ora driver.init loop). db must
// have been created via go_ora.NewConnector so the settings land on this
// datasource's private driver, not the shared package singleton.
func ApplySession(db *sql.DB, params []SessionParam) error {
	for _, p := range params {
		if err := go_ora.AddSessionParam(db, p.Key, p.Value); err != nil {
			return fmt.Errorf("applying session parameter %s: %w", p.Key, err)
		}
	}
	return nil
}
