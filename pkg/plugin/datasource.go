package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	go_ora "github.com/sijms/go-ora/v2"

	"github.com/emreatayy/oracle-datasource/pkg/models"
	"github.com/emreatayy/oracle-datasource/pkg/oracle"
	"github.com/emreatayy/oracle-datasource/pkg/prom"
)

// healthTimeout bounds the whole health-check connect (report §13).
const healthTimeout = 5 * time.Second

// resourceTimeout bounds a single metric-browser/variable resource call.
const resourceTimeout = 10 * time.Second

// Make sure Datasource implements the required interfaces.
var (
	_ backend.QueryDataHandler      = (*Datasource)(nil)
	_ backend.CheckHealthHandler    = (*Datasource)(nil)
	_ backend.CallResourceHandler   = (*Datasource)(nil)
	_ instancemgmt.InstanceDisposer = (*Datasource)(nil)
)

// NewDatasource creates a datasource instance. It parses settings and prepares
// the connection pool but does not connect eagerly — a down database must not
// break instance construction; CheckHealth surfaces the error.
func NewDatasource(_ context.Context, s backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
	settings, err := models.LoadPluginSettings(s)
	if err != nil {
		return nil, fmt.Errorf("could not load datasource settings: %w", err)
	}
	dsn, err := oracle.BuildDSN(settings)
	if err != nil {
		return nil, err
	}

	// Use go_ora.NewConnector (a fresh driver per connector) instead of the
	// registered "oracle" driver: the default driver is a package-level
	// singleton whose session-param map would be shared across datasource
	// instances. NewConnector allocates a private OracleDriver, so session
	// parameters stay isolated per datasource (report §5.4).
	db := sql.OpenDB(go_ora.NewConnector(dsn))
	db.SetMaxOpenConns(settings.MaxOpenConns)
	db.SetMaxIdleConns(settings.MaxIdleConns)
	db.SetConnMaxLifetime(time.Duration(settings.ConnMaxLifetimeSec) * time.Second)

	return &Datasource{
		settings: settings,
		db:       db,
		session:  oracle.SessionParams(settings.SessionTimeZone),
	}, nil
}

// Datasource is the Oracle datasource instance.
type Datasource struct {
	settings *models.PluginSettings
	db       *sql.DB
	session  []oracle.SessionParam

	// mu guards ready and db; ensureReady holds it through the first connect
	// so concurrent queries cannot race the session setup.
	mu    sync.Mutex
	ready bool
}

// Dispose closes the connection pool. The SDK calls it whenever the datasource
// configuration changes, immediately before a new instance is created with the
// new settings (report §18 F1 DoD).
func (d *Datasource) Dispose() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db != nil {
		_ = d.db.Close()
		d.db = nil
		d.ready = false
	}
}

// ensureReady pings the database and applies session parameters once. It is
// retry-safe: a failed attempt (database briefly down) is retried on the next
// call because ready is only set after success.
func (d *Datasource) ensureReady(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db == nil {
		return fmt.Errorf("datasource has been disposed")
	}
	if d.ready {
		return nil
	}
	if err := d.db.PingContext(ctx); err != nil {
		return err
	}
	if err := oracle.ApplySession(d.db, d.session); err != nil {
		return err
	}
	d.ready = true
	return nil
}

// QueryData executes each query and returns one response per RefID.
func (d *Datasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	response := backend.NewQueryDataResponse()
	for _, q := range req.Queries {
		response.Responses[q.RefID] = d.query(ctx, q)
	}
	return response, nil
}

// CheckHealth implements the datasource "Test" button: connect with a 5s bound
// and report the Oracle version banner (report §5.2, §13).
func (d *Datasource) CheckHealth(_ context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()

	if err := d.ensureReady(ctx); err != nil {
		return d.healthError(err), nil
	}

	var banner string
	if err := d.db.QueryRowContext(ctx, "SELECT BANNER FROM V$VERSION WHERE ROWNUM = 1").Scan(&banner); err != nil {
		return d.healthError(err), nil
	}

	msg := strings.TrimSpace(banner)
	// F6: when the Prometheus proxy is enabled, also probe it and report the
	// version banner. A broken proxy is surfaced as health=error (report §9.2).
	if d.settings.PromEnabled {
		promMsg, promErr := d.promHealth(ctx)
		if promErr != nil {
			return &backend.CheckHealthResult{
				Status:  backend.HealthStatusError,
				Message: fmt.Sprintf("Oracle OK: %s — Prometheus: %s", msg, d.safeMessage(promErr)),
			}, nil
		}
		msg = fmt.Sprintf("%s — Prometheus %s", msg, promMsg)
	}

	return &backend.CheckHealthResult{
		Status:  backend.HealthStatusOk,
		Message: msg,
	}, nil
}

