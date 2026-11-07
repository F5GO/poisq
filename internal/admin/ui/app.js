/**
 * poisq Studio v0.2.0 — Минималистичный контроллер веб-интерфейса
 * 
 * Назначение:
 * Обеспечивает полноценное взаимодействие панели управления (:4535) с REST API бэкенда:
 * 1. Фонотека:
 *    - Режимы просмотра: "Все треки", "Исполнители", "Альбомы".
 *    - Быстрые фильтры по текстам (Все, Синхронные, Обычный текст, Без текста, Mojibake, Без обложки).
 *    - Drawer (боковая шторка) подробной информации о треке по клику на строку.
 *    - Статусы аудита и очереди (Пройден, В очереди, Обработка...) с мягкой индикацией.
 *    - Отдельная кнопка «Караоке» для мгновенного перехода к песне.
 * 2. Караоке и LRC Менеджер:
 *    - Сайдбар со списком всех песен фонотеки, живым поиском и статусами текстов.
 *    - Реальное воспроизведение аудиофайлов через HTML5 Audio и бэкенд-стриминг (/api/v1/tracks/stream).
 *    - Плавный Apple-style суфлер, синхронизированный с аудиодорожкой по таймкодам [mm:ss.xx].
 *    - Ручной редактор .lrc с атомарным сохранением прямо в папку с аудиофайлом.
 *    - Запуск онлайн-турнира (LRCLIB/Genius/Yandex) и AI-караоке (Groq Whisper Large v3 + Gemini).
 * 3. Staging-буфер рефакторинга:
 *    - Наглядное side-by-side сравнение обложек (Текущая из папки vs Найденая в сети 3000x3000 HD).
 *    - Защита от ухудшения качества: предложения только действительно лучших обложек.
 *    - Чистый диф тегов: отображаются только измененные поля без лишних перечеркиваний.
 * 4. Очередь фоновых задач (Jobs):
 *    - Мониторинг задач воркера в реальном времени, пауза и возобновление очереди.
 * 5. Пул API-ключей (AES-256-GCM):
 *    - Маскированный ввод без спама автозаполнения браузерных менеджеров паролей.
 */

// Глобальное состояние веб-интерфейса
const state = {
  activeTab: "library",
  viewMode: "tracks",     // "tracks" | "artists" | "albums"
  filterMode: "all",      // "all" | "synced" | "plain" | "none" | "mojibake" | "no_cover"
  searchQuery: "",

  tracks: [],
  selectedTrack: null,
  stagingItems: [],
  keys: [],
  jobs: [],
  jobLogs: {},
  expandedJobIds: new Set(),
  jobsPollTimer: null,
  workerPaused: false,
  lastAppliedStagingId: null,

  // Караоке плеер
  audioEl: null,
  isPlaying: false,
  currentSec: 0,
  totalSec: 0,
  lyrics: [],
  activeLyricIndex: -1,
  editorMode: "karaoke",

  // Дебаунс
  searchDebounceTimer: null,
  karaokeSearchTimer: null
};

// -----------------------------------------------------------------------------
// Вспомогательные утилиты и Toast-уведомления
// -----------------------------------------------------------------------------

async function fetchApi(endpoint, method = "GET", payload = null, timeoutMs = 20000) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);

  const options = {
    method,
    headers: { "Content-Type": "application/json" },
    signal: controller.signal
  };
  if (payload !== null) {
    options.body = JSON.stringify(payload);
  }

  try {
    const res = await fetch("/api/v1" + endpoint, options);
    clearTimeout(timer);
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      const errorMsg = data.error || `HTTP ${res.status}: ${res.statusText || "Ошибка сервера"}`;
      throw new Error(errorMsg);
    }
    return data;
  } catch (err) {
    clearTimeout(timer);
    if (err.name === "AbortError") {
      throw new Error("Таймаут запроса к серверу (сервер занят обработкой)");
    }
    throw err;
  }
}

function showToast(message, type = "info") {
  const container = document.getElementById("toast-container");
  if (!container) return;

  const toast = document.createElement("div");
  toast.className = `toast toast-${type}`;
  toast.textContent = message;

  if (type === "error") {
    toast.style.borderColor = "var(--color-crimson, #f43f5e)";
    toast.style.color = "var(--color-red)";
  } else if (type === "success") {
    toast.style.borderColor = "var(--color-emerald, #10b981)";
    toast.style.color = "var(--color-emerald)";
  }

  container.appendChild(toast);

  setTimeout(() => {
    toast.style.opacity = "0";
    toast.style.transform = "translateY(8px)";
    toast.style.transition = "all 0.3s ease";
    setTimeout(() => toast.remove(), 300);
  }, 4000);
}

function escapeHtml(str) {
  if (!str) return "";
  return String(str)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#039;");
}

function formatDuration(sec) {
  const sTotal = Math.round(sec);
  const m = Math.floor(sTotal / 60);
  const s = Math.floor(sTotal % 60);
  return `${m < 10 ? "0" : ""}${m}:${s < 10 ? "0" : ""}${s}`;
}

function parseLRC(lrcText) {
  if (!lrcText) return [];
  const lines = [];
  const tagRegex = /\[(\d{1,2}):(\d{2})(?:\.(\d{2,3}))?\](.*)/;

  for (const raw of lrcText.split("\n")) {
    const trimmed = raw.trim();
    if (!trimmed) continue;
    const match = trimmed.match(tagRegex);
    if (match) {
      const min = parseInt(match[1], 10);
      const sec = parseInt(match[2], 10);
      const ms = match[3] ? parseInt(match[3].padEnd(3, "0").slice(0, 3), 10) : 0;
      const totalSec = min * 60 + sec + ms / 1000;
      const text = match[4].trim();
      if (text) {
        lines.push({ sec: totalSec, text });
      }
    }
  }

  return lines.sort((a, b) => a.sec - b.sec);
}

// -----------------------------------------------------------------------------
// Инициализация при загрузке документа
// -----------------------------------------------------------------------------
document.addEventListener("DOMContentLoaded", () => {
  state.audioEl = document.getElementById("audio-stream");

  initTheme();
  initNavigation();
  initViewControls();
  initPlayerControls();
  initDrawer();
  initKeyModal();
  initCastForm();
  loadLibraryData();
  loadKeysData();
});

// -----------------------------------------------------------------------------
// 0. Управление цветовой темой и брендингом
// -----------------------------------------------------------------------------
function initTheme() {
  const savedTheme = localStorage.getItem("poisq_theme") || "light";
  applyTheme(savedTheme);

  const toggleBtn = document.getElementById("btn-toggle-theme");
  if (toggleBtn) {
    toggleBtn.addEventListener("click", () => {
      const currentTheme = document.documentElement.getAttribute("data-theme") || "light";
      const newTheme = currentTheme === "dark" ? "light" : "dark";
      applyTheme(newTheme);
      localStorage.setItem("poisq_theme", newTheme);
    });
  }

  const brandHomeLink = document.getElementById("brand-home-link");
  if (brandHomeLink) {
    brandHomeLink.addEventListener("click", () => {
      switchTab("library");
    });
  }
}

function applyTheme(theme) {
  document.documentElement.setAttribute("data-theme", theme);
  const sunIcon = document.getElementById("theme-icon-sun");
  const moonIcon = document.getElementById("theme-icon-moon");
  if (theme === "dark") {
    sunIcon?.classList.remove("hidden");
    moonIcon?.classList.add("hidden");
  } else {
    sunIcon?.classList.add("hidden");
    moonIcon?.classList.remove("hidden");
  }
}

// -----------------------------------------------------------------------------
// 1. Навигация и переключение вкладок
// -----------------------------------------------------------------------------
function initNavigation() {
  document.querySelectorAll(".nav-tab").forEach(tab => {
    tab.addEventListener("click", () => {
      const tabName = tab.dataset.tab;
      switchTab(tabName);
    });
  });

  // Действия в медиатеке
  document.getElementById("btn-sync-navidrome")?.addEventListener("click", syncNavidrome);
  document.getElementById("btn-run-audit")?.addEventListener("click", runMusicBrainzAudit);

  // Staging действия
  document.getElementById("btn-reset-db")?.addEventListener("click", resetFullDatabase);
  document.getElementById("btn-rescan-audit")?.addEventListener("click", rescanStagingAudit);
  document.getElementById("btn-apply-all-staging")?.addEventListener("click", applyAllStaging);
  document.getElementById("btn-discard-all-staging")?.addEventListener("click", discardAllStaging);
  document.getElementById("btn-undo-staging")?.addEventListener("click", undoStaging);

  // Караоке действия
  document.getElementById("btn-create-ai-lrc")?.addEventListener("click", handleCreateAiLrc);
  document.getElementById("btn-enqueue-tournament")?.addEventListener("click", enqueueLrcTournament);
  document.getElementById("btn-cloud-karaoke")?.addEventListener("click", enqueueWhisperKaraoke);
  document.getElementById("btn-toggle-editor")?.addEventListener("click", toggleLrcEditor);
  document.getElementById("btn-delete-lyrics")?.addEventListener("click", () => {
    if (state.selectedTrack) {
      deleteTrackLyrics(state.selectedTrack.id);
    }
  });
  document.getElementById("btn-save-raw-lrc")?.addEventListener("click", saveRawLrc);

  // Фоновые задачи
  document.getElementById("btn-toggle-worker-pause")?.addEventListener("click", toggleWorkerPause);
  document.getElementById("btn-refresh-jobs")?.addEventListener("click", loadJobsData);

  // Инициализация Lightbox для обложек
  initCoverLightboxEvents();
}

function switchTab(tabName) {
  state.activeTab = tabName;
  document.querySelectorAll(".nav-tab").forEach(tab => {
    tab.classList.toggle("active", tab.dataset.tab === tabName);
  });

  document.querySelectorAll(".tab-panel").forEach(panel => {
    panel.classList.toggle("active", panel.id === `panel-${tabName}`);
  });

  if (state.jobsPollTimer) {
    clearInterval(state.jobsPollTimer);
    state.jobsPollTimer = null;
  }

  if (tabName === "library") loadLibraryData();
  if (tabName === "karaoke") {
    renderKaraokeSidebar();
  }
  if (tabName === "staging") loadStagingData();
  if (tabName === "jobs") {
    loadJobsData();
    state.jobsPollTimer = setInterval(() => {
      if (state.activeTab === "jobs") {
        loadJobsData();
      }
    }, 2500);
  }
  if (tabName === "keys") {
    loadKeysData();
    loadCastSettings();
  }
}

// -----------------------------------------------------------------------------
// 2. Медиатека: режимы просмотра (Все треки, Исполнители, Альбомы), фильтры
// -----------------------------------------------------------------------------
function initViewControls() {
  // Селектор режима (Все треки / Исполнители / Альбомы)
  document.querySelectorAll("#view-mode-selector .seg-btn").forEach(btn => {
    btn.addEventListener("click", () => {
      document.querySelectorAll("#view-mode-selector .seg-btn").forEach(b => b.classList.remove("active"));
      btn.classList.add("active");
      state.viewMode = btn.dataset.view;
      renderCurrentLibraryView();
    });
  });

  // Фильтры по текстам
  document.querySelectorAll("#lyrics-filter-pills .filter-pill").forEach(pill => {
    pill.addEventListener("click", () => {
      document.querySelectorAll("#lyrics-filter-pills .filter-pill").forEach(p => p.classList.remove("active"));
      pill.classList.add("active");
      state.filterMode = pill.dataset.filter;
      loadTracks();
    });
  });

  // Поиск по медиатеке с дебаунсом и кнопкой сброса
  const searchInput = document.getElementById("track-search-input");
  const clearBtn = document.getElementById("btn-clear-track-search");

  const updateClearBtn = () => {
    if (!clearBtn) return;
    if (searchInput && searchInput.value.trim().length > 0) {
      clearBtn.classList.remove("hidden");
    } else {
      clearBtn.classList.add("hidden");
    }
  };

  searchInput?.addEventListener("input", (e) => {
    updateClearBtn();
    clearTimeout(state.searchDebounceTimer);
    state.searchDebounceTimer = setTimeout(() => {
      state.searchQuery = e.target.value.trim();
      loadTracks();
    }, 250);
  });

  searchInput?.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      clearTrackSearch();
    }
  });

  clearBtn?.addEventListener("click", () => {
    clearTrackSearch();
  });

  // Поиск в сайдбаре караоке
  document.getElementById("karaoke-search-input")?.addEventListener("input", (e) => {
    clearTimeout(state.karaokeSearchTimer);
    state.karaokeSearchTimer = setTimeout(async () => {
      const text = e.target.value.trim();
      if (!text) {
        state.karaokeSearchResults = null;
        renderKaraokeSidebar();
        return;
      }
      // Умный поиск на сервере: регистр, МакSим/Максим/Maksim, раскладка, опечатки
      try {
        const res = await fetchApi(`/tracks?limit=250&q=${encodeURIComponent(text)}`);
        state.karaokeSearchResults = res.tracks || [];
      } catch (err) {
        state.karaokeSearchResults = null;
      }
      renderKaraokeSidebar(text);
    }, 200);
  });
}

async function loadLibraryData() {
  try {
    const health = await fetchApi("/health");
    const stats = health.stats || {};
    const total = stats.totalTracks || 0;
    const synced = (stats.lyricsSynced || 0) + (stats.lyricsVerified || 0);
    const pct = total > 0 ? Math.round((synced / total) * 100) : 0;
    const stagingCount = stats.pendingStaging || 0;
    const jobsCount = stats.pendingJobs || 0;

    const summary = document.getElementById("library-summary-desc");
    if (summary) {
      summary.innerHTML = `Каталог <span class="mono text-light">/music</span> · ${total} треков · ${pct}% с синхронными LRC · ${stagingCount} в Staging`;
    }
    const badgeStaging = document.getElementById("badge-staging-count");
    if (badgeStaging) badgeStaging.textContent = stagingCount;

    const badgeJobs = document.getElementById("badge-jobs-count");
    if (badgeJobs) badgeJobs.textContent = jobsCount;

    await loadTracks();
  } catch (err) {
    console.warn("Ошибка получения статистики фонотеки:", err);
    await loadTracks();
  }
}

