<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import {
  deleteDataSourceWithMode,
  previewDataSourceDeletion,
  type DataSource,
  type DataSourceDeleteMode,
  type DataSourceDeletePreview,
} from '@/api/datasource'
import {
  DEFAULT_DATASOURCE_DELETE_MODE,
  formatGeneratedStorageBytes,
  hasLegacyUnverifiableResources,
  hasRetainedDeleteResources,
} from './datasourceDeleteState'

const props = defineProps<{
  visible: boolean
  dataSource: DataSource | null
}>()

const emit = defineEmits<{
  (event: 'update:visible', value: boolean): void
  (event: 'completed', mode: DataSourceDeleteMode): void
}>()

const { t, locale } = useI18n()
const loadingPreview = ref(false)
const submitting = ref(false)
const preview = ref<DataSourceDeletePreview | null>(null)
const previewError = ref('')
const selectedMode = ref<DataSourceDeleteMode>(DEFAULT_DATASOURCE_DELETE_MODE)
let previewGeneration = 0

function unwrapResponse<T>(response: unknown): T {
  const value = response as { data?: unknown } | undefined
  return (value?.data ?? response) as T
}

function readError(error: unknown, fallback: string): string {
  const value = error as {
    message?: string
    error?: string | { message?: string }
    response?: { data?: { error?: string | { message?: string }; message?: string } }
  }
  const responseError = value?.response?.data?.error
  return typeof responseError === 'string'
    ? responseError
    : responseError?.message || value?.response?.data?.message || value?.message ||
        (typeof value?.error === 'string' ? value.error : value?.error?.message) || fallback
}

const formattedExpiry = computed(() => {
  if (!preview.value?.expires_at) return ''
  const date = new Date(preview.value.expires_at)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleString(locale.value)
})

const confirmButton = computed(() => ({
  content: t(selectedMode.value === 'purge_generated'
    ? 'datasource.deleteFlow.confirmPurge'
    : 'datasource.deleteFlow.confirmDetach'),
  theme: selectedMode.value === 'purge_generated' ? 'danger' : 'primary',
  loading: submitting.value,
  disabled: !preview.value || loadingPreview.value || (
    selectedMode.value === 'purge_generated' &&
    hasLegacyUnverifiableResources(preview.value.legacy_unverifiable_resources_count)
  ),
}))

async function loadPreview() {
  const sourceId = props.dataSource?.id
  if (!sourceId) return

  const generation = ++previewGeneration
  loadingPreview.value = true
  previewError.value = ''
  preview.value = null
  try {
    const response = await previewDataSourceDeletion(sourceId)
    const next = unwrapResponse<DataSourceDeletePreview>(response)
    // A delayed response from a previously selected card must never supply a
    // token for the newly selected source.
    if (generation !== previewGeneration || props.dataSource?.id !== sourceId) return
    preview.value = next
  } catch (error) {
    if (generation !== previewGeneration) return
    previewError.value = readError(error, t('datasource.deleteFlow.previewFailed'))
  } finally {
    if (generation === previewGeneration) loadingPreview.value = false
  }
}

function resetForOpen() {
  selectedMode.value = DEFAULT_DATASOURCE_DELETE_MODE
  preview.value = null
  previewError.value = ''
  void loadPreview()
}

watch(
  () => [props.visible, props.dataSource?.id] as const,
  ([visible]) => {
    if (visible && props.dataSource?.id) resetForOpen()
    if (!visible) previewGeneration += 1
  },
)

function updateVisibility(next: boolean) {
  if (!next && submitting.value) return
  emit('update:visible', next)
}

