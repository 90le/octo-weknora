<template>
  <div class="deliv-box">
    <div class="deliv-hd">
      <div>
        <b>Octo 发送 Bot 与多目标接收人</b>
        <div class="mini">支持该 Bot 已加入的全部群主区/子区（含未绑库管理群）及实时校验通过的好友 UID</div>
      </div>
      <t-tag v-if="invalidKeys.size > 0" theme="danger" variant="light" size="small">
        {{ invalidKeys.size }} 项不适用当前 Bot
      </t-tag>
      <t-tag v-else-if="targets.length > 0" theme="success" variant="light" size="small">
        已选 {{ targets.length }} 个目标
      </t-tag>
      <t-tag v-else theme="warning" variant="light" size="small">请至少添加 1 个目标</t-tag>
    </div>

    <label class="fld">
      <span>发送 Bot</span>
      <t-select
        :value="senderChannelId"
        :options="botOptions"
        placeholder="选择发送报告的 Octo Bot"
        @change="onBotChange"
      />
    </label>

    <div class="picked-box">
      <div class="row-between">
        <span class="mini"><b>已选接收目标</b>（{{ targets.length }} 个）</span>
        <t-button v-if="invalidKeys.size > 0" size="small" theme="danger" variant="outline" @click="clearInvalid">
          一键移除不适用项
        </t-button>
      </div>
      <div v-if="targets.length" class="chips">
        <span
          v-for="t in targets"
          :key="t.key"
          class="tchip"
          :class="{ bad: invalidKeys.has(t.key) }"
        >
          <span class="tchip-k">{{ t.channel_kind || (t.kind === 'dm' ? '私聊' : '主群') }}</span>
          <b>{{ t.label }}</b>
          <span class="tchip-sub">
            {{ invalidKeys.has(t.key) ? '不适用当前 Bot' : t.kind === 'dm' ? `UID ${t.uid}` : t.bound_kb_count ? `${t.inherited ? '继承' : '直绑'} ${t.bound_kb_count} 库` : '仅入群 · 未绑库' }}
          </span>
          <button type="button" class="tchip-x" @click="removeTarget(t.key)">×</button>
        </span>
      </div>
      <div v-else class="mini">尚未添加目标，可在下方混选群/子区或校验添加好友 UID。</div>
    </div>

    <t-loading :loading="loadingTargets" class="picker-box">
      <div class="row-between">
        <div class="seg">
          <button type="button" :aria-pressed="mode === 'group'" @click="mode = 'group'">
            群聊 / 子区 ({{ groups.length }})
          </button>
          <button type="button" :aria-pressed="mode === 'dm'" @click="mode = 'dm'">
            私聊好友 UID ({{ candidates.length }})
          </button>
        </div>
        <span class="mini">{{ mode === 'dm' ? '仅限该 Bot 好友（实时校验）' : '含未绑库汇报群' }}</span>
      </div>

      <div v-show="mode === 'group'" class="pane">
        <t-input v-model="groupQuery" size="small" clearable placeholder="搜索群名、子区或未绑库群..." />
        <div class="glist">
          <div
            v-for="g in filteredGroups"
            :key="g.key"
            class="grow"
            :class="{ added: hasTarget(g.key) }"
          >
            <div class="min0">
              <div class="gname">{{ g.label }}</div>
              <div class="gtags">
                <t-tag size="small" variant="outline">{{ g.kind }}</t-tag>
                <t-tag size="small" variant="light" :theme="g.bound_kb_count ? 'primary' : 'default'">
                  {{ g.bound_kb_count ? `${g.inherited ? '继承' : '直绑'} ${g.bound_kb_count} 库` : '仅入群 · 未绑库' }}
                </t-tag>
              </div>
            </div>
            <t-button size="small" :theme="hasTarget(g.key) ? 'default' : 'primary'" :variant="hasTarget(g.key) ? 'outline' : 'base'" @click="toggleGroup(g)">
              {{ hasTarget(g.key) ? '✓ 已添加' : '+ 添加' }}
            </t-button>
          </div>
          <div v-if="!filteredGroups.length" class="mini" style="padding: 8px">没有匹配的 Octo 群或子区</div>
        </div>
      </div>

      <div v-show="mode === 'dm'" class="pane">
        <div class="uid-row">
          <t-input v-model="uidInput" size="small" clearable placeholder="输入好友 UID" @change="onUidInput" />
          <t-button size="small" :loading="checkingUid" @click="checkUidNow">校验 UID</t-button>
        </div>
        <div v-if="uidCheck" class="uid-res" :class="uidCheck.ok ? 'ok' : 'err'">
          <span v-if="uidCheck.ok">
            已校验：<b>{{ uidCheck.name }}</b>（{{ uidCheck.uid }} · {{ uidCheck.is_creator ? 'Bot 创建者' : '当前 Bot 好友' }}）
          </span>
          <span v-else>{{ uidCheck.reason_message || '不是当前 Bot 好友' }}</span>
          <t-button
            v-if="uidCheck.ok"
            size="small"
            theme="primary"
            :disabled="hasTarget(`dm:${uidCheck.uid}`)"
            @click="addVerifiedUid"
          >
            {{ hasTarget(`dm:${uidCheck.uid}`) ? '已在目标中' : '+ 添加为接收目标' }}
          </t-button>
        </div>
        <div v-else class="uid-res">输入 Octo 好友 UID 后点「校验 UID」，或点下方候选自动填入并实时校验。</div>

        <div v-if="candidates.length">
          <div class="cand-hd">
            <span class="mini">系统近期私聊 / 联系人候选（点击自动填入并实时校验好友关系）：</span>
            <t-input
              v-model="candQuery"
              size="small"
              clearable
              class="cand-search"
              placeholder="按昵称或 UID 筛选候选..."
            />
          </div>
          <div class="cand-list">
            <t-button
              v-for="c in filteredCandidates"
              :key="c.uid"
              size="small"
              variant="outline"
              :title="`UID: ${c.uid}`"
              @click="pickCandidate(c.uid)"
            >
              <span class="cand-src">{{ candSourceLabel(c.source) }}</span>
              <b>{{ candDisplayName(c) }}</b>
              <span class="cand-uid">({{ shortUid(c.uid) }})</span>
            </t-button>
            <span v-if="!filteredCandidates.length" class="mini">没有匹配的候选成员</span>
          </div>
        </div>
      </div>
    </t-loading>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  botDeliveryTargets,
  verifyBotFriend,
  type BotDMCandidate,
  type BotDeliveryGroup,
  type BotFriendVerifyResult,
  type DeliveryTarget,
} from '@/api/octo-business'
import { partitionTargetsForBot } from './useOctoOpsScope'