async function loadTracks() {
  try {
    let endpoint = `/tracks?limit=250`;
    if (state.filterMode && state.filterMode !== "all") {
      endpoint += `&status=${encodeURIComponent(state.filterMode)}`;
    }
    if (state.searchQuery) {
      endpoint += `&q=${encodeURIComponent(state.searchQuery)}`;
    }

    const res = await fetchApi(endpoint);
    state.tracks = res.tracks || [];

    const countLabel = document.getElementById("tracks-count-label");
    if (countLabel) countLabel.textContent = `Показано: ${state.tracks.length} треков`;

    renderCurrentLibraryView();
    renderKaraokeSidebar();
  } catch (err) {
    console.error("Ошибка загрузки треков:", err);
    const content = document.getElementById("library-view-content");
    if (content) {
      content.innerHTML = `<div class="cell-empty" style="color: var(--color-crimson);">Не удалось загрузить треки: ${escapeHtml(err.message)}</div>`;
    }
  }
}

function renderCurrentLibraryView() {
  const container = document.getElementById("library-view-content");
  if (!container) return;

  if (state.tracks.length === 0) {
    container.innerHTML = `<div class="cell-empty">В медиатеке нет подходящих треков. Нажмите «Синхронизация Navidrome» для сканирования.</div>`;
    return;
  }

  if (state.viewMode === "tracks") {
    renderTracksTable(container, state.tracks);
  } else if (state.viewMode === "artists") {
    renderArtistsGrid(container, state.tracks);
  } else if (state.viewMode === "albums") {
    renderAlbumsGrid(container, state.tracks);
  }
}

function renderTracksTable(container, tracks) {
  container.innerHTML = `
    <div class="table-wrapper">
      <table class="data-table">
        <thead>
          <tr>
            <th class="col-num mono">#</th>
            <th>Трек</th>
            <th>Альбом</th>
            <th>Длительность</th>
            <th>Текст</th>
            <th>Аудит</th>
          </tr>
        </thead>
        <tbody id="tracks-tbody"></tbody>
      </table>
    </div>
  `;

  const tbody = document.getElementById("tracks-tbody");
  tbody.innerHTML = tracks.map((t, idx) => {
    const num = (idx + 1 < 10 ? "0" : "") + (idx + 1);

    // Интерактивный статус текста песни (клик сразу открывает караоке-плеер)
    let statusBadge = `<span class="badge-lyric-btn none" onclick="event.stopPropagation(); openTrackInKaraoke('${escapeHtml(t.id)}')">+ Найти текст</span>`;
    if (t.lyricsStatus === "verified") {
      statusBadge = `<span class="badge-lyric-btn verified" onclick="event.stopPropagation(); openTrackInKaraoke('${escapeHtml(t.id)}')">Проверен</span>`;
    } else if (t.lyricsStatus === "synced") {
      statusBadge = `<span class="badge-lyric-btn synced" onclick="event.stopPropagation(); openTrackInKaraoke('${escapeHtml(t.id)}')">Синхронный</span>`;
    } else if (t.lyricsStatus === "plain") {
      statusBadge = `<span class="badge-lyric-btn plain" onclick="event.stopPropagation(); openTrackInKaraoke('${escapeHtml(t.id)}')">Обычный</span>`;
    }

    // Честный статус аудита медиатеки
    let auditBadge = `<span class="badge-audit-ok">✓ Готов</span>`;
    let rowClass = "";
    if (t.isMojibake) {
      auditBadge = `<span class="badge-audit-queued">Mojibake</span>`;
    } else if (!t.hasCover || (t.coverWidth > 0 && t.coverWidth < 500)) {
      auditBadge = `<span class="badge-audit-queued">Нужна обложка</span>`;
    } else if (t.lyricsStatus === "none" || !t.lyricsStatus) {
      auditBadge = `<span class="badge-audit-queued">Нужен текст</span>`;
    } else if (t.lyricsStatus === "plain") {
      auditBadge = `<span class="badge-audit-queued">⏱ Нужен LRC</span>`;
    }

    return `
      <tr class="${rowClass}" onclick="openTrackDrawer('${escapeHtml(t.id)}')" style="cursor: pointer;">
        <td class="col-num mono">${num}</td>
        <td>
          <div style="font-weight: 600; color: var(--text-primary);">${escapeHtml(t.title || "Без названия")}</div>
          <div style="font-size: 11px; color: var(--text-muted);">${escapeHtml(t.artist || "Неизвестный артист")}</div>
        </td>
        <td style="color: var(--text-secondary);">${escapeHtml(t.album || "—")}</td>
        <td class="mono" style="color: var(--text-muted);">${formatDuration(t.duration || 0)}</td>
        <td>${statusBadge}</td>
        <td>${auditBadge}</td>
      </tr>
    `;
  }).join("");
}

function renderArtistsGrid(container, tracks) {
  const map = new Map();
  for (const t of tracks) {
    const artist = t.artist || "Неизвестный исполнитель";
    map.set(artist, (map.get(artist) || 0) + 1);
  }

  const artists = Array.from(map.entries()).sort((a, b) => a[0].localeCompare(b[0]));
  container.innerHTML = `
    <div class="artist-grid">
      ${artists.map(([name, count]) => `
        <div class="artist-card" onclick="filterByArtist('${escapeHtml(name)}')">
          <div class="artist-avatar-wrap">
            <img 
              src="/api/v1/navidrome/artist-image?artist=${encodeURIComponent(name)}" 
              class="artist-avatar-img" 
              alt="${escapeHtml(name)}" 
              loading="lazy" 
              onerror="this.style.display='none'; const p = this.nextElementSibling; if(p) p.style.display='flex';"
            />
            <div class="artist-avatar-placeholder" style="display:none;">♪</div>
          </div>
          <div class="group-title" style="text-align: center;">${escapeHtml(name)}</div>
          <div class="group-desc" style="text-align: center;">${count} треков в медиатеке</div>
        </div>
      `).join("")}
    </div>
  `;
}

function clearTrackSearch() {
  const searchInput = document.getElementById("track-search-input");
  const clearBtn = document.getElementById("btn-clear-track-search");
  if (searchInput) {
    searchInput.value = "";
    searchInput.focus();
  }
  if (clearBtn) {
    clearBtn.classList.add("hidden");
  }
  state.searchQuery = "";
  loadTracks();
}

function renderAlbumsGrid(container, tracks) {
  const map = new Map();
  for (const t of tracks) {
    const key = (t.artist || "") + " — " + (t.album || "Без альбома");
    if (!map.has(key)) {
      map.set(key, {
        album: t.album || "Без альбома",
        artist: t.artist || "Неизвестный исполнитель",
        count: 0,
        folderPath: t.folderPath,
        hasCover: t.hasCover
      });
    }
    map.get(key).count++;
  }

  const albums = Array.from(map.values()).sort((a, b) => a.album.localeCompare(b.album));
  container.innerHTML = `
    <div class="album-grid">
      ${albums.map(alb => {
        const coverSrc = `/api/v1/navidrome/album-cover?album=${encodeURIComponent(alb.album)}&artist=${encodeURIComponent(alb.artist)}&path=${encodeURIComponent(alb.folderPath || "")}`;
        return `
          <div class="album-card" onclick="filterByAlbum('${escapeHtml(alb.album)}')">
            <img 
              src="${coverSrc}" 
              class="album-cover-img" 
              alt="Обложка" 
              loading="lazy" 
              onerror="this.style.display='none'; const p = this.nextElementSibling; if(p) p.style.display='flex';"
            />
            <div class="album-cover-img" style="display:none;align-items:center;justify-content:center;color:var(--text-muted);font-size:24px;">♪</div>
            <div class="group-title">${escapeHtml(alb.album)}</div>
            <div class="group-desc">${escapeHtml(alb.artist)} · ${alb.count} треков</div>
          </div>
        `;
      }).join("")}
    </div>
  `;
}

function filterByArtist(artist) {
  const input = document.getElementById("track-search-input");
  if (input) input.value = artist;
  const clearBtn = document.getElementById("btn-clear-track-search");
  if (clearBtn) clearBtn.classList.remove("hidden");

  state.searchQuery = artist;
  state.viewMode = "tracks";
  document.querySelectorAll("#view-mode-selector .seg-btn").forEach(b => b.classList.toggle("active", b.dataset.view === "tracks"));
  loadTracks();
}

function filterByAlbum(album) {
  const input = document.getElementById("track-search-input");
  if (input) input.value = album;
  const clearBtn = document.getElementById("btn-clear-track-search");
  if (clearBtn) clearBtn.classList.remove("hidden");

  state.searchQuery = album;
  state.viewMode = "tracks";
  document.querySelectorAll("#view-mode-selector .seg-btn").forEach(b => b.classList.toggle("active", b.dataset.view === "tracks"));
  loadTracks();
}

// -----------------------------------------------------------------------------
// 3. Drawer подробной информации о треке
// -----------------------------------------------------------------------------
function initDrawer() {
  const drawer = document.getElementById("drawer-track-detail");
  const closeBtn = document.getElementById("btn-close-drawer");

  closeBtn?.addEventListener("click", () => drawer?.classList.add("hidden"));
  drawer?.addEventListener("click", (e) => {
    if (e.target === drawer) drawer.classList.add("hidden");
  });
}

async function openTrackDrawer(trackId) {
  const track = state.tracks.find(t => t.id === trackId);
  if (!track) return;

  const drawer = document.getElementById("drawer-track-detail");
  const titleEl = document.getElementById("drawer-track-title");
  const bodyEl = document.getElementById("drawer-track-body");

  if (titleEl) titleEl.textContent = track.title || "Без названия";

  const coverUrl = `/api/v1/navidrome/album-cover?album=${encodeURIComponent(track.album || "")}&artist=${encodeURIComponent(track.artist || "")}&path=${encodeURIComponent(track.folderPath || "")}&trackId=${encodeURIComponent(track.id || "")}`;

  let lyricsStatusDesc = "Отсутствует";
  if (track.lyricsStatus === "verified") lyricsStatusDesc = "✓ Проверен";
  else if (track.lyricsStatus === "synced") lyricsStatusDesc = "Синхронизирован (.lrc)";
  else if (track.lyricsStatus === "plain") lyricsStatusDesc = "Обычный текст (без таймкодов)";

  const content = track.activeLyrics || track.lyricsBody || "";

  bodyEl.innerHTML = `
    <div style="display: flex; gap: 16px; align-items: center;">
      <div class="drawer-cover-wrap">
        ${coverUrl 
          ? `<img src="${coverUrl}" class="drawer-cover-img" alt="Обложка" onerror="this.parentElement.innerHTML='<div style=\\'display:flex;height:100%;align-items:center;justify-content:center;background:var(--brand-gradient-soft);\\'><svg class=\\'mark-ph\\' viewBox=\\'0 0 28 28\\' width=\\'40\\' height=\\'40\\' aria-hidden=\\'true\\'><rect x=\\'3\\' y=\\'11\\' width=\\'3.5\\' height=\\'6\\' rx=\\'1\\'/><rect x=\\'9\\' y=\\'6\\' width=\\'3.5\\' height=\\'16\\' rx=\\'1\\'/><rect x=\\'15\\' y=\\'9\\' width=\\'3.5\\' height=\\'10\\' rx=\\'1\\'/><rect x=\\'21\\' y=\\'3\\' width=\\'3.5\\' height=\\'22\\' rx=\\'1\\' class=\\'mark-live\\'/></svg></div>'"/>` 
          : `<div style="display:flex;height:100%;align-items:center;justify-content:center;background:var(--brand-gradient-soft);"><svg class="mark-ph" viewBox="0 0 28 28" width="40" height="40" aria-hidden="true"><rect x="3" y="11" width="3.5" height="6" rx="1"/><rect x="9" y="6" width="3.5" height="16" rx="1"/><rect x="15" y="9" width="3.5" height="10" rx="1"/><rect x="21" y="3" width="3.5" height="22" rx="1" class="mark-live"/></svg></div>`
        }
      </div>
      <div>
        <div style="font-size: 16px; font-weight: 700; color: var(--text-primary);">${escapeHtml(track.title || "—")}</div>
        <div style="font-size: 13px; color: var(--text-secondary); margin-top: 2px;">${escapeHtml(track.artist || "—")}</div>
        <div style="font-size: 12px; color: var(--text-muted); margin-top: 2px;">${escapeHtml(track.album || "—")}</div>
      </div>
    </div>

    <div class="drawer-meta-list">
      <div class="drawer-meta-row">
        <span class="drawer-meta-label">Длительность</span>
        <span class="drawer-meta-val mono">${formatDuration(track.duration || 0)}</span>
      </div>
      <div class="drawer-meta-row">
        <span class="drawer-meta-label">Год / Номер</span>
        <span class="drawer-meta-val">${track.year || "—"} · #${track.trackNumber || "—"}</span>
      </div>
      <div class="drawer-meta-row">
        <span class="drawer-meta-label">Статус текста</span>
        <span class="drawer-meta-val">${escapeHtml(lyricsStatusDesc)}</span>
      </div>
      <div class="drawer-meta-row">
        <span class="drawer-meta-label">Источник</span>
        <span class="drawer-meta-val mono">${escapeHtml(formatLyricsSource(track.activeSource || track.lyricsSource) || "—")}</span>
      </div>
      <div class="drawer-meta-row">
        <span class="drawer-meta-label">Файл</span>
        <span class="drawer-meta-val mono" style="font-size: 11px;">${escapeHtml(track.filePath || "—")}</span>
      </div>
    </div>

    <div style="display: flex; flex-direction: column; gap: 8px; margin-top: 10px;">
      <button class="btn btn-primary" onclick="openTrackInKaraoke('${escapeHtml(track.id)}'); document.getElementById('drawer-track-detail').classList.add('hidden');">
        Запустить в Караоке-плеере
      </button>
      <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 8px;">
        <button class="btn btn-secondary" onclick="enqueueSingleTournament('${escapeHtml(track.id)}')">
          LRC Турнир
        </button>
        <button class="btn btn-secondary" onclick="enqueueSingleGroq('${escapeHtml(track.id)}')">
          AI Караоке (Groq)
        </button>
      </div>
      ${(content || (track.lyricsStatus && track.lyricsStatus !== "none")) ? `
        <button class="btn btn-danger-outline" onclick="deleteTrackLyrics('${escapeHtml(track.id)}')" style="margin-top: 4px;">
          Удалить текст и .lrc файл
        </button>
      ` : ""}
    </div>

    ${content ? `
      <div style="margin-top: 10px;">
        <div style="font-size: 12px; font-weight: 600; color: var(--text-secondary); margin-bottom: 6px;">Предпросмотр текста:</div>
        <pre class="raw-textarea mono" style="height: 120px; font-size: 11px; padding: 8px;" readonly>${escapeHtml(content.slice(0, 300) + (content.length > 300 ? "..." : ""))}</pre>
      </div>
    ` : ""}
  `;

  drawer?.classList.remove("hidden");
}

