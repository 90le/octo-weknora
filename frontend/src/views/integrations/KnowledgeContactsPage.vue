<template>
  <KnowledgeOperationsLayout :title="`${name || '知识库'} · 联系人`" description="按产品主题配置负责解答的人。Bot 仅提供联系指引，不会自动通知或催办。">
    <template #breadcrumb><router-link class="back-link" :to="{name:'knowledgeBaseDetail',params:{kbId}}">返回知识库</router-link></template>
    <t-alert v-if="error" theme="error">{{ error }} <t-button variant="text" @click="load">重试</t-button></t-alert>
    <t-loading :loading="loading"><OctoBusinessPanel v-if="!loading && !error" :key="`${workspaceKey}:${kbId}`" view="contacts" :knowledge-base-id="kbId" /></t-loading>
  </KnowledgeOperationsLayout>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { getKnowledgeBaseById } from '@/api/knowledge-base'
import KnowledgeOperationsLayout from './KnowledgeOperationsLayout.vue'
import OctoBusinessPanel from './OctoBusinessPanel.vue'
const route=useRoute(),auth=useAuthStore(),name=ref(''),error=ref(''),loading=ref(false)
const kbId=computed(()=>String(route.params.kbId||'')),workspaceKey=computed(()=>String(auth.currentTenantId||''))
let generation=0
async function load(){const version=++generation;name.value='';error.value='';loading.value=true;try{const r=await getKnowledgeBaseById(kbId.value);if(version===generation)name.value=r.data.name}catch{if(version===generation)error.value='无法读取此知识库，请核对当前工作区与访问权限。'}finally{if(version===generation)loading.value=false}}
watch([kbId,workspaceKey],load,{immediate:true});onBeforeUnmount(()=>{generation++})
</script>
<style scoped>.back-link{display:inline-block;color:var(--td-text-color-secondary);margin-bottom:12px;text-decoration:none}.back-link:hover{color:var(--td-brand-color)}</style>
