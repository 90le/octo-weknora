<template>
  <div class="artifact-library">
    <div class="header" style="--wails-draggable: drag">
      <div class="header-title" style="--wails-draggable: drag">
        <h2 style="--wails-draggable: drag">
          <ResourceIcon type="artifact" :size="24" />
          {{ $t('artifactLibrary.title') }}
        </h2>
        <p class="header-subtitle" style="--wails-draggable: drag">{{ $t('artifactLibrary.subtitle') }}</p>
      </div>
    </div>

    <div class="usage-summary" role="status">
      <span>{{ $t('artifactLibrary.usageLabel') }}</span>
      <strong>{{ usage ? formatArtifactSize(usage.logical_bytes) : $t('artifactLibrary.usageUnknown') }}</strong>
      <span v-if="usage">{{ $t('artifactLibrary.usageCount', { count: usage.file_count }) }}</span>
      <span class="usage-note">{{ $t('artifactLibrary.usageNote') }}</span>
    </div>

    <div class="toolbar">
      <div class="category-tabs" role="tablist" :aria-label="$t('artifactLibrary.typeFilter')">
        <button
          v-for="c in ARTIFACT_CATEGORIES"
          :key="c"
          type="button"
          role="tab"
          :aria-selected="category === c"
          :disabled="batchRunning"
          @click="setCategory(c)"
        >
          {{ $t(`artifactLibrary.categories.${c}`) }}
        </button>
      </div>
      <label class="channel-filter">
        <span>{{ $t('artifactLibrary.channelFilter') }}</span>
        <select v-model="sourceFilter" :disabled="batchRunning || channelsUnavailable" :aria-label="$t('artifactLibrary.channelFilter')">
          <option value="">{{ $t('artifactLibrary.allChannels') }}</option>
          <option v-if="sourceOptions.some(option => option.source === 'channel')" value="channel">{{ $t('artifactLibrary.allChannelFiles') }}</option>
          <option v-for="option in sourceOptions" :key="sourceOptionKey(option)" :value="sourceOptionKey(option)">{{ option.name || option.platform || $t('artifactLibrary.webSource') }}</option>
        </select>
        <span v-if="channelsUnavailable" role="status">{{ $t('artifactLibrary.channelsUnavailable') }}</span>
      </label>
      <t-input
        :disabled="batchRunning"
        v-model="keyword"
        class="search-input"
        :placeholder="$t('artifactLibrary.searchPlaceholder')"
        :aria-label="$t('artifactLibrary.searchPlaceholder')"
        clearable
      >
        <template #prefix-icon><t-icon name="search" size="16px" /></template>
      </t-input>
    </div>

    <div class="batch-bar" v-if="items.length || batchRunning || batchResult">
      <label><input type="checkbox" :checked="allLoadedSelected" :disabled="batchRunning || !items.length" @change="toggleAllLoaded" /> {{ $t('artifactLibrary.selectLoaded') }}</label>
      <span>{{ $t('artifactLibrary.selectedCount', { count: selected.size }) }}</span>
      <t-button theme="danger" variant="outline" size="small" :disabled="batchRunning || anyDeleting || !selected.size" @click="confirmBatchDelete">{{ $t('artifactLibrary.deleteSelected') }}</t-button>
      <t-button v-if="batchRunning" size="small" variant="outline" @click="cancelBatch = true" :disabled="cancelBatch">{{ $t('artifactLibrary.stopDeleting') }}</t-button>
      <span v-if="batchRunning" role="status">{{ $t('artifactLibrary.batchProgress', { done: batchDone, total: batchTotal }) }}</span>
    </div>
    <div v-if="batchResult" class="batch-result" role="status">
      {{ $t('artifactLibrary.batchResult', batchResult) }}
      <ul v-if="batchFailures.length"><li v-for="failure in batchFailures" :key="failure.key">{{ failure.name }}</li></ul>
    </div>
    <div ref="scrollRoot" class="library-main">
      <div v-if="loading && !items.length" class="artifact-rows">
        <div v-for="n in 6" :key="'skel-' + n" class="artifact-row is-skeleton">
          <t-skeleton animation="gradient" :row-col="[[{ width: '32px', height: '38px', type: 'rect' },
            { width: '40%', height: '14px' }]]" />
        </div>
      </div>

      <EmptyState
        v-else-if="loadError && !items.length"
        icon="error-circle"
        :title="$t('artifactLibrary.loadFailed')"
      >
        <t-button variant="outline" @click="reload">{{ $t('artifactLibrary.retry') }}</t-button>
      </EmptyState>

      <EmptyState
        v-else-if="!items.length && isFiltered"
        icon="search"
        :title="$t('artifactLibrary.noMatches.title')"
        :description="$t('artifactLibrary.noMatches.description')"
      >
        <t-button variant="outline" @click="clearFilters">{{ $t('artifactLibrary.clearFilters') }}</t-button>
      </EmptyState>

      <EmptyState
        v-else-if="!items.length"
        icon="folder-open"
        :title="$t('artifactLibrary.empty.title')"
        :description="$t('artifactLibrary.empty.description')"
      />

      <template v-else>
        <div class="result-count">{{ $t('artifactLibrary.total', { count: total }) }}</div>
        <section v-for="section in sections" :key="section.group" class="date-section">
          <h3 class="date-heading">{{ $t(`artifactLibrary.groups.${section.group}`) }}</h3>
          <ul class="artifact-rows">
            <li
              v-for="item in section.items"
              :key="itemKey(item)"
              class="artifact-row"
              @click="openPreview(item)"
            >
              <input type="checkbox" :checked="selected.has(itemKey(item))" :disabled="batchRunning || isDeleting(item)" :aria-label="$t('artifactLibrary.selectFile', { name: item.file_name })" @click.stop @change="toggleSelected(item)" />
              <button type="button" class="artifact-open" :title="$t('artifactLibrary.preview')">
                <ArtifactFileIcon :file-name="item.file_name" />
                <span class="artifact-body">
                  <span class="artifact-name" :title="item.file_name">{{ item.file_name }}</span>
                  <span class="artifact-meta">
                    <span>{{ formatArtifactSize(item.file_size) }}</span>
                    <span class="meta-sep">·</span>
                    <span>{{ formatArtifactDateTime(item.created_at) }}</span>
                    <template v-if="item.version_count > 1">
                      <span class="meta-sep">·</span>
                      <span class="version-badge">
                        {{ $t('artifactLibrary.versions', { count: item.version_count }) }}
                      </span>
                    </template>
                  </span>
                </span>
              </button>
              <button
                type="button"
                v-if="!item.im_channel_artifact"
                class="session-link"
                :title="$t('artifactLibrary.openSession')"
                @click.stop="openSession(item)"
              >
                <t-icon name="chat" size="14px" />
                <span>{{ item.session_title || $t('artifactLibrary.untitledSession') }}</span>
              </button>
              <button v-if="item.im_channel_artifact" type="button" class="session-link session-link--channel" :title="$t('artifactLibrary.openSourceConversation')" @click.stop="openChannelConversation(item)">
                <t-icon name="chat" size="14px" />
                <span>{{ $t('artifactLibrary.openSourceConversation') }}</span>
              </button>
              <t-button
                class="row-action"
                variant="text"
                shape="square"
                size="small"
                :title="$t('artifactLibrary.download')"
                :aria-label="$t('artifactLibrary.download')"
                :loading="isDownloading(item)"
                @click.stop="handleDownload(item)"
              >
                <template #icon><t-icon name="download" size="16px" /></template>
              </t-button>
              <t-button
                class="row-action row-action--danger"
                variant="text"
                shape="square"
                size="small"
                :title="$t('artifactLibrary.delete')"
                :aria-label="$t('artifactLibrary.delete')"
                :loading="isDeleting(item)"
                :disabled="batchRunning"
                @click.stop="handleDelete(item)"
              >
                <template #icon><t-icon name="delete" size="16px" /></template>
              </t-button>
            </li>
          </ul>
        </section>

        <div ref="sentinel" class="list-footer">
          <t-loading v-if="loading" size="small" />
          <t-button v-else-if="hasMore" variant="text" @click="loadMore">
            {{ $t('artifactLibrary.loadMore') }}
          </t-button>
        </div>
      </template>
    </div>

    <t-drawer
      v-model:visible="previewVisible"
      class="artifact-library-preview"
      placement="right"
      size="min(880px, 92vw)"
      attach="body"
      :footer="false"
      :close-on-overlay-click="true"
      :close-on-esc-keydown="true"
      @closed="previewItem = null"
    >
      <template #header>
        <div v-if="previewItem" class="preview-header">
          <ArtifactFileIcon class="preview-header-icon" :file-name="previewItem.file_name" />
          <div class="preview-header-text">
            <div class="preview-header-title" :title="previewItem.file_name">{{ previewItem.file_name }}</div>
            <button v-if="!previewItem.im_channel_artifact" type="button" class="session-link session-link--inline" @click="openSession(previewItem)">
              <t-icon name="chat" size="12px" />
              <span>{{ previewItem.session_title || $t('artifactLibrary.untitledSession') }}</span>
            </button>
            <button v-else type="button" class="session-link session-link--inline" @click="openChannelConversation(previewItem)">
              <t-icon name="chat" size="12px" />
              <span>{{ $t('artifactLibrary.openSourceConversation') }}</span>
            </button>
          </div>
          <div ref="previewActions" class="preview-actions" />
          <t-button
            class="row-action"
            variant="text"
            shape="square"
            size="small"
            :title="$t('artifactLibrary.download')"
            :aria-label="$t('artifactLibrary.download')"
            :loading="isDownloading(previewItem)"
            @click="handleDownload(previewItem)"
          >
            <template #icon><t-icon name="download" size="16px" /></template>
          </t-button>
          <t-button
            class="row-action row-action--danger"
            variant="text"
            shape="square"
            size="small"
            :title="$t('artifactLibrary.delete')"
            :aria-label="$t('artifactLibrary.delete')"
            :loading="isDeleting(previewItem)"
            :disabled="batchRunning"
            @click="handleDelete(previewItem)"
          >
            <template #icon><t-icon name="delete" size="16px" /></template>
          </t-button>
          <t-button
            class="row-action"
            variant="text"
            shape="square"
            size="small"
            :title="$t('common.close')"
            :aria-label="$t('common.close')"
            @click="previewVisible = false"
          >
            <template #icon><t-icon name="close" size="16px" /></template>
          </t-button>
        </div>
      </template>
      <div v-if="previewItem" class="preview-body">
        <DocumentPreview
          :key="`${previewItem.message_id}:${previewItem.index}`"
          :toolbar-target="previewActions"
          :session-id="previewItem.session_id"
          :message-id="previewItem.message_id"
          :artifact-index="previewItem.index"
          :channel-artifact="!!previewItem.im_channel_artifact"
          :file-type="previewFileType"
          :file-name="previewItem.file_name"
          :active="previewVisible"
          fill-height
        />
      </div>
    </t-drawer>

    <t-drawer
      v-model:visible="conversationVisible"
      placement="right"
      size="min(760px, 92vw)"
      attach="body"
      :header="$t('artifactLibrary.openSourceConversation')"
      :footer="false"
      :close-on-overlay-click="true"
      :close-on-esc-keydown="true"
    >
      <ChannelConversationReadOnly v-if="conversationVisible && conversationItem" :key="itemKey(conversationItem)" :item="conversationItem" />
    </t-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import { downloadArtifact } from '@/api/chat'
