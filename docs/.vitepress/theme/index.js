/*
 * poisq Docs — Точка входа кастомной темы VitePress
 *
 * Что делает этот код и для чего он нужен:
 * Подключает тему по умолчанию VitePress поверх дизайн-системы «Мастеринг»
 * (tokens.css и studio.css) и добавляет в правый кластер навбара
 * моноширинный индикатор версии сервиса в стиле poisq Studio.
 */

import { h } from 'vue'
import DefaultTheme from 'vitepress/theme'
import './tokens.css'
import './studio.css'

/**
 * Моноширинный статус-бейдж версии сервиса в правом кластере навбара.
 */
function PoisqVersionBadge() {
  return h(
    'div',
    { class: 'poisq-nav-badge', title: 'Версия сервиса в internal/config/config.go' },
    [h('span', { class: 'poisq-nav-dot' }), h('span', null, 'v0.2.0')]
  )
}

export default {
  extends: DefaultTheme,
  Layout() {
    return h(DefaultTheme.Layout, null, {
      'nav-bar-content-after': () => h(PoisqVersionBadge)
    })
  }
}
