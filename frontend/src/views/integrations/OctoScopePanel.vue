<template>
  <section class="octo-panel">
    <header class="page-heading">
      <div><h2>群与知识库</h2><p>选择 Octo 群或子区，查看它能查询哪些知识库、哪些已开放维护。</p></div>
      <div class="heading-actions">
        <t-button variant="outline" :loading="loading" :disabled="saving" @click="load">刷新</t-button>
        <t-button :disabled="!connections.length" @click="showCreate = true"><template #icon><t-icon name="add" /></template>接入群／子区</t-button>
      </div>
    </header>

    <t-alert v-if="error" theme="error">{{ error }} <t-button variant="text" @click="load">重试</t-button></t-alert>
    <t-loading :loading="loading">
      <div v-if="!scopes.length && !error && !loading" class="empty-setup">
        <t-empty :description="connections.length ? '先接入一个 Octo 群，再为它绑定知识库。' : '先连接 Octo Bot，再选择它所在的群。'" />
        <t-button v-if="!connections.length" @click="showConnection = true">连接 Octo Bot</t-button>
        <t-button v-else @click="showCreate = true">接入第一个群</t-button>
        <router-link :to="imSettings">Bot 渠道与私聊设置</router-link>
      </div>

      <div v-else-if="scopes.length" class="scope-layout">
        <aside class="scope-tree" aria-label="Octo 群与子区">
          <div class="tree-heading"><strong>Octo 区域</strong><small>{{ groupCount }} 个群 · {{ childCount }} 个子区</small></div>
          <t-input v-model="search" clearable placeholder="搜索群或子区" aria-label="搜索群或子区"><template #prefix-icon><t-icon name="search" /></template></t-input>
          <nav aria-label="选择群或子区">
            <div v-for="group in groups" :key="group.scope.id" class="tree-group">
              <div class="group-node">
                <button v-if="group.children.length" type="button" class="tree-toggle" :aria-label="`${expanded(group.scope.id) ? '收起' : '展开'} ${group.scope.display_name} 的子区`" :aria-expanded="expanded(group.scope.id)" @click="toggleGroup(group.scope.id)"><t-icon :name="expanded(group.scope.id) ? 'chevron-down' : 'chevron-right'" /></button>
                <span v-else class="tree-toggle-placeholder" />
                <button type="button" class="tree-node" :class="{selected:selected?.id===group.scope.id}" :aria-current="selected?.id===group.scope.id ? 'page' : undefined" @click="select(group.scope)"><t-icon name="chat-message" /><span>{{ group.scope.display_name }}</span><small>群</small></button>
              </div>
              <div v-if="expanded(group.scope.id)" class="children">
                <button v-for="child in group.children" :key="child.id" type="button" class="tree-node child-node" :class="{selected:selected?.id===child.id}" :aria-current="selected?.id===child.id ? 'page' : undefined" @click="select(child)"><span class="child-connector" /><span>{{ child.display_name }}</span><small>子区</small></button>
              </div>
            </div>
            <p v-if="!groups.length" class="tree-empty">没有匹配的群或子区。</p>
          </nav>
          <router-link class="tree-footer" :to="imSettings"><t-icon name="setting" /> Bot 接入与私聊</router-link>
        </aside>

        <main v-if="selected" class="scope-detail">
          <div class="selected-heading">
            <div>
              <p v-if="parentScope" class="breadcrumb"><button type="button" @click="select(parentScope)">{{ parentScope.display_name }}</button><span>／ 子区</span></p>
              <h3>{{ selected.display_name }} <t-tag size="small" variant="light">{{ selected.subarea_id ? '子区' : '主群' }}</t-tag></h3>
              <p class="scope-subtitle">{{ selected.subarea_id ? '此处设置只作用于当前子区。' : '此处设置作用于主群，子区可以独立配置。' }}</p>
            </div>
            <t-tag v-if="selected.sync_status!=='verified'" size="small" theme="warning">{{ statusLabel(selected.sync_status) }}</t-tag>
          </div>

          <div class="scope-summary" aria-label="当前区域配置摘要">
            <div><strong>{{ bindingLoading || bindingError ? '—' : queryCount }}</strong><span>可查询知识库</span></div>
            <div><strong>{{ bindingLoading || bindingError ? '—' : managementCount }}</strong><span>已授权维护</span></div>
            <div><strong>{{ selected.subarea_id && selected.inherit_parent ? '继承 + 本区' : '本区独立' }}</strong><span>知识范围</span></div>
          </div>

          <section class="knowledge-section">
            <div class="section-heading"><div><h4>知识库与权限</h4><p>查询和维护分别授权；解除查询绑定不会删除资料或撤销维护权。</p></div><t-button :disabled="bindingLoading || Boolean(bindingError)" @click="showBind = true"><template #icon><t-icon name="add" /></template>绑定知识库</t-button></div>
            <t-alert v-if="bindingError" theme="error">{{ bindingError }} <t-button variant="text" @click="select(selected)">重新读取</t-button></t-alert>
            <t-loading :loading="bindingLoading">
              <t-empty v-if="!bindingLoading && !bindingError && !knowledgeRows.length" description="本区域尚未绑定知识库，也没有单独的维护授权。" />
              <div v-if="!bindingError" class="knowledge-list">
                <article v-for="row in knowledgeRows" :key="row.knowledgeBaseId" class="knowledge-row">
                  <div class="knowledge-name"><span class="knowledge-icon"><t-icon name="folder" /></span><div><router-link :to="{name:'knowledgeBaseDetail',params:{kbId:row.knowledgeBaseId}}">{{ row.name }}</router-link><small>知识库</small></div></div>
                  <div class="permission-state"><small>本区查询</small><span :class="{muted:row.query==='none'}">{{ row.query==='direct' ? '已开放' : row.query==='inherited' ? '继承主群' : '未开放' }}</span><button v-if="row.query==='inherited'" type="button" class="inline-link" @click="openParent(row.sourceScopeId)">查看主群设置</button></div>
                  <div class="permission-state"><small>维护授权</small><t-tag :theme="row.managed ? 'success' : 'default'" variant="light" size="small">{{ row.managed ? '已授权本区管理者' : '未授权' }}</t-tag></div>
                  <div class="knowledge-actions">
                    <t-popconfirm v-if="row.query==='direct'" :content="`停止「${selected.display_name}」直接查询此库；资料和已有维护授权保留。若子区同时继承主群，继承关系仍可能提供查询。`" @confirm="removeBinding(row.knowledgeBaseId)"><t-button variant="text" :disabled="saving">解除查询</t-button></t-popconfirm>
                    <t-button v-else variant="text" :disabled="saving" @click="bindQuery(row.knowledgeBaseId)">{{ row.query==='inherited' ? '独立绑定' : '开放查询' }}</t-button>
                    <t-popconfirm v-if="row.managed" content="仅撤销本区域的维护授权，查询绑定和知识资料保持不变。" @confirm="revokeManagement(row.knowledgeBaseId)"><t-button variant="text" theme="danger" :disabled="saving">撤销维护</t-button></t-popconfirm>
                    <t-popconfirm v-else :content="grantDescription(row)" @confirm="grantManagement(row.knowledgeBaseId)"><t-button variant="text" :disabled="saving">授权维护</t-button></t-popconfirm>
                  </div>
                </article>
              </div>
            </t-loading>
            <p class="permission-note"><t-icon name="secured" /> 本区管理者指可访问本区域的群主／群管理员，以及渠道单独允许维护的 Bot；实际操作时仍会核验身份与权限。</p>
          </section>

          <section class="policy-section">
            <div class="section-heading"><div><h4>区域规则</h4><p>知识继承、问题汇总和群内建库是独立的选项。</p></div><t-button variant="outline" :disabled="!policiesChanged || saving" :loading="saving" @click="saveScope">保存规则</t-button></div>
            <div v-if="selected.subarea_id" class="policy-row"><div><strong>继承主群知识库</strong><p>增加可查询范围，不继承维护授权、对话或问题记录。</p></div><t-switch v-model="editInherit" aria-label="继承主群知识库" /></div>
            <div v-else class="policy-row"><div><strong>汇总子区问题</strong><p>允许主群查询该群子区的问题记录，不增加子区知识或维护权限。</p></div><t-switch v-model="editAggregate" aria-label="汇总子区问题" /></div>
            <div class="policy-row"><div><strong>允许管理者在群内建库</strong><p>只作用于当前区域；管理者仍需通过原生身份核验，其他工作区资产不会因此开放。</p></div><t-switch v-model="editCreation" aria-label="允许管理者在群内建库" /></div>
          </section>

          <details class="diagnostics" :open="detailsOpen" @toggle="detailsOpen = ($event.target as HTMLDetailsElement).open">
            <summary><t-icon name="tools" /> 连接详情与排查</summary>
            <div class="diagnostics-body">
              <dl><dt>Bot 连接</dt><dd>{{ selected.account_id }}</dd><dt>群 ID</dt><dd>{{ selected.group_id }}</dd><template v-if="selected.subarea_id"><dt>子区 ID</dt><dd>{{ selected.subarea_id }}</dd></template><dt>名称来源</dt><dd>{{ selected.name_source==='octo' ? 'Octo 平台' : '待平台核验' }} · {{ statusLabel(selected.sync_status) }}</dd><dt>上次验证</dt><dd>{{ selected.verified_at ? new Date(selected.verified_at).toLocaleString() : '尚未验证' }}</dd></dl>
              <div class="diagnostic-actions"><t-button variant="outline" :loading="saving" @click="refreshName">同步平台名称</t-button><t-button variant="text" @click="showConnection = true">更新 Bot 连接</t-button></div>
              <p>群和子区名称来自 Octo。请在 Octo 修改名称后同步；连接异常时保留上次名称，不把缓存当成最新验证。</p>
              <h5>核对指定成员的原生角色</h5><p>只检查指定 UID，不展开群成员表。Octo 群角色与知识库管理授权仍是两项检查。</p>
              <div class="role-query"><t-input v-model="roleUID" placeholder="输入要核对的原生 UID" aria-label="原生用户 UID" /><t-button variant="outline" :disabled="!roleUID.trim()" :loading="roleLoading" @click="checkRole">核对角色</t-button></div>
              <p v-if="roleResult" role="status">{{ roleResult }}</p>
            </div>
          </details>
        </main>
      </div>
    </t-loading>

    <t-dialog v-model:visible="showBind" attach="body" :header="`绑定知识库到 ${selected?.display_name || '当前区域'}`" :confirm-loading="saving" :confirm-btn="{content:'绑定知识库',disabled:!kbToBind}" @confirm="addBinding">
      <p class="dialog-description">仅为当前{{ selected?.subarea_id ? '子区' : '主群' }}开放查询，不影响其他区域，也不移动或复制资料。</p>
      <t-select v-model="kbToBind" :options="kbOptions" filterable clearable placeholder="选择当前工作区的知识库" />
      <t-empty v-if="!kbOptions.length" description="没有可新增的知识库。可先到知识库页面创建。" />
      <div class="bind-management"><t-checkbox v-model="grantOnBind">同时允许本区域管理者维护</t-checkbox><p>默认只增加查询绑定；未勾选不会撤销该库原有的维护授权。</p></div>
      <router-link :to="{name:'knowledgeBaseList'}">前往知识库</router-link>
    </t-dialog>
    <OctoScopeCreateDialog v-model:visible="showCreate" :connections="connections" :scopes="scopes" @saved="onScopeCreated" />
    <OctoConnectionDialog v-model:visible="showConnection" @saved="load" />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useRoute } from 'vue-router'
