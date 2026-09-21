<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { localSourceRoots } from '@/api/source-snapshot'
import { browseAuthorizedDirectory } from '@/api/local-source-roots'
import DirectoryPicker from '@/views/storage-spaces/DirectoryPicker.vue'
const settings = defineModel<Record<string, any>>({ required: true })
const { t } = useI18n()
const auth = useAuthStore()
const roots = ref<{id:string;name:string}[]>([])
const error = ref('')
const loading = ref(false)
const root = computed({ get: () => settings.value.root_id || '', set: value => { settings.value = { ...settings.value, root_id: value, directory: '' } } })
const directory = computed({ get: () => settings.value.directory || '', set: value => { settings.value = { ...settings.value, directory: value } } })
const selectedRoot = computed(() => roots.value.find(item => item.id === root.value))
const loadDirectories = (path: string, offset: number) => browseAuthorizedDirectory(root.value, path, offset)
const missingRoot = computed(() => !loading.value && root.value && !roots.value.some(item => item.id === root.value))
let version = 0
async function loadRoots() {
  const request = ++version
  loading.value = true
  error.value = ''
  try { const result = await localSourceRoots(); if (request === version) roots.value = result }
  catch (e:any) { if (request === version) error.value = e?.message || t('datasource.source.unavailable') }
  finally { if (request === version) loading.value = false }
}
watch(() => auth.effectiveTenantId, () => { roots.value = []; void loadRoots() }, { immediate: true })
</script>
<template>
  <t-alert v-if="error" theme="warning" :message="error" />
  <t-alert v-else-if="missingRoot" theme="warning" :message="t('sourceRoots.unavailableSelection')" />
  <t-alert v-else-if="!loading && !roots.length" theme="info" :message="t('sourceRoots.emptySelection')" />
  <t-form label-align="top">
    <t-form-item :label="t('storageSpaces.pickSource')" required>
      <t-select v-model="root" :loading="loading" :placeholder="t('storageSpaces.pickSource')">
        <t-option v-for="item in roots" :key="item.id" :value="item.id" :label="item.name || item.id" />
      </t-select>
    </t-form-item>
  </t-form>
  <template v-if="selectedRoot && !loading">
    <label class="directory-label">{{ t('storageSpaces.pickFolder') }}</label>
    <DirectoryPicker v-model="directory" :context-key="`${auth.effectiveTenantId}:${root}`" :root-label="selectedRoot.name" :load-directories="loadDirectories" />
    <p class="source-root-hint">{{ t('storageSpaces.currentSelection', {path: directory || t('storageSpaces.wholeGrant')}) }}</p>
  </template>
  <div class="source-root-tools">
    <router-link v-if="auth.isSystemAdmin" :to="{name:'storageSpaces'}" target="_blank" rel="noopener">{{ t('storageSpaces.manage') }} <t-icon name="jump" /></router-link>
    <t-button variant="text" size="small" :disabled="loading" @click="loadRoots">{{ t('sourceRoots.refresh') }}</t-button>
  </div>
  <p class="source-root-hint">{{ t('storageSpaces.pickHint') }}</p>
  <p v-if="!auth.isSystemAdmin" class="source-root-hint">{{ t('storageSpaces.adminRequired') }}</p>
</template>
<style scoped>
.source-root-tools { display:flex; align-items:center; gap:8px; margin-top:12px; }
.source-root-tools a { display:inline-flex; gap:4px; align-items:center; color:var(--td-brand-color);font-size:13px;text-decoration:none; }
.directory-label { display:block; margin-bottom:8px; }
.source-root-hint { color:var(--td-text-color-secondary); font-size:12px; line-height:1.6; }
</style>
