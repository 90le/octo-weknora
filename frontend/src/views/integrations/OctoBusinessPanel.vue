<template>
  <section class="business-panel" :class="'view-' + activeView">
    <template v-if="!view">
      <header class="panel-heading">
        <h2>问题反馈</h2>
        <p>查看已登记的问题、处理进展和报告。知识库联系人可在对应知识库内维护。</p>
      </header>
      <t-tabs v-model="tab" class="legacy-tabs">
        <t-tab-panel value="issues" label="问题列表" />
        <t-tab-panel value="reports" label="报告与周报" />
        <t-tab-panel value="contacts" label="知识库联系人" />
      </t-tabs>
    </template>

    <div v-if="knowledgeBaseId" class="context-note">
      <t-tag variant="light" theme="primary">{{ kbName(knowledgeBaseId) }}</t-tag>
      <span>{{ activeView === 'contacts' ? '仅维护当前知识库的联系人' : '仅查看当前知识库的问题' }}</span>
    </div>
    <t-alert v-if="metadataError" theme="warning" class="panel-alert">
      {{ metadataError }}
      <t-button variant="text" @click="metadata">重新加载名称</t-button>
    </t-alert>

    <template v-if="activeView === 'issues'">
      <OctoBusinessFilters v-model="filter" v-model:from-day="fromDay" v-model:to-day="toDay"
        :knowledge-locked="Boolean(knowledgeBaseId)" :scope-options="scopeOptions" :kb-options="kbOptions"
        :loading="loading" @search="search" @reset="resetFilters" />
      <div class="list-heading">
        <p>共 <strong>{{ total }}</strong> 个登记问题 <span>点击问题标题查看来源与处理记录</span></p>
        <t-button variant="text" :loading="loading" @click="loadIssues">刷新列表</t-button>
      </div>
      <t-alert v-if="error" theme="error" class="panel-alert">
        {{ error }} <t-button variant="text" @click="loadIssues">重试</t-button>
      </t-alert>
      <div class="issue-table-scroll">
        <t-table class="issue-table" :data="issues" :columns="columns" row-key="id" table-layout="fixed"
          :loading="loading" :empty="error ? '读取失败，不能视为没有问题' : '暂无符合条件的登记问题'">
          <template #title="{ row }">
            <t-button variant="text" class="issue-title" :title="row.title" @click="openIssue(row.id)">{{ row.title }}</t-button>
            <div class="issue-type"><t-tag size="small" variant="light">{{ issueKindLabels[row.kind] || row.kind }}</t-tag></div>
          </template>
          <template #scope="{ row }">
            <span class="wrap-text">{{ sourceLabel(row) }}</span>
            <small>{{ row.is_direct ? '私聊' : row.subarea_id ? '群聊子区' : '群聊主区' }}</small>
          </template>
          <template #knowledge_base="{ row }"><span class="wrap-text">{{ kbName(row.knowledge_base_id) }}</span></template>
          <template #reporter="{ row }">{{ personName(row.reporter_name, row.reporter_uid) }}</template>
          <template #owner="{ row }">{{ personName(row.owner_name, row.owner_uid, '未指定') }}</template>
          <template #status="{ row }"><t-tag size="small" :theme="statusTheme(row.status)">{{ issueStatusLabels[row.status] || row.status }}</t-tag></template>
          <template #created_at="{ row }"><span class="date-cell">{{ time(row.created_at) }}</span></template>
        </t-table>
      </div>
      <div class="list-footer">
        <p class="hint">仅统计已登记的问题；关闭问题不代表知识已更新。</p>
        <t-pagination v-if="total" v-model="page" :total="total" :page-size="20" :show-page-size="false" @change="loadIssues" />
      </div>
    </template>

    <template v-else-if="activeView === 'reports'">
      <section class="report-section">
        <div class="section-heading">
          <h3>按需汇总</h3>
          <p>按来源、知识库和登记时间生成预览，再下载明细。预览不会自动发送。</p>
        </div>
        <OctoBusinessFilters v-model="filter" v-model:from-day="fromDay" v-model:to-day="toDay"
          :knowledge-locked="Boolean(knowledgeBaseId)" :scope-options="scopeOptions" :kb-options="kbOptions"
          :loading="reportLoading" :auto-search="false" action-label="生成预览" @search="previewReport" @reset="resetFilters" />
        <t-alert v-if="reportError" theme="error" class="panel-alert">{{ reportError }}</t-alert>
        <t-loading :loading="reportLoading">
          <div v-if="report" class="report-preview">
            <div class="report-preview-heading">
              <div><h4>汇总预览</h4><p>{{ reportRange }}</p><small>生成时间：{{ time(report.generated_at) }}</small></div>
              <t-button variant="outline" :disabled="reportOutdated || reportLoading" @click="downloadReport">下载明细 CSV</t-button>
            </div>
            <t-alert v-if="reportOutdated" theme="warning">数据或筛选条件已变更，请重新生成预览后再下载。</t-alert>
            <div class="stat-grid">
              <div><strong>{{ report.total }}</strong><span>登记问题</span></div>
              <div v-for="(label, status) in issueStatusLabels" :key="status"><strong>{{ report.counts[status] || 0 }}</strong><span>{{ label }}</span></div>
            </div>
            <t-alert v-if="report.truncated" theme="warning">仅展示最近 500 项明细，统计包含全部结果。请缩小范围后导出。</t-alert>
            <p class="hint">按登记时间统计，不代表全部提问量或总体解答率；关闭问题不等于知识更新。</p>
            <t-empty v-if="!report.items.length" description="此范围没有已登记问题" />
            <ul v-else class="report-items">
              <li v-for="item in report.items" :key="item.id">
                <t-button variant="text" class="issue-title" @click="openIssue(item.id)">{{ item.title }}</t-button>
                <div><t-tag size="small" :theme="statusTheme(item.status)">{{ issueStatusLabels[item.status] }}</t-tag><span>{{ sourceLabel(item) }}</span><span>负责人：{{ personName(item.owner_name, item.owner_uid, '未指定') }}</span></div>
              </li>
            </ul>
          </div>
          <t-empty v-else-if="!reportLoading && !reportError" class="report-empty" description="选择统计条件，点击「生成预览」查看报告" />
        </t-loading>
      </section>
      <section class="schedule-section">
        <p v-if="knowledgeBaseId" class="hint">自动周报按群或子区配置统计范围，不沿用上方的临时筛选条件。</p>
        <OctoReportSchedules :scopes="scopes" />
      </section>
    </template>

    <template v-else>
      <div class="contact-heading">
        <div><h3>资料维护联系人</h3><p>按主题配置负责人，Bot 只提供联系指引，不自动通知或催办。</p></div>
        <t-button :disabled="!effectiveContactKB" @click="newContact">添加联系人</t-button>
      </div>
      <label v-if="!knowledgeBaseId" class="contact-selector">选择知识库
        <t-select v-model="contactKB" :options="kbOptions" filterable clearable placeholder="选择要维护联系人的知识库" />
      </label>
      <t-alert v-if="contactError" theme="error" class="panel-alert">{{ contactError }} <t-button variant="text" @click="loadContacts">重试</t-button></t-alert>
      <t-loading :loading="contactsLoading">
        <t-empty v-if="!contactsLoading && !contacts.length && !contactError" :description="effectiveContactKB ? '尚未配置联系人。可以按主题添加多位负责人，并设置默认联系人。' : '请先选择知识库'" />
        <div class="contact-grid">
          <article v-for="contact in contacts" :key="contact.id">
            <div class="row"><strong>{{ contact.name }}</strong><t-tag v-if="contact.is_default" size="small" theme="primary">默认联系人</t-tag></div>
            <p>{{ contact.topic || '通用问题' }}</p>
            <p class="prewrap">{{ contact.details || '暂无联系方式或补充说明' }}</p>
            <small>原生 UID：{{ contact.uid }}</small>
            <div class="contact-actions">
              <t-button variant="text" @click="editContact(contact)">编辑</t-button>
              <t-popconfirm content="删除后 Bot 不再推荐此联系人。不会删除用户或发出通知。" @confirm="removeContact(contact.id)">
                <t-button theme="danger" variant="text" :disabled="contactSaving">删除</t-button>
              </t-popconfirm>
            </div>
          </article>
        </div>
      </t-loading>
    </template>

    <t-drawer v-model:visible="detailVisible" attach="body" header="问题详情" size="min(760px, 100vw)" :footer="false" :close-btn="true" :close-on-esc-keydown="true">
      <t-loading :loading="detailLoading">
        <t-alert v-if="detailError" theme="error">{{ detailError }} <t-button v-if="detailID" variant="text" @click="openIssue(detailID)">重试</t-button></t-alert>
        <div v-if="selected" class="issue-detail">
          <div class="detail-heading">
            <div class="row"><t-tag size="small" variant="light">{{ issueKindLabels[selected.kind] }}</t-tag><t-tag size="small" :theme="statusTheme(selected.status)">{{ issueStatusLabels[selected.status] }}</t-tag></div>
            <h3>{{ selected.title }}</h3>
            <p>登记于 {{ time(selected.created_at) }} · 最近更新 {{ time(selected.updated_at) }}</p>
          </div>
          <section class="detail-section">
            <h4>来源</h4>
            <dl class="source-facts">
              <dt>来源区域</dt><dd>{{ sourceLabel(selected) }}</dd>
              <dt>关联知识库</dt><dd><router-link :to="{ name: 'knowledgeBaseDetail', params: { kbId: selected.knowledge_base_id } }">{{ kbName(selected.knowledge_base_id) }}</router-link></dd>
              <dt>提问人</dt><dd>{{ displayPerson(selected.reporter_name, selected.reporter_uid) }}<small v-if="selected.reporter_uid">原生 UID：{{ selected.reporter_uid }}</small></dd>
              <dt>负责人</dt><dd>{{ displayPerson(selected.owner_name, selected.owner_uid) }}<small v-if="selected.owner_uid">原生 UID：{{ selected.owner_uid }}</small></dd>
              <dt>来源消息</dt><dd><code>{{ selected.message_id || '无消息编号' }}</code></dd>
              <dt>记录编号</dt><dd><code>{{ selected.id }}</code></dd>
            </dl>
            <h5>问题描述</h5><p class="prewrap">{{ selected.description }}</p>
            <template v-if="selected.steps"><h5>复现步骤</h5><p class="prewrap">{{ selected.steps }}</p></template>
            <template v-if="selected.expected"><h5>预期结果</h5><p class="prewrap">{{ selected.expected }}</p></template>
            <h5>原始消息</h5><blockquote class="prewrap">{{ selected.original_message || '未保存原始消息' }}</blockquote>
            <ul v-if="selected.attachments?.length" class="attachments"><li v-for="(attachment, index) in selected.attachments" :key="index">
              <a v-if="safeAttachmentURL(attachment.url)" :href="safeAttachmentURL(attachment.url)!" target="_blank" rel="noopener noreferrer">{{ attachment.name || '附件' }}</a>
              <span v-else>{{ attachment.name || '附件（链接不可用）' }}</span>
            </li></ul>
          </section>
          <section class="detail-section">
            <h4>更新处理</h4>
            <div class="two-col">
              <label>处理状态<t-select v-model="issueEdit.status" :options="statusOptions" /></label>
              <div />
              <label>负责人姓名<t-input v-model="issueEdit.owner_name" :maxlength="256" placeholder="未指定时可留空" /></label>
              <label>负责人原生 UID<t-input v-model="issueEdit.owner_uid" :maxlength="128" placeholder="与姓名一同填写或清空" /></label>
            </div>
            <label>处理说明<t-textarea v-model="issueEdit.note" :maxlength="4000" placeholder="记录处理结果、资料链接或关闭原因" /></label>
            <p class="hint">保存处理记录不会修改知识库，也不会给负责人发送通知。</p>
            <t-button :loading="issueSaving" @click="saveIssue">保存处理记录</t-button>
          </section>
          <section class="detail-section">
            <h4>处理记录</h4>
            <t-empty v-if="!events.length" description="暂无后续处理记录，原始提问见上方来源" />
            <ol v-else class="history"><li v-for="(event, index) in events" :key="index">
              <div class="row"><t-tag size="small" :theme="statusTheme(event.status)">{{ issueStatusLabels[event.status] || event.status }}</t-tag><strong :title="`原生 UID：${event.actor_uid}`">{{ eventActor(event) }}</strong><time>{{ time(event.created_at) }}</time></div>
              <p class="prewrap">{{ event.note || '更新了处理状态或负责人，未补充说明' }}</p>
            </li></ol>
          </section>
        </div>
      </t-loading>
    </t-drawer>
    <t-dialog v-model:visible="contactVisible" attach="body" :header="contactEdit.id ? '编辑联系人' : '添加联系人'"
      :confirm-loading="contactSaving" @confirm="submitContact">
      <div class="contact-form">
        <p class="hint">当前知识库：{{ kbName(effectiveContactKB) }}</p>
        <label>姓名<t-input v-model="contactEdit.name" :maxlength="256" /></label>
        <label>原生 UID<t-input v-model="contactEdit.uid" :maxlength="128" placeholder="用于 Octo 原生用户名片" /></label>
        <label>负责主题<t-input v-model="contactEdit.topic" :maxlength="256" placeholder="例如合同、计费、产品使用" /></label>
        <label>联系方式与备注<t-textarea v-model="contactEdit.details" :maxlength="2000" placeholder="电话、邮箱、办公时间或联系说明" /></label>
        <label class="row"><t-switch v-model="contactEdit.is_default" />设为默认联系人</label>
        <p class="hint">默认联系人用于没有匹配主题的问题；更换默认不会删除其他联系人。</p>
      </div>
    </t-dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useRoute } from 'vue-router'