async function confirmDeletion() {
  const sourceId = props.dataSource?.id
  const token = preview.value?.preview_token
  if (!sourceId || !token || submitting.value) return

  submitting.value = true
  try {
    await deleteDataSourceWithMode(sourceId, {
      mode: selectedMode.value,
      preview_token: token,
    })
    MessagePlugin.success(t(selectedMode.value === 'purge_generated'
      ? 'datasource.deleteFlow.purged'
      : 'datasource.deleteFlow.detached'))
    emit('completed', selectedMode.value)
    emit('update:visible', false)
  } catch (error) {
    MessagePlugin.error(readError(error, t('datasource.deleteFlow.deleteFailed')))
    // Tokens are deliberately short-lived. Refresh after a rejected request,
    // then require an explicit second confirmation with the new preview.
    await loadPreview()
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <t-dialog
    :visible="visible"
    width="560px"
    :confirm-btn="confirmButton"
    :cancel-btn="{ content: t('datasource.deleteFlow.cancel') }"
    :close-on-overlay-click="!submitting"
    :close-on-esc-keydown="!submitting"
    @update:visible="updateVisibility"
    @confirm="confirmDeletion"
  >
    <template #header>
      <span class="data-source-delete-dialog__header">
        <t-icon name="delete" aria-hidden="true" />
        <span>{{ t('datasource.deleteFlow.title') }}</span>
      </span>
    </template>

    <div class="data-source-delete-dialog">
      <p class="data-source-delete-dialog__description">
        {{ t('datasource.deleteFlow.description') }}
      </p>

      <t-loading :loading="loadingPreview" size="small">
        <div v-if="loadingPreview" class="data-source-delete-dialog__loading">
          {{ t('datasource.deleteFlow.loadingPreview') }}
        </div>

        <t-alert
          v-else-if="previewError"
          theme="error"
          :message="previewError"
        >
          <template #operation>
            <t-button variant="text" size="small" @click="loadPreview">
              {{ t('datasource.deleteFlow.refreshPreview') }}
            </t-button>
          </template>
        </t-alert>

        <template v-else-if="preview">
          <section class="data-source-delete-dialog__preview" :aria-label="t('datasource.deleteFlow.previewTitle')">
            <div class="data-source-delete-dialog__section-heading">
              <h3>{{ t('datasource.deleteFlow.previewTitle') }}</h3>
              <span v-if="formattedExpiry">{{ t('datasource.deleteFlow.previewExpiry', { time: formattedExpiry }) }}</span>
            </div>
            <dl class="data-source-delete-dialog__metrics">
              <div>
                <dt>{{ t('datasource.deleteFlow.generatedKnowledge') }}</dt>
                <dd>{{ preview.generated_knowledge_count }}</dd>
              </div>
              <div>
                <dt>{{ t('datasource.deleteFlow.generatedStorage') }}</dt>
                <dd>{{ formatGeneratedStorageBytes(preview.generated_storage_bytes, locale) }}</dd>
              </div>
              <div>
                <dt>{{ t('datasource.deleteFlow.retainedResources') }}</dt>
                <dd>{{ preview.shared_or_unverifiable_resources_count }}</dd>
              </div>
            </dl>
            <p
              class="data-source-delete-dialog__retained-note"
              :class="{ 'is-warning': hasRetainedDeleteResources(preview.shared_or_unverifiable_resources_count) }"
            >
              <t-icon :name="hasRetainedDeleteResources(preview.shared_or_unverifiable_resources_count) ? 'info-circle-filled' : 'check-circle-filled'" aria-hidden="true" />
              <span>{{ hasRetainedDeleteResources(preview.shared_or_unverifiable_resources_count)
                ? t('datasource.deleteFlow.retainedResourcesHint')
                : t('datasource.deleteFlow.noRetainedResources') }}</span>
            </p>
            <t-alert
              v-if="hasLegacyUnverifiableResources(preview.legacy_unverifiable_resources_count)"
              class="data-source-delete-dialog__legacy-warning"
              theme="warning"
              :message="t('datasource.deleteFlow.legacyBlocked', { count: preview.legacy_unverifiable_resources_count })"
            />
          </section>

          <t-radio-group v-model="selectedMode" class="data-source-delete-dialog__modes">
            <t-radio value="detach" class="data-source-delete-dialog__mode">
              <span class="data-source-delete-dialog__mode-copy">
                <strong>{{ t('datasource.deleteFlow.detachTitle') }}</strong>
                <small>{{ t('datasource.deleteFlow.detachDescription') }}</small>
              </span>
            </t-radio>
            <t-radio
              value="purge_generated"
              :disabled="hasLegacyUnverifiableResources(preview.legacy_unverifiable_resources_count)"
              class="data-source-delete-dialog__mode data-source-delete-dialog__mode--danger"
            >
              <span class="data-source-delete-dialog__mode-copy">
                <strong>{{ t('datasource.deleteFlow.purgeTitle') }}</strong>
                <small>{{ t('datasource.deleteFlow.purgeDescription') }}</small>
                <em>{{ t('datasource.deleteFlow.purgeWarning') }}</em>
              </span>
            </t-radio>
          </t-radio-group>
        </template>
      </t-loading>
    </div>
  </t-dialog>
</template>

<style scoped lang="less">
.data-source-delete-dialog__header {
  display: inline-flex;
  align-items: center;
  gap: 8px;

  .t-icon { color: var(--td-error-color); }
}

.data-source-delete-dialog {
  color: var(--td-text-color-primary);

  &__description {
    margin: 0 0 18px;
    color: var(--td-text-color-secondary);
    font-size: 13px;
    line-height: 1.6;
  }

  &__loading {
    padding: 28px 0;
    color: var(--td-text-color-secondary);
    font-size: 13px;
    text-align: center;
  }

  &__preview {
    padding: 0 0 16px;
    border-bottom: 1px solid var(--td-component-stroke);
  }

  &__section-heading {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 12px;

    h3 {
      margin: 0;
      font-size: 14px;
      font-weight: 600;
    }

    span {
      color: var(--td-text-color-placeholder);
      font-size: 11px;
      line-height: 1.4;
      text-align: right;
    }
  }

  &__metrics {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
    margin: 0;

    div { min-width: 0; }
    dt {
      overflow: hidden;
      color: var(--td-text-color-secondary);
      font-size: 12px;
      line-height: 1.45;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    dd {
      margin: 4px 0 0;
      color: var(--td-text-color-primary);
      font-size: 18px;
      font-variant-numeric: tabular-nums;
      font-weight: 600;
      line-height: 1.3;
    }
  }

  &__retained-note {
    display: flex;
    align-items: flex-start;
    gap: 6px;
    margin: 12px 0 0;
    color: var(--td-text-color-secondary);
    font-size: 12px;
    line-height: 1.5;

    .t-icon { flex: none; margin-top: 2px; color: var(--td-success-color); }
    &.is-warning .t-icon { color: var(--td-warning-color); }
  }

  &__legacy-warning { margin-top: 12px; }

  &__modes {
    display: flex;
    flex-direction: column;
    gap: 0;
    width: 100%;
    margin-top: 6px;
  }

  &__mode {
    display: flex;
    align-items: flex-start;
    padding: 14px 0;
    border-bottom: 1px solid var(--td-component-stroke);

    &:last-child { border-bottom: 0; }
    :deep(.t-radio__label) {
      display: flex;
      flex: 1;
      min-width: 0;
      white-space: normal;
    }

    &-copy {
      display: flex;
      flex: 1;
      flex-direction: column;
      gap: 3px;
      min-width: 0;
    }

    strong { font-size: 13px; font-weight: 600; line-height: 1.45; }
    small { color: var(--td-text-color-secondary); font-size: 12px; line-height: 1.55; }
    em { color: var(--td-error-color); font-size: 12px; font-style: normal; line-height: 1.5; }
  }

  &__mode--danger strong { color: var(--td-error-color); }
}

@media (max-width: 560px) {
  .data-source-delete-dialog {
    &__section-heading { align-items: flex-start; flex-direction: column; gap: 4px; }
    &__section-heading span { text-align: left; }
    &__metrics { grid-template-columns: 1fr; gap: 8px; }
    &__metrics div { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
    &__metrics dd { margin: 0; }
  }
}
</style>
