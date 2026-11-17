<template>
  <section class="private-access">
    <div>
      <h4>谁可以私聊</h4>
      <p>只影响当前 Bot 的私聊。群和子区继续使用各自的授权。</p>
    </div>
    <div class="private-modes" role="radiogroup" aria-label="私聊准入方式">
      <label v-for="mode in modes" :key="mode.value" :class="{ selected: model.mode === mode.value }">
        <input v-model="model.mode" type="radio" name="octo-private-mode" :value="mode.value" />
        <span><strong>{{ mode.label }}</strong><small>{{ mode.hint }}</small></span>
      </label>
    </div>

    <div class="private-list" v-if="model.mode === 'allowlist'">
      <label>UID 白名单 <small>{{ model.allowed_uids.length }} 个</small></label>
      <t-tag-input v-model="model.allowed_uids" :max="100" :input-props="{ 'aria-label': 'UID 白名单' }"
        placeholder="粘贴 UID，回车添加" @paste="pasteUIDs($event.e, 'allowed_uids')" />
    </div>
    <p v-else-if="model.allowed_uids.length" class="subtle">已保留 {{ model.allowed_uids.length }} 个白名单 UID，切回白名单模式后继续使用。</p>
    <div class="private-list">
      <label>UID 黑名单 <small>{{ model.blocked_uids.length }} 个</small></label>
      <t-tag-input v-model="model.blocked_uids" :max="100" :input-props="{ 'aria-label': 'UID 黑名单' }"
        placeholder="粘贴 UID，回车添加" @paste="pasteUIDs($event.e, 'blocked_uids')" />
      <p>黑名单优先，包括创建者。可一次粘贴多行 UID。</p>
      <t-alert v-if="overlap" theme="warning">有 {{ overlap }} 个 UID 同时在两张名单中，实际结果为拒绝。</t-alert>
    </div>

    <div class="private-list private-divider">
      <h4>私聊知识</h4>
      <label>可查询的知识库</label>
      <t-select v-model="model.knowledge_base_ids" aria-label="私聊知识库" placeholder="选择知识库" multiple clearable filterable
        :options="knowledgeOptions" />
      <p>没有选择知识库时，Bot 不处理私聊问答。</p>
      <t-alert v-if="unavailableKnowledge.length" theme="warning">
        {{ unavailableKnowledge.length }} 个已选知识库当前不可用。请移除后重新选择。
        <t-button variant="text" theme="primary" size="small" @click="removeUnavailableKnowledge">移除不可用项</t-button>
      </t-alert>
    </div>

    <div class="private-list private-divider">
      <h4>保存前预览</h4>
      <div class="preview-action">
        <t-input v-model="uid" aria-label="要检查的 UID" placeholder="输入 UID 检查结果（可选）" :maxlength="128"
          @enter="requestPreview" />
        <t-button variant="outline" theme="primary" :loading="busy" @click="requestPreview">预览权限</t-button>
      </div>
      <p>用草稿检查权限，不发送消息，也不会重连 Bot。</p>
      <div v-if="preview" class="preview-result" aria-live="polite">
        <t-tag :theme="preview.problem ? 'warning' : 'success'" variant="light">
          {{ preview.problem ? '无法保存' : '已核对' }}
        </t-tag>
        <span>拟保存：{{ summary(preview.policy) }}</span>
        <p v-if="preview.previous">当前配置：{{ displaySummary(preview.previous) }}</p>
        <p v-if="preview.problem" role="alert">{{ reasons[preview.problem] || '请检查私聊设置。' }}</p>
        <p v-else-if="preview.decision && lastTestUID === uid.trim()">
          <strong>{{ preview.decision === 'allowed' ? '允许' : '拒绝' }}：</strong>{{ reasons[preview.decision] }}
        </p>
        <p v-if="preview.creator_uid && preview.policy.mode === 'creator_only'">创建者 UID：<code>{{ preview.creator_uid }}</code></p>
        <p v-if="!preview.problem && preview.policy.mode !== 'disabled' && shownTools.length">
          私聊可申请：{{ shownTools.join('、') }}。实际可用性还受智能体配置约束。
        </p>
      </div>
    </div>

    <details class="private-divider private-tools">
      <summary>
        <strong>私聊工具</strong><small>{{ enabledTools }} 项已授予 · 仅影响私聊</small>
      </summary>
      <label v-for="tool in tools" :key="tool.key" class="private-tool">
        <span><strong>{{ tool.label }}</strong><small>{{ tool.hint }}</small></span>
        <input v-model="model.tools[tool.key]" type="checkbox" role="switch" :aria-label="tool.label" />
      </label>
      <div v-if="model.tools.allow_generated_files" class="private-tool">
        <span><strong>旧版 Markdown 发送权限</strong><small>独立于通用文件发送。</small></span>
        <t-button size="small" variant="outline" @click="model.tools.allow_generated_files = false">关闭</t-button>
      </div>
    </details>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue';
import type { OctoPrivatePolicy, OctoPrivatePreview, OctoPrivateSummary, OctoPrivateTools } from '@/api/agent';

const model = defineModel<OctoPrivatePolicy>({ required: true });
const props = defineProps<{
  knowledgeBases: { id: string; name: string }[];
  preview?: OctoPrivatePreview;
  busy: boolean;
}>();
const emit = defineEmits<{ preview: [uid: string] }>();
const uid = ref('');
const lastTestUID = ref('');
function requestPreview() {
  lastTestUID.value = uid.value.trim();
  emit('preview', lastTestUID.value);
}

