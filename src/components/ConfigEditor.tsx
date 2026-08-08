import React, { ChangeEvent } from 'react';
import { FieldSet, InlineField, Input, SecretInput, Select } from '@grafana/ui';
import { DataSourcePluginOptionsEditorProps, SelectableValue } from '@grafana/data';
import { LargeNumberMode, MyDataSourceOptions, MySecureJsonData } from '../types';

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

export function ConfigEditor(props: Props) {
  const { onOptionsChange, options } = props;
  const { jsonData, secureJsonFields, secureJsonData } = options;

  const update = <K extends keyof MyDataSourceOptions>(key: K, value: MyDataSourceOptions[K]) => {
    onOptionsChange({ ...options, jsonData: { ...jsonData, [key]: value } });
  };

  const num = (value: string): number => {
    const n = parseInt(value, 10);
    return Number.isFinite(n) ? n : 0;
  };

  const onPasswordChange = (event: ChangeEvent<HTMLInputElement>) => {
    onOptionsChange({
      ...options,
      secureJsonData: { ...secureJsonData, o_password: event.target.value },
    });
  };

  const onResetPassword = () => {
    onOptionsChange({
      ...options,
      secureJsonFields: { ...secureJsonFields, o_password: false },
      secureJsonData: { ...secureJsonData, o_password: '' },
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
            onReset={onResetPassword}
            onChange={onPasswordChange}
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
