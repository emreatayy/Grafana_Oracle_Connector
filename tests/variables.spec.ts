import { test, expect } from '@grafana/plugin-e2e';
import type { Page } from '@playwright/test';

const PLUGIN_ID = 'emreatayy-oracle-datasource';

// Type a full SQL/PromQL into a monaco editor. The plugin-ui SQLEditor is a
// monaco CodeEditor: its textarea has no accessible label (the testid wrapper
// is the locator anchor), and fill() does not work — focus the editor, select
// all, then type (monaco replaces the selection). monaco mounts its textarea
// asynchronously after the wrapper renders; keystrokes typed before the
// textarea is attached are silently dropped (F8), so wait for and focus it.
async function typeInMonaco(page: Page, testId: string, text: string) {
  const editor = page.getByTestId(testId);
  await editor.click();
  const textarea = editor.locator('.monaco-editor textarea').first();
  await textarea.waitFor({ state: 'attached' });
  await textarea.focus();
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.type(text);
}

// F8 DoD "dropdown variable + multi-select dashboard scenarios e2e" (report
// §18): a SQL single-column query variable populates a Grafana dropdown, a
// Prometheus `label_values(job)` variable populates one from the proxy, and a
// multi-select variable drives a panel through `${var:csv}` → `IN (...)`.
test('query variable: SQL single-column query populates the dropdown', async ({
  variableEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await variableEditPage.datasource.set(ds.name);
  // A bounded single-column query over ALL_OBJECTS returns exactly two
  // deterministic values (SYSTEM sees all objects; TABLE/VIEW always exist).
  await typeInMonaco(
    page,
    'query-editor-sql',
    "SELECT DISTINCT object_type FROM ALL_OBJECTS WHERE object_type IN ('TABLE','VIEW') ORDER BY 1"
  );
  // The scenes variable editor (Grafana 13) runs the query on every change via
  // variable.validateAndUpdate — including the partial SQL mid-typing, which
  // can error. The final keystroke leaves the complete SQL; clicking "Run
  // query" (the submit button) forces a fresh validation with the complete
  // query, then the matcher's generous timeout absorbs the rest.
  await variableEditPage.runQuery();
  await expect(variableEditPage).toDisplayPreviews(['TABLE', 'VIEW'], { timeout: 20000 });
});

test('query variable: Prometheus label_values(job) populates the dropdown', async ({
  variableEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources-prom.yml' });
  await variableEditPage.datasource.set(ds.name);
  // The prometheus-datasource defaults to the Prometheus tab; the call is
  // still idempotent. `label_values(job)` is intercepted by the backend and
  // never reaches the PromQL parser (report §8.5).
  await page.getByRole('tab', { name: 'Prometheus (PromQL)' }).click();
  await typeInMonaco(page, 'query-editor-promql', 'label_values(job)');
  // Same run contract as the SQL test above — click "Run query" to validate
  // with the complete PromQL. Deterministic from devenv/prometheus.yml: the
  // matrix Prometheus scrapes exactly two jobs, and the backend returns them
  // sorted.
  await variableEditPage.runQuery();
  await expect(variableEditPage).toDisplayPreviews(['oracle_exporter', 'prometheus'], { timeout: 20000 });
});

test('multi-value variable: ${var:csv} filters the panel via IN', async ({
  page,
  gotoDashboardPage,
  readProvisionedDataSource,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  const uid = 'f8-multiselect-check';
  const dash = {
    dashboard: {
      uid,
      title: 'F8 multi-value check',
      schemaVersion: 39,
      timezone: 'utc',
      time: { from: 'now-6h', to: 'now' },
      templating: {
        list: [
          {
            name: 'objs',
            type: 'query',
            datasource: { type: PLUGIN_ID, uid: ds.uid },
            query: {
              refId: 'A',
              datasource: { type: PLUGIN_ID, uid: ds.uid },
              sql: 'SELECT DISTINCT object_type FROM ALL_OBJECTS ORDER BY object_type',
              format: 'table',
            },
            multi: true,
            includeAll: false,
            // Deterministic initial selection: exactly two values.
            current: { text: 'TABLE + VIEW', value: ['TABLE', 'VIEW'] },
          },
        ],
      },
      panels: [
        {
          id: 1,
          type: 'table',
          title: 'F8 objs',
          gridPos: { x: 0, y: 0, w: 12, h: 8 },
          datasource: { type: PLUGIN_ID, uid: ds.uid },
          targets: [
            {
              refId: 'A',
              datasource: { type: PLUGIN_ID, uid: ds.uid },
              sql: "SELECT object_type, COUNT(*) AS n FROM ALL_OBJECTS WHERE object_type IN (${objs:csv}) GROUP BY object_type ORDER BY object_type",
              format: 'table',
            },
          ],
          fieldConfig: { defaults: {}, overrides: [] },
        },
      ],
    },
    overwrite: true,
  };
  const res = await page.request.post('/api/dashboards/db', { data: dash });
  expect(res.ok(), `dashboard API ${res.status()}: ${await res.text()}`).toBeTruthy();

  const dashboard = await gotoDashboardPage({ uid });
  const panel = dashboard.getPanelByTitle('F8 objs');
  // Grafana 13 (scenes) does not put dashboard variables in a panel request's
  // scopedVars — datasource.applyTemplateVariables pulls them from the global
  // template service instead, so the backend receives objs=['TABLE','VIEW'] and
  // expands ${objs:csv} -> IN ('TABLE','VIEW'). Both selected values render as
  // table cells; a value outside the selection does not.
  // (Panel exposes `fieldNames`/`data` locators, not `getByText`.)
  await expect(panel.data).toContainText(['TABLE', 'VIEW']);
  await expect(panel.data).not.toContainText(['PROCEDURE']);
});