import { deleteArtifactLibraryItem, deleteChannelArtifact, downloadChannelArtifact, getArtifactUsage, listArtifactLibrary, type ArtifactSourceOption, type ArtifactLibraryItem, type ArtifactUsageResponse } from '@/api/artifacts'
import {
  ARTIFACT_CATEGORIES,
  artifactCategoryExtensions,
  groupArtifactsByDate,
  parseArtifactCategory,
  type ArtifactCategory,
} from '@/utils/artifactLibrary'
import { parseArtifactUsage } from './artifactUsage'
import { deleteSequentially } from './artifactBatch'
import { formatArtifactDateTime, formatArtifactSize } from '@/utils/sessionArtifacts'
import { resolveFilePreviewExt } from '@/utils/filePreview'
import { useConfirmDelete } from '@/components/settings/useConfirmDelete'
import EmptyState from '@/components/EmptyState.vue'
import ResourceIcon from '@/components/icons/ResourceIcon.vue'
import DocumentPreview from '@/components/document-preview.vue'
import ArtifactFileIcon from '@/views/chat/components/ArtifactFileIcon.vue'
import ChannelConversationReadOnly from './ChannelConversationReadOnly.vue'

const PAGE_SIZE = 30
const SEARCH_DEBOUNCE_MS = 300

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const confirmDelete = useConfirmDelete()

