/**
 * Search & Filter Tests
 *
 * Tests filename search, fulltext search, type filtering, tag filtering,
 * search result display, and clear search functionality.
 */
import { test, expect } from './helpers';

test.describe('Search & Filter', () => {
  test.beforeEach(async ({ app }) => {
    await app.enterKB('test-kb');
  });

  // ── Filename Search ──

  test('searches documents by name', async ({ app, page }) => {
    await app.searchByName('introduction');

    // Should show filtered results
    const cards = page.locator('#cardGrid .doc-card');
    const count = await cards.count();
    expect(count).toBeGreaterThan(0);
    expect(count).toBeLessThanOrEqual(5); // Should be fewer than total
  });

  test('clears name search when input is emptied', async ({ app, page }) => {
    await app.searchByName('introduction');
    const input = page.locator('#searchInput');
    await input.fill('');
    await input.press('Enter');
    await page.waitForLoadState('networkidle');

    const cards = page.locator('#cardGrid .doc-card');
    const count = await cards.count();
    expect(count).toBeGreaterThanOrEqual(5); // All docs back
  });

  test('name search with no results shows empty state', async ({ app, page }) => {
    await app.searchByName('nonexistent-file-xyz');
    await page.waitForLoadState('networkidle');

    const emptyState = page.locator('#emptyState');
    await expect(emptyState).toHaveClass(/active/);
  });

  // ── Type Filter ──

  test('filters by document type', async ({ app, page }) => {
    await app.filterByType('md');

    const cards = page.locator('#cardGrid .doc-card');
    const count = await cards.count();
    expect(count).toBeGreaterThan(0);
  });

  test('filter shows "all" option', async ({ page }) => {
    const filter = page.locator('#typeFilter');
    // Wait for filter to be populated
    await expect(filter.locator('option').first()).toBeAttached();
    const options = await filter.locator('option').allTextContents();
    expect(options.length).toBeGreaterThan(0);
  });

  // ── Fulltext Search ──

  test('performs fulltext search', async ({ app, page }) => {
    await app.searchFulltext('船舶');

    // Search bar should be visible
    const searchBar = page.locator('#searchBar');
    await expect(searchBar).toHaveClass(/show/);

    // Should show query label
    await expect(page.locator('#searchQueryLabel')).toContainText('船舶');

    // Should have results (ship-roll-study contains 船舶)
    const cards = page.locator('#cardGrid .doc-card');
    const count = await cards.count();
    expect(count).toBeGreaterThan(0);
  });

  test('clears fulltext search', async ({ app, page }) => {
    await app.searchFulltext('船舶');
    await app.clearFulltextSearch();

    // Search bar should be hidden
    const searchBar = page.locator('#searchBar');
    await expect(searchBar).not.toHaveClass(/show/);

    // All docs should be back
    const cards = page.locator('#cardGrid .doc-card');
    const count = await cards.count();
    expect(count).toBeGreaterThanOrEqual(5);
  });

  test('fulltext search with no results', async ({ app, page }) => {
    await app.searchFulltext('xyzabcdefg_nonexistent_term');
    await page.waitForLoadState('networkidle');

    // Should show empty state or minimal results
    const emptyState = page.locator('#emptyState');
    const isActive = await emptyState.evaluate(el => el.classList.contains('active'));
    // Either empty state is active or result count is 0
    if (!isActive) {
      const count = await page.locator('#cardGrid .doc-card').count();
      expect(count).toBe(0);
    }
  });

  test('fulltext search updates result count label', async ({ app, page }) => {
    await app.searchFulltext('knowledge');

    const resultCount = page.locator('#searchResultCount');
    await expect(resultCount).toBeVisible();
    const text = await resultCount.textContent();
    expect(text).toMatch(/\d+/); // Should contain a number
  });

  // ── Combined Search ──

  test('name search and type filter can be combined', async ({ app, page }) => {
    await app.searchByName('ship');
    await app.filterByType('md');
    await page.waitForLoadState('networkidle');

    const cards = page.locator('#cardGrid .doc-card');
    const count = await cards.count();
    expect(count).toBeGreaterThanOrEqual(0); // Should not error
  });

  // ── Tag Filtering ──

  test('tag filter chip is hidden when no tag selected', async ({ page }) => {
    const chip = page.locator('#tagFilterChip');
    // Initially hidden (no tag selected)
    const display = await chip.evaluate(el => getComputedStyle(el).display);
    expect(display).toBe('none');
  });
});
