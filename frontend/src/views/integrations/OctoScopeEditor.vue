<template>
  <t-alert v-if="error" theme="error" role="alert">
    {{ error }}
    <t-button v-if="!busy" size="small" variant="text" @click="load">重新读取</t-button>
  </t-alert>
  <t-loading :loading="loading">
    <section v-if="snapshot" class="scope-editor">
      <header class="editor-heading">
        <div>
          <h2>{{ botName || snapshot.scope.account_id }} · {{ snapshot.scope.subarea_id ? snapshot.scope.display_name : '主群' }}</h2>
          <p>{{ groupName || snapshot.scope.group_id }} · {{ snapshot.scope.account_id }}</p>
        </div>
        <div v-if="draft" class="actions">
          <t-button variant="outline" :disabled="busy" @click="cancelEdit">取消编辑</t-button>
          <t-button :loading="busy" :disabled="!hasChanges || stale" @click="review">预览变更</t-button>
        </div>
        <div v-else class="actions">
          <t-button variant="text" theme="danger" :disabled="busy || stale" @click="removeOpen = true">移除接入</t-button>
          <t-button :disabled="busy || stale" @click="startEdit">编辑配置</t-button>
        </div>
      </header>

      <div v-if="shown && snapshot.scope.subarea_id" class="inherit-box">
        <label v-if="draft"><input v-model="draft.inherit_parent" type="checkbox" :disabled="busy"> 继承此 Bot 的主群查询</label>
        <strong v-else>{{ shown.inherit_parent ? '继承主群查询' : '子区独立配置' }}</strong>
        <p>直接绑定优先显示。维护授权、公网搜索和其他区域开关不继承。</p>
      </div>

      <div class="section-heading">
        <h3>知识库与权限</h3>
        <span>{{ queryCount }} 可查询 · {{ manageCount }} 已授权维护</span>
      </div>
      <t-input v-if="draft" v-model="kbSearch" clearable placeholder="搜索知识库名称或 ID" aria-label="搜索知识库" class="kb-search" />
      <div v-if="rows.length" class="table-wrap">
        <table aria-label="知识库权限">
          <thead><tr><th>知识库</th><th>查询</th><th>维护</th></tr></thead>
          <tbody>
            <tr v-for="row in rows" :key="row.id">
              <td><router-link :to="{ name: 'knowledgeBaseDetail', params: { kbId: row.id } }">{{ label(row.id) }}</router-link></td>
              <td>
                <label v-if="draft"><input type="checkbox" :checked="row.mode === 'direct'" :disabled="busy || !editableKBs.has(row.id)" @change="toggle('query_ids', row.id)"> 直接绑定</label>
                <span v-else>{{ modeLabel[row.mode] }}</span>
                <small v-if="draft && row.mode === 'inherited'">当前继承主群</small>
              </td>
              <td><label v-if="draft"><input type="checkbox" :checked="row.manage" :disabled="busy || !editableKBs.has(row.id)" @change="toggle('manage_ids', row.id)"> 本区授权</label><span v-else>{{ row.manage ? '已授权' : '未授权' }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="muted">{{ draft ? '没有符合条件的知识库。' : '当前区域没有知识库查询绑定或维护授权。' }}</p>

      <div class="section-heading"><h3>区域规则</h3></div>
      <div v-if="shown" class="rules">
        <label v-if="!snapshot.scope.subarea_id"><input type="checkbox" :checked="shown.aggregate_child_issues" :disabled="!draft || busy" @change="togglePolicy('aggregate_child_issues')"> 汇总子区问题</label>
        <label><input type="checkbox" :checked="shown.allow_knowledge_creation" :disabled="!draft || busy" @change="togglePolicy('allow_knowledge_creation')"> 允许本区管理者建库</label>
        <label><input type="checkbox" :checked="shown.allow_public_web" :disabled="!draft || busy" @change="togglePolicy('allow_public_web')"> 允许本区公网搜索</label>
      </div>

      <details v-if="!draft" class="diagnostics">
        <summary>平台信息与角色核对</summary>
        <p>群 ID：{{ snapshot.scope.group_id }}<template v-if="snapshot.scope.subarea_id"> · 子区 ID：{{ snapshot.scope.subarea_id }}</template></p>
        <p>区域名称：{{ snapshot.scope.display_name }} · {{ snapshot.scope.sync_status === 'verified' ? '平台已核验' : '待核验' }}</p>
        <div class="actions">
          <t-button variant="outline" :loading="busy" @click="syncName">同步区域名称</t-button>
          <t-input v-model="roleUID" placeholder="输入 Octo 用户 ID" aria-label="Octo 用户 ID" class="role-input" />
          <t-button variant="outline" :loading="checkingRole" :disabled="!roleUID.trim()" @click="checkRole">核对成员角色</t-button>
        </div>
        <p v-if="roleResult" role="status">{{ roleResult }}</p>
      </details>
    </section>
  </t-loading>

  <t-dialog v-model:visible="previewOpen" header="确认配置变更" attach="body" :close-on-overlay-click="false"
    :confirm-loading="busy" :confirm-btn="{ content: '确认保存', disabled: stale }" @confirm="save">
    <p>{{ targetLabel }}</p>
    <ul v-if="policyChanges.length"><li v-for="change in policyChanges" :key="change">{{ change }}</li></ul>
    <p v-if="!preview?.impacts.some(i => i.changes.length)">生效的知识权限没有变化。</p>
    <div v-else class="impact-list">
      <section v-for="impact in preview?.impacts.filter(i => i.changes.length)" :key="impact.scope_id">
        <h4>{{ impact.display_name }}<span v-if="impact.scope_id !== scopeId"> · 受影响子区</span></h4>
        <ul><li v-for="change in impact.changes" :key="change.knowledge_base_id">
          {{ label(change.knowledge_base_id) }}：{{ accessLabel(change.before) }} → {{ accessLabel(change.after) }}
        </li></ul>
      </section>
    </div>
  </t-dialog>

  <t-dialog v-model:visible="removeOpen" header="移除此区域接入" attach="body" :close-on-overlay-click="false"
    :confirm-loading="busy" :confirm-btn="{ content: '确认移除', theme: 'danger', disabled: stale || !!snapshot?.child_areas.length }" @confirm="remove">
    <p>{{ targetLabel }}</p>
    <template v-if="snapshot?.child_areas.length">
      <p>请先移除此 Bot 已接入的 {{ snapshot.child_areas.length }} 个子区：</p>
      <ul><li v-for="area in snapshot.child_areas" :key="area.subarea_id">{{ area.display_name }}</li></ul>
    </template>
    <p v-else>移除后，此 Bot 在该区域的查询和维护授权将失效。其他 Bot、Octo 成员关系、知识资料及历史记录保留。</p>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { listKnowledgeBases } from '@/api/knowledge-base'
import {
  inspectMemberRole, previewOctoConfiguration, readOctoConfiguration, removeOctoScope,
  saveOctoConfiguration, syncScopeName, type OctoAccessState, type OctoConfigurationPreview,
  type OctoConfigurationSnapshot, type OctoScope, type OctoScopeConfiguration,
} from '@/api/octo'

const props = defineProps<{ scopeId: string; groupName?: string; botName?: string; tenantId: string }>()
const emit = defineEmits<{ editing: [boolean]; removed: [OctoScope] }>()
const snapshot = ref<OctoConfigurationSnapshot | null>(null)
const draft = ref<OctoScopeConfiguration | null>(null)
const preview = ref<OctoConfigurationPreview | null>(null)
const previewOpen = ref(false), removeOpen = ref(false), loading = ref(false), busy = ref(false), stale = ref(false)
const error = ref(''), kbSearch = ref(''), roleUID = ref(''), roleResult = ref(''), checkingRole = ref(false)
const catalog = ref<Array<{ id: string; name: string; tenant_id?: string | number }>>([])
const editableKBs = computed(() => new Set([
  ...catalog.value.filter(kb => String(kb.tenant_id ?? props.tenantId) === props.tenantId).map(kb => kb.id),
  ...(snapshot.value?.query_ids ?? []), ...(snapshot.value?.manage_ids ?? []),
]))
const names = computed(() => new Map(catalog.value.map(kb => [kb.id, kb.name])))
const label = (id: string) => names.value.get(id) || id
const modeLabel = { direct: '本区直接绑定', inherited: '继承主群', none: '未开放查询' }
const accessLabel = (s: OctoAccessState) => modeLabel[s.query_mode] + (s.can_manage ? ' / 已授权维护' : ' / 未授权维护')
const targetLabel = computed(() => snapshot.value && [
  props.botName || snapshot.value.scope.account_id, props.groupName || snapshot.value.scope.group_id,
  snapshot.value.scope.subarea_id ? snapshot.value.scope.display_name : '主群',
].join(' / '))
const baseline = computed<OctoScopeConfiguration | null>(() => {
  const s = snapshot.value
  return s && {
    query_ids: [...s.query_ids], manage_ids: [...s.manage_ids], inherit_parent: s.scope.inherit_parent,
    allow_knowledge_creation: s.scope.allow_knowledge_creation, aggregate_child_issues: s.scope.aggregate_child_issues,
    allow_public_web: s.scope.allow_public_web,
  }
})
const shown = computed(() => draft.value || baseline.value)
const inherited = computed(() => new Set(shown.value?.inherit_parent ? snapshot.value?.parent_query_ids : []))
const queryCount = computed(() => new Set([...(shown.value?.query_ids ?? []), ...inherited.value]).size)
const manageCount = computed(() => shown.value?.manage_ids.length ?? 0)
const rows = computed(() => {
  const c = shown.value
  if (!c) return []
  const direct = new Set(c.query_ids), managed = new Set(c.manage_ids)
  const ids = new Set([...direct, ...managed, ...inherited.value, ...(draft.value ? editableKBs.value : [])])
  const keyword = kbSearch.value.trim().toLowerCase()
  return [...ids].filter(id => !draft.value || !keyword || (label(id) + ' ' + id).toLowerCase().includes(keyword))
    .map(id => ({ id, mode: (direct.has(id) ? 'direct' : inherited.value.has(id) ? 'inherited' : 'none') as OctoAccessState['query_mode'], manage: managed.has(id) }))
    .sort((a, b) => label(a.id).localeCompare(label(b.id), 'zh-CN'))
})
const flags = {
  inherit_parent: '继承主群查询', allow_knowledge_creation: '允许本区管理者建库',
  aggregate_child_issues: '汇总子区问题', allow_public_web: '允许本区公网搜索',
} as const
type Flag = keyof typeof flags
const policyChanges = computed(() => {
  const before = baseline.value, after = draft.value
  if (!before || !after) return []
  return (Object.keys(flags) as Flag[]).filter(key => before[key] !== after[key])
    .map(key => flags[key] + '：' + (after[key] ? '关闭 → 开启' : '开启 → 关闭'))
})
const hasChanges = computed(() => {
  const before = baseline.value, after = draft.value
  if (!before || !after) return false
  return policyChanges.value.length > 0 || (['query_ids', 'manage_ids'] as const).some(key =>
    before[key].length !== after[key].length || before[key].some(id => !after[key].includes(id)))
})
let active = true
onBeforeUnmount(() => { active = false })
onMounted(load)

function failure(e: unknown, fallback: string) {
  const response = e as { code?: string; status?: number }
  if (response?.code === 'octo_stale_preview' || (response?.status === 409 && !response?.code)) {
    stale.value = true
    error.value = '配置已被修改，请重新读取后再编辑和确认。'
  } else if (response?.code === 'octo_scope_schedule') {
    error.value = '该区域仍有启用的定时报表。请先停用报表，再移除接入。'
  } else if (response?.code === 'octo_scope_children') {
    error.value = '该 Bot 的主群仍有已接入子区。请重新读取并先处理子区。'
  } else error.value = fallback
}
async function load() {
  loading.value = true; error.value = ''; stale.value = false; cancelEdit()
  try {
    const [config, kbs] = await Promise.all([readOctoConfiguration(props.scopeId), listKnowledgeBases()])
    if (!active) return
    snapshot.value = config.data; catalog.value = kbs.data || []
  } catch { if (active) { snapshot.value = null; error.value = '无法读取区域配置或知识库。' } }
  finally { if (active) loading.value = false }
}
function startEdit() {
  if (!baseline.value) return
  draft.value = { ...baseline.value, query_ids: [...baseline.value.query_ids], manage_ids: [...baseline.value.manage_ids] }
  kbSearch.value = ''; error.value = ''; emit('editing', true)
}
function cancelEdit() {
  draft.value = null; preview.value = null; previewOpen.value = false; emit('editing', false)
}
function toggle(key: 'query_ids' | 'manage_ids', id: string) {
  if (!draft.value || busy.value) return
  const values = draft.value[key]
  draft.value[key] = values.includes(id) ? values.filter(k => k !== id) : [...values, id]
}
function togglePolicy(key: Flag) { if (draft.value && !busy.value) draft.value[key] = !draft.value[key] }
async function review() {
  if (!snapshot.value || !draft.value || busy.value || stale.value) return
  busy.value = true; error.value = ''
  try {
    const result = await previewOctoConfiguration(props.scopeId, snapshot.value.revision, draft.value)
    if (active) { preview.value = result.data; previewOpen.value = true }
  } catch (e) { if (active) failure(e, '无法生成变更预览，未保存配置。') }
  finally { if (active) busy.value = false }
}
async function save() {
  if (!snapshot.value || !draft.value || !preview.value || busy.value || stale.value) return
  busy.value = true; error.value = ''
  try {
    await saveOctoConfiguration(props.scopeId, snapshot.value.revision, preview.value.token, draft.value)
    if (!active) return
    await load()
    if (active) await MessagePlugin.success('区域配置已保存')
  } catch (e) {
    if (active) { previewOpen.value = false; failure(e, '保存失败。请核对当前配置后再试。') }
  } finally { if (active) busy.value = false }
}
async function remove() {
  const s = snapshot.value
  if (!s || s.child_areas.length || busy.value || stale.value) return
  busy.value = true; error.value = ''
  try {
    await removeOctoScope(props.scopeId, s.revision)
    if (active) { removeOpen.value = false; emit('removed', s.scope); await MessagePlugin.success('区域接入已移除') }
  } catch (e) { if (active) { removeOpen.value = false; failure(e, '无法移除接入。请重新核对权限和当前配置。') } }
  finally { if (active) busy.value = false }
}
async function syncName() {
  if (busy.value) return
  busy.value = true; error.value = ''
  try { await syncScopeName(props.scopeId); if (active) await load() }
  catch { if (active) error.value = '无法同步平台名称，请检查 Bot 的连接及访问权限。' }
  finally { if (active) busy.value = false }
}
async function checkRole() {
  const uid = roleUID.value.trim()
  if (!uid || checkingRole.value) return
  checkingRole.value = true; roleResult.value = ''
  try {
    const { data } = await inspectMemberRole(props.scopeId, uid)
    if (active) roleResult.value = data.reason === 'bot_admin_not_proven'
      ? '平台未能核验 Bot 的管理员身份。'
      : uid + '：' + (({ owner: '群主', admin: '群管理员', member: '普通成员' } as Record<string, string>)[data.role] || '未能确认身份')
  } catch { if (active) roleResult.value = '无法核对该成员的角色。' }
  finally { if (active) checkingRole.value = false }
}
</script>

<style scoped>
.scope-editor { min-width: 0; }
.editor-heading,.section-heading,.actions { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.editor-heading,.section-heading { justify-content: space-between; margin: 0 0 14px; }
.editor-heading h2,.section-heading h3 { margin: 0; }
.editor-heading p,.inherit-box p,.section-heading span,.muted,small,.diagnostics p { color: var(--td-text-color-secondary); }
.inherit-box { padding: 12px 14px; background: var(--td-brand-color-light); border-radius: var(--app-radius-md); margin: 16px 0; }
.inherit-box p { margin: 4px 0 0; }
.section-heading { margin-top: 20px; }
.table-wrap { overflow-x: auto; border: 1px solid var(--td-component-border); border-radius: var(--app-radius-md); }
table { width: 100%; border-collapse: collapse; }
th,td { padding: 10px 12px; text-align: left; border-bottom: 1px solid var(--td-component-border); overflow-wrap: anywhere; }
th { background: var(--td-bg-color-secondarycontainer); font-weight: 500; }
tr:last-child td { border-bottom: 0; }
small { display: block; }
.kb-search { max-width: 420px; margin-bottom: 10px; }
.rules { display: flex; flex-wrap: wrap; gap: 12px 18px; }
label { cursor: pointer; }
input { accent-color: var(--td-brand-color); }
.diagnostics { margin-top: 22px; }
.diagnostics summary { cursor: pointer; }
.role-input { max-width: 240px; }
.impact-list { max-height: min(430px, 55vh); overflow: auto; }
.impact-list h4 { margin-bottom: 5px; }
</style>
