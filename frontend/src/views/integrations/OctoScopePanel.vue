<template>
  <section class="octo-management">
    <nav v-if="screen !== 'directory'" class="breadcrumbs" aria-label="当前位置">
      <t-button variant="text" :disabled="editing" @click="home">‹ 群与 Bot 管理</t-button>
      <template v-if="focusBot && screen !== 'bot'">
        <span>/</span><t-button variant="text" :disabled="editing" @click="openBot(focusBot)">{{ focusBot.name }}</t-button>
      </template>
      <template v-if="screen === 'scope'">
        <span>/</span><t-button variant="text" :disabled="editing" @click="backToGroup">{{ activeGroup?.display_name || '群详情' }}</t-button>
      </template>
      <span>/</span><span>{{ screen === 'scope' ? '区域配置' : screen === 'bot' ? focusBot?.name : activeGroup?.display_name }}</span>
    </nav>

    <t-alert v-if="error" theme="error" role="alert">{{ error }} <t-button variant="text" size="small" @click="refresh">重试</t-button></t-alert>

    <template v-if="screen === 'directory' || screen === 'bot'">
      <div class="heading">
        <div><h2>{{ screen === 'bot' ? focusBot?.name : '群与 Bot 管理' }}</h2>
          <p v-if="screen === 'bot'">{{ focusBot?.account_id }} · {{ botStatus(focusBot) }} · {{ focusBot?.group_count }} 个群</p>
          <p v-else>查找已接入的群或 Bot，管理主群和子区的知识权限。</p>
        </div>
        <div class="actions">
          <t-button v-if="screen === 'directory'" variant="text" @click="connectOpen = true">连接 Bot</t-button>
          <t-button @click="startAdd()">＋ 接入群或子区</t-button>
        </div>
      </div>
      <div v-if="screen === 'directory'" class="mode" role="group" aria-label="查看方式">
        <button type="button" :aria-pressed="mode === 'group'" @click="setMode('group')">按群查看</button>
        <button type="button" :aria-pressed="mode === 'bot'" @click="setMode('bot')">按 Bot 查看</button>
      </div>
      <div class="toolbar">
        <t-input v-model="query" clearable :maxlength="80" :placeholder="showBots ? '搜索 Bot 名称或连接名' : '搜索群、子区或群 ID'" aria-label="搜索列表" />
        <select v-if="screen === 'directory'" v-model="filter" aria-label="筛选状态">
          <option value="">全部{{ showBots ? ' Bot' : '群' }}</option>
          <option v-if="showBots" value="verified">身份已核验</option>
          <option v-else value="multiple">多个 Bot</option>
          <option value="pending">待核验</option>
        </select>
      </div>
      <t-loading :loading="loading">
        <div class="table-wrap">
          <table v-if="showBots && bots.length" aria-label="Bot 列表">
            <thead><tr><th>Bot</th><th>接入范围</th><th>身份状态</th><th>操作</th></tr></thead>
            <tbody><tr v-for="bot in bots" :key="bot.account_id">
              <td><strong>{{ bot.name }}</strong><small>{{ bot.account_id }}</small></td>
              <td>{{ bot.group_count }} 个群</td><td>{{ botStatus(bot) }}</td>
              <td><t-button variant="text" @click="openBot(bot)">查看 Bot ›</t-button></td>
            </tr></tbody>
          </table>
          <table v-else-if="!showBots && groups.length" aria-label="群列表">
            <thead><tr><th>群</th><th>接入范围</th><th>区域核验</th><th>操作</th></tr></thead>
            <tbody><tr v-for="group in groups" :key="group.group_id">
              <td><strong>{{ group.display_name }}</strong><small>{{ group.group_id }}</small></td>
              <td><template v-if="screen !== 'bot'">{{ group.bot_count }} 个 Bot · </template>{{ group.subarea_count }} 个子区</td>
              <td>{{ group.pending ? group.pending + ' 个配置待核验' : '已核验' }}</td>
              <td><t-button variant="text" @click="openGroup(group)">查看群 ›</t-button></td>
            </tr></tbody>
          </table>
          <p v-else-if="!loading && !error" class="empty">{{ query || filter ? '没有符合条件的结果。' : '暂无接入记录。' }}</p>
        </div>
      </t-loading>
      <div class="pagination" aria-label="列表分页">
        <span>共 {{ total }} 项</span>
        <t-button size="small" variant="outline" :disabled="offset === 0 || loading" @click="offset -= pageSize">上一页</t-button>
        <span>第 {{ 1 + offset / pageSize }} 页</span>
        <t-button size="small" variant="outline" :disabled="offset + pageSize >= total || loading" @click="offset += pageSize">下一页</t-button>
      </div>
    </template>

    <template v-else-if="screen === 'group' && activeGroup">
      <div class="heading">
        <div><h2>{{ activeGroup.display_name }}</h2><p>{{ activeGroup.group_id }} · 在同一区域对照不同 Bot 的配置</p></div>
        <t-button @click="startAdd()">＋ 接入 Bot 或子区</t-button>
      </div>
      <div class="area-picker">
        <h3>选择区域</h3>
        <div class="toolbar">
          <select v-model="areaId" aria-label="群内区域">
            <option value="">主群</option>
            <option v-for="item in areaOptions" :key="item.subarea_id" :value="item.subarea_id">{{ item.display_name }}</option>
          </select>
          <t-input v-model="areaQuery" clearable placeholder="搜索已接入子区" aria-label="搜索子区" :maxlength="80" />
          <t-button v-if="areas.length < areasTotal" variant="outline" :loading="areaLoading" @click="loadMoreAreas">更多子区</t-button>
        </div>
        <label v-if="focusBot"><input v-model="onlyFocusedBot" type="checkbox"> 只看 {{ focusBot.name }}</label>
      </div>
      <h3 class="section-title">{{ areaId ? selectedArea?.display_name || areaId : '主群' }} · Bot 配置对照</h3>
      <t-loading :loading="loading">
        <div class="table-wrap">
          <table v-if="comparison.length" aria-label="同区域 Bot 配置">
            <thead><tr><th>Bot</th><th>区域配置</th><th>身份状态</th><th>操作</th></tr></thead>
            <tbody><tr v-for="row in comparison" :key="row.bot.account_id">
              <td><strong>{{ row.bot.name }}</strong><small>{{ row.bot.account_id }}</small></td>
              <td>
                <template v-if="row.scope">
                  {{ !row.scope.subarea_id ? '主群配置' : row.scope.inherit_parent ? '继承主群查询' : '子区独立配置' }}
                  <small>{{ row.query_count }} 可查询 · {{ row.manage_count }} 已授权维护</small>
                </template>
                <span v-else>此子区未接入</span>
              </td>
              <td>{{ botStatus(row.bot) }}</td>
              <td><t-button v-if="row.scope" variant="text" @click="openScope(row.scope, row.bot.name)">配置 ›</t-button>
                <t-button v-else variant="text" :disabled="!row.bot.connected" @click="startAdd(row.bot.account_id)">接入…</t-button></td>
            </tr></tbody>
          </table>
          <p v-else-if="!loading && !error" class="empty">没有已接入的 Bot。</p>
        </div>
      </t-loading>
      <div class="pagination" aria-label="Bot 配置分页">
        <span>共 {{ total }} 个 Bot</span>
        <t-button size="small" variant="outline" :disabled="!offset || loading" @click="offset -= pageSize">上一页</t-button>
        <t-button size="small" variant="outline" :disabled="offset + pageSize >= total || loading" @click="offset += pageSize">下一页</t-button>
      </div>
    </template>

    <OctoScopeEditor v-else-if="screen === 'scope' && scopeId" :key="scopeId" :scope-id="scopeId"
      :tenant-id="tenantId" :group-name="activeGroup?.display_name" :bot-name="scopeBotName"
      @editing="editing = $event" @removed="onRemoved" />

    <OctoScopeCreateDialog v-model:visible="addOpen" :initial="addTarget" @saved="onAttached" />
    <OctoConnectionDialog v-model:visible="connectOpen" @saved="refresh" />
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import {
  directoryAreas, directoryBots, directoryComparison, directoryGroups, readOctoConfiguration,
  type OctoAreaEntry, type OctoBotEntry, type OctoComparison, type OctoGroupEntry, type OctoScope,
} from '@/api/octo'
import OctoConnectionDialog from './OctoConnectionDialog.vue'
import OctoScopeCreateDialog from './OctoScopeCreateDialog.vue'
import OctoScopeEditor from './OctoScopeEditor.vue'

