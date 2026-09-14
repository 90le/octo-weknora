<template>
  <t-button variant="text" @click="open">Octo 使用范围</t-button>
  <t-drawer v-model:visible="visible" header="Octo 使用范围" :footer="false" size="480px">
    <p>以下是直接绑定本知识库的区域。子区继承的最终范围在群与子区设置中查看。</p>
    <t-loading :loading="loading">
      <p v-if="error" role="alert">{{ error }}</p>
      <p v-else-if="!loading && !rows.length">暂无直接绑定。</p>
      <ul><li v-for="s in rows" :key="s.scope_id"><strong>{{ s.display_name }}</strong> · {{ s.subarea_id ? '子区' : '主群' }}<p>Bot：{{ s.account_id }}<br />群：{{ s.group_id }}<br v-if="s.subarea_id" />{{ s.subarea_id ? `子区：${s.subarea_id}` : '' }}</p></li></ul>
    </t-loading>
    <router-link :to="{ path: '/platform/settings', query: { section: 'integration-octo' } }">前往群与子区管理绑定</router-link>
  </t-drawer>
</template>
<script setup lang="ts">
import { ref, watch } from 'vue'
import { get } from '@/utils/request'
const props = defineProps<{ kbId: string }>()
const visible = ref(false), loading = ref(false), error = ref('')
const rows = ref<Array<{ scope_id: string; display_name: string; account_id: string; group_id: string; subarea_id: string }>>([])
let version = 0
watch(() => props.kbId, () => { version++; visible.value = false; rows.value = [] })
async function open() {
  const v = ++version
  visible.value = true; loading.value = true; error.value = ''; rows.value = []
  try { const r = await get(`/api/v1/octo/knowledge-bases/${encodeURIComponent(props.kbId)}/scopes`); if (v === version) rows.value = r.data }
  catch { if (v === version) error.value = '使用范围读取失败，请检查当前工作区权限。' }
  finally { if (v === version) loading.value = false }
}
</script>
<style scoped>li { padding: 12px 0; overflow-wrap: anywhere; } p { color: var(--td-text-color-secondary); line-height: 1.6; } ul { padding-left: 20px; }</style>
