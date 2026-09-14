<template>
  <section class="octo-panel">
    <header><h2>Octo 群与子区</h2><p>配置知识库的使用范围。绑定仅用于查询，不授予资料维护权限。</p></header>
    <t-alert theme="info">当前为管理员配置，尚未切换群内问答。连接 Octo 后，群和子区名称直接从平台核对；失败时保留上次名称并提示状态。</t-alert>
    <p v-if="error" role="alert" class="error">{{ error }} <t-button variant="text" @click="load">重试</t-button></p>
    <div class="toolbar">
      <t-button :loading="loading" @click="load">刷新</t-button>
      <t-button @click="showConnection = true">配置 Octo 连接</t-button>
      <t-button :disabled="!connections.length" @click="showCreate = true">接入群／子区</t-button>
      <span>已加载 {{ scopes.length }} 个区域</span>
    </div>
    <t-loading :loading="loading">
      <p v-if="!loading && !scopes.length && !error">尚未登记区域。先登记主群，再添加该群子区。</p>
      <div class="layout">
        <nav aria-label="群与子区">
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
          <label>显示名称<t-input v-model="editName" :maxlength="256" :readonly="selected.name_source === 'octo'" /></label>
          <label v-if="selected.subarea_id" class="inherit"><t-switch v-model="editInherit" />继承主群知识库（不继承问题记录或维护权）</label>
          <t-button :loading="saving" :disabled="!editName.trim()" @click="saveScope">保存区域设置</t-button>
          <h3>最终生效的知识库</h3>
          <p v-if="bindingError" role="alert" class="error">{{ bindingError }}</p>
          <t-loading :loading="bindingLoading">
            <p v-if="!bindingLoading && !bindings.length && !bindingError">没有绑定知识库；不会回退查询其他库。</p>
            <ul><li v-for="b in bindings" :key="`${b.from_scope_id}:${b.knowledge_base_id}`">
              <router-link :to="{ name: 'knowledgeBaseDetail', params: { kbId: b.knowledge_base_id } }">{{ kbName(b.knowledge_base_id) }}</router-link>
              <t-tag size="small" :theme="b.inherited ? 'default' : 'primary'">{{ b.inherited ? '继承主群' : '本区绑定' }}</t-tag>
              <t-popconfirm v-if="!b.inherited" content="只解除本区绑定，知识库和其他区域保持不变。" @confirm="removeBinding(b.from_scope_id, b.knowledge_base_id)"><t-button variant="text" theme="danger" :disabled="saving">解除绑定</t-button></t-popconfirm>
              <t-button v-else variant="text" @click="openParent(b.from_scope_id)">前往主群管理</t-button>
            </li></ul>
          </t-loading>
          <div class="toolbar"><t-select v-model="kbToBind" placeholder="选择知识库" filterable clearable :options="kbOptions" /><t-button :disabled="!kbToBind || saving || bindingLoading" @click="addBinding">绑定知识库</t-button></div>
          <p>这里只管理使用关系。资料新增、编辑和解析仍在知识库页面完成。</p>
          <h3>核对群成员角色</h3>
          <p>仅检查指定 UID，不列出成员名单；群管理身份不等于知识库维护权限。</p>
          <div class="toolbar"><t-input v-model="roleUID" placeholder="输入原生 UID" /><t-button :disabled="!roleUID.trim()" :loading="roleLoading" @click="checkRole">核对角色</t-button></div>
          <p v-if="roleResult" role="status">{{ roleResult }}</p>
        </div>
        <p v-else-if="scopes.length">选择左侧主群或子区查看绑定。</p>
      </div>
      <t-button v-if="hasMore" variant="outline" :loading="loading" @click="loadMore">加载更多区域</t-button>
    </t-loading>
    <t-dialog v-model:visible="showCreate" header="登记 Octo 区域" :confirm-loading="saving" @confirm="create">
      <p>从 Octo 复制真实 ID，名称由平台返回。子区必须先接入相同 Bot 账号和群 ID 的主群；无需填写显示名称。</p>
      <div class="form">
        <label>Octo 连接<t-select v-model="draft.account_id" :options="connections.map(c => ({ label: c.account_id, value: c.account_id }))" /></label>
        <label>群 ID<t-input v-model="draft.group_id" :maxlength="128" /></label>
        <label>子区 ID（主群留空）<t-input v-model="draft.subarea_id" :maxlength="128" /></label>
      </div>
    </t-dialog>
    <t-dialog v-model:visible="showConnection" header="配置 Octo 连接" :confirm-loading="saving" @confirm="connect" @closed="connectionToken = ''">
      <p>连接使用官方 Octo 服务。账号标识应与 OpenClaw 中的账号标识一致，例如 octo-xiaoqiu。同一标识再次保存会替换凭据，需要重新验证关联区域。</p>
      <label>账号标识<t-input v-model="connectionAccount" :maxlength="128" autocomplete="off" /></label>
      <label>Bot Token<t-input v-model="connectionToken" type="password" autocomplete="new-password" /></label>
      <p>密钥只提交用于加密保存，不会从服务器回传。未配置原生加密主密钥时服务器会拒绝保存。</p>
    </t-dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { listKnowledgeBases } from '@/api/knowledge-base'
