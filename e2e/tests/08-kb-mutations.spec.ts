/**
 * KB Mutation Tests — must run LAST as they modify the shared mock backend state.
 * These tests use Playwright dialog handling for prompt/confirm native dialogs.
 */
import { test, expect } from './helpers';

test.describe('KB Mutations (runs last)', () => {
  test.skip('creates a new KB via prompt dialog', async ({ app, page }) => {
    // Accept the upcoming prompt dialog with the KB name
    page.on('dialog', async dialog => {
      await dialog.accept('e2e-new-kb');
    });

    await app.goHome();
    await app.clickCreateKB().click();

    // Wait for creation
    await page.waitForLoadState('networkidle');

    // Clean up the dialog listener
    page.removeAllListeners('dialog');

    // New KB should appear in the list
    const items = app.getKBListItems();
    const text = await items.allTextContents();
    expect(text.some(t => t.includes('e2e-new-kb'))).toBe(true);
  });

  test.skip('deletes a KB via confirm dialog', async ({ app, page }) => {
    // Accept all dialogs
    page.on('dialog', async dialog => {
      if (dialog.type() === 'prompt') {
        await dialog.accept('e2e-del-kb');
      } else {
        await dialog.accept();
      }
    });

    await app.goHome();
    await app.clickCreateKB().click();
    await page.waitForLoadState('networkidle');

    // Click delete on the last KB
    const deleteBtn = page.locator('#kbHomeList .kb-del-btn').last();
    await deleteBtn.click();
    await page.waitForLoadState('networkidle');

    page.removeAllListeners('dialog');

    // Verify original KBs still exist
    const items = app.getKBListItems();
    const text = await items.allTextContents();
    expect(text.some(t => t.includes('test-kb'))).toBe(true);
  });
});
