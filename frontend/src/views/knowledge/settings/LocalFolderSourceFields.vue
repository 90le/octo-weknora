<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { localSourceRoots } from '@/api/source-snapshot'
import LocalSourceRootManager from './LocalSourceRootManager.vue'
const settings = defineModel<Record<string, any>>({ required: true })
const { t } = useI18n()
const auth = useAuthStore()
const roots = ref<{id:string;name:string}[]>([])
const error = ref('')
const loading = ref(false)
const managementVisible = ref(false)
const root = computed({ get: () => settings.value.root_id || '', set: value => { settings.value = { ...settings.value, root_id: value, directory: '' } } })
const directory = computed({ get: () => settings.value.directory || '', set: value => { settings.value = { ...settings.value, directory: value } } })
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
watch(() => auth.effectiveTenantId, () => { roots.value = []; managementVisible.value = false; void loadRoots() }, { immediate: true })
</script>
<template>
  <t-alert v-if="error" theme="warning" :message="error" />
  <t-alert v-else-if="missingRoot" theme="warning" :message="t('sourceRoots.unavailableSelection')" />
  <t-alert v-else-if="!loading && !roots.length" theme="info" :message="t('sourceRoots.emptySelection')" />
  <t-form label-align="top">
    <t-form-item :label="t('datasource.source.root')" required>
      <t-select v-model="root" :loading="loading" :placeholder="t('datasource.source.chooseRoot')">
        <t-option v-for="item in roots" :key="item.id" :value="item.id" :label="item.name || item.id" />
      </t-select>
    </t-form-item>
    <t-form-item :label="t('datasource.source.directory')"><t-input v-model="directory" :placeholder="t('datasource.source.directoryHint')" /></t-form-item>
  </t-form>
  <div class="source-root-tools">
    <t-button v-if="auth.isSystemAdmin" variant="text" size="small" @click="managementVisible = true">{{ t('sourceRoots.manage') }}</t-button>
    <t-button variant="text" size="small" :disabled="loading" @click="loadRoots">{{ t('sourceRoots.refresh') }}</t-button>
  </div>
  <p class="source-root-hint">{{ t('datasource.source.rootHint') }}</p>
  <t-dialog v-if="managementVisible" v-model:visible="managementVisible" :header="false" width="min(760px, 95vw)" :footer="false" attach="body">
    <LocalSourceRootManager @changed="loadRoots" />
  </t-dialog>
</template>
<style scoped>
.source-root-tools { display:flex; gap:8px; }
.source-root-hint { color:var(--td-text-color-secondary); font-size:12px; line-height:1.6; }
</style>