const category = ref<ArtifactCategory>(parseArtifactCategory(route.query.type))
const keyword = ref(typeof route.query.q === 'string' ? route.query.q : '')
const sourceFilter = ref('')
const sourceOptions = ref<ArtifactSourceOption[]>([])
const channelsUnavailable = ref(true)
const selected = ref(new Map<string, ArtifactLibraryItem>())
const batchRunning = ref(false)
const cancelBatch = ref(false)
const batchDone = ref(0)
const batchTotal = ref(0)
const batchResult = ref<{ success: number; failed: number; remaining: number } | null>(null)
const batchFailures = ref<{ key: string; name: string }[]>([])
const items = ref<ArtifactLibraryItem[]>([])
const total = ref(0)
const usage = ref<ArtifactUsageResponse['data'] | null>(null)
let usageTicket = 0
const page = ref(0)
const loading = ref(false)
const loadError = ref(false)
const downloading = reactive<Record<string, boolean>>({})
const deleting = reactive<Record<string, boolean>>({})
const previewItem = ref<ArtifactLibraryItem | null>(null)
const previewVisible = ref(false)
const conversationItem = ref<ArtifactLibraryItem | null>(null)
const conversationVisible = ref(false)
const previewActions = ref<HTMLElement | null>(null)
const scrollRoot = ref<HTMLElement | null>(null)
const sentinel = ref<HTMLElement | null>(null)

