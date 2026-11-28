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
  activeTab: null, // выставляет applyRoute по адресу страницы
  viewMode: "tracks",     // "tracks" | "artists" | "albums"
  filterMode: "all",      // "all" | "synced" | "plain" | "none" | "mojibake" | "no_cover"
  searchQuery: "",

  tracks: [],
  selectedTrack: null,
  stagingItems: [],
  stagingNextCursor: "",
  keys: [],
  jobs: [],
  jobLogs: {},
  expandedJobIds: new Set(),
  jobsPollTimer: null,
  workerPaused: false,
  lastAppliedStagingId: null,
  biographyArtists: [],
  selectedBiography: null,
  biographySearchTimer: null,
  biographySearchGeneration: 0,
  biographyBusy: false,
  metadataTracks: [],
  metadataSelectedId: null,
  metadataSelectedIds: new Set(),
  metadataDetail: null,
  metadataHistory: [],
  metadataHistorySelectedIds: new Set(),
  metadataPolicy: null,
  metadataAuditTimer: null,

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

// -----------------------------------------------------------------------------
// Диалог подтверждения: замена системного window.confirm
// -----------------------------------------------------------------------------

const DIALOG_ICONS = {
  danger: '<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/>',
  default: '<circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><path d="M12 17h.01"/>'
};

let dialogQueue = Promise.resolve();

/**
 * Показывает модальное окно подтверждения и возвращает Promise<boolean>.
 * Параметры: title, message, details (список коротких пунктов), confirmLabel,
 * cancelLabel, tone ("danger" — необратимое действие, фокус на «Отмене»).
 * Окна встают в очередь, поэтому два вызова подряд не перекрывают друг друга.
 * Весь текст вставляется через textContent, HTML из данных не исполняется.
 */
function confirmDialog({ title, message = "", details = [], confirmLabel = "Подтвердить", cancelLabel = "Отмена", tone = "default" }) {
  const run = () => new Promise((resolve) => {
    const danger = tone === "danger";
    const opener = document.activeElement;
    const uid = "dlg" + Date.now().toString(36);
    const make = (tag, cls, text) => {
      const el = document.createElement(tag);
      if (cls) el.className = cls;
      if (text !== undefined) el.textContent = text;
      return el;
    };

    const overlay = make("div", "modal-overlay dialog-overlay");
    const card = make("div", "modal-card dialog-card" + (danger ? " dialog-danger" : ""));
    card.setAttribute("role", danger ? "alertdialog" : "dialog");
    card.setAttribute("aria-modal", "true");
    card.setAttribute("aria-labelledby", uid + "-t");

    const body = make("div", "dialog-body");
    const icon = make("div", "dialog-icon");
    icon.setAttribute("aria-hidden", "true");
    icon.innerHTML = `<svg viewBox="0 0 24 24">${DIALOG_ICONS[danger ? "danger" : "default"]}</svg>`;
    const text = make("div", "dialog-text");
    const heading = make("h2", "dialog-title", title);
    heading.id = uid + "-t";
    text.appendChild(heading);
    if (message) {
      const p = make("p", "dialog-message", message);
      p.id = uid + "-d";
      card.setAttribute("aria-describedby", p.id);
      text.appendChild(p);
    }
    if (details.length) {
      const ul = make("ul", "dialog-list");
      details.forEach((d) => ul.appendChild(make("li", "", d)));
      text.appendChild(ul);
    }
    body.append(icon, text);

    const actions = make("div", "dialog-actions");
    const cancelBtn = make("button", "btn btn-secondary", cancelLabel + " ");
    cancelBtn.type = "button";
    cancelBtn.appendChild(make("kbd", "", "Esc"));
    const okBtn = make("button", "btn " + (danger ? "btn-danger" : "btn-primary"), confirmLabel);
    okBtn.type = "button";
    actions.append(cancelBtn, okBtn);

    card.append(body, actions);
    overlay.appendChild(card);
    document.body.appendChild(overlay);

    let done = false;
    const finish = (result) => {
      if (done) return;
      done = true;
      document.removeEventListener("keydown", onKey, true);
      overlay.classList.add("closing");
      setTimeout(() => overlay.remove(), 130);
      if (opener && typeof opener.focus === "function" && document.contains(opener)) opener.focus();
      resolve(result);
    };
    const onKey = (e) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopImmediatePropagation();
        finish(false);
      } else if (e.key === "Tab") {
        const first = cancelBtn, last = okBtn;
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
        else if (!card.contains(document.activeElement)) { e.preventDefault(); first.focus(); }
      }
    };
    document.addEventListener("keydown", onKey, true);
    cancelBtn.addEventListener("click", () => finish(false));
    okBtn.addEventListener("click", () => finish(true));
    overlay.addEventListener("mousedown", (e) => { if (e.target === overlay) finish(false); });

    // Для необратимых действий по умолчанию стоит безопасный выбор
    (danger ? cancelBtn : okBtn).focus();
  });
  const result = dialogQueue.then(run);
  dialogQueue = result.catch(() => {});
  return result;
}
window.confirmDialog = confirmDialog;

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

