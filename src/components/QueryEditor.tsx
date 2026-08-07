import React, { ChangeEvent } from 'react';
import { Button, Field, Stack, TextArea } from '@grafana/ui';
import { QueryEditorProps } from '@grafana/data';
import { DataSource } from '../datasource';
import { MyDataSourceOptions, MyQuery } from '../types';

type Props = QueryEditorProps<DataSource, MyQuery, MyDataSourceOptions>;

export function QueryEditor({ query, onChange, onRunQuery }: Props) {
  const onSQLChange = (event: ChangeEvent<HTMLTextAreaElement>) => {
    onChange({ ...query, sql: event.target.value });
  };

  return (
    <Stack direction="column" gap={1}>
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
