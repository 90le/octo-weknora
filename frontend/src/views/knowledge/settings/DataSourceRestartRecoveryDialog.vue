<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import {
  getDataSourceRestartRecoveryRun,
  previewDataSourceRestartRecovery,
  startDataSourceRestartRecovery,
  type DataSource,
  type DataSourceRestartRecoveryCandidate,
  type DataSourceRestartRecoveryCandidateState,
  type DataSourceRestartRecoveryPreview,
  type DataSourceRestartRecoveryRun,
  type DataSourceRestartRecoveryRunStatus,
} from '@/api/datasource'
import {
  canStartRestartRecovery,
  isRestartRecoveryTerminal,
} from './datasourceRestartRecoveryState'

const props = defineProps<{
  visible: boolean
  dataSource: DataSource | null
}>()

const emit = defineEmits<{
  (event: 'update:visible', value: boolean): void
  (event: 'completed'): void
}>()

const { t, locale } = useI18n()
const loadingPreview = ref(false)
const starting = ref(false)
const preview = ref<DataSourceRestartRecoveryPreview | null>(null)
const previewError = ref('')
const run = ref<DataSourceRestartRecoveryRun | null>(null)
const runError = ref('')
let previewGeneration = 0
let pollTimer: number | null = null
let terminalNotified = false

function unwrapResponse<T>(response: unknown): T {
  const value = response as { data?: unknown } | undefined
  return (value?.data ?? response) as T
}

function stopPolling() {
  if (pollTimer !== null) {
    window.clearTimeout(pollTimer)
    pollTimer = null
  }
}

function candidateTheme(state: DataSourceRestartRecoveryCandidateState) {
  if (state === 'pending' || state === 'published') return 'success'
  if (state === 'blocked') return 'warning'
  if (state === 'failed') return 'danger'
  if (state === 'reparsing') return 'primary'
  return 'default'
}

function candidateLabel(state: DataSourceRestartRecoveryCandidateState) {
  if (state === 'pending') return t('datasource.restartRecovery.eligible')
  if (state === 'excluded') return t('datasource.restartRecovery.excluded')
  if (state === 'blocked') return t('datasource.restartRecovery.blocked')
  if (state === 'reparsing') return t('datasource.restartRecovery.runRunning')
  if (state === 'published') return t('datasource.restartRecovery.runCompleted')
  return t('datasource.restartRecovery.runFailed')
}

function runLabel(status?: DataSourceRestartRecoveryRunStatus) {
  switch (status) {
    case 'pending': return t('datasource.restartRecovery.runPending')
    case 'running': return t('datasource.restartRecovery.runRunning')
    case 'completed': return t('datasource.restartRecovery.runCompleted')
    case 'partial': return t('datasource.restartRecovery.runPartial')
    case 'blocked': return t('datasource.restartRecovery.runBlocked')
    default: return t('datasource.restartRecovery.runFailed')
  }
}

function runTheme(status?: DataSourceRestartRecoveryRunStatus) {
  if (status === 'completed') return 'success'
  if (status === 'partial' || status === 'blocked') return 'warning'
  if (status === 'pending' || status === 'running') return 'primary'
  return 'danger'
}

function candidateTitle(candidate: DataSourceRestartRecoveryCandidate) {
  return candidate.title || candidate.file_name || t('datasource.restartRecovery.unnamedCandidate')
}

const formattedExpiry = computed(() => {
  if (!preview.value?.expires_at) return ''
  const date = new Date(preview.value.expires_at)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString(locale.value)
})

const canStart = computed(() => canStartRestartRecovery(preview.value, loadingPreview.value, starting.value))

const hasActiveRun = computed(() => Boolean(run.value && !isRestartRecoveryTerminal(run.value.status)))

const confirmButton = computed(() => {
  if (run.value) {
    return {
      content: hasActiveRun.value ? runLabel(run.value.status) : t('datasource.restartRecovery.close'),
      theme: 'primary',
      disabled: hasActiveRun.value,
    }
  }
  return {
    content: t(starting.value ? 'datasource.restartRecovery.starting' : 'datasource.restartRecovery.start'),
    theme: 'primary',
    loading: starting.value,
    disabled: !canStart.value,
  }
})

async function loadPreview() {
  const sourceId = props.dataSource?.id
  if (!sourceId || starting.value || run.value) return

  const generation = ++previewGeneration
  loadingPreview.value = true
  previewError.value = ''
  preview.value = null
  try {
    const response = await previewDataSourceRestartRecovery(sourceId)
    if (generation !== previewGeneration || props.dataSource?.id !== sourceId) return
    preview.value = unwrapResponse<DataSourceRestartRecoveryPreview>(response)
  } catch {
    if (generation !== previewGeneration) return
    // Recovery errors can name a storage backend or a candidate. Keep the
    // manager UI display-safe; server logs and the run record retain the
    // operational detail for an authorized operator.
    previewError.value = t('datasource.restartRecovery.previewFailed')
  } finally {
    if (generation === previewGeneration) loadingPreview.value = false
  }
}