import { listKnowledgeBases } from '@/api/knowledge-base'
import { useAuthStore } from '@/stores/auth'
import OctoConnectionDialog from './OctoConnectionDialog.vue'
import OctoScopeCreateDialog from './OctoScopeCreateDialog.vue'
import { groupScopes, scopeKnowledgeRows, type ScopeKnowledgeRow } from './octoScopeDisplay'
import { listScopes, updateScope, effectiveBindings, managedKBs, revokeKBManagement, bindKB, unbindKB, listConnections, syncScopeName, inspectMemberRole, type OctoScope, type EffectiveBinding } from '@/api/octo'

const scopes = ref<OctoScope[]>([]), selected = ref<OctoScope | null>(null)
const kbs = ref<Array<{id:string;name:string}>>([]), bindings = ref<EffectiveBinding[]>([]), managed = ref<string[]>([])
const loading = ref(false), saving = ref(false), bindingLoading = ref(false)
const error = ref(''), bindingError = ref(''), search = ref('')
const showCreate = ref(false), showBind = ref(false), showConnection = ref(false), detailsOpen = ref(false)
const kbToBind = ref(''), grantOnBind = ref(false)
const editInherit = ref(false), editCreation = ref(false), editAggregate = ref(false)
const roleUID = ref(''), roleResult = ref(''), roleLoading = ref(false)
const connections = ref<Array<{account_id:string;updated_at:string}>>([])
const collapsedGroups = ref(new Set<string>())
const auth = useAuthStore(), route = useRoute()
const imSettings = {name:'knowledgeChannels'}
let selectionVersion = 0, loadVersion = 0

