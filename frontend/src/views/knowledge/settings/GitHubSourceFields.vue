<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const settings = defineModel<Record<string, any>>({ required: true })
const { t } = useI18n()
const repository = computed({
  get: () => String(settings.value.repository || ''),
  set: value => { settings.value = { ...settings.value, repository: value } },
})
const refName = computed({
  get: () => String(settings.value.ref || ''),
  set: value => { settings.value = { ...settings.value, ref: value } },
})
const paths = computed({
  get: () => Array.isArray(settings.value.paths) ? settings.value.paths.join('\n') : '',
  set: value => { settings.value = { ...settings.value, paths: value.split('\n').map(p => p.trim()).filter(Boolean) } },
})
</script>

<template>
  <h4 class="setting-drawer__section-title">{{ t('datasource.github.repository') }}</h4>
  <p class="github-source-hint">{{ t('datasource.github.hint') }}</p>
  <t-form label-align="top">
    <t-form-item :label="t('datasource.github.repository')" required>
      <t-input v-model="repository" placeholder="owner/repository" />
    </t-form-item>
    <t-form-item :label="t('datasource.gitlab.ref')">
      <t-input v-model="refName" :placeholder="t('datasource.gitlab.refPlaceholder')" />
    </t-form-item>
    <t-form-item :label="t('datasource.gitlab.paths')">
      <t-textarea v-model="paths" :placeholder="t('datasource.github.pathsHint')" :autosize="{ minRows: 3, maxRows: 7 }" />
    </t-form-item>
  </t-form>
</template>

<style scoped>
.github-source-hint {
  color: var(--td-text-color-secondary);
  font-size: var(--td-font-size-body-small);
  line-height: 1.6;
  margin: 0 0 16px;
}
</style>
