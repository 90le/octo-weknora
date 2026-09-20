// Keep existing bookmarks while moving daily operations out of Settings.
export function knowledgeOperationsTarget(query: Record<string, unknown>) {
  const section = String(query.section || '')
  if (section === 'integration-im') {
    return { name: 'knowledgeChannels', query: typeof query.agentId === 'string' ? { agentId: query.agentId } : {} }
  }
  if (section !== 'integration-octo') return null
  if (query.view === 'contacts') {
    return typeof query.kbId === 'string' && query.kbId
      ? { name: 'knowledgeContacts', params: { kbId: query.kbId } }
      : { name: 'knowledgeBaseList' }
  }
  if (query.view === 'issues') return { name: 'knowledgeIssues' }
  if (query.view === 'schedule' || query.view === 'reports') return { name: 'knowledgeReports' }
  return { name: 'octoGroups', query: typeof query.scope === 'string' ? { scope: query.scope } : {} }
}
