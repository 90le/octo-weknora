<template>
  <div class="business-filters">
    <div class="filter-fields" :class="{ 'knowledge-locked': knowledgeLocked }">
      <label class="keyword-field">搜索问题
        <t-input :model-value="modelValue.keyword" clearable placeholder="标题或描述" @update:model-value="change('keyword', $event)" @enter="$emit('search')" />
      </label>
      <label>来源区域
        <t-select :model-value="modelValue.scope_id" clearable filterable placeholder="全部群聊、子区和私聊" :options="scopeOptions" @update:model-value="change('scope_id', $event)" @change="filterChanged" />
      </label>
      <label v-if="!knowledgeLocked">知识库
        <t-select :model-value="modelValue.knowledge_base_id" clearable filterable placeholder="全部知识库" :options="kbOptions" @update:model-value="change('knowledge_base_id', $event)" @change="filterChanged" />
      </label>
      <label>问题类型
        <t-select :model-value="modelValue.kind" clearable placeholder="全部类型" :options="kindOptions" @update:model-value="change('kind', $event)" @change="filterChanged" />
      </label>
      <label>处理状态
        <t-select :model-value="modelValue.status" clearable placeholder="全部状态" :options="statusOptions" @update:model-value="change('status', $event)" @change="filterChanged" />
      </label>
    </div>
    <div class="filter-actions">
      <label>登记开始日期
        <input :value="fromDay" type="date" aria-label="登记开始日期" @input="$emit('update:fromDay', ($event.target as HTMLInputElement).value)" />
      </label>
      <label>登记结束日期
        <input :value="toDay" type="date" aria-label="登记结束日期（含当天）" @input="$emit('update:toDay', ($event.target as HTMLInputElement).value)" />
      </label>
      <div class="buttons">
        <t-button :loading="loading" @click="$emit('search')">{{ actionLabel || '查询' }}</t-button>
        <t-button variant="outline" :disabled="loading" @click="$emit('reset')">重置筛选</t-button>
      </div>
      <span class="date-note">按本地时间，结束日期含当天</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { IssueFilter } from '@/api/octo-business'
import { issueKindLabels, issueStatusLabels } from './octoBusinessDisplay'

const props = defineProps<{
  modelValue: IssueFilter
  fromDay: string
  toDay: string
  knowledgeLocked?: boolean
  scopeOptions: Array<{ value: string; label: string }>
  kbOptions: Array<{ value: string; label: string }>
  loading?: boolean
  actionLabel?: string
  autoSearch?: boolean
}>()
const emit = defineEmits<{
  'update:modelValue': [value: IssueFilter]
  'update:fromDay': [value: string]
  'update:toDay': [value: string]
  search: []
  reset: []
}>()
const kindOptions = Object.entries(issueKindLabels).map(([value, label]) => ({ value, label }))
const statusOptions = Object.entries(issueStatusLabels).map(([value, label]) => ({ value, label }))
function filterChanged() { if (props.autoSearch !== false) emit('search') }
function change(key: keyof IssueFilter, value: unknown) {
  emit('update:modelValue', { ...props.modelValue, [key]: typeof value === 'string' ? value : undefined })
}
</script>

<style scoped>
.business-filters { padding: 20px; border: 1px solid var(--td-component-border); border-radius: var(--td-radius-large, 8px); background: var(--td-bg-color-container); }
.filter-fields { display: grid; grid-template-columns: minmax(210px, 1.5fr) repeat(4, minmax(130px, 1fr)); gap: 16px; }
.filter-fields.knowledge-locked { grid-template-columns: minmax(230px, 1.5fr) repeat(3, minmax(140px, 1fr)); }
label { display: flex; flex-direction: column; gap: 8px; min-width: 0; font-size: 13px; color: var(--td-text-color-primary); }
.filter-actions { display: flex; align-items: end; flex-wrap: wrap; gap: 16px; margin-top: 16px; }
input[type=date] { box-sizing: border-box; width: 166px; height: 34px; padding: 6px 10px; border: 1px solid var(--td-component-border); border-radius: var(--td-radius-default, 4px); color: var(--td-text-color-primary); background: var(--td-bg-color-container); font: inherit; }
input[type=date]:focus { outline: 2px solid var(--td-brand-color-light); border-color: var(--td-brand-color); }
.buttons { display: flex; gap: 10px; }
.date-note { align-self: end; padding-bottom: 8px; font-size: 12px; color: var(--td-text-color-secondary); }
@media(max-width:1100px) { .filter-fields, .filter-fields.knowledge-locked { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
@media(max-width:720px) { .filter-fields, .filter-fields.knowledge-locked { grid-template-columns: repeat(2, minmax(0, 1fr)); } .keyword-field { grid-column: 1 / -1; } }
@media(max-width:460px) { .filter-fields, .filter-fields.knowledge-locked { grid-template-columns: minmax(0, 1fr); } .filter-actions>label { flex: 1; min-width: 130px; } input[type=date] { width: 100%; } .business-filters { padding: 14px; } }
</style>
