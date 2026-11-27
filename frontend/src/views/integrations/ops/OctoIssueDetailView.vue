<template>
  <t-loading :loading="loading" class="case-wrap">
    <t-alert v-if="error" theme="error" class="mb12">{{ error }}</t-alert>

    <template v-if="issue">
      <!-- Top Bar -->
      <div class="case-top">
        <div class="top-l">
          <t-button size="small" variant="outline" @click="router.push({ name: 'knowledgeIssues' })">← 返回列表</t-button>
          <h2>{{ issue.title }}</h2>
          <t-tag size="small" theme="primary" variant="light">{{ kbName }}</t-tag>
          <t-tag size="small" variant="light" :theme="statusTheme(issue.status)">
            {{ statusLabels[issue.status] || issue.status }}
          </t-tag>
        </div>
        <div class="top-r">
          <t-button size="small" variant="outline" @click="openKnowledgeBase">
            打开所属知识库 ↗
          </t-button>
          <span class="mini copyable" :title="issue.id" @click="copyText(issue.id, '工单编号')">
            #{{ issue.id.slice(0, 8) }}
          </span>
        </div>
      </div>

      <div class="case-grid">
        <!-- Left Main Area: Diagnosis -> Instant Retest -> Optional Strict Gate -> Audit -->
        <div class="case-left">
          <div class="meta-strip">
            <div>
              <small>Octo 来源区域</small>
              <b>{{ issue.is_direct ? 'Octo 私聊' : (issue.scope_name || issue.group_id) }}</b>
            </div>
            <div>
              <small>提问 / 反馈人</small>
              <b :title="issue.reporter_uid ? `UID: ${issue.reporter_uid}` : ''">
                {{ displayReporter }}
              </b>
            </div>
            <div>
              <small>登记时间</small>
              <b>{{ formatTime(issue.created_at) }}</b>
            </div>
            <div>
              <small>原消息定位</small>
              <b
                v-if="issue.message_id"
                class="copyable"
                :title="`点击复制：${issue.message_id}`"
                @click="copyText(issue.message_id, '原消息编号')"
              >
                {{ shortMsgId(issue.message_id) }} 📋
              </b>
              <b v-else>—</b>
            </div>
          </div>

          <!-- Step 1 & 2: Problem Diagnosis + Lightweight Instant Retest -->
          <section class="panel-card">
            <div class="panel-hd">
              <div>
                <h3>问题诊断 & 一键复测对照</h3>
                <small>补充知识库文档或 Wiki 后，无需填表即可实时对比新旧答复</small>
              </div>
              <div class="row-gap">
                <t-button size="small" variant="outline" @click="openKnowledgeBase">去补文档 ↗</t-button>
                <t-button theme="primary" size="small" :loading="instantRunning" :disabled="!evidence?.question" @click="runInstantRetest">
                  ⚡ 一键复测原问题
                </t-button>
              </div>
            </div>
            <div class="panel-bd grid12">
              <div v-if="issue.description || issue.expected" class="desc-box">
                <div v-if="issue.description"><b>问题说明：</b>{{ issue.description }}</div>
                <div v-if="issue.expected" class="mt4"><b>期望效果：</b>{{ issue.expected }}</div>
              </div>

              <div class="cmp-grid">
                <div class="cmp-box old">
                  <h4>当时旧答</h4>
                  <div class="mini mb6">原问：{{ evidence?.question || issue.original_message || '—' }}</div>
                  <OctoMarkdown :text="evidence?.answer || ''" empty="未保留当时答复正文" />
                </div>
                <div class="cmp-box new">
                  <h4>最新即时复测结果</h4>
                  <OctoMarkdown v-if="instantResult" :text="instantResult" />
                  <p v-else class="mini">点击右上角「一键复测原问题」，实时调用当前知识库最新文档与 Wiki 进行对照。</p>
                </div>
              </div>

              <div v-if="instantResult && !isClosed" class="next-cta">
                <span>复测结果已生成，确认满意后可直接回告原会话并办结闭环：</span>
                <div class="row-gap">
                  <t-button size="small" variant="outline" @click="rightTab = 'reply'">去右侧原渠道回告</t-button>
                  <t-button size="small" theme="primary" :loading="savingStatus" @click="quickResolve('复测验证通过，已解决')">
                    直接标记已解决
                  </t-button>
                </div>
              </div>
            </div>
          </section>

          <!-- Collapsible Advanced Strict Remediation Gate -->
          <section class="panel-card">
            <div class="panel-hd compact">
              <div>
                <h3>高级：严格修订与三类回归门禁</h3>
                <small>{{ strictMode ? '已开启：需完成本地源文件版本校验与原问/同义/反例三测核验' : '默认关闭（轻量闭环），仅对高敏/重点口径按需开启' }}</small>
              </div>
              <div class="row-gap">
                <t-switch :value="strictMode" :loading="modeSaving" :disabled="isClosed" @change="toggleStrictMode" />
                <span class="mini">{{ strictMode ? '严格门禁已启用' : '轻量模式' }}</span>
              </div>
            </div>
            <div v-if="strictMode" class="panel-bd">
              <OctoIssueRetests
                :key="issue.id"
                :issue-id="issue.id"
                :kb-id="issue.knowledge_base_id"
                :editable="!issue.archive_state && ['open', 'inprogress'].includes(issue.status)"
                :can-run="Boolean(evidence?.question)"
                @repair-saved="load"
              />
            </div>
          </section>

          <!-- Audit Timeline -->
          <section class="panel-card">
            <div class="panel-hd"><h3>处理与审计留痕</h3><small>{{ events.length }} 条记录</small></div>
            <div class="panel-bd">
              <ul v-if="events.length" class="tl">
                <li v-for="(ev, idx) in events" :key="idx">
                  <div class="tl-hd">
                    <b>{{ ev.actor_name || ev.actor_uid }}</b>
                    <t-tag v-if="ev.status" size="small" variant="light" :theme="statusTheme(ev.status)">
                      {{ statusLabels[ev.status] || ev.status }}
                    </t-tag>
                    <span class="mini">{{ formatTime(ev.created_at) }}</span>
                  </div>
                  <div class="tl-note">{{ ev.note }}</div>
                </li>
              </ul>
              <t-empty v-else description="暂无后续处理记录" />
            </div>
          </section>
        </div>

        <!-- Right Sidebar: Status & Assignment (instant save) + Reply & Close -->
        <div class="case-right">
          <section class="panel-card">
            <div class="panel-hd">
              <h3>工单状态与分派</h3>
              <t-tag size="small" variant="light" :theme="statusTheme(issue.status)">
                {{ statusLabels[issue.status] || issue.status }}
              </t-tag>
            </div>
            <div class="panel-bd grid12">
              <!-- Quick Status Actions -->
              <div class="row-gap">
                <t-button
                  v-if="issue.status === 'open'"
                  size="small"
                  theme="primary"
                  variant="outline"
                  :loading="savingStatus"
                  @click="updateStatus('inprogress', '开始跟进处理')"
                >
                  标记为处理中
                </t-button>
                <t-button
                  v-if="isClosed"
                  size="small"
                  variant="outline"
                  :loading="savingStatus"
                  @click="updateStatus('inprogress', '重新打开继续跟进')"
                >
                  重新打开工单
                </t-button>
              </div>

              <!-- Independent Contact Assignment -->
              <div class="fld">
                <span>业务联系人（可留空，独立保存）</span>
                <div class="assign-row">
                  <t-select
                    v-model="selectedContactId"
                    :options="contactOpts"
                    clearable
                    filterable
                    placeholder="不强制指定联系人"
                  />
                  <t-button
                    size="small"
                    variant="outline"
                    :loading="savingOwner"
                    :disabled="!ownerDirty"
                    @click="saveOwner"
                  >
                    保存分派
                  </t-button>
                </div>
                <div class="mini" v-if="issue.owner_name">
                  当前跟进人：<b>{{ issue.owner_name }}</b>
                </div>
              </div>
              <div class="mini">
                业务联系人仍兼容保留在
                <router-link :to="{ name: 'knowledgeContacts', params: { kbId: issue.knowledge_base_id } }">知识库 → 联系人</router-link>
                独立页维护，留空不阻断闭环。
              </div>
            </div>
          </section>

          <section class="panel-card">
            <div class="rtab-bar">
              <button type="button" :aria-selected="rightTab === 'reply'" @click="rightTab = 'reply'">原渠道回告</button>
              <button type="button" :aria-selected="rightTab === 'close'" @click="rightTab = 'close'">办结闭环</button>
            </div>
            <div v-if="rightTab === 'reply'" class="panel-bd grid10">
              <div v-if="justReplied && !isClosed" class="reply-done-bar">
                <span>原渠道回告已送达，是否顺手将工单标记为「已解决」？</span>
                <t-button size="small" theme="primary" :loading="savingStatus" @click="quickResolve('已通过原渠道回告并完成闭环')">
                  一键标记已解决
                </t-button>
              </div>
              <OctoIssueReply :key="issue.id" :issue="issue" @sent="onReplySent" />
            </div>
            <div v-else class="panel-bd grid10">
              <t-alert :theme="strictMode ? 'warning' : 'success'" :close="false">
                <div class="grid6">
                  <span>
                    {{ strictMode ? '当前为严格门禁模式：需三类复测全部人工核验通过才可标「已解决」，或切回轻量模式后直接办结。' : '当前为轻量闭环模式：无强制三测门禁，核实口径后可一步办结。' }}
                  </span>
                  <t-button v-if="strictMode && !isClosed" size="small" variant="outline" :loading="modeSaving" @click="toggleStrictMode(false)">
                    切回轻量模式
                  </t-button>
                </div>
              </t-alert>
              <t-textarea v-model="closeNote" placeholder="记录已补充的文档/Wiki 或关闭说明..." />
              <div class="row-gap">
                <t-button theme="primary" size="small" :loading="savingStatus" :disabled="isClosed" @click="updateStatus('resolved')">
                  标记已解决
                </t-button>
                <t-button size="small" variant="outline" :loading="savingStatus" :disabled="isClosed" @click="updateStatus('closed')">
                  直接关闭
                </t-button>
              </div>
            </div>
          </section>
        </div>
      </div>
    </template>
  </t-loading>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  issueDetail,
  issueRepair,
  listContacts,
  retestIssue,
  setIssueRemediationMode,
  updateIssue,
  type IssueEvent,
  type IssueEvidence,
  type IssueStatus,
  type OctoContact,
  type OctoIssue,
  type RepairView,
} from '@/api/octo-business'
import OctoMarkdown from '../OctoMarkdown.vue'
import OctoIssueReply from '../OctoIssueReply.vue'
import OctoIssueRetests from '../OctoIssueRetests.vue'

