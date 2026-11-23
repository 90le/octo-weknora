<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  mark?: boolean
  inverse?: boolean
  decorative?: boolean
}>()

const name = computed(() => props.mark ? 'yewen-mark' : 'yewen-horizontal')
</script>

<template>
  <span class="brand-logo" :class="{ 'brand-logo--inverse': inverse }">
    <img class="brand-logo__light" :src="`/brand/${name}.svg`" :alt="decorative ? '' : '业问'" />
    <img class="brand-logo__dark" :src="`/brand/${name}-dark.svg`" :alt="decorative ? '' : '业问'" />
  </span>
</template>

<style scoped>
.brand-logo { display: inline-flex; flex: 0 0 auto; height: var(--brand-logo-height, 24px); }
.brand-logo img { display: block; height: 100%; width: auto; max-width: none; }
.brand-logo .brand-logo__dark { display: none; }
:global(html[theme-mode="dark"]) .brand-logo .brand-logo__light,
.brand-logo--inverse .brand-logo__light { display: none; }
:global(html[theme-mode="dark"]) .brand-logo .brand-logo__dark,
.brand-logo--inverse .brand-logo__dark { display: block; }
</style>
