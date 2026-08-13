import { test, expect } from '@grafana/plugin-e2e';
import type { Page } from '@playwright/test';
import { MyDataSourceOptions, MySecureJsonData } from '../src/types';

// The Port input renders with a default of 1521 that is not committed to
// jsonData until the field is edited, so filling the same value again does not
// fire React's onChange and o_port would stay unset (saved as 0). Fill a
// throwaway value first to force a commit.
async function fillPort(page: Page, port?: number) {
  const target = String(port ?? 1521);
  await page.getByLabel('Port').fill(target === '1521' ? '1520' : target);
  await page.getByLabel('Port').fill(target);
}

test('smoke: should render config editor', async ({ createDataSourceConfigPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await createDataSourceConfigPage({ type: ds.type });
  await expect(page.getByLabel('Host')).toBeVisible();
  await expect(page.getByLabel('Username')).toBeVisible();
  await expect(page.getByLabel('Password')).toBeVisible();
});

test('F14: custom CA TLS mode reveals the CA certificate input', async ({
  createDataSourceConfigPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await createDataSourceConfigPage({ type: ds.type });

  const tlsMode = page.getByRole('combobox', { name: 'TLS mode' });
  await expect(tlsMode).toHaveValue('');
  await expect(page.getByLabel('CA certificate')).toBeHidden();

  await tlsMode.click();
  await page.getByText('SSL, verify custom CA', { exact: true }).click();

  const caCertificate = page.getByLabel('CA certificate');
  await expect(caCertificate).toBeVisible();
  await caCertificate.fill('-----BEGIN CERTIFICATE-----\ntest-ca\n-----END CERTIFICATE-----');
  await expect(caCertificate).toHaveValue(/test-ca/);
});

test('F14: TNS descriptor and column-unit mappings can be entered', async ({
  createDataSourceConfigPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await createDataSourceConfigPage({ type: ds.type });

  const descriptor = '(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=oracle.example)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=ORCLPDB1)))';
  await page.getByLabel('Host').fill(descriptor);
  await page.getByLabel('Column units').fill('LOAD=percent\nMEMORY_BYTES=decbytes');

  await expect(page.getByLabel('Host')).toHaveValue(descriptor);
  await expect(page.getByLabel('Column units')).toHaveValue('LOAD=percent\nMEMORY_BYTES=decbytes');
});

test('"Save & test" should be successful when the database is reachable', async ({
  createDataSourceConfigPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource<MyDataSourceOptions, MySecureJsonData>({ fileName: 'datasources.yml' });
  const configPage = await createDataSourceConfigPage({ type: ds.type });
  // createDataSourceConfigPage starts with an EMPTY datasource (no settings are
  // carried over from provisioning), so fill the full connection. Requires the
  // matrix Oracle (oracle21, reachable over the compose network) to be up.
  await page.getByLabel('Host').fill(ds.jsonData.o_hostname ?? '');
  await fillPort(page, ds.jsonData.o_port);
  await page.getByLabel('Service name').fill(ds.jsonData.o_service ?? '');
  await page.getByLabel('Username').fill(ds.jsonData.o_user ?? '');
  await page.getByLabel('Password').fill('verif_password'); // matches provisioning/datasources/datasources.yml
  await expect(configPage.saveAndTest()).toBeOK();
});

test('"Save & test" should fail when the database is unreachable', async ({
  createDataSourceConfigPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource<MyDataSourceOptions, MySecureJsonData>({ fileName: 'datasources.yml' });
  const configPage = await createDataSourceConfigPage({ type: ds.type });
  // Same full connection, but an unresolvable host => the health check must
  // report a readable NETWORK failure (report §13 / F10), not a
  // config-validation error or a raw stack.
  await page.getByLabel('Host').fill('no-such-host.invalid');
  await fillPort(page, ds.jsonData.o_port);
  await page.getByLabel('Service name').fill(ds.jsonData.o_service ?? '');
  await page.getByLabel('Username').fill(ds.jsonData.o_user ?? '');
  await page.getByLabel('Password').fill('verif_password');
  await expect(configPage.saveAndTest()).not.toBeOK();
  // The Test alert carries the F10 JSON detail (report §13); its error text
  // must read as a readable network error, never the configured password.
  const alert = page.getByRole('alert').filter({ hasText: 'network error' }).first();
  await expect(alert).toBeVisible();
  await expect(alert).not.toContainText('verif_password');
});