const props = defineProps<{ kbs: Array<{ id: string; name: string }> }>()
const route = useRoute()
const router = useRouter()

const loading = ref(false)
const error = ref('')
const issue = ref<OctoIssue | null>(null)
const events = ref<IssueEvent[]>([])
const evidence = ref<IssueEvidence | null>(null)
const repair = ref<RepairView | null>(null)
const contacts = ref<OctoContact[]>([])
const selectedContactId = ref('')
const initialContactId = ref('')
const rightTab = ref<'reply' | 'close'>('reply')
const closeNote = ref('')
const savingStatus = ref(false)
const savingOwner = ref(false)
const instantRunning = ref(false)
const instantResult = ref('')
const modeSaving = ref(false)
const justReplied = ref(false)

const statusLabels: Record<string, string> = { open: '待处理', inprogress: '处理中', resolved: '已解决', closed: '已关闭' }

const issueId = computed(() => String(route.params.issueId || ''))
const kbName = computed(() => props.kbs.find((k) => k.id === issue.value?.knowledge_base_id)?.name || issue.value?.knowledge_base_id || '')
const isClosed = computed(() => Boolean(issue.value && ['resolved', 'closed'].includes(issue.value.status)))
const strictMode = computed(() => Boolean(repair.value && repair.value.verification_mode === 'strict'))
const contactOpts = computed(() => contacts.value.map((c) => ({ label: `${c.name} (${c.topic || '通用'})`, value: c.id })))
const ownerDirty = computed(() => selectedContactId.value !== initialContactId.value)
const displayReporter = computed(() => {
  if (!issue.value) return '—'
  const name = (issue.value.reporter_name || '').trim()
  const uid = (issue.value.reporter_uid || '').trim()
  if (name && name !== uid) return name
  if (uid.length > 16) return `用户 ${uid.slice(0, 8)}…`
  return name || uid || '—'
})

