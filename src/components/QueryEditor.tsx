import React from 'react';
import { Button, Input, Select, Stack, Switch, Tab, TabsBar, TabContent } from '@grafana/ui';
import { DataSourceInstanceSettings, QueryEditorProps, SelectableValue } from '@grafana/data';
import { EditorField, EditorRow, SQLEditor } from '@grafana/plugin-ui';

import { DataSource } from '../datasource';
import { oracleSQLLanguage } from '../language/oracle';
import { promQLSQLLanguage } from '../language/promql';
import { MetricBrowser } from './MetricBrowser';
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
  // configured default (report §14.1). The settings live under a different
  // property depending on the host Grafana: `instanceSettings` on DataSourceApi
  // (≤ 12.x) vs `datasourceInstanceSettings` on DataSourceWithBackend (13.x).
  // The plugin floor is 12.3, so read both defensively.
  const ds = props.datasource as DataSource & { instanceSettings?: DataSourceInstanceSettings<MyDataSourceOptions> };
  const settings = props.datasource.datasourceInstanceSettings ?? ds.instanceSettings;
  const mode: QueryMode = query.mode ?? settings?.jsonData?.defaultMode ?? 'sql';

  // Live mode the change handlers read at CALL time (refs are not bound by the
  // render closure). A monaco editor fires a spurious `onChange('')` from its
  // unmount cleanup (dispose/blur) after a tab switch — the plugin-ui SQLEditor
  // dispatches it through a stale callback (`useLatestCallback` updates after
  // commit). Without this guard that event would pin the query back to the
  // mode the user just left (F8 e2e regression).
  const modeRef = React.useRef<QueryMode>(mode);
  // Updating the ref during render is deliberate (and lint-gated): the guard
  // must read the NEW mode synchronously, before React runs the previous
  // editor's unmount cleanup — a spurious monaco onChange('') fires during the
  // commit phase, i.e. BEFORE any post-commit effect would have synced the ref.
  // eslint-disable-next-line react-hooks/refs
  modeRef.current = mode;

  const onModeChange = (next: QueryMode) => {
    if (next !== mode) {
      onChange({ ...query, mode: next });
    }
  };

  // Editing an editor's content also pins `mode` on the query — when the
  // datasource's defaultMode is 'prometheus' the effective tab already is
  // Prometheus, but query.mode stays undefined until written here, and the
  // backend routes on query.mode (F8). A change event is only honoured while
  // its mode is the ACTIVE one; late unmount events (above) are dropped.
  const onSQLChange = (sql: string) => {
    if (modeRef.current !== 'sql') {
      return;
    }
    onChange({ ...query, mode: 'sql', sql });
  };

  const onFormatChange = (value: SelectableValue<QueryFormat>) => {
    onChange({ ...query, format: value.value });
  };

  const onTimeColumnChange = (value: SelectableValue<string>) => {
    // Empty (Auto) removes the override so the backend auto-detects (§7).
    onChange({ ...query, timeColumn: value.value || undefined });
  };

  const onPromQLChange = (promql: string) => {
    if (modeRef.current !== 'prometheus') {
      return;
    }
    onChange({ ...query, mode: 'prometheus', promql });
  };

  const onInstantChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    onChange({ ...query, instant: e.currentTarget.checked });
  };

  const onLegendFormatChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    onChange({ ...query, legendFormat: e.currentTarget.value });
  };

  const onStepChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    onChange({ ...query, step: e.currentTarget.value });
  };

  // "Use query" from the metric browser commits the built selector and runs it
  // (report §14.1 sketch; the DoD path browser → PromQL → panel).
  const applyMetric = (expr: string) => {
    if (!expr) {
      return;
    }
    onChange({ ...query, promql: expr });
    onRunQuery();
  };

  return (
    <Stack direction="column" gap={1}>
      <TabsBar>
        <Tab label="Oracle (SQL)" active={mode === 'sql'} onChangeTab={() => onModeChange('sql')} />
        <Tab label="Prometheus (PromQL)" active={mode === 'prometheus'} onChangeTab={() => onModeChange('prometheus')} />
      </TabsBar>

      <TabContent>
        {mode === 'prometheus' ? (
          <>
            <MetricBrowser datasource={props.datasource} onSelect={applyMetric} />

            {/* The PromQL editor is the same monaco SQLEditor with a PromQL
                monarch language (src/language/promql.ts); the testid wrapper is
                what the e2e specs locate (monaco's textarea has no label). */}
            <div data-testid="query-editor-promql">
              <SQLEditor
                query={query.promql || ''}
                language={promQLSQLLanguage}
                onChange={onPromQLChange}
                height={220}
              />
            </div>

            <EditorRow>
              <EditorField label="Instant" htmlFor="query-editor-instant">
                <Switch id="query-editor-instant" value={!!query.instant} onChange={onInstantChange} />
              </EditorField>
              <EditorField label="Legend format" htmlFor="query-editor-legend">
                <Input
                  id="query-editor-legend"
                  value={query.legendFormat || ''}
                  onChange={onLegendFormatChange}
                  placeholder="{{ __name__ }}"
                  width={30}
                />
              </EditorField>
              <EditorField label="Step" htmlFor="query-editor-step">
                <Input
                  id="query-editor-step"
                  value={query.step || ''}
                  onChange={onStepChange}
                  placeholder="auto"
                  width={16}
                />
              </EditorField>
            </EditorRow>

            <div>
              <Button size="sm" icon="play" variant="secondary" onClick={onRunQuery}>
                Run query
              </Button>
            </div>
          </>
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