// escapeJsArg безопасно экранирует строку для передачи внутрь JS-строки '...' в HTML-атрибуте onclick/onsubmit.
function escapeJsArg(str) {
  if (str == null) return "";
  const jsEscaped = String(str)
    .replace(/\\/g, "\\\\")
    .replace(/'/g, "\\'")
    .replace(/"/g, "\\\"")
    .replace(/\r/g, "\\r")
    .replace(/\n/g, "\\n")
    .replace(/\u2028/g, "\\u2028")
    .replace(/\u2029/g, "\\u2029");
  return escapeHtml(jsEscaped);
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
  initNavidromeAccountForm();
  initPublicAccessForm();
  initSecureTransportForm();
  initBiographyStudio();
  initMetadataStudio();
  window.addEventListener("popstate", applyRoute);
  applyRoute();
  if (state.activeTab !== "library") loadLibraryData(); // счётчики в шапке нужны на любой странице
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
  // Внутренние ссылки (шапка, подразделы настроек) меняют адрес без перезагрузки; Cmd/Ctrl-клик открывает новую вкладку как обычно.
  document.addEventListener("click", (e) => {
    const link = e.target.closest("a[href^='/']");
    if (!link || link.target || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    navigate(link.getAttribute("href"));
  });

  // Действия в медиатеке
  document.getElementById("btn-sync-navidrome")?.addEventListener("click", syncNavidrome);
  document.getElementById("btn-run-audit")?.addEventListener("click", runMusicBrainzAudit);

  // Staging действия
  document.getElementById("btn-reset-db")?.addEventListener("click", resetFullDatabase);
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

// Адреса страниц: первая часть пути → вкладка. /lyrics/<id> открывает трек, /settings/<раздел> — подраздел настроек.
// Очередь задач живёт внутри «Текстов» (/lyrics/queue), поэтому в шапке подсвечиваются «Тексты».
const ROUTE_TABS = { library: "library", metadata: "metadata", lyrics: "karaoke", biographies: "biographies", fixes: "staging", settings: "keys" };
const TAB_PATHS = { library: "/library", metadata: "/metadata", karaoke: "/lyrics", biographies: "/biographies", staging: "/fixes", jobs: "/lyrics/queue", keys: "/settings/keys" };
const TAB_TITLES = { library: "Медиатека", metadata: "Метаданные", karaoke: "Тексты", biographies: "Биографии", staging: "Исправления", jobs: "Очередь задач", keys: "Настройки" };
const SETTINGS_SECTIONS = ["keys", "navidrome", "cast", "secure", "service"];

function navigate(path, replace = false) {
  if (location.pathname !== path) history[replace ? "replaceState" : "pushState"](null, "", path);
  applyRoute();
}

function switchTab(tabName) {
  navigate(TAB_PATHS[tabName] || "/library");
}

async function applyRoute() {
  const parts = location.pathname.split("/").filter(Boolean).map(decodeURIComponent);
  if (parts[0] === "jobs") return navigate("/lyrics/queue", true); // старый адрес очереди
  let tab = ROUTE_TABS[parts[0]];
  if (!tab) return navigate("/library", true);
  if (tab === "karaoke" && parts[1] === "queue") tab = "jobs";

  if (tab === "keys") {
    if (!SETTINGS_SECTIONS.includes(parts[1])) return navigate("/settings/keys", true);
    document.querySelectorAll("[data-settings-section]").forEach(el => { el.hidden = el.dataset.settingsSection !== parts[1]; });
    document.querySelectorAll("[data-settings]").forEach(el => el.classList.toggle("active", el.dataset.settings === parts[1]));
  }
  document.title = `${TAB_TITLES[tab]} — poisq Studio`;

  if (tab === "karaoke" && !parts[1] && state.selectedTrack) {
    return navigate(`/lyrics/${encodeURIComponent(state.selectedTrack.id)}`, true);
  }
  if (tab === "karaoke" && parts[1] && parts[1] !== state.selectedTrack?.id) {
    await openTrackInKaraoke(parts[1], true);
    // Переход отменён (несохранённые правки) или трек не найден — адрес возвращаем к открытому треку.
    if (state.selectedTrack?.id !== parts[1]) {
      history.replaceState(null, "", state.selectedTrack ? `/lyrics/${encodeURIComponent(state.selectedTrack.id)}` : "/lyrics");
    }
  }
  if (state.activeTab !== tab) activateTab(tab);
}

function activateTab(tabName) {
  state.activeTab = tabName;
  const navTab = tabName === "jobs" ? "karaoke" : tabName;
  document.querySelectorAll(".nav-tab").forEach(tab => {
    tab.classList.toggle("active", tab.dataset.tab === navTab);
  });

  document.querySelectorAll(".tab-panel").forEach(panel => {
    panel.classList.toggle("active", panel.id === `panel-${tabName}`);
  });

  if (state.jobsPollTimer) {
    clearInterval(state.jobsPollTimer);
    state.jobsPollTimer = null;
  }
  if (state.metadataAuditTimer && tabName !== "metadata") {
    clearInterval(state.metadataAuditTimer);
    state.metadataAuditTimer = null;
  }

  if (tabName === "library") loadLibraryData();
  if (tabName === "karaoke") {
    renderKaraokeSidebar();
  }
  if (tabName === "staging") loadStagingData();
  if (tabName === "metadata") loadMetadataWorkspace();
  if (tabName === "jobs") {
    loadJobsData();
    state.jobsPollTimer = setInterval(() => {
      if (state.activeTab === "jobs") {
        loadJobsData();
      }
    }, 2500);
  }
  if (tabName === "biographies") loadBiographyArtists();
  if (tabName === "keys") {
    loadKeysData();
    loadCastSettings();
    loadNavidromeAccount();
    loadPublicAccess();
    loadSecureTransport();
    loadSecureDevices();
    loadJobsData(); // состояние паузы воркера для «Обслуживания»
  }
}

// -----------------------------------------------------------------------------
// 1.1. Биографии: необязательный ручной приоритет над автоматическим результатом
// -----------------------------------------------------------------------------
function initBiographyStudio() {
  const searchInput = document.getElementById("biography-search-input");
  searchInput?.addEventListener("input", () => {
    clearTimeout(state.biographySearchTimer);
    state.biographySearchTimer = setTimeout(() => loadBiographyArtists(searchInput.value), 250);
  });
  document.getElementById("biography-manual-form")?.addEventListener("submit", saveManualBiography);
  document.getElementById("btn-reset-biography")?.addEventListener("click", resetManualBiography);
	document.getElementById("btn-rebuild-biography")?.addEventListener("click", rebuildBiography);
  document.getElementById("btn-reset-biography-cache")?.addEventListener("click", resetBiographyCache);
}

async function loadBiographyArtists(query) {
  const input = document.getElementById("biography-search-input");
  const search = typeof query === "string" ? query.trim() : (input?.value.trim() || "");
  const list = document.getElementById("biography-artist-list");
  if (!list) return;
  const generation = ++state.biographySearchGeneration;
  list.replaceChildren(createBiographyMessage("Загрузка..."));
  try {
    const data = await fetchApi(`/artist-biographies?query=${encodeURIComponent(search)}&limit=50`);
    if (generation !== state.biographySearchGeneration) return;
    state.biographyArtists = Array.isArray(data.artists) ? data.artists : [];
    renderBiographyArtists();
  } catch (err) {
    if (generation !== state.biographySearchGeneration) return;
    list.replaceChildren(createBiographyMessage("Не удалось загрузить исполнителей."));
    showToast(err.message, "error");
  }
}

function createBiographyMessage(text) {
  const message = document.createElement("p");
  message.className = "biography-empty";
  message.textContent = text;
  return message;
}

function renderBiographyArtists() {
  const list = document.getElementById("biography-artist-list");
  if (!list) return;
  list.replaceChildren();
  if (state.biographyArtists.length === 0) {
    list.append(createBiographyMessage("Исполнители не найдены."));
    return;
  }
  for (const artist of state.biographyArtists) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "biography-artist-button";
    button.classList.toggle("active", state.selectedBiography?.artistId === artist.id);
    button.textContent = artist.name;
    button.addEventListener("click", () => loadBiographyDetail(artist.id));
    list.append(button);
  }
}

async function loadBiographyDetail(artistId) {
  try {
    const detail = await fetchApi(`/artist-biographies/detail?id=${encodeURIComponent(artistId)}`);
    renderBiographyDetail(detail);
    renderBiographyArtists();
  } catch (err) {
    showToast(err.message, "error");
  }
}

function renderBiographyDetail(detail) {
  state.selectedBiography = detail;
  document.getElementById("biography-editor-empty")?.classList.add("hidden");
  document.getElementById("biography-editor")?.classList.remove("hidden");
  const name = document.getElementById("biography-artist-name");
  const stateText = document.getElementById("biography-effective-state");
  const badge = document.getElementById("biography-state-badge");
  const automaticMeta = document.getElementById("biography-automatic-meta");
  const automaticPreview = document.getElementById("biography-automatic-preview");
  const manualText = document.getElementById("biography-manual-text");
  const sourceURL = document.getElementById("biography-source-url");
	const jobStatus = document.getElementById("biography-job-status");

  if (name) name.textContent = detail.artistName || "Исполнитель";
  if (stateText) {
    stateText.textContent = detail.manualActive
      ? "Сейчас клиентам отдаётся ручное описание."
      : detail.hasBiography ? "Сейчас клиентам отдаётся автоматическое описание." : "Готового описания пока нет.";
  }
  if (badge) {
    badge.textContent = detail.manualActive ? "Ручное" : detail.hasBiography ? "Автоматическое" : "Нет описания";
    badge.classList.toggle("manual", Boolean(detail.manualActive));
  }
  if (automaticMeta) {
    automaticMeta.textContent = detail.automatic
      ? `Источник: ${detail.automatic.source}${detail.automatic.translated ? " · перевод на русский" : ""}`
      : "Автоматическое описание пока не найдено.";
  }
  if (automaticPreview) automaticPreview.textContent = detail.automatic?.text || "Нет автоматического описания.";
  if (manualText) manualText.value = detail.manualActive ? (detail.manualText || "") : "";
  if (sourceURL) sourceURL.value = detail.manualActive ? (detail.manualSourceUrl || "") : "";
	if (jobStatus) jobStatus.textContent = biographyJobStatusText(detail);
  const reset = document.getElementById("btn-reset-biography");
  if (reset) reset.disabled = !detail.manualActive;
	const rebuild = document.getElementById("btn-rebuild-biography");
	if (rebuild) rebuild.disabled = detail.jobStatus === "queued" || detail.jobStatus === "running";
  setBiographyBusy(false);
	if ((detail.jobStatus === "queued" || detail.jobStatus === "running") && state.activeTab === "biographies") {
		clearTimeout(state.biographyPollTimer);
		state.biographyPollTimer = setTimeout(() => {
			if (state.selectedBiography?.artistId === detail.artistId) loadBiographyDetail(detail.artistId);
		}, 2000);
	}
}

function biographyJobStatusText(detail) {
	if (!detail.jobStatus) return "Описание ещё не запрашивалось.";
	const labels = { queued: "В очереди", running: "Собираем и проверяем источники", ready: "Готово", no_data: "Недостаточно подтверждённых данных", retryable: "Временная ошибка" };
	const parts = [detail.jobStatus === "running" && detail.jobStageText ? detail.jobStageText : (labels[detail.jobStatus] || detail.jobStatus)];
	if (detail.jobStatus === "running") {
		if (detail.jobStageDetail) parts.push(detail.jobStageDetail);
		const since = Date.parse(detail.jobStageSince || "");
		if (Number.isFinite(since) && since > 0) parts.push(`${Math.max(0, Math.round((Date.now() - since) / 1000))} с`);
	}
	if (detail.jobStatus === "retryable") {
		if (detail.lastErrorText) parts.push(detail.lastErrorText);
		const retry = Date.parse(detail.nextRetryAt || "");
		if (Number.isFinite(retry) && retry > Date.now()) {
			parts.push(`повтор после ${new Date(retry).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" })}`);
		}
	}
	return `Состояние: ${parts.join(" · ")}`;
}

function setBiographyBusy(busy) {
  state.biographyBusy = busy;
  const save = document.getElementById("btn-save-biography");
  const reset = document.getElementById("btn-reset-biography");
	const rebuild = document.getElementById("btn-rebuild-biography");
  if (save) save.disabled = busy;
  if (reset) reset.disabled = busy || !state.selectedBiography?.manualActive;
	if (rebuild) rebuild.disabled = busy || state.selectedBiography?.jobStatus === "queued" || state.selectedBiography?.jobStatus === "running";
}

async function rebuildBiography() {
	if (state.biographyBusy || !state.selectedBiography) return;
	if (!await confirmDialog({
		title: "Пересобрать описание?",
		message: "Описание исполнителя будет заново собрано из проверенных источников.",
		confirmLabel: "Пересобрать"
	})) return;
	setBiographyBusy(true);
	try {
		await fetchApi("/artist-biographies/rebuild", "POST", { artistId: state.selectedBiography.artistId });
		await loadBiographyDetail(state.selectedBiography.artistId);
		showToast("Пересборка поставлена в очередь", "success");
	} catch (err) {
		setBiographyBusy(false);
		showToast(err.message, "error");
	}
}

async function saveManualBiography(event) {
  event.preventDefault();
  if (state.biographyBusy || !state.selectedBiography) return;
  const text = document.getElementById("biography-manual-text")?.value || "";
  const sourceUrl = document.getElementById("biography-source-url")?.value || "";
  setBiographyBusy(true);
  try {
    const detail = await fetchApi("/artist-biographies/detail", "PUT", {
      artistId: state.selectedBiography.artistId,
      text,
      sourceUrl
    });
    renderBiographyDetail(detail);
    renderBiographyArtists();
    showToast("Ручное описание сохранено", "success");
  } catch (err) {
    setBiographyBusy(false);
    showToast(err.message, "error");
  }
}

async function resetManualBiography() {
  if (state.biographyBusy || !state.selectedBiography?.manualActive) return;
  if (!await confirmDialog({
    title: "Удалить ручное описание?",
    message: "Снова будет использоваться автоматическое описание.",
    confirmLabel: "Удалить",
    tone: "danger"
  })) return;
  setBiographyBusy(true);
  try {
    const detail = await fetchApi(`/artist-biographies/detail?id=${encodeURIComponent(state.selectedBiography.artistId)}`, "DELETE");
    renderBiographyDetail(detail);
    renderBiographyArtists();
    showToast("Восстановлено автоматическое описание", "success");
  } catch (err) {
    setBiographyBusy(false);
    showToast(err.message, "error");
  }
}

async function resetBiographyCache() {
  if (state.biographyBusy) return;
  if (!await confirmDialog({
    title: "Сбросить автоматическую проверку?",
    message: "Проверка будет сброшена у всех исполнителей. Новая начнётся при открытии страницы исполнителя или ручной пересборке.",
    details: [
      "Удалятся найденные идентичности, кандидаты, переводы и автоматический выбор",
      "Ручные описания сохранятся"
    ],
    confirmLabel: "Сбросить проверку",
    tone: "danger"
  })) return;
  setBiographyBusy(true);
  const button = document.getElementById("btn-reset-biography-cache");
  if (button) button.disabled = true;
  try {
    const result = await fetchApi("/artist-biographies/reset", "POST", {});
    state.selectedBiography = null;
    document.getElementById("biography-editor")?.classList.add("hidden");
    document.getElementById("biography-editor-empty")?.classList.remove("hidden");
    await loadBiographyArtists();
    const cleared = result.cleared || {};
    showToast(`Проверка сброшена: идентичностей ${cleared.identities || 0}, кандидатов ${cleared.candidates || 0}`, "success");
  } catch (err) {
    showToast(err.message, "error");
  } finally {
    setBiographyBusy(false);
    if (button) button.disabled = false;
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
      summary.innerHTML = `Каталог <span class="mono text-light">/music</span> · ${total} треков · ${pct}% с синхронными LRC · ${stagingCount} в исправлениях`;
    }
    const badgeStaging = document.getElementById("badge-staging-count");
    if (badgeStaging) badgeStaging.textContent = stagingCount;

    setJobsCount(jobsCount);

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
    const jsTrackId = escapeJsArg(t.id);
    let statusBadge = `<span class="badge-lyric-btn none" onclick="event.stopPropagation(); openTrackInKaraoke('${jsTrackId}')">+ Найти текст</span>`;
    if (t.lyricsStatus === "verified") {
      statusBadge = `<span class="badge-lyric-btn verified" onclick="event.stopPropagation(); openTrackInKaraoke('${jsTrackId}')">Проверен</span>`;
    } else if (t.lyricsStatus === "synced") {
      statusBadge = `<span class="badge-lyric-btn synced" onclick="event.stopPropagation(); openTrackInKaraoke('${jsTrackId}')">Синхронный</span>`;
    } else if (t.lyricsStatus === "plain") {
      statusBadge = `<span class="badge-lyric-btn plain" onclick="event.stopPropagation(); openTrackInKaraoke('${jsTrackId}')">Обычный</span>`;
    }

    // Честный статус аудита медиатеки
    let auditBadge = `<span class="badge-audit-ok">Готов</span>`;
    let rowClass = "";
    if (t.isMojibake) {
      auditBadge = `<span class="badge-audit-queued">Mojibake</span>`;
    } else if (!t.hasCover || (t.coverWidth > 0 && t.coverWidth < 500)) {
      auditBadge = `<span class="badge-audit-queued">Нужна обложка</span>`;
    } else if (t.lyricsStatus === "none" || !t.lyricsStatus) {
      auditBadge = `<span class="badge-audit-queued">Нужен текст</span>`;
    } else if (t.lyricsStatus === "plain") {
      auditBadge = `<span class="badge-audit-queued">Нужен LRC</span>`;
    }

    return `
      <tr class="${rowClass}" onclick="openTrackDrawer('${jsTrackId}')" style="cursor: pointer;">
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
        <div class="artist-card" onclick="filterByArtist('${escapeJsArg(name)}')">
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
          <div class="album-card" onclick="filterByAlbum('${escapeJsArg(alb.album)}')">
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
  if (track.lyricsStatus === "verified") lyricsStatusDesc = "Проверен";
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
      <button class="btn btn-primary" onclick="openTrackInKaraoke('${escapeJsArg(track.id)}'); document.getElementById('drawer-track-detail').classList.add('hidden');">
        Запустить в Караоке-плеере
      </button>
      <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 8px;">
        <button class="btn btn-secondary" onclick="enqueueSingleTournament('${escapeJsArg(track.id)}')">
          LRC Турнир
        </button>
        <button class="btn btn-secondary" onclick="enqueueSingleGroq('${escapeJsArg(track.id)}')">
          AI Караоке (Groq)
        </button>
      </div>
      <button class="btn btn-secondary" onclick="auditSingleTrack('${escapeJsArg(track.id)}')">
        Проверить трек (MusicBrainz и обложка)
      </button>
      ${(content || (track.lyricsStatus && track.lyricsStatus !== "none")) ? `
        <button class="btn btn-danger-outline" onclick="deleteTrackLyrics('${escapeJsArg(track.id)}')" style="margin-top: 4px;">
          Удалить текст и .lrc файл
        </button>
      ` : ""}
    </div>

    <details style="margin-top: 10px;">
      <summary style="cursor: pointer; font-size: 13px; font-weight: 600; color: var(--text-secondary);">Редактировать теги</summary>
      <form id="form-track-tags" style="display: flex; flex-direction: column; gap: 8px; margin-top: 8px;" onsubmit="event.preventDefault(); saveTrackTags('${escapeJsArg(track.id)}');">
        <div class="form-group"><label class="form-label" for="tag-title">Название</label><input id="tag-title" class="form-input" value="${escapeHtml(track.title || "")}" /></div>
        <div class="form-group"><label class="form-label" for="tag-artist">Исполнитель</label><input id="tag-artist" class="form-input" placeholder="Несколько — через «; »" value="${escapeHtml(track.artist || "")}" /></div>
        <div class="form-group"><label class="form-label" for="tag-album">Альбом</label><input id="tag-album" class="form-input" value="${escapeHtml(track.album || "")}" /></div>
        <div class="form-group"><label class="form-label" for="tag-album-artist">Исполнитель альбома</label><input id="tag-album-artist" class="form-input" value="${escapeHtml(track.albumArtist || "")}" /></div>
        <div class="form-group"><label class="form-label" for="tag-year">Год</label><input id="tag-year" class="form-input mono" type="number" min="0" max="9999" value="${track.year || ""}" /></div>
        <button type="submit" class="btn btn-primary">Записать теги в файл</button>
      </form>
    </details>

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

// -----------------------------------------------------------------------------
// 3.1. Рабочее место метаданных
// -----------------------------------------------------------------------------
const METADATA_FIELDS = [
  ["title", "Название"], ["subtitle", "Подзаголовок"], ["album", "Альбом"],
  ["sort_title", "Название для сортировки"], ["sort_album", "Альбом для сортировки"],
  ["release_date", "Дата выпуска"], ["original_release_date", "Первая дата выпуска"],
  ["track_number", "Номер трека"], ["track_total", "Треков на диске"],
  ["disc_number", "Номер диска"], ["disc_total", "Всего дисков"],
  ["compilation", "Сборник"], ["label", "Лейбл"], ["catalog_number", "Каталожный номер"],
  ["barcode", "Штрихкод"], ["isrc", "ISRC"], ["grouping", "Группа"],
  ["comment", "Комментарий"], ["copyright", "Авторские права"],
  ["bpm", "BPM"], ["rating", "Рейтинг"], ["credits", "Участники"],
  ["genres", "Жанры"], ["external_ids", "Внешние ID"], ["custom_tags", "Пользовательские теги"]
];

const METADATA_VALUE_KEYS = {
  title: "title", subtitle: "subtitle", album: "album", sort_title: "sortTitle", sort_album: "sortAlbum",
  release_date: "releaseDate", original_release_date: "originalReleaseDate", track_number: "trackNumber",
  track_total: "trackTotal", disc_number: "discNumber", disc_total: "discTotal", compilation: "compilation",
  label: "label", catalog_number: "catalogNumber", barcode: "barcode", isrc: "isrc", grouping: "grouping",
  comment: "comment", copyright: "copyright", bpm: "bpm", rating: "rating", credits: "credits",
  genres: "genres", external_ids: "externalIds", custom_tags: "customTags"
};

function initMetadataStudio() {
  const search = document.getElementById("metadata-search-input");
  search?.addEventListener("input", () => renderMetadataTrackList(search.value));
  document.getElementById("btn-metadata-select-visible")?.addEventListener("click", selectVisibleMetadataTracks);
  document.getElementById("btn-metadata-clear-selection")?.addEventListener("click", clearMetadataTrackSelection);
  document.getElementById("btn-metadata-audit")?.addEventListener("click", runMetadataLibraryAudit);
  document.getElementById("btn-save-metadata-policy")?.addEventListener("click", saveMetadataPolicy);
}

async function loadMetadataWorkspace() {
  try {
    const [tracks, policy] = await Promise.all([
      fetchApi("/tracks?limit=250"),
      fetchApi("/metadata/policy")
    ]);
    state.metadataTracks = tracks.tracks || [];
    state.metadataPolicy = policy;
    renderMetadataTrackList(document.getElementById("metadata-search-input")?.value || "");
    renderMetadataPolicy();
    await Promise.all([loadMetadataRecovery(), pollMetadataAuditStatus()]);
    if (!state.metadataAuditTimer) state.metadataAuditTimer = setInterval(pollMetadataAuditStatus, 2000);
    if (state.metadataSelectedId && state.metadataTracks.some(t => t.id === state.metadataSelectedId)) {
      await selectMetadataTrack(state.metadataSelectedId);
    }
  } catch (err) {
    showToast(`Не удалось загрузить метаданные: ${err.message}`, "error");
  }
}

function renderMetadataTrackList(query = "") {
  const list = document.getElementById("metadata-track-list");
  if (!list) return;
  const needle = String(query).trim().toLocaleLowerCase("ru");
  const values = state.metadataTracks.filter(t => !needle || [t.title, t.artist, t.album].some(v => String(v || "").toLocaleLowerCase("ru").includes(needle)));
  if (!values.length) {
    list.innerHTML = `<p class="metadata-empty">Треки не найдены.</p>`;
    return;
  }
  list.innerHTML = values.map(track => `
    <div class="metadata-track-row ${track.id === state.metadataSelectedId ? "active" : ""}">
      <label class="metadata-track-select" title="Добавить в пакетную правку"><input type="checkbox" ${state.metadataSelectedIds.has(track.id) ? "checked" : ""} onchange="toggleMetadataTrackSelection('${escapeJsArg(track.id)}', this.checked)"><span class="sr-only">Выбрать ${escapeHtml(track.title || "трек")}</span></label>
      <button type="button" class="metadata-track" onclick="selectMetadataTrack('${escapeJsArg(track.id)}')">
        <span class="metadata-track-title">${escapeHtml(track.title || "Без названия")}</span>
        <span class="metadata-track-meta">${escapeHtml(track.artist || "Неизвестный исполнитель")} · ${escapeHtml(track.album || "Без альбома")}</span>
      </button>
    </div>`).join("");
  updateMetadataSelectionCount();
}

function visibleMetadataTracks() {
  const needle = String(document.getElementById("metadata-search-input")?.value || "").trim().toLocaleLowerCase("ru");
  return state.metadataTracks.filter(t => !needle || [t.title, t.artist, t.album].some(v => String(v || "").toLocaleLowerCase("ru").includes(needle)));
}

function toggleMetadataTrackSelection(trackId, selected) {
  if (selected) state.metadataSelectedIds.add(trackId); else state.metadataSelectedIds.delete(trackId);
  updateMetadataSelectionCount();
  renderMetadataDetail();
}

function selectVisibleMetadataTracks() {
  visibleMetadataTracks().slice(0, 500).forEach(track => state.metadataSelectedIds.add(track.id));
  renderMetadataTrackList(document.getElementById("metadata-search-input")?.value || "");
  renderMetadataDetail();
}

function clearMetadataTrackSelection() {
  state.metadataSelectedIds.clear();
  renderMetadataTrackList(document.getElementById("metadata-search-input")?.value || "");
  renderMetadataDetail();
}

function updateMetadataSelectionCount() {
  const count = document.getElementById("metadata-selection-count");
  if (count) count.textContent = `Выбрано: ${state.metadataSelectedIds.size}`;
}

async function selectMetadataTrack(trackId) {
  state.metadataSelectedId = trackId;
  renderMetadataTrackList(document.getElementById("metadata-search-input")?.value || "");
  const detail = document.getElementById("metadata-detail");
  const empty = document.getElementById("metadata-detail-empty");
  if (detail) detail.innerHTML = `<p class="metadata-empty">Загрузка метаданных...</p>`;
  detail?.classList.remove("hidden");
  empty?.classList.add("hidden");
  try {
    state.metadataDetail = await fetchApi(`/metadata/track?id=${encodeURIComponent(trackId)}`);
    state.metadataHistorySelectedIds.clear();
    renderMetadataDetail();
    await Promise.all([loadMetadataArtwork(), loadMetadataSimilar(), loadMetadataHistory()]);
  } catch (err) {
    if (detail) detail.innerHTML = `<p class="metadata-empty text-red">${escapeHtml(err.message)}</p>`;
  }
}

function metadataExternalID(type) {
  return (state.metadataDetail?.document?.externalIds || []).find(item => item.type === type)?.value || "";
}

function metadataCredit(role) {
  return (state.metadataDetail?.document?.credits || []).filter(item => item.role === role).map(item => item.name).join("; ");
}

function displayMetadataValue(field, doc) {
  const value = doc[METADATA_VALUE_KEYS[field]];
  if (field === "release_date" || field === "original_release_date") return value?.value || "—";
  if (field === "credits") return (value || []).map(item => `${item.name} · ${item.role}`).join("; ") || "—";
  if (field === "external_ids") return (value || []).map(item => `${item.type}: ${item.value}`).join("; ") || "—";
  if (field === "custom_tags") return (value || []).map(item => `${item.key}=${item.value}`).join("; ") || "—";
  if (Array.isArray(value)) return value.join("; ") || "—";
  if (typeof value === "boolean") return value ? "Да" : "Нет";
  if (value && typeof value === "object") return "—";
  return value === 0 || value ? String(value) : "—";
}

function renderMetadataDetail() {
  const root = document.getElementById("metadata-detail");
  const data = state.metadataDetail;
  if (!root || !data) return;
  const track = data.track || {};
  const doc = data.document || {};
  const states = doc.fieldStates || {};
  const media = data.mediaInfo || {};
  const analysis = data.analysis || {};
  const year = String(doc.releaseDate?.value || track.year || "").slice(0, 4);
  const artistMbid = metadataExternalID("musicbrainz_artist");
  const bulkCount = state.metadataSelectedIds.size;
  const isBulk = bulkCount > 1;

  const fields = METADATA_FIELDS.map(([field, label]) => {
    const source = states[field] || {};
    const confidence = Number.isFinite(source.confidence) ? `${Math.round(source.confidence * 100)}%` : "—";
    return `<div class="metadata-field">
      <div><span class="metadata-field-name">${label}</span><span class="metadata-field-value">${escapeHtml(displayMetadataValue(field, doc))}</span>
      <span class="metadata-field-source">${escapeHtml(source.source || "без источника")}${source.provider ? ` · ${escapeHtml(source.provider)}` : ""} · ${confidence}</span></div>
      <button type="button" class="metadata-lock ${source.locked ? "locked" : ""}" title="${source.locked ? "Разблокировать поле" : "Защитить поле от обновлений"}" aria-label="${source.locked ? "Разблокировать" : "Заблокировать"}: ${label}" onclick="toggleMetadataLock('${field}', ${!source.locked})">${source.locked ? "●" : "○"}</button>
    </div>`;
  }).join("");

  const facts = [
    ["Формат", [media.container, media.audioCodec].filter(Boolean).join(" · ") || "—"],
    ["Аудио", media.sampleRate ? `${media.sampleRate} Гц · ${media.bitDepth || "—"} бит · ${media.channels || "—"} кан.` : "—"],
    ["Громкость", analysis.integratedLufs != null ? `${analysis.integratedLufs.toFixed(1)} LUFS` : "Не измерена"],
    ["Истинный пик", analysis.truePeakDb != null ? `${analysis.truePeakDb.toFixed(1)} dBFS` : "—"]
  ].map(([label, value]) => `<div class="metadata-fact"><span class="metadata-fact-label">${label}</span><span class="metadata-fact-value mono">${escapeHtml(value)}</span></div>`).join("");

  root.innerHTML = `
    <div class="metadata-detail-head">
      <div><h2 class="metadata-detail-title">${escapeHtml(track.title || "Без названия")}</h2><p class="metadata-detail-sub">${escapeHtml(track.artist || "—")} · ${escapeHtml(track.album || "—")}</p></div>
      <div class="metadata-action-row">
        <button type="button" class="btn btn-secondary" onclick="refreshMetadataTrack()">Обновить источники</button>
        <button type="button" class="btn btn-secondary" onclick="analyzeMetadataTrack()">Измерить громкость</button>
      </div>
    </div>
    <div class="metadata-facts">${facts}</div>

    <section class="metadata-section">
      <div class="metadata-section-head"><h3 class="metadata-section-title">Поля и происхождение</h3><span class="metadata-section-note">Круг справа блокирует автоматическую замену</span></div>
      <div class="metadata-fields">${fields}</div>
    </section>

    <section class="metadata-section">
      <div class="metadata-section-head"><h3 class="metadata-section-title">${isBulk ? `Пакетная правка · ${bulkCount} треков` : "Редактирование тегов"}</h3><span class="metadata-section-note">${isBulk ? "Отметьте только поля, которые нужно заменить во всех выбранных треках" : "Сначала показываем различия, затем создаём безопасное предложение"}</span></div>
      <form id="metadata-tags-form" onsubmit="event.preventDefault(); saveMetadataTags();">
        <div class="metadata-edit-grid">
          ${metadataInput("md-title", "Название", doc.title || track.title, "text", "", "title", isBulk)}
          ${metadataInput("md-subtitle", "Подзаголовок", doc.subtitle, "text", "", "subtitle", isBulk)}
          ${metadataInput("md-sort-title", "Название для сортировки", doc.sortTitle, "text", "", "sort_title", isBulk)}
          ${metadataInput("md-album", "Альбом", doc.album || track.album, "text", "", "album", isBulk)}
          ${metadataInput("md-sort-album", "Альбом для сортировки", doc.sortAlbum, "text", "", "sort_album", isBulk)}
          ${metadataInput("md-release-date", "Дата выпуска (ГГГГ-ММ-ДД)", doc.releaseDate?.value || year, "text", "", "release_date", isBulk)}
          ${metadataInput("md-original-release-date", "Первая дата выпуска", doc.originalReleaseDate?.value, "text", "", "original_release_date", isBulk)}
          ${metadataInput("md-track-number", "Номер трека", doc.trackNumber || track.trackNumber, "number", "", "track_number", isBulk)}
          ${metadataInput("md-track-total", "Всего треков", doc.trackTotal, "number", "", "track_total", isBulk)}
          ${metadataInput("md-disc-number", "Номер диска", doc.discNumber, "number", "", "disc_number", isBulk)}
          ${metadataInput("md-disc-total", "Всего дисков", doc.discTotal, "number", "", "disc_total", isBulk)}
          ${metadataInput("md-bpm", "BPM", doc.bpm, "number", "", "bpm", isBulk)}
          ${metadataInput("md-rating", "Рейтинг (0–100, пусто — удалить)", doc.rating, "number", "", "rating", isBulk)}
          ${metadataInput("md-genres", "Жанры через ;", (doc.genres || []).join("; "), "text", "", "genres", isBulk)}
          ${metadataInput("md-artist", "Исполнитель", metadataCredit("artist") || track.artist, "text", "", "credits", isBulk)}
          ${metadataInput("md-album-artist", "Исполнитель альбома", metadataCredit("album_artist") || track.albumArtist, "text", "", "credits", isBulk)}
          ${metadataInput("md-composer", "Композитор", metadataCredit("composer"), "text", "", "credits", isBulk)}
          ${metadataInput("md-lyricist", "Автор текста", metadataCredit("lyricist"), "text", "", "credits", isBulk)}
          ${metadataInput("md-conductor", "Дирижёр", metadataCredit("conductor"), "text", "", "credits", isBulk)}
          ${metadataInput("md-performer", "Исполнитель роли", metadataCredit("performer"), "text", "", "credits", isBulk)}
          ${metadataInput("md-producer", "Продюсер", metadataCredit("producer"), "text", "", "credits", isBulk)}
          ${metadataInput("md-remixer", "Ремиксер", metadataCredit("remixer"), "text", "", "credits", isBulk)}
          ${metadataInput("md-label", "Лейбл", doc.label, "text", "", "label", isBulk)}
          ${metadataInput("md-catalog", "Каталожный номер", doc.catalogNumber, "text", "", "catalog_number", isBulk)}
          ${metadataInput("md-barcode", "Штрихкод", doc.barcode, "text", "", "barcode", isBulk)}
          ${metadataInput("md-isrc", "ISRC", doc.isrc, "text", "", "isrc", isBulk)}
          ${metadataInput("md-grouping", "Группа", doc.grouping, "text", "", "grouping", isBulk)}
          ${metadataInput("md-release-mbid", "MusicBrainz release", metadataExternalID("musicbrainz_release"), "text", "", "external_ids", isBulk)}
          ${metadataInput("md-release-group-mbid", "MusicBrainz release group", metadataExternalID("musicbrainz_release_group"), "text", "", "external_ids", isBulk)}
          ${metadataInput("md-artist-mbid", "MusicBrainz artist", artistMbid, "text", "", "external_ids", isBulk)}
          ${metadataInput("md-recording-mbid", "MusicBrainz recording", metadataExternalID("musicbrainz_recording"), "text", "", "external_ids", isBulk)}
          ${metadataInput("md-release-track-mbid", "MusicBrainz release track", metadataExternalID("musicbrainz_release_track"), "text", "", "external_ids", isBulk)}
          <div class="form-group metadata-edit-field"><label class="metadata-field-toggle"><input type="checkbox" data-metadata-field="compilation" ${isBulk ? "" : "checked"}> Изменить поле</label><label class="metadata-checkbox"><input type="checkbox" id="md-compilation" ${doc.compilation ? "checked" : ""}> Сборник</label></div>
          <div class="form-group wide metadata-edit-field"><label class="metadata-field-toggle"><input type="checkbox" data-metadata-field="comment" ${isBulk ? "" : "checked"}> Изменить поле</label><label class="form-label" for="md-comment">Комментарий</label><textarea id="md-comment" class="form-input metadata-textarea">${escapeHtml(doc.comment || "")}</textarea></div>
          ${metadataInput("md-copyright", "Авторские права", doc.copyright, "text", "wide", "copyright", isBulk)}
          <div class="form-group wide metadata-edit-field"><label class="metadata-field-toggle"><input type="checkbox" data-metadata-field="custom_tags" ${isBulk ? "" : "checked"}> Изменить поле</label><label class="form-label" for="md-custom-tags">Пользовательские теги · по одному KEY=value на строку</label><textarea id="md-custom-tags" class="form-input metadata-textarea mono">${escapeHtml((doc.customTags || []).map(item => `${item.key}=${item.value}`).join("\n"))}</textarea></div>
        </div>
        <button type="submit" class="btn btn-primary">Предпросмотр изменений</button>
      </form>
    </section>

    <section class="metadata-section">
      <div class="metadata-section-head"><h3 class="metadata-section-title">История полей</h3><span class="metadata-section-note">Можно откатить отдельные поля, не затрагивая остальные</span></div>
      <div id="metadata-history" class="metadata-history"><p class="metadata-empty">Загрузка истории...</p></div>
    </section>

    <section class="metadata-section">
      <div class="metadata-section-head"><h3 class="metadata-section-title">Инструменты альбома</h3><span class="metadata-section-note">Изменения файлов сначала попадают в «Исправления»</span></div>
      <div class="metadata-tools">
        <div class="metadata-tool"><h4>NFO для Jellyfin и Kodi</h4><p>Импорт учитывает блокировки. Экспорт создаёт предложения для album.nfo и artist.nfo.</p><div class="metadata-tool-actions"><button class="btn btn-secondary" type="button" onclick="runMetadataNFO('import')">Импортировать NFO</button><button class="btn btn-secondary" type="button" onclick="runMetadataNFO('export')">Подготовить NFO</button></div></div>
        <div class="metadata-tool"><h4>ReplayGain</h4><p>Расчёт основан на сохранённом измерении EBU R128.</p><div class="metadata-inline-input"><input id="md-target-lufs" class="form-input mono" type="number" min="-30" max="-5" step="0.1" value="-14"><button class="btn btn-secondary" type="button" onclick="stageMetadataReplayGain()">Подготовить теги</button></div></div>
        <div class="metadata-tool"><h4>Изображения по ролям</h4><p>Album, artist, disc, logo, banner и backdrop сохраняются отдельными файлами.</p>
          <div class="form-group"><label class="form-label" for="md-art-scope">Область</label><select id="md-art-scope" class="form-select"><option value="album">Альбом</option><option value="artist">Исполнитель</option></select></div>
          <div class="form-group"><label class="form-label" for="md-art-role">Роль</label><select id="md-art-role" class="form-select" onchange="syncMetadataArtworkScope()"><option value="album_primary">Обложка альбома</option><option value="artist_primary">Фото исполнителя</option><option value="disc">Диск</option><option value="logo">Логотип</option><option value="banner">Баннер</option><option value="backdrop">Фон</option></select></div>
          <div class="form-group"><label class="form-label" for="md-art-url">Прямая ссылка на изображение</label><input id="md-art-url" class="form-input" type="url" placeholder="https://..."></div>
          <div class="metadata-inline-input"><div class="form-group"><label class="form-label" for="md-art-width">Ширина</label><input id="md-art-width" class="form-input mono" type="number" min="1"></div><div class="form-group"><label class="form-label" for="md-art-height">Высота</label><input id="md-art-height" class="form-input mono" type="number" min="1"></div></div>
          <div class="metadata-tool-actions" style="margin-top:8px"><button class="btn btn-secondary" type="button" onclick="stageMetadataArtwork()">Подготовить изображение</button></div><div id="metadata-artwork-list" class="metadata-artwork-list"></div>
        </div>
        <div class="metadata-tool"><h4>Похожие исполнители</h4><p>${artistMbid ? "Данные ListenBrainz привязаны к подтверждённому MusicBrainz ID." : "Сначала подтвердите MusicBrainz ID исполнителя."}</p><button class="btn btn-secondary" type="button" ${artistMbid ? "" : "disabled"} onclick="refreshMetadataSimilar('${escapeJsArg(artistMbid)}')">Обновить список</button><div id="metadata-similar-list" class="metadata-similar-list"></div></div>
      </div>
    </section>`;
}

function metadataInput(id, label, value, type = "text", extraClass = "", field = "", bulk = false) {
  return `<div class="form-group metadata-edit-field ${extraClass}">${field ? `<label class="metadata-field-toggle"><input type="checkbox" data-metadata-field="${field}" ${bulk ? "" : "checked"}> Изменить поле</label>` : ""}<label class="form-label" for="${id}">${label}</label><input id="${id}" class="form-input${type === "number" ? " mono" : ""}" type="${type}" ${type === "number" ? 'min="0" max="99999"' : ""} value="${escapeHtml(value == null ? "" : String(value))}"></div>`;
}

async function toggleMetadataLock(field, locked) {
  if (!state.metadataSelectedId) return;
  try {
    await fetchApi("/metadata/lock", "POST", { trackId: state.metadataSelectedId, field, locked });
    await selectMetadataTrack(state.metadataSelectedId);
    showToast(locked ? "Поле защищено от обновлений" : "Поле разблокировано", "success");
  } catch (err) { showToast(`Не удалось изменить блокировку: ${err.message}`, "error"); }
}

function metadataFormValue(id) { return (document.getElementById(id)?.value || "").trim(); }
function metadataFormNumber(id) { return parseInt(metadataFormValue(id), 10) || 0; }

function metadataFieldEnabled(field) {
  return [...document.querySelectorAll(`[data-metadata-field="${field}"]`)].some(input => input.checked);
}

function metadataNames(value, role) {
  return String(value || "").split(";").map(name => name.trim()).filter(Boolean).map(name => ({ role, name }));
}

function parseMetadataCustomTags(value) {
  return String(value || "").split(/\r?\n/).map(line => line.trim()).filter(Boolean).map((line, index) => {
    const separator = line.indexOf("=");
    if (separator < 1) throw new Error(`Строка ${index + 1}: ожидается KEY=value`);
    return { key: line.slice(0, separator).trim(), value: line.slice(separator + 1).trim() };
  });
}

function metadataExternalIDsFromForm() {
  const managed = {
    musicbrainz_release: metadataFormValue("md-release-mbid"),
    musicbrainz_release_group: metadataFormValue("md-release-group-mbid"),
    musicbrainz_artist: metadataFormValue("md-artist-mbid"),
    musicbrainz_recording: metadataFormValue("md-recording-mbid"),
    musicbrainz_release_track: metadataFormValue("md-release-track-mbid")
  };
  const existing = (state.metadataDetail?.document?.externalIds || []).filter(item => !(item.type in managed));
  return existing.concat(Object.entries(managed).filter(([, value]) => value).map(([type, value]) => ({ type, value })));
}

function buildMetadataPatch() {
  const patch = {};
  const clear = [];
  const textFields = {
    title: "md-title", subtitle: "md-subtitle", sortTitle: "md-sort-title", album: "md-album", sortAlbum: "md-sort-album",
    label: "md-label", catalogNumber: "md-catalog", barcode: "md-barcode", isrc: "md-isrc", grouping: "md-grouping",
    comment: "md-comment", copyright: "md-copyright"
  };
  const semantic = {
    title: "title", subtitle: "subtitle", sortTitle: "sort_title", album: "album", sortAlbum: "sort_album",
    label: "label", catalogNumber: "catalog_number", barcode: "barcode", isrc: "isrc", grouping: "grouping",
    comment: "comment", copyright: "copyright"
  };
  Object.entries(textFields).forEach(([jsonKey, id]) => { if (metadataFieldEnabled(semantic[jsonKey])) patch[jsonKey] = metadataFormValue(id); });
  [["releaseDate", "release_date", "md-release-date"], ["originalReleaseDate", "original_release_date", "md-original-release-date"]].forEach(([jsonKey, field, id]) => {
    if (!metadataFieldEnabled(field)) return;
    const value = metadataFormValue(id);
    if (value) patch[jsonKey] = value; else clear.push(field);
  });
  [["trackNumber", "track_number", "md-track-number"], ["trackTotal", "track_total", "md-track-total"], ["discNumber", "disc_number", "md-disc-number"], ["discTotal", "disc_total", "md-disc-total"], ["bpm", "bpm", "md-bpm"]].forEach(([jsonKey, field, id]) => {
    if (metadataFieldEnabled(field)) patch[jsonKey] = metadataFormNumber(id);
  });
  if (metadataFieldEnabled("rating")) {
    const value = metadataFormValue("md-rating");
    if (value === "") clear.push("rating"); else patch.rating = parseInt(value, 10);
  }
  if (metadataFieldEnabled("compilation")) patch.compilation = !!document.getElementById("md-compilation")?.checked;
  if (metadataFieldEnabled("genres")) patch.genres = metadataFormValue("md-genres").split(/[;,]/).map(v => v.trim()).filter(Boolean);
  if (metadataFieldEnabled("credits")) {
    patch.credits = [
      ...metadataNames(metadataFormValue("md-artist"), "artist"), ...metadataNames(metadataFormValue("md-album-artist"), "album_artist"),
      ...metadataNames(metadataFormValue("md-composer"), "composer"), ...metadataNames(metadataFormValue("md-lyricist"), "lyricist"),
      ...metadataNames(metadataFormValue("md-conductor"), "conductor"), ...metadataNames(metadataFormValue("md-performer"), "performer"),
      ...metadataNames(metadataFormValue("md-producer"), "producer"), ...metadataNames(metadataFormValue("md-remixer"), "remixer")
    ];
  }
  if (metadataFieldEnabled("external_ids")) patch.externalIds = metadataExternalIDsFromForm();
  if (metadataFieldEnabled("custom_tags")) patch.customTags = parseMetadataCustomTags(metadataFormValue("md-custom-tags"));
  if (clear.length) patch.clear = clear;
  return patch;
}

function metadataPatchTargets() {
  const selected = [...state.metadataSelectedIds];
  return selected.length ? selected : (state.metadataSelectedId ? [state.metadataSelectedId] : []);
}

function historyValueLabel(value) {
  if (value == null || value === "") return "∅";
  if (Array.isArray(value)) return value.map(item => typeof item === "object" ? (item.name || `${item.key}=${item.value}`) : item).join("; ") || "∅";
  if (typeof value === "object") return value.value || JSON.stringify(value);
  return String(value);
}

async function saveMetadataTags() {
  const trackIds = metadataPatchTargets();
  if (!trackIds.length) return;
  try {
    const patch = buildMetadataPatch();
    if (!Object.keys(patch).length) throw new Error("Отметьте хотя бы одно поле");
    const preview = await fetchApi("/metadata/tags/preview", "POST", { trackIds, patch }, 60000);
    const changed = (preview.items || []).filter(item => !item.unchanged && !item.error);
    const errors = (preview.items || []).filter(item => item.error);
    if (!changed.length) throw new Error(errors[0]?.error || "Выбранные значения уже установлены");
    const details = changed.map(item => `${item.trackId} · ${(item.changes || []).map(change => `${change.field}: ${historyValueLabel(change.before)} → ${historyValueLabel(change.after)}`).join("; ")}`);
    errors.forEach(item => details.push(`${item.trackId} · ошибка: ${item.error}`));
    if (!await confirmDialog({
      title: trackIds.length > 1 ? `Изменить ${changed.length} треков?` : "Подготовить изменение тегов?",
      message: "Файлы пока не меняются: будут созданы предложения в разделе «Исправления».",
      details,
      confirmLabel: "Создать предложения"
    })) return;
    const result = await fetchApi("/metadata/tags/apply", "POST", { trackIds, patch }, 60000);
    const failed = (result.items || []).filter(item => item.error);
    const unchanged = (result.items || []).filter(item => item.unchanged).length;
    showToast(`Создано предложений: ${result.created || 0}; без изменений: ${unchanged}; ошибок: ${failed.length}.`, failed.length ? "info" : "success");
    if (failed.length) {
      await confirmDialog({
        title: "Часть треков не подготовлена",
        message: "Успешные предложения сохранены. Ошибки можно исправить и повторить только для этих треков.",
        details: failed.map(item => `${item.trackId}: ${item.error}`),
        confirmLabel: "Закрыть",
        cancelLabel: "Закрыть"
      });
    }
    if (state.metadataSelectedId) await selectMetadataTrack(state.metadataSelectedId);
  } catch (err) { showToast(`Не удалось подготовить теги: ${err.message}`, "error"); }
}

async function loadMetadataHistory() {
  if (!state.metadataSelectedId) return;
  try {
    const result = await fetchApi(`/metadata/history?trackId=${encodeURIComponent(state.metadataSelectedId)}&limit=100`);
    state.metadataHistory = result.items || [];
    renderMetadataHistory();
  } catch (err) {
    const root = document.getElementById("metadata-history");
    if (root) root.innerHTML = `<p class="metadata-empty text-red">${escapeHtml(err.message)}</p>`;
  }
}

function renderMetadataHistory() {
  const root = document.getElementById("metadata-history");
  if (!root) return;
  if (!state.metadataHistory.length) {
    root.innerHTML = `<p class="metadata-empty">История появится после применения первого предложения.</p>`;
    return;
  }
  root.innerHTML = `<div class="metadata-history-actions"><span>${state.metadataHistorySelectedIds.size ? `Выбрано полей: ${state.metadataHistorySelectedIds.size}` : "Выберите изменения для точечного отката"}</span><button type="button" class="btn btn-secondary" ${state.metadataHistorySelectedIds.size ? "" : "disabled"} onclick="revertMetadataHistory()">Откатить выбранные</button></div>` + state.metadataHistory.map(item => {
    const oldValue = item.oldValue ?? null;
    const newValue = item.newValue ?? null;
    const reverted = !!item.revertedByProposalId;
    return `<label class="metadata-history-row ${reverted ? "is-reverted" : ""}"><input type="checkbox" ${state.metadataHistorySelectedIds.has(item.id) ? "checked" : ""} ${reverted ? "disabled" : ""} onchange="toggleMetadataHistorySelection(${item.id}, this.checked)"><span><strong>${escapeHtml(item.field)}</strong><span class="metadata-history-diff">${escapeHtml(historyValueLabel(oldValue))} → ${escapeHtml(historyValueLabel(newValue))}</span><small>Предложение #${item.proposalId} · ${escapeHtml(item.source || "manual")} · ${escapeHtml(item.actor || "system")} · ${new Date(item.createdAt * 1000).toLocaleString("ru-RU")}${reverted ? ` · откат #${item.revertedByProposalId}` : ""}</small></span></label>`;
  }).join("");
}

