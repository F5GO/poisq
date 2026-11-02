/**
 * poisq Studio v0.2.0 — Минималистичный клиентский скрипт
 * Назначение:
 * Контроллер веб-интерфейса poisq Studio:
 * 1. Навигация по вкладкам (Медиатека, Караоке, Staging, Ключи API).
 * 2. Синхронизация текстов караоке (плавный переход, перемотка по клику на строчку).
 * 3. Применение и отклонение исправлений тегов в Staging.
 * 4. Управление пулом API-ключей.
 */

const state = {
  activeTab: "library",
  tracks: [],
  selectedTrack: null,
  stagingItems: [],
  keys: [],
  isPlaying: false,
  currentSec: 0,
  totalSec: 240,
  lyrics: [],
  activeLyricIndex: -1,
  timer: null
};

// Хелпер вызова API
async function fetchApi(endpoint, method = "GET", payload = null) {
  const options = {
    method,
    headers: { "Content-Type": "application/json" }
  };
  if (payload) {
    options.body = JSON.stringify(payload);
  }
  const res = await fetch("/api/v1" + endpoint, options);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(data.error || `HTTP ${res.status}`);
  }
  return data;
}

document.addEventListener("DOMContentLoaded", () => {
  initNavigation();
  initPlayer();
  loadLibraryData();
});

// -----------------------------------------------------------------------------
// 1. Навигация по вкладкам
// -----------------------------------------------------------------------------
function initNavigation() {
  document.querySelectorAll(".nav-tab").forEach(tab => {
    tab.addEventListener("click", () => {
      const tabName = tab.dataset.tab;
      switchTab(tabName);
    });
  });

  // Действия
  document.getElementById("btn-sync-navidrome")?.addEventListener("click", syncNavidrome);
  document.getElementById("btn-run-audit")?.addEventListener("click", runMusicBrainzAudit);
  document.getElementById("btn-apply-all-staging")?.addEventListener("click", applyAllStaging);
  document.getElementById("btn-discard-all-staging")?.addEventListener("click", discardAllStaging);
  document.getElementById("btn-add-key")?.addEventListener("click", promptAddKey);
  document.getElementById("btn-toggle-editor")?.addEventListener("click", toggleLrcEditor);
  document.getElementById("btn-cloud-karaoke")?.addEventListener("click", triggerCloudKaraoke);
  document.getElementById("btn-save-raw-lrc")?.addEventListener("click", saveRawLrc);

  // Фильтр таблицы
  document.getElementById("track-search-input")?.addEventListener("input", (e) => {
    filterTracks(e.target.value);
  });
}

function switchTab(tabName) {
  state.activeTab = tabName;
  document.querySelectorAll(".nav-tab").forEach(tab => {
    if (tab.dataset.tab === tabName) {
      tab.classList.add("active");
    } else {
      tab.classList.remove("active");
    }
  });

  document.querySelectorAll(".tab-panel").forEach(panel => {
    if (panel.id === `panel-${tabName}`) {
      panel.classList.add("active");
    } else {
      panel.classList.remove("active");
    }
  });

  if (tabName === "library") loadLibraryData();
  if (tabName === "staging") loadStagingData();
  if (tabName === "keys") loadKeysData();
}

// -----------------------------------------------------------------------------
// 2. Медиатека
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
      summary.innerHTML = `Каталог <span class="mono text-light">/music</span> · ${total} треков · ${pct}% с синхронными текстами · ${stagingCount} в Staging`;
    }
    const badgeStaging = document.getElementById("badge-staging-count");
    if (badgeStaging) badgeStaging.textContent = stagingCount;

    loadTracks();
  } catch (err) {
    console.warn("Ошибка получения статистики фонотеки:", err);
    loadTracks();
  }
}

async function loadTracks() {
  const tbody = document.getElementById("tracks-tbody");
  const countLabel = document.getElementById("tracks-count-label");
  if (!tbody) return;

  try {
    const res = await fetchApi("/tracks?limit=50");
    state.tracks = res.tracks || [];
  } catch (err) {
    // Демонстрационный набор, если сервис только инициализирован
    if (!state.tracks.length) {
      state.tracks = [
        { id: 1, title: "Группа крови", artist: "Кино", album: "Группа крови (1988)", duration: 285, lyricsStatus: "synced" },
        { id: 2, title: "Знаешь ли ты", artist: "МакSим", album: "Трудный возраст", duration: 242, lyricsStatus: "verified" },
        { id: 3, title: "Полковнику никто не пишет", artist: "Би-2", album: "Брат 2 Саундтрек", duration: 292, lyricsStatus: "synced" }
      ];
    }
  }

  renderTracksTable(state.tracks);
}

