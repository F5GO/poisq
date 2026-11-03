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
  workerPaused: false,
  lastAppliedStagingId: null,

  // Караоке плеер
  audioEl: null,
  isPlaying: false,
  currentSec: 0,
  totalSec: 0,
  lyrics: [],
  activeLyricIndex: -1,

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
    toast.style.color = "#fda4af";
  } else if (type === "success") {
    toast.style.borderColor = "var(--color-emerald, #10b981)";
    toast.style.color = "#6ee7b7";
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

  initNavigation();
  initViewControls();
  initPlayerControls();
  initDrawer();
  initKeyModal();
  loadLibraryData();
});

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
  document.getElementById("btn-clear-covers-staging")?.addEventListener("click", clearCoversStaging);
  document.getElementById("btn-rescan-audit")?.addEventListener("click", rescanStagingAudit);
  document.getElementById("btn-apply-all-staging")?.addEventListener("click", applyAllStaging);
  document.getElementById("btn-discard-all-staging")?.addEventListener("click", discardAllStaging);
  document.getElementById("btn-undo-staging")?.addEventListener("click", undoStaging);

  // Караоке действия
  document.getElementById("btn-enqueue-tournament")?.addEventListener("click", enqueueLrcTournament);
  document.getElementById("btn-cloud-karaoke")?.addEventListener("click", enqueueWhisperKaraoke);
  document.getElementById("btn-toggle-editor")?.addEventListener("click", toggleLrcEditor);
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

  if (tabName === "library") loadLibraryData();
  if (tabName === "karaoke") {
    renderKaraokeSidebar();
  }
  if (tabName === "staging") loadStagingData();
  if (tabName === "jobs") loadJobsData();
  if (tabName === "keys") loadKeysData();
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
    state.karaokeSearchTimer = setTimeout(() => {
      renderKaraokeSidebar(e.target.value.trim());
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
            <th class="col-actions">Действие</th>
          </tr>
        </thead>
        <tbody id="tracks-tbody"></tbody>
      </table>
    </div>
  `;

  const tbody = document.getElementById("tracks-tbody");
  tbody.innerHTML = tracks.map((t, idx) => {
    const num = (idx + 1 < 10 ? "0" : "") + (idx + 1);

    // Статус текста песни
    let statusBadge = `<span class="text-muted">Нет текста</span>`;
    if (t.lyricsStatus === "verified") {
      statusBadge = `<span class="text-emerald">✓ Проверен</span>`;
    } else if (t.lyricsStatus === "synced") {
      statusBadge = `<span class="text-emerald">Синхронный</span>`;
    } else if (t.lyricsStatus === "plain") {
      statusBadge = `<span class="text-secondary">Обычный</span>`;
    }

    // Статус аудита / очереди
    let auditBadge = `<span class="badge-audit-ok">✓ Готов</span>`;
    let rowClass = "";
    if (t.isMojibake) {
      auditBadge = `<span class="badge-audit-queued">Mojibake</span>`;
    } else if (!t.hasCover || (t.coverWidth > 0 && t.coverWidth < 500)) {
      auditBadge = `<span class="badge-audit-queued">Без обложки</span>`;
    }

    return `
      <tr class="${rowClass}" onclick="openTrackDrawer('${escapeHtml(t.id)}')" style="cursor: pointer;">
        <td class="col-num mono">${num}</td>
        <td>
          <div style="font-weight: 500; color: #ffffff;">${escapeHtml(t.title || "Без названия")}</div>
          <div style="font-size: 11px; color: var(--text-muted);">${escapeHtml(t.artist || "Неизвестный артист")}</div>
        </td>
        <td style="color: var(--text-secondary);">${escapeHtml(t.album || "—")}</td>
        <td class="mono" style="color: var(--text-muted);">${formatDuration(t.duration || 0)}</td>
        <td>${statusBadge}</td>
        <td>${auditBadge}</td>
        <td class="col-actions">
          <button class="btn btn-secondary" onclick="event.stopPropagation(); openTrackInKaraoke('${escapeHtml(t.id)}')">Караоке</button>
        </td>
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
            <div class="artist-avatar-placeholder" style="display:none;">👤</div>
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

  const coverUrl = track.folderPath
    ? `/api/v1/covers/local?path=${encodeURIComponent(track.folderPath)}`
    : "";

  let lyricsStatusDesc = "Отсутствует";
  if (track.lyricsStatus === "verified") lyricsStatusDesc = "✓ Проверен";
  else if (track.lyricsStatus === "synced") lyricsStatusDesc = "Синхронизирован (.lrc)";
  else if (track.lyricsStatus === "plain") lyricsStatusDesc = "Обычный текст (без таймкодов)";

  const content = track.activeLyrics || track.lyricsBody || "";

  bodyEl.innerHTML = `
    <div style="display: flex; gap: 16px; align-items: center;">
      <div class="drawer-cover-wrap">
        ${coverUrl 
          ? `<img src="${coverUrl}" class="drawer-cover-img" alt="Обложка" onerror="this.parentElement.innerHTML='<div style=\\'display:flex;height:100%;align-items:center;justify-content:center;color:var(--text-muted);font-size:32px;\\'>♪</div>'"/>` 
          : `<div style="display:flex;height:100%;align-items:center;justify-content:center;color:var(--text-muted);font-size:32px;">♪</div>`
        }
      </div>
      <div>
        <div style="font-size: 16px; font-weight: 600; color: #ffffff;">${escapeHtml(track.title || "—")}</div>
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
        <span class="drawer-meta-val mono">${escapeHtml(track.activeSource || "—")}</span>
      </div>
      <div class="drawer-meta-row">
        <span class="drawer-meta-label">Файл</span>
        <span class="drawer-meta-val mono" style="font-size: 11px;">${escapeHtml(track.filePath || "—")}</span>
      </div>
    </div>

    <div style="display: flex; flex-direction: column; gap: 8px; margin-top: 10px;">
      <button class="btn btn-primary" onclick="openTrackInKaraoke('${escapeHtml(track.id)}'); document.getElementById('drawer-track-detail').classList.add('hidden');">
        🎤 Запустить в Караоке-плеере
      </button>
      <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 8px;">
        <button class="btn btn-secondary" onclick="enqueueSingleTournament('${escapeHtml(track.id)}')">
          LRC Турнир
        </button>
        <button class="btn btn-secondary" onclick="enqueueSingleGroq('${escapeHtml(track.id)}')">
          AI Караоке (Groq)
        </button>
      </div>
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
  try {
    await fetchApi("/lyrics/enqueue", "POST", { trackId, jobType: "whisper_karaoke" });
    showToast("✓ Задача AI Караоке (Groq Whisper) поставлена в очередь", "success");
    loadJobsData();
  } catch (err) {
    showToast(`Ошибка: ${err.message}`, "error");
  }
}

// -----------------------------------------------------------------------------
// 4. Караоке и LRC Плеер (Реальное аудио + Синхронизация)
// -----------------------------------------------------------------------------
function initPlayerControls() {
  document.getElementById("btn-play")?.addEventListener("click", togglePlay);
  document.getElementById("btn-step-back")?.addEventListener("click", () => seekDelta(-10));
  document.getElementById("btn-step-forward")?.addEventListener("click", () => seekDelta(10));
  document.getElementById("scrub-bar")?.addEventListener("click", handleScrub);

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
  const list = state.tracks.filter(t => 
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
  let track = state.tracks.find(t => t.id === trackId);

  try {
    const res = await fetchApi(`/lyrics/cache?trackId=${encodeURIComponent(trackId)}`);
    if (res.track) {
      track = res.track;
    }
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
      playerCover.innerHTML = `<img src="${coverUrl}" class="player-cover-img" alt="Обложка" onerror="this.parentElement.innerHTML='♪'"/>`;
    } else {
      playerCover.innerHTML = "♪";
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
      providerLabel.textContent = `Источник: ${src.toUpperCase()}`;
    } else {
      providerLabel.textContent = lyricsContent ? "Синхронизирован" : "Нет текста";
    }
  }

  // Парсим LRC
  if (lyricsContent) {
    const parsed = parseLRC(lyricsContent);
    if (parsed.length > 0) {
      state.lyrics = parsed;
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

  // Подключаем реальный аудиопоток к плееру
  if (state.audioEl) {
    state.audioEl.src = `/api/v1/tracks/stream?id=${encodeURIComponent(track.id)}`;
    state.audioEl.load();
    state.audioEl.play().then(() => {
      state.isPlaying = true;
      updatePlayIcons();
    }).catch(() => {
      state.isPlaying = false;
      updatePlayIcons();
    });
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
        Нажмите «Найти текст (LRC Турнир)» или «AI Караоке (Groq)» в шапке.
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

function toggleLrcEditor() {
  const tele = document.getElementById("view-karaoke");
  const raw = document.getElementById("view-raw-editor");
  const btn = document.getElementById("btn-toggle-editor");

  if (raw.classList.contains("hidden")) {
    raw.classList.remove("hidden");
    tele.classList.add("hidden");
    if (btn) btn.textContent = "Режим суфлера";
  } else {
    raw.classList.add("hidden");
    tele.classList.remove("hidden");
    if (btn) btn.textContent = "Редактор .lrc";
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

  try {
    const res = await fetchApi("/lyrics/save", "POST", {
      trackId: state.selectedTrack.id,
      content,
      verified: true
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

    renderLyricsTeleprompter();
    updatePlayerUI();
    toggleLrcEditor();
    loadTracks();
  } catch (err) {
    showToast(`Ошибка сохранения: ${err.message}`, "error");
  }
}

async function enqueueLrcTournament() {
  if (!state.selectedTrack) {
    showToast("Сначала выберите трек в медиатеке", "error");
    return;
  }

  try {
    await fetchApi("/lyrics/enqueue", "POST", {
      trackId: state.selectedTrack.id,
      jobType: "tournament"
    });
    showToast("✓ Задача турнира (LRCLIB, Genius, Yandex) поставлена в очередь воркера", "success");
    loadJobsData();
  } catch (err) {
    showToast(`Ошибка: ${err.message}`, "error");
  }
}

async function enqueueWhisperKaraoke() {
  if (!state.selectedTrack) {
    showToast("Сначала выберите трек в медиатеке", "error");
    return;
  }

  try {
    await fetchApi("/lyrics/enqueue", "POST", {
      trackId: state.selectedTrack.id,
      jobType: "whisper_karaoke"
    });
    showToast("✓ Задача AI Караоке (Groq Whisper Large v3 + Gemini) поставлена в очередь", "success");
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
        ? `/api/v1/covers/local?path=${encodeURIComponent(curObj.existingFile)}`
        : (path ? `/api/v1/covers/local?path=${encodeURIComponent(path)}` : "");

      const chosenCover = window.selectedCovers?.[item.id] || propObj.coverUrl;
      const propRes = propObj.width && propObj.height ? `${propObj.width}×${propObj.height} px` : "3000×3000 px HD";
      const propSource = propObj.source === "itunes" ? "Apple Music / iTunes" : "Cover Art Archive";

      const propArtist = propObj.artist || curObj.artist || (path.split("/").length >= 2 ? path.split("/")[path.split("/").length - 2] : "");
      const propAlbum = propObj.album || curObj.album || (path.split("/").length >= 1 ? path.split("/")[path.split("/").length - 1] : "");

      const hasAi = !!propObj.aiExplanation;
      const aiBadgeHtml = `
        <div class="cover-ai-badge ${hasAi ? '' : 'hidden'}" id="ai-badge-${item.id}">
          <span class="ai-sparkle">✨</span>
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
              <div class="cover-side-zoom-hint">🔍 Нажмите для увеличения</div>
              <div class="cover-side-meta" id="cur-meta-${item.id}">Файл: ${curHasCover ? "Найден" : "Отсутствует"} · ${curRes}</div>
            </div>

            <div class="cover-side-card" style="border-color: rgba(16, 185, 129, 0.4);">
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
              <div class="cover-side-zoom-hint">🔍 Нажмите для увеличения</div>
              <div class="cover-side-meta" id="prop-cover-meta-${item.id}">${propRes} · ${propSource}</div>

              <!-- Кнопки управления вариантами и AI СПРАВА, под новой обложкой -->
              <div style="margin-top: 10px; width: 100%; display: flex; flex-direction: column; gap: 8px;">
                <button 
                  class="cover-candidates-toggle-btn" 
                  id="btn-toggle-cand-${item.id}"
                  onclick="toggleCoverCandidates(${item.id}, '${escapeHtml(propArtist)}', '${escapeHtml(propAlbum)}', '${escapeHtml(path)}')"
                >
                  📷 Другие варианты обложек <span id="cand-count-${item.id}"></span>
                </button>
                <button 
                  class="cover-ai-resolve-btn" 
                  id="btn-ai-resolve-${item.id}"
                  onclick="resolveCoverViaAI(${item.id}, '${escapeHtml(path)}')"
                  title="Определить канонический студийный альбом через Gemini 2.5 Flash и найти оригинальную обложку"
                >
                  ✨ Уточнить альбом через AI
                </button>
              </div>
            </div>
          </div>

          <!-- Секция альтернативных вариантов обложек под сеткой -->
          <div id="candidates-container-${item.id}" class="cover-candidates-grid hidden"></div>

          <div style="margin-top: 10px; font-size: 11px; color: var(--text-muted);">
            ℹ️ <strong>Аудиофайлы не изменяются.</strong> В папку альбома будет скачан файл <code class="mono">cover.jpg</code>.
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
            ℹ️ ${escapeHtml(propObj.reason)}
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
          <span class="cover-cand-zoom-hint">🔍 Увеличить</span>
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
  const origBtnText = btn ? btn.innerHTML : "✨ Уточнить альбом через AI";
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
    showToast(`✨ Канонический альбом: ${dec.canonical_album} (${dec.release_type})`, "success");

    // Обновляем бейдж AI в карточке
    let badge = document.getElementById(`ai-badge-${itemId}`);
    if (badge) {
      badge.innerHTML = `
        <span class="ai-sparkle">✨</span>
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
      btn.innerHTML = `✨ Обновлено через AI`;
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

async function clearCoversStaging() {
  if (!confirm("Очистить все не примененные предложения обложек в базе? Это позволит запустить чистый аудит обложек заново.")) {
    return;
  }
  try {
    const res = await fetchApi("/staging/clear-covers", "POST");
    showToast(`✓ Очищено устаревших предложений обложек: ${res.deleted || 0}`, "success");
    await loadStagingData();
    await loadHealthStats();
  } catch (err) {
    showToast(`Ошибка очистки обложек: ${err.message}`, "error");
  }
}

async function rescanStagingAudit() {
  try {
    showToast("Запущен глубокий аудит обложек и тегов...", "info");
    await fetchApi("/scan/audit", "POST", { limitFolders: 50 });
    setTimeout(async () => {
      await loadStagingData();
      await loadHealthStats();
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
    state.workerPaused = !!res.paused;

    if (pauseBtn) {
      pauseBtn.textContent = state.workerPaused ? "Возобновить воркер" : "Приостановить воркер";
      pauseBtn.className = state.workerPaused ? "btn btn-primary" : "btn btn-secondary";
    }

    const badgeJobs = document.getElementById("badge-jobs-count");
    const activeCount = state.jobs.filter(j => j.status === "pending" || j.status === "running").length;
    if (badgeJobs) badgeJobs.textContent = activeCount;

    if (state.jobs.length === 0) {
      tbody.innerHTML = `<tr><td colspan="7" class="cell-empty">Очередь фоновых задач пуста.</td></tr>`;
      return;
    }

    tbody.innerHTML = state.jobs.map((j, idx) => {
      let statusBadge = `<span class="badge-audit-queued">Ожидание</span>`;
      if (j.status === "running") statusBadge = `<span class="badge-audit-processing">Выполняется...</span>`;
      else if (j.status === "completed") statusBadge = `<span class="text-emerald">Завершена</span>`;
      else if (j.status === "failed") statusBadge = `<span class="text-crimson" style="color:var(--color-crimson);">Ошибка</span>`;
      else if (j.status === "waiting_quota") statusBadge = `<span class="text-amber">Ждет квоту API</span>`;

      let jobTypeLabel = j.jobType;
      if (j.jobType === "tournament") jobTypeLabel = "LRC Турнир";
      else if (j.jobType === "whisper_karaoke") jobTypeLabel = "AI Караоке (Groq)";
      else if (j.jobType === "full_pipeline") jobTypeLabel = "Полный конвейер";

      const timeStr = j.createdAt ? new Date(j.createdAt * 1000).toLocaleTimeString() : "—";

      return `
        <tr>
          <td class="col-num mono">${j.id}</td>
          <td><span class="mono" style="font-size: 12px; color: #ffffff;">${escapeHtml(j.targetId)}</span></td>
          <td>${escapeHtml(jobTypeLabel)}</td>
          <td>${statusBadge}</td>
          <td class="mono">${j.attempts || 0}</td>
          <td class="mono text-muted" style="font-size: 11px;">${escapeHtml(j.lastError || "—")}</td>
          <td class="mono text-muted">${timeStr}</td>
        </tr>
      `;
    }).join("");
  } catch (err) {
    console.error("Ошибка загрузки задач:", err);
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
    genius: "Genius API"
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
        <td style="font-weight: 500; color: #ffffff;">${escapeHtml(providerTitle)}</td>
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