import { listKnowledgeBases } from '@/api/knowledge-base'
import { listScopes, type OctoScope } from '@/api/octo'
import {
  listIssues, issueDetail, updateIssue, listContacts, saveContact, deleteContact, issueReport,
  type OctoIssue, type OctoContact, type IssueEvent, type IssueStatus, type IssueFilter,
} from '@/api/octo-business'
import { useAuthStore } from '@/stores/auth'
import { issueKindLabels, issueStatusLabels, displayPerson, csvCell, safeAttachmentURL } from './octoBusinessDisplay'
import { resolveBusinessView, scopedIssueFilter, issueFilterKey, scopePath, issueSourceLabel, issueBelongsToContext, type OctoBusinessView } from './octoBusinessView'
import OctoBusinessFilters from './OctoBusinessFilters.vue'
import OctoReportSchedules from './OctoReportSchedules.vue'

const props = defineProps<{ view?: OctoBusinessView; knowledgeBaseId?: string }>()
const auth = useAuthStore()
const route = useRoute()
const tab = ref<OctoBusinessView>('issues')
const activeView = computed(() => resolveBusinessView(props.view, tab.value))
const filter = ref<IssueFilter>({})
const fromDay = ref('')
const toDay = ref('')
const page = ref(1)
const total = ref(0)
const scopes = ref<OctoScope[]>([])
const kbs = ref<Array<{ id: string; name: string }>>([])
const issues = ref<OctoIssue[]>([])
const loading = ref(false)
const error = ref('')
const metadataLoading = ref(false)
const metadataError = ref('')
const statusOptions = Object.entries(issueStatusLabels).map(([value, label]) => ({ value, label }))
const scopeOptions = computed(() => scopes.value.map(scope => ({ value: scope.id, label: scopePath(scope, scopes.value) })))
const kbOptions = computed(() => kbs.value.map(kb => ({ value: kb.id, label: kb.name })))
const kbName = (id: string) => kbs.value.find(kb => kb.id === id)?.name || (metadataLoading.value ? '正在加载名称…' : id === props.knowledgeBaseId ? '当前知识库' : '知识库已移除或不可见')
const sourceLabel = (issue: OctoIssue) => issueSourceLabel(issue, scopes.value)
const time = (value: string) => value ? new Date(value).toLocaleString() : '—'
const personName = (name?: string, uid?: string, empty = '未提供姓名') => name?.trim() || (uid ? '姓名待核对' : empty)
const eventActor = (event: { actor_name?: string; actor_uid: string }) =>
  event.actor_uid === auth.user?.id ? (auth.user.username || '当前用户')
    : event.actor_name && event.actor_name !== event.actor_uid ? event.actor_name : '姓名待核对'