function formatLyricsSource(src) {
  if (!src) return "";
  const lower = String(src).toLowerCase();
  if (lower === "poisq" || lower === "whisper_gemini" || lower === "gemini_consensus") {
    return "poisq";
  }
  return String(src).toUpperCase();
}

async function deleteTrackLyrics(trackId) {
  if (!trackId) return;
  const track = state.tracks.find(t => t.id === trackId) || state.selectedTrack;
  const titleName = track ? `«${track.artist || ""} — ${track.title || ""}»` : trackId;

  if (!confirm(`Удалить текст песни и файл .lrc с диска для трека ${titleName}?`)) {
    return;
  }

  try {
    await fetchApi("/lyrics/delete", "POST", { trackId });
    showToast("✓ Текст песни и файл .lrc успешно удалены", "success");

    if (state.selectedTrack && state.selectedTrack.id === trackId) {
      state.selectedTrack.activeLyrics = "";
      state.selectedTrack.lyricsBody = "";
      state.selectedTrack.lyricsStatus = "none";
      state.selectedTrack.activeSource = "";
      state.lyrics = [];
      const rawTextarea = document.getElementById("raw-lrc-textarea");
      if (rawTextarea) rawTextarea.value = "";
      const providerLabel = document.getElementById("provider-info-label");
      if (providerLabel) providerLabel.textContent = "Нет текста";
      document.getElementById("btn-delete-lyrics")?.classList.add("hidden");
      document.getElementById("btn-create-ai-lrc")?.classList.remove("hidden");
      renderLyricsTeleprompter();
      updatePlayerUI();
    }

    document.getElementById("drawer-track-detail")?.classList.add("hidden");
    await loadTracks();
  } catch (err) {
    showToast(`Ошибка удаления текста: ${err.message}`, "error");
  }
}

async function enqueueSingleTournament(trackId) {
  try {
    await fetchApi("/lyrics/enqueue", "POST", { trackId, jobType: "tournament" });
    showToast("✓ Задача турнира поставлена в очередь воркера", "success");
    loadJobsData();
  } catch (err) {
    showToast(`Ошибка: ${err.message}`, "error");
  }
}

async function enqueueSingleGroq(trackId) {
  const track = state.tracks.find(t => t.id === trackId);
  if (track) {
    state.selectedTrack = track;
  }
  document.getElementById("drawer-track-detail")?.classList.add("hidden");
  handleCreateAiLrc();
}

// -----------------------------------------------------------------------------
// 4. Караоке и LRC Плеер (Реальное аудио + Синхронизация + Громкость)
// -----------------------------------------------------------------------------
function initPlayerControls() {
  document.getElementById("btn-play")?.addEventListener("click", togglePlay);
  document.getElementById("btn-step-back")?.addEventListener("click", () => seekDelta(-10));
  document.getElementById("btn-step-forward")?.addEventListener("click", () => seekDelta(10));
  document.getElementById("scrub-bar")?.addEventListener("click", handleScrub);

  // Регулятор громкости
  const volSlider = document.getElementById("player-volume-slider");
  const volMuteBtn = document.getElementById("btn-volume-mute");
  const iconVolOn = document.getElementById("icon-vol-on");
  const iconVolMuted = document.getElementById("icon-vol-mute");

  let savedVol = parseFloat(localStorage.getItem("poisq_player_volume") || "1.0");
  if (isNaN(savedVol) || savedVol < 0 || savedVol > 1) savedVol = 1.0;

  if (state.audioEl) {
    state.audioEl.volume = savedVol;
  }
  if (volSlider) {
    volSlider.value = savedVol;
    volSlider.addEventListener("input", (e) => {
      const val = parseFloat(e.target.value);
      if (state.audioEl) state.audioEl.volume = val;
      localStorage.setItem("poisq_player_volume", val);
      updateVolumeIcons(val);
    });
  }

  function updateVolumeIcons(val) {
    if (val <= 0.01) {
      iconVolOn?.classList.add("hidden");
      iconVolMuted?.classList.remove("hidden");
    } else {
      iconVolOn?.classList.remove("hidden");
      iconVolMuted?.classList.add("hidden");
    }
  }
  updateVolumeIcons(savedVol);

  volMuteBtn?.addEventListener("click", () => {
    if (!state.audioEl) return;
    if (state.audioEl.volume > 0.01) {
      state.previousVolume = state.audioEl.volume;
      state.audioEl.volume = 0;
      if (volSlider) volSlider.value = 0;
      updateVolumeIcons(0);
    } else {
      const restoreVol = state.previousVolume || 0.8;
      state.audioEl.volume = restoreVol;
      if (volSlider) volSlider.value = restoreVol;
      localStorage.setItem("poisq_player_volume", restoreVol);
      updateVolumeIcons(restoreVol);
    }
  });

  // Модальное окно подтверждения AI Whisper для создания караоке
  document.getElementById("btn-whisper-offer-cancel")?.addEventListener("click", () => {
    document.getElementById("modal-whisper-offer")?.classList.add("hidden");
  });
  document.getElementById("btn-whisper-offer-confirm")?.addEventListener("click", () => {
    document.getElementById("modal-whisper-offer")?.classList.add("hidden");
    const hasGroq = (state.keys || []).some(k => k.provider === "groq" && k.status !== "invalid");
    if (!hasGroq) {
      showToast("Для караоке Whisper требуется добавить бесплатный ключ Groq Cloud во вкладке «Ключи API».", "info");
      switchTab("keys");
      return;
    }
    enqueueWhisperKaraoke();
  });

  if (state.audioEl) {
    state.audioEl.addEventListener("timeupdate", () => {
      state.currentSec = state.audioEl.currentTime;
      updatePlayerUI();
    });
    state.audioEl.addEventListener("ended", () => {
      state.isPlaying = false;
      updatePlayIcons();
    });
    state.audioEl.addEventListener("error", () => {
      console.warn("Ошибка аудиопотока браузера (файл может быть недоступен для прямого декодирования)");
    });
  }

  window.addEventListener("keydown", (e) => {
    if (e.code === "Space" && e.target.tagName !== "INPUT" && e.target.tagName !== "TEXTAREA") {
      e.preventDefault();
      togglePlay();
    }
  });
}

function renderKaraokeSidebar(filterText = "") {
  const container = document.getElementById("karaoke-tracks-list");
  if (!container) return;

  const q = filterText.toLowerCase();
  // При непустом запросе используем результат серверного умного поиска (если он уже получен)
  const list = (q && state.karaokeSearchResults) ? state.karaokeSearchResults : state.tracks.filter(t =>
    !q ||
    (t.title && t.title.toLowerCase().includes(q)) ||
    (t.artist && t.artist.toLowerCase().includes(q))
  );

  if (list.length === 0) {
    container.innerHTML = `<div class="cell-empty">Песни не найдены</div>`;
    return;
  }

  container.innerHTML = list.map(t => {
    const isSelected = state.selectedTrack && state.selectedTrack.id === t.id;
    let badge = `<span class="text-muted" style="font-size: 10px;">Нет</span>`;
    if (t.lyricsStatus === "verified" || t.lyricsStatus === "synced") {
      badge = `<span class="text-emerald" style="font-size: 10px;">LRC</span>`;
    } else if (t.lyricsStatus === "plain") {
      badge = `<span class="text-secondary" style="font-size: 10px;">TXT</span>`;
    }

    return `
      <div class="sidebar-track-item ${isSelected ? "active" : ""}" onclick="openTrackInKaraoke('${escapeHtml(t.id)}')">
        <div class="sidebar-track-meta">
          <div class="sidebar-track-title">${escapeHtml(t.title || "Без названия")}</div>
          <div class="sidebar-track-artist">${escapeHtml(t.artist || "—")}</div>
        </div>
        <div>${badge}</div>
      </div>
    `;
  }).join("");
}

async function openTrackInKaraoke(trackId) {
  if (window.TimelineEditor && window.TimelineEditor.isDirty() &&
      state.selectedTrack && state.selectedTrack.id !== trackId &&
      !confirm("В редакторе таймингов есть несохранённые изменения. Перейти к другому треку без сохранения?")) {
    return;
  }
  let track = state.tracks.find(t => t.id === trackId);
  let confidence = null;

  try {
    const res = await fetchApi(`/lyrics/cache?trackId=${encodeURIComponent(trackId)}`);
    if (res.track) {
      track = res.track;
    }
    confidence = res.confidence || null;
  } catch (err) {
    console.warn("Не удалось получить свежие данные текстов трека:", err);
  }

  if (!track) {
    showToast("Трек не найден", "error");
    return;
  }

  state.selectedTrack = track;
  state.totalSec = track.duration || 180;
  state.currentSec = 0;
  state.activeLyricIndex = -1;

  // Обновляем обложку альбома в шапке караоке-плеера
  const playerCover = document.getElementById("player-cover");
  if (playerCover) {
    const coverUrl = track.folderPath
      ? `/api/v1/navidrome/album-cover?album=${encodeURIComponent(track.album || "")}&artist=${encodeURIComponent(track.artist || "")}&path=${encodeURIComponent(track.folderPath)}`
      : "";
    if (coverUrl) {
      playerCover.innerHTML = `<img src="${coverUrl}" class="player-cover-img" alt="Обложка" onerror="this.parentElement.innerHTML='<svg class=\\'mark-ph\\' viewBox=\\'0 0 28 28\\' width=\\'28\\' height=\\'28\\' aria-hidden=\\'true\\'><rect x=\\'3\\' y=\\'11\\' width=\\'3.5\\' height=\\'6\\' rx=\\'1\\'/><rect x=\\'9\\' y=\\'6\\' width=\\'3.5\\' height=\\'16\\' rx=\\'1\\'/><rect x=\\'15\\' y=\\'9\\' width=\\'3.5\\' height=\\'10\\' rx=\\'1\\'/><rect x=\\'21\\' y=\\'3\\' width=\\'3.5\\' height=\\'22\\' rx=\\'1\\' class=\\'mark-live\\'/></svg>'"/>`;
    } else {
      playerCover.innerHTML = `<svg class="mark-ph" viewBox="0 0 28 28" width="28" height="28" aria-hidden="true"><rect x="3" y="11" width="3.5" height="6" rx="1"/><rect x="9" y="6" width="3.5" height="16" rx="1"/><rect x="15" y="9" width="3.5" height="10" rx="1"/><rect x="21" y="3" width="3.5" height="22" rx="1" class="mark-live"/></svg>`;
    }
  }

  document.getElementById("player-title").textContent = track.title || "Без названия";
  document.getElementById("player-artist").textContent = `${track.artist || "Неизвестный артист"} — ${track.album || "Без альбома"}`;
  document.getElementById("time-total").textContent = formatDuration(state.totalSec);

  const lrcPath = track.filePath ? track.filePath.replace(/\.[^/.]+$/, ".lrc") : "/music/track.lrc";
  document.getElementById("raw-file-path").textContent = lrcPath;

  // ИСПРАВЛЕНИЕ БАГА: читаем activeLyrics с фоллбэком на lyricsBody!
  const lyricsContent = (track.activeLyrics || track.lyricsBody || "").trim();

  const rawTextarea = document.getElementById("raw-lrc-textarea");
  if (rawTextarea) {
    rawTextarea.value = lyricsContent;
  }

  const providerLabel = document.getElementById("provider-info-label");
  if (providerLabel) {
    const src = track.activeSource || track.lyricsSource;
    if (src) {
      providerLabel.textContent = `Источник: ${formatLyricsSource(src)}`;
    } else {
      providerLabel.textContent = lyricsContent ? "Синхронизирован" : "Нет текста";
    }
  }

  // Парсим LRC
  let hasValidLrcTimestamps = false;
  if (lyricsContent) {
    const parsed = parseLRC(lyricsContent);
    if (parsed.length > 0) {
      state.lyrics = parsed;
      hasValidLrcTimestamps = true;
    } else {
      // Текст без таймкодов — раскладываем построчно с шагом 5 секунд
      state.lyrics = lyricsContent.split("\n")
        .map(l => l.trim())
        .filter(l => l.length > 0)
        .map((text, idx) => ({ sec: idx * 5, text }));
    }
  } else {
    state.lyrics = [];
  }

  // Управление кнопками генерации и удаления LRC
  const btnCreateAiLrc = document.getElementById("btn-create-ai-lrc");
  if (btnCreateAiLrc) {
    const needsAiSync = !hasValidLrcTimestamps;
    btnCreateAiLrc.classList.toggle("hidden", !needsAiSync);
  }

  const btnDeleteLyrics = document.getElementById("btn-delete-lyrics");
  if (btnDeleteLyrics) {
    const hasLyrics = Boolean(lyricsContent) || (track.lyricsStatus && track.lyricsStatus !== "none");
    btnDeleteLyrics.classList.toggle("hidden", !hasLyrics);
  }

  // Подключаем реальный аудиопоток к плееру (БЕЗ автозапуска — воспроизведение только по нажатию Play)
  if (state.audioEl) {
    state.audioEl.src = `/api/v1/tracks/stream?id=${encodeURIComponent(track.id)}`;
    state.audioEl.load();
    state.isPlaying = false;
    updatePlayIcons();
  }

  window.TimelineEditor?.load(track, state.lyrics, hasValidLrcTimestamps, confidence);
  if (state.editorMode === "timeline" && state.audioEl) {
    state.audioEl.preload = "metadata";
    state.audioEl.load();
  }

  renderLyricsTeleprompter();
  renderKaraokeSidebar();
  updatePlayerUI();
  switchTab("karaoke");
}