const anyDeleting = computed(() => Object.values(deleting).some(Boolean))
const sections = computed(() => groupArtifactsByDate(items.value))
const hasMore = computed(() => items.value.length < total.value)
const isFiltered = computed(() => category.value !== 'all' || keyword.value.trim() !== '' || !!sourceFilter.value)
const allLoadedSelected = computed(() => items.value.length > 0 && items.value.every(item => selected.value.has(itemKey(item))))
const previewFileType = computed(() => {
  const item = previewItem.value
  return item ? resolveFilePreviewExt(item.file_name, item.file_type) : ''
})

async function refreshUsage() {
  const mine = ++usageTicket
  usage.value = null
  try {
    const res = await getArtifactUsage()
    if (mine !== usageTicket) return
    usage.value = parseArtifactUsage(res)
  } catch (err) {
    if (mine === usageTicket) console.error('[ArtifactLibrary] usage failed:', err)
  }
}

// Every fetch takes a ticket; a response whose ticket is stale (the filters
// changed while it was in flight) is dropped.
let ticket = 0

async function fetchPage(nextPage: number) {
  const mine = ++ticket
  loading.value = true
  loadError.value = false
  try {
    const res = await listArtifactLibrary({
      keyword: keyword.value,
      source: sourceFilter.value === 'web' ? 'web' : sourceFilter.value ? 'channel' : undefined,
      channelId: sourceFilter.value.startsWith('channel:') ? sourceFilter.value.slice(8) : undefined,
      fileTypes: artifactCategoryExtensions(category.value),
      page: nextPage,
      pageSize: PAGE_SIZE,
    })
    if (mine !== ticket) return
    if (res?.success !== true) throw new Error('Artifact list failed')
    const expectedSource = sourceFilter.value === 'web' ? 'web' : sourceFilter.value ? 'channel' : ''
    const expectedChannel = sourceFilter.value.startsWith('channel:') ? sourceFilter.value.slice(8) : ''
    if (expectedSource && (res.source !== expectedSource || (expectedChannel && res.channel_id !== expectedChannel))) throw new Error('Artifact filter not acknowledged')
    if (Array.isArray(res.source_options)) {
      sourceOptions.value = res.source_options.filter(option => option.source === 'web' || (option.source === 'channel' && !!option.channel_id))
      channelsUnavailable.value = false
    }
    const rows = Array.isArray(res?.data) ? res.data : []
    // Deleting a row shifts every later row one page-offset earlier, so an
    // appended page can overlap what is already shown. Drop the repeats rather
    // than rendering an item twice under the same key.
    if (nextPage === 1) {
      items.value = rows
    } else {
      const shown = new Set(items.value.map(itemKey))
      items.value = [...items.value, ...rows.filter((row) => !shown.has(itemKey(row)))]
    }
    total.value = typeof res?.total === 'number' ? res.total : items.value.length
    page.value = nextPage
  } catch (err) {
    if (mine !== ticket) return
    console.error('[ArtifactLibrary] load failed:', err)
    loadError.value = true
    if (nextPage > 1) MessagePlugin.error(t('artifactLibrary.loadFailed'))
  } finally {
    if (mine === ticket) loading.value = false
  }
}