const groups = computed(() => groupScopes(scopes.value, search.value))
const groupCount = computed(() => scopes.value.filter(scope => !scope.subarea_id).length)
const childCount = computed(() => scopes.value.length - groupCount.value)
const knowledgeRows = computed(() => scopeKnowledgeRows(bindings.value, managed.value, kbs.value))
const queryCount = computed(() => knowledgeRows.value.filter(row => row.query !== 'none').length)
const managementCount = computed(() => knowledgeRows.value.filter(row => row.managed).length)
const parentScope = computed(() => selected.value?.subarea_id ? scopes.value.find(scope => !scope.subarea_id && scope.account_id===selected.value?.account_id && scope.group_id===selected.value?.group_id) : undefined)
const kbOptions = computed(() => kbs.value.filter(kb => !bindings.value.some(binding => !binding.inherited && binding.knowledge_base_id===kb.id)).map(kb => ({label:kb.name,value:kb.id})))
const policiesChanged = computed(() => Boolean(selected.value) && (editInherit.value!==Boolean(selected.value?.inherit_parent) || editCreation.value!==Boolean(selected.value?.allow_knowledge_creation) || editAggregate.value!==Boolean(selected.value?.aggregate_child_issues)))
const statusLabel = (status:string) => ({verified:'已验证',error:'同步失败',needs_refresh:'待重新验证',unverified:'未验证'}[status] || '未验证')
const expanded = (id:string) => Boolean(search.value.trim()) || !collapsedGroups.value.has(id)
function toggleGroup(id:string) { const next=new Set(collapsedGroups.value); next.has(id) ? next.delete(id) : next.add(id); collapsedGroups.value=next }

