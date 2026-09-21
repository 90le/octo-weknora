<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import { useAuthStore } from '@/stores/auth'
import { listLocalSpaces, listManagedLocalRoots, discoverLocalSpaces, registerLocalSpace, type LocalSpace, type LocalRoot, type DiscoveredSpaces, type DirectoryEntry } from '@/api/local-source-roots'
import KnowledgeOperationsLayout from '@/views/integrations/KnowledgeOperationsLayout.vue'
import LocalSourceRootManager from '@/views/knowledge/settings/LocalSourceRootManager.vue'
import { isDiscoveredSpaceRegistered, storageErrorMessage } from './storageSpaceDisplay'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()
const { t } = useI18n()
const spaces = ref<LocalSpace[]>([])
const roots = ref<LocalRoot[]>([])
const busy = ref(false)
const error = ref('')
const discoveryVisible = ref(false)
const discovery = ref<DiscoveredSpaces | null>(null)
const discoveryBusy = ref(false)
const discoveryError = ref('')
const registering = ref('')
const registrationName = ref('')
const savingSpace = ref(false)
let generation = 0
let discoveryGeneration = 0
const selectedID = computed(() => typeof route.query.space === 'string' ? route.query.space : '')
const selected = computed(() => spaces.value.find(space => space.id === selectedID.value))
const selectedRoots = computed(() => roots.value.filter(root => root.space_id === selectedID.value))
const statusText = (space: LocalSpace) => t(space.status === 'ready' && space.readable ? 'storageSpaces.ready' : space.status === 'unsafe' ? 'storageSpaces.unsafe' : 'storageSpaces.unavailable')
const registered = (entry: DirectoryEntry) => isDiscoveredSpaceRegistered(entry, spaces.value)
const problem = (e: any) => storageErrorMessage(e, t)
async function load() {
  const current = ++generation
  busy.value = true; error.value = ''
  try {
    const [newSpaces, newRoots] = await Promise.all([listLocalSpaces(), listManagedLocalRoots()])
    if (current !== generation) return
    spaces.value = newSpaces; roots.value = newRoots
    if (selectedID.value && !newSpaces.some(space => space.id === selectedID.value)) await router.replace({name:'storageSpaces'})
  } catch (e) { if (current === generation) error.value = problem(e) }
  finally { if (current === generation) busy.value = false }
}
async function discover() {
  const current = ++discoveryGeneration
  discoveryVisible.value = true; discoveryBusy.value = true; discoveryError.value = ''; discovery.value = null
  try { const result = await discoverLocalSpaces(); if (current === discoveryGeneration) discovery.value = result }
  catch (e) { if (current === discoveryGeneration) discoveryError.value = problem(e) }
  finally { if (current === discoveryGeneration) discoveryBusy.value = false }
}
function startRegister(entry: DirectoryEntry) { registering.value = entry.directory; registrationName.value = entry.name; discoveryError.value = '' }
async function register() {
  if (!registrationName.value.trim() || !registering.value) { discoveryError.value = t('storageSpaces.required'); return }
  const current = discoveryGeneration
  savingSpace.value = true; discoveryError.value = ''
  try {
    const space = await registerLocalSpace({name: registrationName.value.trim(), directory: registering.value})
    if (current !== discoveryGeneration) return
    registering.value = ''
    MessagePlugin.success(t('storageSpaces.registered'))
    await load()
    if (current !== discoveryGeneration) return
    await router.replace({name:'storageSpaces', query:{space:space.id}})
  } catch (e) { if (current === discoveryGeneration) discoveryError.value = problem(e) }
  finally { if (current === discoveryGeneration) savingSpace.value = false }
}
watch(() => auth.effectiveTenantId, () => {
  ++generation; ++discoveryGeneration
  spaces.value = []; roots.value = []; discovery.value = null; discoveryVisible.value = false; registering.value = ''; savingSpace.value = false; discoveryBusy.value = false
  if (auth.isSystemAdmin) void load()
}, {immediate:true})
</script>
<template>
  <KnowledgeOperationsLayout v-if="auth.isSystemAdmin" :title="t('storageSpaces.title')" :description="t('storageSpaces.description')">
    <template #breadcrumb><router-link :to="{name:'knowledgeBaseList'}" class="back-link"><t-icon name="chevron-left" />{{ t('storageSpaces.back') }}</router-link></template>
    <div class="workflow" :aria-label="t('storageSpaces.workflow')">
      <div><strong>{{ t('storageSpaces.stepDiscover') }}</strong><p>{{ t('storageSpaces.stepDiscoverHint') }}</p></div>
      <div><strong>{{ t('storageSpaces.stepAuthorize') }}</strong><p>{{ t('storageSpaces.stepAuthorizeHint') }}</p></div>
      <div><strong>{{ t('storageSpaces.stepSource') }}</strong><p>{{ t('storageSpaces.stepSourceHint') }}</p></div>
    </div>
    <div class="space-toolbar"><div><h2>{{ t('storageSpaces.spaces') }}</h2><p>{{ t('storageSpaces.workspace', {name:auth.currentTenantName}) }}</p></div><div class="actions"><t-button :loading="discoveryBusy" :disabled="savingSpace" @click="discover">{{ t('storageSpaces.discover') }}</t-button><t-button variant="outline" :loading="busy" @click="load">{{ t('sourceRoots.refresh') }}</t-button></div></div>
    <p class="hint">{{ t('storageSpaces.sourceCountHint') }}</p>
    <t-alert v-if="error" theme="error" :message="error" />
    <section v-if="discoveryVisible" class="discovery-panel">
      <div class="section-heading"><h3>{{ t('storageSpaces.discoveredTitle') }}</h3><t-button variant="text" :disabled="savingSpace" @click="discoveryVisible = false">{{ t('storageSpaces.close') }}</t-button></div>
      <p class="hint">{{ t('storageSpaces.discoverHint') }}</p>
      <t-alert v-if="discoveryError" theme="error" :message="discoveryError" />
      <t-loading :loading="discoveryBusy" style="width:100%">
        <t-alert v-if="discovery && !discovery.available" theme="warning" :message="t('storageSpaces.discoverUnavailable')" />
        <p v-else-if="discovery && !discovery.entries.length" class="hint">{{ t('storageSpaces.discoveredEmpty') }}</p>
        <div v-for="entry in discovery?.entries || []" :key="entry.directory" class="discovered-row">
          <div><t-icon name="folder" /> <strong>{{ entry.name }}</strong></div>
          <t-tag v-if="registered(entry)" theme="success" variant="light">{{ t('storageSpaces.alreadyRegistered') }}</t-tag>
          <t-button v-else variant="outline" :disabled="savingSpace" @click="startRegister(entry)">{{ t('storageSpaces.register') }}</t-button>
        </div>
        <p v-if="discovery?.truncated" class="hint">{{ t('storageSpaces.discoveredTruncated') }}</p>
        <div v-if="registering" class="registration-form">
          <label>{{ t('storageSpaces.spaceName') }} · {{ registering }}</label>
          <t-input v-model="registrationName" :maxlength="128" :disabled="savingSpace" />
          <div class="actions"><t-button :loading="savingSpace" @click="register">{{ t('storageSpaces.register') }}</t-button><t-button variant="text" :disabled="savingSpace" @click="registering = ''">{{ t('sourceRoots.cancel') }}</t-button></div>
        </div>
      </t-loading>
    </section>
    <t-loading :loading="busy" style="width:100%">
      <p v-if="!spaces.length && !busy && !error" class="empty">{{ t('storageSpaces.empty') }}</p>
      <div class="space-grid">
        <button v-for="space in spaces" :key="space.id" type="button" class="space-card" :class="{selected:selectedID === space.id}" :aria-pressed="selectedID === space.id" @click="router.replace({name:'storageSpaces', query:{space:space.id}})">
          <div class="space-name"><t-icon name="folder-open" size="22" /><strong>{{ space.name }}</strong><t-icon name="chevron-right" /></div>
          <div class="space-tags"><t-tag size="small" :theme="space.status === 'ready' && space.readable ? 'success' : 'warning'">{{ statusText(space) }}</t-tag><t-tag size="small" variant="light">{{ t('storageSpaces.readOnly') }}</t-tag></div>
          <p>{{ t('storageSpaces.authorizedCount', {count:space.authorized_root_count}) }}</p><small>{{ space.usage_complete ? t('storageSpaces.sourceCount', {count:space.data_source_count}) : t('storageSpaces.usageUnknown') }}</small>
        </button>
      </div>
    </t-loading>
    <section v-if="selected" class="selected-space">
      <div class="selected-heading"><h2>{{ selected.name }}</h2><t-tag :theme="selected.status === 'ready' ? 'success' : 'warning'">{{ statusText(selected) }}</t-tag></div>
      <t-alert v-if="selected.status !== 'ready' || !selected.readable" theme="warning" :message="t('storageSpaces.healthHint')" />
      <LocalSourceRootManager :key="`${auth.effectiveTenantId}:${selected.id}`" :space="selected" :roots="selectedRoots" :loading="busy" @changed="load" />
      <details class="connection-details"><summary>{{ t('storageSpaces.adminDetails') }}</summary><dl><dt>{{ t('storageSpaces.containerPath') }}</dt><dd><code>{{ selected.path }}</code></dd></dl><p class="hint">{{ t('storageSpaces.healthHint') }}</p></details>
    </section>
    <div class="next-step"><p>{{ t('storageSpaces.stepSourceHint') }}</p><router-link :to="{name:'knowledgeBaseList'}">{{ t('storageSpaces.sourceNext') }} <t-icon name="arrow-right" /></router-link></div>
    <details class="mount-guidance"><summary>{{ t('storageSpaces.mountTitle') }}</summary><p>{{ t('storageSpaces.mountBody') }}</p><p class="hint">{{ t('storageSpaces.permissions') }}</p></details>
  </KnowledgeOperationsLayout>
