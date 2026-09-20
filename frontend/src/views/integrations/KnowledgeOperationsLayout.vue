<template>
  <main class="knowledge-operations-page">
    <header class="page-heading">
      <slot name="breadcrumb" />
      <h1>{{ title }}</h1>
      <p>{{ description }}</p>
    </header>
    <slot name="navigation" />
    <section class="page-body"><slot /></section>
  </main>
</template>
<script setup lang="ts">
defineProps<{ title: string; description: string }>()
</script>
<style scoped lang="less">
.knowledge-operations-page{height:100%;min-width:0;overflow:auto;background:var(--td-bg-color-container);color:var(--td-text-color-primary);padding:32px 40px;box-sizing:border-box}
.page-heading{margin-bottom:24px;h1{font-size:24px;font-weight:600;line-height:1.4;margin:0 0 8px}p{color:var(--td-text-color-secondary);font-size:14px;line-height:1.6;margin:0}}
.page-body{padding-top:24px;min-width:0}
:deep(.operations-navigation){display:flex;gap:28px;border-bottom:1px solid var(--td-component-border);margin:0;overflow-x:auto}
:deep(.operations-navigation a){padding:12px 2px;color:var(--td-text-color-secondary);text-decoration:none;white-space:nowrap;border-bottom:2px solid transparent;font-size:14px}
:deep(.operations-navigation a[aria-current="page"]){color:var(--td-brand-color);border-bottom-color:var(--td-brand-color);font-weight:600}
:deep(a:focus-visible),:deep(button:focus-visible){outline:2px solid var(--td-brand-color);outline-offset:3px}
@media(max-width:960px){.knowledge-operations-page{padding:24px}}
@media(max-width:640px){.knowledge-operations-page{padding:20px 16px}.page-heading h1{font-size:22px}}
</style>
