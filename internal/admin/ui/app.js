/**
 * poisq Studio v0.2.0 — Минималистичный контроллер веб-интерфейса
 * 
 * Назначение:
 * Обеспечивает полноценное взаимодействие панели управления (:4535) с REST API бэкенда:
 * 1. Дашборд фонотеки: получение живой статистики здоровья медиатеки, синхронизация с Navidrome (:30043)
 *    и запуск фонового глубокого аудита MusicBrainz / AcoustID.
 * 2. Караоке и LRC Менеджер: загрузка текстов треков, симуляция синхронизированного воспроизведения
 *    с таймкодами (Apple-style teleprompter), ручной редактор .lrc с атомарным сохранением рядом с файлом,
 *    запуск Турнира провайдеров (LRCLIB/Genius/Yandex) и AI-караоке (Groq Whisper Large v3 + Gemini).
 * 3. Staging-буфер рефакторинга: просмотр предложений Picard/Mojibake/Cover, поштучное или массовое
 *    применение изменений в аудиофайлы, отклонение и мгновенный откат (Undo).
 * 4. Пул API-ключей: отображение статусов ротации (LRU, 65s Cooldown, Exhausted Daily), модальное окно
 *    добавления ключей с явным выбором провайдера (Gemini, Groq, AcoustID, Yandex, Genius) и удаление.
 * 5. Всплывающие уведомления (Toasts) для мгновенной обратной связи без блокирующих alert().
 */

// Состояние приложения
const state = {
  activeTab: "library",
  tracks: [],
  selectedTrack: null,
  stagingItems: [],
  keys: [],
  lastAppliedStagingId: null,
  
  // Плеер караоке
  isPlaying: false,
  currentSec: 0,
  totalSec: 0,
  lyrics: [],
  activeLyricIndex: -1,
  timer: null,

  // Поиск с дебаунсом
  searchDebounceTimer: null
};

// -----------------------------------------------------------------------------
// Вспомогательные утилиты и Toast-уведомления
// -----------------------------------------------------------------------------

/**
 * Выполняет сетевой запрос к REST API бэкенда (/api/v1).
 */
async function fetchApi(endpoint, method = "GET", payload = null) {
  const options = {
    method,
    headers: { "Content-Type": "application/json" }
  };
  if (payload !== null) {
    options.body = JSON.stringify(payload);
  }
  const res = await fetch("/api/v1" + endpoint, options);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const errorMsg = data.error || `HTTP ${res.status}: ${res.statusText || "Ошибка сервера"}`;
    throw new Error(errorMsg);
  }
  return data;
}

/**
 * Отображает немодальное всплывающее уведомление (Toast).
 */
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

function formatLrcTimestamp(sec) {
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  const ms = Math.floor((sec % 1) * 100);
  return `${m < 10 ? "0" : ""}${m}:${s < 10 ? "0" : ""}${s}.${ms < 10 ? "0" : ""}${ms}`;
}

/**
 * Парсит LRC-строки формата [mm:ss.xx]Текст в массив { sec, text }.
 */
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
  initNavigation();
  initPlayerControls();
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

  // Действия в шапках панелей
  document.getElementById("btn-sync-navidrome")?.addEventListener("click", syncNavidrome);
  document.getElementById("btn-run-audit")?.addEventListener("click", runMusicBrainzAudit);
  
  // Staging действия
  document.getElementById("btn-apply-all-staging")?.addEventListener("click", applyAllStaging);
  document.getElementById("btn-discard-all-staging")?.addEventListener("click", discardAllStaging);
  document.getElementById("btn-undo-staging")?.addEventListener("click", undoStaging);

  // Караоке действия
  document.getElementById("btn-enqueue-tournament")?.addEventListener("click", enqueueLrcTournament);
  document.getElementById("btn-cloud-karaoke")?.addEventListener("click", enqueueWhisperKaraoke);
  document.getElementById("btn-toggle-editor")?.addEventListener("click", toggleLrcEditor);
  document.getElementById("btn-save-raw-lrc")?.addEventListener("click", saveRawLrc);

  // Поиск по трекам с дебаунсом
  document.getElementById("track-search-input")?.addEventListener("input", (e) => {
    clearTimeout(state.searchDebounceTimer);
    state.searchDebounceTimer = setTimeout(() => {
      searchTracks(e.target.value);
    }, 250);
  });
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
  if (tabName === "staging") loadStagingData();
  if (tabName === "keys") loadKeysData();
}

