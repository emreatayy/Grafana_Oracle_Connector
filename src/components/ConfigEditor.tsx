import React, { ChangeEvent } from 'react';
import { FieldSet, InlineField, Input, SecretInput, Select, Switch } from '@grafana/ui';
import { DataSourcePluginOptionsEditorProps, SelectableValue } from '@grafana/data';
import {
  LargeNumberMode,
  MyDataSourceOptions,
  MySecureJsonData,
  PromAuthType,
  QueryMode,
} from '../types';

interface Props extends DataSourcePluginOptionsEditorProps<MyDataSourceOptions, MySecureJsonData> {}

const LARGE_NUMBER_OPTIONS: Array<SelectableValue<LargeNumberMode | ''>> = [
  {
    label: 'Auto',
    value: '',
    description: 'Preserve precision (string) in table, float in time series (ADR-8 default)',
  },
  { label: 'String (preserve precision)', value: 'string' },
  { label: 'Float (may lose digits)', value: 'float' },
];

// Default query-editor mode (report §14.1).
const MODE_OPTIONS: Array<SelectableValue<QueryMode>> = [
  { label: 'SQL (Oracle)', value: 'sql' },
  { label: 'Prometheus (PromQL)', value: 'prometheus' },
];

// Prometheus proxy auth (report §9.1).
const PROM_AUTH_OPTIONS: Array<SelectableValue<PromAuthType>> = [
  { label: 'None', value: 'none' },
  { label: 'Basic auth', value: 'basic' },
  { label: 'Bearer token', value: 'token' },
];

