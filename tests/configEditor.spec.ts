import { test, expect } from '@grafana/plugin-e2e';
import { MyDataSourceOptions, MySecureJsonData } from '../src/types';

test('smoke: should render config editor', async ({ createDataSourceConfigPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });
  await createDataSourceConfigPage({ type: ds.type });
  await expect(page.getByLabel('Host')).toBeVisible();
  await expect(page.getByLabel('Username')).toBeVisible();
  await expect(page.getByLabel('Password')).toBeVisible();
});

test('"Save & test" should be successful when the database is reachable', async ({
  createDataSourceConfigPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource<MyDataSourceOptions, MySecureJsonData>({ fileName: 'datasources.yml' });
  const configPage = await createDataSourceConfigPage({ type: ds.type });
  // Provisioning already supplies the connection; filling the fields keeps the
  // test explicit. Requires the matrix Oracle (oracle21) to be up.
  await page.getByRole('textbox', { name: 'Host' }).fill(ds.jsonData.o_hostname ?? '');
  await page.getByRole('textbox', { name: 'Username' }).fill(ds.jsonData.o_user ?? '');
  await expect(configPage.saveAndTest()).toBeOK();
});

test('"Save & test" should fail when the database is unreachable', async ({
  createDataSourceConfigPage,
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource<MyDataSourceOptions, MySecureJsonData>({ fileName: 'datasources.yml' });
  const configPage = await createDataSourceConfigPage({ type: ds.type });
  await page.getByRole('textbox', { name: 'Host' }).fill('no-such-host.invalid');
  await expect(configPage.saveAndTest()).not.toBeOK();
});