function toggleMetadataHistorySelection(id, selected) {
  if (selected) {
    const field = state.metadataHistory.find(item => item.id === id)?.field;
    state.metadataHistory.filter(item => item.field === field).forEach(item => state.metadataHistorySelectedIds.delete(item.id));
    state.metadataHistorySelectedIds.add(id);
  } else state.metadataHistorySelectedIds.delete(id);
  renderMetadataHistory();
}

async function revertMetadataHistory() {
  const historyIds = [...state.metadataHistorySelectedIds];
  if (!historyIds.length) return;
  try {
    const preview = await fetchApi("/metadata/history/revert/preview", "POST", { historyIds });
    const details = (preview.changes || []).map(change => `${change.field}: ${historyValueLabel(change.before)} → ${historyValueLabel(change.after)}`);
    if (!await confirmDialog({
      title: "Откатить выбранные поля?",
      message: preview.diverged ? "Текущие значения уже отличаются от этой записи истории. Проверьте изменения особенно внимательно." : "Откат будет создан как отдельное предложение в «Исправлениях».",
      details,
      confirmLabel: "Подготовить откат",
      tone: preview.diverged ? "danger" : "default"
    })) return;
    const result = await fetchApi("/metadata/history/revert/apply", "POST", { historyIds, fingerprint: preview.fingerprint });
    state.metadataHistorySelectedIds.clear();
    showToast(`Откат подготовлен (предложение #${result.stagingId})`, "success");
  } catch (err) { showToast(`Не удалось подготовить откат: ${err.message}`, "error"); }
}

