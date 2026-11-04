import { expect, test, type Page } from '@playwright/test';
import type { Transcription, Status } from '../../src/lib/types';

function item(id: string, status: Status = 'succeeded', createdAt = '2026-10-13T10:00:00Z', completedAt = '2026-10-14T10:00:00Z'): Transcription {
  return {
    id, status, options: { model: 'large-v3-turbo-q5_0', language: 'en', task: 'transcribe', wordTimestamps: false, initialPrompt: '', beamSize: 5, temperature: 0 },
    audio: { filename: `${id}.wav`, mediaType: 'audio/wav', sizeBytes: 1000, durationSeconds: 2, sampleRateHz: 16000, channels: 1, codec: 'pcm_s16le' },
    result: status === 'succeeded' ? { text: `Transcript for ${id}`, language: 'en', durationSeconds: 2, model: 'large-v3-turbo-q5_0', task: 'transcribe', segments: [{ id: 0, start: 0, end: 2, text: `Transcript for ${id}` }] } : null,
    attempts: 1, maxAttempts: 3, createdAt, updatedAt: new Date().toISOString(), startedAt: status === 'running' ? new Date(Date.now() - 10000).toISOString() : undefined,
    completedAt: status === 'pending' || status === 'running' ? undefined : completedAt,
  };
}

async function mockService(page: Page, items: () => Transcription[]) {
  await page.route('**/readyz', r => r.fulfill({ json: { ok: true, checks: { database: { ok: true }, storage: { ok: true, freeBytes: 50000, totalBytes: 100000 } } } }));
  await page.route('**/api/v1/storage', r => r.fulfill({ json: { freeBytes: 50000 } }));
  await page.route('**/api/v1/transcriptions?*', r => r.fulfill({ json: { items: items() } }));
  await page.route('**/api/v1/transcriptions/*', r => {
    const id = new URL(r.request().url()).pathname.split('/').pop();
    const row = items().find(t => t.id === id);
    return r.fulfill(row ? { json: row } : { status: 404, json: { code: 'not_found', message: 'Not found' } });
  });
  await page.route('**/api/v1/transcriptions/*/transcript?*', r => {
    const id = new URL(r.request().url()).pathname.split('/').at(-2);
    const row = items().find(t => t.id === id);
    return r.fulfill(row?.result ? { contentType: 'text/plain', body: row.result.text } : { status: 404, body: 'Not available' });
  });
}

test('Queue puts running work first and pending work in submission order', async ({ page }) => {
  const rows = [item('new-pending', 'pending', '2026-10-13T12:00:00Z'), item('old-pending', 'pending'), item('running', 'running', '2026-10-13T11:00:00Z')];
  await mockService(page, () => rows); await page.goto('/');
  const active = page.getByRole('list', { name: 'Active transcriptions' }).getByRole('listitem');
  await expect(active).toHaveCount(3);
  expect(await active.allTextContents()).toEqual([expect.stringContaining('running.wav'), expect.stringContaining('old-pending.wav'), expect.stringContaining('new-pending.wav')]);
});

test('Library lists only finished items, newest completion first', async ({ page }) => {
  const empty = item('empty', 'succeeded'); empty.audio.sizeBytes = 0;
  const rows = [item('older', 'succeeded'), item('latest', 'succeeded', '2026-10-12T10:00:00Z', '2026-10-14T11:00:00Z'), item('pending', 'pending'), item('running', 'running'), item('failed', 'failed'), item('canceled', 'canceled'), empty];
  await mockService(page, () => rows); await page.goto('/');
  const query = page.waitForRequest(r => r.url().includes('library=true'));
  await page.getByRole('button', { name: 'Library', exact: true }).click(); await query;
  const library = page.getByRole('list', { name: 'Transcriptions', exact: true }).getByRole('listitem');
  await expect(library).toHaveCount(2);
  expect(await library.allTextContents()).toEqual([expect.stringContaining('latest.wav'), expect.stringContaining('older.wav')]);
  for (const name of ['pending', 'running', 'failed', 'canceled', 'empty']) await expect(page.getByText(`${name}.wav`, { exact: true })).toHaveCount(0);
  await expect(page.getByRole('radio', { name: 'Failed', exact: true })).toHaveCount(0);
});

