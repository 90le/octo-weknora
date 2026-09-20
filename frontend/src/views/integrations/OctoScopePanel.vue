<template>
  <section class="octo-panel">
    <header><h2>Octo 群与子区</h2><p>配置知识库的查询和维护范围。查询绑定默认只读，维护必须单独授权。</p></header>
    <t-alert theme="info">先在 IM 集成启用 Bot 渠道，再为群／子区绑定知识库。子区默认独立；继承知识不会共享其他区域的对话或问题记录。</t-alert>
    <p v-if="error" role="alert" class="error">{{ error }} <t-button variant="text" @click="load">重试</t-button></p>
    <div class="toolbar">
      <t-button :loading="loading" @click="load">刷新</t-button>
      <t-button @click="showConnection = true">配置 Octo 连接</t-button>
      <t-button :disabled="!connections.length" @click="showCreate = true">接入群／子区</t-button>
      <router-link :to="{ path: '/platform/settings', query: { section: 'integration-im' } }">管理 Bot 渠道与私聊</router-link>
      <span>已加载 {{ scopes.length }} 个区域</span>
    </div>
    <t-loading :loading="loading">
      <t-empty v-if="!loading && !scopes.length && !error" :description="connections.length ? '还没有接入区域。接入主群后绑定知识库，即可开始群内问答。' : '先配置一个 Octo Bot 连接，再接入群和知识库。'" />
      <div class="layout">
        <nav aria-label="群与子区">
          <t-input v-model="search" clearable placeholder="搜索群、子区或 Bot" aria-label="搜索群与子区" />
          <div v-for="group in groups" :key="group.id" class="group">
            <button :class="{ selected: selected?.id === group.id }" @click="select(group)">{{ group.display_name }} <small>主群</small></button>
            <button v-for="child in children(group)" :key="child.id" class="child" :class="{ selected: selected?.id === child.id }" @click="select(child)">{{ child.display_name }} <small>子区</small></button>
          </div>
        </nav>
        <div v-if="selected" class="detail">
          <h3>{{ selected.display_name }}</h3>
          <dl><dt>Bot 账号</dt><dd>{{ selected.account_id }}</dd><dt>群 ID</dt><dd>{{ selected.group_id }}</dd><dt v-if="selected.subarea_id">子区 ID</dt><dd v-if="selected.subarea_id">{{ selected.subarea_id }}</dd></dl>
          <p>名称来源：{{ selected.name_source === 'octo' ? 'Octo 平台' : '旧人工配置' }} · {{ statusLabel(selected.sync_status) }}</p>
          <p v-if="selected.verified_at">上次验证：{{ new Date(selected.verified_at).toLocaleString() }}</p>
          <t-button :loading="saving" @click="refreshName">同步平台名称</t-button>
          <p v-if="selected.name_source === 'octo'">名称来自 Octo，改名后点击“同步平台名称”更新。</p>
          <label v-else>显示名称<t-input v-model="editName" :maxlength="256" /></label>
          <label v-if="selected.subarea_id" class="inherit"><t-switch v-model="editInherit" />继承主群知识库（不继承问题记录或维护权）</label>
          <label v-if="!selected.subarea_id" class="inherit"><t-switch v-model="editAggregate" />允许主群汇总该群子区的问题记录</label>
          <label class="inherit"><t-switch v-model="editCreation" />允许本区已验证的群管理员创建知识库</label>
          <p>建库授权仅限当前区域；不会获得工作区其他知识库权限。汇总问题与继承知识独立配置。</p>
          <t-button :loading="saving" :disabled="!editName.trim()" @click="saveScope">保存区域设置</t-button>
          <h3>最终生效的知识库</h3>
          <p v-if="bindingError" role="alert" class="error">{{ bindingError }}</p>
          <t-loading :loading="bindingLoading">
            <p v-if="!bindingLoading && !bindings.length && !bindingError">没有绑定知识库；不会回退查询其他库。</p>
            <ul><li v-for="b in bindings" :key="`${b.from_scope_id}:${b.knowledge_base_id}`">
              <router-link :to="{ name: 'knowledgeBaseDetail', params: { kbId: b.knowledge_base_id } }">{{ kbName(b.knowledge_base_id) }}</router-link>
              <t-tag size="small" :theme="b.inherited ? 'default' : 'primary'">{{ b.inherited ? '继承主群' : '本区绑定' }}</t-tag>
              <label v-if="!b.inherited" class="inherit"><t-switch :value="b.can_manage" :disabled="saving" @change="setMaintenance(b, $event)" size="small" />允许群管理员维护</label>
              <t-tag v-else size="small">仅查询</t-tag>
              <t-popconfirm v-if="!b.inherited" content="只停止本区直接查询，资料与已有管理授权保留；管理授权需要单独撤销。" @confirm="removeBinding(b.from_scope_id, b.knowledge_base_id)"><t-button variant="text" theme="danger" :disabled="saving">解除查询绑定</t-button></t-popconfirm>
              <t-button v-else variant="text" @click="openParent(b.from_scope_id)">前往主群管理</t-button>
            </li></ul>
          </t-loading>
          <div class="toolbar"><t-select v-model="kbToBind" placeholder="选择知识库" filterable clearable :options="kbOptions" /><t-button :disabled="!kbToBind || saving || bindingLoading" @click="addBinding">绑定知识库</t-button></div>
          <p>维护权只授予本区经 Octo 核验的管理员，不授予普通成员。也可直接在知识库页面维护资料。</p>
          <h3>已授权管理的知识库</h3>
          <p>管理授权与查询绑定独立。解除查询不会让已委托维护的资料失去管理员；如需收回维护权，请明确撤销。</p>
          <p v-if="!bindingLoading && !managed.length && !bindingError">本区尚未获授知识库维护权限。</p>
          <ul><li v-for="id in managed" :key="id"><router-link :to="{name:'knowledgeBaseDetail',params:{kbId:id}}">{{ kbName(id) }}</router-link><t-tag v-if="!bindings.some(b=>!b.inherited&&b.knowledge_base_id===id)" size="small" theme="warning">未直接绑定查询</t-tag><t-button v-if="!bindings.some(b=>!b.inherited&&b.knowledge_base_id===id)" variant="text" :disabled="saving" @click="rebindManaged(id)">绑定查询</t-button><t-popconfirm content="撤销本区管理员对此知识库的维护权；已有查询绑定和知识资料不变。" @confirm="revokeManagement(id)"><t-button variant="text" theme="danger" :disabled="saving">撤销管理授权</t-button></t-popconfirm></li></ul>
          <h3>核对群成员角色</h3>
          <p>仅检查指定 UID，不列出成员名单；群管理身份不等于知识库维护权限。</p>
          <div class="toolbar"><t-input v-model="roleUID" placeholder="输入原生 UID" /><t-button :disabled="!roleUID.trim()" :loading="roleLoading" @click="checkRole">核对角色</t-button></div>
          <p v-if="roleResult" role="status">{{ roleResult }}</p>
        </div>
        <p v-else-if="scopes.length">选择左侧主群或子区查看绑定。</p>
      </div>
    </t-loading>
    <OctoScopeCreateDialog v-model:visible="showCreate" :connections="connections" :scopes="scopes" @saved="onScopeCreated" />
    <OctoConnectionDialog v-model:visible="showConnection" @saved="load" />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { listKnowledgeBases } from '@/api/knowledge-base'