const props = defineProps<{
  bots: Array<{ id: string; name: string }>
  senderChannelId: string
  targets: DeliveryTarget[]
}>()

const emit = defineEmits<{
  'update:senderChannelId': [string]
  'update:targets': [DeliveryTarget[]]
  'invalid-count': [number]
}>()

const mode = ref<'group' | 'dm'>('group')
const groupQuery = ref('')
const candQuery = ref('')
const uidInput = ref('')
const uidCheck = ref<BotFriendVerifyResult | null>(null)
const checkingUid = ref(false)
const loadingTargets = ref(false)
const groups = ref<BotDeliveryGroup[]>([])
const candidates = ref<BotDMCandidate[]>([])
const verifiedFriendsForCurrentBot = ref<Set<string>>(new Set())

const botOptions = computed(() => props.bots.map((b) => ({ label: b.name || b.id, value: b.id })))
const filteredGroups = computed(() => {
  const q = groupQuery.value.trim().toLowerCase()
  if (!q) return groups.value
  return groups.value.filter((g) => `${g.label} ${g.kind} ${g.bound_kb_count ? '已绑库' : '未绑库 仅入群'}`.toLowerCase().includes(q))
})
const filteredCandidates = computed(() => {
  const q = candQuery.value.trim().toLowerCase()
  if (!q) return candidates.value
  return candidates.value.filter((c) => `${c.name} ${c.uid} ${candSourceLabel(c.source)}`.toLowerCase().includes(q))
})
function candSourceLabel(src: string) {
  if (src === 'creator') return '创建者'
  if (src === 'contact') return '联系人'
  return '近期私聊'
}
function candDisplayName(c: BotDMCandidate) {
  const name = (c.name || '').trim()
  if (name && name !== c.uid) return name
  return `用户 ${shortUid(c.uid)}`
}
function shortUid(uid: string) {
  return uid.length > 10 ? `${uid.slice(0, 8)}…` : uid
}