const route = useRoute(), router = useRouter(), auth = useAuthStore()
const tenantId = computed(() => String(auth.currentTenantId || ''))
const screen = ref<'directory' | 'group' | 'bot' | 'scope'>('directory')
const mode = ref<'group' | 'bot'>('group')
const query = ref(''), filter = ref(''), offset = ref(0), total = ref(0), pageSize = 20
const groups = ref<OctoGroupEntry[]>([]), bots = ref<OctoBotEntry[]>([]), comparison = ref<OctoComparison[]>([])
const focusBot = ref<OctoBotEntry | null>(null), activeGroup = ref<OctoGroupEntry | null>(null)
const onlyFocusedBot = ref(false)
const areaId = ref(''), areaQuery = ref(''), areas = ref<OctoAreaEntry[]>([]), areasTotal = ref(0)
const keptArea = ref<OctoAreaEntry | null>(null), areaLoading = ref(false)
const loading = ref(false), error = ref(''), editing = ref(false)
const scopeId = ref(''), scopeBotName = ref('')
const addOpen = ref(false), connectOpen = ref(false)
const addTarget = ref<{ account_id?: string; group_id?: string; subarea_id?: string }>({})
const showBots = computed(() => screen.value === 'directory' && mode.value === 'bot')
const selectedArea = computed(() => areas.value.find(a => a.subarea_id === areaId.value) || (keptArea.value?.subarea_id === areaId.value ? keptArea.value : null))
const areaOptions = computed(() => selectedArea.value && !areas.value.some(a => a.subarea_id === areaId.value) ? [selectedArea.value, ...areas.value] : areas.value)
let request = 0, areaRequest = 0, deepLinkRequest = 0
let searchTimer: ReturnType<typeof setTimeout> | undefined, areaTimer: ReturnType<typeof setTimeout> | undefined
const listState = { group: { q: '', filter: '', offset: 0 }, bot: { q: '', filter: '', offset: 0 } }