const statusTheme = (status: string): 'success' | 'default' | 'warning' | 'primary' =>
  status === 'resolved' ? 'success' : status === 'closed' ? 'default' : status === 'inprogress' ? 'primary' : 'warning'
const columns = [
  { colKey: 'title', title: '问题／类型', minWidth: 230 },
  { colKey: 'scope', title: '来源群聊／子区', minWidth: 170 },
  { colKey: 'knowledge_base', title: '知识库', minWidth: 150 },
  { colKey: 'reporter', title: '提问人', minWidth: 100 },
  { colKey: 'owner', title: '负责人', minWidth: 100 },
  { colKey: 'status', title: '状态', width: 85 },
  { colKey: 'created_at', title: '登记时间', width: 160 },
]

let listVersion = 0, metadataVersion = 0, detailVersion = 0, contactVersion = 0, reportVersion = 0, contextVersion = 0
function query() { return scopedIssueFilter(filter.value, props.knowledgeBaseId, fromDay.value, toDay.value) }
async function metadata() {
  const version = ++metadataVersion
  metadataLoading.value = true
  metadataError.value = ''
  try {
    const [knowledge, first] = await Promise.all([listKnowledgeBases(), listScopes()])
    if (version !== metadataVersion) return
    kbs.value = knowledge.data || []
    scopes.value = first.data
    if (first.data.length === 100) {
      for (let offset = 100; ; offset += 100) {
        const next = await listScopes(offset)
        if (version !== metadataVersion) return
        scopes.value.push(...next.data)
        if (next.data.length < 100) break
      }
    }
  } catch {
    if (version === metadataVersion) metadataError.value = '部分群或知识库名称加载失败，请重试。'
  } finally { if (version === metadataVersion) metadataLoading.value = false }
}
async function loadIssues() {
  const version = ++listVersion
  loading.value = true
  error.value = ''
  try {
    const result = await listIssues({ ...query(), page: page.value, page_size: 20 })
    if (version === listVersion) {
      issues.value = result.data.items
      total.value = result.data.total
    }
  } catch (cause) {
    if (version === listVersion) {
      issues.value = []
      total.value = 0
      error.value = cause instanceof Error && cause.message.includes('日期') ? cause.message : '问题列表读取失败，请检查权限和服务状态。'
    }
  } finally { if (version === listVersion) loading.value = false }
}
function search() { page.value = 1; void loadIssues() }
function resetFilters() {
  filter.value = {}
  fromDay.value = ''
  toDay.value = ''
  if (activeView.value === 'issues') search()
  else { reportVersion++; report.value = null; reportError.value = ''; reportLoading.value = false }
}

