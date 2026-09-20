<template>
  <KnowledgeOperationsLayout title="问题反馈" description="集中处理知识缺口、Bug 和建议。处理问题、补充知识与发布资料分别记录。">
    <template #navigation>
      <nav class="operations-navigation" aria-label="问题运营">
        <router-link :to="{name:'knowledgeIssues'}" :aria-current="view==='issues'?'page':undefined">问题列表</router-link>
        <router-link :to="{name:'knowledgeReports'}" :aria-current="view==='reports'?'page':undefined">报告与周报</router-link>
      </nav>
    </template>
    <OctoBusinessPanel :key="workspaceKey" :view="view" />
  </KnowledgeOperationsLayout>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import KnowledgeOperationsLayout from './KnowledgeOperationsLayout.vue'
import OctoBusinessPanel from './OctoBusinessPanel.vue'
const route=useRoute(), auth=useAuthStore()
const view=computed(()=>route.name==='knowledgeReports'?'reports':'issues')
const workspaceKey=computed(()=>String(auth.currentTenantId||''))
</script>