function botStatus(bot?: OctoBotEntry | null) { return !bot?.connected ? '未配置连接' : bot.verified ? '身份已核验' : '待核验' }
function setMode(next: 'group' | 'bot') {
  if (next === mode.value) return
  remember(); mode.value = next; restore()
}
function remember() { listState[mode.value] = { q: query.value, filter: filter.value, offset: offset.value } }
function restore() { const s = listState[mode.value]; query.value = s.q; filter.value = s.filter; offset.value = s.offset }
function clearDeepLink() {
  if (route.query.scope) { const next = { ...route.query }; delete next.scope; void router.replace({ query: next }) }
}
function home() {
  deepLinkRequest++; clearDeepLink(); screen.value = 'directory'; focusBot.value = null; activeGroup.value = null
  restore()
}
function openBot(bot: OctoBotEntry) {
  if (screen.value === 'directory') remember()
  clearDeepLink(); focusBot.value = bot; screen.value = 'bot'; query.value = ''; filter.value = ''; offset.value = 0
}
function openGroup(group: OctoGroupEntry) {
  if (screen.value === 'directory') { remember(); focusBot.value = null }
  clearDeepLink(); activeGroup.value = group; onlyFocusedBot.value = !!focusBot.value; keptArea.value = null
  screen.value = 'group'; areaId.value = ''; areaQuery.value = ''; offset.value = 0
}
function backToGroup() { clearDeepLink(); screen.value = 'group'; offset.value = 0 }
function openScope(scope: OctoScope, bot: string) {
  if (scope.subarea_id) keptArea.value = { subarea_id: scope.subarea_id, display_name: scope.display_name }
  scopeId.value = scope.id; scopeBotName.value = bot; areaId.value = scope.subarea_id; editing.value = false; screen.value = 'scope'
}
function startAdd(account?: string) {
  addTarget.value = {
    account_id: account || focusBot.value?.account_id,
    group_id: screen.value === 'group' ? activeGroup.value?.group_id : undefined,
    subarea_id: screen.value === 'group' ? areaId.value : undefined,
  }
  addOpen.value = true
}
function onAttached(scope: OctoScope) {
  if (screen.value === 'directory') remember()
  if (activeGroup.value?.group_id !== scope.group_id) {
    activeGroup.value = { group_id: scope.group_id, display_name: scope.subarea_id ? scope.group_id : scope.display_name, bot_count: 0, subarea_count: 0, pending: 0 }
  }
  if (focusBot.value?.account_id !== scope.account_id) focusBot.value = null
  openScope(scope, focusBot.value?.name || comparison.value.find(r => r.bot.account_id === scope.account_id)?.bot.name || '')
}
function onRemoved(scope: OctoScope) {
  if (!scope.subarea_id) { home(); return }
  // The subarea might still exist for another Bot. Refresh the group and
  // select its main area to avoid leaving a now-empty subarea selected.
  areaId.value = ''; keptArea.value = null; backToGroup()
}
async function loadList() {
  const version = ++request
  if (screen.value === 'scope') { loading.value = false; error.value = ''; return }
  const params = { limit: pageSize, offset: offset.value, q: query.value.trim(), filter: screen.value === 'directory' ? filter.value : '' }
  loading.value = true; error.value = ''; groups.value = []; bots.value = []; comparison.value = []
  try {
    if (screen.value === 'group' && activeGroup.value) {
      const { data } = await directoryComparison({
        limit: pageSize, offset: offset.value, group_id: activeGroup.value.group_id, subarea_id: areaId.value,
        account_id: onlyFocusedBot.value ? focusBot.value?.account_id : undefined,
      })
      if (version === request) { comparison.value = data.items; total.value = data.total }
    } else if (showBots.value) {
      const { data } = await directoryBots(params)
      if (version === request) { bots.value = data.items; total.value = data.total }
    } else {
      const { data } = await directoryGroups({ ...params, account_id: screen.value === 'bot' ? focusBot.value?.account_id : undefined })
      if (version === request) { groups.value = data.items; total.value = data.total }
    }
  } catch { if (version === request) { error.value = '无法读取管理列表，请检查工作区权限或稍后重试。'; total.value = 0 } }
  finally { if (version === request) loading.value = false }
}
async function loadAreas(append = false) {
  const version = ++areaRequest, group = activeGroup.value
  if (screen.value !== 'group' || !group) { areaLoading.value = false; return }
  if (!append) { areas.value = []; areasTotal.value = 0 }
  areaLoading.value = true
  try {
    const { data } = await directoryAreas({ group_id: group.group_id, q: areaQuery.value.trim(), offset: append ? areas.value.length : 0, limit: pageSize })
    if (version === areaRequest) { areas.value = append ? [...areas.value, ...data.items] : data.items; areasTotal.value = data.total }
  } catch { if (version === areaRequest) error.value = '无法读取子区列表，请重试。' }
  finally { if (version === areaRequest) areaLoading.value = false }
}
function loadMoreAreas() { if (!areaLoading.value) void loadAreas(true) }
function refresh() { void loadList(); if (screen.value === 'group') void loadAreas() }
watch([screen, mode, filter, offset, areaId, onlyFocusedBot, tenantId], () => { clearTimeout(searchTimer); void loadList() }, { immediate: true })
watch([mode, filter, areaId, onlyFocusedBot], () => { offset.value = 0 }, { flush: 'sync' })
watch(query, () => {
  offset.value = 0; ++request; clearTimeout(searchTimer)
  searchTimer = setTimeout(loadList, 220)
})
watch([screen, activeGroup, tenantId], () => { clearTimeout(areaTimer); void loadAreas() })
watch(areaQuery, () => { ++areaRequest; clearTimeout(areaTimer); areaTimer = setTimeout(() => void loadAreas(), 220) })
watch(selectedArea, value => { if (value) keptArea.value = value })
watch([() => route.query.scope, tenantId], async ([id]) => {
  const version = ++deepLinkRequest
  if (typeof id !== 'string' || !id) return
  try {
    const { data } = await readOctoConfiguration(id)
    if (version !== deepLinkRequest) return
    const s = data.scope
    activeGroup.value = { group_id: s.group_id, display_name: s.subarea_id ? s.group_id : s.display_name, bot_count: 0, subarea_count: 0, pending: 0 }
    openScope(s, '')
  } catch { if (version === deepLinkRequest) error.value = '无法打开此区域，可能已移除或不在当前工作区。' }
}, { immediate: true })
watch(tenantId, () => {
  scopeId.value = ''; editing.value = false; addOpen.value = false; connectOpen.value = false
  listState.group = { q: '', filter: '', offset: 0 }; listState.bot = { q: '', filter: '', offset: 0 }
  home()
}, { flush: 'sync' })
onBeforeUnmount(() => { ++request; ++areaRequest; ++deepLinkRequest; clearTimeout(searchTimer); clearTimeout(areaTimer) })
</script>