function statusTheme(status: string): 'warning' | 'primary' | 'success' | 'default' {
  if (status === 'resolved') return 'success'
  if (status === 'inprogress') return 'primary'
  if (status === 'closed') return 'default'
  return 'warning'
}

function formatTime(v: string) {
  if (!v) return '—'
  const d = new Date(v)
  return Number.isNaN(d.getTime())
    ? v
    : d.toLocaleString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}

function shortMsgId(id: string) {
  const parts = id.split(':')
  const tail = parts[parts.length - 1] || id
  return `消息 #${tail.length > 10 ? tail.slice(-8) : tail}`
}

function copyText(text: string, label: string) {
  if (!text) return
  navigator.clipboard?.writeText(text).then(() => MessagePlugin.success(`已复制${label}`)).catch(() => {})
}

function openKnowledgeBase() {
  if (!issue.value?.knowledge_base_id) return
  const resolved = router.resolve({ path: `/platform/knowledge-bases/${issue.value.knowledge_base_id}` })
  window.open(resolved.href, '_blank', 'noopener')
}

async function load() {
  if (!issueId.value) return
  loading.value = true
  error.value = ''
  try {
    const res = await issueDetail(issueId.value)
    issue.value = res.data.issue
    events.value = res.data.events || []
    evidence.value = res.data.evidence || null
    const [repRes, ctRes] = await Promise.allSettled([
      issueRepair(issueId.value),
      listContacts(res.data.issue.knowledge_base_id),
    ])
    if (repRes.status === 'fulfilled') repair.value = repRes.value.data
    if (ctRes.status === 'fulfilled') {
      contacts.value = ctRes.value.data || []
      const matched = contacts.value.find((c) => c.uid && c.uid === issue.value?.owner_uid)
      selectedContactId.value = matched?.id || ''
      initialContactId.value = selectedContactId.value
    }
  } catch {
    error.value = '加载问题详情失败，请确认记录存在且在授权范围内。'
  } finally {
    loading.value = false
  }
}