function scheduleRunPoll() {
  stopPolling()
  if (!run.value || isRestartRecoveryTerminal(run.value.status) || !props.visible) return
  pollTimer = window.setTimeout(() => void pollRun(), 2000)
}

async function pollRun() {
  const runId = run.value?.id
  if (!runId || !props.visible || isRestartRecoveryTerminal(run.value?.status)) return
  try {
    const response = await getDataSourceRestartRecoveryRun(runId)
    if (!run.value || run.value.id !== runId) return
    run.value = unwrapResponse<DataSourceRestartRecoveryRun>(response)
    runError.value = ''
    if (isRestartRecoveryTerminal(run.value.status)) {
      if (!terminalNotified) {
        terminalNotified = true
        if (run.value.status === 'completed') {
          MessagePlugin.success(runLabel(run.value.status))
        } else {
          MessagePlugin.warning(runLabel(run.value.status))
        }
      }
      emit('completed')
      stopPolling()
      return
    }
  } catch {
    runError.value = t('datasource.restartRecovery.pollFailed')
  }
  scheduleRunPoll()
}

async function startRecovery() {
  const sourceId = props.dataSource?.id
  const token = preview.value?.preview_token
  if (!sourceId || !token || !canStart.value) return

  starting.value = true
  previewError.value = ''
  let refreshPreview = false
  try {
    const response = await startDataSourceRestartRecovery(sourceId, { preview_token: token })
    run.value = unwrapResponse<DataSourceRestartRecoveryRun>(response)
    // The token has served its only UI purpose. Do not keep it in a visible
    // preview after execution begins; status polling is bound to the run ID.
    preview.value = null
    terminalNotified = false
    MessagePlugin.success(t('datasource.restartRecovery.started'))
    await pollRun()
  } catch {
    MessagePlugin.error(t('datasource.restartRecovery.startFailed'))
    // A rejected token may be stale. Require the manager to review a new
    // preview rather than silently starting a different recovery plan.
    refreshPreview = true
  } finally {
    starting.value = false
    if (refreshPreview) await loadPreview()
  }
}

function resetForOpen() {
  stopPolling()
  terminalNotified = false
  run.value = null
  runError.value = ''
  preview.value = null
  previewError.value = ''
  void loadPreview()
}

function updateVisibility(next: boolean) {
  // The current backend exposes a run only by run ID. Keep the dialog open
  // while it is active so a manager cannot lose the only visible status handle
  // and accidentally launch a second recovery from a fresh preview.
  if (!next && (starting.value || hasActiveRun.value)) return
  emit('update:visible', next)
}

function handleConfirm() {
  if (run.value) {
    updateVisibility(false)
    return
  }
  void startRecovery()
}

watch(
  () => [props.visible, props.dataSource?.id] as const,
  ([visible]) => {
    if (visible && props.dataSource?.id) resetForOpen()
    if (!visible) {
      previewGeneration += 1
      stopPolling()
      preview.value = null
      run.value = null
    }
  },
)

onBeforeUnmount(stopPolling)
</script>