// -----------------------------------------------------------------------------
// 2. Медиатека и синхронизация
// -----------------------------------------------------------------------------
async function loadLibraryData() {
  try {
    const health = await fetchApi("/health");
    const stats = health.stats || {};
    const total = stats.totalTracks || 0;
    const synced = (stats.lyricsSynced || 0) + (stats.lyricsVerified || 0);
    const pct = total > 0 ? Math.round((synced / total) * 100) : 0;
    const stagingCount = stats.pendingStaging || 0;

    const summary = document.getElementById("library-summary-desc");
    if (summary) {
      summary.innerHTML = `Каталог <span class="mono text-light">/music</span> · ${total} треков · ${pct}% с синхронными LRC · ${stagingCount} в Staging`;
    }
    const badgeStaging = document.getElementById("badge-staging-count");
    if (badgeStaging) badgeStaging.textContent = stagingCount;

    await loadTracks();
  } catch (err) {
    console.warn("Ошибка получения статистики:", err);
    await loadTracks();
  }
}

async function loadTracks(query = "") {
  const tbody = document.getElementById("tracks-tbody");
  const countLabel = document.getElementById("tracks-count-label");
  if (!tbody) return;

  try {
    const endpoint = query ? `/tracks?q=${encodeURIComponent(query)}` : "/tracks";
    const res = await fetchApi(endpoint);
    state.tracks = res.tracks || [];

    if (countLabel) countLabel.textContent = `Показано: ${state.tracks.length} треков`;
    renderTracksTable(state.tracks);
  } catch (err) {
    console.error("Ошибка загрузки треков:", err);
    tbody.innerHTML = `<tr><td colspan="6" class="cell-empty" style="color: var(--color-crimson);">Не удалось загрузить треки: ${escapeHtml(err.message)}</td></tr>`;
  }
}

function searchTracks(query) {
  loadTracks(query.trim());
}

function renderTracksTable(tracks) {
  const tbody = document.getElementById("tracks-tbody");
  if (!tbody) return;

  if (tracks.length === 0) {
    tbody.innerHTML = `<tr><td colspan="6" class="cell-empty">В медиатеке пока нет треков. Нажмите «Синхронизация Navidrome» для сканирования.</td></tr>`;
    return;
  }

  tbody.innerHTML = tracks.map((t, idx) => {
    const num = (idx + 1 < 10 ? "0" : "") + (idx + 1);
    let statusBadge = `<span class="text-muted">Нет текста</span>`;
    if (t.lyricsStatus === "verified") {
      statusBadge = `<span class="text-emerald">✓ Проверен</span>`;
    } else if (t.lyricsStatus === "synced") {
      statusBadge = `<span class="text-emerald">Синхронный</span>`;
    } else if (t.lyricsStatus === "plain") {
      statusBadge = `<span class="text-secondary">Обычный текст</span>`;
    }

    return `
      <tr onclick="openTrackInKaraoke('${escapeHtml(t.id)}')" style="cursor: pointer;">
        <td class="col-num mono">${num}</td>
        <td>
          <div style="font-weight: 500; color: #ffffff;">${escapeHtml(t.title || "Без названия")}</div>
          <div style="font-size: 11px; color: var(--text-muted);">${escapeHtml(t.artist || "Неизвестный артист")}</div>
        </td>
        <td style="color: var(--text-secondary);">${escapeHtml(t.album || "—")}</td>
        <td class="mono" style="color: var(--text-muted);">${formatDuration(t.duration || 0)}</td>
        <td>${statusBadge}</td>
        <td class="col-actions">
          <button class="btn btn-secondary" onclick="event.stopPropagation(); openTrackInKaraoke('${escapeHtml(t.id)}')">Караоке</button>
        </td>
      </tr>
    `;
  }).join("");
}