function renderLyricsTeleprompter() {
  const container = document.getElementById("lyrics-stream");
  if (!container) return;

  if (state.lyrics.length === 0) {
    container.innerHTML = `
      <div class="lyric-line inactive" style="font-size: 16px; opacity: 0.7;">
        Текст для этой песни еще не найден.<br>
        Нажмите «Найти текст» в шапке.
      </div>
    `;
    return;
  }

  container.innerHTML = state.lyrics.map((l, idx) => `
    <div class="lyric-line inactive" data-index="${idx}" data-sec="${l.sec}" onclick="seekToSecond(${l.sec})">
      ${escapeHtml(l.text)}
    </div>
  `).join("");
}

function seekToSecond(sec) {
  state.currentSec = Math.max(0, Math.min(state.totalSec, sec));
  if (state.audioEl && !isNaN(state.audioEl.duration)) {
    state.audioEl.currentTime = state.currentSec;
    if (!state.isPlaying) {
      state.audioEl.play();
      state.isPlaying = true;
      updatePlayIcons();
    }
  }
  updatePlayerUI();
}

function seekDelta(delta) {
  state.currentSec = Math.max(0, Math.min(state.totalSec, state.currentSec + delta));
  if (state.audioEl && !isNaN(state.audioEl.duration)) {
    state.audioEl.currentTime = state.currentSec;
  }
  updatePlayerUI();
}

function handleScrub(e) {
  if (state.totalSec <= 0) return;
  const rect = e.currentTarget.getBoundingClientRect();
  const clickX = e.clientX - rect.left;
  const pct = Math.max(0, Math.min(1, clickX / rect.width));
  state.currentSec = Math.floor(pct * state.totalSec);
  if (state.audioEl && !isNaN(state.audioEl.duration)) {
    state.audioEl.currentTime = state.currentSec;
  }
  updatePlayerUI();
}

function togglePlay() {
  state.isPlaying = !state.isPlaying;
  if (state.audioEl) {
    if (state.isPlaying) {
      state.audioEl.play().catch(() => {});
    } else {
      state.audioEl.pause();
    }
  }
  updatePlayIcons();
}

function updatePlayIcons() {
  const playIcon = document.getElementById("play-icon");
  const pauseIcon = document.getElementById("pause-icon");
  if (state.isPlaying) {
    playIcon?.classList.add("hidden");
    pauseIcon?.classList.remove("hidden");
  } else {
    playIcon?.classList.remove("hidden");
    pauseIcon?.classList.add("hidden");
  }
}

function updatePlayerUI() {
  const curLabel = document.getElementById("time-current");
  const progress = document.getElementById("scrub-progress");
  if (curLabel) curLabel.textContent = formatDuration(state.currentSec);
  if (progress && state.totalSec > 0) {
    const pct = (state.currentSec / state.totalSec) * 100;
    progress.style.width = `${pct}%`;
  }

  const lines = document.querySelectorAll("#lyrics-stream .lyric-line");
  let activeIdx = -1;

  for (let i = 0; i < state.lyrics.length; i++) {
    if (state.lyrics[i].sec <= state.currentSec) {
      activeIdx = i;
    }
  }

  if (activeIdx !== state.activeLyricIndex) {
    state.activeLyricIndex = activeIdx;
    lines.forEach((line, i) => {
      if (i === activeIdx) {
        line.classList.add("active");
        line.classList.remove("inactive");
        
        // Скроллим исключительно контейнер телесуфлера, не двигая страницу и плеер
        const viewport = document.getElementById("view-karaoke");
        if (viewport) {
          const lineTop = line.offsetTop;
          const lineH = line.offsetHeight;
          const vpH = viewport.clientHeight;
          viewport.scrollTo({
            top: lineTop - (vpH / 2) + (lineH / 2),
            behavior: "smooth"
          });
        }
      } else {
        line.classList.remove("active");
        line.classList.add("inactive");
      }
    });
  }
}

// Режимы правой области: "karaoke" (суфлёр), "timeline" (редактор с волной), "raw" (сырой текст .lrc)
function setEditorMode(mode) {
  state.editorMode = mode;
  document.getElementById("view-karaoke")?.classList.toggle("hidden", mode !== "karaoke");
  document.getElementById("view-timeline")?.classList.toggle("hidden", mode !== "timeline");
  document.getElementById("view-raw-editor")?.classList.toggle("hidden", mode !== "raw");
  const btn = document.getElementById("btn-toggle-editor");
  if (btn) btn.textContent = mode === "karaoke" ? "Редактор таймингов" : "Режим суфлёра";

  if (window.TimelineEditor) {
    if (mode === "timeline") {
      // Для перемотки по метке плееру нужны метаданные трека (до первого Play их нет при preload="none")
      const a = state.audioEl;
      if (a && a.src && isNaN(a.duration) && a.preload !== "metadata") {
        a.preload = "metadata";
        a.load();
      }
      window.TimelineEditor.show();
    } else {
      window.TimelineEditor.hide();
    }
  }
}

function toggleLrcEditor() {
  setEditorMode((state.editorMode || "karaoke") === "karaoke" ? "timeline" : "karaoke");
}

function openRawEditor() {
  const ta = document.getElementById("raw-lrc-textarea");
  // Если в таймлайн-редакторе есть несохранённые правки, показываем именно их
  if (ta && window.TimelineEditor && window.TimelineEditor.isDirty()) {
    ta.value = window.TimelineEditor.currentLrc();
  }
  setEditorMode("raw");
}

// Записывает LRC в базу и рядом с аудиофайлом. Возвращает true при успехе.
async function persistLrcContent(content, extra = {}) {
  if (!state.selectedTrack) {
    showToast("Трек не выбран", "error");
    return false;
  }
  try {
    const res = await fetchApi("/lyrics/save", "POST", {
      trackId: state.selectedTrack.id,
      content,
      verified: true,
      ...extra
    });

    if (res.lrcWritten) {
      showToast(`✓ Файл .lrc сохранен рядом с аудиофайлом!`, "success");
    } else if (res.writeError) {
      showToast(`Текст сохранен в базу, но не удалось записать .lrc: ${res.writeError}`, "info");
    }

    state.selectedTrack.activeLyrics = content;
    state.selectedTrack.lyricsBody = content;
    state.selectedTrack.lyricsStatus = res.status || "verified";
    state.lyrics = parseLRC(content);
    const raw = document.getElementById("raw-lrc-textarea");
    if (raw) raw.value = content;
    document.getElementById("btn-delete-lyrics")?.classList.remove("hidden");
    document.getElementById("btn-create-ai-lrc")?.classList.add("hidden");

    renderLyricsTeleprompter();
    updatePlayerUI();
    loadTracks();
    return true;
  } catch (err) {
    showToast(`Ошибка сохранения: ${err.message}`, "error");
    return false;
  }
}

async function saveRawLrc() {
  if (!state.selectedTrack) {
    showToast("Трек не выбран", "error");
    return;
  }

  const textarea = document.getElementById("raw-lrc-textarea");
  const content = textarea ? textarea.value.trim() : "";
  if (!content) {
    showToast("Текст LRC не может быть пустым", "error");
    return;
  }

  if (await persistLrcContent(content)) {
    window.TimelineEditor?.load(state.selectedTrack, state.lyrics, true, null);
    setEditorMode("karaoke");
  }
}

let karaokeJobPollTimer = null;

function showKaraokeJobBanner(text) {
  const banner = document.getElementById("karaoke-job-banner");
  const txt = document.getElementById("karaoke-job-text");
  if (banner && txt) {
    txt.textContent = text;
    banner.classList.remove("hidden");
  }
}

function hideKaraokeJobBanner() {
  const banner = document.getElementById("karaoke-job-banner");
  if (banner) banner.classList.add("hidden");
}

function startPollingKaraokeJob(trackId, initialText) {
  if (karaokeJobPollTimer) {
    clearInterval(karaokeJobPollTimer);
    karaokeJobPollTimer = null;
  }

  showKaraokeJobBanner(initialText);

  let attempts = 0;
  karaokeJobPollTimer = setInterval(async () => {
    attempts++;
    if (attempts > 60) {
      clearInterval(karaokeJobPollTimer);
      karaokeJobPollTimer = null;
      hideKaraokeJobBanner();
      return;
    }

    try {
      const res = await fetchApi("/jobs");
      const jobs = res.jobs || [];
      const job = jobs.find(j => j.trackId === trackId);

      if (!job) return;

      if (job.status === "running") {
        const liveMsg = job.errorLog || (job.jobType === "whisper_karaoke"
          ? "Генерация пословных таймкодов караоке через AI Whisper..."
          : "Сравнение текстов в турнире (LRCLIB, Yandex, Genius)...");
        showKaraokeJobBanner(liveMsg);
      } else if (job.status === "done") {
        clearInterval(karaokeJobPollTimer);
        karaokeJobPollTimer = null;
        showKaraokeJobBanner("✓ Текст успешно обновлен (Источник: poisq)!");

        await refreshCurrentKaraokeTrack(trackId);

        setTimeout(() => {
          hideKaraokeJobBanner();
        }, 2500);
      } else if (job.status === "failed") {
        clearInterval(karaokeJobPollTimer);
        karaokeJobPollTimer = null;
        showKaraokeJobBanner("Текст не найден или ошибка провайдера");
        setTimeout(() => {
          hideKaraokeJobBanner();
        }, 3000);
      }
    } catch (err) {
      console.warn("Ошибка опроса статуса задачи:", err);
    }
  }, 1500);
}

async function refreshCurrentKaraokeTrack(trackId) {
  try {
    const res = await fetchApi(`/lyrics/cache?trackId=${encodeURIComponent(trackId)}`);
    if (res.track) {
      const updatedTrack = res.track;
      const idx = state.tracks.findIndex(t => t.id === trackId);
      if (idx !== -1) {
        state.tracks[idx] = updatedTrack;
      }
      if (state.selectedTrack && state.selectedTrack.id === trackId) {
        state.selectedTrack = updatedTrack;
        const lyricsContent = (updatedTrack.activeLyrics || updatedTrack.lyricsBody || "").trim();
        const rawTextarea = document.getElementById("raw-lrc-textarea");
        if (rawTextarea) rawTextarea.value = lyricsContent;

        const providerLabel = document.getElementById("provider-info-label");
        if (providerLabel) {
          const src = updatedTrack.activeSource || updatedTrack.lyricsSource;
          providerLabel.textContent = src ? `Источник: ${formatLyricsSource(src)}` : (lyricsContent ? "Синхронизирован" : "Нет текста");
        }

        let hasValidLrcTimestamps = false;
        if (lyricsContent) {
          const parsed = parseLRC(lyricsContent);
          if (parsed.length > 0) {
            state.lyrics = parsed;
            hasValidLrcTimestamps = true;
          } else {
            state.lyrics = lyricsContent.split("\n")
              .map(l => l.trim())
              .filter(l => l.length > 0)
              .map((text, i) => ({ sec: i * 5, text }));
          }
        } else {
          state.lyrics = [];
        }

        const btnCreateAiLrc = document.getElementById("btn-create-ai-lrc");
        if (btnCreateAiLrc) {
          btnCreateAiLrc.classList.toggle("hidden", hasValidLrcTimestamps);
        }
        const btnDeleteLyrics = document.getElementById("btn-delete-lyrics");
        if (btnDeleteLyrics) {
          const hasLyrics = Boolean(lyricsContent) || (updatedTrack.lyricsStatus && updatedTrack.lyricsStatus !== "none");
          btnDeleteLyrics.classList.toggle("hidden", !hasLyrics);
        }

        renderLyricsTeleprompter();
        updatePlayerUI();
        renderKaraokeSidebar();
        loadTracks();
        loadJobsData();

        // Если найден только обычный plain текст (без таймкодов) — сразу предлагаем создать караоке через AI Whisper
        if (updatedTrack.lyricsStatus === "plain" || (lyricsContent && state.lyrics.length > 0 && !lyricsContent.includes("["))) {
          const descEl = document.getElementById("whisper-offer-desc");
          if (descEl) {
            descEl.textContent = `Для песни «${updatedTrack.artist} — ${updatedTrack.title}» найден обычный текст без таймкодов. Хотите автоматически сгенерировать синхронный .lrc файл караоке при помощи AI Whisper?`;
          }
          document.getElementById("modal-whisper-offer")?.classList.remove("hidden");
        }
      }
    }
  } catch (err) {
    console.warn("Ошибка обновления данных трека:", err);
  }
}

async function enqueueLrcTournament() {
  if (!state.selectedTrack) {
    showToast("Сначала выберите трек в караоке-плеере", "error");
    return;
  }

  const trackId = state.selectedTrack.id;
  try {
    await fetchApi("/lyrics/enqueue", "POST", {
      trackId,
      jobType: "tournament"
    });
    showToast("✓ Поиск текста запущен", "info");
    startPollingKaraokeJob(trackId, "Поиск текста в LRCLIB, Яндекс Музыке, Genius...");
    loadJobsData();
  } catch (err) {
    showToast(`Ошибка: ${err.message}`, "error");
  }
}

function handleCreateAiLrc() {
  if (!state.selectedTrack) {
    showToast("Сначала выберите трек в караоке-плеере", "error");
    return;
  }
  openLyricsGenModal(state.selectedTrack);
}