async function refreshMetadataTrack() {
  if (!state.metadataSelectedId) return;
  try { await fetchApi("/metadata/refresh", "POST", { trackId: state.metadataSelectedId }); showToast("Обновление источников запущено", "info"); pollMetadataAuditStatus(); }
  catch (err) { showToast(`Не удалось запустить обновление: ${err.message}`, "error"); }
}

async function analyzeMetadataTrack() {
  if (!state.metadataSelectedId) return;
  try {
    await fetchApi("/metadata/analyze", "POST", { trackId: state.metadataSelectedId });
    showToast("Измерение громкости запущено", "info");
    setTimeout(() => selectMetadataTrack(state.metadataSelectedId), 3500);
  } catch (err) { showToast(`Не удалось запустить измерение: ${err.message}`, "error"); }
}

async function runMetadataNFO(action) {
  const folderPath = state.metadataDetail?.track?.folderPath;
  if (!folderPath) return;
  try { const result = await fetchApi("/metadata/nfo", "POST", { folderPath, action }); showToast(`Создано предложений: ${result.created || 0}`, "success"); }
  catch (err) { showToast(`Операция NFO не выполнена: ${err.message}`, "error"); }
}

async function stageMetadataReplayGain() {
  const targetLufs = parseFloat(metadataFormValue("md-target-lufs"));
  try { await fetchApi("/metadata/replaygain", "POST", { trackId: state.metadataSelectedId, targetLufs }); showToast("Теги ReplayGain добавлены в исправления", "success"); }
  catch (err) { showToast(`ReplayGain не подготовлен: ${err.message}`, "error"); }
}