const selected = ref<OctoIssue | null>(null)
const events = ref<IssueEvent[]>([])
const detailID = ref('')
const detailVisible = ref(false)
const detailLoading = ref(false)
const detailError = ref('')
const issueSaving = ref(false)
const issueEdit = ref<{ status: IssueStatus; owner_uid: string; owner_name: string; note: string }>({ status: 'open', owner_uid: '', owner_name: '', note: '' })
async function openIssue(id: string) {
  const version = ++detailVersion
  detailID.value = id
  detailVisible.value = true
  detailLoading.value = true
  detailError.value = ''
  selected.value = null
  events.value = []
  try {
    const result = await issueDetail(id)
    if (version !== detailVersion) return
    if (!issueBelongsToContext(result.data.issue, props.knowledgeBaseId)) {
      detailError.value = '这条问题不属于当前知识库，请从对应知识库或问题列表中查看。'
      return
    }
    selected.value = result.data.issue
    events.value = result.data.events
    issueEdit.value = { status: selected.value.status, owner_uid: selected.value.owner_uid || '', owner_name: selected.value.owner_name || '', note: '' }
  } catch {
    if (version === detailVersion) detailError.value = '读取详情失败，记录可能已删除或权限已变更。'
  } finally { if (version === detailVersion) detailLoading.value = false }
}
async function saveIssue() {
  if (!selected.value || issueSaving.value) return
  if (Boolean(issueEdit.value.owner_uid.trim()) !== Boolean(issueEdit.value.owner_name.trim())) {
    await MessagePlugin.warning('负责人姓名和 UID 请一同填写，或一同清空')
    return
  }
  const id = selected.value.id, version = contextVersion
  issueSaving.value = true
  try {
    await updateIssue(id, { ...issueEdit.value })
    if (version !== contextVersion) return
    await Promise.all([
      detailVisible.value && detailID.value === id ? openIssue(id) : Promise.resolve(),
      activeView.value === 'issues' ? loadIssues() : Promise.resolve(),
    ])
    reportQuery.value = ''
    await MessagePlugin.success('处理记录已保存')
  } catch { if (version === contextVersion) await MessagePlugin.error('保存失败，请检查权限后重试') }
  finally { issueSaving.value = false }
}