function openLyricsGenModal(track) {
  if (!track) return;
  const modal = document.getElementById("modal-whisper-offer");
  const titleEl = document.getElementById("whisper-offer-track-title");
  if (titleEl) {
    titleEl.textContent = `${track.artist || "Неизвестный артист"} — ${track.title || "Без названия"}`;
  }
  const confirmBtn = document.getElementById("btn-whisper-offer-confirm");
  const cancelBtn = document.getElementById("btn-whisper-offer-cancel");
  const closeBtn = document.getElementById("btn-close-whisper-offer");

  const onConfirm = () => {
    modal?.classList.add("hidden");
    confirmBtn?.removeEventListener("click", onConfirm);
    enqueueWhisperKaraoke();
  };
  const onCancel = () => {
    modal?.classList.add("hidden");
    confirmBtn?.removeEventListener("click", onConfirm);
  };

  confirmBtn?.addEventListener("click", onConfirm);
  cancelBtn?.addEventListener("click", onCancel);
  closeBtn?.addEventListener("click", onCancel);

  modal?.classList.remove("hidden");
}

async function enqueueWhisperKaraoke() {
  if (!state.selectedTrack) {
    showToast("Сначала выберите трек в караоке-плеере", "error");
    return;
  }

  const hasGroq = (state.keys || []).some(k => k.provider === "groq" && k.status !== "invalid");
  if (!hasGroq) {
    showToast("Для создания караоке Whisper требуется добавить ключ Groq Cloud во вкладке «Ключи API».", "info");
    switchTab("keys");
    return;
  }

  const trackId = state.selectedTrack.id;
  try {
    await fetchApi("/lyrics/enqueue", "POST", {
      trackId,
      jobType: "whisper_karaoke"
    });
    showToast("✓ Задача AI Караоке (Groq Whisper Large v3) запущена", "info");
    startPollingKaraokeJob(trackId, "Создание синхронного караоке через AI Whisper...");
    loadJobsData();
  } catch (err) {
    showToast(`Ошибка: ${err.message}`, "error");
  }
}

// -----------------------------------------------------------------------------
// 5. Staging-буфер рефакторинга: Side-by-Side обложки и чистый диф тегов
// -----------------------------------------------------------------------------
async function loadStagingData() {
  const container = document.getElementById("staging-cards-container");
  if (!container) return;

  try {
    const res = await fetchApi("/staging?status=pending");
    state.stagingItems = res.items || [];
  } catch (err) {
    console.error("Ошибка загрузки Staging:", err);
    state.stagingItems = [];
  }

  const badgeStaging = document.getElementById("badge-staging-count");
  if (badgeStaging) badgeStaging.textContent = state.stagingItems.length;

  if (state.stagingItems.length === 0) {
    container.innerHTML = `<div class="cell-empty">Очередь Staging пуста. Все теги каноничны. Нажмите «MusicBrainz аудит» для поиска предложений.</div>`;
    return;
  }

  container.innerHTML = state.stagingItems.map(item => {
    let curObj = {};
    let propObj = {};
    try { curObj = JSON.parse(item.currentJson || "{}"); } catch (e) {}
    try { propObj = JSON.parse(item.proposedJson || "{}"); } catch (e) {}

    const path = item.folderPath || item.targetId || "Аудиофайл";

    // 1. Отображение только обложки (Side-by-Side без лишнего перечеркивания неизмененных тегов)
    if (item.changeType === "cover") {
      const curHasCover = curObj.hasCover;
      const curRes = curHasCover && curObj.width > 0 ? `${curObj.width}×${curObj.height} px` : (curHasCover ? "Найден" : "Поиск на диске...");
      const curCoverSrc = curObj.existingFile
        ? `/api/v1/covers/local?path=${encodeURIComponent(curObj.existingFile)}&album=${encodeURIComponent(curObj.album || propObj.album || "")}&artist=${encodeURIComponent(curObj.artist || propObj.artist || "")}`
        : (path ? `/api/v1/covers/local?path=${encodeURIComponent(path)}&album=${encodeURIComponent(curObj.album || propObj.album || "")}&artist=${encodeURIComponent(curObj.artist || propObj.artist || "")}` : "");

      const chosenCover = window.selectedCovers?.[item.id] || propObj.coverUrl;
      const propRes = propObj.width && propObj.height ? `${propObj.width}×${propObj.height} px` : "3000×3000 px HD";
      const propSource = propObj.source === "itunes" ? "Apple Music / iTunes" : "Cover Art Archive";

      const propArtist = propObj.artist || curObj.artist || (path.split("/").length >= 2 ? path.split("/")[path.split("/").length - 2] : "");
      const propAlbum = propObj.album || curObj.album || (path.split("/").length >= 1 ? path.split("/")[path.split("/").length - 1] : "");

      const hasAi = !!propObj.aiExplanation;
      const aiBadgeHtml = `
        <div class="cover-ai-badge ${hasAi ? '' : 'hidden'}" id="ai-badge-${item.id}">
          <span class="ai-sparkle">AI</span>
          <span><strong>AI-арбитр (${escapeHtml(propObj.releaseType || "Альбом")}):</strong> Канонический студийный альбом <em>«${escapeHtml(propObj.canonicalAlbum || propObj.album)}»</em>. ${escapeHtml(propObj.aiExplanation || "")}</span>
        </div>
      `;

      return `
        <div class="staging-card">
          <div class="staging-top">
            <div>
              <div class="staging-issue">Найдена обложка альбома высокого разрешения (Уверенность: ${item.confidence || 95}%)</div>
              <div class="staging-path mono">${escapeHtml(path)}</div>
              ${aiBadgeHtml}
            </div>
            <div style="display: flex; gap: 8px;">
              <button class="btn btn-secondary" onclick="dismissStaging(${item.id})">Оставить текущую</button>
              <button class="btn btn-primary" onclick="applyStaging(${item.id})">Заменить на новую</button>
            </div>
          </div>

          <div class="cover-compare-grid">
            <div class="cover-side-card">
              <div class="cover-side-title">Было (В папке альбома)</div>
              ${curCoverSrc 
                ? `
                  <img 
                    src="${curCoverSrc}" 
                    id="cur-cover-img-${item.id}"
                    class="cover-side-img" 
                    alt="Текущая" 
                    title="Нажмите для полноэкранного просмотра"
                    onclick="openCoverLightbox(${item.id}, 0)"
                    onload="const m = document.getElementById('cur-meta-${item.id}'); if (m && this.naturalWidth) m.innerText = 'Файл: Найден на диске · ' + this.naturalWidth + '×' + this.naturalHeight + ' px'; const emp = document.getElementById('cur-empty-${item.id}'); if (emp) emp.style.display='none'; this.style.display='block';"
                    onerror="this.style.display='none'; const emp = document.getElementById('cur-empty-${item.id}'); if (emp) emp.style.display='flex'; const m = document.getElementById('cur-meta-${item.id}'); if (m) m.innerText = 'Файл: Отсутствует · Отсутствует';"
                  />
                  <div id="cur-empty-${item.id}" class="cover-side-img" style="display:none;align-items:center;justify-content:center;color:var(--text-muted);" onclick="openCoverLightbox(${item.id}, 0)">Нет файла</div>
                ` 
                : `<div class="cover-side-img" style="display:flex;align-items:center;justify-content:center;color:var(--text-muted);" onclick="openCoverLightbox(${item.id}, 0)">Нет файла</div>`
              }
              <div class="cover-side-zoom-hint">Нажмите для увеличения</div>
              <div class="cover-side-meta" id="cur-meta-${item.id}">Файл: ${curHasCover ? "Найден" : "Отсутствует"} · ${curRes}</div>
            </div>

            <div class="cover-side-card" style="border-color: var(--color-emerald-border);">
              <div class="cover-side-title text-emerald">Станет (Новый cover.jpg)</div>
              ${chosenCover 
                ? `
                  <img 
                    src="${escapeHtml(chosenCover)}" 
                    id="prop-cover-img-${item.id}" 
                    class="cover-side-img" 
                    alt="Новая" 
                    title="Нажмите для полноэкранного просмотра"
                    loading="lazy" 
                    onclick="openCoverLightbox(${item.id}, 1)"
                  />
                ` 
                : `<div class="cover-side-img" style="display:flex;align-items:center;justify-content:center;color:var(--text-muted);">♪</div>`
              }
              <div class="cover-side-zoom-hint">Нажмите для увеличения</div>
              <div class="cover-side-meta" id="prop-cover-meta-${item.id}">${propRes} · ${propSource}</div>

              <!-- Кнопки управления вариантами и AI СПРАВА, под новой обложкой -->
              <div style="margin-top: 10px; width: 100%; display: flex; flex-direction: column; gap: 8px;">
                <button 
                  class="cover-candidates-toggle-btn" 
                  id="btn-toggle-cand-${item.id}"
                  onclick="toggleCoverCandidates(${item.id}, '${escapeHtml(propArtist)}', '${escapeHtml(propAlbum)}', '${escapeHtml(path)}')"
                >
                  Другие варианты обложек <span id="cand-count-${item.id}"></span>
                </button>
                <button 
                  class="cover-ai-resolve-btn" 
                  id="btn-ai-resolve-${item.id}"
                  onclick="resolveCoverViaAI(${item.id}, '${escapeHtml(path)}')"
                  title="Определить канонический студийный альбом через Gemini 2.5 Flash и найти оригинальную обложку"
                >
                  Уточнить альбом через AI
                </button>
              </div>
            </div>
          </div>

          <!-- Секция альтернативных вариантов обложек под сеткой -->
          <div id="candidates-container-${item.id}" class="cover-candidates-grid hidden"></div>

          <div style="margin-top: 10px; font-size: 11px; color: var(--text-muted);">
            <strong>Аудиофайлы не изменяются.</strong> В папку альбома будет скачан файл <code class="mono">cover.jpg</code>.
          </div>
        </div>
      `;
    }

    // 2. Отображение тегов: выводим ТОЛЬКО те поля, которые действительно изменились!
    const diffRows = [];

    if (curObj.artist !== propObj.artist && propObj.artist) {
      diffRows.push({
        label: "Артист",
        oldVal: curObj.artist || "—",
        newVal: propObj.artist
      });
    }
    if (curObj.album !== propObj.album && propObj.album) {
      diffRows.push({
        label: "Альбом",
        oldVal: curObj.album || "—",
        newVal: propObj.album
      });
    }
    if (curObj.title !== propObj.title && propObj.title) {
      diffRows.push({
        label: "Трек",
        oldVal: curObj.title || "—",
        newVal: propObj.title
      });
    }
    if (curObj.year !== propObj.year && propObj.year) {
      diffRows.push({
        label: "Год",
        oldVal: curObj.year || "—",
        newVal: propObj.year
      });
    }

    let issueTitle = "Канонизация тегов MusicBrainz";
    if (item.changeType === "mojibake") issueTitle = "Битая кодировка (CP1251 → UTF-8)";
    if (item.changeType === "artist_canonical") issueTitle = "Канонизация имени артиста";

    return `
      <div class="staging-card">
        <div class="staging-top">
          <div>
            <div class="staging-issue">${escapeHtml(issueTitle)} (Уверенность: ${item.confidence || 90}%)</div>
            <div class="staging-path mono">${escapeHtml(path)}</div>
          </div>
          <div style="display: flex; gap: 8px;">
            <button class="btn btn-secondary" onclick="dismissStaging(${item.id})">Пропустить</button>
            <button class="btn btn-primary" onclick="applyStaging(${item.id})">Применить</button>
          </div>
        </div>

        <div class="staging-diff-grid">
          <div class="diff-cell">
            <div class="diff-cell-head">Было (В файле)</div>
            ${diffRows.map(r => `
              <div><span class="text-muted">${r.label}:</span> <span class="diff-val-old">${escapeHtml(r.oldVal)}</span></div>
            `).join("")}
          </div>
          <div class="diff-cell">
            <div class="diff-cell-head text-emerald">Станет (Канонично)</div>
            ${diffRows.map(r => `
              <div><span class="text-muted">${r.label}:</span> <span class="diff-val-new">${escapeHtml(r.newVal)}</span></div>
            `).join("")}
          </div>
        </div>

        ${propObj.reason ? `
          <div style="margin-top: 8px; font-size: 11px; color: var(--text-muted);">
            ${escapeHtml(propObj.reason)}
          </div>
        ` : ""}
      </div>
    `;
  }).join("");
}

async function applyStaging(id) {
  try {
    const payload = { id };
    if (window.selectedCovers && window.selectedCovers[id]) {
      payload.coverUrl = window.selectedCovers[id];
    }
    await fetchApi("/staging/apply", "POST", payload);
    state.lastAppliedStagingId = id;
    showToast("✓ Изменения успешно применены в файл", "success");
    await loadStagingData();
  } catch (err) {
    showToast(`Ошибка применения: ${err.message}`, "error");
  }
}

// -----------------------------------------------------------------------------
// Выбор альтернативных вариантов обложек
// -----------------------------------------------------------------------------
window.selectedCovers = window.selectedCovers || {};
window.coverCandidatesCache = window.coverCandidatesCache || {};

async function toggleCoverCandidates(itemId, artist, album, path) {
  const container = document.getElementById(`candidates-container-${itemId}`);
  if (!container) return;

  if (!container.classList.contains("hidden")) {
    container.classList.add("hidden");
    return;
  }

  container.classList.remove("hidden");
  if (container.children.length > 0) return;

  container.innerHTML = `<div style="grid-column: 1/-1; padding: 12px; color: var(--text-muted); font-size: 12px; text-align: center;">Поиск доступных обложек в Apple Music и Cover Art Archive...</div>`;

  try {
    const item = (state.stagingItems || []).find(it => it.id === itemId);
    let candidates = [];

    // Запрашиваем актуальные варианты обложек через /api/v1/covers/candidates
    const q = new URLSearchParams();
    if (artist) q.set("artist", artist);
    if (album) q.set("album", album);
    if (path) q.set("path", path);

    const resp = await fetchApi(`/covers/candidates?${q.toString()}`);
    if (resp && Array.isArray(resp.candidates) && resp.candidates.length > 0) {
      candidates = resp.candidates;
    }

    // Если кандидаты не найдены по запросу, но в предложении были альтернативы
    if (candidates.length === 0 && item && item.proposedJson) {
      try {
        const prop = JSON.parse(item.proposedJson);
        if (Array.isArray(prop.alternatives) && prop.alternatives.length > 0) {
          candidates = prop.alternatives;
        }
      } catch (e) {}
    }

    window.coverCandidatesCache[itemId] = candidates;

    if (!candidates || candidates.length === 0) {
      container.innerHTML = `<div style="grid-column: 1/-1; padding: 12px; color: var(--text-muted); font-size: 12px; text-align: center;">Других вариантов обложек не найдено.</div>`;
      return;
    }

    renderCoverCandidatesGrid(itemId, candidates, album, artist);

    const countBadge = document.getElementById(`cand-count-${itemId}`);
    if (countBadge) countBadge.innerText = `(${candidates.length})`;

  } catch (err) {
    container.innerHTML = `<div style="grid-column: 1/-1; padding: 12px; color: var(--accent-danger); font-size: 12px; text-align: center;">Ошибка поиска вариантов: ${escapeHtml(err.message)}</div>`;
  }
}

