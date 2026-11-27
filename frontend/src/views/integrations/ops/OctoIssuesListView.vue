<template>
  <div class="iss-wrap">
    <section class="panel-card">
      <div class="panel-hd">
        <div class="seg" role="group">
          <button
            v-for="q in queues"
            :key="q.v"
            type="button"
            :aria-pressed="visibility === q.v"
            @click="switchQueue(q.v)"
          >
            {{ q.label }}
          </button>
        </div>
        <span class="mini">共 {{ total }} 条 · 勾选后底部浮出批量操作栏</span>
      </div>

      <t-loading :loading="loading">
        <t-table
          v-model:selected-row-keys="selectedKeys"
          :data="issues"
          :columns="columns"
          row-key="id"
          size="small"
          hover
          empty="当前队列为空"
          @row-click="({ row }: { row: OctoIssue }) => openDetail(row.id)"
        >
          <template #title="{ row }">
            <div class="cell-title">
              <b>{{ row.title }}</b>
              <t-tag size="small" variant="light">{{ kindLabels[row.kind] || row.kind }}</t-tag>
            </div>
            <div class="cell-sub">{{ row.original_message || row.description }}</div>
          </template>
          <template #status="{ row }">
            <t-tag size="small" variant="light" :theme="statusTheme(row.status)">
              {{ statusLabels[row.status] || row.status }}
            </t-tag>
          </template>
          <template #source="{ row }">
            <div>{{ kbName(row.knowledge_base_id) }}</div>
            <div class="mini">{{ row.is_direct ? '私聊' : row.subarea_id ? '子区' : '主群' }} · {{ row.scope_name || '原会话' }}</div>
          </template>
          <template #owner="{ row }">
            <div>{{ row.owner_name || '暂未指定' }}</div>
            <div class="mini">{{ formatTime(row.created_at) }}</div>
          </template>
          <template #actions="{ row }">
            <div class="row-acts" @click.stop>
              <t-button size="small" variant="outline" :loading="retestingId === row.id" @click="quickRetest(row)">一键复测</t-button>
              <t-button size="small" theme="primary" @click="openDetail(row.id)">处理</t-button>
            </div>
          </template>
        </t-table>
      </t-loading>
    </section>

    <div v-if="selectedKeys.length" class="float-bar">
      <b>已选 {{ selectedKeys.length }} 件问题</b>
      <template v-if="visibility === 'archive' || visibility === 'trash'">
        <t-button size="small" theme="primary" @click="runBulk('restore')">批量恢复</t-button>
      </template>
      <template v-else>
        <t-button size="small" variant="outline" @click="runBulk('archive')">批量归档</t-button>
        <t-button size="small" variant="outline" @click="runBulk('trash')">移入回收站</t-button>
      </template>
      <t-button size="small" variant="text" @click="selectedKeys = []">取消</t-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import { batchIssues, listIssues, retestIssue, type OctoIssue } from '@/api/octo-business'
import { opsScope, resolveScopeQuery } from './useOctoOpsScope'

const props = defineProps<{ kbs: Array<{ id: string; name: string }> }>()
const router = useRouter()

const visibility = ref<'active' | 'mine' | 'archive' | 'trash'>('active')
const loading = ref(false)
const issues = ref<OctoIssue[]>([])
const total = ref(0)
const selectedKeys = ref<string[]>([])
const retestingId = ref('')

const queues = [
  { v: 'active' as const, label: '待处理' },
  { v: 'mine' as const, label: '我跟进的' },
  { v: 'archive' as const, label: '已归档' },
  { v: 'trash' as const, label: '回收站' },
]

const kindLabels: Record<string, string> = { missing: '知识缺口', bug: 'Bug 反馈', suggestion: '优化建议' }
const statusLabels: Record<string, string> = { open: '待处理', inprogress: '处理中', resolved: '已解决', closed: '已关闭' }