function reload() {
  items.value = []
  total.value = 0
  page.value = 0
  void fetchPage(1)
}

function loadMore() {
  if (batchRunning.value || loading.value || !hasMore.value) return
  // Page from how many rows are actually shown, not from the last page number:
  // after a delete those differ, and paging by number would skip a row.
  void fetchPage(Math.floor(items.value.length / PAGE_SIZE) + 1)
}

function syncQuery() {
  const query = { ...route.query }
  if (category.value === 'all') delete query.type
  else query.type = category.value
  const q = keyword.value.trim()
  if (q) query.q = q
  else delete query.q
  void router.replace({ query })
}

function setCategory(next: ArtifactCategory) {
  if (category.value === next) return
  category.value = next
}

function clearFilters() {
  category.value = 'all'
  keyword.value = ''
  sourceFilter.value = ''
}

let searchTimer: ReturnType<typeof setTimeout> | null = null
watch(keyword, () => {
  selected.value.clear()
  batchResult.value = null
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    searchTimer = null
    syncQuery()
    reload()
  }, SEARCH_DEBOUNCE_MS)
})
watch([category, sourceFilter], () => {
  selected.value.clear()
  batchResult.value = null
  syncQuery()
  reload()
})

function openPreview(item: ArtifactLibraryItem) {
  previewItem.value = item
  previewVisible.value = true
}

function openChannelConversation(item: ArtifactLibraryItem) {
  if (!item.im_channel_artifact) return
  conversationItem.value = item
  conversationVisible.value = true
}

watch(conversationVisible, visible => {
  if (!visible) conversationItem.value = null
})

function openSession(item: ArtifactLibraryItem) {
  previewVisible.value = false
  void router.push(`/platform/chat/${item.session_id}`)
}

function itemKey(item: ArtifactLibraryItem) {
  return JSON.stringify([!!item.im_channel_artifact, item.session_id, item.message_id, item.index])
}

function isDownloading(item: ArtifactLibraryItem) {
  return !!downloading[itemKey(item)]
}

