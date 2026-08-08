import React, { ChangeEvent } from 'react';
import { Button, Field, Select, Stack, TextArea } from '@grafana/ui';
import { QueryEditorProps, SelectableValue } from '@grafana/data';
import { DataSource } from '../datasource';
import { MyDataSourceOptions, MyQuery, QueryFormat } from '../types';

type Props = QueryEditorProps<DataSource, MyQuery, MyDataSourceOptions>;

const FORMAT_OPTIONS: Array<SelectableValue<QueryFormat>> = [
  { label: 'Auto', value: 'auto', description: 'Time column + numeric value → time series, otherwise table' },
  { label: 'Table', value: 'table' },
  { label: 'Time series', value: 'time_series' },
];

export function QueryEditor({ query, onChange, onRunQuery }: Props) {
  const onSQLChange = (event: ChangeEvent<HTMLTextAreaElement>) => {
    onChange({ ...query, sql: event.target.value });
  };

  const onFormatChange = (value: SelectableValue<QueryFormat>) => {
    onChange({ ...query, format: value.value });
  };

  return (
    <Stack direction="column" gap={1}>
      <Field label="Format" description="How the result is shaped into a frame (report §7)">
        <Select<QueryFormat>
          id="query-editor-format"
          options={FORMAT_OPTIONS}
          value={query.format ?? 'auto'}
          onChange={onFormatChange}
          width={30}
        />
      </Field>
      <Field label="SQL" description="Read-only: only SELECT and WITH statements are allowed">
        <TextArea
          id="query-editor-sql"
          value={query.sql || ''}
          onChange={onSQLChange}
          onBlur={onRunQuery}
          placeholder="SELECT * FROM DUAL"
          rows={8}
          style={{ fontFamily: 'var(--font-monospace)' }}
        />
      </Field>
      <div>
        <Button size="sm" icon="play" variant="secondary" onClick={onRunQuery}>
          Run query
        </Button>
      </div>
    </Stack>
  );
}