// Unfinished items stay in the queue. A double-click opens their detail view.
for (const status of ['pending', 'running', 'failed', 'canceled'] as const) {
  test(`Stored ${status} audio stays downloadable from the queue while transcript export is unavailable`, async ({ page }) => {
    const rows = [item(`source-${status}`, status)]; await mockService(page, () => rows); await page.goto('/');
    await page.getByText(`source-${status}.wav`, { exact: true }).dblclick();
    await expect(page.getByRole('button', { name: 'Download audio', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Save as…', exact: true })).toHaveCount(0);
  });
}

test('A later visit to the Library does not reopen the item from "View in Library"', async ({ page }) => {
  const rows = [item('failed-once', 'failed'), item('done')]; await mockService(page, () => rows); await page.goto('/');
  await page.getByText('failed-once.wav', { exact: true }).dblclick();
  await expect(page.getByRole('button', { name: 'Download audio', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Transcribe', exact: true }).click();
  await page.getByRole('button', { name: 'Library', exact: true }).click();
  await expect(page.getByText('done.wav', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Download audio', exact: true })).toHaveCount(0);
});

test('Queue keeps failed rows after Clear completed', async ({ page }) => {
  const rows = [item('done'), item('failed-one', 'failed')]; await mockService(page, () => rows); await page.goto('/');
  await page.getByRole('button', { name: 'Clear completed', exact: true }).click();
  await expect(page.getByText('done.wav', { exact: true })).toHaveCount(0);
  await expect(page.getByText('failed-one.wav', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Clear completed', exact: true })).toHaveCount(0);
});

test('Open detail updates when a polled transcription completes', async ({ page }) => {
  let rows = [item('live', 'running')]; await mockService(page, () => rows); await page.goto('/');
  await page.getByText('live.wav', { exact: true }).dblclick();
  await expect(page.getByRole('button', { name: 'Save as…', exact: true })).toHaveCount(0);
  rows = [item('live')];
  await expect(page.getByRole('button', { name: 'Save as…', exact: true })).toBeVisible({ timeout: 12000 });
});

test('Real source and transcript downloads stream and distinct URLs launch promptly', async ({ page, request }) => {
  test.skip(!process.env.HELIOS_WEB_API_URL, 'Requires the isolated REST validation service with successful audio uploads.');
  const response = await request.get(`${process.env.HELIOS_WEB_API_URL}/api/v1/transcriptions?library=true&status=succeeded&limit=1`);
  expect(response.ok()).toBeTruthy(); const { items } = await response.json(); expect(items).toHaveLength(1);
  await page.goto('/'); await page.getByRole('button', { name: 'Library', exact: true }).click();
  await page.getByText(items[0].audio.filename, { exact: true }).dblclick();
  const audio = page.getByRole('button', { name: 'Download audio', exact: true });
  const downloads: string[] = []; page.on('download', download => downloads.push(download.suggestedFilename()));
  await audio.dblclick(); await expect(page.getByRole('button', { name: 'Download requested…', exact: true })).toBeDisabled();
  await page.getByRole('button', { name: 'Save as…', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Download', exact: true }).click();
  await expect.poll(() => downloads.length).toBe(2);
  expect(downloads.some(name => name.endsWith('.wav'))).toBeTruthy(); expect(downloads.some(name => name.endsWith('.txt'))).toBeTruthy();
  await expect(page.getByRole('navigation', { name: 'Primary' })).toBeVisible();
});

test('Live detail still updates when search hides its row', async ({ page }) => {
  let rows = [item('hidden-live', 'running')]; await mockService(page, () => rows); await page.goto('/');
  await page.getByText('hidden-live.wav', { exact: true }).dblclick();
  await page.getByRole('textbox', { name: /Search/ }).fill('does-not-match');
  rows = [item('hidden-live')];
  await expect(page.getByRole('button', { name: 'Save as…', exact: true })).toBeVisible({ timeout: 12000 });
});

test('Real paginated Library has globally ordered completions and excludes empty sources', async ({ page, request }) => {
  test.skip(!process.env.HELIOS_WEB_API_URL, 'Requires the isolated seeded validation backend.');
  const base = `${process.env.HELIOS_WEB_API_URL}/api/v1/transcriptions?library=true&status=succeeded&limit=40`;
  const firstResponse = await request.get(base); expect(firstResponse.ok()).toBeTruthy();
  const first = await firstResponse.json(); expect(first.items).toHaveLength(40); expect(first.nextCursor).toBeTruthy();
  expect(first.items[0].audio.filename).toBe('Latest completion from old upload.wav');
  const second = await (await request.get(`${base}&cursor=${encodeURIComponent(first.nextCursor)}`)).json();
  expect(second.items).toHaveLength(5);
  const all = [...first.items, ...second.items]; expect(new Set(all.map(row => row.id)).size).toBe(45);
  expect(all.every(row => row.audio.sizeBytes > 0)).toBeTruthy();
  await page.goto('/'); await page.getByRole('button', { name: 'Library', exact: true }).click();
  const list = page.getByRole('list', { name: 'Transcriptions', exact: true }).getByRole('listitem');
  await expect(list).toHaveCount(40); await page.getByRole('button', { name: 'Load more', exact: true }).click();
  await expect(list).toHaveCount(45); await expect(page.getByText('empty upload.wav', { exact: true })).toHaveCount(0);
});

test('Real canceled upload can download its stored audio without a transcript', async ({ page, request }) => {
  test.skip(!process.env.HELIOS_WEB_API_URL, 'Requires the isolated validation backend with canceled source audio.');
  const response = await request.get(`${process.env.HELIOS_WEB_API_URL}/api/v1/transcriptions?status=canceled&limit=1`);
  expect(response.ok()).toBeTruthy(); const { items } = await response.json(); expect(items).toHaveLength(1);
  await page.goto('/');
  await page.getByText(items[0].audio.filename, { exact: true }).dblclick();
  await expect(page.getByRole('button', { name: 'Save as…', exact: true })).toHaveCount(0);
  const downloaded = page.waitForEvent('download'); await page.getByRole('button', { name: 'Download audio', exact: true }).click();
  const audio = await downloaded; expect(audio.suggestedFilename()).toMatch(/\.wav$/); expect(await audio.failure()).toBeNull();
});
