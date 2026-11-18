<template>
  <KnowledgeOperationsLayout title="知识运营" description="查看问答质量、处理知识缺口与问题、管理运营报告。">
    <template #navigation>
      <nav class="operations-navigation" aria-label="问题运营">
        <router-link :to="{name:'knowledgeOperations'}" :aria-current="view==='overview'?'page':undefined">总览</router-link>
        <router-link :to="{name:'knowledgeIssues'}" :aria-current="view==='issues'?'page':undefined">问题列表</router-link>
        <router-link :to="{name:'knowledgeQuality'}" :aria-current="view==='quality'?'page':undefined">问答质量</router-link>
        <router-link :to="{name:'knowledgeReports'}" :aria-current="view==='reports'?'page':undefined">运营报告</router-link>
      </nav>
    </template>
    <OctoQualityReview v-if="view==='quality' || view==='overview'" :key="`${workspaceKey}:${view}`" :overview="view==='overview'" />
    <OctoBusinessPanel v-else :key="workspaceKey" :view="view" />
  </KnowledgeOperationsLayout>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import KnowledgeOperationsLayout from './KnowledgeOperationsLayout.vue'
import OctoBusinessPanel from './OctoBusinessPanel.vue'
import OctoQualityReview from './OctoQualityReview.vue'
const route=useRoute(), auth=useAuthStore()
const view=computed(()=>route.name==='knowledgeOperations'?'overview':route.name==='knowledgeQuality'?'quality':route.name==='knowledgeReports'?'reports':'issues')
const workspaceKey=computed(()=>String(auth.effectiveTenantId||''))
</script>
