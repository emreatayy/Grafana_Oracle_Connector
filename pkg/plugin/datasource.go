package plugin

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	go_ora "github.com/sijms/go-ora/v2"

	"github.com/emreatayy/oracle-datasource/pkg/models"
	"github.com/emreatayy/oracle-datasource/pkg/oracle"
)

// healthTimeout bounds the whole health-check connect (report §13).
const healthTimeout = 5 * time.Second

// Make sure Datasource implements the required interfaces.
var (
	_ backend.QueryDataHandler      = (*Datasource)(nil)
	_ backend.CheckHealthHandler    = (*Datasource)(nil)
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

	return &backend.CheckHealthResult{
		Status:  backend.HealthStatusOk,
		Message: strings.TrimSpace(banner),
	}, nil
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
