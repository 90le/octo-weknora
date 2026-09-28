<template>
  <t-button variant="text" @click="open">Octo 使用范围</t-button>
  <t-drawer v-model:visible="visible" attach="body" header="Octo 使用范围" :footer="false" size="min(720px, 100vw)" :close-btn="true" :close-on-esc-keydown="true">
    <p class="intro">这里展示本知识库在各群和子区的实际查询范围，以及分别授予的维护权限。工作区管理员在此配置跨区域关系；获授权的群管理者可在本群向小丘提案，并由本人确认本区知识库变更。</p>
    <t-alert theme="info">子区继承主群时无需重复绑定。维护授权不随查询范围继承；解除查询绑定也不会删除资料或撤销已有维护授权。</t-alert>
    <t-loading :loading="loading">
      <p v-if="error" role="alert">{{ error }} <t-button variant="text" @click="load">重试</t-button></p>
      <t-empty v-else-if="!loading && !groups.length" description="此知识库尚未向 Octo 区域开放查询或维护" />
      <div v-if="!error && groups.length" class="use-groups">
        <section v-for="group in groups" :key="group.key" class="use-group">
          <header class="group-heading">
            <div><strong>{{ group.parentScope?.display_name || '主群名称暂不可用' }}</strong><small>主群 · Bot：{{ group.accountId }}</small></div>
            <t-tag v-if="group.parentScope && !nameVerified(group.parentScope)" size="small" theme="warning" variant="light">主群名称待核验</t-tag>
          </header>
          <ul>
            <li v-for="row in group.uses" :key="row.scope_id">
              <div class="use-detail">
                <div class="use-title"><strong>{{ row.display_name }}</strong><t-tag size="small" variant="light">{{ row.subarea_id ? '子区' : '主群' }}</t-tag><t-tag v-if="!nameVerified(row)" size="small" theme="warning" variant="light">名称待核验</t-tag></div>
                <p><span>{{ queryLabel(row) }}</span><span class="separator">·</span><span>{{ row.can_manage ? '本区域已授权维护' : '未授权维护' }}</span></p>
                <p v-if="row.query_mode === 'inherited'" class="source-note">查询来自主群「{{ sourceName(row) }}」，在主群调整此绑定。</p>
                <small v-if="row.verified_at" class="verified-time">平台名称上次核验：{{ new Date(row.verified_at).toLocaleString() }}</small>
              </div>
              <div class="actions">
                <router-link :to="scopeRoute(row.query_mode === 'inherited' ? row.from_scope_id : row.scope_id)">{{ row.query_mode === 'inherited' ? '查看主群设置' : '区域设置' }}</router-link>
                <t-popconfirm v-if="row.query_mode === 'direct'" :content="unbindImpact(row)" @confirm="remove(row.scope_id)"><t-button theme="danger" variant="text" :disabled="saving">解除直接绑定</t-button></t-popconfirm>
              </div>
            </li>
          </ul>
        </section>
      </div>
    </t-loading>
    <label>增加查询区域<t-select v-model="selectedScope" :options="options" filterable clearable placeholder="选择群或子区" /></label>
    <p class="hint">可为正在继承主群的子区建立独立绑定。这里只增加查询权限；维护授权请到区域设置中单独调整。</p>
    <t-button :loading="saving" :disabled="!selectedScope || loading || Boolean(error)" @click="add">绑定到所选区域</t-button>
    <p><router-link :to="{ name: 'octoGroups' }">接入新的群／子区</router-link></p>
    <p><router-link :to="{ name: 'knowledgeContacts', params: { kbId } }">管理此知识库的联系人</router-link></p>
  </t-drawer>
</template>
<script setup lang="ts">
import { computed, ref, watch, onBeforeUnmount } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useAuthStore } from '@/stores/auth'
import { listScopes, effectiveKBUses, bindKB, unbindKB, type EffectiveScopeUse, type OctoScope } from '@/api/octo'
import { affectedInheritedUses, directChildFallsBackToParent, effectiveUseGroups, scopeUnbindFingerprint } from './octoKBUsesDisplay'

const props = defineProps<{ kbId: string }>()
const auth = useAuthStore()
const visible = ref(false), loading = ref(false), saving = ref(false), error = ref(''), selectedScope = ref('')
const scopes = ref<OctoScope[]>([]), rows = ref<EffectiveScopeUse[]>([])
const groups = computed(() => effectiveUseGroups(rows.value, scopes.value))
const options = computed(() => scopes.value.filter(scope => !rows.value.some(row => row.scope_id === scope.id && row.query_mode === 'direct'))
  .map(scope => ({ value: scope.id, label: (scope.subarea_id ? '子区 · ' + parentName(scope) + ' / ' : '主群 · ') + scope.display_name + ' · ' + scope.account_id }))
  .sort((a, b) => a.label.localeCompare(b.label, 'zh-CN')))
let version = 0

watch(() => props.kbId, reset)
watch(() => auth.currentTenantId, reset)
watch(visible, open => { if (!open) version++ })
onBeforeUnmount(() => { version++ })