async function handleDownload(item: ArtifactLibraryItem) {
  const key = itemKey(item)
  if (downloading[key]) return
  downloading[key] = true
  try {
    const blob = item.im_channel_artifact ? await downloadChannelArtifact(item) : await downloadArtifact(item.session_id, item.message_id, item.index)
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = item.file_name || 'artifact'
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch (err) {
    console.error('[ArtifactLibrary] download failed:', err)
    MessagePlugin.error(t('artifactLibrary.downloadFailed'))
  } finally {
    downloading[key] = false
  }
}

function isDeleting(item: ArtifactLibraryItem) {
  return !!deleting[itemKey(item)]
}

// A library row is the file, not one regeneration of it, so the confirmation
// says how many versions go with it. The bytes are reclaimed server side, which
// is why this asks at all.
function handleDelete(item: ArtifactLibraryItem) {
  if (batchRunning.value || isDeleting(item)) return
  confirmDelete({
    title: t('artifactLibrary.deleteTitle'),
    body:
      item.im_channel_artifact
        ? t('artifactLibrary.deleteChannelConfirm', { name: item.file_name, count: item.version_count })
        : item.version_count > 1
        ? t('artifactLibrary.deleteConfirmVersions', { name: item.file_name, count: item.version_count })
        : t('artifactLibrary.deleteConfirm', { name: item.file_name }),
    onConfirm: () => performDelete(item),
  })
}

async function performDelete(item: ArtifactLibraryItem) {
  const key = itemKey(item)
  if (batchRunning.value || deleting[key]) return
  deleting[key] = true
  try {
    const result = item.im_channel_artifact ? await deleteChannelArtifact(item) : await deleteArtifactLibraryItem(item)
    if (!result?.success) throw new Error('Artifact deletion was not confirmed')
    void refreshUsage()
    // A concurrent list response can still contain the deleted row.
    if (loading.value) { ticket++; loading.value = false }
    // Splice the row out instead of reloading: this is an infinite list, and a
    // reload would throw away everything the user has scrolled past.
    const at = items.value.findIndex((row) => itemKey(row) === key)
    if (at >= 0) {
      items.value.splice(at, 1)
      total.value = Math.max(0, total.value - 1)
    }
    selected.value.delete(key)
    if (!items.value.length && total.value > 0) reload()
    if (previewItem.value && itemKey(previewItem.value) === key) previewVisible.value = false
    if (conversationItem.value && itemKey(conversationItem.value) === key) conversationVisible.value = false
    MessagePlugin.success(t('artifactLibrary.deleted'))
  } catch (err) {
    console.error('[ArtifactLibrary] delete failed:', err)
    MessagePlugin.error(t('artifactLibrary.deleteFailed'))
  } finally {
    deleting[key] = false
  }
}

function toggleSelected(item: ArtifactLibraryItem) {
  const key = itemKey(item)
  if (selected.value.has(key)) selected.value.delete(key)
  else selected.value.set(key, item)
}

function toggleAllLoaded() {
  if (allLoadedSelected.value) items.value.forEach(item => selected.value.delete(itemKey(item)))
  else items.value.forEach(item => selected.value.set(itemKey(item), item))
}

function confirmBatchDelete() {
  if (batchRunning.value || anyDeleting.value || !selected.value.size) return
  const snapshot = [...selected.value.values()]
  confirmDelete({
    title: t('artifactLibrary.batchDeleteTitle'),
    body: t('artifactLibrary.batchDeleteConfirm', { count: snapshot.length }),
    onConfirm: () => { void performBatchDelete(snapshot) },
  })
}

async function performBatchDelete(snapshot: ArtifactLibraryItem[]) {
  if (batchRunning.value) return
  if (searchTimer) { clearTimeout(searchTimer); searchTimer = null }
  batchRunning.value = true
  cancelBatch.value = false
  batchDone.value = 0
  batchTotal.value = snapshot.length
  batchResult.value = null
  batchFailures.value = []
  // Discard any in-flight list request before rows start moving.
  ticket++
  loading.value = false
  let success = 0
  const { remaining } = await deleteSequentially(snapshot, () => cancelBatch.value, async (item) => {
    const result = item.im_channel_artifact ? await deleteChannelArtifact(item) : await deleteArtifactLibraryItem(item)
    if (!result?.success) throw new Error('Artifact deletion was not confirmed')
  }, (item, error) => {
    const key = itemKey(item)
    if (error) {
      console.error('[ArtifactLibrary] batch delete failed:', error)
      batchFailures.value.push({ key, name: item.file_name })
    } else {
      success++
      selected.value.delete(key)
      items.value = items.value.filter(row => itemKey(row) !== key)
      total.value = Math.max(0, total.value - 1)
      if (previewItem.value && itemKey(previewItem.value) === key) previewVisible.value = false
      if (conversationItem.value && itemKey(conversationItem.value) === key) conversationVisible.value = false
    }
    batchDone.value++
  })
  batchResult.value = { success, failed: batchFailures.value.length, remaining }
  batchRunning.value = false
  if (success) {
    void refreshUsage()
    reload()
  }
}

function sourceOptionKey(option: ArtifactSourceOption) {
  return option.source === 'web' ? 'web' : `channel:${option.channel_id}`
}

// Load the next page as the footer scrolls into view; the footer button
// covers browsers without IntersectionObserver and failed auto-loads.
let observer: IntersectionObserver | null = null
watch(sentinel, (el, prev) => {
  if (prev) observer?.unobserve(prev)
  if (el) observer?.observe(el)
})

onMounted(() => {
  if (typeof IntersectionObserver !== 'undefined') {
    observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting) && !loadError.value) loadMore()
      },
      { root: scrollRoot.value, rootMargin: '200px' },
    )
    if (sentinel.value) observer.observe(sentinel.value)
  }
  void fetchPage(1)
  void refreshUsage()
})