async function runInstantRetest() {
  if (!issue.value) return
  instantRunning.value = true
  try {
    const res = await retestIssue(issue.value.id, 'original', repair.value?.revision || 0)
    instantResult.value = res.data.answer || '复测完成（空答复）'
    MessagePlugin.success('一键即时复测完成')
  } catch {
    MessagePlugin.error('一键复测失败，请检查原问题、知识库与模型状态')
  } finally {
    instantRunning.value = false
  }
}

async function toggleStrictMode(next: boolean) {
  if (!issue.value || modeSaving.value) return
  modeSaving.value = true
  try {
    const mode = next ? 'strict' : 'lightweight'
    const res = await setIssueRemediationMode(issue.value.id, mode)
    repair.value = res.data
    MessagePlugin.success(next ? '已开启严格修订与三类回归门禁' : '已关闭严格门禁并退回轻量模式（已写审计）')
    await load()
  } catch {
    MessagePlugin.error('切换门禁模式失败')
  } finally {
    modeSaving.value = false
  }
}

async function saveOwner() {
  if (!issue.value || savingOwner.value) return
  savingOwner.value = true
  try {
    const body = selectedContactId.value
      ? {
          status: issue.value.status,
          owner_contact_id: selectedContactId.value,
          note: '更新工单跟进联系人',
        }
      : {
          status: issue.value.status,
          owner_uid: '',
          owner_name: '',
          note: '清空工单跟进联系人',
        }
    await updateIssue(issue.value.id, body)
    MessagePlugin.success('分派信息已保存')
    await load()
  } catch {
    MessagePlugin.error('保存分派失败，请检查联系人或权限')
  } finally {
    savingOwner.value = false
  }
}

function onReplySent() {
  justReplied.value = true
  void load()
}

async function quickResolve(defaultNote: string) {
  await updateStatus('resolved', defaultNote)
}

async function updateStatus(status: IssueStatus, overrideNote?: string) {
  if (!issue.value) return
  savingStatus.value = true
  try {
    const defaultNote =
      status === 'resolved' ? '已核实口径并完成闭环' : status === 'inprogress' ? '开始跟进处理' : '直接关闭'
    await updateIssue(issue.value.id, {
      status,
      owner_contact_id: selectedContactId.value || undefined,
      note: overrideNote || closeNote.value.trim() || defaultNote,
    })
    const labelMap: Record<IssueStatus, string> = {
      open: '已重置为待处理',
      inprogress: '已更新为「处理中」',
      resolved: '已标记为「已解决」',
      closed: '已直接关闭',
    }
    MessagePlugin.success(labelMap[status])
    closeNote.value = ''
    justReplied.value = false
    await load()
  } catch {
    MessagePlugin.error('状态更新失败（如开启了严格门禁，请先完成三类复测核验或切回轻量模式）')
  } finally {
    savingStatus.value = false
  }
}

watch(issueId, () => {
  instantResult.value = ''
  justReplied.value = false
  void load()
})
onMounted(load)
</script>

