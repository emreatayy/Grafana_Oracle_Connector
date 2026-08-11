package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// TestHealthResult_Shapes covers the four report §13 states the Test screen can
// show: oracle up/down × prometheus not-configured / configured ok / configured
// down. The message must be parseable JSON carrying oracle.ok and
// prometheus.ok booleans, with the readable error in the failing subsystem.
func TestHealthResult_Shapes(t *testing.T) {
	t.Run("oracle ok, prometheus not configured", func(t *testing.T) {
		res := healthResult(true, "  Oracle Database 21c  ", "", nil)
		if res.Status != backend.HealthStatusOk {
			t.Fatalf("status = %s, want OK", res.Status)
		}
		msg := parseHealth(t, res.Message)
		if !msg.Oracle.OK || strings.TrimSpace(msg.Oracle.Version) != "Oracle Database 21c" {
			t.Errorf("oracle = %+v, want ok with trimmed version", msg.Oracle)
		}
		if msg.Prometheus.Enabled || !msg.Prometheus.OK {
			t.Errorf("prometheus = %+v, want enabled=false ok=true (green without config)", msg.Prometheus)
		}
	})

	t.Run("oracle down: readable error, status Error", func(t *testing.T) {
		res := healthResult(false, "", "ORA-01017: invalid username or password", nil)
		if res.Status != backend.HealthStatusError {
			t.Fatalf("status = %s, want Error", res.Status)
		}
		msg := parseHealth(t, res.Message)
		if msg.Oracle.OK || msg.Oracle.Error != "ORA-01017: invalid username or password" {
			t.Errorf("oracle = %+v, want ok=false with mapped error", msg.Oracle)
		}
		// The not-configured proxy must not flip an Oracle failure into OK.
		if msg.Prometheus.Enabled || !msg.Prometheus.OK {
			t.Errorf("prometheus = %+v, want enabled=false ok=true", msg.Prometheus)
		}
	})

	t.Run("oracle ok, prometheus enabled and reachable", func(t *testing.T) {
		prom := &prometheusHealth{Enabled: true, OK: true, Version: "Prometheus 3.5.0"}
		res := healthResult(true, "Oracle Database 21c", "", prom)
		if res.Status != backend.HealthStatusOk {
			t.Fatalf("status = %s, want OK", res.Status)
		}
		msg := parseHealth(t, res.Message)
		if !msg.Prometheus.Enabled || !msg.Prometheus.OK || msg.Prometheus.Version != "Prometheus 3.5.0" {
			t.Errorf("prometheus = %+v, want enabled ok with version", msg.Prometheus)
		}
	})

	t.Run("oracle ok, prometheus enabled but down", func(t *testing.T) {
		prom := &prometheusHealth{Enabled: true, OK: false, Error: "network error: dial tcp ..."}
		res := healthResult(true, "Oracle Database 21c", "", prom)
		if res.Status != backend.HealthStatusError {
			t.Fatalf("status = %s, want Error (broken proxy)", res.Status)
		}
		msg := parseHealth(t, res.Message)
		if !msg.Oracle.OK {
			t.Errorf("oracle = %+v, want still ok", msg.Oracle)
		}
		if !msg.Prometheus.Enabled || msg.Prometheus.OK || !strings.Contains(msg.Prometheus.Error, "network error") {
			t.Errorf("prometheus = %+v, want enabled down with network error", msg.Prometheus)
		}
	})
}

func TestHealthResult_ErrorIsOmittedWhenOK(t *testing.T) {
	res := healthResult(true, "Oracle", "", nil)
	if strings.Contains(res.Message, `"error"`) {
		t.Errorf("message %q has an error field on a green health", res.Message)
	}
}

func parseHealth(t *testing.T, msg string) healthMessage {
	t.Helper()
	var parsed healthMessage
	if err := json.Unmarshal([]byte(msg), &parsed); err != nil {
		t.Fatalf("health message is not JSON: %v\n%s", err, msg)
	}
	return parsed
}
