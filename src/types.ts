import { DataSourceJsonData } from '@grafana/data';
import { DataQuery } from '@grafana/schema';

export type QueryFormat = 'auto' | 'table' | 'time_series';
export type LargeNumberMode = 'string' | 'float';
// Per-query editor mode (report §4.3, §14.1). 'prometheus' is a placeholder
// until F6/F7; the backend rejects it with a clear error (F4).
export type QueryMode = 'sql' | 'prometheus';

// Query sent to the backend (report §4.3). F2 wires SQL, format, an optional
// explicit time column and a per-query large-number override. F3 adds
// scopedVars, the dashboard variables attached at query time by
// applyTemplateVariables (alert queries never carry them — §8.3). F4 adds mode
// (the editor tab selection, stored per query) and promql (inert until F6).
export interface MyQuery extends DataQuery {
  sql: string;
  mode?: QueryMode;
  format?: QueryFormat;
  timeColumn?: string;
  largeNumberMode?: LargeNumberMode;
  scopedVars?: Record<string, unknown>;
  promql?: string;
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
  defaultMode?: QueryMode;
  defaultFormat?: string;
  // ADR-8: big NUMBER (scale=0, precision>15) policy. Empty = per-format
  // default (string in table, float in time_series).
  largeNumberMode?: LargeNumberMode;
  // Variable names that may expand unquoted in SQL (report §8.2). A hint, not a
  // trust grant — the backend still verifies a strict numeric literal.
  numericVariables?: string[];
}

/** Credentials stored securely in the backend; never sent to the browser. */
export interface MySecureJsonData {
  o_password?: string;
}