const invalidKeys = computed(() => {
  const validGroupKeys = new Set(groups.value.map((g) => g.key))
  const { invalid } = partitionTargetsForBot(props.targets, validGroupKeys, verifiedFriendsForCurrentBot.value)
  return new Set(invalid.map((t) => t.key))
})

watch(invalidKeys, (s) => emit('invalid-count', s.size), { immediate: true })

function hasTarget(key: string) {
  return props.targets.some((t) => t.key === key)
}

async function loadBotTargets(channelId: string) {
  if (!channelId) return
  loadingTargets.value = true
  uidCheck.value = null
  verifiedFriendsForCurrentBot.value = new Set()
  try {
    const res = await botDeliveryTargets(channelId)
    groups.value = res.data.groups || []
    candidates.value = res.data.dm_candidates || []
    // Re-verify any already-picked DM targets against the newly selected Bot!
    for (const t of props.targets) {
      if (t.kind === 'dm' && t.uid) {
        try {
          const v = await verifyBotFriend(channelId, t.uid)
          if (v.data.ok) verifiedFriendsForCurrentBot.value.add(t.uid)
        } catch {
          // treat unverified as invalid on bot switch
        }
      }
    }
  } catch {
    MessagePlugin.warning('拉取该 Bot 的群/子区列表失败')
    groups.value = []
  } finally {
    loadingTargets.value = false
  }
}

function onBotChange(val: string) {
  emit('update:senderChannelId', val)
}

function toggleGroup(g: BotDeliveryGroup) {
  const next = [...props.targets]
  const idx = next.findIndex((t) => t.key === g.key)
  if (idx >= 0) {
    next.splice(idx, 1)
  } else {
    next.push({
      key: g.key,
      kind: 'group',
      group_id: g.group_id,
      subarea_id: g.subarea_id,
      label: g.label,
      channel_kind: g.kind,
      bound_kb_count: g.bound_kb_count,
      inherited: g.inherited,
    })
  }
  emit('update:targets', next)
}

function removeTarget(key: string) {
  emit('update:targets', props.targets.filter((t) => t.key !== key))
}

function clearInvalid() {
  emit('update:targets', props.targets.filter((t) => !invalidKeys.value.has(t.key)))
  MessagePlugin.success('已移除不适用于当前 Bot 的目标')
}

function onUidInput() {
  if (uidCheck.value && uidCheck.value.uid !== uidInput.value.trim()) {
    uidCheck.value = null
  }
}

async function checkUidNow() {
  const uid = uidInput.value.trim()
  if (!uid || !props.senderChannelId) return
  checkingUid.value = true
  try {
    const res = await verifyBotFriend(props.senderChannelId, uid)
    uidCheck.value = res.data
    if (res.data.ok) verifiedFriendsForCurrentBot.value.add(res.data.uid)
  } catch {
    uidCheck.value = { ok: false, uid, name: '', is_creator: false, reason_message: '好友校验接口请求失败' }
  } finally {
    checkingUid.value = false
  }
}

function pickCandidate(uid: string) {
  uidInput.value = uid
  void checkUidNow()
}

function addVerifiedUid() {
  if (!uidCheck.value?.ok) return
  const c = uidCheck.value
  const key = `dm:${c.uid}`
  const next = props.targets.filter((t) => t.key !== key)
  next.push({ key, kind: 'dm', uid: c.uid, label: c.name || c.uid, channel_kind: '私聊' })
  emit('update:targets', next)
  uidInput.value = ''
  uidCheck.value = null
}

