/**
 * KB Home & Management Tests
 *
 * Tests the knowledge base home page, KB creation, deletion, switching,
 * and navigation between KB home and document view.
 */
import { test, expect } from './helpers';

// ── KB Home Page ───────────────────────────────────────────────────────────────

test.describe('KB Home Page', () => {
  test('displays KB home on first load', async ({ app, page }) => {
    // Reset localStorage so we land on KB home
    await page.evaluate(() => localStorage.removeItem('kb_lastKB'));
    await page.reload();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('#kbHome')).toBeVisible();
    await expect(page.locator('#kbHomeList .kb-list-item')).not.toHaveCount(0);
  });

  test('lists all available knowledge bases', async ({ app, page }) => {
    await app.goHome();
    const items = app.getKBListItems();
    await expect(items).not.toHaveCount(0);
    // Should have at least test-kb and second-kb
    const text = await items.allTextContents();
    expect(text.some(t => t.includes('test-kb'))).toBe(true);
  });

  test('has create KB button', async ({ app }) => {
    await app.goHome();
    const btn = app.clickCreateKB();
    await expect(btn).toBeVisible();
  });

  test('each KB has a delete button', async ({ app, page }) => {
    await app.goHome();
    const deleteBtn = page.locator('#kbHomeList .kb-del-btn').first();
    await expect(deleteBtn).toBeVisible();
  });
});

// ── Entering & Switching KBs ───────────────────────────────────────────────────

test.describe('KB Navigation', () => {
  test('enters a KB and shows document list', async ({ app, page }) => {
    await app.goHome();
    await app.enterKB('test-kb');

    // Should show main content
    await expect(page.locator('#mainContent')).toBeVisible();
    // Should show stats
    await expect(page.locator('#statTotal')).toBeVisible();
    // Should have documents
    const count = await app.getCardCount();
    expect(count).toBeGreaterThan(0);
  });

  test('KB label shows current KB name', async ({ app, page }) => {
    await app.enterKB('test-kb');
    const label = page.locator('#currentKBLabel');
    await expect(label).toHaveText('test-kb');
  });

  test('switches KB via dropdown', async ({ app, page }) => {
    await app.enterKB('test-kb');
    await app.switchKB('second-kb');

    // Wait for reload
    await page.waitForLoadState('networkidle');
    const label = page.locator('#currentKBLabel');
    await expect(label).toHaveText('second-kb');
  });

  test('goes home and back', async ({ app, page }) => {
    await app.enterKB('test-kb');
    await app.goHome();
    await expect(page.locator('#kbHome')).toBeVisible();

    await app.enterKB('test-kb');
    await expect(page.locator('#mainContent')).toBeVisible();
  });
});

// ── KB Creation & Deletion tests moved to 08-kb-mutations.spec.ts ──