import { useAuthStore } from '@/stores/auth'
import { listScopes, createScope, updateScope, effectiveBindings, bindKB, unbindKB, listConnections, saveConnection, syncScopeName, inspectMemberRole, type OctoScope, type EffectiveBinding } from '@/api/octo'

const scopes = ref<OctoScope[]>([]), selected = ref<OctoScope | null>(null)
const kbs = ref<Array<{ id: string; name: string }>>([]), bindings = ref<EffectiveBinding[]>([])
const loading = ref(false), saving = ref(false), bindingLoading = ref(false), hasMore = ref(false), showCreate = ref(false)
const error = ref(''), bindingError = ref(''), editName = ref(''), editInherit = ref(false), kbToBind = ref('')
const draft = ref({ account_id: '', group_id: '', subarea_id: '', display_name: '', inherit_parent: false })
const connections = ref<Array<{ account_id: string; updated_at: string }>>([])
const showConnection = ref(false), connectionAccount = ref(''), connectionToken = ref('')
const roleUID = ref(''), roleResult = ref(''), roleLoading = ref(false)
const statusLabel = (status: string) => ({ verified: '已验证', error: '同步失败，显示上次名称', needs_refresh: '连接已变更，待重新验证', unverified: '未验证' }[status] || '未验证')
const auth = useAuthStore()
let selectionVersion = 0, loadVersion = 0
const groups = computed(() => scopes.value.filter(s => !s.subarea_id))
const children = (g: OctoScope) => scopes.value.filter(s => s.subarea_id && s.account_id === g.account_id && s.group_id === g.group_id)
const kbName = (id: string) => kbs.value.find(k => k.id === id)?.name || id
const kbOptions = computed(() => kbs.value.filter(k => !bindings.value.some(b => b.knowledge_base_id === k.id && !b.inherited)).map(k => ({ label: k.name, value: k.id })))

async function load() {
  const version = ++loadVersion
  loading.value = true; error.value = ''
  try {
    const [s, k, c] = await Promise.all([listScopes(), listKnowledgeBases(), listConnections()])
    if (version !== loadVersion) return
    scopes.value = s.data; kbs.value = k.data || []; hasMore.value = s.data.length === 100
    connections.value = c.data
    const fresh = scopes.value.find(s => s.id === selected.value?.id)
    if (fresh) await select(fresh)
    else { selected.value = null; bindings.value = []; selectionVersion++ }
  } catch { if (version === loadVersion) { error.value = '无法加载区域配置，请确认当前工作区管理员权限和服务状态。'; scopes.value = []; selected.value = null; bindings.value = []; kbs.value = []; selectionVersion++ } }
  finally { if (version === loadVersion) loading.value = false }
}
async function loadMore() {
  const version = ++loadVersion
  loading.value = true
  try { const r = await listScopes(scopes.value.length); if (version === loadVersion) { scopes.value.push(...r.data); hasMore.value = r.data.length === 100 } }
  catch { if (version === loadVersion) error.value = '加载更多区域失败，请重试。' }
  finally { if (version === loadVersion) loading.value = false }
}
async function select(scope: OctoScope) {
  const version = ++selectionVersion
  selected.value = scope; editName.value = scope.display_name; editInherit.value = scope.inherit_parent
  roleResult.value = ''; roleUID.value = ''; roleLoading.value = false
  bindings.value = []; bindingError.value = ''; kbToBind.value = ''; bindingLoading.value = true
  try { const r = await effectiveBindings(scope.id); if (version === selectionVersion) bindings.value = r.data }
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
function saveScope() { const s = selected.value; if (s) return mutate(() => updateScope(s.id, editName.value.trim(), editInherit.value)) }
function addBinding() { const id = selected.value?.id, kb = kbToBind.value; if (id && kb) return mutate(() => bindKB(id, kb)) }
function removeBinding(scope: string, kb: string) { return mutate(() => unbindKB(scope, kb)) }
function openParent(id: string) { const s = scopes.value.find(s => s.id === id); if (s) void select(s) }
function refreshName() { const id = selected.value?.id; if (id) return mutate(async () => { try { await syncScopeName(id) } catch (e) { await load(); throw e } }) }
function connect() {
  if (!connectionAccount.value.trim() || !connectionToken.value.startsWith('bf_')) { void MessagePlugin.warning('请填写账号标识和 User Bot Token'); return }
  const account = connectionAccount.value.trim(), token = connectionToken.value
  return mutate(async () => { await saveConnection(account, token); connectionToken.value = ''; showConnection.value = false })
}
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
function create() {
  if (!draft.value.account_id.trim() || !draft.value.group_id.trim()) { void MessagePlugin.warning('请选择连接并填写群 ID'); return }
  const data = { ...draft.value }
  return mutate(async () => { await createScope(data); showCreate.value = false; draft.value = { account_id: '', group_id: '', subarea_id: '', display_name: '', inherit_parent: false } })
}
onMounted(load)
watch(() => auth.currentTenantId, () => {
  selectionVersion++; loadVersion++; scopes.value = []; selected.value = null; bindings.value = []; kbs.value = []; connections.value = []; showCreate.value = false; showConnection.value = false; connectionToken.value = ''; roleResult.value = ''
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
