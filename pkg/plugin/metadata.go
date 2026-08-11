package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// Schema metadata for the query editor (report §14.3). Sources: ALL_TAB_COMMENTS
// (tables the datasource account can access) and ALL_TAB_COLUMNS joined with
// ALL_COL_COMMENTS for column names/types/descriptions.
//
// Two properties matter:
//   - The table list is bounded and both lists are cached with a short TTL, so
//     editor keystrokes and time-column lookups never hammer the database.
//   - Any metadata failure (permission missing, database down) is silent: the
//     resource returns 200 with empty arrays. The editor falls back to Auto +
//     free text instead of surfacing an error (report §14.3).
//
// Note on ALL_TAB_PRIVS: the report lists it alongside ALL_TAB_COMMENTS for
// "tables the user can access", but ALL_TAB_COMMENTS already restricts to the
// accessible set and covers role-granted access that ALL_TAB_PRIVS (grants to
// the user directly) would miss — so the privs view is deliberately not joined.

const (
	// metadataTTL bounds how long a metadata result is reused.
	metadataTTL = 5 * time.Minute
	// metadataMaxTables caps the table list (§14.3: "the table list is bounded").
	metadataMaxTables = 200
	// metadataMaxTableName guards the table/prefix params (length sanity only —
	// both are bound parameters, never interpolated).
	metadataMaxTableName = 128
)

// MetadataColumn is one column of a table as served to the editor.
type MetadataColumn struct {
	Name     string `json:"name"`
	DataType string `json:"dataType"`
	Comment  string `json:"comment,omitempty"`
}

type metadataColumnCache struct {
	at   time.Time
	cols []MetadataColumn
}

// metadataResource serves the metadata/ namespace under CallResource
// (report §14.3). It is read-only, credential-free and never touches the query
// pipeline — the same call the editor's completion and time-column dropdown use.
func (d *Datasource) metadataResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	params := url.Values{}
	if u, err := url.Parse(req.URL); err == nil {
		params = u.Query()
	}

	switch rel := strings.TrimPrefix(req.Path, "metadata/"); rel {
	case "tables":
		ctx, cancel := context.WithTimeout(ctx, resourceTimeout)
		defer cancel()
		tables, err := d.metadataTables(ctx, params.Get("prefix"))
		if err != nil {
			return d.silentMetadata(sender, map[string]any{"tables": []string{}})
		}
		return d.silentMetadata(sender, map[string]any{"tables": tables})

	case "columns":
		table := strings.TrimSpace(params.Get("table"))
		if table == "" {
			return sender.Send(resourceError(http.StatusBadRequest, "metadata/columns requires a table parameter"))
		}
		if len(table) > metadataMaxTableName {
			return sender.Send(resourceError(http.StatusBadRequest, "table parameter too long"))
		}
		ctx, cancel := context.WithTimeout(ctx, resourceTimeout)
		defer cancel()
		cols, err := d.metadataColumns(ctx, table)
		if err != nil {
			return d.silentMetadata(sender, map[string]any{"table": table, "columns": []MetadataColumn{}})
		}
		return d.silentMetadata(sender, map[string]any{"table": table, "columns": cols})

	default:
		return sender.Send(resourceError(http.StatusNotFound, "unknown resource"))
	}
}

// metadataTables lists the accessible tables, optionally narrowed by prefix.
// The full (unprefixed) result is cached for metadataTTL; prefix narrowing is
// applied by the database each time (a prefix request is a fresh bounded query).
// Errors are returned to the caller, which turns them into silent-empty JSON.
func (d *Datasource) metadataTables(ctx context.Context, prefix string) ([]string, error) {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))

	d.metaMu.Lock()
	if prefix == "" && d.metaTables != nil && time.Since(d.metaTablesAt) < metadataTTL {
		tables := d.metaTables
		d.metaMu.Unlock()
		return tables, nil
	}
	d.metaMu.Unlock()

	if err := d.ensureReady(ctx); err != nil {
		return nil, err
	}

	// ROWNUM is applied in an outer query so the bound applies to the ORDERED
	// result (Oracle applies ROWNUM before ORDER BY otherwise). Placeholders are
	// go-ora's numbered :N style (a literal `?` is rejected with ORA-00911), so
	// the numbering depends on whether the prefix narrowed the inner SELECT.
	q := `SELECT table_name FROM (
		SELECT table_name FROM all_tab_comments
		WHERE table_type IN ('TABLE','VIEW')`
	args := []any{}
	if prefix != "" {
		q += ` AND table_name LIKE :1`
		args = append(args, prefix+"%")
		q += ` ORDER BY table_name) WHERE ROWNUM <= :2`
	} else {
		q += ` ORDER BY table_name) WHERE ROWNUM <= :1`
	}
	args = append(args, metadataMaxTables)

	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	tables := make([]string, 0, metadataMaxTables)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if prefix == "" {
		d.metaMu.Lock()
		d.metaTables = tables
		d.metaTablesAt = time.Now()
		d.metaMu.Unlock()
	}
	return tables, nil
}

// metadataColumns lists the columns of one table. `table` may be schema
// qualified (OWNER.TABLE); unqualified names resolve to the login schema (USER).
// Results are cached per owner.table for metadataTTL.
func (d *Datasource) metadataColumns(ctx context.Context, table string) ([]MetadataColumn, error) {
	owner, name := "", strings.ToUpper(strings.TrimSpace(table))
	if i := strings.IndexByte(name, '.'); i >= 0 {
		owner, name = name[:i], name[i+1:]
	}
	if name == "" {
		return []MetadataColumn{}, nil
	}

	key := owner + "." + name
	d.metaMu.Lock()
	if entry, ok := d.metaCols[key]; ok && time.Since(entry.at) < metadataTTL {
		cols := entry.cols
		d.metaMu.Unlock()
		return cols, nil
	}
	d.metaMu.Unlock()

	if err := d.ensureReady(ctx); err != nil {
		return nil, err
	}

	q := `SELECT c.column_name, c.data_type, cc.comments
	FROM all_tab_columns c
	LEFT JOIN all_col_comments cc
		ON cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name
	WHERE c.table_name = :1 AND c.owner = COALESCE(NULLIF(:2, ''), USER)
	ORDER BY c.column_id`
	rows, err := d.db.QueryContext(ctx, q, name, owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	cols := make([]MetadataColumn, 0)
	for rows.Next() {
		var c MetadataColumn
		var comment sql.NullString // ALL_COL_COMMENTS.comments is NULLable
		if err := rows.Scan(&c.Name, &c.DataType, &comment); err != nil {
			return nil, err
		}
		c.Comment = comment.String
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	d.metaMu.Lock()
	if d.metaCols == nil {
		d.metaCols = map[string]metadataColumnCache{}
	}
	d.metaCols[key] = metadataColumnCache{at: time.Now(), cols: cols}
	d.metaMu.Unlock()
	return cols, nil
}

// silentMetadata sends a 200 JSON body. Used for the metadata endpoint so a
// missing ALL_TAB_* permission or a down database degrades to empty suggestions
// rather than an error the editor would have to explain (report §14.3).
func (d *Datasource) silentMetadata(sender backend.CallResourceResponseSender, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return sender.Send(resourceError(http.StatusInternalServerError, "internal error"))
	}
	return sender.Send(&backend.CallResourceResponse{
		Status:  http.StatusOK,
		Headers: map[string][]string{"Content-Type": {"application/json"}},
		Body:    body,
	})
}