import { useAuthStore } from '@/stores/auth'
import { useRoute } from 'vue-router'
import OctoConnectionDialog from './OctoConnectionDialog.vue'
import OctoScopeCreateDialog from './OctoScopeCreateDialog.vue'
import { listScopes, updateScope, effectiveBindings, managedKBs, revokeKBManagement, bindKB, unbindKB, listConnections, syncScopeName, inspectMemberRole, type OctoScope, type EffectiveBinding } from '@/api/octo'

const scopes = ref<OctoScope[]>([]), selected = ref<OctoScope | null>(null)
const kbs = ref<Array<{ id: string; name: string }>>([]), bindings = ref<EffectiveBinding[]>([])
const managed=ref<string[]>([])
const loading = ref(false), saving = ref(false), bindingLoading = ref(false), showCreate = ref(false)
const error = ref(''), bindingError = ref(''), editName = ref(''), editInherit = ref(false), kbToBind = ref('')
const editCreation=ref(false),editAggregate=ref(false)
const search = ref(''), route = useRoute()
const connections = ref<Array<{ account_id: string; updated_at: string }>>([])
const showConnection = ref(false)
const roleUID = ref(''), roleResult = ref(''), roleLoading = ref(false)
const statusLabel = (status: string) => ({ verified: '已验证', error: '同步失败，显示上次名称', needs_refresh: '连接已变更，待重新验证', unverified: '未验证' }[status] || '未验证')
const auth = useAuthStore()
let selectionVersion = 0, loadVersion = 0
const matches = (s: OctoScope) => `${s.display_name} ${s.account_id} ${s.group_id} ${s.subarea_id}`.toLowerCase().includes(search.value.trim().toLowerCase())
const groups = computed(() => scopes.value.filter(s => !s.subarea_id && (matches(s) || children(s).some(matches))))
const children = (g: OctoScope) => scopes.value.filter(s => s.subarea_id && s.account_id === g.account_id && s.group_id === g.group_id)
const kbName = (id: string) => kbs.value.find(k => k.id === id)?.name || id
const kbOptions = computed(() => kbs.value.filter(k => !bindings.value.some(b => b.knowledge_base_id === k.id && !b.inherited)).map(k => ({ label: k.name, value: k.id })))