onBeforeUnmount(() => {
  cancelBatch.value = true
  observer?.disconnect()
  if (searchTimer) clearTimeout(searchTimer)
  ticket++
  usageTicket++
})
</script>

<style scoped lang="less">
@import '@/components/css/artifact-filter-tabs.less';
@import (reference) '@/components/css/resource-card.less';

.artifact-library { .resource-list-content(); }

.header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
  flex-shrink: 0;

  .header-title {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    color: var(--td-text-color-primary);
    font-family: var(--app-font-family);
    font-size: var(--app-text-4xl);
    font-weight: 600;
    line-height: 32px;
  }
}

.header-subtitle {
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-base);
  line-height: 20px;
}

.usage-summary {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 4px 10px;
  padding: 10px 0 14px;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-base);

  strong { color: var(--td-text-color-primary); }
  .usage-note { flex-basis: 100%; }
}

.toolbar {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--app-space-3);
  padding-bottom: var(--app-space-3);
  border-bottom: 1px solid var(--td-component-stroke);
}

// 与右侧 32px 的搜索框同高：3px 内边距 × 2 + 26px 按钮
.category-tabs {
  .artifact-filter-tabs();
  button { display: inline-flex; align-items: center; height: 26px; padding: 0 12px; }
}

.search-input {
  width: 260px;
  max-width: 100%;
  :deep(.t-input) { border-radius: var(--app-radius-sm); }
}

 .channel-filter, .batch-bar { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; font-size: var(--app-text-sm); }
.channel-filter select { max-width: 200px; padding: 5px; color: var(--td-text-color-primary); background: var(--td-bg-color-container); border: 1px solid var(--td-component-stroke); border-radius: var(--app-radius-sm); }
.batch-bar { padding: 10px 0; }
.batch-result { padding: 8px; color: var(--td-text-color-secondary); }
.batch-result ul { margin: 4px 0; }

.library-main {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
  display: flex;
  flex-direction: column;
  padding: var(--app-space-3) 0 var(--app-space-6);
}

.result-count {
  color: var(--td-text-color-placeholder);
  font-size: var(--app-text-sm);
  padding: 0 var(--app-space-2) var(--app-space-1);
}

.date-section + .date-section {
  margin-top: var(--app-space-3);
}

.date-heading {
  margin: 0;
  padding: var(--app-space-2) var(--app-space-2) var(--app-space-1);
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
  font-weight: 500;
}

.artifact-rows {
  margin: 0;
  padding: 0;
  list-style: none;
}

