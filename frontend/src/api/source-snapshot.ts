import { get } from '@/utils/request'

export interface SourceSummary { id: string; name: string; type: string; status: string; snapshot_id?: string; revision?: string; file_count: number; skipped?: Record<string,number> }
export interface SourceEntry { path: string; directory: boolean; size?: number }
export interface SourceRead { source_id: string; snapshot_id: string; path: string; revision: string; start_line: number; end_line: number; total_lines: number; content: string; truncated: boolean; source_url?: string; preview_url: string }
export interface SourceMatch { path: string; line: number; text: string; source_url?: string }
const unpack = (r: any) => r?.data ?? r
const base = (kb: string, source?: string) => `/api/v1/knowledge-bases/${encodeURIComponent(kb)}/sources${source ? '/'+encodeURIComponent(source) : ''}`
export const listSourceSnapshots = async (kb: string): Promise<SourceSummary[]> => unpack(await get(base(kb)))
export const localSourceRoots = async (): Promise<{id: string; name: string}[]> => unpack(await get('/api/v1/datasource/local-roots'))
export const sourceTree = async (kb: string, source: string, snapshot: string, directory: string, offset=0): Promise<{snapshot_id:string;entries:SourceEntry[];total:number}> => unpack(await get(base(kb,source)+'/tree', { params: { snapshot_id:snapshot, directory, offset } }))
export const sourceRead = async (kb: string, source: string, snapshot: string, path: string, start=1): Promise<SourceRead> => unpack(await get(base(kb,source)+'/read', { params: { snapshot_id:snapshot, path, start_line:start, end_line:start+99 } }))
export const sourceSearch = async (kb: string, source: string, snapshot: string, q: string, path: string): Promise<{snapshot_id:string;matches:SourceMatch[];complete:boolean}> => unpack(await get(base(kb,source)+'/search', { params: { snapshot_id:snapshot, q, path } }))
