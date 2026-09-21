<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { DirectoryEntry, DirectoryPage } from '@/api/local-source-roots'
import { ancestorDirectories, immediateDirectories, nextDirectoryOffset } from './directoryTree'
import { storageErrorMessage } from './storageSpaceDisplay'

const props = defineProps<{ contextKey: string; rootLabel: string; disabled?: boolean; loadDirectories: (directory: string, offset: number) => Promise<DirectoryPage> }>()
const value = defineModel<string>({ required: true })
const { t } = useI18n()
interface Branch { entries: DirectoryEntry[]; expanded: boolean; busy: boolean; loaded: boolean; error: string; next: number | null; truncated: boolean }
// Folder names are external data; names such as __proto__ must remain ordinary keys.
const branches = ref<Record<string, Branch>>(Object.create(null))
let generation = 0
function branch(path: string): Branch {
  return branches.value[path] ||= { entries: [], expanded: false, busy: false, loaded: false, error: '', next: null, truncated: false }
}
async function load(path: string, offset = 0) {
  const state = branch(path)
  if (state.busy || props.disabled) return
  const current = generation
  state.busy = true
  state.error = ''
  try {
    const page = await props.loadDirectories(path, offset)
    if (generation !== current) return
    if (page.directory !== path) throw new Error(t('storageSpaces.treeMismatch'))
    state.entries = immediateDirectories(path, [...(offset ? state.entries : []), ...page.entries])
    state.next = nextDirectoryOffset(page, offset)
    state.truncated = page.truncated
    state.loaded = true
  } catch (error: any) { if (generation === current) state.error = storageErrorMessage(error, t) }
  finally { if (generation === current) state.busy = false }
}
async function toggle(path: string) {
  const state = branch(path)
  state.expanded = !state.expanded
  if (state.expanded && !state.loaded) await load(path)
}
const rows = computed(() => {
  const result: {path: string; label: string; depth: number; kind: 'directory' | 'status'; state: Branch}[] = []
  function walk(path: string, label: string, depth: number) {
    const state = branch(path)
    result.push({path, label, depth, kind: 'directory', state})
    if (!state.expanded) return
    for (const entry of state.entries) walk(entry.directory, entry.name, depth + 1)
    result.push({path, label, depth: depth + 1, kind: 'status', state})
  }
  walk('', props.rootLabel, 0)
  return result
})
watch(() => props.contextKey, async () => {
  ++generation
  const current = generation
  branches.value = Object.create(null)
  for (const path of ancestorDirectories(value.value)) {
    if (generation !== current) return
    branch(path).expanded = true
    await load(path)
  }
}, {immediate: true})
</script>
<template>
  <div class="directory-picker" :aria-label="t('storageSpaces.directoryTree')">
    <template v-for="row in rows" :key="row.kind + ':' + row.path">
      <div v-if="row.kind === 'directory'" class="directory-row" :class="{ selected: value === row.path }" :style="{'padding-inline-start': `${12 + row.depth * 20}px`}">
        <button type="button" class="tree-expand" :aria-expanded="row.state.expanded" :aria-label="t(row.state.expanded ? 'storageSpaces.collapse' : 'storageSpaces.expand', {name: row.label})" :disabled="disabled" @click="toggle(row.path)">
          <t-icon :name="row.state.expanded ? 'chevron-down' : 'chevron-right'" />
        </button>
        <button type="button" class="tree-select" :aria-pressed="value === row.path" :disabled="disabled" @click="value = row.path">
          <t-icon name="folder" /><span>{{ row.label }}</span><t-icon v-if="value === row.path" name="check" class="selected-mark" />
        </button>
      </div>
      <div v-else class="tree-status" :style="{'padding-inline-start': `${12 + row.depth * 20}px`}">
        <span v-if="row.state.busy" role="status">{{ t('storageSpaces.loading') }}</span>
        <span v-else-if="row.state.error" role="alert">{{ row.state.error }} <t-button size="small" variant="text" @click="load(row.path)">{{ t('sourceRoots.refresh') }}</t-button></span>
        <template v-else>
          <span v-if="row.state.loaded && !row.state.entries.length">{{ t('storageSpaces.noSubdirectories') }}</span>
          <t-button v-if="row.state.next !== null" size="small" variant="text" :disabled="disabled" @click="load(row.path, row.state.next!)">{{ t('storageSpaces.moreDirectories') }}</t-button>
          <span v-else-if="row.state.truncated">{{ t('storageSpaces.treeTruncated') }}</span>
        </template>
      </div>
    </template>
  </div>
</template>
<style scoped>
.directory-picker{border:1px solid var(--td-component-border);border-radius:8px;max-height:340px;overflow:auto;background:var(--td-bg-color-container)}
.directory-row{display:flex;gap:4px;align-items:center;min-height:38px;padding-right:12px}.directory-row.selected{background:var(--td-brand-color-light)}
.tree-expand,.tree-select{border:0;background:transparent;color:inherit;font:inherit;display:flex;align-items:center;cursor:pointer;min-height:36px}
.tree-expand{flex:0 0 28px;justify-content:center}.tree-select{gap:8px;flex:1;text-align:start;min-width:0}.tree-select span{overflow-wrap:anywhere}.selected-mark{color:var(--td-brand-color);margin-left:auto}
button:focus-visible{outline:2px solid var(--td-brand-color);outline-offset:-2px}button:disabled{cursor:not-allowed;color:var(--td-text-color-disabled)}
.tree-status{font-size:12px;color:var(--td-text-color-secondary);line-height:1.7;padding-right:12px}.tree-status:empty{display:none}
</style>
