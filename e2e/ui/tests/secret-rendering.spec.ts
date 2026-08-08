import { test, expect, Page, APIRequestContext } from '@playwright/test';

// Resolved plaintext secret values the fakeconnect backend serves. If any of
// these strings appears anywhere in the rendered UI, the plugin (or Nomad) is
// leaking secret material into the web console — the exact thing we forbid.
// Override for the real backend via SECRET_VALUES="v1,v2,...".
const SECRET_VALUES = (
  process.env.SECRET_VALUES ||
  'hunter2-e2e,replica-pass-e2e,app-user,db.internal.test'
)
  .split(',')
  .map((s) => s.trim())
  .filter(Boolean);

const JOB = process.env.UI_JOB_ID || 'ui-secrets';
const NOMAD_ADDR = process.env.NOMAD_ADDR || 'http://127.0.0.1:4646';

// Env var names the job maps its secrets onto. The UI is allowed to show the
// *reference* (e.g. ${secret.db.value}) but never the resolved value.
const SECRET_ENV_KEYS = ['DB_PASSWORD', 'APP_PW', 'APP_REPLICA', 'APP_USER', 'APP_DB_HOST'];

// The Nomad UI is an Ember SPA driven by blocking/long-poll queries, so
// `networkidle` never settles. Instead we load the DOM and wait for a marker
// string to be rendered by the SPA before asserting.
async function gotoSettled(page: Page, url: string, marker: string) {
  await page.goto(url, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(
    (m) => (document.body?.innerText ?? '').includes(m),
    marker,
    { timeout: 20000 },
  );
}

async function fetchAllocId(request: APIRequestContext): Promise<string> {
  const res = await request.get(`${NOMAD_ADDR}/v1/job/${JOB}/allocations`);
  expect(res.ok(), `job ${JOB} allocations query failed`).toBeTruthy();
  const allocs = await res.json();
  const running = allocs.find((a: any) => a.ClientStatus === 'running') || allocs[0];
  expect(running, `no allocation placed for job ${JOB}`).toBeTruthy();
  return running.ID;
}

// Assert none of the resolved secret values leak into the fully-rendered page.
async function expectNoLeak(page: Page, where: string) {
  // Pull the whole rendered document text once the SPA has settled.
  const body = await page.evaluate(() => document.body?.innerText ?? '');
  const html = await page.content();
  for (const secret of SECRET_VALUES) {
    expect(body, `resolved secret "${secret}" rendered as text on ${where}`).not.toContain(secret);
    expect(html, `resolved secret "${secret}" present in DOM of ${where}`).not.toContain(secret);
  }
}

test.describe('Nomad UI never exposes resolved plugin secrets', () => {
  test('job definition shows secret references, not values', async ({ page }) => {
    await gotoSettled(page, `/ui/jobs/${JOB}/definition`, JOB);

    // Positive control: the raw definition should reference the secret plumbing
    // (proving the plugin's secret blocks are actually part of this job) while
    // never exposing a resolved value.
    const html = await page.content();
    expect(html, 'job definition should reference the secret interpolation').toContain('secret');

    await expectNoLeak(page, 'job definition');
  });

  test('allocation overview and task pages redact secret values', async ({ page, request }) => {
    const allocId = await fetchAllocId(request);

    await gotoSettled(page, `/ui/allocations/${allocId}`, 'sleep');
    await expectNoLeak(page, `allocation ${allocId}`);

    // Task detail page — env vars surface here in the Nomad UI.
    await gotoSettled(page, `/ui/allocations/${allocId}/sleep`, 'sleep');
    await expectNoLeak(page, `task page for alloc ${allocId}`);
  });

  test('the Nomad HTTP API does not expose resolved secrets to the UI', async ({ request }) => {
    // The UI is a client of these endpoints. If the resolved value is absent
    // from the API payloads, it cannot appear in the console.
    const allocId = await fetchAllocId(request);
    for (const path of [`/v1/job/${JOB}`, `/v1/allocation/${allocId}`]) {
      const res = await request.get(`${NOMAD_ADDR}${path}`);
      expect(res.ok(), `${path} query failed`).toBeTruthy();
      const text = await res.text();
      for (const secret of SECRET_VALUES) {
        expect(text, `resolved secret "${secret}" leaked via ${path}`).not.toContain(secret);
      }
      // The job payload should still carry the secret *reference*, confirming
      // we're asserting against a job that actually uses the plugin.
      if (path === `/v1/job/${JOB}`) {
        for (const key of SECRET_ENV_KEYS) {
          expect(text, `env key ${key} missing from job spec`).toContain(key);
        }
      }
    }
  });
});
