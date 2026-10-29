<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import { getLocalDeletionReview, confirmLocalDeletionReview, type DataSource, type LocalDeletionReviewPage } from '@/api/datasource'

const props = defineProps<{ visible: boolean; source: DataSource | null }>()
const emit = defineEmits<{ (event: 'update:visible', value: boolean): void; (event: 'completed'): void }>()
const { t } = useI18n()
const page = ref<LocalDeletionReviewPage | null>(null)
const paths = ref<string[]>([])
const error = ref('')
const busy = ref(false)
const submitting = ref(false)
let generation = 0
const confirmButton = computed(() => ({
  content: t('storageSpaces.confirmDeletionReview'), theme: 'danger', loading: submitting.value,
  disabled: !page.value || page.value.has_more || !!error.value || busy.value,
}))

async function load(reset = false) {
  const source = props.source
  if (!source || busy.value) return
  const current = generation
  const previous = reset ? null : page.value
  const offset = previous?.next_offset || 0
  busy.value = true
  error.value = ''
  if (reset) { page.value = null; paths.value = [] }
  try {
    const response: any = await getLocalDeletionReview(source.id, offset)
    const next = (response?.data ?? response) as LocalDeletionReviewPage
    if (current !== generation || source.id !== props.source?.id) return
    if (previous && previous.digest !== next.digest) throw new Error(t('storageSpaces.reviewChanged'))
    page.value = next
    paths.value = reset ? next.paths : [...paths.value, ...next.paths]
  } catch (e: any) {
    if (current === generation) error.value = e?.message || t('storageSpaces.reviewUnavailable')
  } finally { if (current === generation) busy.value = false }
}

watch(() => [props.visible, props.source?.id] as const, ([visible]) => {
  ++generation
  busy.value = false
  page.value = null
  paths.value = []
  error.value = ''
  if (visible && props.source) void load(true)
})

async function confirm() {
  const source = props.source
  const digest = page.value?.digest
  if (!source || !digest || page.value?.has_more || submitting.value) return
  submitting.value = true
  try {
    await confirmLocalDeletionReview(source.id, digest)
    MessagePlugin.success(t('storageSpaces.reviewQueued'))
    emit('update:visible', false)
    emit('completed')
  } catch (e: any) {
    error.value = e?.message || t('storageSpaces.reviewChanged')
  } finally { submitting.value = false }
}
function updateVisibility(value: boolean) {
  if (!submitting.value) emit('update:visible', value)
}
</script>

<template>
  <t-dialog :visible="visible" width="640px" :confirm-btn="confirmButton" :close-on-overlay-click="!submitting" :close-on-esc-keydown="!submitting" @update:visible="updateVisibility" @confirm="confirm">
    <template #header>{{ t('storageSpaces.deletionReviewTitle') }} · {{ source?.name }}</template>
    <p>{{ t('storageSpaces.deletionReviewHint') }}</p>
    <t-alert v-if="error" theme="warning" :message="error" />
    <t-loading :loading="busy">
      <template v-if="page">
        <strong>{{ t('storageSpaces.deletionReviewCount', { count: page.count }) }}</strong>
        <ul class="review-paths"><li v-for="path in paths" :key="path">{{ path }}</li></ul>
        <t-button v-if="page.has_more" variant="outline" :loading="busy" @click="load(false)">{{ t('storageSpaces.reviewLoadMore') }}</t-button>
      </template>
    </t-loading>
    <t-button v-if="error" variant="text" :loading="busy" @click="load(true)">{{ t('sourceRoots.refresh') }}</t-button>
  </t-dialog>
</template>

<style scoped>
.review-paths{max-height:320px;overflow:auto;border:1px solid var(--td-component-border);border-radius:6px;padding:12px 12px 12px 32px}.review-paths li{overflow-wrap:anywhere;padding:3px 0}p{line-height:1.6;color:var(--td-text-color-secondary)}
</style>
