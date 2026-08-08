import { DataSourceJsonData } from '@grafana/data';
import { DataQuery } from '@grafana/schema';

export type QueryFormat = 'auto' | 'table' | 'time_series';
export type LargeNumberMode = 'string' | 'float';

// Query sent to the backend (report §4.3). F2 wires SQL, format, an optional
// explicit time column and a per-query large-number override; mode and the
// Prometheus fields arrive in later phases.
export interface MyQuery extends DataQuery {
  sql: string;
  format?: QueryFormat;
  timeColumn?: string;
  largeNumberMode?: LargeNumberMode;
}

export const DEFAULT_QUERY: Partial<MyQuery> = {
  sql: 'SELECT 1 AS value FROM DUAL',
  format: 'auto',
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
  // ADR-8: big NUMBER (scale=0, precision>15) policy. Empty = per-format
  // default (string in table, float in time_series).
  largeNumberMode?: LargeNumberMode;
}

/** Credentials stored securely in the backend; never sent to the browser. */
export interface MySecureJsonData {
  o_password?: string;
}
