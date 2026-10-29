<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { browseManagedMountRoot, createManagedMount, deleteManagedMount, listManagedMountRoots, listManagedMounts, registerLocalSpace, type LocalSpace, type ManagedMount, type ManagedMountRoot } from '@/api/local-source-roots'
import DirectoryPicker from './DirectoryPicker.vue'

const props = defineProps<{ visible: boolean; spaces: LocalSpace[] }>()
const emit = defineEmits<{ (event: 'update:visible', value: boolean): void; (event: 'created', value: LocalSpace): void }>()
const { t } = useI18n()
const roots = ref<ManagedMountRoot[]>([])
const mounts = ref<ManagedMount[]>([])
const root = ref('')
const directory = ref('')
const name = ref('')
const busy = ref(false)
const submitting = ref(false)
const error = ref('')
let generation = 0
const browse = (dir: string, offset: number) => browseManagedMountRoot(root.value, dir, offset)
const registered = (mount: ManagedMount) => props.spaces.some(space => space.path.replace(/\/$/, '') === `/source-roots/managed/${mount.id}`)
const mountInUse = (mount: ManagedMount) => props.spaces.some(space => {
  const existing = space.path.replace(/\/$/, '')
  const target = `/source-roots/managed/${mount.id}`
  return existing === target || existing.startsWith(`${target}/`) || target.startsWith(`${existing}/`)
})

async function load() {
  const current = ++generation
  busy.value = true; error.value = ''
  try {
    const [available, existing] = await Promise.all([listManagedMountRoots(), listManagedMounts()])
    if (current !== generation) return
    roots.value = available; mounts.value = existing || []
    if (!available.some(item => item.id === root.value)) root.value = available[0]?.id || ''
  } catch { if (current === generation) error.value = t('storageSpaces.hostMountUnavailable') }
  finally { if (current === generation) busy.value = false }
}
watch(() => props.visible, visible => {
  if (visible) { directory.value = ''; name.value = ''; void load() }
  else { ++generation; busy.value = false; error.value = '' }
})
watch(root, () => { directory.value = '' })

async function register(mount: ManagedMount, displayName: string) {
  const space = await registerLocalSpace({name: displayName, directory: `managed/${mount.id}`})
  MessagePlugin.success(t('storageSpaces.hostMountRegistered'))
  emit('created', space)
  emit('update:visible', false)
}
async function create() {
  if (!root.value || !name.value.trim() || submitting.value) return
  submitting.value = true; error.value = ''
  try {
    const mount = await createManagedMount(root.value, directory.value)
    // If registration fails, the broker keeps the mount and it remains
    // visible below for a safe retry instead of being silently removed.
    await register(mount, name.value.trim())
  } catch (e: any) {
    error.value = e?.message || t('storageSpaces.hostMountFailed')
    await load()
    error.value ||= t('storageSpaces.hostMountFailed')
  } finally { submitting.value = false }
}
async function registerExisting(mount: ManagedMount) {
  if (submitting.value || !name.value.trim()) { error.value = t('storageSpaces.hostMountNameRequired'); return }
  submitting.value = true; error.value = ''
  try { await register(mount, name.value.trim()) }
  catch (e: any) { error.value = e?.message || t('storageSpaces.hostMountFailed') }
  finally { submitting.value = false }
}
function removeMount(mount: ManagedMount) {
  const dialog = DialogPlugin.confirm({
    header: t('storageSpaces.hostMountRemove'), body: t('storageSpaces.hostMountRemoveHint'), theme: 'danger',
    onConfirm: async () => {
      submitting.value = true
      try { await deleteManagedMount(mount.id); dialog.hide(); await load(); MessagePlugin.success(t('storageSpaces.hostMountRemoved')) }
      catch (e: any) { error.value = e?.message || t('storageSpaces.hostMountFailed') }
      finally { submitting.value = false }
    },
  })
}
function updateVisibility(value: boolean) {
  if (!submitting.value) emit('update:visible', value)
}
</script>

<template>
  <t-dialog :visible="visible" width="720px" :footer="false" :close-on-overlay-click="!submitting" :close-on-esc-keydown="!submitting" @update:visible="updateVisibility">
    <template #header>{{ t('storageSpaces.hostMountTitle') }}</template>
    <p class="hint">{{ t('storageSpaces.hostMountHint') }}</p>
    <t-alert v-if="error" theme="warning" :message="error" />
    <t-loading :loading="busy">
      <div v-if="roots.length" class="mount-form">
        <label>{{ t('storageSpaces.hostMountRoot') }}</label>
        <t-select v-model="root" :disabled="submitting"><t-option v-for="item in roots" :key="item.id" :value="item.id" :label="item.id" /></t-select>
        <label>{{ t('storageSpaces.hostMountDirectory') }}</label>
        <DirectoryPicker v-if="root" v-model="directory" :context-key="`host:${root}`" :root-label="root" :load-directories="browse" :disabled="submitting" />
        <p class="hint">{{ t('storageSpaces.currentSelection', {path: directory || root}) }}</p>
        <label>{{ t('storageSpaces.spaceName') }}</label>
        <t-input v-model="name" :maxlength="128" :disabled="submitting" />
        <t-button :loading="submitting" :disabled="!name.trim() || !root" @click="create">{{ t('storageSpaces.hostMountCreate') }}</t-button>
      </div>
      <section v-if="mounts.length" class="existing">
        <h3>{{ t('storageSpaces.hostMountExisting') }}</h3>
        <div v-for="mount in mounts" :key="mount.id" class="existing-row">
          <div><strong>{{ mount.root }}</strong> / {{ mount.directory || '.' }} <span class="hint">· {{ mount.status === 'ready' ? t('storageSpaces.ready') : t('storageSpaces.unavailable') }}</span></div>
          <div class="existing-actions">
            <t-tag v-if="registered(mount)" theme="success" variant="light">{{ t('storageSpaces.alreadyRegistered') }}</t-tag>
            <t-button v-else size="small" variant="outline" :disabled="mount.status !== 'ready' || submitting" @click="registerExisting(mount)">{{ t('storageSpaces.register') }}</t-button>
            <t-button size="small" variant="text" theme="danger" :disabled="mountInUse(mount) || submitting" @click="removeMount(mount)">{{ t('storageSpaces.hostMountRemove') }}</t-button>
          </div>
        </div>
      </section>
    </t-loading>
  </t-dialog>
</template>

<style scoped>
.hint{font-size:var(--app-text-md);line-height:1.6;color:var(--td-text-color-secondary)}.mount-form{display:grid;gap:10px;margin:16px 0}.existing{border-top:1px solid var(--td-component-border);padding-top:14px}.existing h3{font-size:var(--app-text-lg)}.existing-row{display:flex;justify-content:space-between;align-items:center;gap:12px;padding:10px 0}.existing-row div{min-width:0;overflow-wrap:anywhere}.existing-actions{display:flex;gap:8px;align-items:center}
</style>