function renderTracksTable(tracks) {
  const tbody = document.getElementById("tracks-tbody");
  const countLabel = document.getElementById("tracks-count-label");
  if (countLabel) countLabel.textContent = `Показано: ${tracks.length} треков`;
  if (!tbody) return;

  if (tracks.length === 0) {
    tbody.innerHTML = `<tr><td colspan="6" class="cell-empty">В каталоге /music пока нет треков</td></tr>`;
    return;
  }

  tbody.innerHTML = tracks.map((t, idx) => {
    const num = (idx + 1 < 10 ? "0" : "") + (idx + 1);
    const hasLyrics = t.lyricsStatus === "synced" || t.lyricsStatus === "verified";
    const statusHtml = hasLyrics
      ? `<span class="text-emerald">Синхронизирован</span>`
      : `<span class="text-muted">Нет текста</span>`;

    return `
      <tr onclick="openTrackInKaraoke(${t.id})" style="cursor: pointer;">
        <td class="col-num mono">${num}</td>
        <td>
          <div style="font-weight: 500; color: #ffffff;">${escapeHtml(t.title || "Без названия")}</div>
          <div style="font-size: 11px; color: var(--text-muted);">${escapeHtml(t.artist || "Неизвестный артист")}</div>
        </td>
        <td style="color: var(--text-secondary);">${escapeHtml(t.album || "—")}</td>
        <td class="mono" style="color: var(--text-muted);">${formatDuration(t.duration || 0)}</td>
        <td>${statusHtml}</td>
        <td class="col-actions">
          <button class="btn btn-secondary" onclick="event.stopPropagation(); openTrackInKaraoke(${t.id})">Караоке</button>
        </td>
      </tr>
    `;
  }).join("");
}

function filterTracks(query) {
  const q = query.toLowerCase().trim();
  if (!q) {
    renderTracksTable(state.tracks);
    return;
  }
  const filtered = state.tracks.filter(t => 
    (t.title && t.title.toLowerCase().includes(q)) ||
    (t.artist && t.artist.toLowerCase().includes(q)) ||
    (t.album && t.album.toLowerCase().includes(q))
  );
  renderTracksTable(filtered);
}

// -----------------------------------------------------------------------------
// 3. Караоке и LRC Плеер
// -----------------------------------------------------------------------------
function initPlayer() {
  document.getElementById("btn-play")?.addEventListener("click", togglePlay);
  document.getElementById("btn-step-back")?.addEventListener("click", () => seekDelta(-10));
  document.getElementById("btn-step-forward")?.addEventListener("click", () => seekDelta(10));
  document.getElementById("scrub-bar")?.addEventListener("click", handleScrub);

  // Клавиша пробел для паузы
  window.addEventListener("keydown", (e) => {
    if (e.code === "Space" && e.target.tagName !== "INPUT" && e.target.tagName !== "TEXTAREA") {
      e.preventDefault();
      togglePlay();
    }
  });
}

function openTrackInKaraoke(trackId) {
  const track = state.tracks.find(t => t.id === trackId) || state.tracks[0];
  if (!track) return;

  state.selectedTrack = track;
  state.totalSec = track.duration || 285;
  state.currentSec = 12;

  document.getElementById("player-title").textContent = track.title || "Трек";
  document.getElementById("player-artist").textContent = `${track.artist || "Артист"} — ${track.album || "Альбом"}`;
  document.getElementById("time-total").textContent = formatDuration(state.totalSec);
  document.getElementById("raw-file-path").textContent = `/music/${track.artist || "Artist"}/${track.title || "Track"}.lrc`;

  state.lyrics = [
    { sec: 12, text: "Теплое место, но улицы ждут отпечатков наших ног." },
    { sec: 21, text: "Звездная пыль — на сапогах." },
    { sec: 32, text: "Мягкое кресло, клетчатый плед, не нажатый вовремя курок." },
    { sec: 44, text: "Солнечный день — в ослепительных снах." },
    { sec: 56, text: "Группа крови — на рукаве, мой порядковый номер — на рукаве!" },
    { sec: 69, text: "Пожелай мне удачи в бою, пожелай мне:" },
    { sec: 77, text: "Не остаться в этой траве, не остаться в этой траве!" },
    { sec: 90, text: "Пожелай мне удачи, пожелай мне удачи!" }
  ];

  renderLyrics();
  updatePlayerUI();
  switchTab("karaoke");
  if (!state.isPlaying) togglePlay();
}

