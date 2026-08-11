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

    if (Object.keys(vars).length === 0) {
      return query;
    }
    return { ...query, scopedVars: vars };
  }
}
