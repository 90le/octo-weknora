<template>
  <t-dialog v-model:visible="visible" header="接入群或子区" attach="body" :close-on-overlay-click="false"
    :confirm-loading="busy" :confirm-btn="{ content: checked ? '确认接入' : '检查接入条件', disabled: !target || loading }" @confirm="next">
    <template v-if="checked">
      <p><strong>{{ botOptions.find(b => b.value === account)?.label || account }} / {{ groupOptions.find(g => g.value === group)?.label || group }} / {{ kind === 'main' ? '主群' : checked }}</strong></p>
      <p class="hint">已核验目标区域。接入后查询绑定和维护授权为空，可到配置页分别设置。</p>
      <t-button variant="text" :disabled="busy" @click="checked = ''">返回修改</t-button>
    </template>
    <template v-else>
      <p class="hint">选择 Bot 在 Octo 已可访问的区域。接入子区前，先接入同一 Bot 的主群。</p>
      <label>搜索 Bot<t-input v-model="botQuery" :maxlength="80" clearable placeholder="名称或连接名" :disabled="busy" /></label>
      <label>Bot 连接<t-select v-model="account" :options="botOptions" :loading="botLoading" :disabled="busy" /></label>
      <t-button v-if="botRows.length < botTotal" variant="text" :loading="botLoading" @click="loadBots(true)">更多 Bot</t-button>
      <label>Octo 群<t-select v-model="group" :options="groupOptions" filterable :loading="loading" :disabled="!account || busy" /></label>
      <label>区域类型
        <t-radio-group v-model="kind" :disabled="busy">
          <t-radio-button value="main">主群</t-radio-button><t-radio-button value="child">子区</t-radio-button>
        </t-radio-group>
      </label>
      <label v-if="kind === 'child'">子区<t-select v-model="subarea" :options="areaOptions" filterable :loading="loading" :disabled="!group || busy" /></label>
      <t-button v-if="kind === 'child' && moreAreas" variant="text" :loading="loading" @click="loadAreas(true)">更多子区</t-button>
      <t-button v-if="account" variant="text" :disabled="busy || loading" @click="loadGroups">重新读取平台群</t-button>
    </template>
    <t-alert v-if="error" theme="error" role="alert">{{ error }}</t-alert>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { availableScopes, createScope, directoryBots, previewOctoAttachment, type AvailableOctoScope, type OctoBotEntry, type OctoScope } from '@/api/octo'

const visible = defineModel<boolean>('visible', { default: false })
const props = defineProps<{ initial: { account_id?: string; group_id?: string; subarea_id?: string } }>()
const emit = defineEmits<{ saved: [OctoScope] }>()
const account = ref(''), group = ref(''), subarea = ref(''), kind = ref('main'), botQuery = ref('')
const loading = ref(false), botLoading = ref(false), busy = ref(false), error = ref(''), checked = ref('')
const groups = ref<AvailableOctoScope[]>([]), areas = ref<AvailableOctoScope[]>([]), botRows = ref<OctoBotEntry[]>([])
const botTotal = ref(0), moreAreas = ref(false)
const botOptions = computed(() => {
  const result = botRows.value.map(b => ({ label: b.name === b.account_id ? b.name : b.name + ' · ' + b.account_id, value: b.account_id }))
  if (account.value && !result.some(b => b.value === account.value)) result.unshift({ label: account.value, value: account.value })
  return result
})
const groupOptions = computed(() => groups.value.map(g => ({ label: g.name, value: g.group_id })))
const areaOptions = computed(() => {
  const result = areas.value.map(a => ({ label: a.name, value: a.subarea_id }))
  if (subarea.value && !result.some(a => a.value === subarea.value)) result.unshift({ label: subarea.value, value: subarea.value })
  return result
})
const target = computed(() => account.value && group.value && (kind.value === 'main' || subarea.value)
  ? { account_id: account.value, group_id: group.value, subarea_id: kind.value === 'main' ? '' : subarea.value } : null)
let version = 0, botVersion = 0, operation = 0, nextAreaPage = 1
let timer: ReturnType<typeof setTimeout> | undefined

