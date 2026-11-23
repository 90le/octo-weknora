/** Anchors in the guide shipped with the frontend. */
export const DOC_PAGES = {
  home: '', cli: 'cli', tenantAuth: 'access', models: 'models',
  modelsCompat: 'model-compatibility', knowledgeGraph: 'knowledge-graph',
  imIntegration: 'channels', apiOverview: 'api',
  troubleshootingMigrations: 'migrations', sandboxDeployment: 'sandbox',
} as const

export type DocPage = keyof typeof DOC_PAGES

export function docsUrl(page: DocPage): string {
  const anchor = DOC_PAGES[page]
  return '/product-guide.html' + (anchor ? '#' + anchor : '')
}