const modes = [
  { value: 'disabled', label: '关闭私聊', hint: '不处理任何私聊问答' },
  { value: 'creator_only', label: '仅创建者', hint: '只允许 Octo 核验过的 Bot 创建者' },
  { value: 'allowlist', label: '仅白名单', hint: '只允许名单中的原生 UID' },
] as const;
const tools: { key: keyof OctoPrivateTools; label: string; hint: string }[] = [
  { key: 'allow_file_delivery', label: '发送生成的文件', hint: '只发送到当前已获准的私聊。' },
  { key: 'allow_sandbox_shell', label: '沙箱终端', hint: '由智能体在隔离环境中运行命令。' },
  { key: 'allow_sandbox_skills', label: '沙箱技能', hint: '使用智能体已配置且获授权的技能。' },
];
const overlap = computed(() => model.value.allowed_uids.filter(uid => model.value.blocked_uids.includes(uid)).length);
const enabledTools = computed(() => Object.values(model.value.tools).filter(Boolean).length);
const knownKnowledge = computed(() => new Set(props.knowledgeBases.map(kb => kb.id)));
const unavailableKnowledge = computed(() => model.value.knowledge_base_ids.filter(id => !knownKnowledge.value.has(id)));
const knowledgeOptions = computed(() => [
  ...props.knowledgeBases.map(kb => ({ label: kb.name, value: kb.id })),
  ...unavailableKnowledge.value.map(id => ({ label: `当前不可用 · ${id}`, value: id })),
]);
function removeUnavailableKnowledge() {
  model.value.knowledge_base_ids = model.value.knowledge_base_ids.filter(id => knownKnowledge.value.has(id));
}
const shownTools = computed(() => [
  ...tools.filter(tool => props.preview?.policy.tools[tool.key]).map(tool => tool.label),
  ...(props.preview?.policy.tools.allow_generated_files ? ['旧版 Markdown 发送'] : []),
]);
const reasons: Record<string, string> = {
  allowed: '这个 UID 符合私聊准入规则。',
  blocked: '这个 UID 在黑名单中。',
  disabled: '当前关闭私聊。',
  creator_unverified: '尚未核验创建者，请检查 Bot 连接。',
  not_creator: '这个 UID 不是已核验的创建者。',
  not_listed: '这个 UID 不在白名单中。',
  invalid_uid: '请输入有效的原生 UID。',
  no_knowledge: '请至少选择一个可查询的知识库。',
  invalid_knowledge: '所选知识库已失效或不属于当前工作区。',
};
function summary(policy: OctoPrivatePolicy) {
  return displaySummary({
    mode: policy.mode, allowed: policy.allowed_uids.filter(id => !policy.blocked_uids.includes(id)).length,
    blocked: policy.blocked_uids.length, knowledge: policy.knowledge_base_ids.length,
  });
}
function displaySummary(state: OctoPrivateSummary) {
  const audience = state.mode === 'allowlist'
    ? `白名单 ${state.allowed} 个可用` : modes.find(mode => mode.value === state.mode)?.label;
  return `${audience} · 黑名单 ${state.blocked} 个 · ${state.knowledge} 个知识库`;
}
function pasteUIDs(event: ClipboardEvent, key: 'allowed_uids' | 'blocked_uids') {
  const values = event.clipboardData?.getData('text').trim().split(/[\s,，;；]+/).filter(Boolean) || [];
  if (values.length < 2 || values.some(value => value.length > 128)) return;
  const next = [...new Set([...model.value[key], ...values])];
  if (next.length > 100) return;
  event.preventDefault();
  model.value[key] = next;
}
</script>

<style scoped>
.private-access, .private-list { display: grid; gap: 10px; }
.private-access { gap: 18px; padding: 6px 2px 16px; }
h4, p { margin: 0; }
h4 { font-size: 15px; }
p, small { color: var(--td-text-color-secondary); font-size: 12px; line-height: 1.55; }
.private-modes { display: grid; gap: 8px; }
.private-modes label { display: flex; align-items: center; gap: 10px; padding: 9px 12px; border: 1px solid var(--td-component-stroke); border-radius: var(--td-radius-medium); cursor: pointer; }
.private-modes label.selected { border-color: var(--td-brand-color); background: var(--td-brand-color-light); }
.private-modes input { accent-color: var(--td-brand-color); }
.private-modes small, .private-tool small { display: block; }
.private-list > label { font-weight: 500; }
.private-list > label small { margin-left: 5px; }
.private-divider { border-top: 1px solid var(--td-component-stroke); padding-top: 14px; }
.private-tools summary { cursor: pointer; }
.private-tools summary small { margin-left: 8px; }
.private-tools[open] summary { margin-bottom: 6px; }
.private-tool { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 6px 0; }
.private-tool strong { font-weight: 500; }
.private-tool input { appearance: none; flex: none; margin: 0; width: 30px; height: 17px;
  border: 1px solid var(--td-component-stroke); border-radius: 999px; background: var(--td-bg-color-secondarycontainer); cursor: pointer; }
.private-tool input::before { content: ''; display: block; margin: 2px; width: 11px; height: 11px;
  border-radius: 50%; background: var(--td-text-color-placeholder); }
.private-tool input:checked { background: var(--td-brand-color); border-color: var(--td-brand-color); }
.private-tool input:checked::before { transform: translateX(13px); background: var(--td-text-color-anti); }
.private-tool input:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: 2px; }
.preview-action { display: flex; gap: 8px; }
.preview-result { padding: 10px 12px; border-radius: var(--td-radius-medium); background: var(--td-bg-color-container-hover); }
.preview-result > span + span { margin-left: 8px; }
.preview-result p { margin-top: 6px; overflow-wrap: anywhere; }
</style>