const contacts = ref<OctoContact[]>([])
const contactKB = ref(props.knowledgeBaseId || '')
const effectiveContactKB = computed(() => props.knowledgeBaseId || contactKB.value)
const contactsLoading = ref(false)
const contactSaving = ref(false)
const contactVisible = ref(false)
const contactError = ref('')
const blankContact = (): OctoContact => ({ id: '', knowledge_base_id: effectiveContactKB.value, topic: '', name: '', uid: '', details: '', is_default: false })
const contactEdit = ref<OctoContact>(blankContact())
async function loadContacts() {
  const version = ++contactVersion, kb = effectiveContactKB.value
  contacts.value = []
  contactError.value = ''
  contactsLoading.value = Boolean(kb)
  if (!kb) return
  try {
    const result = await listContacts(kb)
    if (version === contactVersion) contacts.value = result.data
  } catch { if (version === contactVersion) contactError.value = '联系人加载失败，请检查知识库权限。' }
  finally { if (version === contactVersion) contactsLoading.value = false }
}
function newContact() { contactEdit.value = blankContact(); contactVisible.value = true }
function editContact(contact: OctoContact) {
  if (contact.knowledge_base_id !== effectiveContactKB.value) return
  contactEdit.value = { ...contact }
  contactVisible.value = true
}
async function submitContact() {
  if (contactSaving.value || !effectiveContactKB.value) return
  if (!contactEdit.value.name.trim() || !contactEdit.value.uid.trim()) { await MessagePlugin.warning('请填写联系人姓名和原生 UID'); return }
  const version = contextVersion, kb = effectiveContactKB.value
  contactSaving.value = true
  try {
    await saveContact({ ...contactEdit.value, knowledge_base_id: kb })
    if (version !== contextVersion || kb !== effectiveContactKB.value) return
    contactVisible.value = false
    await loadContacts()
    await MessagePlugin.success('联系人已保存')
  } catch { if (version === contextVersion) await MessagePlugin.error('联系人保存失败，请检查权限和填写内容') }
  finally { contactSaving.value = false }
}
async function removeContact(id: string) {
  if (contactSaving.value) return
  const version = contextVersion
  contactSaving.value = true
  try { await deleteContact(id); if (version === contextVersion) await loadContacts() }
  catch { if (version === contextVersion) await MessagePlugin.error('删除失败，联系人保持不变') }
  finally { contactSaving.value = false }
}