</template>
<style scoped>
.back-link{display:inline-flex;gap:4px;align-items:center;color:var(--td-text-color-secondary);font-size:13px;text-decoration:none;margin-bottom:16px}.workflow{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:20px;padding:20px;background:var(--td-bg-color-secondarycontainer);border-radius:8px;margin-bottom:28px}.workflow strong{font-size:14px}.workflow p,.hint{font-size:13px;line-height:1.7;color:var(--td-text-color-secondary);margin:8px 0}
.space-toolbar,.section-heading,.selected-heading{display:flex;gap:16px;align-items:center;justify-content:space-between}.space-toolbar h2{font-size:18px;margin:0 0 6px}.space-toolbar p{font-size:13px;color:var(--td-text-color-secondary);margin:0}.actions{display:flex;align-items:center;gap:8px;flex-wrap:wrap}
.discovery-panel{padding:20px;border:1px solid var(--td-component-border);border-radius:8px;margin:20px 0}.section-heading h3{margin:0;font-size:16px}.discovered-row{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:12px 0;border-bottom:1px solid var(--td-component-border)}.discovered-row strong{overflow-wrap:anywhere}.registration-form{display:grid;gap:12px;max-width:520px;padding-top:20px}
.space-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(250px,1fr));gap:16px;margin:20px 0 28px}.space-card{border:1px solid var(--td-component-border);border-radius:8px;padding:20px;text-align:start;background:var(--td-bg-color-container);color:var(--td-text-color-primary);font:inherit;cursor:pointer;min-width:0}.space-card:hover{border-color:var(--td-brand-color)}.space-card.selected{border-color:var(--td-brand-color);background:var(--td-brand-color-light)}.space-name{display:flex;gap:10px;align-items:center}.space-name strong{flex:1;overflow-wrap:anywhere}.space-tags{display:flex;gap:8px;flex-wrap:wrap;margin-top:16px}.space-card p{margin:16px 0 6px;font-size:13px}.space-card small,.empty{color:var(--td-text-color-secondary)}.empty{padding:20px 0}
.selected-space{padding:24px;border:1px solid var(--td-component-border);border-radius:8px;margin:24px 0}.selected-heading{justify-content:flex-start;margin-bottom:24px}.selected-heading h2{margin:0;font-size:20px;overflow-wrap:anywhere}.connection-details{margin-top:20px;font-size:13px;color:var(--td-text-color-secondary)}summary{cursor:pointer;line-height:1.8}.connection-details dl{display:flex;gap:12px;flex-wrap:wrap}.connection-details dd{margin:0;overflow-wrap:anywhere}
.next-step{display:flex;align-items:center;justify-content:space-between;gap:20px;padding:16px 0;font-size:14px}.next-step p{color:var(--td-text-color-secondary)}.next-step a{color:var(--td-brand-color);white-space:nowrap;text-decoration:none}.mount-guidance{padding:20px;background:var(--td-bg-color-secondarycontainer);border-radius:8px;margin-top:16px;font-size:14px;line-height:1.8}.mount-guidance p{color:var(--td-text-color-secondary)}button:focus-visible,summary:focus-visible{outline:2px solid var(--td-brand-color);outline-offset:3px}
@media(max-width:900px){.workflow{grid-template-columns:1fr;gap:12px}.space-toolbar{align-items:flex-start;flex-direction:column}.selected-space{padding:16px}.next-step{align-items:flex-start;flex-direction:column;gap:0}}
</style>