<style scoped lang="less">
.case-wrap { max-width: 1440px; margin: 0 auto; display: grid; gap: 14px; }
.mb12 { margin-bottom: 12px; }
.mb6 { margin-bottom: 6px; }
.mt4 { margin-top: 4px; }
.case-top {
  display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap;
  padding: 12px 16px; background: var(--td-bg-color-container); border: 1px solid var(--td-component-border); border-radius: 10px;
}
.top-l, .top-r { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.top-l h2 { margin: 0; font-size: 16px; }
.case-grid { display: grid; grid-template-columns: minmax(0, 1.58fr) minmax(340px, 0.98fr); gap: 15px; align-items: start; }
.case-left, .case-right { display: grid; gap: 14px; min-width: 0; }
.case-right { position: sticky; top: 12px; }
.meta-strip {
  display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; padding: 12px 14px;
  background: var(--td-bg-color-container); border: 1px solid var(--td-component-border); border-radius: 10px;
}
.meta-strip small { display: block; font-size: 11.5px; color: var(--td-text-color-placeholder); }
.meta-strip b { display: block; font-size: 13px; margin-top: 2px; overflow-wrap: anywhere; }
.copyable { cursor: pointer; }
.copyable:hover { color: var(--td-brand-color); }
.panel-card { background: var(--td-bg-color-container); border: 1px solid var(--td-component-border); border-radius: 10px; overflow: hidden; }
.panel-hd { display: flex; justify-content: space-between; align-items: center; gap: 10px; flex-wrap: wrap; padding: 12px 16px; border-bottom: 1px solid var(--td-component-border); }
.panel-hd.compact { border-bottom: 0; }
.panel-hd h3 { margin: 0; font-size: 14px; display: inline; }
.panel-hd small { font-size: 12px; color: var(--td-text-color-placeholder); margin-left: 8px; }
.panel-bd { padding: 14px 16px; }
.desc-box { padding: 10px 12px; border-radius: 8px; background: var(--td-bg-color-secondarycontainer); font-size: 13px; color: var(--td-text-color-secondary); }
.cmp-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.cmp-box { padding: 12px; border-radius: 8px; background: var(--td-bg-color-secondarycontainer); border: 1px solid var(--td-component-border); }
.cmp-box.old { border-left: 3px solid var(--td-error-color); }
.cmp-box.new { border-left: 3px solid var(--td-success-color); }
.cmp-box h4 { margin: 0 0 6px; font-size: 12.5px; color: var(--td-text-color-secondary); }
.next-cta {
  display: flex; justify-content: space-between; align-items: center; gap: 10px; flex-wrap: wrap;
  padding: 10px 12px; border-radius: 8px; background: var(--td-success-color-1); border: 1px solid var(--td-success-color-3); font-size: 13px;
}
.reply-done-bar {
  display: flex; justify-content: space-between; align-items: center; gap: 8px; flex-wrap: wrap;
  padding: 10px 12px; border-radius: 8px; background: var(--td-success-color-1); border: 1px solid var(--td-success-color-3); font-size: 12.5px;
}
.assign-row { display: flex; gap: 8px; align-items: center; }
.assign-row :deep(.t-select__wrap) { flex: 1; min-width: 0; }
.rtab-bar { display: flex; border-bottom: 1px solid var(--td-component-border); padding: 0 14px; gap: 8px; }
.rtab-bar button { padding: 10px 8px; border: 0; border-bottom: 2px solid transparent; background: transparent; cursor: pointer; font-size: 13px; color: var(--td-text-color-secondary); }
.rtab-bar button[aria-selected='true'] { color: var(--td-brand-color); border-bottom-color: var(--td-brand-color); font-weight: 600; }
.grid12 { display: grid; gap: 12px; }
.grid10 { display: grid; gap: 10px; }
.grid6 { display: grid; gap: 6px; }
.row-gap { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.fld { display: grid; gap: 5px; font-size: 12.5px; color: var(--td-text-color-secondary); }
.tl { list-style: none; margin: 0; padding: 0; display: grid; gap: 10px; font-size: 13px; }
.tl-hd { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.tl-note { color: var(--td-text-color-secondary); margin-top: 3px; }
.mini { font-size: 12px; color: var(--td-text-color-placeholder); }
@media (max-width: 1120px) {
  .case-grid, .cmp-grid { grid-template-columns: minmax(0, 1fr); }
  .meta-strip { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .case-right { position: static; }
}
</style>