async function load() {
  const version = ++loadVersion
  loading.value = true; error.value = ''
  try {
    const [s, k, c] = await Promise.all([listScopes(), listKnowledgeBases(), listConnections()])
    if (version !== loadVersion) return
    const all = [...s.data]
    let count = s.data.length
    while (count === 100) {
      const next = await listScopes(all.length)
      if (version !== loadVersion) return
      all.push(...next.data); count = next.data.length
    }
    scopes.value = all; kbs.value = k.data || []
    connections.value = c.data
    const fresh = scopes.value.find(s => s.id === (selected.value?.id || route.query.scope)) || scopes.value[0]
    if (fresh) await select(fresh)
    else { selected.value = null; bindings.value = []; selectionVersion++ }
  } catch { if (version === loadVersion) { error.value = '无法加载区域配置，请确认当前工作区管理员权限和服务状态。'; scopes.value = []; selected.value = null; bindings.value = []; kbs.value = []; selectionVersion++ } }
  finally { if (version === loadVersion) loading.value = false }
}
async function select(scope: OctoScope) {
  const version = ++selectionVersion
  selected.value = scope; editName.value = scope.display_name; editInherit.value = scope.inherit_parent
  editCreation.value=Boolean(scope.allow_knowledge_creation);editAggregate.value=Boolean(scope.aggregate_child_issues)
  roleResult.value = ''; roleUID.value = ''; roleLoading.value = false
  bindings.value = []; managed.value=[]; bindingError.value = ''; kbToBind.value = ''; bindingLoading.value = true
  try { const [r,m] = await Promise.all([effectiveBindings(scope.id),managedKBs(scope.id)]); if (version === selectionVersion) {bindings.value = r.data;managed.value=m.data} }
  catch { if (version === selectionVersion) bindingError.value = '绑定读取失败，不能据此判断该区域没有知识库。' }
  finally { if (version === selectionVersion) bindingLoading.value = false }
}
async function mutate(operation: () => Promise<unknown>) {
  if (saving.value) return
  saving.value = true
  try { await operation(); await MessagePlugin.success('已保存'); await load() }
  catch (e) {
    const message = (e as { response?: { data?: { error?: string } } }).response?.data?.error || ''
    await MessagePlugin.error(message.includes('SYSTEM_AES_KEY') ? '服务器尚未配置 WeKnora 原生加密主密钥，连接未保存。' : '未能完成操作，请检查连接、群 ID、权限和服务状态。')
  }
  finally { saving.value = false }
}
function saveScope() { const s = selected.value; if (s) return mutate(() => updateScope(s.id, editName.value.trim(), editInherit.value,editCreation.value,editAggregate.value)) }
function setMaintenance(b:EffectiveBinding,value:unknown){if(!b.inherited)return mutate(()=>bindKB(b.from_scope_id,b.knowledge_base_id,Boolean(value)))}
function addBinding() { const id = selected.value?.id, kb = kbToBind.value; if (id && kb) return mutate(() => bindKB(id, kb)) }
function rebindManaged(kb:string){const id=selected.value?.id;if(id)return mutate(()=>bindKB(id,kb))}
function revokeManagement(kb:string){const id=selected.value?.id;if(id)return mutate(()=>revokeKBManagement(id,kb))}
function removeBinding(scope: string, kb: string) { return mutate(() => unbindKB(scope, kb)) }
function openParent(id: string) { const s = scopes.value.find(s => s.id === id); if (s) void select(s) }
function refreshName() { const id = selected.value?.id; if (id) return mutate(async () => { try { await syncScopeName(id) } catch (e) { await load(); throw e } }) }
async function checkRole() {
  const s = selected.value, version = selectionVersion, uid = roleUID.value.trim()
  if (!s || !uid) return
  roleLoading.value = true; roleResult.value = ''
  try {
    const r = await inspectMemberRole(s.id, uid)
    if (version !== selectionVersion) return
    const names: Record<string, string> = { owner: '群主', admin: '群管理员', member: '普通成员', unknown: '未能确认身份' }
    roleResult.value = r.data.reason === 'bot_admin_not_proven'
      ? `${r.data.uid}：Bot 管理身份待验证。平台未提供可验证的 Bot 管理字段，不能据此授权。`
      : `${r.data.uid}：${names[r.data.role] || '未知角色'}。此结果仅核对群角色，不授予知识库权限。`
  } catch { if (version === selectionVersion) roleResult.value = '角色核对失败，未作授权判断。' }
  finally { if (version === selectionVersion) roleLoading.value = false }
}
async function onScopeCreated(scope: OctoScope) { selected.value=scope; await load(); await select(scope); await MessagePlugin.success('区域已接入，请绑定知识库') }
onMounted(load)
watch(() => auth.currentTenantId, () => {
  selectionVersion++; loadVersion++; scopes.value = []; selected.value = null; bindings.value = []; managed.value=[]; kbs.value = []; connections.value = []; showCreate.value = false; showConnection.value = false; roleResult.value = ''
  void load()
})
onBeforeUnmount(() => { selectionVersion++; loadVersion++ })
</script>

