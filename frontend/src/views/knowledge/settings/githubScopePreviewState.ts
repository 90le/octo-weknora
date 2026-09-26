export function githubScopePreviewRequest(sourceId: string, settings: Record<string, unknown>) {
  return {
    source_id: sourceId,
    paths: Array.isArray(settings.paths) ? [...settings.paths] as string[] : [],
    ...(Array.isArray(settings.exclude) ? { exclude: [...settings.exclude] as string[] } : {}),
  }
}

export function canShowGitHubScopePreview(preview: { tree_state: string }): boolean {
  return preview.tree_state === 'complete' || preview.tree_state === 'truncated'
}

export function githubScopePreviewErrorKey(value: { tree_state?: string; error_code?: string }): string {
  if (value.error_code === 'github_exclusion_invalid') return 'datasource.githubBulk.scopePreview.invalidExclude'
  return value.tree_state === 'missing_path' || value.error_code === 'github_selected_path_missing'
    ? 'datasource.githubBulk.scopePreview.missingPath'
    : 'datasource.githubBulk.scopePreview.failed'
}