type Report = Awaited<ReturnType<typeof issueReport>>['data']
const report = ref<Report | null>(null)
const reportLoading = ref(false)
const reportError = ref('')
const reportRange = ref('')
const reportQuery = ref('')
const queryKey = computed(() => { try { return issueFilterKey(query()) } catch { return 'invalid-date' } })
const reportOutdated = computed(() => Boolean(report.value && queryKey.value !== reportQuery.value))
async function previewReport() {
  const version = ++reportVersion
  reportLoading.value = true
  reportError.value = ''
  try {
    const input = query(), key = issueFilterKey(input)
    const region = scopeOptions.value.find(scope => scope.value === input.scope_id)?.label || '全部授权来源'
    const range = `登记日期：${fromDay.value || '不限起始'} 至 ${toDay.value || '不限结束'}；来源：${region}；知识库：${input.knowledge_base_id ? kbName(input.knowledge_base_id) : '全部知识库'}`
    const result = await issueReport(input)
    if (version !== reportVersion) return
    report.value = result.data
    reportQuery.value = key
    reportRange.value = range
  } catch (cause) {
    if (version === reportVersion) {
      report.value = null
      reportQuery.value = ''
      reportError.value = cause instanceof Error && cause.message.includes('日期') ? cause.message : '汇总生成失败，请检查日期与权限。'
    }
  } finally { if (version === reportVersion) reportLoading.value = false }
}
function downloadReport() {
  if (!report.value || reportOutdated.value) return
  const rows = [
    ['编号', '类型', '标题', '描述', '来源群聊／子区', '知识库', '提问人', '提问人 UID', '负责人', '负责人 UID', '状态', '登记时间'],
    ...report.value.items.map(item => [item.id, issueKindLabels[item.kind], item.title, item.description, sourceLabel(item), kbName(item.knowledge_base_id), item.reporter_name, item.reporter_uid, item.owner_name, item.owner_uid, issueStatusLabels[item.status], time(item.created_at)]),
  ]
  const url = URL.createObjectURL(new Blob(['\uFEFF' + rows.map(row => row.map(csvCell).join(',')).join('\r\n')], { type: 'text/csv;charset=utf-8' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `octo-issues-${new Date().toISOString().slice(0, 10)}.csv`
  anchor.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
function invalidate() { listVersion++; metadataVersion++; detailVersion++; contactVersion++; reportVersion++; contextVersion++ }
function clearContext() {
  invalidate()
  issues.value = []; total.value = 0; contacts.value = []; selected.value = null; events.value = []; report.value = null
  detailVisible.value = false; detailID.value = ''; contactVisible.value = false
  filter.value = {}; fromDay.value = ''; toDay.value = ''; contactKB.value = props.knowledgeBaseId || ''
  loading.value = false; contactsLoading.value = false; reportLoading.value = false; metadataLoading.value = false
  error.value = ''; contactError.value = ''; reportError.value = ''
}
function loadCurrentView() {
  if (activeView.value === 'issues') void loadIssues()
  else if (activeView.value === 'contacts') void loadContacts()
}
watch(() => auth.currentTenantId, () => {
  clearContext(); scopes.value = []; kbs.value = []
  void metadata(); loadCurrentView()
})
watch(() => props.knowledgeBaseId, () => { clearContext(); void metadata(); loadCurrentView() })
watch(activeView, () => { detailVisible.value = false; loadCurrentView() })
watch(contactKB, () => {
  if (props.knowledgeBaseId) return
  contactVersion++; contacts.value = []; contactVisible.value = false
  if (activeView.value === 'contacts') void loadContacts()
})
watch(detailVisible, visible => { if (!visible) detailVersion++ })
watch(() => [route.query.view, route.query.kbId], ([view, kb]) => {
  if (!props.view && view) tab.value = resolveBusinessView(undefined, view)
  if (!props.knowledgeBaseId && typeof kb === 'string') {
    filter.value.knowledge_base_id = kb
    contactKB.value = kb
  }
}, { immediate: true })
onMounted(() => { void metadata(); loadCurrentView() })
onBeforeUnmount(invalidate)
</script>

<style scoped>
.business-panel { min-width: 0; max-width: 100%; }
.panel-heading h2 { margin: 0 0 8px; font-size: 20px; }
.panel-heading p, .hint, .section-heading p, .contact-heading p { font-size: 13px; color: var(--td-text-color-secondary); line-height: 1.7; }
.legacy-tabs { margin: 16px 0 24px; }
.context-note { display: flex; align-items: center; gap: 10px; margin-bottom: 18px; font-size: 13px; color: var(--td-text-color-secondary); }
.panel-alert { margin: 16px 0; }
.list-heading, .list-footer, .contact-heading, .report-preview-heading { display: flex; justify-content: space-between; align-items: center; gap: 16px; flex-wrap: wrap; }
.list-heading { margin: 18px 0 12px; }
.list-heading p { margin: 0; font-size: 14px; }
.list-heading p>span { margin-left: 14px; color: var(--td-text-color-secondary); font-size: 12px; }
.list-footer { margin-top: 18px; }
.list-footer .hint { margin: 0; }
.issue-table-scroll { min-width: 0; width: 100%; max-width: 100%; overflow-x: auto; border: 1px solid var(--td-component-border); border-radius: var(--td-radius-large, 8px); }
.issue-table { min-width: 995px; }
.issue-type { margin-top:6px; }
.issue-table :deep(.t-table__content) { overflow-x: auto; }
.issue-table :deep(th) { font-weight: 500; color: var(--td-text-color-secondary); }
.issue-table :deep(td) { vertical-align: top; }
.issue-title { max-width: 100%; height: auto; min-height: 24px; padding: 0; white-space: normal; text-align: left; justify-content: flex-start; line-height: 1.65; }
.issue-title :deep(.t-button__text) { white-space: normal; overflow-wrap: anywhere; text-align: left; }
.wrap-text { overflow-wrap: anywhere; line-height: 1.65; }
.date-cell { font-size: 12px; white-space: normal; line-height: 1.65; }
small { display: block; margin-top: 5px; color: var(--td-text-color-secondary); font-size: 12px; overflow-wrap: anywhere; }
.row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.report-section, .schedule-section { min-width: 0; }
.section-heading { margin-bottom: 16px; }
.section-heading h3, .contact-heading h3 { margin: 0 0 8px; font-size: 17px; }
.section-heading p, .contact-heading p { margin: 0; }
.report-empty { padding: 36px 16px; }
.report-preview { margin-top: 24px; padding: 22px; border: 1px solid var(--td-component-border); border-radius: var(--td-radius-large, 8px); }
.report-preview-heading h4 { margin: 0 0 8px; font-size: 16px; }
.report-preview-heading p { margin: 0; font-size: 13px; line-height: 1.65; }
.report-preview-heading { margin-bottom: 18px; align-items: flex-start; }
.stat-grid { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 12px; margin: 22px 0; }
.stat-grid>div { padding: 16px; border-radius: 6px; background: var(--td-bg-color-secondarycontainer); }
.stat-grid strong { display: block; font-size: 25px; font-weight: 600; }
.stat-grid span { display: block; margin-top: 6px; font-size: 12px; color: var(--td-text-color-secondary); }
.report-items { padding: 0; margin: 12px 0 0; list-style: none; }
.report-items li { padding: 14px 0; border-bottom: 1px solid var(--td-component-border); }
.report-items li:last-child { border-bottom: 0; }
.report-items li>div { display: flex; flex-wrap: wrap; gap: 12px; align-items: center; margin-top: 6px; font-size: 12px; color: var(--td-text-color-secondary); }
.schedule-section { margin-top: 32px; padding-top: 16px; border-top: 1px solid var(--td-component-border); }
.contact-heading { margin-bottom: 24px; }
.contact-selector { max-width: 420px; margin-bottom: 24px; }
.contact-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(270px, 1fr)); gap: 16px; }
.contact-grid article { min-width: 0; padding: 20px; border: 1px solid var(--td-component-border); border-radius: 8px; }
.contact-grid article>p { font-size: 13px; line-height: 1.65; }
.contact-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; }
.issue-detail { padding-bottom: 20px; }
.detail-heading h3 { font-size: 19px; line-height: 1.6; margin: 14px 0 8px; overflow-wrap: anywhere; }
.detail-heading>p { color: var(--td-text-color-secondary); font-size: 12px; }
.detail-section { padding-top: 20px; margin-top: 20px; border-top: 1px solid var(--td-component-border); }
.detail-section h4 { font-size: 15px; margin: 0 0 16px; }
.detail-section h5 { font-size: 13px; margin: 18px 0 8px; color: var(--td-text-color-secondary); font-weight: 500; }
.source-facts { display: grid; grid-template-columns: 95px minmax(0, 1fr); gap: 14px; margin: 0; font-size: 13px; }
.source-facts dt { color: var(--td-text-color-secondary); }
.source-facts dd { margin: 0; overflow-wrap: anywhere; line-height: 1.6; }
.source-facts code { font-size: 12px; }
.prewrap { white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.7; font-size: 14px; margin: 8px 0; }
blockquote { margin: 8px 0; border-left: 3px solid var(--td-component-border); padding: 12px 16px; background: var(--td-bg-color-secondarycontainer); }
.attachments { padding-left: 20px; }
label { display: flex; flex-direction: column; gap: 8px; margin: 16px 0; font-size: 13px; }
label.row { flex-direction: row; }
.two-col { display: grid; grid-template-columns: 1fr 1fr; gap: 0 16px; }
.two-col label { margin-top: 0; }
.history { list-style: none; padding: 0; margin: 0; }
.history li { padding: 14px 0 14px 18px; border-left: 2px solid var(--td-component-border); margin-left: 5px; }
.history time { color: var(--td-text-color-secondary); font-size: 12px; }
.history p { margin-bottom: 0; }
.contact-form { max-height: 65vh; overflow-y: auto; padding-right: 4px; }
@media(max-width:800px) { .stat-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); } .list-heading p>span { display: block; margin: 6px 0 0; } .list-footer { justify-content: flex-start; } }
@media(max-width:520px) { .two-col { grid-template-columns: minmax(0, 1fr); } .two-col>div:empty { display: none; } .stat-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } .report-preview { padding: 16px; } .source-facts { grid-template-columns: 75px minmax(0, 1fr); } .context-note { flex-wrap: wrap; } }
</style>
