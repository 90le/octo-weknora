<template>
  <KnowledgeOperationsLayout title="渠道接入" description="连接 Bot，选择回复的智能体，再设置各群、子区和私聊可以使用的知识。">
    <template #navigation>
      <nav class="operations-navigation" aria-label="渠道管理">
        <router-link :to="{name:'knowledgeChannels'}" :aria-current="!isGroups?'page':undefined">Bot 与私聊</router-link>
        <router-link :to="{name:'octoGroups'}" :aria-current="isGroups?'page':undefined">Octo 群与子区</router-link>
      </nav>
    </template>
    <OctoScopePanel v-if="isGroups" :key="workspaceKey" />
    <template v-else>
      <div class="channel-intro"><h2>已连接的 Bot</h2><p>连接设置、私聊授权和消息处理记录在同一 Bot 的详情中管理。启用开关与实时连接状态分别显示。</p></div>
      <IMChannelPanel :key="workspaceKey" v-model:filter-agent-id="filterAgentId" />
    </template>
  </KnowledgeOperationsLayout>
</template>
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import IMChannelPanel from '@/components/IMChannelPanel.vue'
import OctoScopePanel from './OctoScopePanel.vue'
import KnowledgeOperationsLayout from './KnowledgeOperationsLayout.vue'
const route=useRoute(), auth=useAuthStore(), filterAgentId=ref('')
const isGroups=computed(()=>route.name==='octoGroups')
const workspaceKey=computed(()=>String(auth.currentTenantId||''))
watch(()=>route.query.agentId,value=>{filterAgentId.value=typeof value==='string'?value:''},{immediate:true})
</script>
<style scoped>.channel-intro{margin-bottom:20px}.channel-intro h2{font-size:18px;margin:0 0 8px}.channel-intro p{color:var(--td-text-color-secondary);line-height:1.6;margin:0}</style>
