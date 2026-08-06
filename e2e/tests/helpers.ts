/**
 * E2E Test Helpers — Page Object helpers for the Knowledge Base Manager SPA.
 *
 * All selectors reference the single-page app at internal/knowledge/manage/ui/index.html.
 */
import { Page, Locator, expect } from '@playwright/test';

// ── Selectors ──────────────────────────────────────────────────────────────────

export const SEL = {
  // Layout
  kbHome: '#kbHome',
  header: '#header',
  mainContent: '#mainContent',
  currentKBLabel: '#currentKBLabel',

  // KB Home
  kbHomeList: '#kbHomeList',
  kbCreateBtn: 'button:has-text("创建新知识库")',
  kbSelect: '#kbSelect',

  // Stats
  statTotal: '#statTotal',
  statChunks: '#statChunks',
  statPapers: '#statPapers',
  statTypes: '#statTypes',
  statVectors: '#statVectors',

  // Model info
  testBtnEmbedder: '#testBtnEmbedder',
  testBtnReranker: '#testBtnReranker',
  testBtnDocParser: '#testBtnDocParser',

  // Upload
  dropzone: '.dropzone',
  fileInput: '#fileInput',
  uploadProgress: '.upload-progress',

  // Toolbar
  searchInput: '#searchInput',
  fulltextInput: '#fulltextInput',
  typeFilter: '#typeFilter',
  searchBar: '#searchBar',
  searchQueryLabel: '#searchQueryLabel',
  searchResultCount: '#searchResultCount',

  // View toggle
  viewCard: '#viewCard',
  viewTable: '#viewTable',

  // Content
  cardGrid: '#cardGrid',
  tableView: '#tableView',
  tableBody: '#tableBody',
  emptyState: '#emptyState',
  pagination: '#pagination',
  resultCount: '#resultCount',
  loadingDocs: '#loadingDocs',

  // Drawer
  drawer: '#drawer',
  drawerOverlay: '#drawerOverlay',
  drawerTitle: '#drawerTitle',
  ddFileName: '#ddFileName',
  ddType: '#ddType',
  ddSize: '#ddSize',
  ddTime: '#ddTime',
  ddTagsGroup: '#ddTagsGroup',
  ddPaperGroup: '#ddPaperGroup',
  ddManifestGroup: '#ddManifestGroup',
  previewSection: '#previewSection',

  // Confirm delete
  confirmOverlay: '#confirmOverlay',
  confirmBtn: '#confirmBtn',

  // Panels
  configOverlay: '#configOverlay',
  searchDebugOverlay: '#searchDebugOverlay',
  statusOverlay: '#statusOverlay',
  tombstoneOverlay: '#tombstoneOverlay',
  reconcileOverlay: '#reconcileOverlay',
  vectorIndexOverlay: '#vectorIndexOverlay',
  toolDescsOverlay: '#toolDescsOverlay',

  // Toast
  toast: '#toast',
} as const;

// ── Page Object ────────────────────────────────────────────────────────────────

export class KBManagerPage {
  constructor(public page: Page) {}

  // ── Navigation ──

  async goto() {
    await this.page.goto('/');
    await this.page.waitForLoadState('networkidle');
  }

  async waitForApp() {
    // Wait for either KB home or main content to be visible
    await expect(
      this.page.locator(`${SEL.kbHome}, ${SEL.mainContent}`).first()
    ).toBeVisible({ timeout: 10000 });
  }

  // ── KB Home ──

  async isKBHomeVisible() {
    return this.page.locator(SEL.kbHome).isVisible();
  }

  getKBListItems() {
    return this.page.locator('#kbHomeList .kb-list-item');
  }

  async enterKB(name: string) {
    // If we're not on the KB home page, go home first
    if (!(await this.page.locator(SEL.kbHome).isVisible())) {
      await this.goHome();
    }
    await this.page.locator(`#kbHomeList .kb-list-item:has-text("${name}")`).click();
    await this.page.waitForSelector(SEL.mainContent, { state: 'visible' });
    // Wait for document list to finish loading
    await this.page.waitForLoadState('networkidle');
  }

  clickCreateKB() {
    return this.page.locator(SEL.kbCreateBtn);
  }

  async goHome() {
    // Already on home page — nothing to do
    if (await this.page.locator(SEL.kbHome).isVisible()) return;
    await this.page.locator('button:has-text("主页")').click();
    await this.page.waitForSelector(SEL.kbHome, { state: 'visible' });
  }

  async switchKB(name: string) {
    await this.page.locator(SEL.kbSelect).selectOption(name);
    await this.page.waitForLoadState('networkidle');
  }

  // ── Documents ──

  getCardCount() {
    return this.page.locator(`${SEL.cardGrid} .doc-card`).count();
  }

  getTableRowCount() {
    return this.page.locator(`${SEL.tableBody} tr`).count();
  }

  async clickCardBySlug(slug: string) {
    await this.page.locator(`${SEL.cardGrid} .doc-card`).filter({ hasText: slug }).click();
  }

  async switchToCardView() {
    await this.page.locator(SEL.viewCard).click();
    await this.page.waitForSelector(`${SEL.cardGrid}.active`);
  }

  async switchToTableView() {
    await this.page.locator(SEL.viewTable).click();
    await this.page.waitForSelector(`${SEL.tableView}.active`);
  }

  // ── Search ──

  async searchByName(query: string) {
    const input = this.page.locator(SEL.searchInput);
    await input.fill(query);
    // Trigger input event (the app listens to input changes)
    await input.press('Enter');
    await this.page.waitForLoadState('networkidle');
  }

