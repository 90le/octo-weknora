<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { browseLocalSpace, probeLocalRoot, createLocalRoot, updateLocalRoot, deleteLocalRoot, type LocalRoot, type LocalSpace } from '@/api/local-source-roots'
import DirectoryPicker from '@/views/storage-spaces/DirectoryPicker.vue'
import { canGrantDirectory, canRemoveDirectoryGrant, storageErrorMessage } from '@/views/storage-spaces/storageSpaceDisplay'

const props = defineProps<{ space: LocalSpace; roots: LocalRoot[]; loading?: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const auth = useAuthStore()
const { t } = useI18n()
const editing = ref(false)
const editingID = ref('')
const name = ref('')
const directory = ref('')
const busy = ref(false)
const error = ref('')
const probeOK = ref(false)
const contextKey = computed(() => `${auth.effectiveTenantId}:${props.space.id}`)
let revision = 0
watch(contextKey, () => { ++revision; editing.value = false; busy.value = false; error.value = ''; probeOK.value = false }, {immediate: true})
watch([name, directory], () => { probeOK.value = false })
function start(root?: LocalRoot) {
  editingID.value = root?.id || ''
  name.value = root?.name || ''
  directory.value = root?.directory || ''
  error.value = ''
  probeOK.value = false
  editing.value = true
}
const loadDirectories = (path: string, offset: number) => browseLocalSpace(props.space.id, path, offset)
const registration = () => ({name: name.value.trim(), space_id: props.space.id, directory: directory.value})
const problem = (e: any) => storageErrorMessage(e, t)
async function probe() {
  const current = revision
  busy.value = true; error.value = ''
  try { const result = await probeLocalRoot(registration()); if (current === revision) probeOK.value = result.readable }
  catch (e) { if (current === revision) error.value = problem(e) }
  finally { if (current === revision) busy.value = false }
}
async function save() {
  if (!name.value.trim()) { error.value = t('storageSpaces.required'); return }
  const current = revision
  busy.value = true; error.value = ''
  try {
    if (editingID.value) {
      const old = props.roots.find(item => item.id === editingID.value)
      if (!old) throw new Error(t('sourceRoots.unavailableSelection'))
      await updateLocalRoot(old.id, {name: name.value.trim(), enabled: old.enabled})
    } else await createLocalRoot(registration())
    if (current !== revision) return
    MessagePlugin.success(t(editingID.value ? 'sourceRoots.saved' : 'storageSpaces.grantSaved'))
    editing.value = false
    emit('changed')
  } catch (e) { if (current === revision) error.value = problem(e) }
  finally { if (current === revision) busy.value = false }
}
async function mutate(root: LocalRoot, action: 'toggle' | 'remove', current: number) {
  if (current !== revision || busy.value) return
  busy.value = true; error.value = ''
  try {
    if (action === 'remove') await deleteLocalRoot(root.id)
    else await updateLocalRoot(root.id, {name: root.name, enabled: !root.enabled})
    if (current !== revision) return
    emit('changed')
    if (action === 'remove') MessagePlugin.success(t('sourceRoots.removed'))
  } catch (e) { if (current === revision) error.value = problem(e) }
  finally { if (current === revision) busy.value = false }
}
function confirm(root: LocalRoot, action: 'toggle' | 'remove') {
  const current = revision
  if (action === 'toggle' && !root.enabled) { void mutate(root, action, current); return }
  const dialog = DialogPlugin.confirm({
    header: t(action === 'remove' ? 'sourceRoots.removeTitle' : 'sourceRoots.disableTitle'),
    body: action === 'remove' ? t('storageSpaces.deleteImpact') : root.usage_complete
      ? t('storageSpaces.disableImpact', {count: root.data_source_count ?? 0}) : t('sourceRoots.disableBody', {name: root.name}),
    onConfirm: () => { dialog.destroy(); void mutate(root, action, current) },
    onCancel: () => dialog.destroy(),
  })
}
</script>
<template>
  <section v-if="auth.isSystemAdmin" class="grants">
    <div class="grants-heading"><div><h2>{{ t('storageSpaces.authorizedTitle') }}</h2><p>{{ t('storageSpaces.authorizationHint') }}</p></div>
      <t-button :disabled="busy || loading || !canGrantDirectory(space)" @click="start()">{{ t('storageSpaces.addDirectory') }}</t-button>
    </div>
    <t-alert v-if="error" theme="error" :message="error" />
    <div v-if="editing" class="grant-editor">
      <h3>{{ t(editingID ? 'storageSpaces.editName' : 'storageSpaces.addDirectory') }}</h3>
      <t-form label-align="top" @submit.prevent="save">
        <t-form-item :label="t('storageSpaces.grantName')" required><t-input v-model="name" :disabled="busy" :maxlength="128" :placeholder="t('storageSpaces.grantNameHint')" /></t-form-item>
        <template v-if="!editingID">
          <label class="field-label">{{ t('storageSpaces.chooseDirectory') }}</label>
          <DirectoryPicker v-model="directory" :context-key="contextKey" :root-label="space.name" :load-directories="loadDirectories" :disabled="busy" />
        </template>
        <p class="hint">{{ t('storageSpaces.selectedDirectory', {path: directory || t('storageSpaces.wholeSpace')}) }}</p>
        <p v-if="editingID" class="hint">{{ t('storageSpaces.editHint') }}</p>
        <t-alert v-if="probeOK" theme="success" :message="t('sourceRoots.probeSuccess')" />
        <div class="actions">
          <t-button :loading="busy" @click="save">{{ t(editingID ? 'sourceRoots.save' : 'storageSpaces.grantSave') }}</t-button>
          <t-button v-if="!editingID" variant="outline" :disabled="busy" @click="probe">{{ t('sourceRoots.probe') }}</t-button>
          <t-button variant="text" :disabled="busy" @click="editing = false">{{ t('sourceRoots.cancel') }}</t-button>
        </div>
      </t-form>
    </div>
    <t-loading :loading="loading" style="width:100%">
      <p v-if="!roots.length && !loading" class="empty">{{ t('storageSpaces.noGrant') }}</p>
      <div class="grant-list">
        <article v-for="root in roots" :key="root.id" class="grant-row">
          <div class="grant-content"><div class="grant-title"><strong>{{ root.name }}</strong><t-tag size="small" :theme="root.enabled ? 'success' : 'default'">{{ t(root.enabled ? 'storageSpaces.grantEnabled' : 'storageSpaces.grantDisabled') }}</t-tag></div>
            <p>{{ root.directory || t('storageSpaces.wholeSpace') }}</p>
            <small>{{ root.usage_complete ? t('storageSpaces.sourceCount', {count: root.data_source_count ?? 0}) : t('storageSpaces.usageUnknown') }}</small>
          </div>
          <div class="actions">
            <t-button variant="text" size="small" :disabled="busy" @click="start(root)">{{ t('sourceRoots.rename') }}</t-button>
            <t-button variant="text" size="small" :disabled="busy" @click="confirm(root, 'toggle')">{{ t(root.enabled ? 'sourceRoots.disable' : 'sourceRoots.enable') }}</t-button>
            <t-tooltip :content="root.usage_complete ? t('storageSpaces.usedDelete') : t('storageSpaces.usageUnknown')" :disabled="canRemoveDirectoryGrant(root)"><span><t-button variant="text" size="small" theme="danger" :disabled="busy || !canRemoveDirectoryGrant(root)" @click="confirm(root, 'remove')">{{ t('sourceRoots.remove') }}</t-button></span></t-tooltip>
          </div>
        </article>
      </div>
    </t-loading>
  </section>
</template>
<style scoped>
.grants-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;margin-bottom:20px}.grants-heading h2{font-size:18px;margin:0 0 8px}.grants-heading p,.hint{font-size:13px;color:var(--td-text-color-secondary);line-height:1.7;margin:0 0 12px}
.grant-editor{padding:20px;background:var(--td-bg-color-secondarycontainer);border-radius:8px;margin:20px 0}.grant-editor h3{margin:0 0 16px;font-size:16px}.field-label{display:block;margin-bottom:8px}.grant-editor .hint{margin-top:12px}
.actions{display:flex;align-items:center;gap:8px;flex-wrap:wrap}.grant-list{border-top:1px solid var(--td-component-border)}.grant-row{display:flex;gap:16px;justify-content:space-between;align-items:center;padding:18px 0;border-bottom:1px solid var(--td-component-border)}
.grant-content{min-width:0}.grant-title{display:flex;gap:8px;align-items:center;flex-wrap:wrap}.grant-title strong{overflow-wrap:anywhere}.grant-content p{font-size:13px;margin:8px 0;color:var(--td-text-color-secondary);overflow-wrap:anywhere}.grant-content small,.empty{color:var(--td-text-color-secondary)}
@media(max-width:760px){.grants-heading,.grant-row{flex-direction:column}.grant-editor{padding:14px}}
</style>