function metadataArtworkScopeID(scope = "album") {
  const track = state.metadataDetail?.track || {};
  if (scope === "artist") return metadataExternalID("musicbrainz_artist") || track.albumArtist || track.artist || state.metadataSelectedId;
  return metadataExternalID("musicbrainz_release") || track.folderPath || track.album || state.metadataSelectedId;
}

function syncMetadataArtworkScope() {
  const role = metadataFormValue("md-art-role");
  const scope = document.getElementById("md-art-scope");
  if (!scope) return;
  if (role === "artist_primary") scope.value = "artist";
  if (role === "album_primary" || role === "disc") scope.value = "album";
}

async function stageMetadataArtwork() {
  const url = metadataFormValue("md-art-url");
  const scope = metadataFormValue("md-art-scope") || "album";
  const scopeId = metadataArtworkScopeID(scope);
  const role = metadataFormValue("md-art-role");
  const width = metadataFormNumber("md-art-width"), height = metadataFormNumber("md-art-height");
  const folderPath = state.metadataDetail?.track?.folderPath;
  try {
    await fetchApi("/metadata/artwork", "POST", { scope, scopeId, folderPath, role, candidates: [{ url, source: "manual", width, height, score: 80 }] });
    showToast("Изображение добавлено в исправления", "success");
  } catch (err) { showToast(`Изображение не подготовлено: ${err.message}`, "error"); }
}

