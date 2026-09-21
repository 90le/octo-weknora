import type { DirectoryEntry, DirectoryPage } from '@/api/local-source-roots'

// These display checks prevent malformed tree state. The server is always the
// authority for containment, symlinks, workspace grants and filesystem access.
export function isRelativeDirectory(path: string): boolean {
  return path === '' || (!path.startsWith('/') && !/[\\\0]/.test(path) && path.split('/').every(part => part !== '' && part !== '.' && part !== '..'))
}
export function immediateDirectories(parent: string, entries: DirectoryEntry[]): DirectoryEntry[] {
  if (!isRelativeDirectory(parent)) return []
  const prefix = parent ? parent + '/' : ''
  const seen = new Set<string>()
  return entries.filter(entry => {
    const tail = entry.directory.slice(prefix.length)
    if (!entry.directory.startsWith(prefix) || !tail || tail.includes('/') || !isRelativeDirectory(entry.directory) || seen.has(entry.directory)) return false
    seen.add(entry.directory)
    return true
  })
}
export function nextDirectoryOffset(page: DirectoryPage, offset: number): number | null {
  return page.has_more && Number.isSafeInteger(page.next_offset) && page.next_offset > offset ? page.next_offset : null
}
export function ancestorDirectories(path: string): string[] {
  if (!isRelativeDirectory(path) || !path) return ['']
  const segments = path.split('/')
  return ['', ...segments.slice(0, -1).map((_, i) => segments.slice(0, i + 1).join('/'))]
}
