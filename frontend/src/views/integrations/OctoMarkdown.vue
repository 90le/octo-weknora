<template>
  <p v-if="compact && text" class="octo-markdown-summary">{{ summary || empty }}</p>
  <div v-else-if="text" class="octo-markdown" v-html="html"></div>
  <p v-else class="octo-markdown-empty">{{ empty }}</p>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Marked } from 'marked'
import { escapeHTML, sanitizeHTML } from '@/utils/security'

const markdown = new Marked({
  breaks: true,
  renderer: {
    html: ({ text }) => escapeHTML(text),
    image: ({ text }) => escapeHTML(text),
  },
})

const props = defineProps<{ text?: string; compact?: boolean; empty?: string }>()
const html = computed(() => sanitizeHTML(markdown.parse(props.text || '', { async: false })))
const summary = computed(() => {
  const { body } = new DOMParser().parseFromString(html.value, 'text/html')
  const excerpt = body.querySelector('p, li, tbody tr') ?? body.firstElementChild ?? body
  const text = excerpt.matches('tr')
    ? Array.from(excerpt.children, cell => cell.textContent).join(' · ')
    : excerpt.textContent
  return text?.replace(/\s+/g, ' ').trim() ?? ''
})
</script>

<style scoped>
.octo-markdown { overflow-wrap: anywhere; overflow-x: auto; line-height: 1.55; }
.octo-markdown-summary {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  overflow: hidden;
  overflow-wrap: anywhere;
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: var(--td-font-size-body-medium);
  line-height: 1.5;
}
.octo-markdown :deep(p), .octo-markdown-empty { margin: 0 0 .45em; }
.octo-markdown-empty { color: var(--td-text-color-secondary); }
.octo-markdown :deep(pre) { overflow: auto; padding: .75em; background: var(--td-bg-color-secondarycontainer); }
.octo-markdown :deep(table) { border-collapse: collapse; }
.octo-markdown :deep(th), .octo-markdown :deep(td) { border: 1px solid var(--td-component-border); padding: .35em .6em; }
.octo-markdown :deep(img) { max-width: 100%; }
</style>