<style scoped>
.octo-management { min-width: 0; color: var(--td-text-color-primary); }
.heading,.toolbar,.actions,.breadcrumbs,.pagination,.mode { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
.heading { justify-content: space-between; margin-bottom: 18px; }
.heading h2,.area-picker h3 { margin: 0; }
.heading p { margin: 5px 0 0; }
.heading p,small,.pagination,.empty,.breadcrumbs { color: var(--td-text-color-secondary); }
.breadcrumbs { margin-bottom: 12px; }
.mode { border-bottom: 1px solid var(--td-component-border); margin-bottom: 14px; }
.mode button { border: 0; border-bottom: 2px solid transparent; background: transparent; color: inherit; font: inherit; padding: 9px 14px; cursor: pointer; }
.mode button[aria-pressed='true'] { color: var(--td-brand-color); border-color: var(--td-brand-color); font-weight: 600; }
.toolbar { margin: 12px 0; }
.toolbar :deep(.t-input__wrap) { flex: 1 1 210px; max-width: 540px; }
select { max-width: min(100%, 280px); min-height: 32px; font: inherit; color: inherit; padding: 4px 8px; border: 1px solid var(--td-component-border); border-radius: 6px; background: var(--td-bg-color-container); }
.area-picker { background: var(--td-bg-color-secondarycontainer); border-radius: 8px; padding: 12px 14px; }
.section-title { margin: 16px 0 8px; }
.table-wrap { border: 1px solid var(--td-component-border); border-radius: 8px; overflow-x: auto; }
table { width: 100%; border-collapse: collapse; }
th,td { padding: 10px 12px; text-align: left; border-bottom: 1px solid var(--td-component-border); overflow-wrap: anywhere; }
th { font-weight: 500; background: var(--td-bg-color-secondarycontainer); }
td:last-child,th:last-child { text-align: right; }
tr:last-child td { border-bottom: 0; }
small { display: block; }
.pagination { justify-content: flex-end; margin-top: 10px; }
.empty { padding: 14px; margin: 0; }
@media (max-width: 650px) { th,td { padding: 8px 6px; } .heading { align-items: flex-start; } }
</style>
