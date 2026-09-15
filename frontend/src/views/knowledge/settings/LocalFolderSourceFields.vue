<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { localSourceRoots } from '@/api/source-snapshot'
const settings=defineModel<Record<string,any>>({required:true})
const {t}=useI18n()
const roots=ref<{id:string;name:string}[]>([])
const error=ref('')
const root=computed({get:()=>settings.value.root_id||'',set:value=>{settings.value={...settings.value,root_id:value}}})
const directory=computed({get:()=>settings.value.directory||'',set:value=>{settings.value={...settings.value,directory:value}}})
onMounted(async()=>{try{roots.value=await localSourceRoots()}catch(e:any){error.value=e?.message||t('datasource.source.unavailable')}})
</script>
<template>
  <t-alert v-if="error" theme="warning" :message="error" />
  <t-form label-align="top">
    <t-form-item :label="t('datasource.source.root')" required>
      <t-select v-model="root" :placeholder="t('datasource.source.chooseRoot')">
        <t-option v-for="item in roots" :key="item.id" :value="item.id" :label="item.name||item.id" />
      </t-select>
    </t-form-item>
    <t-form-item :label="t('datasource.source.directory')">
      <t-input v-model="directory" :placeholder="t('datasource.source.directoryHint')" />
    </t-form-item>
  </t-form>
  <p class="source-root-hint">{{ t('datasource.source.rootHint') }}</p>
</template>
<style scoped>.source-root-hint{color:var(--td-text-color-secondary);font-size:12px;line-height:1.6}</style>
