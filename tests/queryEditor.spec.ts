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
  // F7: the PromQL editor is a monaco SQLEditor (plugin-ui) with a PromQL
  // monarch language, located via its testid (the textarea carries no label).
  await expect(panelEditPage.getQueryEditorRow('A').getByTestId('query-editor-promql')).toBeVisible();
  await expect(panelEditPage.getQueryEditorRow('A').getByLabel('Legend format')).toBeVisible();
  await page.getByRole('tab', { name: 'Oracle (SQL)' }).click();
  await expect(panelEditPage.getQueryEditorRow('A').getByTestId('query-editor-sql')).toBeVisible();
});

// F7 DoD ("metric browser → PromQL → panel"): requires the matrix Prometheus
// (docker compose --profile prom up -d prometheus) and the provisioned
// prometheus-datasource from provisioning/datasources/datasources-prom.yml.
test('metric browser: selecting a metric builds a PromQL query', async ({
  panelEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources-prom.yml' });
  await panelEditPage.datasource.set(ds.name);
  await page.getByRole('tab', { name: 'Prometheus (PromQL)' }).click();
  const browser = panelEditPage.getQueryEditorRow('A').getByTestId('query-editor-metric-browser');
  await expect(browser).toBeVisible();
  // react-select renders the dropdown; typing applies the prefix filter. The
  // menu is PORTALED to the document root, so options must be located on the
  // page, not inside the metric-browser subtree. The metric list also loads
  // asynchronously through the resource proxy, so wait for the filtered "up"
  // option before committing — Enter on an empty/loading menu selects nothing
  // and "Use query" stays disabled.
  await browser.getByRole('combobox').click();
  await page.keyboard.type('up');
  await expect(page.getByRole('option', { name: 'up', exact: true })).toBeVisible();
  await page.getByRole('option', { name: 'up', exact: true }).click();
  // Selecting a metric loads its series → the label filter row + Use query.
  await expect(browser.getByRole('button', { name: 'Use query' })).toBeEnabled();
  await browser.getByRole('button', { name: 'Use query' }).click();
  // The built PromQL lands in the editor and the query runs against the proxy.
  await expect(panelEditPage.getQueryEditorRow('A').getByTestId('query-editor-promql')).toContainText('up');
  await expect(panelEditPage.refreshPanel()).toBeOK();
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
