import { test, expect } from '@grafana/plugin-e2e';

// F9 — dashboard annotations (report §11). A dashboard annotation with a query
// target on our datasource must be evaluated by the FRONTEND annotation runner
// (Grafana 13 executeAnnotationQuery): the request carries scopedVars.__annotation
// and refId 'Anno', applyTemplateVariables sets query.annotation=true, and the
// backend must return the frame with schema.meta.dataTopic === 'annotations'
// (table shape, never time-series). That DataTopic is what makes Grafana draw
// the events on the panel.
//
// NOTE: the server endpoint GET /api/annotations only returns *persisted*
// annotations — dashboard query annotations are evaluated client-side — so the
// assertion is on the browser's /api/ds/query call, not the annotations API.
//
// The annotation target queries ALL_OBJECTS (no seeding needed): the CREATED
// DATE becomes the annotation time, OBJECT_TYPE the title, OBJECT_NAME the text.

test('annotations: a dashboard annotation query is marked on the panel', async ({
  readProvisionedDataSource,
  page,
}) => {
  const ds = await readProvisionedDataSource({ fileName: 'datasources.yml' });

  const sql =
    'SELECT CREATED AS "time", OBJECT_TYPE AS "title", OBJECT_NAME AS "text" FROM ALL_OBJECTS WHERE $__timeFilter(CREATED) AND ROWNUM <= 5';
  const uid = `f9-anno-${Date.now()}`;
  const createResp = await page.request.post('/api/dashboards/db', {
    data: {
      dashboard: {
        title: 'F9 annotations',
        uid,
        // Wide range: ALL_OBJECTS.CREATED spans the lifetime of the database.
        time: { from: 'now-100y', to: 'now' },
        timezone: 'utc',
        annotations: {
          list: [
            {
              name: 'Oracle ALL_OBJECTS',
              datasource: { type: 'emreatayy-oracle-datasource', uid: ds.uid },
              enable: true,
              hide: false,
              iconColor: 'red',
              // target is the saved MyQuery; the annotation runner adds refId 'Anno'
              // and scopedVars.__annotation at evaluation time.
              target: {
                refId: 'Anno',
                mode: 'sql',
                format: 'table',
                sql,
                datasource: { type: 'emreatayy-oracle-datasource', uid: ds.uid },
              },
            },
          ],
        },
        panels: [],
      },
      overwrite: true,
    },
  });
  expect(createResp.ok()).toBeTruthy();

  // The dashboard loads → the annotation runner POSTs /api/ds/query with
  // queries[0].refId === 'Anno'. Capture that exact response.
  const annoQuery = page.waitForResponse(async (resp) => {
    if (resp.request().method() !== 'POST' || !resp.url().includes('/api/ds/query')) {
      return false;
    }
    const body = resp.request().postDataJSON();
    const refIds: string[] = (body?.queries ?? []).map((q: { refId?: string }) => q.refId ?? '');
    return refIds.includes('Anno');
  });

  await page.goto(`/d/${uid}/f9-annotations`);

  const resp = await annoQuery;
  expect(resp.status()).toBe(200);
  const body = await resp.json();
  const frame = body?.results?.Anno?.frames?.[0];
  expect(frame, 'expected an annotation frame under results.Anno').toBeTruthy();
  // The backend marks the frame DataTopic.Annotations (report §11) — without it
  // Grafana would not draw the events on the panel.
  expect(frame.schema?.meta?.dataTopic).toBe('annotations');

  // The annotation target is table-shaped: a time field plus the string title
  // and text fields getAnnotationsFromData maps by name.
  const fieldNames: string[] = (frame.schema?.fields ?? []).map((f: { name?: string }) => f.name ?? '');
  expect(fieldNames).toContain('time');
  expect(fieldNames).toContain('text');
  // ROWNUM <= 5 → up to five annotation events came back.
  const rows = frame.data?.values?.[0]?.length ?? 0;
  expect(rows).toBeGreaterThan(0);
  expect(rows).toBeLessThanOrEqual(5);
});
