import React from 'react';
import { Alert, Button, Select, Stack, Tab, TabsBar, TabContent } from '@grafana/ui';
import { QueryEditorProps, SelectableValue } from '@grafana/data';
import { EditorField, EditorRow, SQLEditor } from '@grafana/plugin-ui';

import { DataSource } from '../datasource';
import { oracleSQLLanguage } from '../language/oracle';
import { MyDataSourceOptions, MyQuery, QueryFormat, QueryMode } from '../types';

type Props = QueryEditorProps<DataSource, MyQuery, MyDataSourceOptions>;

const FORMAT_OPTIONS: Array<SelectableValue<QueryFormat>> = [
  { label: 'Auto', value: 'auto', description: 'Time column + numeric value → time series, otherwise table' },
  { label: 'Table', value: 'table' },
  { label: 'Time series', value: 'time_series' },
];

// Auto + free text until F9 wires the metadata-driven column list (report
// §14.1: "wired to F9 metadata, static first").
const TIME_COLUMN_OPTIONS: Array<SelectableValue<string>> = [
  { label: 'Auto', value: '', description: 'Detect the time column from the result' },
];

export function QueryEditor(props: Props) {
  const { query, onChange, onRunQuery } = props;

  // The editor mode is stored per query and defaults to the datasource's
  // configured default (report §14.1).
  // DataSourceWithBackend exposes the settings as `datasourceInstanceSettings`
  // (G13 moved instanceSettings off DataSourceApi).
  const mode: QueryMode = query.mode ?? props.datasource.datasourceInstanceSettings.jsonData.defaultMode ?? 'sql';

  const onModeChange = (next: QueryMode) => {
    if (next !== mode) {
      onChange({ ...query, mode: next });
    }
  };

  const onSQLChange = (sql: string) => {
    onChange({ ...query, sql });
  };

  const onFormatChange = (value: SelectableValue<QueryFormat>) => {
    onChange({ ...query, format: value.value });
  };

  const onTimeColumnChange = (value: SelectableValue<string>) => {
    // Empty (Auto) removes the override so the backend auto-detects (§7).
    onChange({ ...query, timeColumn: value.value || undefined });
  };

  return (
    <Stack direction="column" gap={1}>
      <TabsBar>
        <Tab label="Oracle (SQL)" active={mode === 'sql'} onChangeTab={() => onModeChange('sql')} />
        <Tab label="Prometheus (PromQL)" active={mode === 'prometheus'} onChangeTab={() => onModeChange('prometheus')} />
      </TabsBar>

      <TabContent>
        {mode === 'prometheus' ? (
          <Alert severity="info" title="Prometheus proxy arrives in a later phase">
            The Prometheus (PromQL) editor is planned for phase F6/F7. The mode selection is saved with this query; the
            backend rejects Prometheus-mode queries until then.
          </Alert>
        ) : (
          <>
            <EditorRow>
              <EditorField label="Format" htmlFor="query-editor-format">
                <Select<QueryFormat>
                  id="query-editor-format"
                  options={FORMAT_OPTIONS}
                  value={query.format ?? 'auto'}
                  onChange={onFormatChange}
                  width={30}
                />
              </EditorField>
              {/* react-select renders the id on both the container div and the
                  inner input, so getByLabel resolves to a non-labelable div and
                  never matches — locate via testid instead (e2e). */}
              <div data-testid="query-editor-time-column">
                <EditorField label="Time column" htmlFor="query-editor-time-column">
                  <Select<string>
                    id="query-editor-time-column"
                    options={TIME_COLUMN_OPTIONS}
                    value={query.timeColumn ?? ''}
                    onChange={onTimeColumnChange}
                    allowCustomValue
                    width={24}
                  />
                </EditorField>
              </div>
            </EditorRow>

            <div data-testid="query-editor-sql">
              <SQLEditor query={query.sql || ''} language={oracleSQLLanguage} onChange={onSQLChange} height={220} />
            </div>

            <div>
              <Button size="sm" icon="play" variant="secondary" onClick={onRunQuery}>
                Run query
              </Button>
            </div>
          </>
        )}
      </TabContent>
    </Stack>
  );
}