function renderCoverCandidatesGrid(itemId, candidates, defaultAlbum = "", defaultArtist = "") {
  const container = document.getElementById(`candidates-container-${itemId}`);
  if (!container) return;

  const item = (state.stagingItems || []).find(it => it.id === itemId);
  let activeUrl = window.selectedCovers[itemId];
  if (!activeUrl && item && item.proposedJson) {
    try {
      activeUrl = JSON.parse(item.proposedJson).coverUrl;
    } catch (e) {}
  }

  container.innerHTML = candidates.map((cand, idx) => {
    const isSel = cand.url === activeUrl;
    const resText = cand.width && cand.height ? `${cand.width}×${cand.height} px` : "3000×3000 px";
    const srcText = cand.source === "itunes" ? "Apple Music" : "Cover Art Archive";
    const title = cand.title || defaultAlbum || "Альбом";
    const artist = cand.artist || defaultArtist || "";

    return `
      <div class="cover-cand-card ${isSel ? 'active' : ''}" id="cand-card-${itemId}-${idx}">
        <div class="cover-cand-thumb-wrap" onclick="openCoverLightboxForCandidate(${itemId}, ${idx})" title="Нажмите для полноэкранного просмотра">
          <img src="${escapeHtml(cand.url)}" class="cover-cand-thumb" alt="Вариант" loading="lazy" />
          <span class="cover-cand-zoom-hint">Увеличить</span>
        </div>
        <div class="cover-cand-title" title="${escapeHtml(title)}">${escapeHtml(title)}</div>
        <div class="cover-cand-meta">${escapeHtml(artist)}</div>
        <div class="cover-cand-meta">${resText} · ${srcText}</div>
        <button class="btn btn-secondary cover-cand-btn" onclick="selectCoverCandidate(${itemId}, ${idx})">${isSel ? '✓ Выбрано' : 'Выбрать'}</button>
      </div>
    `;
  }).join("");
}

async function resolveCoverViaAI(itemId, folderPath) {
  const btn = document.getElementById(`btn-ai-resolve-${itemId}`);
  const origBtnText = btn ? btn.innerHTML : "Уточнить альбом через AI";
  if (btn) {
    btn.disabled = true;
    btn.innerHTML = `<span class="spinner-inline"></span> Анализируем через Gemini...`;
  }

  try {
    const res = await fetchApi("/covers/ai-resolve", "POST", {
      id: itemId,
      path: folderPath
    });

    if (!res.success || !res.decision) {
      throw new Error(res.error || "Не удалось получить решение AI");
    }

    const dec = res.decision;
    showToast(`Канонический альбом: ${dec.canonical_album} (${dec.release_type})`, "success");

    // Обновляем бейдж AI в карточке
    let badge = document.getElementById(`ai-badge-${itemId}`);
    if (badge) {
      badge.innerHTML = `
        <span class="ai-sparkle">AI</span>
        <span><strong>AI-арбитр (${escapeHtml(dec.release_type || "Альбом")}):</strong> Канонический студийный альбом <em>«${escapeHtml(dec.canonical_album)}»</em>. ${escapeHtml(dec.explanation || "")}</span>
      `;
      badge.classList.remove("hidden");
    }

    // Если есть кандидаты обложек канонического альбома
    if (res.candidates && res.candidates.length > 0) {
      const best = res.candidates[0];
      if (!window.selectedCovers) window.selectedCovers = {};
      window.selectedCovers[itemId] = best.url;

      // Обновляем превью Станет
      const propImg = document.getElementById(`prop-cover-img-${itemId}`);
      if (propImg) {
        propImg.src = best.url;
      }
      const propMeta = document.getElementById(`prop-cover-meta-${itemId}`);
      if (propMeta) {
        propMeta.textContent = `${best.width}×${best.height} px · ${best.source === "itunes" ? "Apple Music / iTunes" : "Cover Art Archive"} · ${dec.canonical_album}`;
      }

      // Кэшируем кандидатов для этого itemId
      if (!window.coverCandidatesCache) window.coverCandidatesCache = {};
      window.coverCandidatesCache[itemId] = res.candidates;

      // Перерендерим галерею вариантов, если контейнер открыт
      const candContainer = document.getElementById(`candidates-container-${itemId}`);
      if (candContainer && !candContainer.classList.contains("hidden")) {
        renderCoverCandidatesGrid(itemId, res.candidates, dec.canonical_album, dec.canonical_artist);
      }
      const countBadge = document.getElementById(`cand-count-${itemId}`);
      if (countBadge) countBadge.innerText = `(${res.candidates.length})`;
    } else {
      showToast("AI определил альбом, но обложек в каталогах Apple не найдено", "info");
    }

    // Если на диске была обнаружена обложка, обновляем блок Было
    if (res.hasLocalCover) {
      const curImg = document.getElementById(`cur-cover-img-${itemId}`);
      const curEmpty = document.getElementById(`cur-empty-${itemId}`);
      const curMeta = document.getElementById(`cur-meta-${itemId}`);
      if (curEmpty) curEmpty.style.display = "none";
      if (curImg) {
        curImg.src = `/api/v1/covers/local?path=${encodeURIComponent(folderPath)}&t=${Date.now()}`;
        curImg.style.display = "block";
      }
      if (curMeta) {
        curMeta.textContent = `Файл: Найден на диске · ${res.localWidth || 0}×${res.localHeight || 0} px`;
      }
    }
  } catch (err) {
    console.error("Ошибка при анализе обложки через AI:", err);
    showToast(`Ошибка AI: ${err.message}`, "error");
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.innerHTML = `Обновлено через AI`;
      setTimeout(() => {
        if (btn) btn.innerHTML = origBtnText;
      }, 4000);
    }
  }
}

function openCoverLightboxForCandidate(itemId, candIndex) {
  openCoverLightbox(itemId, 2 + candIndex);
}

function selectCoverCandidate(itemId, candIndex) {
  const candidates = window.coverCandidatesCache[itemId];
  if (!candidates || !candidates[candIndex]) return;

  const cand = candidates[candIndex];
  window.selectedCovers[itemId] = cand.url;

  // Обновляем подсветку карточек вариантов
  candidates.forEach((_, idx) => {
    const el = document.getElementById(`cand-card-${itemId}-${idx}`);
    if (el) {
      if (idx === candIndex) {
        el.classList.add("active");
        const btn = el.querySelector(".cover-cand-btn");
        if (btn) btn.innerText = "✓ Выбрано";
      } else {
        el.classList.remove("active");
        const btn = el.querySelector(".cover-cand-btn");
        if (btn) btn.innerText = "Выбрать";
      }
    }
  });

  // Мгновенно обновляем превью в карточке Staging
  const propImg = document.getElementById(`prop-cover-img-${itemId}`);
  if (propImg) propImg.src = cand.url;
  const propMeta = document.getElementById(`prop-cover-meta-${itemId}`);
  if (propMeta) {
    const resText = cand.width && cand.height ? `${cand.width}×${cand.height} px` : "3000×3000 px HD";
    const srcText = cand.source === "itunes" ? "Apple Music / iTunes" : "Cover Art Archive";
    propMeta.innerText = `${resText} · ${srcText}`;
  }

  showToast(`Выбрана обложка: ${cand.title || "Альбом"}`, "info");
}

// -----------------------------------------------------------------------------
// Полноэкранный Lightbox для просмотра и перелистывания обложек
// -----------------------------------------------------------------------------
window.lightboxState = {
  slides: [],
  currentIndex: 0,
};

function openCoverLightbox(itemId, initialIndex) {
  const item = (state.stagingItems || []).find(it => it.id === itemId);
  if (!item) return;

  let curObj = {};
  let propObj = {};
  try { curObj = JSON.parse(item.currentJson || "{}"); } catch (e) {}
  try { propObj = JSON.parse(item.proposedJson || "{}"); } catch (e) {}

  const path = item.folderPath || item.targetId || "";
  const curSrc = curObj.existingFile
    ? `/api/v1/covers/local?path=${encodeURIComponent(curObj.existingFile)}`
    : (path ? `/api/v1/covers/local?path=${encodeURIComponent(path)}` : "");
  const curRes = curObj.width > 0 ? `${curObj.width}×${curObj.height} px` : "Локальный файл";

  const chosenUrl = window.selectedCovers[itemId] || propObj.coverUrl;
  const propRes = propObj.width > 0 ? `${propObj.width}×${propObj.height} px` : "3000×3000 px HD";
  const propSource = propObj.source === "itunes" ? "Apple Music / iTunes" : "Cover Art Archive";

  const slides = [
    {
      type: "current",
      badgeTitle: "Было (В папке альбома)",
      badgeClass: "badge-current",
      res: curRes,
      src: curSrc,
      tabTitle: "Было",
    },
    {
      type: "proposed",
      badgeTitle: "Станет (Новый cover.jpg)",
      badgeClass: "badge-proposed",
      res: `${propRes} · ${propSource}`,
      src: chosenUrl,
      tabTitle: "Станет",
    }
  ];

  // Добавляем ВСЕ найденные кандидаты в слайды, чтобы их можно было перелистывать!
  const candidates = window.coverCandidatesCache[itemId];
  if (candidates && candidates.length > 0) {
    candidates.forEach((cand, idx) => {
      slides.push({
        type: "candidate",
        badgeTitle: `Вариант ${idx + 1}: ${cand.title || 'Альбом'}`,
        badgeClass: "badge-proposed",
        res: `${cand.width}×${cand.height} px · ${cand.source === 'itunes' ? 'Apple Music' : 'Cover Art Archive'}`,
        src: cand.url,
        tabTitle: `Вариант ${idx + 1}`,
      });
    });
  }

  window.lightboxState.slides = slides;
  window.lightboxState.currentIndex = Math.max(0, Math.min(initialIndex || 0, slides.length - 1));

  renderLightboxSlide();

  const modal = document.getElementById("modal-cover-lightbox");
  if (modal) {
    modal.classList.remove("hidden");
    modal.focus();
  }
}

function renderLightboxSlide() {
  const { slides, currentIndex } = window.lightboxState;
  if (!slides || slides.length === 0) return;

  const slide = slides[currentIndex];
  const typeBadge = document.getElementById("lightbox-badge-type");
  const resBadge = document.getElementById("lightbox-badge-res");
  const counter = document.getElementById("lightbox-counter");
  const img = document.getElementById("lightbox-img");
  const emptyState = document.getElementById("lightbox-empty-state");
  const tabsContainer = document.getElementById("lightbox-tabs");

  if (typeBadge) {
    typeBadge.innerText = slide.badgeTitle;
    typeBadge.className = `lightbox-badge ${slide.badgeClass || ''}`;
  }
  if (resBadge) resBadge.innerText = slide.res;
  if (counter) counter.innerText = `${currentIndex + 1} / ${slides.length}`;

  if (slide.src) {
    img.style.display = "block";
    img.src = slide.src;
    img.onerror = () => {
      img.style.display = "none";
      if (emptyState) emptyState.classList.remove("hidden");
    };
    img.onload = () => {
      if (emptyState) emptyState.classList.add("hidden");
      if (img.naturalWidth && img.naturalHeight && resBadge) {
        const parts = slide.res.split('·');
        const srcPart = parts.length > 1 ? ` · ${parts[1].trim()}` : '';
        resBadge.innerText = `${img.naturalWidth}×${img.naturalHeight} px${srcPart}`;
      }
    };
    if (emptyState) emptyState.classList.add("hidden");
  } else {
    img.style.display = "none";
    if (emptyState) emptyState.classList.remove("hidden");
  }

  // Обновляем нижние переключатели
  if (tabsContainer) {
    tabsContainer.innerHTML = slides.map((s, idx) => `
      <button class="lightbox-tab ${idx === currentIndex ? 'active' : ''}" onclick="setCoverLightboxIndex(${idx})">
        ${escapeHtml(s.tabTitle)}
      </button>
    `).join("");
  }
}

function nextCoverLightbox() {
  const { slides, currentIndex } = window.lightboxState;
  if (!slides || slides.length === 0) return;
  window.lightboxState.currentIndex = (currentIndex + 1) % slides.length;
  renderLightboxSlide();
}

function prevCoverLightbox() {
  const { slides, currentIndex } = window.lightboxState;
  if (!slides || slides.length === 0) return;
  window.lightboxState.currentIndex = (currentIndex - 1 + slides.length) % slides.length;
  renderLightboxSlide();
}

function setCoverLightboxIndex(idx) {
  const { slides } = window.lightboxState;
  if (!slides || idx < 0 || idx >= slides.length) return;
  window.lightboxState.currentIndex = idx;
  renderLightboxSlide();
}

function closeCoverLightbox() {
  const modal = document.getElementById("modal-cover-lightbox");
  if (modal) {
    modal.classList.add("hidden");
  }
}

function initCoverLightboxEvents() {
  document.getElementById("lightbox-btn-close")?.addEventListener("click", closeCoverLightbox);
  document.getElementById("lightbox-btn-next")?.addEventListener("click", nextCoverLightbox);
  document.getElementById("lightbox-btn-prev")?.addEventListener("click", prevCoverLightbox);
  document.getElementById("lightbox-nav-left")?.addEventListener("click", prevCoverLightbox);
  document.getElementById("lightbox-nav-right")?.addEventListener("click", nextCoverLightbox);

  const modal = document.getElementById("modal-cover-lightbox");
  modal?.addEventListener("click", (e) => {
    if (e.target === modal || e.target.classList?.contains("lightbox-container") || e.target.classList?.contains("lightbox-body")) {
      closeCoverLightbox();
    }
  });

  document.addEventListener("keydown", (e) => {
    if (!modal || modal.classList.contains("hidden")) return;
    if (e.key === "Escape") {
      closeCoverLightbox();
    } else if (e.key === "ArrowRight") {
      nextCoverLightbox();
    } else if (e.key === "ArrowLeft") {
      prevCoverLightbox();
    }
  });
}

