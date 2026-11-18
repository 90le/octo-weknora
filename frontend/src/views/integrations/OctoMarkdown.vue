<template>
  <div v-if="text" class="octo-markdown" :class="{ compact }" v-html="html"></div>
  <p v-else class="octo-markdown-empty">{{ empty }}</p>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { safeMarkdownToHTML } from '@/utils/security'

const props = defineProps<{ text?: string; compact?: boolean; empty?: string }>()
const html = computed(() => safeMarkdownToHTML(props.text || ''))
</script>

<style scoped>
.octo-markdown { overflow-wrap: anywhere; overflow-x: auto; line-height: 1.55; }
.octo-markdown.compact { max-height: 4.7em; overflow: hidden; }
.octo-markdown :deep(p), .octo-markdown-empty { margin: 0 0 .45em; }
.octo-markdown :deep(pre) { overflow: auto; padding: .75em; background: var(--td-bg-color-secondarycontainer); }
.octo-markdown :deep(table) { border-collapse: collapse; }
.octo-markdown :deep(th), .octo-markdown :deep(td) { border: 1px solid var(--td-component-border); padding: .35em .6em; }
.octo-markdown :deep(img) { max-width: 100%; }
</style>
