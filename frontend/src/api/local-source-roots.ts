import { del, get, post, put } from '@/utils/request'
export interface LocalRoot { id: string; name: string; space_id: string; directory: string; tenant_id: number; enabled: boolean }
export interface LocalSpace { id: string; name: string; path: string }
export interface RootRegistration { name: string; space_id: string; directory: string }
const base = '/api/v1/system/admin/local-source-roots'
const unpack = (r: any) => r?.data ?? r
export const listManagedLocalRoots = async (): Promise<LocalRoot[]> => unpack(await get(base))
export const listLocalSpaces = async (): Promise<LocalSpace[]> => unpack(await get(base + '/spaces'))
export const probeLocalRoot = async (value: RootRegistration): Promise<{readable: boolean}> => unpack(await post(base + '/probe', value))
export const createLocalRoot = async (value: RootRegistration): Promise<LocalRoot> => unpack(await post(base, value))
export const updateLocalRoot = async (id: string, value: {name: string; enabled: boolean}): Promise<LocalRoot> => unpack(await put(base + '/' + encodeURIComponent(id), value))
export const deleteLocalRoot = async (id: string): Promise<void> => { await del(base + '/' + encodeURIComponent(id)) }