async function load() {
  const version=++loadVersion
  loading.value=true; error.value=''
  try {
    const [result,libraries,accounts]=await Promise.all([listScopes(),listKnowledgeBases(),listConnections()])
    if(version!==loadVersion) return
    const all=[...result.data]
    let count=result.data.length
    while(count===100) {
      const next=await listScopes(all.length)
      if(version!==loadVersion) return
      all.push(...next.data); count=next.data.length
    }
    scopes.value=all; kbs.value=libraries.data || []; connections.value=accounts.data
    const wanted=selected.value?.id || (typeof route.query.scope==='string' ? route.query.scope : undefined)
    const fresh=all.find(scope => scope.id===wanted) || all.find(scope=>scope.id===selected.value?.id) || all[0]
    if(fresh) await select(fresh)
    else {selected.value=null;bindings.value=[];managed.value=[];selectionVersion++}
  } catch {
    if(version===loadVersion) {error.value='无法读取当前工作区的群与知识库配置，请检查权限和服务状态。';scopes.value=[];selected.value=null;bindings.value=[];managed.value=[];kbs.value=[];selectionVersion++}
  } finally {if(version===loadVersion) loading.value=false}
}

async function select(scope:OctoScope) {
  const version=++selectionVersion
  selected.value=scope;editInherit.value=Boolean(scope.inherit_parent);editCreation.value=Boolean(scope.allow_knowledge_creation);editAggregate.value=Boolean(scope.aggregate_child_issues)
  roleUID.value='';roleResult.value='';roleLoading.value=false;detailsOpen.value=false;showBind.value=false
  bindings.value=[];managed.value=[];bindingError.value='';kbToBind.value='';bindingLoading.value=true
  try {
    const [queries,grants]=await Promise.all([effectiveBindings(scope.id),managedKBs(scope.id)])
    if(!Array.isArray(queries.data)||!Array.isArray(grants.data)) throw new Error('invalid scope response')
    if(version===selectionVersion) {bindings.value=queries.data;managed.value=grants.data}
  } catch {if(version===selectionVersion) bindingError.value='权限读取失败，当前结果不能当成零绑定或未授权。请重新读取。'}
  finally {if(version===selectionVersion) bindingLoading.value=false}
}