async function loadMetadataArtwork() {
  const list = document.getElementById("metadata-artwork-list");
  if (!list || !state.metadataDetail) return;
  try {
    const results = await Promise.all(["album", "artist"].map(async scope => {
      const result = await fetchApi(`/metadata/artwork?scope=${scope}&scopeId=${encodeURIComponent(metadataArtworkScopeID(scope))}`);
      return (result.items || []).map(item => ({ ...item, scope }));
    }));
    const items = results.flat();
    list.innerHTML = items.length ? items.map(item => `<div class="metadata-list-row"><span>${item.scope === "artist" ? "Исполнитель" : "Альбом"} · ${escapeHtml(item.role)}</span><span class="mono">${item.width}×${item.height}</span></div>`).join("") : `<span class="metadata-section-note">Сохранённых ролей пока нет.</span>`;
  } catch (err) { list.innerHTML = `<span class="metadata-section-note">${escapeHtml(err.message)}</span>`; }
}

async function refreshMetadataSimilar(artistMbid) {
  if (!artistMbid) return;
  try { await fetchApi("/metadata/similar", "POST", { artistMbid, limit: 12 }, 30000); await loadMetadataSimilar(); showToast("Список похожих исполнителей обновлён", "success"); }
  catch (err) { showToast(`Не удалось обновить список: ${err.message}`, "error"); }
}

async function loadMetadataSimilar() {
  const list = document.getElementById("metadata-similar-list");
  const artistMbid = metadataExternalID("musicbrainz_artist");
  if (!list || !artistMbid) return;
  try {
    const result = await fetchApi(`/metadata/similar?artistMbid=${encodeURIComponent(artistMbid)}`);
    list.innerHTML = (result.items || []).length ? result.items.map(item => `<div class="metadata-list-row"><span>${escapeHtml(item.name)}</span><span class="mono">${Math.round((item.score || 0) * 100)}%</span></div>`).join("") : `<span class="metadata-section-note">Список ещё не загружен.</span>`;
  } catch (err) { list.innerHTML = `<span class="metadata-section-note">${escapeHtml(err.message)}</span>`; }
}

function renderMetadataPolicy() {
  const root = document.getElementById("metadata-policy-rules");
  if (!root) return;
  const labels = { manual: "Ручные правки", audio_file: "Теги файла", nfo: "NFO", musicbrainz: "MusicBrainz", derived: "Расчётные", navidrome: "Navidrome", legacy: "Совместимость" };
  root.innerHTML = (state.metadataPolicy?.rules || []).map((rule, index) => `<div class="metadata-policy-row" data-policy-index="${index}">
    <span>${escapeHtml(labels[rule.source] || rule.source)}${rule.provider ? ` · ${escapeHtml(rule.provider)}` : ""}</span>
    <input class="form-input mono" data-policy="priority" aria-label="Приоритет: ${escapeHtml(labels[rule.source] || rule.source)}" type="number" min="0" max="100000" value="${rule.priority}">
    <select class="form-select" data-policy="mode" aria-label="Режим обновления: ${escapeHtml(labels[rule.source] || rule.source)}"><option value="priority" ${rule.refreshMode === "priority" ? "selected" : ""}>По приоритету</option><option value="missing_only" ${rule.refreshMode === "missing_only" ? "selected" : ""}>Только пустые</option><option value="newer" ${rule.refreshMode === "newer" ? "selected" : ""}>Только новее</option><option value="never" ${rule.refreshMode === "never" ? "selected" : ""}>Не обновлять</option></select>
    <input class="form-input mono" data-policy="confidence" aria-label="Минимальная уверенность: ${escapeHtml(labels[rule.source] || rule.source)}" type="number" min="0" max="1" step="0.05" value="${rule.minConfidence}">
    <label class="metadata-checkbox"><input data-policy="enabled" type="checkbox" ${rule.enabled ? "checked" : ""}> Вкл.</label>
  </div>`).join("");
}

async function saveMetadataPolicy() {
  const rows = Array.from(document.querySelectorAll("[data-policy-index]"));
  const original = state.metadataPolicy?.rules || [];
  const rules = rows.map((row, index) => ({
    source: original[index].source, provider: original[index].provider || "", priority: parseInt(row.querySelector('[data-policy="priority"]').value, 10),
    refreshMode: row.querySelector('[data-policy="mode"]').value, enabled: row.querySelector('[data-policy="enabled"]').checked,
    minConfidence: parseFloat(row.querySelector('[data-policy="confidence"]').value)
  }));
  try { state.metadataPolicy = await fetchApi("/metadata/policy", "PUT", { rules }); renderMetadataPolicy(); showToast("Порядок источников сохранён", "success"); }
  catch (err) { showToast(`Порядок не сохранён: ${err.message}`, "error"); }
}

async function loadMetadataRecovery() {
  const root = document.getElementById("metadata-recovery-status");
  if (!root) return;
  try {
    const result = await fetchApi("/metadata/writeback");
    root.classList.toggle("is-ok", result.healthy);
    root.classList.toggle("is-warning", !result.healthy);
    root.textContent = result.healthy ? "Запись файлов: восстановление не требуется" : `Запись файлов: незавершённых ${result.incomplete}, ожидают сверки ${result.unreconciled}`;
  } catch (err) { root.textContent = `Состояние записи недоступно: ${err.message}`; }
}

async function runMetadataLibraryAudit() {
  try { await fetchApi("/scan/audit", "POST", { limitFolders: 100 }); showToast("Проверка медиатеки запущена", "info"); pollMetadataAuditStatus(); }
  catch (err) { showToast(`Не удалось запустить проверку: ${err.message}`, "error"); }
}

async function pollMetadataAuditStatus() {
  const badge = document.getElementById("metadata-audit-status");
  if (!badge) return;
  try {
    const status = await fetchApi("/scan/audit");
    badge.textContent = status.running ? (status.trackId ? "Проверяется трек" : "Проверяется медиатека") : status.finishedAt ? (status.error ? "Проверка завершилась с ошибкой" : `Готово · предложений ${status.created || 0}`) : "Аудит не запущен";
    if (!status.running && status.finishedAt && state.activeTab === "metadata") loadMetadataRecovery();
  } catch (err) { badge.textContent = "Статус аудита недоступен"; }
}

