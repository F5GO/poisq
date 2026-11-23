---
layout: home
title: Документация

hero:
  name: poisq
  text: Умный прокси и медиастудия для Navidrome
  tagline: Поиск с исправлением раскладки, транслита и опечаток, синхронизированные тексты песен, проверенные биографии на русском, аудит тегов и трансляция на колонки через Music Assistant.
  actions:
    - theme: brand
      text: Обзор и архитектура
      link: /guide/overview
    - theme: alt
      text: Установка и запуск
      link: /guide/deployment
    - theme: alt
      text: HTTP API
      link: /reference/api

features:
  - title: "Умный поиск (:4534)"
    details: "Перехватывает Subsonic search/search2/search3 и Native API Navidrome. Исправляет раскладку, транслитерацию, опечатки (Дамерау–Левенштейн) и разговорные алиасы, объединяет до 4 параллельных запросов в единый JSON/XML."
    link: /architecture/search-proxy
  - title: "Тексты и караоке"
    details: "Каскадный турнир по LRCLIB, Яндекс Музыке, NetEase и Genius с кластерным консенсусом. Для треков без таймкодов — облачное выравнивание (Groq Whisper) или локальный sidecar poisq-aligner (Demucs + Qwen3 + MMS)."
    link: /architecture/lyrics-aligner
  - title: "Биографии на русском"
    details: "Перехватывает getArtistInfo2, подтверждает личность артиста через MusicBrainz/Яндекс, собирает факты из 7 каталогов и Tavily, синтезирует проверенный текст на русском и даёт приоритет ручным правкам."
    link: /architecture/biographies
  - title: "Аудит, обложки и теггер"
    details: "Синхронизирует каталог из navidrome.db, находит битую кодировку CP1251 и низкие обложки, подбирает 3000×3000 из iTunes/Cover Art Archive и атомарно записывает теги через ffmpeg с MD5-проверкой для FLAC."
    link: /architecture/library-tagger
  - title: "Трансляция Cast"
    details: "Расширение OpenSubsonic poisq (версия 1) и эндпоинты /rest/poisqCast* для вывода очереди треков Navidrome на колонки Music Assistant с фоновой догрузкой длинных плейлистов."
    link: /architecture/cast
  - title: "poisq Studio (:4535)"
    details: "Встроенная в бинарник веб-панель управления фонотекой: визуальный редактор таймингов на волне громкости, буфер исправлений с Undo, управление очередью задач, биографиями и шифрованным пулом API-ключей."
    link: /studio/web-ui
---

<div class="poisq-ports-strip">
  <div class="poisq-ports-grid">
    <div class="poisq-port-card">
      <div class="poisq-port-head">
        <span class="poisq-port-num">:4534</span>
        <span class="poisq-port-tag">Публичный</span>
      </div>
      <p class="poisq-port-desc">Прозрачный Subsonic/OpenSubsonic-прокси перед Navidrome: умный поиск, биографии артистов, расширение <code>poisq</code> и трансляция Cast.</p>
    </div>
    <div class="poisq-port-card">
      <div class="poisq-port-head">
        <span class="poisq-port-num">:4535</span>
        <span class="poisq-port-tag">Внутренний</span>
      </div>
      <p class="poisq-port-desc">Веб-интерфейс <strong>poisq Studio</strong> и REST API v1 для фонотеки, редактора LRC, Staging-правок, биографий, пула ключей и настроек.</p>
    </div>
    <div class="poisq-port-card">
      <div class="poisq-port-head">
        <span class="poisq-port-num">:4540</span>
        <span class="poisq-port-tag">Опционально</span>
      </div>
      <p class="poisq-port-desc">Python-сервис <strong>poisq-aligner</strong>: разделение вокала (Demucs), ансамбль Whisper, Qwen3-ForcedAligner и оценка уверенности каждой строки.</p>
    </div>
  </div>
</div>
