import type { DirectoryEntry, LocalRoot, LocalSpace } from '@/api/local-source-roots'

export const canGrantDirectory = (space: LocalSpace): boolean => space.status === 'ready' && space.readable === true
export const canRemoveDirectoryGrant = (root: LocalRoot): boolean => root.usage_complete === true && root.data_source_count === 0
export function isDiscoveredSpaceRegistered(entry: DirectoryEntry, spaces: LocalSpace[]): boolean {
  return spaces.some(space => space.path.replace(/\/+$/, '') === '/source-roots/' + entry.directory)
}
export function storageErrorMessage(error: unknown, translate: (key: string) => string): string {
  const value = error as {code?: string; message?: string; error?: {code?: string}} | null
  const code = value?.code || value?.error?.code
  const keys: Record<string, string> = {
    invalid_folder_input: 'storageSpaces.invalidDirectory',
    unsafe_folder: 'storageSpaces.unsafeDirectory',
    folder_unavailable: 'storageSpaces.unreadableDirectory',
    folder_not_found: 'storageSpaces.directoryNotFound',
    folder_in_use: 'storageSpaces.usedDelete',
    folder_already_registered: 'storageSpaces.directoryExists',
    folder_access_denied: 'storageSpaces.directoryDenied',
  }
  return code && Object.hasOwn(keys, code) ? translate(keys[code]) : value?.message || translate('sourceRoots.failed')
}