// promClient builds a Prometheus client with the datasource's credentials and
// transport settings (report §9.1). limit is the maxSeries-bound `limit` param
// for query endpoints; resource calls pass 0 (label/series endpoints do not
// accept it). A shared construction keeps auth/TLS identical everywhere.
func (d *Datasource) promClient(limit int) (*prom.Client, error) {
	user, password, token := d.settings.PromCreds()
	return prom.NewClient(d.settings.PromURL, prom.Options{
		AuthType:    d.settings.PromAuthType,
		User:        user,
		Password:    password,
		Token:       token,
		TLSInsecure: d.settings.PromTlsSkipVerify,
		Timeout:     time.Duration(d.settings.QueryTimeoutSec) * time.Second,
		Limit:       limit,
	})
}

// promHealth probes the configured Prometheus and returns its version banner.
func (d *Datasource) promHealth(ctx context.Context) (string, error) {
	client, err := d.promClient(0)
	if err != nil {
		return "", err
	}
	version, err := client.BuildInfo(ctx)
	if err != nil {
		return "", err
	}
	if version == "" {
		version = "reachable"
	}
	return version, nil
}

// CallResource serves read-only Prometheus API v1 resources to the frontend
// (F7 metric browser, F8 label_values variables — report §9.2). The browser
// must not call Prometheus directly: credentials live server-side in
// secureJsonData and a browser request would be CORS-blocked, so every browse
// call is proxied through the plugin with the same auth/TLS as queries. Paths
// are namespaced under "prometheus/" and individually whitelisted; unknown
// paths (e.g. the F15 "command" resource) get a clean 404.
func (d *Datasource) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	const prefix = "prometheus/"
	if !strings.HasPrefix(req.Path, prefix) {
		return sender.Send(resourceError(http.StatusNotFound, "unknown resource"))
	}
	if !d.settings.PromEnabled {
		return sender.Send(resourceError(http.StatusBadRequest,
			"Prometheus proxy is disabled: enable it in the datasource settings"))
	}
	client, err := d.promClient(0)
	if err != nil {
		return sender.Send(resourceError(http.StatusBadRequest, err.Error()))
	}

	var apiPath string
	var params url.Values
	switch rel := strings.TrimPrefix(req.Path, prefix); {
	case rel == "labels":
		apiPath = "/api/v1/labels"
	case strings.HasPrefix(rel, "label/") && strings.HasSuffix(rel, "/values"):
		name := strings.TrimSuffix(strings.TrimPrefix(rel, "label/"), "/values")
		if !validPromLabelName(name) {
			return sender.Send(resourceError(http.StatusBadRequest, "invalid label name"))
		}
		apiPath = "/api/v1/label/" + name + "/values"
	case rel == "series":
		apiPath = "/api/v1/series"
		params = seriesParams(req.URL)
	default:
		return sender.Send(resourceError(http.StatusNotFound, "unknown prometheus resource"))
	}

	ctx, cancel := context.WithTimeout(ctx, resourceTimeout)
	defer cancel()
	body, err := client.RawJSON(ctx, apiPath, params)
	if err != nil {
		return sender.Send(resourceError(http.StatusBadGateway, d.safeMessage(err)))
	}
	return sender.Send(&backend.CallResourceResponse{
		Status:  http.StatusOK,
		Headers: map[string][]string{"Content-Type": {"application/json"}},
		Body:    body,
	})
}

// resourceError builds a JSON error body for a resource call. The message is
// already redacted by the caller.
func resourceError(status int, msg string) *backend.CallResourceResponse {
	body, _ := json.Marshal(map[string]string{"error": msg})
	return &backend.CallResourceResponse{
		Status:  status,
		Headers: map[string][]string{"Content-Type": {"application/json"}},
		Body:    body,
	}
}

// validPromLabelName admits only Prometheus label-name characters
// ([a-zA-Z0-9_:]) — anything else (path traversal, URL control characters) is
// rejected before it can reach the proxied path.
func validPromLabelName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == ':':
		default:
			return false
		}
	}
	return true
}

// seriesParams forwards only the match[] selector to /api/v1/series; anything
// else in the request query is dropped.
func seriesParams(rawURL string) url.Values {
	params := url.Values{}
	if rawURL == "" {
		return params
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return params
	}
	if m := u.Query()["match[]"]; len(m) > 0 {
		params["match[]"] = m
	}
	return params
}

// healthError formats a health failure without leaking credentials.
func (d *Datasource) healthError(err error) *backend.CheckHealthResult {
	return &backend.CheckHealthResult{
		Status:  backend.HealthStatusError,
		Message: d.safeMessage(err),
	}
}

// safeMessage formats an error and strips any password/credential content.
func (d *Datasource) safeMessage(err error) string {
	msg := oracle.FormatError(err)
	if d.settings != nil {
		msg = oracle.Redact(msg, d.settings.Password())
	}
	return msg
}