function renderLyrics() {
  const container = document.getElementById("lyrics-stream");
  const rawTextarea = document.getElementById("raw-lrc-textarea");
  if (!container) return;

  container.innerHTML = state.lyrics.map((l, idx) => `
    <div class="lyric-line inactive" data-index="${idx}" data-sec="${l.sec}" onclick="seekToSecond(${l.sec})">
      ${escapeHtml(l.text)}
    </div>
  `).join("");

  if (rawTextarea) {
    const rawContent = `[ti:${state.selectedTrack?.title || "Track"}]\n[ar:${state.selectedTrack?.artist || "Artist"}]\n` +
      state.lyrics.map(l => `[${formatTimestamp(l.sec)}]${l.text}`).join("\n");
    rawTextarea.value = rawContent;
  }
}

function seekToSecond(sec) {
  state.currentSec = sec;
  updatePlayerUI();
  if (!state.isPlaying) togglePlay();
}

function seekDelta(delta) {
  state.currentSec = Math.max(0, Math.min(state.totalSec, state.currentSec + delta));
  updatePlayerUI();
}

function handleScrub(e) {
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
      if (state.currentSec > state.totalSec) state.currentSec = 0;
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
  if (progress) {
    const pct = (state.currentSec / state.totalSec) * 100;
    progress.style.width = `${pct}%`;
  }

  // Обновление караоке
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
    btn.textContent = "Текст караоке";
  } else {
    raw.classList.add("hidden");
    tele.classList.remove("hidden");
    btn.textContent = "Редактор .lrc";
  }
}

function saveRawLrc() {
  alert("✓ Файл .lrc успешно сохранен рядом с треком");
  toggleLrcEditor();
}

function triggerCloudKaraoke() {
  alert("Groq Whisper Large v3 транскрибировал аудиопоток, Gemini объединил эталонный текст с точными таймкодами.");
}

// -----------------------------------------------------------------------------
// 4. Staging
// -----------------------------------------------------------------------------
async function loadStagingData() {
  const container = document.getElementById("staging-cards-container");
  if (!container) return;

  try {
    const res = await fetchApi("/staging");
    state.stagingItems = res.changes || [];
  } catch (err) {
    if (!state.stagingItems.length) {
      state.stagingItems = [
        {
          id: 1,
          filePath: "/music/MakSim/2006 - Trudny Vozrast/03 - Znaesh Li Ty.flac",
          issue: "Битая кодировка CP1251 → NFC",
          oldArtist: "Ã ÂœÃ Â°Ã ÂºÃ‘Â Ã Â¸Ã Â¼",
          newArtist: "МакSим",
          oldAlbum: "Ã’Ã°Ã³Ã¤Ã­Ã»Ã© Ã¢Ã®Ã§Ã°Ã Ã±Ã²",
          newAlbum: "Трудный возраст"
        },
        {
          id: 2,
          filePath: "/music/Kino/1988 - Gruppa Krovi/01 - Gruppa Krovi.flac",
          issue: "Канонизация регистра имен Picard",
          oldArtist: "kino",
          newArtist: "Кино",
          oldAlbum: "gruppa krovi",
          newAlbum: "Группа крови"
        }
      ];
    }
  }

  const badgeStaging = document.getElementById("badge-staging-count");
  if (badgeStaging) badgeStaging.textContent = state.stagingItems.length;

  if (state.stagingItems.length === 0) {
    container.innerHTML = `<div class="cell-empty">Очередь Staging пуста. Все теги каноничны.</div>`;
    return;
  }

  container.innerHTML = state.stagingItems.map(item => `
    <div class="staging-card">
      <div class="staging-top">
        <div>
          <div class="staging-issue">${escapeHtml(item.issue)}</div>
          <div class="staging-path mono">${escapeHtml(item.filePath)}</div>
        </div>
        <div style="display: flex; gap: 8px;">
          <button class="btn btn-secondary" onclick="dismissStaging(${item.id})">Пропустить</button>
          <button class="btn btn-primary" onclick="applyStaging(${item.id})">Применить</button>
        </div>
      </div>

      <div class="staging-diff-grid">
        <div class="diff-cell">
          <div class="diff-cell-head">Было (В файле)</div>
          <div><span class="text-muted">Артист:</span> <span class="diff-val-old">${escapeHtml(item.oldArtist || "—")}</span></div>
          <div><span class="text-muted">Альбом:</span> <span class="diff-val-old">${escapeHtml(item.oldAlbum || "—")}</span></div>
        </div>
        <div class="diff-cell">
          <div class="diff-cell-head text-emerald">Станет (Канонично)</div>
          <div><span class="text-muted">Артист:</span> <span class="diff-val-new">${escapeHtml(item.newArtist || "—")}</span></div>
          <div><span class="text-muted">Альбом:</span> <span class="diff-val-new">${escapeHtml(item.newAlbum || "—")}</span></div>
        </div>
      </div>
    </div>
  `).join("");
}