async function mutate(operation:()=>Promise<unknown>,success='配置已更新') {
  if(saving.value) return
  const tenant=auth.currentTenantId
  saving.value=true
  try {await operation();if(tenant!==auth.currentTenantId)return;await load();await MessagePlugin.success(success)}
  catch {if(tenant===auth.currentTenantId) await MessagePlugin.error('操作未完成，请检查当前权限、连接与服务状态。')}
  finally {saving.value=false}
}
function saveScope() {const scope=selected.value;if(scope)return mutate(()=>updateScope(scope.id,scope.display_name,editInherit.value,editCreation.value,editAggregate.value),'区域规则已保存')}
function bindQuery(kb:string) {const id=selected.value?.id;if(id)return mutate(()=>bindKB(id,kb),'查询绑定已更新')}
function removeBinding(kb:string) {const id=selected.value?.id;if(id)return mutate(()=>unbindKB(id,kb),'查询绑定已解除；独立维护授权保持不变')}
function grantManagement(kb:string) {const id=selected.value?.id;if(id)return mutate(()=>bindKB(id,kb,true),'本区域维护授权已更新')}
function revokeManagement(kb:string) {const id=selected.value?.id;if(id)return mutate(()=>revokeKBManagement(id,kb),'维护授权已撤销；查询绑定保持不变')}
function grantDescription(row:ScopeKnowledgeRow) {return row.query==='direct' ? '允许当前区域经核验的管理者维护此知识库；普通成员不会因此获得维护权。' : '将在本区建立独立查询绑定，并允许本区经核验的管理者维护该库。主群与其他子区保持不变。'}
function addBinding() {const id=selected.value?.id,kb=kbToBind.value,grant=grantOnBind.value;if(id&&kb)return mutate(async()=>{await bindKB(id,kb,grant ? true : undefined);showBind.value=false},'知识库已绑定')}
function openParent(id:string) {const scope=scopes.value.find(scope=>scope.id===id);if(scope)void select(scope)}
function refreshName() {const id=selected.value?.id;if(id)return mutate(async()=>{try{await syncScopeName(id)}catch(e){await load();throw e}},'平台名称已同步')}
async function checkRole() {
  const scope=selected.value,version=selectionVersion,uid=roleUID.value.trim()
  if(!scope||!uid) return
  roleLoading.value=true;roleResult.value=''
  try {
    const result=await inspectMemberRole(scope.id,uid)
    if(version!==selectionVersion)return
    const names:Record<string,string>={owner:'群主',admin:'群管理员',member:'普通成员',unknown:'未能确认身份'}
    roleResult.value=result.data.reason==='bot_admin_not_proven' ? '平台未提供可验证的 Bot 管理字段。测试 Bot 的维护权限须结合渠道明确许可和本区知识授权核对。' : `${result.data.uid}：${names[result.data.role] || '未知角色'}。这里只核对原生群角色，不授予知识库权限。`
  } catch {if(version===selectionVersion)roleResult.value='角色核对失败，未作授权判断。'}
  finally {if(version===selectionVersion)roleLoading.value=false}
}
async function onScopeCreated(scope:OctoScope) {selected.value=scope;await load();await select(scope);await MessagePlugin.success('区域已接入，请绑定知识库')}
watch(showBind,open=>{if(open){kbToBind.value='';grantOnBind.value=false}})
watch(()=>route.query.scope,id=>{if(typeof id==='string'&&id!==selected.value?.id){const scope=scopes.value.find(scope=>scope.id===id);if(scope)void select(scope)}})
watch(()=>auth.currentTenantId,()=>{selectionVersion++;loadVersion++;scopes.value=[];selected.value=null;bindings.value=[];managed.value=[];kbs.value=[];connections.value=[];showCreate.value=false;showBind.value=false;showConnection.value=false;roleResult.value='';collapsedGroups.value=new Set();void load()})
onMounted(load)
onBeforeUnmount(()=>{selectionVersion++;loadVersion++})
</script>

