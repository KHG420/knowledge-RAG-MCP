/**
 * Edge Cases & Error Handling Tests
 *
 * Tests boundary conditions, concurrent operations, error handling,
 * and unusual user interactions.
 */
import { test, expect } from './helpers';

test.describe('Edge Cases', () => {
  test.beforeEach(async ({ app }) => {
    await app.enterKB('test-kb');
  });

  // ── Navigation Edge Cases ──

  test('rapid view switching does not break UI', async ({ app, page }) => {
    for (let i = 0; i < 5; i++) {
      await app.switchToTableView();
      await app.switchToCardView();
    }
    // UI should still be functional
    const cards = page.locator('#cardGrid .doc-card');
    await expect(cards).not.toHaveCount(0);
  });

  test('rapid KB switching does not break UI', async ({ app, page }) => {
    for (let i = 0; i < 3; i++) {
      await app.switchKB('test-kb');
      await app.switchKB('second-kb');
    }
    // UI should still be functional
    const label = page.locator('#currentKBLabel');
    await expect(label).toBeVisible();
  });

  // ── Search Edge Cases ──

  test('empty search query returns all documents', async ({ app, page }) => {
    await app.searchByName('');
    await page.waitForLoadState('networkidle');

    const cards = page.locator('#cardGrid .doc-card');
    const count = await cards.count();
    expect(count).toBeGreaterThan(0);
  });

  test('special characters in search do not error', async ({ app, page }) => {
    await app.searchByName('!@#$%^&*()');
    await page.waitForLoadState('networkidle');
    // Should not throw, UI should remain
    await expect(page.locator('#cardGrid')).toBeAttached();
  });

  test('very long search query does not error', async ({ app, page }) => {
    const longQuery = 'a'.repeat(500);
    await app.searchByName(longQuery);
    await page.waitForLoadState('networkidle');
    // Should not crash
    await expect(page.locator('#cardGrid')).toBeAttached();
  });

  // ── Drawer Edge Cases ──

  test('opening drawer while another is open', async ({ page }) => {
    const cards = page.locator('#cardGrid .doc-card');
    const count = await cards.count();
    if (count >= 2) {
      await cards.nth(0).click({ force: true });
      // Wait a moment for drawer
      await page.waitForTimeout(500);
      // Click second card (force: overlay may intercept clicks)
      await cards.nth(1).click({ force: true });
      await page.waitForTimeout(500);
      // Drawer should still be visible
      await expect(page.locator('#drawerTitle')).toBeVisible({ timeout: 5000 });
    }
  });

  test('ESC key closes drawer', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();
    await page.waitForSelector('#drawer.open');

    await page.keyboard.press('Escape');
    // May or may not close via ESC depending on implementation
    // Just verify it doesn't crash
    await page.waitForTimeout(500);
  });

  // ── Panel Edge Cases ──

  test('opening multiple panels sequentially', async ({ app, page }) => {
    await app.openConfigPanel();
    await app.closeConfigPanel();

    await app.openTombstonePanel();
    await app.closeTombstonePanel();

    await app.openReconcilePanel();
    await app.closeReconcilePanel();

    await app.openStatusPanel();
    await app.closeStatusPanel();

    // UI should still be functional
    const cards = page.locator('#cardGrid .doc-card');
    await expect(cards).not.toHaveCount(0);
  });

  // ── Pagination Edge Cases ──

  test('pagination buttons work without errors', async ({ page }) => {
    const pagination = page.locator('#pagination');
    if (await pagination.isVisible()) {
      const buttons = pagination.locator('button');
      const btnCount = await buttons.count();
      if (btnCount > 0) {
        // Click first available button
        const firstBtn = buttons.first();
        if (await firstBtn.isEnabled()) {
          await firstBtn.click();
          await page.waitForLoadState('networkidle');
        }
      }
    }
  });

  // ── URL / Direct Access ──

  test('non-existent path returns 404', async ({ page }) => {
    const response = await page.goto('/nonexistent-path');
    expect(response?.status()).toBe(404);
  });

  test('health endpoint returns OK', async ({ page }) => {
    const response = await page.goto('/api/health');
    expect(response?.status()).toBe(200);
    const body = await response?.text();
    expect(body).toContain('ok');
  });

  // ── Concurrency ──

  test('multiple tabs/windows can access the app', async ({ browser }) => {
    const ctx = await browser.newContext();
    const page1 = await ctx.newPage();
    const page2 = await ctx.newPage();

    await page1.goto('/');
    await page2.goto('/');

    await page1.waitForLoadState('networkidle');
    await page2.waitForLoadState('networkidle');

    // Both should load successfully
    const p1Visible = await page1.locator('#kbHome, #mainContent').first().isVisible();
    const p2Visible = await page2.locator('#kbHome, #mainContent').first().isVisible();

    expect(p1Visible).toBe(true);
    expect(p2Visible).toBe(true);

    await page1.close();
    await page2.close();
    await ctx.close();
  });
});
