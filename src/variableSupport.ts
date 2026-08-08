import { DataSourceVariableSupport } from '@grafana/data';

import type { DataSource } from './datasource';
import type { MyDataSourceOptions, MyQuery } from './types';

/**
 * Query-variable support (report §8.5). Extending DataSourceVariableSupport
 * tells Grafana to reuse this plugin's own QueryEditor and query path for query
 * variables: the variable editor renders the F4 SQL editor, the saved
 * variable.query is a full MyQuery (with `sql`), and the DatasourceQueryRunner
 * sends it through the standard backend query path. Without this class the
 * variable editor renders no Query field at all (getVariableQueryEditor returns
 * null), so "Preview of values" stays empty.
 */
export class OracleVariableSupport extends DataSourceVariableSupport<DataSource, MyQuery, MyDataSourceOptions> {}
