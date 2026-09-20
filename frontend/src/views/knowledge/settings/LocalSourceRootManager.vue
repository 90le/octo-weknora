<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { listManagedLocalRoots, listLocalSpaces, probeLocalRoot, createLocalRoot, updateLocalRoot, deleteLocalRoot, type LocalRoot, type LocalSpace } from '@/api/local-source-roots'

const emit = defineEmits<{ changed: [] }>()
const auth = useAuthStore()
const { t } = useI18n()
const roots = ref<LocalRoot[]>([])
const spaces = ref<LocalSpace[]>([])
const busy = ref(false)
const error = ref('')
const editing = ref(false)
const editingID = ref('')
const name = ref('')
const spaceID = ref('')
const directory = ref('')
const probeOK = ref(false)
let revision = 0
const selectedSpace = computed(() => spaces.value.find(item => item.id === spaceID.value))
const problem = (e: any) => e?.message || e?.error || t('sourceRoots.failed')
const registration = () => ({ name: name.value.trim(), space_id: spaceID.value, directory: directory.value.trim() })
watch([name, spaceID, directory], () => { probeOK.value = false })
async function load() {
  const version = ++revision
  error.value = ''
  busy.value = true
  try {
    const result = await Promise.all([listManagedLocalRoots(), listLocalSpaces()])
    if (version !== revision) return
    ;[roots.value, spaces.value] = result
  } catch (e: any) { if (version === revision) error.value = problem(e) }
  finally { if (version === revision) busy.value = false }
}
watch(() => auth.effectiveTenantId, () => {
  editing.value = false
  roots.value = []
  spaces.value = []
  void load()
}, { immediate: true })
function start(root?: LocalRoot) {
  editingID.value = root?.id || ''
  name.value = root?.name || ''
  spaceID.value = root?.space_id || spaces.value[0]?.id || ''
  directory.value = root?.directory || ''
  probeOK.value = false
  error.value = ''
  editing.value = true
}
function validate() {
  if (!name.value.trim() || !spaceID.value) { error.value = t('sourceRoots.required'); return false }
  const path = directory.value.trim()
  if (path.startsWith('/') || path.includes('\\') || path.split('/').some(p => p === '..' || p === '.')) {
    error.value = t('sourceRoots.pathInvalid'); return false
  }
  return true
}
async function probe() {
  if (!validate()) return
  const version = revision
  busy.value = true
  error.value = ''
  try { await probeLocalRoot(registration()); if (version === revision) probeOK.value = true }
  catch (e: any) { if (version === revision) error.value = problem(e) }
  finally { if (version === revision) busy.value = false }
}
async function save() {
  if (!validate()) return
  const version = revision
  busy.value = true
  error.value = ''
  try {
    if (editingID.value) {
      const old = roots.value.find(item => item.id === editingID.value)
      if (!old) return
      await updateLocalRoot(old.id, { name: name.value.trim(), enabled: old.enabled })
    } else { await createLocalRoot(registration()) }
    if (version !== revision) return
    editing.value = false
    MessagePlugin.success(t('sourceRoots.saved'))
    emit('changed')
    await load()
  } catch (e: any) { if (version === revision) error.value = problem(e) }
  finally { if (version === revision) busy.value = false }
}
async function toggle(root: LocalRoot) {
  const version = revision
  const run = async () => {
    if (version !== revision) return
    busy.value = true
    error.value = ''
    try {
      await updateLocalRoot(root.id, { name: root.name, enabled: !root.enabled })
      if (version !== revision) return
      emit('changed')
      await load()
    } catch (e: any) { if (version === revision) error.value = problem(e) }
    finally { if (version === revision) busy.value = false }
  }
  if (!root.enabled) { await run(); return }
  const dialog = DialogPlugin.confirm({ header: t('sourceRoots.disableTitle'), body: t('sourceRoots.disableBody', { name: root.name }), onConfirm: () => { dialog.destroy(); void run() }, onCancel: () => dialog.destroy() })
}
function remove(root: LocalRoot) {
  const version = revision
  const dialog = DialogPlugin.confirm({
    header: t('sourceRoots.removeTitle'), body: t('sourceRoots.removeBody', { name: root.name }),
    onConfirm: async () => {
      dialog.destroy()
      if (version !== revision) return
      busy.value = true
      error.value = ''
      try {
        await deleteLocalRoot(root.id)
        if (version !== revision) return
        MessagePlugin.success(t('sourceRoots.removed'))
        emit('changed')
        await load()
      } catch (e: any) { if (version === revision) error.value = problem(e) }
      finally { if (version === revision) busy.value = false }
    }, onCancel: () => dialog.destroy(),
  })
}
const spaceLabel = (id: string) => spaces.value.find(item => item.id === id)?.name || id
</script>