function reset() { version++; visible.value = false; rows.value = []; scopes.value = []; selectedScope.value = ''; error.value = '' }
function nameVerified(row: Pick<OctoScope, 'name_source' | 'sync_status' | 'verified_at'>): boolean { return row.name_source === 'octo' && row.sync_status === 'verified' && Boolean(row.verified_at) }
function parentName(scope: OctoScope): string { return scopes.value.find(parent => parent.account_id === scope.account_id && parent.group_id === scope.group_id && !parent.subarea_id)?.display_name || '主群名称暂不可用' }
function sourceName(row: EffectiveScopeUse): string { return scopes.value.find(scope => scope.id === row.from_scope_id)?.display_name || '主群名称暂不可用' }
function scopeRoute(id: string) { return { name: 'octoGroups', query: { scope: id } } }
function queryLabel(row: EffectiveScopeUse): string { return { direct: '本区域直接可查', inherited: '继承主群可查', none: '不可查询（仅维护）' }[row.query_mode] }
function unbindImpact(row: EffectiveScopeUse): string {
  if (!row.subarea_id) {
    const children = affectedInheritedUses(row, rows.value)
    if (children.length) {
      const shown = children.slice(0, 3).map(child => child.display_name).join('、')
      const remaining = children.length > 3 ? '等' : ''
      return '解除主群的直接绑定后，' + shown + remaining + '共 ' + children.length + ' 个子区将失去继承查询；其他独立绑定与维护授权保留。'
    }
  }
  if (directChildFallsBackToParent(row, rows.value, scopes.value)) return '解除这个子区的直接绑定后，它仍可继承主群查询。本区的维护授权和知识资料保留。'
  return '仅解除本区域的直接查询绑定；本区域将不能通过此绑定查询。独立维护授权和知识资料保留。'
}

async function open() { visible.value = true; await load() }
async function load() {
  const v = ++version; loading.value = true; error.value = ''; rows.value = []; scopes.value = []
  try {
    const result = await effectiveKBUses(props.kbId)
    if (result.success !== true || !Array.isArray(result.data)) throw new Error('invalid effective scope response')
    let offset = 0; const all: OctoScope[] = []
    while (v === version) {
      const batch = await listScopes(offset)
      if (batch.success !== true || !Array.isArray(batch.data)) throw new Error('invalid scope response')
      all.push(...batch.data)
      if (batch.data.length < 100) break
      offset += 100
    }
    if (v === version) { rows.value = result.data; scopes.value = all }
  } catch { if (v === version) error.value = '有效使用范围读取失败，当前结果不能当成无绑定或无授权。请检查工作区权限与服务状态。' }
  finally { if (v === version) loading.value = false }
}
async function change(operation: () => Promise<unknown>) {
  if (saving.value) return
  const tenant = auth.currentTenantId, kb = props.kbId
  saving.value = true
  try {
    await operation()
    if (tenant !== auth.currentTenantId || kb !== props.kbId) return
    selectedScope.value = ''; await load(); await MessagePlugin.success('使用范围已更新')
  } catch (e) {
    if (tenant === auth.currentTenantId && kb === props.kbId) {
      if (e instanceof Error && e.message === 'stale_scope_preview') {
        await load()
        await MessagePlugin.warning('使用范围已变化，请重新查看影响后再确认。')
      } else await MessagePlugin.error('操作结果未确认，请刷新使用范围后核对。')
    }
  } finally { saving.value = false }
}
function add() { const id = selectedScope.value, kb = props.kbId; if (id) return change(() => bindKB(id, kb)) }
function remove(id: string) {
  const kb = props.kbId, scope = scopes.value.find(item => item.id === id)
  if (!scope) { void MessagePlugin.error('区域资料已变化，请重新读取使用范围。'); return }
  const expected = scopeUnbindFingerprint(scope, rows.value)
  return change(async () => {
    const fresh = await effectiveKBUses(kb)
    if (fresh.success !== true || !Array.isArray(fresh.data)) throw new Error('impact unavailable')
    if (scopeUnbindFingerprint(scope, fresh.data) !== expected) throw new Error('stale_scope_preview')
    await unbindKB(id, kb)
  })
}
</script>
<style scoped>
.intro,.hint { color:var(--td-text-color-secondary);line-height:1.65;font-size:13px; }
.use-groups { display:flex;flex-direction:column;gap:18px;margin-top:20px; }
.use-group { border:1px solid var(--td-component-border);border-radius:8px;background:var(--td-bg-color-container);overflow:hidden; }
.group-heading { display:flex;align-items:flex-start;justify-content:space-between;gap:12px;padding:14px 16px;background:var(--td-bg-color-secondarycontainer); }
.group-heading strong { display:block;font-size:14px; }.group-heading small { display:block;margin-top:4px;color:var(--td-text-color-secondary);font-size:12px; }
ul { padding:0;margin:0;list-style:none; }
li { padding:14px 16px;overflow-wrap:anywhere;border-top:1px solid var(--td-component-border);display:flex;justify-content:space-between;gap:14px; }
.use-detail { min-width:0; }.use-title { display:flex;align-items:center;gap:8px;flex-wrap:wrap; }.use-title strong { font-weight:500; }
.use-detail p { margin:8px 0 0;color:var(--td-text-color-secondary);line-height:1.5;font-size:12px; }.use-detail .source-note { color:var(--td-brand-color); }
.separator { margin:0 7px; }.verified-time { display:block;margin-top:7px;color:var(--td-text-color-placeholder);font-size:11px; }
.actions { display:flex;align-items:flex-start;justify-content:flex-end;gap:8px;flex-wrap:wrap;flex-shrink:0;font-size:12px; }.actions a { padding-top:5px; }
label { display:grid;gap:8px;margin:22px 0 8px; }.hint { margin:0 0 12px; }
@media (max-width:560px) { li { flex-direction:column; }.actions { justify-content:flex-start; } }
</style>
