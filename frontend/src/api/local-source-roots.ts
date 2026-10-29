import { del, get, post, put } from '@/utils/request'
export interface LocalRoot { id: string; name: string; space_id: string; directory: string; tenant_id: number; enabled: boolean; data_source_count?: number; usage_complete?: boolean }
export interface LocalSpace { id: string; name: string; path: string; status: 'ready' | 'unavailable' | 'unsafe'; readable: boolean; authorized_root_count: number; enabled_root_count: number; data_source_count: number; usage_complete: boolean }
export interface DirectoryEntry { name: string; directory: string }
export interface DirectoryPage { directory: string; entries: DirectoryEntry[]; has_more: boolean; next_offset: number; truncated: boolean }
export interface DiscoveredSpaces { status: 'ready' | 'unavailable' | 'unsafe'; available: boolean; entries: DirectoryEntry[]; truncated: boolean }
export interface RootRegistration { name: string; space_id: string; directory: string }
export interface ManagedMountRoot { id: string }
export interface ManagedMount { id: string; root: string; directory: string; status: 'ready' | 'unavailable' | 'pending' }
const base = '/api/v1/system/admin/local-source-roots'
const unpack = (r: any) => r?.data ?? r
export const listManagedLocalRoots = async (): Promise<LocalRoot[]> => unpack(await get(base))
export const listLocalSpaces = async (): Promise<LocalSpace[]> => unpack(await get(base + '/spaces'))
export const discoverLocalSpaces = async (): Promise<DiscoveredSpaces> => unpack(await get(base + '/spaces/discovered'))
export const browseMountDirectory = async (directory = '', offset = 0): Promise<DirectoryPage> => unpack(await get(base + '/discovery/directories', {params: {directory, offset}}))
export const listManagedMountRoots = async (): Promise<ManagedMountRoot[]> => unpack(await get(base + '/managed/roots'))
export const listManagedMounts = async (): Promise<ManagedMount[]> => unpack(await get(base + '/managed/mounts'))
export const browseManagedMountRoot = async (root: string, directory = '', offset = 0): Promise<DirectoryPage> => unpack(await get(base + '/managed/directories', {params: {root, directory, offset}}))
export const createManagedMount = async (root: string, directory: string): Promise<ManagedMount> => unpack(await post(base + '/managed/mounts', {root, directory}))
export const deleteManagedMount = async (id: string): Promise<void> => { await del(base + '/managed/mounts/' + encodeURIComponent(id)) }
export const registerLocalSpace = async (value: {name: string; directory: string}): Promise<LocalSpace> => unpack(await post(base + '/spaces', value))
export const deleteLocalSpace = async (id: string): Promise<void> => { await del(base + '/spaces/' + encodeURIComponent(id)) }
export const browseLocalSpace = async (id: string, directory = '', offset = 0): Promise<DirectoryPage> => unpack(await get(base + '/spaces/' + encodeURIComponent(id) + '/directories', {params: {directory, offset}}))
export const browseAuthorizedDirectory = async (id: string, directory = '', offset = 0): Promise<DirectoryPage> => unpack(await get('/api/v1/datasource/local-roots/' + encodeURIComponent(id) + '/directories', {params: {directory, offset}}))
export const probeLocalRoot = async (value: RootRegistration): Promise<{readable: boolean}> => unpack(await post(base + '/probe', value))
export const createLocalRoot = async (value: RootRegistration): Promise<LocalRoot> => unpack(await post(base, value))
export const updateLocalRoot = async (id: string, value: {name: string; enabled: boolean}): Promise<LocalRoot> => unpack(await put(base + '/' + encodeURIComponent(id), value))
export const deleteLocalRoot = async (id: string): Promise<void> => { await del(base + '/' + encodeURIComponent(id)) }
