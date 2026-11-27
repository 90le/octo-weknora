<template>
  <t-drawer
    :visible="visible"
    header="Octo 运营与后台复核设置"
    size="440px"
    attach="body"
    @close="emit('update:visible', false)"
  >
    <div class="settings-form">
      <t-alert theme="info" :close="false">
        后台自动复核优先扫描反复未答、对话中断与检索异常，再按比例抽检普通答复；知识库联系人仍兼容保留在各知识库「联系人」独立页维护。
      </t-alert>
      <div class="row-between">
        <span>开启后台自动 AI 复核</span>
        <t-switch v-model="form.enabled" />
      </div>
      <label class="fld">
        <span>复核模型（留空沿用当前 Octo Bot 绑定模型）</span>
        <t-select
          v-model="form.model_id"
          :options="modelOptions"
          :loading="loadingModels"
          clearable
          filterable
          placeholder="留空沿用当前 Octo Bot 绑定模型"
        />
        <small v-if="form.model_id && !matchedModel" class="warn-hint">
          当前保存的模型 ID（{{ form.model_id }}）未在可用对话模型列表中，建议重新选择或清空沿用默认。
        </small>
      </label>
      <div class="two-col">
        <label class="fld">
          <span>每批上限（1–12）</span>
          <t-input-number v-model="form.per_run" :min="1" :max="12" theme="normal" />
        </label>
        <label class="fld">
          <span>每日上限（条）</span>
          <t-input-number v-model="form.daily_limit" :min="1" :max="500" theme="normal" />
        </label>
      </div>
      <label class="fld">
        <span>普通答复抽检比例（%）</span>
        <t-input-number v-model="form.sample_percent" :min="0" :max="100" theme="normal" />
      </label>
    </div>
    <template #footer>
      <t-button variant="outline" @click="emit('update:visible', false)">取消</t-button>
      <t-button theme="primary" :loading="saving" @click="save">保存设置</t-button>
    </template>
  </t-drawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { listModels, type ModelConfig } from '@/api/model'
import { qualitySettings, saveQualitySettings, type QualitySettings } from '@/api/octo-business'

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{ 'update:visible': [boolean]; saved: [] }>()

const saving = ref(false)
const loadingModels = ref(false)
const models = ref<ModelConfig[]>([])
const form = reactive<QualitySettings>({
  enabled: true,
  model_id: '',
  per_run: 8,
  daily_limit: 32,
  sample_percent: 14,
})

const matchedModel = computed(() => models.value.find((m) => m.id === form.model_id))

const modelOptions = computed(() => {
  const opts = models.value
    .filter((m): m is ModelConfig & { id: string } => Boolean(m.id))
    .map((m) => {
      const title = m.display_name || m.name || m.id
      const src = m.source === 'local' ? '本地' : '远端'
      const tag = m.is_default ? `${src} · 默认` : src
      return {
        label: `${title}（${tag}）`,
        value: m.id,
      }
    })
  if (form.model_id && !opts.some((o) => o.value === form.model_id)) {
    opts.unshift({
      label: `历史模型（${form.model_id.slice(0, 8)}… · 已不可用）`,
      value: form.model_id,
    })
  }
  return opts
})

watch(
  () => props.visible,
  async (open) => {
    if (!open) return
    loadingModels.value = true
    const [cfgRes, modelRes] = await Promise.allSettled([
      qualitySettings(),
      listModels('KnowledgeQA'),
    ])
    loadingModels.value = false
    if (cfgRes.status === 'fulfilled' && cfgRes.value.data) {
      Object.assign(form, cfgRes.value.data)
    } else if (cfgRes.status === 'rejected') {
      MessagePlugin.warning('读取后台复核设置失败，展示默认参考值')
    }
    if (modelRes.status === 'fulfilled') {
      models.value = modelRes.value || []
    }
  },
)

async function save() {
  saving.value = true
  try {
    await saveQualitySettings({ ...form, model_id: form.model_id || '' })
    MessagePlugin.success('Octo 复核设置已保存')
    emit('saved')
    emit('update:visible', false)
  } catch {
    MessagePlugin.error('保存设置失败，请检查参数范围或权限')
  } finally {
    saving.value = false
  }
}
</script>

<style scoped lang="less">
.settings-form { display: grid; gap: 14px; }
.row-between { display: flex; justify-content: space-between; align-items: center; font-size: 13.5px; }
.two-col { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.fld { display: grid; gap: 5px; font-size: 12.5px; color: var(--td-text-color-secondary); }
.warn-hint { color: var(--td-warning-color); font-size: 12px; }
</style>
