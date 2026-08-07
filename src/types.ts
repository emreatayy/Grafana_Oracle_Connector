import { DataSourceJsonData } from '@grafana/data';
import { DataQuery } from '@grafana/schema';

// Query sent to the backend (report §4.3). F1 wires SQL only; mode/format and
// parameters arrive in later phases.
export interface MyQuery extends DataQuery {
  sql: string;
}

export const DEFAULT_QUERY: Partial<MyQuery> = {
  sql: 'SELECT 1 AS value FROM DUAL',
};

/**
 * Options configured per datasource instance (report §4.3). The o_* fields are
 * the Oracle connection; the rest are pool/timeout/cardinality limits.
 */
export interface MyDataSourceOptions extends DataSourceJsonData {
  o_connStr?: string;
  o_hostname?: string;
  o_port?: number;
  o_service?: string;
  o_sid?: string;
  o_user?: string;
  maxOpenConns?: number;
  maxIdleConns?: number;
  connMaxLifetimeSec?: number;
  queryTimeoutSec?: number;
  commandTimeoutSec?: number;
  maxRows?: number;
  maxSeries?: number;
  sessionTimeZone?: string;
  defaultMode?: string;
  defaultFormat?: string;
}

/** Credentials stored securely in the backend; never sent to the browser. */
export interface MySecureJsonData {
  o_password?: string;
}