<style scoped>
.octo-panel { max-width: 1160px; } header p, .detail p { color: var(--td-text-color-secondary); line-height: 1.6; }
.toolbar { display: flex; align-items: center; gap: 12px; margin: 20px 0; flex-wrap: wrap; }
.layout { display: grid; grid-template-columns: minmax(200px, 30%) 1fr; gap: 24px; }
nav { border-right: 1px solid var(--td-component-border); padding-right: 16px; }
nav button { display: block; width: 100%; text-align: left; padding: 12px; border: 0; border-radius: 6px; background: transparent; color: var(--td-text-color-primary); cursor: pointer; overflow-wrap: anywhere; }
nav button:hover, nav button.selected { background: var(--td-brand-color-light); } nav button.child { padding-left: 30px; } small { color: var(--td-text-color-secondary); margin-left: 8px; }
.detail { min-width: 0; } dl { display: grid; grid-template-columns: 90px 1fr; gap: 8px; } dd { margin: 0; overflow-wrap: anywhere; }
label { display: grid; gap: 8px; margin: 16px 0; } .inherit { display: flex; align-items: center; }
ul { padding: 0; } li { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 12px 0; border-bottom: 1px solid var(--td-component-border); }
.error { color: var(--td-error-color); } .toolbar .t-select__wrap { min-width: 220px; flex: 1; }
@media(max-width: 760px) { .layout { grid-template-columns: 1fr; } nav { border-right: 0; border-bottom: 1px solid var(--td-component-border); padding-bottom: 12px; } }
</style>
