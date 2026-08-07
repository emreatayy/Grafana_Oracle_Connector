import { DataSourceInstanceSettings, CoreApp } from '@grafana/data';
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
}
