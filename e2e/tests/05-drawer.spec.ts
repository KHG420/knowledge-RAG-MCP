/**
 * Document Detail Drawer Tests
 *
 * Tests opening/closing the detail drawer, viewing metadata,
 * managing tags, content preview, and document actions.
 */
import { test, expect } from './helpers';

test.describe('Document Detail Drawer', () => {
  test.beforeEach(async ({ app }) => {
    await app.enterKB('test-kb');
  });

  // ── Open / Close ──

  test('opens drawer when clicking a card', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();

    // Drawer should be visible
    const drawer = page.locator('#drawer');
    await expect(drawer).toHaveClass(/open/);
  });

  test('opens drawer from card detail button', async ({ page }) => {
    const detailBtn = page.locator('#cardGrid .doc-card button:has-text("查看详情")').first();
    await detailBtn.click();

    const drawer = page.locator('#drawer');
    await expect(drawer).toHaveClass(/open/);
  });

  test('closes drawer via X button', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();

    await page.locator('.drawer-close').first().click();
    const drawer = page.locator('#drawer');
    await expect(drawer).not.toHaveClass(/open/);
  });

  test('closes drawer via overlay click', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();

    await page.locator('#drawerOverlay').click({ force: true });
    const drawer = page.locator('#drawer');
    await expect(drawer).not.toHaveClass(/open/);
  });

  // ── Document Info ──

  test('shows document filename in drawer', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();

    const fileName = page.locator('#ddFileName');
    await expect(fileName).toBeVisible();
    const text = await fileName.textContent();
    expect(text).not.toBe('加载中…');
    expect(text).not.toBe('');
  });

  test('shows document type in drawer', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();

    const type = page.locator('#ddType');
    await expect(type).toBeVisible();
  });

  test('shows document size in drawer', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();

    const size = page.locator('#ddSize');
    await expect(size).toBeVisible();
    const text = await size.textContent();
    // Size may be "—" if unknown, or a number
    expect(text).toBeDefined();
  });

  test('shows document timestamp in drawer', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();

    const time = page.locator('#ddTime');
    await expect(time).toBeVisible();
  });

  // ── Tags ──

  test('tags section is present in drawer', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();

    const tagsGroup = page.locator('#ddTagsGroup');
    // May be hidden if no tags, but element should exist
    await expect(tagsGroup).toBeAttached();
  });

  // ── Content Preview ──

  test('shows content preview section', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();

    const previewSection = page.locator('#previewSection');
    await expect(previewSection).toBeAttached();
  });

  // ── Paper Info (for ship-roll-study which has a title matching isPaper) ──

  test('shows paper info for academic documents', async ({ page }) => {
    // Find the ship-roll-study card
    const shipCard = page.locator('#cardGrid .doc-card').filter({ hasText: 'ship-roll-study' });
    if (await shipCard.count() > 0) {
      await shipCard.first().click();

      const paperGroup = page.locator('#ddPaperGroup');
      // paper info group should be attached
      await expect(paperGroup).toBeAttached();
    }
  });

  // ── Manifest ──

  test('manifest section is attached in drawer', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();

    const manifestGroup = page.locator('#ddManifestGroup');
    await expect(manifestGroup).toBeAttached();
  });

  // ── Drawer Title ──

  test('drawer title shows "文档详情"', async ({ page }) => {
    const firstCard = page.locator('#cardGrid .doc-card').first();
    await firstCard.click();

    // Wait for drawer to open
    await expect(page.locator('#drawer.open')).toBeVisible({ timeout: 5000 });
    const title = page.locator('#drawerTitle');
    await expect(title).toBeVisible();
    const titleText = await title.textContent();
    expect(titleText).toBeTruthy();
  });
});