  async searchFulltext(query: string) {
    const input = this.page.locator(SEL.fulltextInput);
    await input.fill(query);
    await input.press('Enter');
    await this.page.waitForLoadState('networkidle');
  }

  async clearFulltextSearch() {
    await this.page.locator(`${SEL.searchBar} .clear-btn`).click();
    await this.page.waitForLoadState('networkidle');
  }

  async filterByType(type: string) {
    await this.page.locator(SEL.typeFilter).selectOption(type);
    await this.page.waitForLoadState('networkidle');
  }

  // ── Upload ──

  async uploadFile(filePath: string) {
    const fileChooserPromise = this.page.waitForEvent('filechooser');
    await this.page.locator('.dropzone .upload-btn').click();
    const fileChooser = await fileChooserPromise;
    await fileChooser.setFiles(filePath);
  }

  // ── Drawer ──

  isDrawerOpen() {
    return this.page.locator(SEL.drawer).evaluate(el => el.classList.contains('open'));
  }

  async closeDrawer() {
    await this.page.locator('.drawer-close').first().click();
    await this.page.waitForSelector(`${SEL.drawer}:not(.open)`, { timeout: 5000 }).catch(() => {});
  }

  async clickDrawerOverlay() {
    await this.page.locator(SEL.drawerOverlay).click();
  }

  // ── Delete ──

  async confirmDelete() {
    await this.page.locator(SEL.confirmBtn).click();
    await this.page.waitForLoadState('networkidle');
  }

  async cancelDelete() {
    await this.page.locator(`${SEL.confirmOverlay} button:has-text("取消")`).click();
  }

  // ── Panels ──

  async openConfigPanel() {
    await this.page.locator('button:has-text("配置")').first().click();
    await this.page.waitForSelector(`${SEL.configOverlay}.show`);
  }

  async closeConfigPanel() {
    await this.page.locator(`${SEL.configOverlay} .drawer-close`).click();
  }

  async openSearchDebugPanel() {
    await this.page.locator('button:has-text("搜索调试")').first().click();
    await this.page.waitForSelector(`${SEL.searchDebugOverlay}.show`);
  }

  async closeSearchDebugPanel() {
    await this.page.locator(`${SEL.searchDebugOverlay} .drawer-close`).click();
  }

  async openStatusPanel() {
    await this.page.locator('button:has-text("状态")').first().click();
    await this.page.waitForSelector(`${SEL.statusOverlay}.show`);
  }

  async closeStatusPanel() {
    await this.page.locator(`${SEL.statusOverlay} .drawer-close`).click();
  }

  async openTombstonePanel() {
    await this.page.locator('button:has-text("墓碑")').first().click();
    await this.page.waitForSelector(`${SEL.tombstoneOverlay}.show`);
  }

  async closeTombstonePanel() {
    await this.page.locator(`${SEL.tombstoneOverlay} .drawer-close`).click();
  }

  async openReconcilePanel() {
    await this.page.locator('button:has-text("对账")').first().click();
    await this.page.waitForSelector(`${SEL.reconcileOverlay}.show`);
  }

  async closeReconcilePanel() {
    await this.page.locator(`${SEL.reconcileOverlay} .drawer-close`).click();
  }

  async openVectorIndexPanel() {
    await this.page.locator('button:has-text("向量")').first().click();
    await this.page.waitForSelector(`${SEL.vectorIndexOverlay}.show`);
  }

  async closeVectorIndexPanel() {
    await this.page.locator(`${SEL.vectorIndexOverlay} .drawer-close`).click();
  }

  async openToolDescsPanel() {
    await this.page.locator('button:has-text("工具描述")').first().click();
    await this.page.waitForSelector(`${SEL.toolDescsOverlay}.show`);
  }

  async closeToolDescsPanel() {
    await this.page.locator(`${SEL.toolDescsOverlay} .drawer-close`).click();
  }

  // ── Model Testing ──

  async testEmbedder() {
    await this.page.locator(SEL.testBtnEmbedder).click();
    await this.page.waitForTimeout(2000);
  }

  async testReranker() {
    await this.page.locator(SEL.testBtnReranker).click();
    await this.page.waitForTimeout(2000);
  }

  async testDocParser() {
    await this.page.locator(SEL.testBtnDocParser).click();
    await this.page.waitForTimeout(2000);
  }

  // ── Toast ──

  getToastText() {
    const toast = this.page.locator(SEL.toast);
    return toast.isVisible().then(v => v ? toast.textContent() : null);
  }

  // ── Stats ──

  getStatValue(id: string) {
    return this.page.locator(id).textContent();
  }

  // ── Pagination ──

  isPaginationVisible() {
    return this.page.locator(SEL.pagination).evaluate(el => el.classList.contains('active'));
  }

  async clickPage(n: number) {
    await this.page.locator(`${SEL.pagination} button`).filter({ hasText: String(n + 1) }).click();
    await this.page.waitForLoadState('networkidle');
  }

  // ── Export ──

  async clickExport() {
    // The export button triggers a download
    const downloadPromise = this.page.waitForEvent('download');
    await this.page.locator('button:has-text("导出")').click();
    return downloadPromise;
  }
}

// ── Fixture ────────────────────────────────────────────────────────────────────

import { test as base } from '@playwright/test';

type KBManagerFixtures = {
  app: KBManagerPage;
};

export const test = base.extend<KBManagerFixtures>({
  app: async ({ page }, use) => {
    const app = new KBManagerPage(page);
    // Clear localStorage to prevent test cross-contamination, then reload
    await page.goto('/');
    await page.evaluate(() => localStorage.clear());
    await page.reload();
    await app.waitForApp();
    await use(app);
  },
});

export { expect };