.artifact-row {
  display: flex;
  align-items: center;
  gap: var(--app-space-3);
  padding: 10px var(--app-space-2);
  border-radius: var(--app-radius-md);
  cursor: pointer;
  transition: background var(--app-motion-fast) ease;

  &:hover {
    background: var(--td-bg-color-container-hover);
  }

  &.is-skeleton {
    cursor: default;

    &:hover {
      background: transparent;
    }
  }
}

.artifact-open {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: var(--app-space-3);
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;

  &:focus-visible {
    outline: 2px solid var(--td-text-color-secondary);
    outline-offset: 4px;
    border-radius: var(--app-radius-xs);
  }
}

.artifact-body {
  flex: 1;
  min-width: 0;
}

.artifact-name {
  display: block;
  color: var(--td-text-color-primary);
  font-size: var(--app-text-base);
  font-weight: 500;
  line-height: 1.4;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.artifact-meta {
  margin-top: 2px;
  display: flex;
  align-items: center;
  gap: var(--app-space-1);
  color: var(--td-text-color-placeholder);
  font-size: var(--app-text-sm);
  line-height: 1.3;
  white-space: nowrap;
}

.meta-sep {
  opacity: 0.6;
}

.version-badge {
  color: var(--td-text-color-secondary);
}

.session-link {
  flex: 0 1 240px;
  min-width: 0;
  display: inline-flex;
  align-items: center;
  gap: var(--app-space-1);
  padding: 4px 8px;
  border: 0;
  border-radius: var(--app-radius-sm);
  background: transparent;
  color: var(--td-text-color-secondary);
  font: inherit;
  font-size: var(--app-text-sm);
  cursor: pointer;
  transition: background var(--app-motion-fast) ease, color var(--app-motion-fast) ease;

  span {
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }

  .t-icon {
    flex-shrink: 0;
  }

  &:hover {
    background: var(--td-bg-color-secondarycontainer);
    color: var(--td-brand-color);
  }

  &:focus-visible {
    outline: 2px solid var(--td-brand-color);
    outline-offset: 2px;
  }

  &--inline {
    flex: none;
    max-width: 100%;
    padding: 0;
    margin-top: 2px;

    &:hover {
      background: transparent;
    }
  }
}

.row-action.t-button {
  flex-shrink: 0;
  width: 30px;
  height: 30px;
  border-radius: var(--app-radius-sm);
  color: var(--td-text-color-secondary);

  &:not(.t-is-disabled):not(.t-is-loading):hover {
    background: color-mix(in srgb, var(--td-text-color-primary) 10%, var(--td-bg-color-container));
    color: var(--td-text-color-primary);
  }

  :deep(.t-button__icon) {
    margin: 0;
  }

  &--danger:not(.t-is-disabled):not(.t-is-loading):hover {
    background: var(--td-error-color-1);
    color: var(--td-error-color);
  }
}

.list-footer {
  display: flex;
  justify-content: center;
  min-height: 40px;
  padding-top: var(--app-space-3);
}

@media (max-width: 720px) {
  .artifact-library {
    margin: 0;
    padding: var(--app-space-4) var(--app-space-4) 0;
  }

  .search-input {
    width: 100%;
  }

  .session-link:not(.session-link--inline):not(.session-link--channel) {
    display: none;
  }
}
</style>

<style lang="less">
.artifact-library-preview {
  .t-drawer__header {
    padding: 10px 16px;
  }

  .t-drawer__body {
    padding: 8px;
    display: flex;
    flex-direction: column;
  }

  .preview-header {
    display: flex;
    align-items: center;
    gap: var(--app-space-2);
    width: 100%;
    min-width: 0;
  }

  .preview-header-icon {
    width: 26px;
    height: 32px;
  }

  .preview-header-text {
    flex: 1;
    min-width: 0;
  }

  .preview-header-title {
    color: var(--td-text-color-primary);
    font-size: var(--app-text-md);
    font-weight: 600;
    line-height: 1.4;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }

  .preview-actions {
    flex-shrink: 0;
  }

  .preview-body {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
}
</style>