watch(visible, open => {
  ++version; ++botVersion; ++operation; clearTimeout(timer)
  loading.value = false; botLoading.value = false; busy.value = false
  error.value = ''; checked.value = ''; botRows.value = []; botTotal.value = 0
  groups.value = []; areas.value = []; moreAreas.value = false
  if (!open) { account.value = ''; group.value = ''; subarea.value = ''; return }
  account.value = props.initial.account_id || ''; group.value = props.initial.group_id || ''
  subarea.value = props.initial.subarea_id || ''; kind.value = subarea.value ? 'child' : 'main'; botQuery.value = ''
  void loadBots()
})
watch(account, () => { if (visible.value) void loadGroups() })
watch([group, kind], () => { if (visible.value) void loadAreas() })
watch(botQuery, () => { if (!visible.value) return; ++botVersion; clearTimeout(timer); timer = setTimeout(() => void loadBots(), 200) })
onBeforeUnmount(() => { ++version; ++botVersion; ++operation; clearTimeout(timer) })

async function loadBots(append = false) {
  const v = ++botVersion
  botLoading.value = true
  if (!append) { botRows.value = []; botTotal.value = 0 }
  try {
    const { data } = await directoryBots({ filter: 'connected', q: botQuery.value.trim(), limit: 20, offset: append ? botRows.value.length : 0 })
    if (v === botVersion) {
      botRows.value = append ? [...botRows.value, ...data.items] : data.items; botTotal.value = data.total
      if (!account.value && data.total === 1 && !botQuery.value) account.value = data.items[0]?.account_id || ''
    }
  } catch { if (v === botVersion) error.value = '无法读取 Bot 连接。' }
  finally { if (v === botVersion) botLoading.value = false }
}
async function loadGroups() {
  const v = ++version, bot = account.value
  groups.value = []; areas.value = []; moreAreas.value = false; error.value = ''; loading.value = !!bot
  if (!bot) { group.value = ''; subarea.value = ''; return }
  try {
    const { data } = await availableScopes(bot)
    if (v !== version) return
    groups.value = data
    if (!data.some(g => g.group_id === group.value)) { group.value = ''; subarea.value = '' }
  } catch { if (v === version) { group.value = ''; subarea.value = ''; error.value = '无法读取此 Bot 可访问的 Octo 群。' } }
  finally { if (v === version) loading.value = false }
  if (v === version) void loadAreas()
}
async function loadAreas(append = false) {
  // A group request may still be verifying that the preselected group is
  // visible to this Bot. It loads the areas itself once that is known.
  if (!groups.value.length) return
  if (!append) {
    areas.value = []; moreAreas.value = false; nextAreaPage = 1
    if (subarea.value !== props.initial.subarea_id || group.value !== props.initial.group_id) subarea.value = ''
  }
  const v = ++version
  loading.value = false
  if (kind.value !== 'child' || !group.value || !account.value) return
  loading.value = true; error.value = ''
  try {
    const { data } = await availableScopes(account.value, group.value, nextAreaPage)
    if (v === version) { areas.value = [...areas.value, ...data]; moreAreas.value = data.length === 100 && nextAreaPage < 100; nextAreaPage++ }
  } catch { if (v === version) error.value = '无法读取此 Bot 可访问的子区。' }
  finally { if (v === version) loading.value = false }
}
async function next() {
  const selection = target.value
  if (!selection || busy.value || loading.value) return
  const v = ++operation
  busy.value = true; error.value = ''
  try {
    if (!checked.value) {
      const { data } = await previewOctoAttachment(selection)
      if (v === operation) checked.value = data.display_name
    } else {
      const { data } = await createScope({ ...selection, inherit_parent: false })
      if (v === operation) { emit('saved', data); visible.value = false }
    }
  } catch (e) {
    if (v === operation) {
      checked.value = ''
      const status = (e as { status?: number })?.status
      error.value = status === 409 ? '此区域已接入，或 Bot 身份需要重新核验。' : status === 404 && kind.value === 'child'
        ? '请先接入此 Bot 的主群，并核对 Bot 是否能访问子区。' : '未能核验或接入区域。请核对 Bot 的连接和平台权限。'
    }
  } finally { if (v === operation) busy.value = false }
}
</script>

<style scoped>
label { display: grid; gap: 5px; margin: 10px 0; }
.hint { color: var(--td-text-color-secondary); line-height: 1.65; }
</style>
