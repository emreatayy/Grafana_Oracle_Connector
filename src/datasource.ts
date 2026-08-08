import { AdHocVariableFilter, DataSourceInstanceSettings, CoreApp, ScopedVars } from '@grafana/data';
import { DataSourceWithBackend } from '@grafana/runtime';

import { MyQuery, MyDataSourceOptions, DEFAULT_QUERY } from './types';

export class DataSource extends DataSourceWithBackend<MyQuery, MyDataSourceOptions> {
  constructor(instanceSettings: DataSourceInstanceSettings<MyDataSourceOptions>) {
    super(instanceSettings);
  }

  getDefaultQuery(_: CoreApp): Partial<MyQuery> {
    return DEFAULT_QUERY;
  }

  // Interpolation happens on the backend (ADR-2): the frontend sends raw SQL
  // and the plugin expands macros/variables server-side (report §8).
  filterQuery(query: MyQuery): boolean {
    return !!query.sql && query.sql.trim().length > 0;
  }

  // Attach the dashboard's scoped variables to the query so the backend can
  // expand ${var} (ADR-2, report §8.1). Alert rules skip this hook (ngalert
  // evaluates server-side without the frontend), so alert queries resolve only
  // $__ macros + TimeRange (§8.3). The saved query is never mutated.
  applyTemplateVariables(query: MyQuery, scopedVars: ScopedVars, _filters?: AdHocVariableFilter[]): MyQuery {
    const vars = Object.entries(scopedVars)
      .filter(([name]) => !name.startsWith('__') && name !== 'org' && name !== 'user')
      .reduce<Record<string, unknown>>((acc, [name, v]) => {
        if (!v || v.value === undefined || v.value === null) {
          return acc;
        }
        const value = v.value;
        if (Array.isArray(value)) {
          acc[name] = value.map((el) => String(el));
        } else if (typeof value === 'object') {
          // Ad-hoc/object-valued variables are dropped — the backend must not
          // receive map[string]any values it cannot expand.
          return acc;
        } else {
          // Every value is stringified so the backend receives plain
          // strings/arrays of strings (§8.1).
          acc[name] = String(value);
        }
        return acc;
      }, {});
    if (Object.keys(vars).length === 0) {
      return query;
    }
    return { ...query, scopedVars: vars };
  }
}
