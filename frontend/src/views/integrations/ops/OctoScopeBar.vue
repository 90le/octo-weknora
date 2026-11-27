<template>
  <div class="ops-scope-bar">
    <t-input
      v-model="opsScope.keyword"
      size="small"
      clearable
      class="scope-search"
      placeholder="搜索问法、产品库、群或成员..."
      @enter="emit('change')"
      @clear="emit('change')"
    />
    <t-select
      v-model="opsScope.sourceValue"
      size="small"
      filterable
      clearable
      class="scope-select"
      placeholder="全部 Octo 来源"
      :options="sourceOptions"
      @change="emit('change')"
    />
    <t-select
      v-model="opsScope.kbId"
      size="small"
      filterable
      clearable
      class="scope-select"
      placeholder="全部产品库"
      :options="kbOptions"
      @change="emit('change')"
    />
    <t-select
      v-model="opsScope.channelId"
      size="small"
      filterable
      clearable
      class="scope-select sm"
      placeholder="全部 Octo Bot"
      :options="botOptions"
      @change="emit('change')"
    />
    <div class="scope-days" role="group" aria-label="统计时间窗">
      <button
        v-for="opt in dayOpts"
        :key="opt.v"
        type="button"
        class="day-btn"
        :aria-pressed="opsScope.days === opt.v"
        @click="pickDays(opt.v)"
      >
        {{ opt.label }}
      </button>
    </div>
    <span v-if="opsScope.days === 0" class="scope-dates">
      <input v-model="opsScope.customFrom" type="date" class="date-in" @change="emit('change')" />
      <span class="mini">至</span>
      <input v-model="opsScope.customTo" type="date" class="date-in" @change="emit('change')" />
    </span>
    <t-button v-if="hasFilters" variant="text" size="small" @click="clearAll">重置</t-button>
    <span v-if="summaryText" class="scope-meta">{{ summaryText }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { OctoScope } from '@/api/octo'
import { opsScope, resetOpsScope } from './useOctoOpsScope'

const props = defineProps<{
  scopes: OctoScope[]
  kbs: Array<{ id: string; name: string }>
  bots: Array<{ id: string; name: string }>
  summaryText?: string
}>()
const emit = defineEmits<{ change: [] }>()

const dayOpts = [
  { v: 7, label: '近 7 天' },
  { v: 14, label: '近 14 天' },
  { v: 30, label: '近 30 天' },
  { v: 0, label: '自定义' },
]

const sourceOptions = computed(() => [
  {
    group: '按会话类型',
    children: [
      { label: '全部主群', value: 'type:group' },
      { label: '全部继承子区', value: 'type:subarea' },
      { label: '全部私聊（占 26%）', value: 'type:dm' },
    ],
  },
  {
    group: '已核验群 / 子区',
    children: props.scopes.map((sc) => ({
      label: sc.subarea_id ? `子区 · ${sc.display_name}` : `主群 · ${sc.display_name}`,
      value: `scope:${sc.id}`,
    })),
  },
])

const kbOptions = computed(() =>
  props.kbs.filter((k) => !k.name.startsWith('__')).map((k) => ({ label: k.name, value: k.id })),
)

const botOptions = computed(() => props.bots.map((b) => ({ label: b.name || b.id, value: b.id })))

const hasFilters = computed(() =>
  Boolean(opsScope.keyword || opsScope.sourceValue || opsScope.kbId || opsScope.channelId),
)

function pickDays(v: number) {
  opsScope.days = v
  emit('change')
}

function clearAll() {
  resetOpsScope()
  emit('change')
}
</script>

<style scoped lang="less">
.ops-scope-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 8px 20px;
  background: var(--td-bg-color-container);
  border-bottom: 1px solid var(--td-component-border);
}
.scope-search { width: 220px; }
.scope-select { width: 176px; }
.scope-select.sm { width: 144px; }
.scope-days {
  display: inline-flex;
  padding: 2px;
  border-radius: 6px;
  background: var(--td-bg-color-secondarycontainer);
  border: 1px solid var(--td-component-border);
}
.day-btn {
  height: 24px;
  padding: 0 9px;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: var(--td-text-color-secondary);
  font-size: 12px;
  cursor: pointer;
}
.day-btn[aria-pressed='true'] {
  background: var(--td-bg-color-container);
  color: var(--td-brand-color);
  font-weight: 600;
}
.scope-dates { display: inline-flex; align-items: center; gap: 4px; }
.date-in {
  height: 28px;
  padding: 0 6px;
  border: 1px solid var(--td-border-level-2-color);
  border-radius: 6px;
  background: var(--td-bg-color-container);
  color: var(--td-text-color-primary);
  font-size: 12px;
}
.scope-meta { margin-left: auto; font-size: 12px; color: var(--td-text-color-placeholder); }
.mini { font-size: 12px; color: var(--td-text-color-placeholder); }
</style>