const columns = [
  { colKey: 'row-select', type: 'multiple' as const, width: 42 },
  { colKey: 'title', title: '问题与原问' },
  { colKey: 'status', title: '状态', width: 104 },
  { colKey: 'source', title: '产品库与来源', width: 220 },
  { colKey: 'owner', title: '跟进 / 时间', width: 160 },
  { colKey: 'actions', title: '快捷操作', width: 170, align: 'right' as const },
]

function kbName(id: string) {
  return props.kbs.find((k) => k.id === id)?.name || id
}
function statusTheme(s: string): 'success' | 'warning' | 'primary' | 'default' {
  if (s === 'resolved') return 'success'
  if (s === 'inprogress') return 'primary'
  if (s === 'closed') return 'default'
  return 'warning'
}
function formatTime(v: string) {
  return v ? new Date(v).toLocaleDateString() : ''
}
function openDetail(id: string) {
  router.push({ name: 'knowledgeIssueDetail', params: { issueId: id } })
}
function switchQueue(v: 'active' | 'mine' | 'archive' | 'trash') {
  visibility.value = v
  selectedKeys.value = []
  void load()
}

async function load() {
  loading.value = true
  try {
    const q = resolveScopeQuery(opsScope)
    const res = await listIssues({
      scope_id: q.scope_id || undefined,
      knowledge_base_id: q.knowledge_base_id || undefined,
      keyword: q.keyword || undefined,
      visibility: visibility.value,
      page: 1,
      page_size: 40,
    })
    issues.value = res.data.items || []
    total.value = res.data.total || 0
  } catch {
    MessagePlugin.error('加载问题列表失败')
  } finally {
    loading.value = false
  }
}

async function quickRetest(row: OctoIssue) {
  retestingId.value = row.id
  try {
    await retestIssue(row.id, 'original', 0)
    MessagePlugin.success('一键即时复测完成，正在打开工单对照')
    openDetail(row.id)
  } catch {
    MessagePlugin.warning('无法直接一键复测，进入工单详情核对状态')
    openDetail(row.id)
  } finally {
    retestingId.value = ''
  }
}

async function runBulk(action: string) {
  const targets = issues.value
    .filter((i) => selectedKeys.value.includes(i.id))
    .map((i) => ({ id: i.id, updated_at: i.updated_at }))
  if (!targets.length) return
  try {
    await batchIssues({ targets, action, note: `批量操作：${action}` })
    MessagePlugin.success(`已批量处理 ${targets.length} 件问题`)
    selectedKeys.value = []
    await load()
  } catch {
    MessagePlugin.error('批量处理失败')
  }
}

defineExpose({ reload: load })
onMounted(load)
</script>

<style scoped lang="less">
.iss-wrap { max-width: 1440px; margin: 0 auto; }
.panel-card { background: var(--td-bg-color-container); border: 1px solid var(--td-component-border); border-radius: 10px; overflow: hidden; }
.panel-hd { display: flex; justify-content: space-between; align-items: center; gap: 10px; padding: 12px 16px; border-bottom: 1px solid var(--td-component-border); flex-wrap: wrap; }
.seg { display: inline-flex; padding: 2px; border-radius: 6px; background: var(--td-bg-color-secondarycontainer); border: 1px solid var(--td-component-border); }
.seg button { height: 26px; padding: 0 11px; border: 0; border-radius: 4px; background: transparent; color: var(--td-text-color-secondary); font-size: 12.5px; cursor: pointer; }
.seg button[aria-pressed='true'] { background: var(--td-bg-color-container); color: var(--td-brand-color); font-weight: 600; }
.cell-title { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.cell-sub { font-size: 12px; color: var(--td-text-color-placeholder); max-width: 380px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; margin-top: 2px; }
.row-acts { display: flex; gap: 6px; justify-content: flex-end; }
.mini { font-size: 12px; color: var(--td-text-color-placeholder); }
.float-bar { position: fixed; bottom: 20px; left: 50%; transform: translateX(-50%); display: flex; align-items: center; gap: 10px; padding: 9px 16px; border-radius: 10px; background: var(--td-text-color-primary); color: var(--td-bg-color-container); z-index: 40; box-shadow: 0 12px 32px rgba(0,0,0,0.22); }
</style>