<template>
  <section class="source-roots" v-if="auth.isSystemAdmin">
    <div class="source-roots__heading">
      <div><h3>{{ t('sourceRoots.title') }}</h3><p>{{ auth.currentTenantName }}</p></div>
      <t-button variant="text" :disabled="busy" @click="load">{{ t('sourceRoots.refresh') }}</t-button>
    </div>
    <p class="source-roots__intro">{{ t('sourceRoots.introduction') }}</p>
    <t-alert theme="info" :message="t('sourceRoots.boundary')" />
    <t-alert v-if="error" theme="error" :message="error" class="source-roots__notice" />
    <t-alert v-if="!busy && !spaces.length" theme="warning" :message="t('sourceRoots.emptySpaces')" class="source-roots__notice" />
    <div v-if="editing" class="source-roots__editor">
      <t-form label-align="top" @submit.prevent="save">
        <t-form-item :label="t('sourceRoots.name')" required><t-input v-model="name" :disabled="busy" :maxlength="128" :placeholder="t('sourceRoots.nameHint')" /></t-form-item>
        <t-form-item :label="t('sourceRoots.space')" required>
          <t-select v-model="spaceID" :disabled="!!editingID || busy" :placeholder="t('sourceRoots.chooseSpace')"><t-option v-for="space in spaces" :key="space.id" :value="space.id" :label="space.name" /></t-select>
        </t-form-item>
        <p v-if="selectedSpace" class="source-roots__path">{{ selectedSpace.path }}</p>
        <t-form-item :label="t('sourceRoots.directory')"><t-input v-model="directory" :disabled="!!editingID || busy" :placeholder="t('sourceRoots.directoryHint')" /></t-form-item>
        <p v-if="editingID" class="source-roots__hint">{{ t('sourceRoots.immutable') }}</p>
        <t-alert v-if="probeOK" theme="success" :message="t('sourceRoots.probeSuccess')" />
        <div class="source-roots__actions">
          <t-button theme="primary" :loading="busy" @click="save">{{ t('sourceRoots.save') }}</t-button>
          <t-button variant="outline" :disabled="busy" @click="probe">{{ t('sourceRoots.probe') }}</t-button>
          <t-button variant="text" :disabled="busy" @click="editing = false">{{ t('sourceRoots.cancel') }}</t-button>
        </div>
      </t-form>
    </div>
    <template v-else>
      <div class="source-roots__toolbar"><t-button :disabled="busy || !spaces.length" @click="start()">{{ t('sourceRoots.add') }}</t-button></div>
      <t-loading :loading="busy" style="width: 100%">
        <p v-if="!roots.length && !busy" class="source-roots__hint">{{ t('sourceRoots.emptyRoots') }}</p>
        <article v-for="root in roots" :key="root.id" class="source-roots__item">
          <div class="source-roots__details">
            <strong>{{ root.name }}</strong> <t-tag size="small" :theme="root.enabled ? 'success' : 'default'">{{ t(root.enabled ? 'sourceRoots.enabled' : 'sourceRoots.disabled') }}</t-tag>
            <p>{{ spaceLabel(root.space_id) }} / {{ root.directory || t('sourceRoots.allFiles') }}</p>
          </div>
          <div class="source-roots__actions">
            <t-button size="small" variant="text" :disabled="busy" @click="start(root)">{{ t('sourceRoots.rename') }}</t-button>
            <t-button size="small" variant="text" :disabled="busy" @click="toggle(root)">{{ t(root.enabled ? 'sourceRoots.disable' : 'sourceRoots.enable') }}</t-button>
            <t-button size="small" variant="text" theme="danger" :disabled="busy" @click="remove(root)">{{ t('sourceRoots.remove') }}</t-button>
          </div>
        </article>
      </t-loading>
    </template>
    <p class="source-roots__hint">{{ t('sourceRoots.mountHint') }}</p>
  </section>
</template>
<style scoped>
.source-roots { color: var(--td-text-color-primary); }
.source-roots__heading { display:flex; align-items:center; justify-content:space-between; gap:16px; }
.source-roots h3 { margin:0; font-size:18px; font-weight:600; }
.source-roots__heading p, .source-roots__details p { margin:6px 0 0; font-size:13px; color:var(--td-text-color-secondary); overflow-wrap:anywhere; }
.source-roots__intro { line-height:1.7; color:var(--td-text-color-secondary); }
.source-roots__notice, .source-roots__toolbar, .source-roots__editor { margin-top:16px; }
.source-roots__editor { padding:20px; border:1px solid var(--td-component-border); border-radius:8px; }
.source-roots__path { margin: -12px 0 20px; overflow-wrap:anywhere; color:var(--td-text-color-placeholder); font-size:12px; }
.source-roots__hint { margin:16px 0; font-size:12px; line-height:1.7; color:var(--td-text-color-secondary); }
.source-roots__actions { display:flex; align-items:center; flex-wrap:wrap; gap:8px; }
.source-roots__editor .source-roots__actions { margin-top:20px; }
.source-roots__item { display:flex; align-items:center; justify-content:space-between; flex-wrap:wrap; gap:12px; padding:16px 0; border-bottom:1px solid var(--td-component-border); }
.source-roots__details { flex:1; min-width:180px; }
</style>