watch(() => props.senderChannelId, (id) => void loadBotTargets(id), { immediate: true })
</script>

<style scoped lang="less">
.deliv-box { display: grid; gap: 11px; padding: 13px; border: 1px solid var(--td-component-border); border-radius: 10px; background: var(--td-bg-color-secondarycontainer); }
.deliv-hd, .row-between { display: flex; justify-content: space-between; align-items: center; gap: 8px; flex-wrap: wrap; }
.fld { display: grid; gap: 5px; font-size: 12.5px; color: var(--td-text-color-secondary); }
.picked-box, .picker-box { display: grid; gap: 8px; padding: 10px 12px; border: 1px solid var(--td-component-border); border-radius: 8px; background: var(--td-bg-color-container); }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.tchip { display: inline-flex; align-items: center; gap: 5px; padding: 3px 7px; border-radius: 6px; border: 1px solid var(--td-brand-color-3); background: var(--td-brand-color-light); font-size: 12px; }
.tchip.bad { border-color: var(--td-error-color-3); background: var(--td-error-color-1); }
.tchip-k { padding: 1px 5px; border-radius: 4px; background: var(--td-bg-color-container); color: var(--td-brand-color); font-size: 11px; font-weight: 600; }
.tchip.bad .tchip-k, .tchip.bad .tchip-sub { color: var(--td-error-color); }
.tchip-sub { color: var(--td-text-color-placeholder); font-size: 11px; }
.tchip-x { border: 0; background: transparent; cursor: pointer; font-size: 14px; color: var(--td-text-color-secondary); padding: 0 3px; }
.seg { display: inline-flex; padding: 2px; border-radius: 6px; background: var(--td-bg-color-secondarycontainer); border: 1px solid var(--td-component-border); }
.seg button { height: 26px; padding: 0 10px; border: 0; border-radius: 4px; background: transparent; font-size: 12px; color: var(--td-text-color-secondary); cursor: pointer; }
.seg button[aria-pressed='true'] { background: var(--td-bg-color-container); color: var(--td-brand-color); font-weight: 600; }
.pane { display: grid; gap: 8px; }
.glist { display: grid; gap: 6px; max-height: 180px; overflow-y: auto; }
.grow { display: flex; justify-content: space-between; align-items: center; gap: 10px; padding: 7px 10px; border-radius: 6px; border: 1px solid var(--td-component-border); background: var(--td-bg-color-secondarycontainer); }
.grow.added { border-color: var(--td-brand-color-3); background: var(--td-brand-color-light); }
.gname { font-size: 12.5px; font-weight: 600; }
.gtags { display: flex; gap: 5px; margin-top: 3px; }
.uid-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }
.uid-res { display: flex; justify-content: space-between; align-items: center; gap: 8px; flex-wrap: wrap; padding: 8px 10px; border-radius: 6px; font-size: 12px; border: 1px solid var(--td-component-border); background: var(--td-bg-color-secondarycontainer); }
.uid-res.ok { border-color: var(--td-success-color-3); background: var(--td-success-color-1); color: var(--td-success-color); }
.uid-res.err { border-color: var(--td-error-color-3); background: var(--td-error-color-1); color: var(--td-error-color); }
.cand-hd { display: flex; justify-content: space-between; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 6px; }
.cand-search { width: 190px; }
.cand-list { display: flex; flex-wrap: wrap; gap: 6px; max-height: 150px; overflow-y: auto; }
.cand-src { display: inline-block; margin-right: 4px; padding: 0 4px; border-radius: 4px; font-size: 11px; background: var(--td-bg-color-secondarycontainer); color: var(--td-text-color-secondary); }
.cand-uid { margin-left: 4px; font-size: 11px; color: var(--td-text-color-placeholder); }
.min0 { min-width: 0; }
.mini { font-size: 12px; color: var(--td-text-color-placeholder); }
</style>