<template>
  <t-dialog
    :visible="visible"
    width="620px"
    :confirm-btn="confirmButton"
    :cancel-btn="run ? false : { content: t('datasource.restartRecovery.cancel') }"
    :close-on-overlay-click="!starting && !hasActiveRun"
    :close-on-esc-keydown="!starting && !hasActiveRun"
    @update:visible="updateVisibility"
    @confirm="handleConfirm"
  >
    <template #header>
      <span class="restart-recovery__header">
        <t-icon name="refresh" aria-hidden="true" />
        <span>{{ t('datasource.restartRecovery.title') }}</span>
      </span>
    </template>

    <div class="restart-recovery">
      <p class="restart-recovery__description">{{ t('datasource.restartRecovery.description') }}</p>

      <section v-if="run" class="restart-recovery__run" :aria-label="t('datasource.restartRecovery.runStatus')">
        <div class="restart-recovery__section-heading">
          <h3>{{ t('datasource.restartRecovery.runStatus') }}</h3>
          <t-tag :theme="runTheme(run.status)" variant="light">{{ runLabel(run.status) }}</t-tag>
        </div>
        <p v-if="run.error_message" class="restart-recovery__run-error">{{ t('datasource.restartRecovery.runDetailsHidden') }}</p>
        <t-alert v-if="runError" theme="warning" :message="runError" />
      </section>

      <t-loading v-else :loading="loadingPreview" size="small">
        <div v-if="loadingPreview" class="restart-recovery__loading">
          {{ t('datasource.restartRecovery.loadingPreview') }}
        </div>

        <t-alert v-else-if="previewError" theme="error" :message="previewError">
          <template #operation>
            <t-button variant="text" size="small" @click="loadPreview">
              {{ t('datasource.restartRecovery.refreshPreview') }}
            </t-button>
          </template>
        </t-alert>

        <template v-else-if="preview">
          <section class="restart-recovery__summary">
            <div class="restart-recovery__section-heading">
              <h3>{{ t('datasource.restartRecovery.candidates') }}</h3>
              <span v-if="formattedExpiry">{{ t('datasource.restartRecovery.previewExpiry', { time: formattedExpiry }) }}</span>
            </div>
            <div class="restart-recovery__metrics">
              <span><strong>{{ preview.eligible_count }}</strong>{{ t('datasource.restartRecovery.eligible') }}</span>
              <span><strong>{{ preview.excluded_count }}</strong>{{ t('datasource.restartRecovery.excluded') }}</span>
              <span><strong>{{ preview.blocked_count }}</strong>{{ t('datasource.restartRecovery.blocked') }}</span>
            </div>
          </section>

          <t-alert
            v-for="blocker in preview.blockers || []"
            :key="blocker"
            class="restart-recovery__blocker"
            theme="warning"
            :message="blocker"
          />

          <p v-if="preview.candidates.length === 0" class="restart-recovery__empty">
            {{ t('datasource.restartRecovery.noCandidates') }}
          </p>
          <ul v-else class="restart-recovery__candidates">
            <li v-for="candidate in preview.candidates" :key="candidate.knowledge_id">
              <div class="restart-recovery__candidate-main">
                <span class="restart-recovery__candidate-title" :title="candidateTitle(candidate)">{{ candidateTitle(candidate) }}</span>
                <t-tag :theme="candidateTheme(candidate.state)" variant="light" size="small">
                  {{ candidateLabel(candidate.state) }}
                </t-tag>
              </div>
              <p v-if="candidate.reason" class="restart-recovery__candidate-reason">{{ candidate.reason }}</p>
            </li>
          </ul>
        </template>
      </t-loading>
    </div>
  </t-dialog>
</template>

<style scoped lang="less">
.restart-recovery {
  color: var(--td-text-color-primary);

  &__header {
    display: inline-flex;
    align-items: center;
    gap: 8px;

    .t-icon { color: var(--td-brand-color); }
  }

  &__description {
    margin: 0 0 18px;
    color: var(--td-text-color-secondary);
    font-size: 13px;
    line-height: 1.65;
  }

  &__loading,
  &__empty {
    margin: 0;
    padding: 28px 0;
    color: var(--td-text-color-secondary);
    font-size: 13px;
    text-align: center;
  }

  &__summary,
  &__run {
    padding: 0 0 16px;
    border-bottom: 1px solid var(--td-component-stroke);
  }

  &__section-heading {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 12px;

    h3 {
      margin: 0;
      font-size: 14px;
      font-weight: 600;
    }

    > span {
      color: var(--td-text-color-placeholder);
      font-size: 11px;
      line-height: 1.45;
      text-align: right;
    }
  }

  &__metrics {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;

    span {
      display: inline-flex;
      align-items: baseline;
      gap: 4px;
      padding: 6px 9px;
      border-radius: 6px;
      background: var(--td-bg-color-secondarycontainer);
      color: var(--td-text-color-secondary);
      font-size: 12px;
    }

    strong {
      color: var(--td-text-color-primary);
      font-size: 16px;
      font-variant-numeric: tabular-nums;
    }
  }

  &__blocker { margin-top: 12px; }

  &__candidates {
    display: grid;
    gap: 8px;
    max-height: 280px;
    margin: 16px 0 0;
    padding: 0;
    overflow: auto;
    list-style: none;

    li {
      padding: 10px 12px;
      border: 1px solid var(--td-component-stroke);
      border-radius: 7px;
      background: var(--td-bg-color-container);
    }
  }

  &__candidate-main {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    min-width: 0;
  }

  &__candidate-title {
    overflow: hidden;
    font-size: 13px;
    font-weight: 500;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  &__candidate-reason,
  &__run-error {
    margin: 5px 0 0;
    color: var(--td-text-color-secondary);
    font-size: 12px;
    line-height: 1.55;
    overflow-wrap: anywhere;
  }

  &__run-error { color: var(--td-error-color); }
}
</style>
