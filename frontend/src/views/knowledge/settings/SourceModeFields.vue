<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
const settings=defineModel<Record<string,any>>({required:true})
const {t}=useI18n()
const mode=computed({get:()=>settings.value.mode||'documents',set:value=>{settings.value={...settings.value,mode:value}}})
const excludes=computed({get:()=>Array.isArray(settings.value.exclude)?settings.value.exclude.join('\n'):'node_modules\nvendor\ndist\nbuild\n.venv\n__pycache__\ncoverage',set:value=>{settings.value={...settings.value,exclude:value.split('\n').map(p=>p.trim()).filter(Boolean)}}})
</script>
<template>
  <section class="source-mode">
    <h4 class="setting-drawer__section-title">{{ t('datasource.source.usage') }}</h4>
    <t-radio-group v-model="mode">
      <t-radio value="documents">{{ t('datasource.source.documents') }}</t-radio>
      <t-radio value="source">{{ t('datasource.source.readonly') }}</t-radio>
    </t-radio-group>
    <p>{{ t(mode==='source'?'datasource.source.sourceHint':'datasource.source.documentHint') }}</p>
    <template v-if="mode==='source'">
      <label>{{ t('datasource.source.excludes') }}</label>
      <t-textarea v-model="excludes" :autosize="{minRows:3,maxRows:7}" />
      <p>{{ t('datasource.source.excludesHint') }}</p>
    </template>
  </section>
</template>
<style scoped>
.source-mode{margin-bottom:24px}.source-mode p{color:var(--td-text-color-secondary);font-size:12px;line-height:1.6;margin:10px 0}.source-mode label{display:block;margin:12px 0 8px}
</style>
