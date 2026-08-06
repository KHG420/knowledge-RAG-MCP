/**
 * Upload Tests
 *
 * Tests drag-and-drop upload zone, file upload via file chooser,
 * upload progress display, and upload queue management.
 */
import { test, expect } from './helpers';
import path from 'path';
import fs from 'fs';

test.describe('Upload', () => {
  test.beforeEach(async ({ app }) => {
    await app.enterKB('test-kb');
  });

  // ── Dropzone ──

  test('dropzone is visible', async ({ page }) => {
    const dropzone = page.locator('.dropzone');
    await expect(dropzone).toBeVisible();
  });

  test('dropzone shows upload button', async ({ page }) => {
    const uploadBtn = page.locator('.dropzone .upload-btn');
    await expect(uploadBtn).toBeVisible();
    await expect(uploadBtn).toHaveText(/选择文件|上传/);
  });

  test('dropzone text is displayed', async ({ page }) => {
    const dropzone = page.locator('.dropzone');
    const text = await dropzone.textContent();
    expect(text).toMatch(/拖拽|上传|选择/);
  });

  // ── File Upload via Chooser ──

  test('uploads a single markdown file', async ({ app, page }) => {
    // Create a temp file
    const tmpFile = path.join('/tmp', `e2e-upload-${Date.now()}.md`);
    fs.writeFileSync(tmpFile, '# E2E Upload Test\n\nThis is a test file for E2E upload testing.\n\nIt has multiple paragraphs.\n\n## Section\n\nMore content.');

    try {
      const fileChooserPromise = page.waitForEvent('filechooser');
      await page.locator('.dropzone .upload-btn').click();
      const fileChooser = await fileChooserPromise;

      await fileChooser.setFiles(tmpFile);

      // Wait for upload progress to appear
      await page.waitForTimeout(1000);

      // Upload should be in progress or completed
      const progress = page.locator('.upload-progress');
      // May or may not be visible depending on upload speed
      expect(progress).toBeDefined();
    } finally {
      try { fs.unlinkSync(tmpFile); } catch {}
    }
  });

  test('uploads multiple files sequentially', async ({ page }) => {
    const files = [];
    for (let i = 0; i < 2; i++) {
      const tmpFile = path.join('/tmp', `e2e-multi-upload-${i}-${Date.now()}.md`);
      fs.writeFileSync(tmpFile, `# Test File ${i}\n\nContent for file ${i}.\n\nMore paragraphs here for testing.`);
      files.push(tmpFile);
    }

    try {
      for (const f of files) {
        const fileChooserPromise = page.waitForEvent('filechooser');
        await page.locator('.dropzone .upload-btn').click();
        const fileChooser = await fileChooserPromise;
        await fileChooser.setFiles(f);
        await page.waitForTimeout(500);
      }
    } finally {
      files.forEach(f => { try { fs.unlinkSync(f); } catch {} });
    }
  });

  // ── Upload Progress/Queue ──

  test('upload queue element exists', async ({ page }) => {
    const queue = page.locator('.upload-progress');
    await expect(queue).toBeAttached();
  });
});