async function syncNavidrome() {
  const btn = document.getElementById("btn-sync-navidrome");
  const oldText = btn ? btn.textContent : "";
  if (btn) {
    btn.disabled = true;
    btn.textContent = "Синхронизация...";
  }

  showToast("Запущена синхронизация с базой Navidrome...", "info");

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
    
    // Переключаемся на Staging или обновляем бейдж через 2 секунды
    setTimeout(() => {
      loadLibraryData();
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

// -----------------------------------------------------------------------------
// 3. Караоке и LRC Плеер
// -----------------------------------------------------------------------------
function initPlayerControls() {
  document.getElementById("btn-play")?.addEventListener("click", togglePlay);
  document.getElementById("btn-step-back")?.addEventListener("click", () => seekDelta(-10));
  document.getElementById("btn-step-forward")?.addEventListener("click", () => seekDelta(10));
  document.getElementById("scrub-bar")?.addEventListener("click", handleScrub);

  window.addEventListener("keydown", (e) => {
    if (e.code === "Space" && e.target.tagName !== "INPUT" && e.target.tagName !== "TEXTAREA") {
      e.preventDefault();
      togglePlay();
    }
  });
}

async function openTrackInKaraoke(trackId) {
  let track = state.tracks.find(t => t.id === trackId);
  
  try {
    const res = await fetchApi(`/lyrics/cache?trackId=${encodeURIComponent(trackId)}`);
    if (res.track) {
      track = res.track;
    }
  } catch (err) {
    console.warn("Не удалось получить кэш текстов трека:", err);
  }

  if (!track) {
    showToast("Трек не найден", "error");
    return;
  }

  state.selectedTrack = track;
  state.totalSec = track.duration || 180;
  state.currentSec = 0;
  state.activeLyricIndex = -1;

  document.getElementById("player-title").textContent = track.title || "Без названия";
  document.getElementById("player-artist").textContent = `${track.artist || "Неизвестный артист"} — ${track.album || "Без альбома"}`;
  document.getElementById("time-total").textContent = formatDuration(state.totalSec);

  const lrcPath = track.filePath ? track.filePath.replace(/\.[^/.]+$/, ".lrc") : "/music/track.lrc";
  document.getElementById("raw-file-path").textContent = lrcPath;

  const rawTextarea = document.getElementById("raw-lrc-textarea");
  if (rawTextarea) {
    rawTextarea.value = track.lyricsBody || "";
  }

  const providerLabel = document.getElementById("provider-info-label");
  if (providerLabel) {
    if (track.lyricsSource) {
      providerLabel.textContent = `Источник: ${track.lyricsSource.toUpperCase()}`;
    } else {
      providerLabel.textContent = "Нет текста";
    }
  }

  // Парсим LRC
  if (track.lyricsBody && track.lyricsBody.trim()) {
    const parsed = parseLRC(track.lyricsBody);
    if (parsed.length > 0) {
      state.lyrics = parsed;
    } else {
      // Обычный текст без таймкодов — разбиваем построчно
      state.lyrics = track.lyricsBody.split("\n")
        .map(l => l.trim())
        .filter(l => l.length > 0)
        .map((text, idx) => ({ sec: idx * 5, text }));
    }
  } else {
    state.lyrics = [];
  }

  renderLyricsTeleprompter();
  updatePlayerUI();
  switchTab("karaoke");
}

function renderLyricsTeleprompter() {
  const container = document.getElementById("lyrics-stream");
  if (!container) return;

  if (state.lyrics.length === 0) {
    container.innerHTML = `
      <div class="lyric-line inactive" style="font-size: 16px; opacity: 0.7;">
        Текст для этого трека еще не найден.<br>
        Нажмите «Найти текст (LRC Турнир)» или «AI Караоке (Groq)» вверху.
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
  updatePlayerUI();
  if (!state.isPlaying) togglePlay();
}

function seekDelta(delta) {
  state.currentSec = Math.max(0, Math.min(state.totalSec, state.currentSec + delta));
  updatePlayerUI();
}

function handleScrub(e) {
  if (state.totalSec <= 0) return;
  const rect = e.currentTarget.getBoundingClientRect();
  const clickX = e.clientX - rect.left;
  const pct = Math.max(0, Math.min(1, clickX / rect.width));
  state.currentSec = Math.floor(pct * state.totalSec);
  updatePlayerUI();
}

function togglePlay() {
  state.isPlaying = !state.isPlaying;
  const playIcon = document.getElementById("play-icon");
  const pauseIcon = document.getElementById("pause-icon");

  if (state.isPlaying) {
    playIcon?.classList.add("hidden");
    pauseIcon?.classList.remove("hidden");
    clearInterval(state.timer);
    state.timer = setInterval(() => {
      state.currentSec++;
      if (state.currentSec > state.totalSec) {
        state.currentSec = 0;
        togglePlay();
        return;
      }
      updatePlayerUI();
    }, 1000);
  } else {
    playIcon?.classList.remove("hidden");
    pauseIcon?.classList.add("hidden");
    clearInterval(state.timer);
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
        line.scrollIntoView({ behavior: "smooth", block: "center" });
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

    state.selectedTrack.lyricsBody = content;
    state.selectedTrack.lyricsStatus = res.status || "verified";
    state.lyrics = parseLRC(content);
    
    renderLyricsTeleprompter();
    updatePlayerUI();
    toggleLrcEditor(); // переключаем обратно в суфлер
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
  } catch (err) {
    showToast(`Ошибка: ${err.message}`, "error");
  }
}

// -----------------------------------------------------------------------------
// 4. Staging-буфер рефакторинга
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
    container.innerHTML = `<div class="cell-empty">Очередь Staging пуста. Все теги каноничны. Запустите «MusicBrainz аудит» для поиска предложений.</div>`;
    return;
  }

  container.innerHTML = state.stagingItems.map(item => {
    let curObj = {};
    let propObj = {};
    try { curObj = JSON.parse(item.currentJson || "{}"); } catch (e) {}
    try { propObj = JSON.parse(item.proposedJson || "{}"); } catch (e) {}

    let issueTitle = "Предложение по тегам";
    if (item.changeType === "mojibake") issueTitle = "Битая кодировка (CP1251 → UTF-8)";
    if (item.changeType === "musicbrainz") issueTitle = "Канонизация тегов MusicBrainz";
    if (item.changeType === "artist_canonical") issueTitle = "Канонизация артиста";
    if (item.changeType === "cover") issueTitle = "Найдена обложка альбома";

    const path = item.folderPath || item.targetId || "Аудиофайл";

    // Специальное компактное и информативное отображение для обложек альбомов
    if (item.changeType === "cover") {
      const curCoverDesc = curObj.hasCover
        ? `Низкое разрешение (${curObj.width || 0}×${curObj.height || 0} px)`
        : `<span class="text-amber">Отсутствует в папке альбома</span>`;
      const propRes = propObj.width && propObj.height ? `${propObj.width}×${propObj.height} px` : "3000×3000 px HD";
      const sourceName = propObj.source === "itunes" ? "Apple Music / iTunes" : "Cover Art Archive";

      return `
        <div class="staging-card">
          <div class="staging-top">
            <div>
              <div class="staging-issue">Найдена обложка альбома (Уверенность: ${item.confidence || 95}%)</div>
              <div class="staging-path mono">${escapeHtml(path)}</div>
            </div>
            <div style="display: flex; gap: 8px;">
              <button class="btn btn-secondary" onclick="dismissStaging(${item.id})">Пропустить</button>
              <button class="btn btn-primary" onclick="applyStaging(${item.id})">Применить</button>
            </div>
          </div>

          <div class="staging-diff-grid">
            <div class="diff-cell">
              <div class="diff-cell-head">Было (В папке альбома)</div>
              <div><span class="text-muted">Артист:</span> ${escapeHtml(curObj.artist || "—")}</div>
              <div><span class="text-muted">Альбом:</span> ${escapeHtml(curObj.album || "—")}</div>
              <div style="margin-top: 4px;"><span class="text-muted">Файл обложки:</span> ${curCoverDesc}</div>
            </div>
            <div class="diff-cell" style="display: flex; gap: 14px; align-items: flex-start;">
              <div style="flex: 1;">
                <div class="diff-cell-head text-emerald">Станет (Новый cover.jpg)</div>
                <div><span class="text-muted">Разрешение:</span> <span class="diff-val-new">${escapeHtml(propRes)}</span></div>
                <div><span class="text-muted">Источник:</span> <span class="text-light">${escapeHtml(sourceName)}</span></div>
                <div style="margin-top: 6px; font-size: 11px; color: var(--text-muted); line-height: 1.4;">
                  ℹ️ <strong>Аудиофайлы не изменяются.</strong> В папку альбома будет атомарно скачан файл <code class="mono">cover.jpg</code> для отображения в Navidrome.
                </div>
              </div>
              ${propObj.coverUrl ? `
                <div style="width: 76px; height: 76px; border-radius: 6px; overflow: hidden; background: #000; flex-shrink: 0; border: 1px solid var(--border-active); box-shadow: 0 4px 12px rgba(0,0,0,0.5);">
                  <img src="${escapeHtml(propObj.coverUrl)}" alt="Обложка" style="width: 100%; height: 100%; object-fit: cover;" loading="lazy" />
                </div>
              ` : ""}
            </div>
          </div>
        </div>
      `;
    }

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
            <div><span class="text-muted">Артист:</span> <span class="diff-val-old">${escapeHtml(curObj.artist || curObj.albumArtist || "—")}</span></div>
            <div><span class="text-muted">Альбом:</span> <span class="diff-val-old">${escapeHtml(curObj.album || "—")}</span></div>
            ${curObj.title ? `<div><span class="text-muted">Трек:</span> <span class="diff-val-old">${escapeHtml(curObj.title)}</span></div>` : ""}
          </div>
          <div class="diff-cell">
            <div class="diff-cell-head text-emerald">Станет (Канонично)</div>
            <div><span class="text-muted">Артист:</span> <span class="diff-val-new">${escapeHtml(propObj.artist || propObj.albumArtist || "—")}</span></div>
            <div><span class="text-muted">Альбом:</span> <span class="diff-val-new">${escapeHtml(propObj.album || "—")}</span></div>
            ${propObj.title ? `<div><span class="text-muted">Трек:</span> <span class="diff-val-new">${escapeHtml(propObj.title)}</span></div>` : ""}
          </div>
        </div>
      </div>
    `;
  }).join("");
}

async function applyStaging(id) {
  try {
    await fetchApi("/staging/apply", "POST", { id });
    state.lastAppliedStagingId = id;
    showToast("✓ Изменения успешно применены в файл", "success");
    await loadStagingData();
  } catch (err) {
    showToast(`Ошибка применения: ${err.message}`, "error");
  }
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
    showToast("Нет активных предложений для применения", "info");
    return;
  }

  if (!confirm(`Применить все ${state.stagingItems.length} предложений в файлы фонотеки?`)) {
    return;
  }

  const ids = state.stagingItems.map(item => item.id);
  try {
    const res = await fetchApi("/staging/apply", "POST", { ids });
    showToast(`✓ Успешно применено изменений: ${res.applied}`, "success");
    await loadStagingData();
    await loadLibraryData();
  } catch (err) {
    showToast(`Ошибка массового применения: ${err.message}`, "error");
  }
}

async function discardAllStaging() {
  if (state.stagingItems.length === 0) return;

  if (!confirm("Отклонить все текущие предложения Staging?")) {
    return;
  }

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
// 5. Пул API-ключей (AES-256-GCM)
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
      showToast("Выберите провайдера и укажите API-ключ", "error");
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
  if (!confirm("Удалить данный API-ключ из пула ротации?")) {
    return;
  }

  try {
    await fetchApi(`/keys?id=${id}`, "DELETE");
    showToast("Ключ удален из пула", "info");
    await loadKeysData();
  } catch (err) {
    showToast(`Ошибка удаления ключа: ${err.message}`, "error");
  }
}
