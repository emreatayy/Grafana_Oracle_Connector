import { AdHocVariableFilter, DataSourceInstanceSettings, CoreApp, ScopedVars } from '@grafana/data';
import { DataSourceWithBackend, getTemplateSrv } from '@grafana/runtime';

import { MyQuery, MyDataSourceOptions, DEFAULT_QUERY } from './types';
import { OracleVariableSupport } from './variableSupport';

// Shape of a Prometheus API v1 envelope returned by the plugin resource proxy
// (report §9.2); `data` is typed per endpoint.
export interface PromAPIResponse<T> {
  status: string;
  data: T;
  error?: string;
}

// Schema metadata served by the backend resource (report §14.3). A column's
// dataType is the Oracle type (DATE/TIMESTAMP/NUMBER/…) used to pick date/time
// time-column candidates; comment is the optional ALL_COL_COMMENTS text.
export interface MetadataColumn {
  name: string;
  dataType: string;
  comment?: string;
}

interface MetadataTablesResponse {
  tables: string[];
}

interface MetadataColumnsResponse {
  table?: string;
  columns: MetadataColumn[];
}

// F15 Run-command result (report §12.1): a structured {ok, message} rendered
// inline under the button. rowsAffected is present when the driver reports one
// (PL/SQL blocks typically do not).
export interface CommandResponse {
  ok: boolean;
  message: string;
  rowsAffected?: number;
}

export class DataSource extends DataSourceWithBackend<MyQuery, MyDataSourceOptions> {
  constructor(instanceSettings: DataSourceInstanceSettings<MyDataSourceOptions>) {
    super(instanceSettings);
    // Query variables (report §8.5): without this, Grafana's variable editor
    // has no Query field and the preview never runs. With it, the variable
    // editor reuses the F4 SQL editor and the standard backend query path.
    this.variables = new OracleVariableSupport();
  }

  // Prometheus metric-browser resources (F7). Every browse call is proxied
  // through the plugin's CallResource handler — the browser must never talk to
  // Prometheus directly (credentials live server-side, CORS would block it).

  // All metric names (GET /api/v1/label/__name__/values).
  async getPromMetricNames(): Promise<string[]> {
    const resp = await this.getResource<PromAPIResponse<string[]>>('prometheus/label/__name__/values');
    return resp?.data ?? [];
  }

  // Values for a label (F8 label_values variables). Label names are server-side
  // validated to [a-zA-Z0-9_:].
  async getPromLabelValues(label: string): Promise<string[]> {
    const resp = await this.getResource<PromAPIResponse<string[]>>(
      `prometheus/label/${encodeURIComponent(label)}/values`
    );
    return resp?.data ?? [];
  }

  // Series matching a selector — label names/values for the metric browser.
  async getPromSeries(match: string): Promise<Array<Record<string, string>>> {
    const resp = await this.getResource<PromAPIResponse<Array<Record<string, string>>>>('prometheus/series', {
      'match[]': match,
    });
    return resp?.data ?? [];
  }

  // --- Schema metadata (F9, report §14.3) --------------------------------
  // Both are served by the backend resource handler from ALL_TAB_COMMENTS /
  // ALL_TAB_COLUMNS with a 5-minute cache. The backend is silent-empty on any
  // metadata error (permission missing, database down) — the UI falls back to
  // Auto + free text instead of surfacing an error.

  // Table names the datasource account can access (bounded server-side; the
  // optional prefix narrows the list for editor completion).
  async getMetadataTables(prefix?: string): Promise<string[]> {
    const resp = await this.getResource<MetadataTablesResponse>('metadata/tables', prefix ? { prefix } : {});
    return resp?.tables ?? [];
  }

  // Columns of one table; `table` may be schema-qualified (OWNER.TABLE). Used
  // by the editor completion and the time-column dropdown.
  async getMetadataColumns(table: string): Promise<MetadataColumn[]> {
    const resp = await this.getResource<MetadataColumnsResponse>('metadata/columns', { table });
    return resp?.columns ?? [];
  }

  // --- Command execution (F15, report §12) --------------------------------
  // One-shot operational actions (scheduler jobs, procedure calls). The backend
  // resource handler always requires confirm=true (report §12.3) and only runs
  // allowlisted commands — the query pipeline never carries them.
  async runCommand(sql: string, confirm: boolean): Promise<CommandResponse> {
    return this.postResource<CommandResponse>('command', { sql, confirm });
  }

  getDefaultQuery(_: CoreApp): Partial<MyQuery> {
    return DEFAULT_QUERY;
  }

  // Interpolation happens on the backend (ADR-2): the frontend sends raw SQL
  // and the plugin expands macros/variables server-side (report §8).
  // A query is runnable iff ITS MODE's expression is non-empty — checking only
  // `sql` dropped every Prometheus-mode query (F7 dashboard panels, F8
  // label_values variables) in Grafana's runRequest filter (F8).
  filterQuery(query: MyQuery): boolean {
    if (query.mode === 'prometheus') {
      return !!query.promql && query.promql.trim().length > 0;
    }
    return !!query.sql && query.sql.trim().length > 0;
  }

  // Attach the dashboard's variables to the query so the backend can expand
  // ${var} (ADR-2, report §8.1). Two sources are merged:
  //   1. scopedVars already on the request (variable editor, direct
  //      /api/ds/query, alert-rule evaluation), and
  //   2. the current values of all dashboard variables from the global template
  //      service — Grafana 13 (scenes) puts only __-internal vars in a panel
  //      request's scopedVars, so without this the backend would never resolve
  //      ${var} in a dashboard (F8).
  // Alert rules skip this hook (ngalert evaluates server-side), so alert
  // queries resolve only $__ macros + TimeRange (§8.3). The saved query is
  // never mutated.
  applyTemplateVariables(query: MyQuery, scopedVars: ScopedVars, _filters?: AdHocVariableFilter[]): MyQuery {
    // F9: a dashboard annotation target calls this hook with a `__annotation`
    // scopedVar (Grafana 13 executeAnnotationQuery). Mark the query so the
    // backend shapes the frame as an annotation (report §11). The marker must
    // survive the early return below when there are no other variables.
    const annotation = Boolean(scopedVars && scopedVars['__annotation']);
    const vars: Record<string, unknown> = {};

    const add = (name: string, value: unknown) => {
      if (!name || name.startsWith('__') || name === 'org' || name === 'user') {
        return;
      }
      if (value === undefined || value === null) {
        return;
      }
      if (Array.isArray(value)) {
        vars[name] = value.map((el) => String(el));
      } else if (typeof value === 'object') {
        // Object-valued values (system vars like $__dashboard, ad-hoc filter
        // maps) are dropped — the backend must not receive map[string]any it
        // cannot expand.
        return;
      } else {
        // Every value is stringified so the backend receives plain strings or
        // arrays of strings (§8.1).
        vars[name] = String(value);
      }
    };

    for (const [name, v] of Object.entries(scopedVars)) {
      if (v && v.value !== undefined && v.value !== null) {
        add(name, v.value);
      }
    }

    for (const v of getTemplateSrv().getVariables()) {
      const current = (v as { current?: { value?: unknown } }).current;
      if (current && current.value !== undefined) {
        add(v.name, current.value);
      }
    }

    const next = Object.keys(vars).length === 0 ? query : { ...query, scopedVars: vars };
    return annotation ? { ...next, annotation: true } : next;
  }
}
