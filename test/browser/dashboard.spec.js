const { test, expect } = require('@playwright/test');
const fs = require('node:fs');

test('approval dashboard, search, drafts, handoff, errors and responsive layout', async ({ page, request }) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  for (const [index, name] of ['strawhat-ci-bot', 'release-notifier', 'security-scanner'].entries()) {
    const res = await request.post('/api/ingest', {
      headers: { Authorization: 'Bearer browser-test-only' },
      data: {
        owner_type: 'org', owner: 'StrawHat-Enterprise', secret_storage: 'vault', requester: 'BK3820',
        source: { type: 'issue', repo: 'StrawHat-Enterprise/github-app-factory', issue_number: index + 1,
          url: `https://github.com/StrawHat-Enterprise/github-app-factory/issues/${index + 1}` },
        manifest: { name, description: ['Review pull requests with tightly scoped repository access.',
          'Keep release operations informed with a dedicated GitHub App.',
          'Automate repository checks while keeping credentials in your vault.'][index],
          url: 'https://github.com/StrawHat-Enterprise/github-app-factory',
          default_permissions: { contents: 'read', pull_requests: 'write' }, default_events: [] },
      },
    });
    expect(res.status()).toBe(201);
  }
  await page.goto('/');
  await expect(page.locator('.card')).toHaveCount(3);
  await expect(page.locator('#stat-total')).toHaveText('3');
  if (process.env.CAPTURE_UI === 'true') {
    fs.mkdirSync('docs/images', { recursive: true });
    await page.screenshot({ path: 'docs/images/dashboard.png', fullPage: true });
  }
  const search = page.getByRole('searchbox', { name: 'Search requests' });
  await search.fill('strawhat-ci');
  await expect(page.locator('.card')).toHaveCount(1);
  const card = page.getByRole('article', { name: 'strawhat-ci-bot request' });
  await card.getByRole('radio', { name: /ZIP outbox/ }).check();
  await card.getByRole('textbox', { name: /Delivery email/ }).fill('app-team@example.com');
  await page.getByRole('button', { name: 'Refresh queue' }).click();
  await expect(card.getByRole('textbox', { name: /Delivery email/ })).toHaveValue('app-team@example.com');
  await expect(card.getByRole('radio', { name: /ZIP outbox/ })).toBeChecked();
  await card.getByText('View manifest', { exact: true }).click();
  await expect(card.locator('pre')).toBeVisible();
  await card.getByRole('button', { name: 'Simulate create (demo)' }).click();
  await expect(card.getByText(/ZIP prepared for/)).toBeVisible();
  await expect(card.getByText(/not emailed/)).toBeVisible();
  await expect(page.locator('#stat-created')).toHaveText('1');
  const zip = await request.get(await card.getByRole('link', { name: 'download zip' }).getAttribute('href'));
  expect(zip.status()).toBe(200);
  expect((await zip.body()).subarray(0, 2).toString()).toBe('PK');
  if (process.env.CAPTURE_UI === 'true') {
    await expect(page.locator('#toast')).toBeHidden();
    await page.screenshot({ path: 'docs/images/credential-handoff.png', fullPage: true });
  }
  await search.fill('no-such-app');
  await expect(page.getByText('No matching requests')).toBeVisible();
  await search.fill('');
  await page.getByRole('button', { name: 'All requests', exact: true }).click();
  await expect(page.locator('.card')).toHaveCount(3);
  await page.getByRole('combobox', { name: 'Sort requests' }).selectOption('name');
  await expect(page.locator('.card').first()).toHaveAttribute('aria-label', 'release-notifier request');
  page.once('dialog', dialog => dialog.accept('Request no longer needed'));
  await page.getByRole('article', { name: 'security-scanner request' }).getByRole('button', { name: 'Reject', exact: true }).click();
  await page.getByRole('button', { name: 'Rejected', exact: true }).click();
  await expect(page.locator('.card')).toHaveCount(1);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole('heading', { name: 'Your app approval workspace.' })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  if (process.env.CAPTURE_UI === 'true') {
    await expect(page.locator('#toast')).toBeHidden();
    await page.screenshot({ path: 'docs/images/mobile.png', fullPage: true });
  }
  await page.route('**/api/requests', route => route.fulfill({
    status: 503, contentType: 'application/json', body: '{"error":"Service unavailable"}',
  }));
  await page.getByRole('button', { name: 'Refresh queue' }).click();
  await expect(page.getByRole('alert')).toContainText('Service unavailable');
  expect(errors).toEqual([]);
});