async function dismissStaging(id) {
  try {
    await fetchApi("/staging/ignore", "POST", { id });
    showToast("Предложение отклонено", "info");
    await loadStagingData();
  } catch (err) {
    showToast(`Ошибка: ${err.message}`, "error");
  }
}

async function applyAllStaging() {
  if (state.stagingItems.length === 0) {
    showToast("Нет предложений для применения", "info");
    return;
  }

  if (!confirm(`Применить все ${state.stagingItems.length} предложений в файлы медиатеки?`)) {
    return;
  }

  const ids = state.stagingItems.map(item => item.id);
  try {
    const res = await fetchApi("/staging/apply", "POST", { ids });
    showToast(`✓ Успешно применено изменений: ${res.applied}`, "success");
    await loadStagingData();
    await loadLibraryData();
  } catch (err) {
    showToast(`Ошибка применения: ${err.message}`, "error");
  }
}

async function resetFullDatabase() {
  const confirmed = confirm(
    "ВНИМАНИЕ: Вы уверены, что хотите выполнить ПОЛНЫЙ СБРОС БД?\n\n" +
    "• Будет очищена вся очередь предложений Staging;\n" +
    "• Будут сброшены кэш треков и текстов;\n" +
    "• Все API-ключи (Gemini / Groq) будут сохранены;\n" +
    "• Сразу после сброса запустится свежее сканирование из Navidrome.\n\n" +
    "Выполнить полный сброс базы данных?"
  );
  if (!confirmed) return;

  const btn = document.getElementById("btn-reset-db");
  const origText = btn ? btn.textContent : "Полный сброс БД";
  if (btn) {
    btn.disabled = true;
    btn.textContent = "Сброс базы...";
  }

  try {
    showToast("Выполняется сброс базы данных и синхронизация из Navidrome...", "info");
    const res = await fetchApi("/db/reset", "POST", { keepKeys: true });
    showToast(`✓ База успешно сброшена! Синхронизировано треков: ${res.syncedTracks || 0}`, "success");
    await loadStagingData();
    await loadLibraryData();
  } catch (err) {
    showToast(`Ошибка сброса базы: ${err.message}`, "error");
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.textContent = origText;
    }
  }
}

async function rescanStagingAudit() {
  try {
    showToast("Запущен глубокий аудит обложек и тегов...", "info");
    await fetchApi("/scan/audit", "POST", { limitFolders: 50 });
    setTimeout(async () => {
      await loadStagingData();
      await loadLibraryData();
    }, 2000);
  } catch (err) {
    showToast(`Ошибка запуска аудита: ${err.message}`, "error");
  }
}

async function discardAllStaging() {
  if (state.stagingItems.length === 0) return;
  if (!confirm("Отклонить все текущие предложения Staging?")) return;

  for (const item of state.stagingItems) {
    try {
      await fetchApi("/staging/ignore", "POST", { id: item.id });
    } catch (e) {}
  }
  showToast("Все предложения отклонены", "info");
  await loadStagingData();
}

async function undoStaging() {
  if (!state.lastAppliedStagingId) {
    showToast("Нет недавно примененных предложений для отката", "info");
    return;
  }

  try {
    await fetchApi("/staging/undo", "POST", { id: state.lastAppliedStagingId });
    showToast("✓ Последнее изменение успешно откатано", "success");
    state.lastAppliedStagingId = null;
    await loadStagingData();
    await loadLibraryData();
  } catch (err) {
    showToast(`Ошибка отката: ${err.message}`, "error");
  }
}

// -----------------------------------------------------------------------------
// 6. Очередь фоновых задач (Jobs)
// -----------------------------------------------------------------------------
async function loadJobsData() {
  const tbody = document.getElementById("jobs-tbody");
  const pauseBtn = document.getElementById("btn-toggle-worker-pause");
  if (!tbody) return;

  try {
    const res = await fetchApi("/jobs");
    state.jobs = res.jobs || [];
    state.jobLogs = res.jobLogs || {};
    state.workerPaused = !!res.paused;

    if (pauseBtn) {
      pauseBtn.textContent = state.workerPaused ? "Возобновить воркер" : "Приостановить воркер";
      pauseBtn.className = state.workerPaused ? "btn btn-primary" : "btn btn-secondary";
    }

    const badgeJobs = document.getElementById("badge-jobs-count");
    const activeCount = state.jobs.filter(j => j.status === "pending" || j.status === "running").length;
    if (badgeJobs) badgeJobs.textContent = activeCount;

    if (state.jobs.length === 0) {
      tbody.innerHTML = `<tr><td colspan="8" class="cell-empty">Очередь фоновых задач пуста.</td></tr>`;
      return;
    }

    // Сохраняем состояние прокрутки открытых консолей задач перед перерисовкой
    const consoleScrollState = {};
    tbody.querySelectorAll(".job-console-body[data-job-id]").forEach(el => {
      const jid = el.getAttribute("data-job-id");
      const distFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
      consoleScrollState[jid] = {
        stickToBottom: distFromBottom <= 36,
        scrollTop: el.scrollTop
      };
    });

    tbody.innerHTML = state.jobs.map((j) => {
      const isExpanded = state.expandedJobIds.has(j.id);
      let statusBadge = `<span class="badge-audit-queued">Ожидание</span>`;
      if (j.status === "running") statusBadge = `<span class="badge-audit-processing">Выполняется...</span>`;
      else if (j.status === "done" || j.status === "completed") statusBadge = `<span class="text-emerald" style="font-weight: 500;">✓ Завершена</span>`;
      else if (j.status === "failed") statusBadge = `<span class="text-crimson" style="color:var(--color-crimson, #f43f5e);">Ошибка</span>`;
      else if (j.status === "waiting_quota") statusBadge = `<span class="text-amber">Ждет квоту API</span>`;
      else if (j.status === "paused") statusBadge = `<span class="badge-audit-queued" style="background: rgba(148, 163, 184, 0.15); color: var(--text-secondary);">На паузе</span>`;

      let jobTypeLabel = j.jobType;
      if (j.jobType === "tournament") jobTypeLabel = "LRC Турнир";
      else if (j.jobType === "whisper_karaoke") jobTypeLabel = "AI Караоке (Groq + Gemini)";
      else if (j.jobType === "full_pipeline") jobTypeLabel = "Полный конвейер";

      const timeUnix = j.updatedAt || j.createdAt || 0;
      const timeStr = timeUnix ? new Date(timeUnix * 1000).toLocaleTimeString() : "—";

      // Подтягиваем читаемое название трека (Артист — Название) вместо пустого или сырого UUID
      const trackId = j.trackId || j.targetId || "";
      const trackObj = (state.tracks || []).find(t => t.id === trackId);
      let trackDisplay = trackId || "—";
      if (trackObj) {
        trackDisplay = `${trackObj.artist || "Неизвестный артист"} — ${trackObj.title || "Без названия"}`;
      }

      // Формируем отображение текущего этапа или результата
      const rawStepOrErr = j.errorLog || j.lastError || "";
      let stepSummaryHtml = `<span class="text-muted">—</span>`;
      if (j.status === "done" || j.status === "completed") {
        stepSummaryHtml = `<span class="text-emerald">✓ Текст сохранен (.lrc · poisq)</span>`;
      } else if (j.status === "running") {
        stepSummaryHtml = `<span style="color: var(--color-primary, #38bdf8); font-weight: 500;">${escapeHtml(rawStepOrErr || "Обработка этапа...")}</span>`;
      } else if (rawStepOrErr) {
        stepSummaryHtml = `<span class="${j.status === 'failed' ? 'text-crimson' : 'text-muted'}">${escapeHtml(rawStepOrErr)}</span>`;
      }

      // Кнопки управления конкретной задачей
      let actionBtns = `<div class="table-actions">`;
      if (j.status === "paused") {
        actionBtns += `<button class="btn-table-action primary" onclick="event.stopPropagation(); jobAction(${j.id}, 'resume')" title="Возобновить выполнение задачи">▶ Старт</button>`;
        actionBtns += `<button class="btn-table-action danger" onclick="event.stopPropagation(); jobAction(${j.id}, 'delete')" title="Удалить приостановленную задачу">✕</button>`;
      } else if (j.status === "pending" || j.status === "waiting_quota") {
        actionBtns += `<button class="btn-table-action" onclick="event.stopPropagation(); jobAction(${j.id}, 'pause')" title="Приостановить задачу">Пауза</button>`;
        actionBtns += `<button class="btn-table-action danger" onclick="event.stopPropagation(); jobAction(${j.id}, 'delete')" title="Удалить задачу">✕</button>`;
      } else {
        // running, done, failed
        actionBtns += `<button class="btn-table-action primary" onclick="event.stopPropagation(); promptJobRetry(${j.id})" title="Повторить задачу">↻ Повтор</button>`;
        actionBtns += `<button class="btn-table-action danger" onclick="event.stopPropagation(); jobAction(${j.id}, 'delete')" title="Удалить запись о задаче">✕</button>`;
      }
      actionBtns += `</div>`;

      const mainRow = `
        <tr class="job-row ${isExpanded ? "expanded" : ""}" onclick="toggleJobDetails(${j.id})" style="cursor: pointer;" title="Нажмите, чтобы раскрыть подробный лог выполнения задачи">
          <td class="col-num mono"><span class="job-expand-chevron">${isExpanded ? "▾" : "▸"}</span> ${j.id}</td>
          <td>
            <div style="font-weight: 600; color: var(--text-primary);">${escapeHtml(trackDisplay)}</div>
            ${trackObj && trackId ? `<div class="mono text-muted" style="font-size: 10px;">${escapeHtml(trackId)}</div>` : ""}
          </td>
          <td>${escapeHtml(jobTypeLabel)}</td>
          <td>${statusBadge}</td>
          <td class="mono">${j.attemptCount ?? j.attempts ?? 0}</td>
          <td class="mono" style="font-size: 11px; max-width: 340px;">${stepSummaryHtml}</td>
          <td class="mono text-muted">${timeStr}</td>
          <td class="col-actions">${actionBtns}</td>
        </tr>
      `;

      if (!isExpanded) {
        return mainRow;
      }

      return mainRow + buildJobDetailsRowHtml(j, trackObj);
    }).join("");

    // Восстанавливаем скролл или автоматически прокручиваем лог вниз к новым записям
    tbody.querySelectorAll(".job-console-body[data-job-id]").forEach(el => {
      const jid = el.getAttribute("data-job-id");
      const prev = consoleScrollState[jid];
      if (!prev || prev.stickToBottom) {
        el.scrollTop = el.scrollHeight;
      } else {
        el.scrollTop = prev.scrollTop;
      }
    });
  } catch (err) {
    console.error("Ошибка загрузки задач:", err);
  }
}

function toggleJobDetails(jobId) {
  const id = Number(jobId);
  if (state.expandedJobIds.has(id)) {
    state.expandedJobIds.delete(id);
  } else {
    state.expandedJobIds.add(id);
  }
  loadJobsData();
}

function buildJobDetailsRowHtml(j, trackObj) {
  const logs = state.jobLogs[j.id] || state.jobLogs[String(j.id)] || [];
  const rawStep = j.errorLog || j.lastError || "";
  const isDone = j.status === "done" || j.status === "completed";
  const isFailed = j.status === "failed";
  const isRunning = j.status === "running";

  // Определяем активный шаг визуального конвейера
  let activeStageIdx = 0;
  const joinedLogs = (logs.join(" ") + " " + rawStep).toLowerCase();
  if (isDone) {
    activeStageIdx = 4;
  } else if (joinedLogs.includes("этап 4") || joinedLogs.includes("запись")) {
    activeStageIdx = 3;
  } else if (joinedLogs.includes("этап 3") || joinedLogs.includes("gemini") || joinedLogs.includes("forced alignment")) {
    activeStageIdx = 2;
  } else if (joinedLogs.includes("этап 2") || joinedLogs.includes("whisper") || joinedLogs.includes("opus")) {
    activeStageIdx = 1;
  } else {
    activeStageIdx = 0;
  }

  const stages = j.jobType === "tournament"
    ? [
        { title: "1. Умный поиск по LRC-базам", desc: "LRCLIB, Яндекс Музыка, NetEase, Genius" },
        { title: "2. Запись .lrc", desc: "Атомарное сохранение рядом с аудиофайлом" }
      ]
    : [
        { title: "1. Турнир источников", desc: "Поиск эталонного текста в 4 базах" },
        { title: "2. FFmpeg + Groq Whisper", desc: "Сжатие моно-Opus 16kHz и пословные DTW-таймкоды" },
        { title: "3. Gemini 3.8 Alignment", desc: "Математические якоря + пословная синхронизация" },
        { title: "4. Сохранение .lrc", desc: "Запись на диск (Источник: poisq)" }
      ];

  const pipelineHtml = stages.map((st, idx) => {
    let cls = "pending";
    let icon = "○";
    if (isDone || idx < activeStageIdx) {
      cls = "done";
      icon = "✓";
    } else if (idx === activeStageIdx) {
      if (isFailed) {
        cls = "failed";
        icon = "✕";
      } else if (isRunning) {
        cls = "running";
        icon = "";
      }
    }
    return `
      <div class="job-stage-card ${cls}">
        <div class="job-stage-title"><span class="job-stage-icon">${icon}</span> ${escapeHtml(st.title)}</div>
        <div class="job-stage-desc">${escapeHtml(st.desc)}</div>
      </div>
    `;
  }).join("");

  let logLinesHtml = "";
  if (logs.length > 0) {
    logLinesHtml = logs.map(line => `<div class="job-log-line">${escapeHtml(line)}</div>`).join("");
  } else if (rawStep) {
    logLinesHtml = `<div class="job-log-line">${escapeHtml(rawStep)}</div>`;
  } else if (isDone) {
    logLinesHtml = `<div class="job-log-line text-emerald">✓ Задача успешно выполнена. Синхронный .lrc файл записан рядом с треком.</div>`;
  } else {
    logLinesHtml = `<div class="job-log-line text-muted">Ожидание захвата задачи фоновым воркером...</div>`;
  }

  const filePath = trackObj?.filePath || "—";
  const createdStr = j.createdAt ? new Date(j.createdAt * 1000).toLocaleString() : "—";
  const updatedStr = j.updatedAt ? new Date(j.updatedAt * 1000).toLocaleString() : "—";

  return `
    <tr class="job-details-row">
      <td colspan="8">
        <div class="job-details-box">
          <div class="job-stages-grid">
            ${pipelineHtml}
          </div>
          <div class="job-meta-bar mono">
            <span><strong>Файл:</strong> ${escapeHtml(filePath)}</span>
            <span><strong>Создана:</strong> ${escapeHtml(createdStr)}</span>
            <span><strong>Обновлена:</strong> ${escapeHtml(updatedStr)}</span>
            ${trackObj ? `<button class="btn-table-action primary" onclick="event.stopPropagation(); openTrackInKaraoke('${escapeHtml(trackObj.id)}')">Открыть в Караоке</button>` : ""}
          </div>
          <div class="job-console">
            <div class="job-console-header mono">Живой журнал выполнения под капотом (Задача #${j.id})</div>
            <div class="job-console-body mono" id="job-console-body-${j.id}" data-job-id="${j.id}">${logLinesHtml}</div>
          </div>
        </div>
      </td>
    </tr>
  `;
}