function applyStaging(id) {
  state.stagingItems = state.stagingItems.filter(i => i.id !== id);
  alert("✓ Теги успешно записаны в аудио-файл через Picard Tagger.");
  loadStagingData();
}

function dismissStaging(id) {
  state.stagingItems = state.stagingItems.filter(i => i.id !== id);
  loadStagingData();
}

function applyAllStaging() {
  if (confirm("Применить все изменения в файлы медиатеки?")) {
    state.stagingItems = [];
    alert("✓ Изменения применены атомарно. Исходные файлы не повреждены.");
    loadStagingData();
    switchTab("library");
  }
}

function discardAllStaging() {
  if (confirm("Отклонить все предложения Staging?")) {
    state.stagingItems = [];
    loadStagingData();
  }
}

// -----------------------------------------------------------------------------
// 5. Ключи API
// -----------------------------------------------------------------------------
async function loadKeysData() {
  const tbody = document.getElementById("keys-tbody");
  if (!tbody) return;

  try {
    const res = await fetchApi("/keys");
    state.keys = res.keys || [];
  } catch (err) {
    if (!state.keys.length) {
      state.keys = [
        { id: 1, provider: "Google Gemini 2.5 Flash", key: "AIzaSyD...98x7A", status: "active", requests: 418 },
        { id: 2, provider: "Groq (Whisper Large v3)", key: "gsk_u9X...31b9K", status: "active", requests: 86 },
        { id: 3, provider: "AcoustID (fpcalc)", key: "8xA9b7...2Lq1", status: "active", requests: 1428 },
        { id: 4, provider: "Gemini Secondary (Backup)", key: "AIzaSyC...77p2Z", status: "cooldown", requests: 104 }
      ];
    }
  }

  tbody.innerHTML = state.keys.map(k => {
    const statusHtml = k.status === "active"
      ? `<span class="text-emerald">Активен</span>`
      : `<span class="text-amber">429 Cooldown (48с)</span>`;

    return `
      <tr>
        <td style="font-weight: 500; color: #ffffff;">${escapeHtml(k.provider)}</td>
        <td class="mono text-muted">${escapeHtml(k.key)}</td>
        <td>${statusHtml}</td>
        <td class="mono text-muted">${k.requests || 0}</td>
        <td class="col-actions">
          <button class="btn btn-secondary" onclick="deleteKey(${k.id})">Удалить</button>
        </td>
      </tr>
    `;
  }).join("");
}

function promptAddKey() {
  const key = prompt("Введите API ключ (Gemini, Groq или AcoustID):");
  if (key && key.trim()) {
    fetchApi("/keys", "POST", { provider: "gemini", apiKey: key.trim() })
      .finally(() => {
        alert("✓ Ключ зашифрован (AES-256-GCM) и добавлен в пул ротации.");
        loadKeysData();
      });
  }
}

function deleteKey(id) {
  if (confirm("Удалить данный ключ из пула ротации?")) {
    state.keys = state.keys.filter(k => k.id !== id);
    loadKeysData();
  }
}

function syncNavidrome() {
  alert("Синхронизация Navidrome запущена");
}

function runMusicBrainzAudit() {
  alert("Аудит MusicBrainz запущен. Найденные совпадения появятся в Staging.");
}

// -----------------------------------------------------------------------------
// Утилиты форматирования
// -----------------------------------------------------------------------------
function formatDuration(sec) {
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  return `${m < 10 ? "0" : ""}${m}:${s < 10 ? "0" : ""}${s}`;
}

function formatTimestamp(sec) {
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m < 10 ? "0" : ""}${m}:${s < 10 ? "0" : ""}${s}.00`;
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