export function ConfigEditor(props: Props) {
  const { onOptionsChange, options } = props;
  const { jsonData, secureJsonFields, secureJsonData } = options;

  const authType: PromAuthType = jsonData.promAuthType ?? 'none';

  const update = <K extends keyof MyDataSourceOptions>(key: K, value: MyDataSourceOptions[K]) => {
    onOptionsChange({ ...options, jsonData: { ...jsonData, [key]: value } });
  };

  const num = (value: string): number => {
    const n = parseInt(value, 10);
    return Number.isFinite(n) ? n : 0;
  };

  // One generic pair of handlers for every secret field (Oracle password and the
  // Prometheus credentials). SecretInput only ever calls onChange when the user
  // types a new value, and onReset when the "reset" action is clicked.
  const onSecretChange =
    (key: keyof MySecureJsonData) => (event: ChangeEvent<HTMLInputElement>) => {
      onOptionsChange({
        ...options,
        secureJsonData: { ...secureJsonData, [key]: event.target.value },
      });
    };

  const onResetSecret = (key: keyof MySecureJsonData) => () => {
    onOptionsChange({
      ...options,
      secureJsonFields: { ...secureJsonFields, [key]: false },
      secureJsonData: { ...secureJsonData, [key]: '' },
    });
  };

  return (
    <>
      <FieldSet label="Connection">
        <InlineField
          label="Connection string"
          labelWidth={24}
          interactive
          tooltip="Optional full oracle:// URL. When set, it overrides the fields below."
        >
          <Input
            id="config-editor-connstr"
            value={jsonData.o_connStr}
            onChange={(e) => update('o_connStr', e.currentTarget.value)}
            placeholder="oracle://user:pass@host:1521/service"
            width={50}
          />
        </InlineField>
        <InlineField label="Host" labelWidth={24}>
          <Input
            id="config-editor-host"
            value={jsonData.o_hostname}
            onChange={(e) => update('o_hostname', e.currentTarget.value)}
            placeholder="localhost"
            width={50}
          />
        </InlineField>
        <InlineField label="Port" labelWidth={24}>
          <Input
            id="config-editor-port"
            type="number"
            value={jsonData.o_port ?? 1521}
            onChange={(e) => update('o_port', num(e.currentTarget.value))}
            width={50}
          />
        </InlineField>
        <InlineField
          label="Service name"
          labelWidth={24}
          interactive
          tooltip="Oracle service name (e.g. XEPDB1). Either a service name or a SID is required."
        >
          <Input
            id="config-editor-service"
            value={jsonData.o_service}
            onChange={(e) => update('o_service', e.currentTarget.value)}
            placeholder="XEPDB1"
            width={50}
          />
        </InlineField>
        <InlineField label="SID" labelWidth={24}>
          <Input
            id="config-editor-sid"
            value={jsonData.o_sid}
            onChange={(e) => update('o_sid', e.currentTarget.value)}
            placeholder="ORCL"
            width={50}
          />
        </InlineField>
        <InlineField label="Username" labelWidth={24}>
          <Input
            id="config-editor-user"
            value={jsonData.o_user}
            onChange={(e) => update('o_user', e.currentTarget.value)}
            placeholder="SYSTEM"
            width={50}
          />
        </InlineField>
        <InlineField label="Password" labelWidth={24}>
          <SecretInput
            id="config-editor-password"
            isConfigured={secureJsonFields.o_password}
            value={secureJsonData?.o_password}
            placeholder="Oracle password"
            width={50}
            onReset={onResetSecret('o_password')}
            onChange={onSecretChange('o_password')}
          />
        </InlineField>
      </FieldSet>

      <FieldSet label="Prometheus">
        <InlineField
          label="Enable proxy"
          labelWidth={24}
          interactive
          tooltip="Forward Prometheus-mode queries to a Prometheus HTTP API v1 (report §9). When disabled, Prometheus-mode queries fail cleanly without touching Oracle."
        >
          <Switch
            id="config-editor-prom-enabled"
            value={jsonData.promEnabled ?? false}
            onChange={(e) => update('promEnabled', e.currentTarget.checked)}
          />
        </InlineField>
        <InlineField
          label="URL"
          labelWidth={24}
          interactive
          tooltip="Base URL of the Prometheus server (http/https only; embedded credentials are rejected)."
        >
          <Input
            id="config-editor-prom-url"
            value={jsonData.promUrl}
            onChange={(e) => update('promUrl', e.currentTarget.value)}
            placeholder="http://localhost:9090"
            width={50}
          />
        </InlineField>
        <InlineField
          label="Auth type"
          labelWidth={24}
          interactive
          tooltip="HTTP authentication for Prometheus requests: none, basic (user + password) or bearer token. Credentials are stored securely."
        >
          <Select<PromAuthType>
            id="config-editor-prom-auth"
            options={PROM_AUTH_OPTIONS}
            value={jsonData.promAuthType ?? 'none'}
            onChange={(v) => update('promAuthType', v.value)}
            width={50}
          />
        </InlineField>
        {authType === 'basic' && (
          <>
            <InlineField label="Basic user" labelWidth={24}>
              <SecretInput
                id="config-editor-prom-user"
                isConfigured={secureJsonFields.promUser}
                value={secureJsonData?.promUser}
                placeholder="Prometheus basic auth user"
                width={50}
                onReset={onResetSecret('promUser')}
                onChange={onSecretChange('promUser')}
              />
            </InlineField>
            <InlineField label="Basic password" labelWidth={24}>
              <SecretInput
                id="config-editor-prom-password"
                isConfigured={secureJsonFields.promPassword}
                value={secureJsonData?.promPassword}
                placeholder="Prometheus basic auth password"
                width={50}
                onReset={onResetSecret('promPassword')}
                onChange={onSecretChange('promPassword')}
              />
            </InlineField>
          </>
        )}
        {authType === 'token' && (
          <InlineField label="Bearer token" labelWidth={24}>
            <SecretInput
              id="config-editor-prom-token"
              isConfigured={secureJsonFields.promToken}
              value={secureJsonData?.promToken}
              placeholder="Prometheus bearer token"
              width={50}
              onReset={onResetSecret('promToken')}
              onChange={onSecretChange('promToken')}
            />
          </InlineField>
        )}
        <InlineField
          label="Skip TLS verify"
          labelWidth={24}
          interactive
          tooltip="Disable TLS certificate verification for self-signed endpoints. Off by default; enable only for trusted private Prometheus servers."
        >
          <Switch
            id="config-editor-prom-tls"
            value={jsonData.promTlsSkipVerify ?? false}
            onChange={(e) => update('promTlsSkipVerify', e.currentTarget.checked)}
          />
        </InlineField>
      </FieldSet>

      <FieldSet label="General">
        <InlineField
          label="Default mode"
          labelWidth={24}
          interactive
          tooltip="Editor tab opened for new queries (report §14.1). Each query stores its own mode; this is only the default. Prometheus is a placeholder until F6."
        >
          <Select<QueryMode>
            id="config-editor-default-mode"
            options={MODE_OPTIONS}
            value={jsonData.defaultMode ?? 'sql'}
            onChange={(v) => update('defaultMode', v.value)}
            width={50}
          />
        </InlineField>
      </FieldSet>

      <FieldSet label="Limits">
        <InlineField
          label="Session time zone"
          labelWidth={24}
          interactive
          tooltip="Pinned at connect (ALTER SESSION SET TIME_ZONE) so results are independent of the plugin host's locale."
        >
          <Input
            id="config-editor-tz"
            value={jsonData.sessionTimeZone ?? 'UTC'}
            onChange={(e) => update('sessionTimeZone', e.currentTarget.value)}
            width={50}
          />
        </InlineField>
        <InlineField label="Query timeout (s)" labelWidth={24}>
          <Input
            id="config-editor-query-timeout"
            type="number"
            value={jsonData.queryTimeoutSec ?? 30}
            onChange={(e) => update('queryTimeoutSec', num(e.currentTarget.value))}
            width={50}
          />
        </InlineField>
        <InlineField label="Max open connections" labelWidth={24}>
          <Input
            id="config-editor-max-open"
            type="number"
            value={jsonData.maxOpenConns ?? 10}
            onChange={(e) => update('maxOpenConns', num(e.currentTarget.value))}
            width={50}
          />
        </InlineField>
        <InlineField label="Max idle connections" labelWidth={24}>
          <Input
            id="config-editor-max-idle"
            type="number"
            value={jsonData.maxIdleConns ?? 5}
            onChange={(e) => update('maxIdleConns', num(e.currentTarget.value))}
            width={50}
          />
        </InlineField>
        <InlineField label="Connection lifetime (s)" labelWidth={24}>
          <Input
            id="config-editor-lifetime"
            type="number"
            value={jsonData.connMaxLifetimeSec ?? 1800}
            onChange={(e) => update('connMaxLifetimeSec', num(e.currentTarget.value))}
            width={50}
          />
        </InlineField>
        <InlineField label="Max rows" labelWidth={24}>
          <Input
            id="config-editor-max-rows"
            type="number"
            value={jsonData.maxRows ?? 100000}
            onChange={(e) => update('maxRows', num(e.currentTarget.value))}
            width={50}
          />
        </InlineField>
        <InlineField label="Max series" labelWidth={24}>
          <Input
            id="config-editor-max-series"
            type="number"
            value={jsonData.maxSeries ?? 1000}
            onChange={(e) => update('maxSeries', num(e.currentTarget.value))}
            width={50}
          />
        </InlineField>
        <InlineField
          label="Large numbers"
          labelWidth={24}
          interactive
          tooltip="Policy for NUMBER(scale=0, precision>15) columns (ADR-8). Auto keeps full precision as strings in table panels and uses float in time series."
        >
          <Select<LargeNumberMode | ''>
            id="config-editor-large-numbers"
            options={LARGE_NUMBER_OPTIONS}
            value={(jsonData.largeNumberMode as LargeNumberMode | '') ?? ''}
            onChange={(v) => update('largeNumberMode', v.value === '' ? undefined : v.value)}
            width={50}
          />
        </InlineField>
        <InlineField
          label="Numeric variables"
          labelWidth={24}
          interactive
          tooltip="Dashboard variable names that may expand unquoted in SQL (report §8.2), comma-separated. A hint only — the backend still verifies each value is a strict numeric literal before emitting it bare."
        >
          <Input
            id="config-editor-numeric-vars"
            value={jsonData.numericVariables?.join(', ') ?? ''}
            onChange={(e) =>
              update(
                'numericVariables',
                e.currentTarget.value
                  .split(',')
                  .map((s) => s.trim())
                  .filter(Boolean)
              )
            }
            placeholder="port, limit"
            width={50}
          />
        </InlineField>
      </FieldSet>
    </>
  );
}
