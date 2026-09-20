import type { IMChannelRuntimeStatus } from '@/api/agent'
export function imRuntimeDisplay(enabled: boolean, runtime?: IMChannelRuntimeStatus) {
  if (!enabled) return { label: '已停用', theme: 'default' as const }
  switch (runtime?.state) {
    case 'online': return { label: '已连接', theme: 'success' as const }
    case 'starting': return { label: '连接中', theme: 'primary' as const }
    case 'reconnecting': return { label: '正在重连', theme: 'warning' as const }
    case 'stopped': case 'unavailable': return { label: '连接中断', theme: 'danger' as const }
    default: return { label: '已启用', theme: 'default' as const }
  }
}
