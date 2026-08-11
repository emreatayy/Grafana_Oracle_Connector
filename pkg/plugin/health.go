package plugin

import (
	"encoding/json"
	"strings"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// healthMessage is the structured JSON detail the datasource "Test" screen
// shows (report §13 "Message"). It carries oracle.ok / prometheus.ok booleans
// plus the version banner and the readable error per subsystem. Grafana
// renders the message verbatim, and the stable booleans let programmatic
// consumers read the health state without parsing prose.
type healthMessage struct {
	Oracle     oracleHealth     `json:"oracle"`
	Prometheus prometheusHealth `json:"prometheus"`
}

type oracleHealth struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

type prometheusHealth struct {
	Enabled bool   `json:"enabled"`
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// healthResult assembles the Test-screen CheckHealthResult. Error strings must
// already be safe (readable mapped message with credentials redacted) — the
// builder never formats a raw error itself. A nil prom means the proxy is not
// configured, which is reported as enabled=false/ok=true so health stays green
// without a Prometheus config (report §13, F10 DoD). Overall status is Error
// when either subsystem is down.
func healthResult(oracleOK bool, oracleVersion, oracleErr string, prom *prometheusHealth) *backend.CheckHealthResult {
	msg := healthMessage{
		Oracle: oracleHealth{OK: oracleOK, Version: strings.TrimSpace(oracleVersion), Error: oracleErr},
		Prometheus: prometheusHealth{ // not configured ⇒ green, never a hard fail
			Enabled: false,
			OK:      true,
		},
	}
	if prom != nil {
		msg.Prometheus = *prom
	}

	status := backend.HealthStatusOk
	if !oracleOK || (prom != nil && !prom.OK) {
		status = backend.HealthStatusError
	}

	body, err := json.Marshal(msg)
	if err != nil {
		// Unreachable for these structs; degrade to a readable one-liner.
		text := "health check failed"
		if oracleErr != "" {
			text = oracleErr
		}
		return &backend.CheckHealthResult{Status: status, Message: text}
	}
	return &backend.CheckHealthResult{Status: status, Message: string(body)}
}
