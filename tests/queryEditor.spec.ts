import { test, expect } from '@grafana/plugin-e2e';
import type { Page } from '@playwright/test';

test('smoke: should render the monaco SQL editor with SQL mode active by default', async ({
  panelEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);
  // F4: the SQL editor is a monaco code editor (plugin-ui SQLEditor), located
  // via the testid wrapper (monaco's textarea carries no accessible label).
  const editor = panelEditPage.getQueryEditorRow('A').getByTestId('query-editor-sql');
  await expect(editor).toBeVisible();
  await expect(page.getByRole('tab', { name: 'Oracle (SQL)' })).toBeVisible();
  await expect(page.getByRole('tab', { name: 'Prometheus (PromQL)' })).toBeVisible();
});

test('mode is stored per query: switching tabs keeps the editor mode', async ({
  panelEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);
  await page.getByRole('tab', { name: 'Prometheus (PromQL)' }).click();
  // F6: the backend proxy is live; the PromQL editor UI is planned for F7.
  await expect(page.getByText('Prometheus (PromQL) editor arrives in phase F7')).toBeVisible();
  await page.getByRole('tab', { name: 'Oracle (SQL)' }).click();
  await expect(panelEditPage.getQueryEditorRow('A').getByTestId('query-editor-sql')).toBeVisible();
});

test('time column select accepts a custom column name', async ({ panelEditPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);
  // react-select renders the Select id on both its container div and inner
  // input, so getByLabel('Time column') never resolves — the editor locates
  // the field via a data-testid wrapper (see QueryEditor.tsx).
  const timeColumn = panelEditPage.getQueryEditorRow('A').getByTestId('query-editor-time-column');
  await expect(timeColumn).toBeVisible();
  // The dropdown is static (Auto) until F9 wires table metadata (report
  // §14.1); with allowCustomValue a typed column name is accepted and saved.
  await timeColumn.getByRole('combobox').click();
  await page.keyboard.type('ts');
  await page.keyboard.press('Enter');
  // The created option is selected and rendered in react-select's value
  // container (the inner input clears on selection, so assert on the label).
  await expect(timeColumn).toContainText('ts');
});

// The default query (SELECT 1 AS value FROM DUAL) runs against the real Oracle
// when the panel loads. Requires the matrix Oracle (oracle21) to be up.
test('data query should return a frame', async ({ panelEditPage, readProvisionedDataSource }) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);
  await panelEditPage.setVisualization('Table');
  await expect(panelEditPage.refreshPanel()).toBeOK();
  await expect(panelEditPage.panel.data).toContainText(['1']);
});
