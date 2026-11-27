<template>
  <div class="ops-shell">
    <header class="ops-hd">
      <div class="ops-brand">
        <h1>知识运营</h1>
        <t-tag size="small" theme="primary" variant="light">Octo 渠道</t-tag>
      </div>
      <nav class="ops-tabs" aria-label="知识运营导航">
        <router-link :to="{ name: 'knowledgeOperations' }" :aria-current="activeView === 'overview' ? 'page' : undefined">
          工作台
        </router-link>
        <router-link :to="{ name: 'knowledgeQuality' }" :aria-current="activeView === 'quality' ? 'page' : undefined">
          问答质量
          <span v-if="counts.pendingQA" class="cnt">{{ counts.pendingQA }}</span>
        </router-link>
        <router-link :to="{ name: 'knowledgeIssues' }" :aria-current="activeView === 'issues' || activeView === 'issueDetail' ? 'page' : undefined">
          问题闭环
          <span v-if="counts.openIssues" class="cnt">{{ counts.openIssues }}</span>
        </router-link>
        <router-link :to="{ name: 'knowledgeReports' }" :aria-current="activeView === 'reports' ? 'page' : undefined">
          报告与分发
        </router-link>
      </nav>
      <div class="ops-hd-r">
        <t-button size="small" variant="outline" @click="settingsOpen = true">复核设置</t-button>
      </div>
    </header>

    <OctoScopeBar
      v-if="activeView !== 'issueDetail'"
      :scopes="scopes"
      :kbs="kbs"
      :bots="bots"
      :summary-text="`${counts.pendingQA} 条待审 · ${counts.openIssues} 件在办`"
      @change="reloadActiveView"
    />

    <main class="ops-body" :class="{ 'no-pad': activeView === 'quality' }">
      <OctoWorkbenchView
        v-if="activeView === 'overview'"
        :key="`${workspaceKey}:overview`"
        ref="workbenchRef"
        @counts-updated="onCounts"
      />
      <OctoQualitySplitView
        v-else-if="activeView === 'quality'"
        :key="`${workspaceKey}:quality`"
        ref="qualityRef"
        :kbs="kbs"
      />
      <OctoIssueDetailView
        v-else-if="activeView === 'issueDetail'"
        :key="`${workspaceKey}:detail:${route.params.issueId}`"
        :kbs="kbs"
      />
      <OctoReportsCenterView
        v-else-if="activeView === 'reports'"
        :key="`${workspaceKey}:reports`"
        ref="reportsRef"
        :kbs="kbs"
        :bots="bots"
      />
      <OctoIssuesListView
        v-else
        :key="`${workspaceKey}:issues`"
        ref="issuesRef"
        :kbs="kbs"
      />
    </main>

    <OctoSettingsDrawer v-model:visible="settingsOpen" @saved="reloadActiveView" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { listAllIMChannels } from '@/api/agent'
import { listKnowledgeBases } from '@/api/knowledge-base'
import { listScopes, type OctoScope } from '@/api/octo'
import { workbenchSummary } from '@/api/octo-business'
import { useAuthStore } from '@/stores/auth'
import OctoScopeBar from './ops/OctoScopeBar.vue'
import OctoSettingsDrawer from './ops/OctoSettingsDrawer.vue'
import OctoWorkbenchView from './ops/OctoWorkbenchView.vue'
import OctoQualitySplitView from './ops/OctoQualitySplitView.vue'
import OctoIssuesListView from './ops/OctoIssuesListView.vue'
import OctoIssueDetailView from './ops/OctoIssueDetailView.vue'
import OctoReportsCenterView from './ops/OctoReportsCenterView.vue'

const route = useRoute()
const auth = useAuthStore()

const activeView = computed(() => {
  if (route.name === 'knowledgeOperations') return 'overview'
  if (route.name === 'knowledgeQuality') return 'quality'
  if (route.name === 'knowledgeReports') return 'reports'
  if (route.name === 'knowledgeIssueDetail') return 'issueDetail'
  return 'issues'
})
const workspaceKey = computed(() => String(auth.effectiveTenantId || ''))

const scopes = ref<OctoScope[]>([])
const kbs = ref<Array<{ id: string; name: string }>>([])
const bots = ref<Array<{ id: string; name: string }>>([])
const settingsOpen = ref(false)
const counts = reactive({ pendingQA: 0, openIssues: 0 })

const workbenchRef = ref<{ reload: () => void } | null>(null)
const qualityRef = ref<{ reload: () => void } | null>(null)
const issuesRef = ref<{ reload: () => void } | null>(null)
const reportsRef = ref<{ reload: () => void } | null>(null)

function onCounts(v: { pendingQA: number; openIssues: number }) {
  counts.pendingQA = v.pendingQA
  counts.openIssues = v.openIssues
}

function reloadActiveView() {
  workbenchRef.value?.reload()
  qualityRef.value?.reload()
  issuesRef.value?.reload()
  reportsRef.value?.reload()
}

async function loadMeta() {
  const [scRes, kbRes, chRes, wbRes] = await Promise.allSettled([
    listScopes(),
    listKnowledgeBases(),
    listAllIMChannels(),
    workbenchSummary({}),
  ])
  if (scRes.status === 'fulfilled') scopes.value = scRes.value.data || []
  if (kbRes.status === 'fulfilled') {
    const raw = (kbRes.value as unknown as { data?: Array<{ id: string; name: string }> })?.data || []
    kbs.value = raw
  }
  if (chRes.status === 'fulfilled') {
    bots.value = (chRes.value.data || [])
      .filter((c) => c.platform === 'octo')
      .map((c) => ({ id: c.id, name: c.name || c.bot_identity || c.id }))
  }
  if (wbRes.status === 'fulfilled' && wbRes.value.data) {
    counts.pendingQA = wbRes.value.data.pending_qa || 0
    counts.openIssues = wbRes.value.data.open_issues || 0
  }
}

onMounted(loadMeta)
</script>

<style scoped lang="less">
.ops-shell {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 0;
  background: var(--td-bg-color-page);
  color: var(--td-text-color-primary);
}
.ops-hd {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  gap: 14px;
  height: 54px;
  padding: 0 20px;
  background: var(--td-bg-color-container);
  border-bottom: 1px solid var(--td-component-border);
}
.ops-brand {
  display: flex;
  align-items: center;
  gap: 8px;
  white-space: nowrap;
  h1 { margin: 0; font-size: 16px; font-weight: 700; }
}
.ops-tabs {
  display: flex;
  align-items: center;
  gap: 4px;
  height: 100%;
  overflow-x: auto;
}
.ops-tabs a {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 100%;
  padding: 0 12px;
  color: var(--td-text-color-secondary);
  text-decoration: none;
  font-size: 13.5px;
  white-space: nowrap;
}
.ops-tabs a[aria-current='page'] {
  color: var(--td-brand-color);
  font-weight: 600;
}
.ops-tabs a[aria-current='page']::after {
  content: '';
  position: absolute;
  left: 10px;
  right: 10px;
  bottom: 0;
  height: 2.5px;
  border-radius: 2px 2px 0 0;
  background: var(--td-brand-color);
}
.cnt {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: 99px;
  background: var(--td-bg-color-secondarycontainer);
  font-size: 11px;
  font-weight: 600;
}
.ops-hd-r { margin-left: auto; display: flex; align-items: center; gap: 8px; }
.ops-body { flex: 1; min-height: 0; overflow-y: auto; padding: 18px 22px 64px; }
.ops-body.no-pad { padding: 0; overflow: hidden; display: flex; flex-direction: column; }
</style>