function promptJobRetry(jobId) {
  const j = (state.jobs || []).find(item => item.id === jobId);
  if (!j) {
    jobAction(jobId, 'retry', 'restart');
    return;
  }

  const modal = document.getElementById("modal-job-retry");
  const trackEl = document.getElementById("modal-retry-track");
  const descEl = document.getElementById("modal-retry-desc");
  const errEl = document.getElementById("modal-retry-error");
  const actionsEl = document.getElementById("modal-retry-actions");
  if (!modal || !actionsEl) return;

  const trackObj = (state.tracks || []).find(t => t.id === (j.trackId || j.targetId));
  const trackName = trackObj ? `${trackObj.artist || "Неизвестный артист"} — ${trackObj.title || "Без названия"}` : (j.trackId || `Задача #${j.id}`);
  if (trackEl) trackEl.textContent = trackName;

  const errText = j.errorLog || j.lastError || "";
  if (errText) {
    errEl.textContent = errText;
    errEl.classList.remove("hidden");
  } else {
    errEl.classList.add("hidden");
  }

  // Проверяем, произошел ли сбой на этапе Gemini или это задача караоке
  const isGeminiStageFailure = errText.includes("Gemini") || (j.jobType === "whisper_karaoke" && (j.attemptCount || 0) > 0);

  if (isGeminiStageFailure) {
    descEl.innerHTML = `Этап акустического распознавания Whisper успешно пройден и сохранен в кэше. Сбой произошел на этапе связи с Google Gemini.<br><br>С какого этапа перезапустить задачу?`;
    actionsEl.innerHTML = `
      <button type="button" class="btn btn-secondary" onclick="closeRetryModal()">Отмена</button>
      <button type="button" class="btn btn-secondary" onclick="closeRetryModal(); jobAction(${j.id}, 'retry', 'restart')">Заново с 1 этапа</button>
      <button type="button" class="btn btn-primary" onclick="closeRetryModal(); jobAction(${j.id}, 'retry', 'resume')">С этапа Gemini</button>
    `;
  } else {
    descEl.textContent = `Перезапустить выполнение задачи #${j.id} в очереди фонового воркера?`;
    actionsEl.innerHTML = `
      <button type="button" class="btn btn-secondary" onclick="closeRetryModal()">Отмена</button>
      <button type="button" class="btn btn-primary" onclick="closeRetryModal(); jobAction(${j.id}, 'retry', 'restart')">Перезапустить задачу</button>
    `;
  }

  document.getElementById("btn-close-retry-modal")?.addEventListener("click", closeRetryModal);
  modal.classList.remove("hidden");
}

function closeRetryModal() {
  document.getElementById("modal-job-retry")?.classList.add("hidden");
}

async function jobAction(jobId, action, stage = "resume") {
  try {
    await fetchApi("/jobs/action", "POST", { id: Number(jobId), action, stage });
    let msg = "Действие выполнено";
    if (action === "pause") msg = `Задача #${jobId} приостановлена`;
    else if (action === "resume") msg = `Задача #${jobId} возобновлена`;
    else if (action === "retry") {
      msg = stage === "resume" ? `Задача #${jobId} возобновлена с этапа Gemini` : `Задача #${jobId} перезапущена с 1 этапа`;
    } else if (action === "delete") msg = `Задача #${jobId} удалена`;
    showToast(`✓ ${msg}`, "success");
    await loadJobsData();
  } catch (err) {
    showToast(`Ошибка: ${err.message}`, "error");
  }
}

async function toggleWorkerPause() {
  try {
    const res = await fetchApi("/jobs", "POST", { paused: !state.workerPaused });
    state.workerPaused = !!res.paused;
    showToast(state.workerPaused ? "Воркер фоновых задач приостановлен" : "Воркер фоновых задач возобновлен", "info");
    loadJobsData();
  } catch (err) {
    showToast(`Ошибка: ${err.message}`, "error");
  }
}

// -----------------------------------------------------------------------------
// 7. Пул API-ключей (AES-256-GCM)
// -----------------------------------------------------------------------------
function initKeyModal() {
  const modal = document.getElementById("modal-add-key");
  const btnOpen = document.getElementById("btn-open-add-key-modal");
  const btnClose = document.getElementById("btn-close-key-modal");
  const btnCancel = document.getElementById("btn-cancel-key");
  const form = document.getElementById("form-add-key");

  btnOpen?.addEventListener("click", () => {
    modal?.classList.remove("hidden");
    document.getElementById("key-secret")?.focus();
  });

  const closeModal = () => {
    modal?.classList.add("hidden");
    form?.reset();
  };

  btnClose?.addEventListener("click", closeModal);
  btnCancel?.addEventListener("click", closeModal);

  modal?.addEventListener("click", (e) => {
    if (e.target === modal) closeModal();
  });

  form?.addEventListener("submit", async (e) => {
    e.preventDefault();
    const provider = document.getElementById("key-provider")?.value;
    const label = document.getElementById("key-label")?.value.trim() || "";
    const apiKey = document.getElementById("key-secret")?.value.trim() || "";

    if (!provider || !apiKey) {
      showToast("Выберите провайдера и укажите ключ", "error");
      return;
    }

    try {
      await fetchApi("/keys", "POST", { provider, label, apiKey });
      showToast("✓ Ключ зашифрован (AES-256-GCM) и добавлен в пул ротации", "success");
      closeModal();
      await loadKeysData();
      if (provider === "music_assistant") await loadCastSettings();
    } catch (err) {
      showToast(`Ошибка сохранения ключа: ${err.message}`, "error");
    }
  });
}

async function loadKeysData() {
  const tbody = document.getElementById("keys-tbody");
  if (!tbody) return;

  try {
    const res = await fetchApi("/keys");
    state.keys = res.keys || [];
  } catch (err) {
    console.error("Ошибка загрузки ключей:", err);
    state.keys = [];
  }

  if (state.keys.length === 0) {
    tbody.innerHTML = `<tr><td colspan="5" class="cell-empty">В пуле пока нет ключей. Нажмите «+ Добавить ключ» для подключения Gemini, Groq, AcoustID или Yandex.</td></tr>`;
    return;
  }

  const providerNames = {
    gemini: "Google Gemini",
    groq: "Groq Cloud (Whisper)",
    acoustid: "AcoustID (fpcalc)",
    yandex: "Яндекс Музыка",
    genius: "Genius API",
    music_assistant: "Music Assistant"
  };

  tbody.innerHTML = state.keys.map(k => {
    let statusBadge = `<span class="text-emerald">Активен</span>`;
    if (k.status === "cooldown") {
      statusBadge = `<span class="text-amber">429 Cooldown (65с)</span>`;
    } else if (k.status === "exhausted_daily") {
      statusBadge = `<span class="text-amber">Исчерпан до 11:00 МСК</span>`;
    } else if (k.status === "invalid") {
      statusBadge = `<span class="text-crimson" style="color: var(--color-crimson);">Невалиден (401/403)</span>`;
    }

    const providerTitle = providerNames[k.provider] || k.provider;
    const keyDisplay = k.label ? `${escapeHtml(k.label)} <span class="mono text-muted">(${escapeHtml(k.keyMask)})</span>` : `<span class="mono">${escapeHtml(k.keyMask)}</span>`;

    return `
      <tr>
        <td style="font-weight: 600; color: var(--text-primary);">${escapeHtml(providerTitle)}</td>
        <td>${keyDisplay}</td>
        <td>${statusBadge}</td>
        <td class="mono text-muted">${k.successCount || 0} / ${k.errorCount || 0}</td>
        <td class="col-actions">
          <button class="btn btn-secondary" onclick="deleteKey(${k.id})">Удалить</button>
        </td>
      </tr>
    `;
  }).join("");
}

async function deleteKey(id) {
  if (!confirm("Удалить данный API-ключ из пула ротации?")) return;

  try {
    await fetchApi(`/keys?id=${id}`, "DELETE");
    showToast("Ключ удален из пула", "info");
    await loadKeysData();
  } catch (err) {
    showToast(`Ошибка удаления ключа: ${err.message}`, "error");
  }
}

// -----------------------------------------------------------------------------
// 7.1 Трансляция на колонки через Music Assistant
// -----------------------------------------------------------------------------
function initCastForm() {
  document.getElementById("form-cast")?.addEventListener("submit", async (e) => {
    e.preventDefault();
    const boxes = [...document.querySelectorAll("#cast-players-list input[type=checkbox]")];
    const payload = {
      url: document.getElementById("cast-url")?.value.trim() || "",
      provider: document.getElementById("cast-provider")?.value || "",
      // MA недоступен и список не построен — не теряем уже выбранные колонки
      players: boxes.length ? boxes.filter(b => b.checked).map(b => b.value) : (state.castPlayers || [])
    };
    const btn = document.getElementById("btn-save-cast");
    if (btn) btn.disabled = true;
    const res = await loadCastSettings(payload);
    if (btn) btn.disabled = false;
    if (res) showToast(res.error ? "Настройки сохранены, но MA не ответил" : "✓ Настройки трансляции сохранены", res.error ? "info" : "success");
  });
}

async function loadCastSettings(payload = null) {
  const status = document.getElementById("cast-status");
  if (!status) return null;
  let res;
  try {
    res = payload ? await fetchApi("/cast", "POST", payload) : await fetchApi("/cast");
  } catch (err) {
    status.textContent = `Ошибка: ${err.message}`;
    return null;
  }

  const st = res.settings || {};
  state.castPlayers = st.players || [];
  document.getElementById("cast-url").value = st.url || "";

  const sel = document.getElementById("cast-provider");
  const providers = res.providers || [];
  let options = `<option value="">— выберите —</option>` +
    providers.map(p => `<option value="${escapeHtml(p.instance_id)}">${escapeHtml(p.name || p.instance_id)}</option>`).join("");
  if (st.provider && !providers.some(p => p.instance_id === st.provider)) {
    options += `<option value="${escapeHtml(st.provider)}">${escapeHtml(st.provider)} (не найден в MA)</option>`;
  }
  sel.innerHTML = options;
  sel.value = st.provider || "";

  const list = document.getElementById("cast-players-list");
  const enabled = new Set(state.castPlayers);
  const players = res.players || [];
  list.innerHTML = players.length
    ? players.map(p => `
        <label class="cast-player">
          <input type="checkbox" value="${escapeHtml(p.player_id)}" ${enabled.has(p.player_id) ? "checked" : ""} />
          <span>${escapeHtml(p.name || p.player_id)}</span>
          ${p.available ? "" : `<span class="text-muted">(недоступна)</span>`}
        </label>`).join("")
    : "Колонки появятся после подключения к Music Assistant.";

  if (!res.hasToken) status.textContent = "Добавьте токен Music Assistant в пул ключей выше.";
  else if (res.error) status.textContent = `Music Assistant: ${res.error}`;
  else if (!st.provider) status.textContent = "Выберите источник музыки и колонки, затем сохраните.";
  else status.textContent = `✓ Подключено. Колонок в приложении: ${state.castPlayers.length}.`;
  return res;
}

async function syncNavidrome() {
  const btn = document.getElementById("btn-sync-navidrome");
  const oldText = btn ? btn.textContent : "";
  if (btn) {
    btn.disabled = true;
    btn.textContent = "Синхронизация...";
  }

  showToast("Запущена синхронизация с Navidrome...", "info");

  try {
    const res = await fetchApi("/scan/sync", "POST");
    showToast(`✓ Синхронизация завершена: обновлено ${res.syncedTracks} треков`, "success");
    await loadLibraryData();
  } catch (err) {
    showToast(`Ошибка синхронизации: ${err.message}`, "error");
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.textContent = oldText;
    }
  }
}

async function runMusicBrainzAudit() {
  const btn = document.getElementById("btn-run-audit");
  const oldText = btn ? btn.textContent : "";
  if (btn) {
    btn.disabled = true;
    btn.textContent = "Запуск аудита...";
  }

  showToast("Запущен аудит MusicBrainz и поиск обложек в фоне...", "info");

  try {
    const res = await fetchApi("/scan/audit", "POST", { limitFolders: 50 });
    showToast(res.message || "Аудит запущен! Результаты появятся в Staging-буфере.", "success");
    setTimeout(() => {
      loadLibraryData();
      loadStagingData();
    }, 2000);
  } catch (err) {
    showToast(`Ошибка запуска аудита: ${err.message}`, "error");
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.textContent = oldText;
    }
  }
}