<style scoped>
.octo-panel { width:100%;min-width:0;color:var(--td-text-color-primary); }
.page-heading,.selected-heading,.section-heading { display:flex;align-items:flex-start;justify-content:space-between;gap:20px; }
.page-heading { margin-bottom:24px; }.page-heading h2 { margin:0 0 8px;font-size:22px;font-weight:600; }.page-heading p,.section-heading p { margin:0;color:var(--td-text-color-secondary);font-size:13px;line-height:1.6; }
.heading-actions { display:flex;align-items:center;gap:12px;flex-shrink:0; }.empty-setup { min-height:280px;display:flex;align-items:center;justify-content:center;gap:18px;flex-direction:column; }
.scope-layout { display:grid;grid-template-columns:250px minmax(0,1fr);gap:28px;align-items:start; }.scope-tree { min-width:0;border:1px solid var(--td-component-border);border-radius:8px;background:var(--td-bg-color-container);padding:16px; }
.tree-heading { display:flex;justify-content:space-between;align-items:center;gap:8px;margin-bottom:14px;font-size:14px; }.tree-heading small,.tree-node small,.tree-empty { color:var(--td-text-color-secondary);font-size:12px; }
.scope-tree nav { margin-top:16px;max-height:calc(100vh - 270px);overflow:auto; }.tree-group + .tree-group { margin-top:12px;padding-top:12px;border-top:1px solid var(--td-component-border); }.group-node { display:flex;align-items:center;gap:3px; }
.tree-toggle,.tree-toggle-placeholder { width:20px;min-width:20px; }.tree-toggle { padding:0;display:flex;align-items:center;justify-content:center;height:28px;border:0;background:transparent;color:var(--td-text-color-secondary);cursor:pointer; }
.tree-node { min-width:0;flex:1;width:100%;display:flex;align-items:flex-start;gap:8px;padding:10px 8px;text-align:left;border:0;border-radius:6px;color:var(--td-text-color-primary);background:transparent;cursor:pointer;font:inherit;font-size:13px;line-height:1.5; }
.tree-node > span:not(.child-connector) { flex:1;min-width:0;overflow-wrap:anywhere; }.tree-node .t-icon { margin-top:3px;flex-shrink:0;color:var(--td-text-color-secondary); }.tree-node small { flex-shrink:0;line-height:21px; }
.tree-node:hover,.tree-node.selected { background:var(--td-brand-color-light); }.tree-node.selected { color:var(--td-brand-color);font-weight:500; }.children { margin:4px 0 0 31px;padding-left:8px;border-left:1px solid var(--td-component-border); }.child-node { position:relative; }.child-connector { position:absolute;left:-8px;top:20px;width:7px;border-top:1px solid var(--td-component-border); }
.tree-footer { display:flex;align-items:center;gap:8px;margin-top:22px;padding-top:14px;border-top:1px solid var(--td-component-border);color:var(--td-text-color-secondary);font-size:13px;text-decoration:none; }.tree-footer:hover { color:var(--td-brand-color); }
.scope-detail { min-width:0;container-type:inline-size; }.selected-heading { margin-bottom:18px; }.selected-heading h3 { margin:0;font-size:20px;line-height:1.5;display:flex;align-items:center;gap:10px;flex-wrap:wrap; }.scope-subtitle { margin:7px 0 0;font-size:13px;color:var(--td-text-color-secondary);line-height:1.6; }
.breadcrumb { display:flex;align-items:center;gap:4px;margin:0 0 8px;color:var(--td-text-color-secondary);font-size:12px; }.breadcrumb button,.inline-link { border:0;background:transparent;padding:0;color:var(--td-brand-color);cursor:pointer;font:inherit; }
.scope-summary { display:flex;gap:32px;padding:16px 0 20px;margin-bottom:16px;border-bottom:1px solid var(--td-component-border); }.scope-summary > div { display:flex;flex-direction:column;gap:5px; }.scope-summary strong { font-size:21px;font-weight:600; }.scope-summary span { color:var(--td-text-color-secondary);font-size:12px; }
.section-heading { align-items:center;margin-bottom:16px; }.section-heading h4 { margin:0 0 6px;font-size:16px;font-weight:600; }.section-heading > .t-button { flex-shrink:0; }.knowledge-list { display:flex;flex-direction:column;gap:12px; }
.knowledge-row { display:grid;grid-template-columns:minmax(170px,1.5fr) minmax(95px,.7fr) minmax(120px,.8fr) auto;gap:14px;align-items:center;padding:16px;border:1px solid var(--td-component-border);border-radius:8px;background:var(--td-bg-color-container); }
.knowledge-name { display:flex;align-items:flex-start;gap:10px;min-width:0; }.knowledge-icon { display:flex;align-items:center;justify-content:center;width:32px;height:32px;background:var(--td-brand-color-light);color:var(--td-brand-color);border-radius:6px;flex-shrink:0; }.knowledge-name a { color:var(--td-text-color-primary);font-weight:500;line-height:1.5;text-decoration:none;overflow-wrap:anywhere; }.knowledge-name a:hover { color:var(--td-brand-color); }.knowledge-name small,.permission-state small { display:block;color:var(--td-text-color-secondary);font-size:12px;margin-top:4px; }.permission-state { display:flex;flex-direction:column;align-items:flex-start;gap:7px;font-size:13px;min-width:0; }.permission-state small { margin-top:0; }.muted { color:var(--td-text-color-secondary); }.inline-link { font-size:12px; }.knowledge-actions { display:flex;gap:2px;align-items:center;justify-content:flex-end;flex-wrap:wrap;max-width:168px; }
.permission-note { display:flex;align-items:flex-start;gap:6px;margin:14px 0 0;color:var(--td-text-color-secondary);font-size:12px;line-height:1.7; }.permission-note .t-icon { margin-top:3px;flex-shrink:0; }
.policy-section { margin-top:28px;padding-top:24px;border-top:1px solid var(--td-component-border); }.policy-row { display:flex;align-items:center;justify-content:space-between;gap:24px;padding:14px 0; }.policy-row strong { font-size:14px;font-weight:500; }.policy-row p { color:var(--td-text-color-secondary);font-size:12px;line-height:1.6;margin:6px 0 0; }.policy-row .t-switch { flex-shrink:0; }
.diagnostics { margin-top:26px;border-top:1px solid var(--td-component-border); }.diagnostics summary { padding:18px 0;cursor:pointer;color:var(--td-text-color-secondary);font-size:13px; }.diagnostics summary .t-icon { margin-right:6px; }.diagnostics-body { padding-bottom:16px;font-size:13px; }.diagnostics dl { display:grid;grid-template-columns:85px minmax(0,1fr);gap:10px;margin:0 0 14px; }.diagnostics dt { color:var(--td-text-color-secondary); }.diagnostics dd { margin:0;overflow-wrap:anywhere; }.diagnostics p { color:var(--td-text-color-secondary);font-size:12px;line-height:1.65; }.diagnostics h5 { font-size:13px;margin:22px 0 0; }.diagnostic-actions,.role-query { display:flex;align-items:center;gap:10px; }.role-query .t-input__wrap { flex:1;min-width:0; }.role-query { max-width:580px; }.role-query .t-button { flex-shrink:0; }
.dialog-description,.bind-management p { font-size:13px;line-height:1.65;color:var(--td-text-color-secondary); }.bind-management { margin:18px 0; }
@container (max-width:720px) { .knowledge-row { grid-template-columns:minmax(0,1fr) minmax(0,1fr); }.knowledge-name { grid-column:1/-1; }.knowledge-actions { grid-column:1/-1;justify-content:flex-start;max-width:none; } }
@media (max-width:1050px) { .scope-layout { grid-template-columns:220px minmax(0,1fr);gap:20px; }.scope-tree { padding:12px; }.tree-heading { flex-direction:column;align-items:flex-start;gap:4px; } }
@media (max-width:760px) { .page-heading { flex-direction:column;gap:14px; }.scope-layout { grid-template-columns:minmax(0,1fr); }.scope-tree nav { max-height:240px;overflow:auto; }.tree-heading { flex-direction:row;align-items:center; }.scope-summary { gap:22px; }.scope-summary strong { font-size:18px; }.section-heading { align-items:flex-start;gap:12px; }.diagnostic-actions { flex-wrap:wrap; } }
</style>
