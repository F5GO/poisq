/*
 * poisq Docs — Конфигурация VitePress
 *
 * Что делает этот код и для чего он нужен:
 * Настраивает структуру разделов документации сервиса poisq, русскую локализацию
 * интерфейса и локального поиска, подключение шрифтов IBM Plex Sans / IBM Plex Mono
 * и фавиконов из бренд-ресурсов poisq Studio.
 */

import { defineConfig } from 'vitepress'

export default defineConfig({
  lang: 'ru-RU',
  title: 'poisq',
  description: 'Документация сервиса poisq: умный Subsonic-прокси, Studio, тексты песен, биографии артистов и трансляция Cast.',
  cleanUrls: true,
  lastUpdated: false,

  head: [
    ['meta', { name: 'theme-color', content: '#EBEBEB', media: '(prefers-color-scheme: light)' }],
    ['meta', { name: 'theme-color', content: '#0E0E0E', media: '(prefers-color-scheme: dark)' }],
    ['link', { rel: 'icon', type: 'image/png', sizes: '32x32', href: '/brand/favicon-32x32.png' }],
    ['link', { rel: 'icon', type: 'image/png', sizes: '16x16', href: '/brand/favicon-16x16.png' }],
    ['link', { rel: 'apple-touch-icon', sizes: '180x180', href: '/brand/apple-touch-icon.png' }],
    ['link', { rel: 'shortcut icon', href: '/brand/favicon.ico' }],
    ['link', { rel: 'preconnect', href: 'https://fonts.googleapis.com' }],
    ['link', { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: '' }],
    [
      'link',
      {
        rel: 'stylesheet',
        href: 'https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=IBM+Plex+Sans:wght@400;500;600&display=swap'
      }
    ]
  ],

  themeConfig: {
    logo: {
      light: '/brand/mark-light.svg',
      dark: '/brand/mark-dark.svg',
      alt: 'poisq'
    },
    siteTitle: '<span class="poisq-brand-text"><span class="poisq-brand-name">poisq</span><span class="poisq-brand-sub">docs</span></span>',

    nav: [
      { text: 'Старт', link: '/guide/overview', activeMatch: '/guide/' },
      { text: 'Подсистемы', link: '/architecture/search-proxy', activeMatch: '/architecture/' },
      { text: 'Studio и клиенты', link: '/studio/web-ui', activeMatch: '/studio/' },
      { text: 'Справочник', link: '/reference/api', activeMatch: '/reference/' }
    ],

    sidebar: [
      {
        text: 'Начало работы',
        items: [
          { text: 'Обзор и архитектура', link: '/guide/overview' },
          { text: 'Установка и запуск', link: '/guide/deployment' },
          { text: 'Конфигурация', link: '/guide/configuration' }
        ]
      },
      {
        text: 'Подсистемы сервиса',
        items: [
          { text: 'Умный поиск и прокси (:4534)', link: '/architecture/search-proxy' },
          { text: 'Тексты песен и Aligner', link: '/architecture/lyrics-aligner' },
          { text: 'Биографии артистов', link: '/architecture/biographies' },
          { text: 'Аудит, обложки и теггер', link: '/architecture/library-tagger' },
          { text: 'Трансляция Cast (MA)', link: '/architecture/cast' },
          { text: 'ИИ-слой и пул API-ключей', link: '/architecture/ai-keypool' }
        ]
      },
      {
        text: 'Интерфейс и клиенты',
        items: [
          { text: 'poisq Studio (:4535)', link: '/studio/web-ui' },
          { text: 'iOS-клиент, Chrome и AlignEval', link: '/studio/clients-tools' }
        ]
      },
      {
        text: 'Справочник',
        items: [
          { text: 'HTTP API', link: '/reference/api' },
          { text: 'Базы данных и безопасность', link: '/reference/storage-security' }
        ]
      }
    ],

    outline: {
      level: [2, 3],
      label: 'На этой странице'
    },

    docFooter: {
      prev: 'Назад',
      next: 'Далее'
    },

    darkModeSwitchLabel: 'Тема',
    lightModeSwitchTitle: 'Переключить на светлую тему',
    darkModeSwitchTitle: 'Переключить на тёмную тему',
    sidebarMenuLabel: 'Разделы',
    returnToTopLabel: 'Наверх',
    langMenuLabel: 'Язык',

    search: {
      provider: 'local',
      options: {
        translations: {
          button: {
            buttonText: 'Поиск по документации',
            buttonAriaLabel: 'Поиск по документации'
          },
          modal: {
            displayDetails: 'Показать подробности',
            resetButtonTitle: 'Сбросить поиск',
            backButtonTitle: 'Закрыть поиск',
            noResultsText: 'Ничего не найдено по запросу',
            footer: {
              selectText: 'выбрать',
              navigateText: 'перейти',
              closeText: 'закрыть'
            }
          }
        }
      }
    },

    footer: {
      message: 'Документация составлена по исходному коду репозитория poisq',
      copyright: 'poisq v0.2.0'
    }
  }
})
