// Prometheus metric browser (report §14.1 sketch, F7). A prefix-filtered
// metric dropdown (GET /api/v1/label/__name__/values via the plugin resource
// proxy) plus per-label value filters derived from the metric's series
// (GET /api/v1/series). "Use query" commits the built PromQL selector to the
// editor; nothing is written while browsing.
//
// The browser must be able to render even when the proxy is disabled (the
// editor is still usable by typing PromQL directly), so failures degrade to a
// compact inline note instead of an error wall.
import React, { useEffect, useMemo, useState } from 'react';
import { Alert, Button, Select } from '@grafana/ui';
import type { SelectableValue } from '@grafana/data';
import { EditorField, EditorRow } from '@grafana/plugin-ui';

import type { DataSource } from '../datasource';

interface MetricBrowserProps {
  datasource: DataSource;
  /** Commit the built PromQL expression to the query. */
  onSelect: (expr: string) => void;
}

const ALL_VALUES = '__all__';

// PromQL selector values are quoted; escape embedded quotes/backslashes so the
// built expression stays valid (report §9.2).
function escapeLabelValue(v: string): string {
  return v.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
}

export function MetricBrowser({ datasource, onSelect }: MetricBrowserProps) {
  const [metrics, setMetrics] = useState<string[]>([]);
  const [metric, setMetric] = useState<string | undefined>();
  const [labelValues, setLabelValues] = useState<Record<string, string[]>>({});
  const [filters, setFilters] = useState<Record<string, string>>({});
  const [loadingMetrics, setLoadingMetrics] = useState(true);
  const [loadingLabels, setLoadingLabels] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    datasource
      .getPromMetricNames()
      .then((names) => {
        if (!cancelled) {
          setMetrics(names);
          setLoadingMetrics(false);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err));
          setLoadingMetrics(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [datasource]);

  const onMetricChange = async (v: SelectableValue<string>) => {
    const name = v?.value;
    setMetric(name);
    setFilters({});
    if (!name) {
      setLabelValues({});
      return;
    }
    setLoadingLabels(true);
    setError(null);
    try {
      const series = await datasource.getPromSeries(name);
      // Union of the metric's label names (minus __name__) with the distinct
      // values each takes — one series call covers both the dropdown and its
      // value lists, so browsing stays a single request.
      const byLabel: Record<string, Set<string>> = {};
      for (const s of series) {
        for (const [k, val] of Object.entries(s)) {
          if (k === '__name__') {
            continue;
          }
          (byLabel[k] ??= new Set()).add(val);
        }
      }
      const values: Record<string, string[]> = {};
      for (const [k, vals] of Object.entries(byLabel)) {
        values[k] = [...vals].sort();
      }
      setLabelValues(values);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setLabelValues({});
    } finally {
      setLoadingLabels(false);
    }
  };

  const onFilterChange = (label: string) => (v: SelectableValue<string>) => {
    setFilters((f) => ({ ...f, [label]: v?.value ?? ALL_VALUES }));
  };

  const expr = useMemo(() => {
    if (!metric) {
      return '';
    }
    const active = Object.entries(filters).filter(([, v]) => v && v !== ALL_VALUES);
    if (active.length === 0) {
      return metric;
    }
    const parts = active.map(([k, v]) => `${k}="${escapeLabelValue(v)}"`);
    return `${metric}{${parts.join(', ')}}`;
  }, [metric, filters]);

  const metricOptions: Array<SelectableValue<string>> = metrics.map((m) => ({ label: m, value: m }));
  // Prefix filter, case-insensitive (report §14.1 "prefix filter").
  const filterOption = (option: SelectableValue<string>, search: string) => {
    const q = search.trim().toLowerCase();
    return q === '' || (option.label ?? '').toLowerCase().startsWith(q);
  };

  const labelNames = Object.keys(labelValues).sort();
  const disabled = error !== null && !loadingMetrics;

  return (
    <div data-testid="query-editor-metric-browser">
      <EditorRow>
        <EditorField label="Metric" htmlFor="query-editor-metric">
          <Select<string>
            id="query-editor-metric"
            aria-label="Metric"
            placeholder="Select a metric…"
            options={metricOptions}
            value={metric ?? null}
            onChange={onMetricChange}
            isLoading={loadingMetrics}
            filterOption={filterOption}
            isClearable
            width={36}
          />
        </EditorField>
        <Button size="sm" variant="secondary" icon="play" disabled={!expr} onClick={() => onSelect(expr)}>
          Use query
        </Button>
      </EditorRow>

      {loadingLabels && <Alert severity="info" title="Loading label values…" />}

      {!loadingLabels && labelNames.length > 0 && (
        <EditorRow>
          {labelNames.map((label) => (
            <EditorField key={label} label={label} htmlFor={`query-editor-label-${label}`}>
              <Select<string>
                id={`query-editor-label-${label}`}
                aria-label={label}
                placeholder="All"
                options={[
                  { label: 'All', value: ALL_VALUES },
                  ...labelValues[label].map((v) => ({ label: v, value: v })),
                ]}
                value={filters[label] ?? ALL_VALUES}
                onChange={onFilterChange(label)}
                isClearable
                width={24}
              />
            </EditorField>
          ))}
        </EditorRow>
      )}

      {metric && expr !== metric && (
        <div style={{ padding: '4px 0', color: 'var(--text-secondary)', fontSize: '12px' }}>{expr}</div>
      )}

      {disabled && (
        <Alert severity="info" title="Metric browser unavailable">
          {error}
        </Alert>
      )}
    </div>
  );
}