async function deleteTrackLyrics(trackId) {
  if (!trackId) return;
  const track = state.tracks.find(t => t.id === trackId) || state.selectedTrack;
  const titleName = track ? `«${track.artist || ""} — ${track.title || ""}»` : trackId;

  if (!await confirmDialog({
    title: "Удалить текст песни?",
    message: `Текст и файл .lrc будут удалены с диска для трека ${titleName}.`,
    confirmLabel: "Удалить",
    tone: "danger"
  })) {
    return;
  }

  try {
    await fetchApi("/lyrics/delete", "POST", { trackId });
    showToast("Текст песни и файл .lrc успешно удалены", "success");

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

async function saveTrackTags(trackId) {
  const val = (id) => (document.getElementById(id)?.value || "").trim();
  const payload = {
    trackId,
    title: val("tag-title"),
    artist: val("tag-artist"),
    album: val("tag-album"),
    albumArtist: val("tag-album-artist"),
    year: parseInt(val("tag-year"), 10) || 0,
  };
  try {
    const res = await fetchApi("/tracks/tags", "POST", payload, 60000);
    state.lastAppliedStagingId = res.stagingId; // правку можно откатить кнопкой Undo в Staging
    showToast("Теги записаны в файл", "success");
    document.getElementById("drawer-track-detail")?.classList.add("hidden");
    await loadTracks();
  } catch (err) {
    showToast(`Ошибка записи тегов: ${err.message}`, "error");
  }
}

async function auditSingleTrack(trackId) {
  try {
    await fetchApi("/scan/audit", "POST", { trackId });
    showToast("Проверка трека запущена в фоне — предложения появятся в «Исправлениях»", "info");
    setTimeout(loadStagingData, 3000);
  } catch (err) {
    showToast(`Ошибка запуска проверки: ${err.message}`, "error");
  }
}

async function enqueueSingleTournament(trackId) {
  try {
    await fetchApi("/lyrics/enqueue", "POST", { trackId, jobType: "tournament" });
    showToast("Задача турнира поставлена в очередь воркера", "success");
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
  const iconVolMuted = document.getElementById("icon-vol-muted");

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
  const closeWhisperModal = () => {
    document.getElementById("modal-whisper-offer")?.classList.add("hidden");
  };
  document.getElementById("btn-whisper-offer-cancel")?.addEventListener("click", closeWhisperModal);
  document.getElementById("btn-close-whisper-offer")?.addEventListener("click", closeWhisperModal);
  document.getElementById("btn-whisper-offer-confirm")?.addEventListener("click", () => {
    closeWhisperModal();
    const hasGroq = (state.keys || []).some(k => k.provider === "groq" && k.status !== "invalid");
    if (!hasGroq) {
      showToast("Для караоке Whisper требуется добавить бесплатный ключ Groq Cloud в «Настройки → Ключи API».", "info");
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
      <div class="sidebar-track-item ${isSelected ? "active" : ""}" onclick="openTrackInKaraoke('${escapeJsArg(t.id)}')">
        <div class="sidebar-track-meta">
          <div class="sidebar-track-title">${escapeHtml(t.title || "Без названия")}</div>
          <div class="sidebar-track-artist">${escapeHtml(t.artist || "—")}</div>
        </div>
        <div>${badge}</div>
      </div>
    `;
  }).join("");
}

async function openTrackInKaraoke(trackId, fromRoute = false) {
  if (window.TimelineEditor && window.TimelineEditor.isDirty() &&
      state.selectedTrack && state.selectedTrack.id !== trackId &&
      !await confirmDialog({
        title: "Есть несохранённые изменения",
        message: "Если перейти к другому треку, правки таймингов будут потеряны.",
        confirmLabel: "Перейти без сохранения",
        cancelLabel: "Остаться",
        tone: "danger"
      })) {
    return;
  }
  if (state.selectedTrack && state.selectedTrack.id !== trackId) {
    if (karaokeJobPollTimer) {
      clearInterval(karaokeJobPollTimer);
      karaokeJobPollTimer = null;
    }
    hideKaraokeJobBanner();
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
  // По треку уже идёт задача (например, вернулись на страницу) — сразу показываем её статус.
  if (!karaokeJobPollTimer) {
    fetchApi(`/jobs?trackId=${encodeURIComponent(track.id)}`).then(({ job }) => {
      const active = job && ["pending", "running", "waiting_quota", "paused"].includes(job.status);
      if (active && state.selectedTrack?.id === track.id && !karaokeJobPollTimer) {
        startPollingKaraokeJob(track.id, "Проверяем статус задачи...");
      }
    }).catch(() => {});
  }
  if (!fromRoute) navigate(`/lyrics/${encodeURIComponent(track.id)}`);
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
      showToast(`Файл .lrc сохранён рядом с аудиофайлом`, "success");
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

// kind: "progress" (spinner), "done" (final, no spinner) or "error" (final, red).
function showKaraokeJobBanner(text, kind = "progress") {
  const banner = document.getElementById("karaoke-job-banner");
  const txt = document.getElementById("karaoke-job-text");
  if (banner && txt) {
    txt.textContent = text;
    txt.title = text; // full text when the banner clamps a long status
    banner.classList.toggle("is-final", kind !== "progress");
    banner.classList.toggle("is-error", kind === "error");
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
  const stop = (text, hideAfterMs, kind = "done") => {
    clearInterval(karaokeJobPollTimer);
    karaokeJobPollTimer = null;
    if (text) showKaraokeJobBanner(text, kind);
    setTimeout(hideKaraokeJobBanner, hideAfterMs);
  };

  // Опрашиваем, пока задача не завершится: караоке строится минуты, поэтому лимита по времени нет.
  // Переход к другому треку останавливает опрос в openTrackInKaraoke.
  karaokeJobPollTimer = setInterval(async () => {
    try {
      const { job } = await fetchApi(`/jobs?trackId=${encodeURIComponent(trackId)}`);
      if (!job) return stop("", 0);

      if (job.status === "running") {
        showKaraokeJobBanner(job.errorLog || (job.jobType === "whisper_karaoke"
          ? "Генерация пословных таймкодов караоке через AI Whisper..."
          : "Сравнение текстов в турнире (LRCLIB, Yandex, Genius)..."));
      } else if (job.status === "pending") {
        showKaraokeJobBanner("Задача в очереди, ждёт воркер...");
      } else if (job.status === "waiting_quota") {
        showKaraokeJobBanner(job.errorLog || "Ожидание освобождения API-ключей...");
      } else if (job.status === "paused") {
        showKaraokeJobBanner("Задача на паузе. Возобновить можно в очереди задач.");
      } else if (job.status === "done") {
        stop("Текст успешно обновлен (источник: poisq)", 2500);
        await refreshCurrentKaraokeTrack(trackId);
      } else if (job.status === "failed") {
        stop(job.errorLog ? `Не получилось: ${job.errorLog}` : "Текст не найден или ошибка провайдера", 5000, "error");
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

        window.TimelineEditor?.load(updatedTrack, state.lyrics, hasValidLrcTimestamps, res.confidence || null);
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
    showToast("Поиск текста запущен", "info");
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
  modal?.classList.remove("hidden");
}

async function enqueueWhisperKaraoke() {
  if (!state.selectedTrack) {
    showToast("Сначала выберите трек в караоке-плеере", "error");
    return;
  }

  const hasGroq = (state.keys || []).some(k => k.provider === "groq" && k.status !== "invalid");
  if (!hasGroq) {
    showToast("Для создания караоке Whisper требуется добавить ключ Groq Cloud в «Настройки → Ключи API».", "info");
    switchTab("keys");
    return;
  }

  const trackId = state.selectedTrack.id;
  try {
    await fetchApi("/lyrics/enqueue", "POST", {
      trackId,
      jobType: "whisper_karaoke"
    });
    showToast("Задача AI Караоке (Groq Whisper Large v3) запущена", "info");
    startPollingKaraokeJob(trackId, "Создание синхронного караоке через AI Whisper...");
    loadJobsData();
  } catch (err) {
    showToast(`Ошибка: ${err.message}`, "error");
  }
}

// -----------------------------------------------------------------------------
// 5. Staging-буфер рефакторинга: Side-by-Side обложки и чистый диф тегов
// -----------------------------------------------------------------------------
async function loadStagingData(append = false) {
  const container = document.getElementById("staging-cards-container");
  if (!container) return;

  try {
    const cursor = append && state.stagingNextCursor ? `&cursor=${encodeURIComponent(state.stagingNextCursor)}` : "";
    const res = await fetchApi(`/staging?status=pending&limit=100${cursor}`);
    state.stagingItems = append ? state.stagingItems.concat(res.items || []) : (res.items || []);
    state.stagingNextCursor = res.nextCursor || "";
  } catch (err) {
    console.error("Ошибка загрузки Staging:", err);
    if (!append) state.stagingItems = [];
    state.stagingNextCursor = "";
  }

  const badgeStaging = document.getElementById("badge-staging-count");
  if (badgeStaging) badgeStaging.textContent = state.stagingItems.length;

  if (state.stagingItems.length === 0) {
    container.innerHTML = `<div class="cell-empty">Исправлений нет. Все теги в порядке. Нажмите «Запустить аудит», чтобы поискать новые.</div>`;
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
                  onclick="toggleCoverCandidates(${item.id}, '${escapeJsArg(propArtist)}', '${escapeJsArg(propAlbum)}', '${escapeJsArg(path)}')"
                >
                  Другие варианты обложек <span id="cand-count-${item.id}"></span>
                </button>
                <button 
                  class="cover-ai-resolve-btn" 
                  id="btn-ai-resolve-${item.id}"
                  onclick="resolveCoverViaAI(${item.id}, '${escapeJsArg(path)}')"
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
    if (item.changeType === "manual") issueTitle = "Ручная правка тегов";

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
  }).join("") + (state.stagingNextCursor ? `<div class="staging-load-more"><button type="button" class="btn btn-secondary" onclick="loadStagingData(true)">Показать ещё исправления</button></div>` : "");
}

async function applyStaging(id) {
  try {
    const payload = { id };
    if (window.selectedCovers && window.selectedCovers[id]) {
      payload.coverUrl = window.selectedCovers[id];
    }
    await fetchApi("/staging/apply", "POST", payload);
    state.lastAppliedStagingId = id;
    showToast("Изменения успешно применены в файл", "success");
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
        <button class="btn btn-secondary cover-cand-btn" onclick="selectCoverCandidate(${itemId}, ${idx})">${isSel ? 'Выбрано' : 'Выбрать'}</button>
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
        if (btn) btn.innerText = "Выбрано";
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

  if (!await confirmDialog({
    title: "Применить все предложения?",
    message: `Изменения (${state.stagingItems.length}) будут записаны в файлы медиатеки.`,
    confirmLabel: "Применить все"
  })) {
    return;
  }

  const ids = state.stagingItems.map(item => item.id);
  try {
    const res = await fetchApi("/staging/apply", "POST", { ids });
    showToast(`Успешно применено изменений: ${res.applied}`, "success");
    await loadStagingData();
    await loadLibraryData();
  } catch (err) {
    showToast(`Ошибка применения: ${err.message}`, "error");
  }
}

async function resetFullDatabase() {
  const confirmed = await confirmDialog({
    title: "Полностью сбросить базу poisq?",
    message: "После сброса каталог будет заново прочитан из Navidrome.",
    details: [
      "Удалятся все предложения исправлений",
      "Сбросятся кэш треков и текстов",
      "API-ключи сохранятся",
      "Сразу запустится новое сканирование Navidrome"
    ],
    confirmLabel: "Сбросить базу",
    tone: "danger"
  });
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
    showToast(`База успешно сброшена! Синхронизировано треков: ${res.syncedTracks || 0}`, "success");
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

async function discardAllStaging() {
  if (state.stagingItems.length === 0) return;
  if (!await confirmDialog({
    title: "Отклонить все исправления?",
    message: "Все текущие предложения исчезнут из очереди.",
    confirmLabel: "Отклонить все",
    tone: "danger"
  })) return;

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
    showToast("Последнее изменение успешно откатано", "success");
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

    setJobsCount(state.jobs.filter(j => j.status === "pending" || j.status === "running").length);

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
      else if (j.status === "done" || j.status === "completed") statusBadge = `<span class="text-emerald" style="font-weight: 500;">Завершена</span>`;
      else if (j.status === "failed") statusBadge = `<span class="text-crimson" style="color:var(--color-crimson, #f43f5e);">Ошибка</span>`;
      else if (j.status === "waiting_quota") statusBadge = `<span class="text-amber">Ждет квоту API</span>`;
      else if (j.status === "paused") statusBadge = `<span class="badge-audit-queued" style="background: var(--bg-subtle); color: var(--text-secondary);">На паузе</span>`;

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
        stepSummaryHtml = `<span class="text-emerald">Текст сохранен (.lrc · poisq)</span>`;
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
    logLinesHtml = `<div class="job-log-line text-emerald">Задача успешно выполнена. Синхронный .lrc файл записан рядом с треком.</div>`;
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
            ${trackObj ? `<button class="btn-table-action primary" onclick="event.stopPropagation(); openTrackInKaraoke('${escapeJsArg(trackObj.id)}')">Открыть текст</button>` : ""}
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
    showToast(`${msg}`, "success");
    await loadJobsData();
  } catch (err) {
    showToast(`Ошибка: ${err.message}`, "error");
  }
}

function setJobsCount(count) {
  document.querySelectorAll(".jobs-count").forEach(el => { el.textContent = count; });
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
      showToast("Ключ зашифрован (AES-256-GCM) и добавлен в пул ротации", "success");
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

  const poolKeys = state.keys.filter(k => k.provider !== "music_assistant" && k.provider !== "navidrome");
  if (poolKeys.length === 0) {
    tbody.innerHTML = `<tr><td colspan="5" class="cell-empty">В пуле пока нет ключей. Нажмите «+ Добавить ключ» для подключения Groq Cloud, Яндекс Музыки или Tavily Search.</td></tr>`;
    return;
  }

  const providerNames = {
    groq: "Groq Cloud (Whisper + LLM)",
    yandex: "Яндекс Музыка",
    tavily: "Tavily Search",
    gemini: "Google Gemini",
    acoustid: "AcoustID (fpcalc)",
    genius: "Genius API"
  };

  tbody.innerHTML = poolKeys.map(k => {
    let statusBadge = `<span class="text-emerald">Активен</span>`;
    if (k.status === "cooldown") {
      statusBadge = `<span class="text-amber">429 Cooldown (65с)</span>`;
    } else if (k.status === "exhausted_daily") {
      statusBadge = `<span class="text-amber">Исчерпан до 11:00 МСК</span>`;
    } else if (k.status === "invalid") {
      statusBadge = `<span class="text-crimson" style="color: var(--color-crimson);">Невалиден (401/403)</span>`;
    } else if (k.status === "decrypt_error") {
      statusBadge = `<span class="text-amber">Не расшифрован: проверьте POISQ_SECRET_KEY</span>`;
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
  if (!await confirmDialog({
    title: "Удалить API-ключ?",
    message: "Ключ будет удалён из пула ротации.",
    confirmLabel: "Удалить",
    tone: "danger"
  })) return;

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
function collectCastFormPayload(extra = {}) {
  const boxes = [...document.querySelectorAll("#cast-players-list input[type=checkbox]")];
  const tokenInput = document.getElementById("cast-token");
  const token = tokenInput?.value.trim() || "";
  return {
    url: document.getElementById("cast-url")?.value.trim() || "",
    provider: document.getElementById("cast-provider")?.value || "",
    // MA недоступен и список не построен — не теряем уже выбранные колонки
    players: boxes.length ? boxes.filter(b => b.checked).map(b => b.value) : (state.castPlayers || []),
    ...(token ? { token } : {}),
    ...extra
  };
}

function initCastForm() {
  document.getElementById("form-cast")?.addEventListener("submit", async (e) => {
    e.preventDefault();
    const payload = collectCastFormPayload();
    const btn = document.getElementById("btn-save-cast");
    if (btn) btn.disabled = true;
    const res = await loadCastSettings(payload);
    if (btn) btn.disabled = false;
    const tokenInput = document.getElementById("cast-token");
    if (res && tokenInput) tokenInput.value = "";
    if (res) {
      showToast(
        res.error ? "Настройки сохранены, но сервер MA не ответил" : "Настройки Music Assistant сохранены",
        res.error ? "info" : "success"
      );
    }
  });

  document.getElementById("btn-clear-cast-token")?.addEventListener("click", async () => {
    if (!await confirmDialog({
      title: "Удалить токен Music Assistant?",
      message: "Сохранённый токен доступа будет удалён.",
      confirmLabel: "Удалить",
      tone: "danger"
    })) return;
    const tokenInput = document.getElementById("cast-token");
    if (tokenInput) tokenInput.value = "";
    const payload = collectCastFormPayload({ token: "", clearToken: true });
    const res = await loadCastSettings(payload);
    if (res) showToast("Токен Music Assistant удален", "info");
  });
}

function initNavidromeAccountForm() {
  const form = document.getElementById("form-navidrome-account");
  form?.addEventListener("submit", async (e) => {
    e.preventDefault();
    const btn = document.getElementById("btn-save-navidrome-account");
    if (btn) btn.disabled = true;
    const res = await loadNavidromeAccount({
      username: document.getElementById("navidrome-username")?.value.trim() || "",
      password: document.getElementById("navidrome-password")?.value || ""
    });
    if (btn) btn.disabled = false;
    if (res) showToast("Учётная запись Navidrome сохранена", "success");
  });
  document.getElementById("btn-clear-navidrome-account")?.addEventListener("click", async () => {
    if (!await confirmDialog({
      title: "Удалить учётную запись Navidrome?",
      message: "Сохранённые данные служебной учётной записи будут удалены.",
      confirmLabel: "Удалить",
      tone: "danger"
    })) return;
    const res = await loadNavidromeAccount({ clear: true });
    if (res) showToast("Учётная запись Navidrome удалена", "info");
  });
}

function initPublicAccessForm() {
  const box = document.getElementById("public-access-navidrome-web");
  box?.addEventListener("change", async () => {
    const open = box.checked;
    if (open && !await confirmDialog({
      title: "Открыть веб-интерфейс Navidrome?",
      message: "Панель и собственное API Navidrome станут доступны через публичный вход, в том числе через CDN.",
      confirmLabel: "Открыть",
      tone: "danger"
    })) {
      box.checked = false;
      return;
    }
    box.disabled = true;
    const res = await loadPublicAccess({ navidromeWebOpen: open });
    box.disabled = false;
    if (res) showToast(res.navidromeWebOpen ? "Веб-интерфейс Navidrome открыт" : "Веб-интерфейс Navidrome закрыт", res.navidromeWebOpen ? "info" : "success");
  });
}

function initSecureTransportForm() {
  document.getElementById("form-secure")?.addEventListener("submit", async (e) => {
    e.preventDefault();
    const btn = document.getElementById("btn-save-secure");
    if (btn) btn.disabled = true;
    const res = await loadSecureTransport({
      enabled: !!document.getElementById("secure-enabled")?.checked,
      publicOrigin: document.getElementById("secure-origin")?.value.trim() || ""
    });
    if (btn) btn.disabled = false;
    if (res) showToast(res.enabled ? "Secure Transport включён" : "Secure Transport выключен", "success");
  });
}

async function loadSecureTransport(payload = null) {
  const status = document.getElementById("secure-status");
  const badge = document.getElementById("secure-status-badge");
  const pairing = document.getElementById("secure-pairing");
  if (!status) return null;
  let res;
  try {
    res = payload ? await fetchApi("/secure-transport", "POST", payload) : await fetchApi("/secure-transport");
  } catch (err) {
    status.textContent = `Ошибка: ${err.message}`;
    return null;
  }
  status.textContent = "";
  document.getElementById("secure-enabled").checked = !!res.enabled;
  document.getElementById("secure-origin").value = res.publicOrigin || "";
  if (badge) {
    badge.className = res.enabled ? "badge badge-success" : "badge badge-muted";
    badge.textContent = res.enabled ? "Включён" : "Выключен";
  }
  const showQR = !!(res.enabled && res.pairingCode);
  pairing?.classList.toggle("hidden", !showQR);
  if (showQR) {
    // Новый адрес на каждую загрузку: QR меняется вместе с адресом и ключом.
    document.getElementById("secure-qr").src = `/api/v1/secure-transport/qr.svg?t=${Date.now()}`;
    document.getElementById("secure-fingerprint").textContent = res.fingerprint || "";
    document.getElementById("secure-epoch").textContent = res.certificateExpiresAt
      ? `Рабочий ключ: эпоха ${res.currentEpoch}, обновится автоматически до ${new Date(res.certificateExpiresAt * 1000).toLocaleString("ru-RU")}`
      : "";
  }
  return res;
}

async function loadSecureDevices(payload = null) {
  const box = document.getElementById("secure-devices");
  if (!box) return null;
  let res;
  try {
    res = payload ? await fetchApi("/secure-transport/devices", "POST", payload) : await fetchApi("/secure-transport/devices");
  } catch (err) {
    box.textContent = `Ошибка: ${err.message}`;
    return null;
  }
  const devices = res.devices || [];
  if (!devices.length) {
    box.textContent = "Пока ни одно устройство не входило.";
    return res;
  }
  const fmt = (v) => new Date(v).toLocaleString("ru-RU");
  box.innerHTML = devices.map(d => `
    <div class="cast-footer">
      <p class="cast-status-text"><strong>${escapeHtml(d.name)}</strong> · ${escapeHtml(d.username)}<br>
        <span class="form-hint">Вход ${escapeHtml(fmt(d.createdAt))} · активность ${escapeHtml(fmt(d.lastUsedAt))}</span></p>
      <button type="button" class="btn btn-secondary" data-revoke-device="${escapeHtml(d.id)}">Отозвать</button>
    </div>`).join("");
  box.querySelectorAll("[data-revoke-device]").forEach(btn => btn.addEventListener("click", async () => {
    const device = devices.find(d => d.id === btn.dataset.revokeDevice);
    if (!await confirmDialog({
      title: "Отозвать устройство?",
      message: `«${device?.name || ""}» (${device?.username || ""}) потеряет доступ, его сессии завершатся. Для повторного входа понадобится пароль.`,
      confirmLabel: "Отозвать",
      tone: "danger"
    })) return;
    btn.disabled = true;
    if (await loadSecureDevices({ id: btn.dataset.revokeDevice })) showToast("Устройство отозвано", "success");
    btn.disabled = false;
  }));
  return res;
}

async function loadPublicAccess(payload = null) {
  const box = document.getElementById("public-access-navidrome-web");
  const status = document.getElementById("public-access-status");
  const badge = document.getElementById("public-access-badge");
  if (!box) return null;
  let res;
  try {
    res = payload ? await fetchApi("/public-access", "POST", payload) : await fetchApi("/public-access");
  } catch (err) {
    if (status) status.textContent = `Ошибка: ${err.message}`;
    return null;
  }
  if (status) status.textContent = "";
  box.checked = !!res.navidromeWebOpen;
  if (badge) {
    badge.className = res.navidromeWebOpen ? "badge badge-danger" : "badge badge-success";
    badge.textContent = res.navidromeWebOpen ? "Веб открыт" : "Только Subsonic";
  }
  return res;
}

async function loadNavidromeAccount(payload = null) {
  const status = document.getElementById("navidrome-account-status");
  const badge = document.getElementById("navidrome-account-badge");
  if (!status) return null;
  let res;
  try {
    res = payload ? await fetchApi("/navidrome/account", "POST", payload) : await fetchApi("/navidrome/account");
  } catch (err) {
    status.textContent = `Ошибка: ${err.message}`;
    return null;
  }
  status.textContent = "";
  document.getElementById("navidrome-password").value = "";
  document.getElementById("navidrome-username").value = res.username || "";
  document.getElementById("btn-clear-navidrome-account")?.classList.toggle("hidden", !res.configured);
  if (badge) {
    badge.className = res.configured ? "badge badge-success" : "badge badge-muted";
    badge.textContent = res.configured ? "Задана" : "Не задана";
  }
  return res;
}

async function loadCastSettings(payload = null) {
  const status = document.getElementById("cast-status");
  const statusBadge = document.getElementById("cast-status-badge");
  const tokenBadge = document.getElementById("cast-token-badge");
  const clearTokenBtn = document.getElementById("btn-clear-cast-token");
  const tokenInput = document.getElementById("cast-token");
  if (!status) return null;

  let res;
  try {
    res = payload ? await fetchApi("/cast", "POST", payload) : await fetchApi("/cast");
  } catch (err) {
    status.textContent = `Ошибка: ${err.message}`;
    if (statusBadge) {
      statusBadge.className = "badge badge-danger";
      statusBadge.textContent = "Ошибка";
    }
    return null;
  }

  const st = res.settings || {};
  state.castPlayers = st.players || [];
  document.getElementById("cast-url").value = st.url || "";

  if (tokenBadge) {
    if (res.hasToken) {
      tokenBadge.textContent = `Сохранён (${res.tokenMask || "••••"})`;
      tokenBadge.className = "cast-token-meta text-success";
    } else {
      tokenBadge.textContent = "Не задан";
      tokenBadge.className = "cast-token-meta text-muted";
    }
  }
  if (clearTokenBtn) {
    clearTokenBtn.classList.toggle("hidden", !res.hasToken);
  }
  if (tokenInput) {
    tokenInput.placeholder = res.hasToken
      ? "Введите новый токен для замены текущего..."
      : "Вставьте долгосрочный токен MA...";
  }

  const sel = document.getElementById("cast-provider");
  const providers = res.providers || [];
  let options = `<option value="">— выберите источник —</option>` +
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

  if (!st.url) {
    status.textContent = "Укажите адрес сервера и токен доступа Music Assistant.";
    if (statusBadge) {
      statusBadge.className = "badge badge-muted";
      statusBadge.textContent = "Не настроен";
    }
  } else if (!res.hasToken) {
    status.textContent = "Укажите токен доступа Music Assistant и нажмите «Проверить и сохранить».";
    if (statusBadge) {
      statusBadge.className = "badge badge-warning";
      statusBadge.textContent = "Нет токена";
    }
  } else if (res.error) {
    status.textContent = `Music Assistant: ${res.error}`;
    if (statusBadge) {
      statusBadge.className = "badge badge-warning";
      statusBadge.textContent = "Нет связи";
    }
  } else if (!st.provider) {
    status.textContent = "Выберите источник музыки и колонки, затем сохраните настройки.";
    if (statusBadge) {
      statusBadge.className = "badge badge-warning";
      statusBadge.textContent = "Выберите источник";
    }
  } else {
    status.textContent = `Подключено к Music Assistant. Активных колонок в приложении: ${state.castPlayers.length}.`;
    if (statusBadge) {
      statusBadge.className = "badge badge-success";
      statusBadge.textContent = "Подключено";
    }
  }
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
    showToast(`Синхронизация завершена: обновлено ${res.syncedTracks} треков`, "success");
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
    showToast(res.message || "Аудит запущен. Результаты появятся в разделе «Исправления».", "success");
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
