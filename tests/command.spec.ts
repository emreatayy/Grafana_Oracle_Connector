import { test, expect } from '@grafana/plugin-e2e';

// F15 "Run command" (report §12). The button is a deliberate one-shot action —
// it posts the current SQL as an anonymous PL/SQL command to the backend
// resource handler, which ALWAYS requires confirm=true (§12.3) and only runs
// allowlisted commands. The result/error renders inline under the button.
// Requires the matrix Oracle (oracle21) to be up.

// The default query (SELECT 1 AS value FROM DUAL) is itself an allowlisted
// command shape, so no editor editing is needed for the happy path: Run command
// posts it, the confirmation dialog gates it, and the returned value (1)
// renders inline.
test('run command: allowlisted SELECT…FROM DUAL runs and shows the result inline', async ({
  panelEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);
  const row = panelEditPage.getQueryEditorRow('A');
  await expect(row.getByTestId('query-editor-sql')).toBeVisible();

  await row.getByRole('button', { name: 'Run command' }).click();

  // §12.3: a confirmation dialog precedes execution (config-toggleable).
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: 'Confirm' }).click();

  // The returned value renders inline (§12.1); the box disappears on a new run.
  const result = row.getByTestId('query-editor-command-result');
  await expect(result).toContainText('1');
});

// DoD: "allowlist rejects DDL". Replacing the query with DDL and running it must
// surface a readable inline rejection — the command never reaches Oracle.
test('run command: allowlist rejects DDL with a readable inline error', async ({
  panelEditPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await panelEditPage.datasource.set(ds.name);
  const row = panelEditPage.getQueryEditorRow('A');
  await expect(row.getByTestId('query-editor-sql')).toBeVisible();

  // The SQLEditor is monaco: focus its hidden textarea, select all, replace.
  const textarea = row.getByTestId('query-editor-sql').locator('textarea').first();
  await textarea.click();
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.type('DROP TABLE dual');
  // Wait for the editor to commit the new text before reading query.sql.
  await expect(row.getByTestId('query-editor-sql')).toContainText('DROP TABLE dual');

  await row.getByRole('button', { name: 'Run command' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: 'Confirm' }).click();

  const result = row.getByTestId('query-editor-command-result');
  // The backend shape guard's readable message ("command must be a BEGIN...END;
  // PL/SQL block or a SELECT ... FROM DUAL"), rendered inline as a failed alert.
  await expect(result).toContainText('must be a BEGIN');
});
