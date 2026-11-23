import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { compileStyle, parse } from 'vue/compiler-sfc'

test('BrandLogo applies dark-theme rules to the logo instead of the page root', () => {
  const filename = 'BrandLogo.vue'
  const { descriptor } = parse(readFileSync(new URL('./BrandLogo.vue', import.meta.url), 'utf8'), {
    filename,
  })
  const style = descriptor.styles[0]
  assert.ok(style?.scoped)

  const { code, errors } = compileStyle({
    filename,
    source: style.content,
    id: 'data-v-brand-logo',
    scoped: true,
  })
  assert.deepEqual(errors, [])
  assert.match(code, /html\[theme-mode="dark"\] \.brand-logo \.brand-logo__light\[data-v-brand-logo\]/)
  assert.match(code, /html\[theme-mode="dark"\] \.brand-logo \.brand-logo__dark\[data-v-brand-logo\]/)
  assert.doesNotMatch(code, /html\[theme-mode="dark"\]\s*[,\{]/)
})
