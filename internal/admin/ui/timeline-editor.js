/*
 * poisq Studio — Редактор таймингов LRC с крупной волной (Timeline Editor)
 *
 * Что делает этот код и для чего он нужен:
 * Редактор строк LRC в стиле таймлайна видеоредакторов. Сверху список строк: у каждой правятся
 * текст и время (мм:сс.хх). Внизу крупная волна трека: при выборе строки на ней появляется
 * флажок-метка, которую можно перетаскивать мышью, двигать стрелками (±0.1 с, с Shift ±0.01 с)
 * или поправить время вручную в списке. Остальные метки видны тусклыми, чтобы была видна структура
 * песни. Клик по пустому месту снимает выделение и ставит курсор воспроизведения.
 * Изменения копятся в рабочей копии (с отменой/повтором) и записываются только по «Сохранить»
 * тем же путём, что и сырой редактор .lrc.
 *
 * Волна приходит с сервера (/api/v1/tracks/waveform: 100 пар min/max в секунду), рисуется на canvas
 * без сторонних библиотек. Звук воспроизводит общий <audio id="audio-stream"> панели плеера.
 *
 * Зависит от глобальных функций и состояния app.js: state, fetchApi, showToast, escapeHtml,
 * updatePlayerUI, renderLyricsTeleprompter, loadTracks, parseLRC.
 */
(function () {
  "use strict";

  const RULER_H = 26;
  const HIT_PX = 8;
  const MIN_GAP = 0.01;
  const HISTORY_LIMIT = 200;

  const ed = {
    track: null,
    lines: [],            // [{id, sec, text}]
    original: "",         // LRC исходного состояния (для dirty)
    selectedId: null,
    nextId: 1,
    history: [],
    histIdx: -1,
    synthetic: false,     // у трека нет настоящих таймингов
    wave: null,           // {rate, duration, min:Int8Array, max:Int8Array}
    waveState: "idle",    // idle | loading | ready | error
    waveError: "",
    pxPerSec: 80,
    viewStart: 0,         // секунда у левого края
    drag: null,           // {id, moved}
    hoverCursor: "default",
    raf: 0,
    colors: null,
    colorsAt: 0,
    canvas: null,
    ctx: null,
    cssW: 0,
    cssH: 0,
    visible: false,
  };

  // ---------------------------------------------------------------------------
  // Время и LRC
  // ---------------------------------------------------------------------------
  function fmtTime(sec) {
    const t = Math.max(0, Math.round(sec * 100));
    const m = Math.floor(t / 6000);
    const s = Math.floor((t % 6000) / 100);
    const cs = t % 100;
    return `${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}.${String(cs).padStart(2, "0")}`;
  }

  function parseTimeInput(str) {
    const s = String(str || "").trim().replace(",", ".");
    if (!s) return null;
    const m = s.match(/^(?:(\d+):)?(\d+(?:\.\d+)?)$/);
    if (!m) return null;
    const v = (m[1] ? parseInt(m[1], 10) * 60 : 0) + parseFloat(m[2]);
    return Number.isFinite(v) ? v : null;
  }

  function buildLrc(lines) {
    return lines
      .filter((l) => l.text.trim() !== "")
      .slice()
      .sort((a, b) => a.sec - b.sec)
      .map((l) => `[${fmtTime(l.sec)}]${l.text.trim()}`)
      .join("\n");
  }

  function duration() {
    if (ed.wave && ed.wave.duration > 0) return ed.wave.duration;
    return state.totalSec || 0;
  }

  function clamp(v, lo, hi) {
    return Math.max(lo, Math.min(hi, v));
  }

  // ---------------------------------------------------------------------------
  // Рабочая копия, история, dirty
  // ---------------------------------------------------------------------------
  function snapshot() {
    return ed.lines.map((l) => ({ id: l.id, sec: l.sec, text: l.text }));
  }

  function sortLines() {
    ed.lines.sort((a, b) => a.sec - b.sec);
  }

  function resetHistory() {
    ed.history = [JSON.stringify(snapshot())];
    ed.histIdx = 0;
    updateToolbar();
  }

  function commit() {
    const cur = JSON.stringify(snapshot());
    if (cur === ed.history[ed.histIdx]) return;
    ed.history = ed.history.slice(0, ed.histIdx + 1);
    ed.history.push(cur);
    if (ed.history.length > HISTORY_LIMIT) ed.history.shift();
    ed.histIdx = ed.history.length - 1;
    updateToolbar();
  }

  function restore(idx) {
    ed.histIdx = idx;
    ed.lines = JSON.parse(ed.history[idx]);
    if (!ed.lines.some((l) => l.id === ed.selectedId)) ed.selectedId = null;
    renderRows();
    updateToolbar();
    draw();
  }

  function undo() {
    if (ed.histIdx > 0) restore(ed.histIdx - 1);
  }

  function redo() {
    if (ed.histIdx < ed.history.length - 1) restore(ed.histIdx + 1);
  }

  function isDirty() {
    return !!ed.track && buildLrc(ed.lines) !== ed.original;
  }

  // ---------------------------------------------------------------------------
  // Загрузка трека
  // ---------------------------------------------------------------------------
  function load(track, lyrics, hasTimestamps) {
    ed.track = track;
    ed.nextId = 1;
    ed.synthetic = !hasTimestamps && (lyrics || []).length > 0;
    ed.lines = (lyrics || []).map((l) => ({ id: ed.nextId++, sec: l.sec, text: l.text }));
    sortLines();
    ed.original = ed.synthetic ? "" : buildLrc(ed.lines);
    ed.selectedId = null;
    ed.wave = null;
    ed.waveState = "idle";
    ed.viewStart = 0;
    resetHistory();
    renderRows();
    updateToolbar();
    if (ed.visible) startWave();
    draw();
  }

  async function startWave() {
    if (!ed.track || ed.waveState === "loading" || ed.waveState === "ready") return;
    const trackId = ed.track.id;
    ed.waveState = "loading";
    draw();
    try {
      const data = await fetchApi(`/tracks/waveform?id=${encodeURIComponent(trackId)}`, "GET", null, 180000);
      if (!ed.track || ed.track.id !== trackId) return;
      ed.wave = {
        rate: data.rate || 100,
        duration: data.duration || 0,
        min: Int8Array.from(data.min || []),
        max: Int8Array.from(data.max || []),
      };
      ed.waveState = "ready";
    } catch (err) {
      if (!ed.track || ed.track.id !== trackId) return;
      ed.waveState = "error";
      ed.waveError = err.message || "ошибка";
    }
    draw();
  }

  // ---------------------------------------------------------------------------
  // Список строк
  // ---------------------------------------------------------------------------
  function rowsEl() {
    return document.getElementById("tl-rows");
  }

  function renderRows() {
    const box = rowsEl();
    if (!box) return;
    if (ed.lines.length === 0) {
      box.innerHTML = `<div class="cell-empty">Текста пока нет. Нажмите «+ Строка» или найдите текст кнопкой в шапке.</div>`;
      return;
    }
    const note = ed.synthetic
      ? `<div class="tl-note">У этого текста нет настоящих таймингов: времена выставлены по 5 секунд. Расставьте их на волне или вручную.</div>`
      : "";
    box.innerHTML =
      note +
      ed.lines
        .map(
          (l, i) => `
      <div class="tl-row${l.id === ed.selectedId ? " selected" : ""}" data-id="${l.id}">
        <span class="tl-num mono">${i + 1}</span>
        <input class="tl-time mono" value="${fmtTime(l.sec)}" spellcheck="false" aria-label="Время строки ${i + 1}" />
        <input class="tl-text" value="${escapeHtml(l.text).replace(/"/g, "&quot;")}" aria-label="Текст строки ${i + 1}" />
        <button class="tl-del" title="Удалить строку" aria-label="Удалить строку ${i + 1}">✕</button>
      </div>`
        )
        .join("");
  }

  function rowById(id) {
    return rowsEl()?.querySelector(`.tl-row[data-id="${id}"]`);
  }

  function lineById(id) {
    return ed.lines.find((l) => l.id === id);
  }

  function syncRowTime(id) {
    const l = lineById(id);
    const row = rowById(id);
    if (l && row) {
      const inp = row.querySelector(".tl-time");
      if (inp && document.activeElement !== inp) inp.value = fmtTime(l.sec);
    }
  }

  function markSelectedRow(scroll) {
    rowsEl()?.querySelectorAll(".tl-row").forEach((r) => {
      r.classList.toggle("selected", Number(r.dataset.id) === ed.selectedId);
    });
    if (scroll && ed.selectedId != null) {
      rowById(ed.selectedId)?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  }

  function onRowsClick(e) {
    const row = e.target.closest(".tl-row");
    if (!row) return;
    const id = Number(row.dataset.id);
    if (e.target.closest(".tl-del")) {
      const idx = ed.lines.findIndex((l) => l.id === id);
      if (idx >= 0) {
        ed.lines.splice(idx, 1);
        if (ed.selectedId === id) ed.selectedId = null;
        renderRows();
        commit();
        draw();
      }
      return;
    }
    select(id, { center: true, seek: true });
  }

  function onRowsFocusIn(e) {
    const row = e.target.closest(".tl-row");
    if (!row) return;
    const id = Number(row.dataset.id);
    if (id !== ed.selectedId) select(id, { center: true, seek: true, keepFocus: true });
  }

  function onRowsInput(e) {
    if (!e.target.classList.contains("tl-text")) return;
    const id = Number(e.target.closest(".tl-row").dataset.id);
    const l = lineById(id);
    if (l) {
      l.text = e.target.value;
      updateToolbar();
      draw();
    }
  }

  function onRowsChange(e) {
    const row = e.target.closest(".tl-row");
    if (!row) return;
    const id = Number(row.dataset.id);
    const l = lineById(id);
    if (!l) return;
    if (e.target.classList.contains("tl-text")) {
      l.text = e.target.value;
      commit();
    } else if (e.target.classList.contains("tl-time")) {
      const v = parseTimeInput(e.target.value);
      if (v === null) {
        e.target.value = fmtTime(l.sec);
        showToast("Время вводится как мм:сс.хх, например 01:23.45", "error");
        return;
      }
      l.sec = clamp(v, 0, duration() || v);
      const before = ed.lines.map((x) => x.id).join(",");
      sortLines();
      if (ed.lines.map((x) => x.id).join(",") !== before) {
        renderRows();
        markSelectedRow(true);
      } else {
        e.target.value = fmtTime(l.sec);
      }
      commit();
      if (id === ed.selectedId) centerOn(l.sec);
      draw();
    }
  }

  function onRowsKeydown(e) {
    if (e.key === "Enter" && e.target.tagName === "INPUT") {
      e.target.blur();
    }
  }

  // ---------------------------------------------------------------------------
  // Выбор строки, перемотка, воспроизведение
  // ---------------------------------------------------------------------------
  function setPlayhead(sec) {
    const t = clamp(sec, 0, duration() || sec);
    state.currentSec = t;
    const a = state.audioEl;
    if (a && !isNaN(a.duration)) a.currentTime = t;
    if (typeof updatePlayerUI === "function") updatePlayerUI();
  }

  function centerOn(sec) {
    const w = ed.cssW || 800;
    ed.viewStart = clampView(sec - w / ed.pxPerSec / 2);
  }

  function clampView(v) {
    const total = duration();
    const visible = (ed.cssW || 800) / ed.pxPerSec;
    return clamp(v, 0, Math.max(0, total - visible));
  }

  function select(id, opts) {
    opts = opts || {};
    ed.selectedId = id;
    const l = lineById(id);
    markSelectedRow(!opts.fromRow);
    if (l) {
      if (opts.center) centerOn(l.sec);
      if (opts.seek) setPlayhead(l.sec);
    }
    updateToolbar();
    draw();
  }

  function deselect() {
    if (ed.selectedId == null) return;
    ed.selectedId = null;
    commit();
    markSelectedRow(false);
    updateToolbar();
    draw();
  }

  function playFromMarker() {
    const a = state.audioEl;
    if (!a) return;
    const l = lineById(ed.selectedId);
    const start = l ? Math.max(0, l.sec - 2) : state.currentSec;
    setPlayhead(start);
    a.play().catch(() => {});
    state.isPlaying = true;
    if (typeof updatePlayIcons === "function") updatePlayIcons();
  }

  function addLine() {
    const sec = Math.round((state.currentSec || 0) * 100) / 100;
    const line = { id: ed.nextId++, sec, text: "" };
    ed.lines.push(line);
    sortLines();
    ed.selectedId = line.id;
    renderRows();
    markSelectedRow(true);
    commit();
    rowById(line.id)?.querySelector(".tl-text")?.focus();
    draw();
  }

  // ---------------------------------------------------------------------------
  // Сохранение
  // ---------------------------------------------------------------------------
  async function save() {
    if (!ed.track) return;
    const content = buildLrc(ed.lines);
    if (!content) {
      showToast("Нечего сохранять: нет ни одной строки с текстом", "error");
      return;
    }
    const ok = await persistLrcContent(content);
    if (ok) {
      ed.original = buildLrc(ed.lines);
      ed.synthetic = false;
      renderRows();
      markSelectedRow(false);
      updateToolbar();
    }
  }

  function revert() {
    if (!isDirty() && ed.histIdx === 0) return;
    if (isDirty() && !confirm("Отменить все несохранённые изменения этого трека?")) return;
    if (ed.synthetic) {
      // вернуть исходный (синтетический) вид: первая запись истории
      ed.lines = JSON.parse(ed.history[0]);
    } else {
      ed.lines = parseLRC(ed.original).map((l) => ({ id: ed.nextId++, sec: l.sec, text: l.text }));
    }
    ed.selectedId = null;
    resetHistory();
    renderRows();
    draw();
  }

  function updateToolbar() {
    const dirty = isDirty();
    document.getElementById("tl-save")?.toggleAttribute("disabled", !dirty);
    document.getElementById("tl-revert")?.toggleAttribute("disabled", !dirty && ed.histIdx === 0);
    document.getElementById("tl-undo")?.toggleAttribute("disabled", ed.histIdx <= 0);
    document.getElementById("tl-redo")?.toggleAttribute("disabled", ed.histIdx >= ed.history.length - 1);
    document.getElementById("tl-dirty")?.classList.toggle("hidden", !dirty);
    document.getElementById("tl-play-marker")?.toggleAttribute("disabled", !ed.track);
  }

  // ---------------------------------------------------------------------------
  // Волна: геометрия
  // ---------------------------------------------------------------------------
  function xOf(sec) {
    return (sec - ed.viewStart) * ed.pxPerSec;
  }

  function secAt(x) {
    return ed.viewStart + x / ed.pxPerSec;
  }

  function minPx() {
    const total = duration();
    return total > 0 ? Math.max(2, (ed.cssW || 800) / total) : 2;
  }

  function setZoom(px, anchorX) {
    const old = ed.pxPerSec;
    const anchor = anchorX == null ? (ed.cssW || 800) / 2 : anchorX;
    const anchorSec = secAt(anchor);
    ed.pxPerSec = clamp(px, minPx(), 800);
    ed.viewStart = clampView(anchorSec - anchor / ed.pxPerSec);
    if (old !== ed.pxPerSec) syncZoomSlider();
    draw();
  }

  function zoomToSlider() {
    const lo = Math.log(minPx());
    const hi = Math.log(800);
    return hi > lo ? (Math.log(ed.pxPerSec) - lo) / (hi - lo) : 0;
  }

  function syncZoomSlider() {
    const s = document.getElementById("tl-zoom");
    if (s) s.value = String(zoomToSlider());
  }

  function onZoomSlider(e) {
    const lo = Math.log(minPx());
    const hi = Math.log(800);
    setZoom(Math.exp(lo + parseFloat(e.target.value) * (hi - lo)));
  }

  // ---------------------------------------------------------------------------
  // Волна: рисование
  // ---------------------------------------------------------------------------
  function readColors() {
    const now = performance.now();
    if (ed.colors && now - ed.colorsAt < 1000) return ed.colors;
    const cs = getComputedStyle(document.documentElement);
    const v = (n, d) => (cs.getPropertyValue(n).trim() || d);
    ed.colors = {
      bg: v("--bg-surface-secondary", "#F1F4FA"),
      wave: v("--poisq-slate", "#666B82"),
      grid: v("--border-subtle", "#E3E7F0"),
      text: v("--text-secondary", "#666B82"),
      textStrong: v("--text-primary", "#191B2B"),
      accent: v("--poisq-blue", "#2563EB"),
      violet: v("--poisq-violet", "#7C3AED"),
      surface: v("--bg-surface", "#FFFFFF"),
      red: v("--color-red", "#EF4444"),
    };
    ed.colorsAt = now;
    return ed.colors;
  }

  function resizeCanvas() {
    const c = ed.canvas;
    if (!c) return;
    const wrap = c.parentElement;
    const w = Math.max(200, wrap.clientWidth);
    const h = Math.max(140, wrap.clientHeight);
    const dpr = window.devicePixelRatio || 1;
    if (c.width !== Math.round(w * dpr) || c.height !== Math.round(h * dpr)) {
      c.width = Math.round(w * dpr);
      c.height = Math.round(h * dpr);
    }
    c.style.width = w + "px";
    c.style.height = h + "px";
    ed.cssW = w;
    ed.cssH = h;
    ed.ctx = c.getContext("2d");
    ed.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  }

  function niceStep(pxPerSec) {
    const steps = [0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 15, 30, 60, 120];
    return steps.find((s) => s * pxPerSec >= 80) || 120;
  }

  function draw() {
    if (!ed.visible || !ed.canvas) return;
    resizeCanvas();
    const ctx = ed.ctx;
    const W = ed.cssW;
    const H = ed.cssH;
    const col = readColors();
    ctx.clearRect(0, 0, W, H);
    ctx.fillStyle = col.bg;
    ctx.fillRect(0, 0, W, H);

    ed.viewStart = clampView(ed.viewStart);
    const total = duration();
    const midY = RULER_H + (H - RULER_H) / 2;
    const amp = (H - RULER_H) / 2 - 6;

    // Линейка времени
    ctx.font = "11px ui-monospace, Menlo, monospace";
    ctx.textBaseline = "middle";
    const step = niceStep(ed.pxPerSec);
    const firstTick = Math.floor(ed.viewStart / step) * step;
    ctx.strokeStyle = col.grid;
    ctx.fillStyle = col.text;
    ctx.lineWidth = 1;
    for (let t = firstTick; t <= ed.viewStart + W / ed.pxPerSec + step; t += step) {
      const x = Math.round(xOf(t)) + 0.5;
      ctx.beginPath();
      ctx.moveTo(x, RULER_H - 6);
      ctx.lineTo(x, H);
      ctx.stroke();
      ctx.fillText(fmtTime(t).replace(/\.00$/, ""), x + 4, RULER_H / 2 - 2);
    }

    // Волна
    if (ed.waveState === "ready" && ed.wave) {
      const { rate, min, max } = ed.wave;
      ctx.strokeStyle = col.wave;
      ctx.globalAlpha = 0.85;
      ctx.beginPath();
      for (let x = 0; x < W; x++) {
        const t0 = secAt(x);
        const t1 = secAt(x + 1);
        let i0 = Math.floor(t0 * rate);
        let i1 = Math.max(i0, Math.ceil(t1 * rate) - 1);
        if (i1 < 0 || i0 >= min.length) continue;
        i0 = Math.max(0, i0);
        i1 = Math.min(min.length - 1, i1);
        let lo = 127;
        let hi = -128;
        for (let i = i0; i <= i1; i++) {
          if (min[i] < lo) lo = min[i];
          if (max[i] > hi) hi = max[i];
        }
        const y0 = midY - (hi / 127) * amp;
        const y1 = midY - (lo / 127) * amp;
        ctx.moveTo(x + 0.5, y0);
        ctx.lineTo(x + 0.5, Math.max(y1, y0 + 1));
      }
      ctx.stroke();
      ctx.globalAlpha = 1;
    } else {
      ctx.fillStyle = col.text;
      ctx.textAlign = "center";
      ctx.font = "13px system-ui, sans-serif";
      const msg =
        ed.waveState === "loading"
          ? "Строим волну трека…"
          : ed.waveState === "error"
          ? `Волна недоступна (${ed.waveError}). Метки можно двигать, но без волны.`
          : "";
      if (msg) ctx.fillText(msg, W / 2, midY);
      ctx.textAlign = "left";
      ctx.strokeStyle = col.grid;
      ctx.beginPath();
      ctx.moveTo(0, midY + 0.5);
      ctx.lineTo(W, midY + 0.5);
      ctx.stroke();
    }

    // Метки строк
    ctx.font = "11px system-ui, sans-serif";
    ctx.textBaseline = "middle";
    ed.lines.forEach((l, i) => {
      const x = xOf(l.sec);
      if (x < -200 || x > W + 4) return;
      const selected = l.id === ed.selectedId;
      if (selected) return; // выделенную рисуем последней, поверх остальных
      ctx.strokeStyle = col.violet;
      ctx.globalAlpha = 0.45;
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(Math.round(x) + 0.5, RULER_H);
      ctx.lineTo(Math.round(x) + 0.5, H);
      ctx.stroke();
      ctx.globalAlpha = 1;
      // подпись: номер и начало текста, если хватает места до следующей метки
      const next = ed.lines[i + 1];
      const room = next ? xOf(next.sec) - x - 6 : W - x;
      if (room > 26) {
        ctx.fillStyle = col.violet;
        ctx.globalAlpha = 0.85;
        let label = `${i + 1} ${l.text}`;
        while (label.length > 3 && ctx.measureText(label).width > room) label = label.slice(0, -2);
        ctx.fillText(label, x + 4, H - 10);
        ctx.globalAlpha = 1;
      }
    });

    const sel = lineById(ed.selectedId);
    if (sel) {
      const x = Math.round(xOf(sel.sec)) + 0.5;
      ctx.strokeStyle = col.accent;
      ctx.lineWidth = 2;
      ctx.beginPath();
      ctx.moveTo(x, RULER_H - 4);
      ctx.lineTo(x, H);
      ctx.stroke();
      // флажок
      const idx = ed.lines.indexOf(sel) + 1;
      const label = `${idx}  ${fmtTime(sel.sec)}`;
      ctx.font = "600 11px ui-monospace, Menlo, monospace";
      const fw = ctx.measureText(label).width + 14;
      const fx = x + 1;
      ctx.fillStyle = col.accent;
      ctx.beginPath();
      ctx.roundRect(fx, 2, fw, 20, [3, 6, 6, 3]);
      ctx.fill();
      ctx.fillStyle = "#fff";
      ctx.fillText(label, fx + 7, 12.5);
      // ручка
      ctx.fillStyle = col.accent;
      ctx.beginPath();
      ctx.arc(x, midY, 5, 0, Math.PI * 2);
      ctx.fill();
      ctx.strokeStyle = col.surface;
      ctx.lineWidth = 2;
      ctx.stroke();
      // текст выбранной строки
      ctx.font = "600 12px system-ui, sans-serif";
      ctx.fillStyle = col.textStrong;
      let t = sel.text;
      const room = W - x - 12;
      while (t.length > 3 && ctx.measureText(t).width > room) t = t.slice(0, -2);
      ctx.fillText(t, x + 8, H - 10);
    }

    // Курсор воспроизведения
    const px = xOf(state.currentSec || 0);
    if (px >= -2 && px <= W + 2) {
      ctx.strokeStyle = col.red;
      ctx.lineWidth = 1.5;
      ctx.beginPath();
      ctx.moveTo(Math.round(px) + 0.5, 0);
      ctx.lineTo(Math.round(px) + 0.5, H);
      ctx.stroke();
      ctx.fillStyle = col.red;
      ctx.beginPath();
      ctx.moveTo(px - 5, 0);
      ctx.lineTo(px + 5, 0);
      ctx.lineTo(px, 7);
      ctx.closePath();
      ctx.fill();
    }

    const label = document.getElementById("tl-time-label");
    if (label) label.textContent = `${fmtTime(state.currentSec || 0)} / ${fmtTime(total)}`;
  }

  // ---------------------------------------------------------------------------
  // Волна: мышь, колесо, клавиатура
  // ---------------------------------------------------------------------------
  function pointerX(e) {
    const r = ed.canvas.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  }

  function hitMarker(x, y) {
    // сначала выделенная (приоритет), затем ближайшая в пределах HIT_PX
    const sel = lineById(ed.selectedId);
    if (sel && Math.abs(xOf(sel.sec) - x) <= HIT_PX) return sel;
    let best = null;
    let bestD = HIT_PX + 1;
    for (const l of ed.lines) {
      const d = Math.abs(xOf(l.sec) - x);
      if (d < bestD) {
        best = l;
        bestD = d;
      }
    }
    return best;
  }

  function onPointerDown(e) {
    if (e.button !== 0) return;
    // фокус из поля строки уходит, чтобы стрелки двигали метку, а не курсор в тексте
    const ae = document.activeElement;
    if (ae && ae !== document.body && ae.closest && ae.closest("#tl-rows")) ae.blur();
    const { x, y } = pointerX(e);
    const hit = hitMarker(x, y);
    if (hit) {
      if (hit.id !== ed.selectedId) select(hit.id, { seek: true });
      ed.drag = { id: hit.id, moved: false, startSec: hit.sec };
      ed.canvas.setPointerCapture(e.pointerId);
      e.preventDefault();
      return;
    }
    // пустое место: снять выделение и поставить курсор
    deselect();
    setPlayhead(secAt(x));
    draw();
  }

  function onPointerMove(e) {
    const { x, y } = pointerX(e);
    if (ed.drag) {
      const l = lineById(ed.drag.id);
      if (!l) return;
      const idx = ed.lines.indexOf(l);
      const prev = ed.lines[idx - 1];
      const next = ed.lines[idx + 1];
      const lo = prev ? prev.sec + MIN_GAP : 0;
      const hi = next ? next.sec - MIN_GAP : duration() || secAt(x);
      const sec = Math.round(clamp(secAt(x), lo, hi) * 100) / 100;
      if (sec !== l.sec) {
        l.sec = sec;
        ed.drag.moved = true;
        syncRowTime(l.id);
        updateToolbar();
        draw();
      }
      // автопрокрутка у краёв
      if (x < 20) ed.viewStart = clampView(ed.viewStart - 10 / ed.pxPerSec * 3);
      if (x > ed.cssW - 20) ed.viewStart = clampView(ed.viewStart + 10 / ed.pxPerSec * 3);
      return;
    }
    const over = hitMarker(x, y);
    const cur = over ? "ew-resize" : "default";
    if (cur !== ed.hoverCursor) {
      ed.hoverCursor = cur;
      ed.canvas.style.cursor = cur;
    }
  }

  function onPointerUp(e) {
    if (!ed.drag) return;
    const moved = ed.drag.moved;
    const l = lineById(ed.drag.id);
    ed.drag = null;
    try {
      ed.canvas.releasePointerCapture(e.pointerId);
    } catch (_) {
      /* курсор уже отпущен */
    }
    if (moved) {
      commit();
      if (l) setPlayhead(l.sec);
    }
    draw();
  }

  function onWheel(e) {
    e.preventDefault();
    const { x } = pointerX(e);
    if (e.ctrlKey || e.metaKey) {
      // мягкий шаг: один «щелчок» колеса ≈ ×1.5, жест щипка на трекпаде — плавно
      setZoom(ed.pxPerSec * clamp(Math.exp(-e.deltaY * 0.004), 0.7, 1.45), x);
    } else {
      const d = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY;
      ed.viewStart = clampView(ed.viewStart + d / ed.pxPerSec);
      draw();
    }
  }

  function nudgeSelected(delta) {
    const l = lineById(ed.selectedId);
    if (!l) return false;
    const idx = ed.lines.indexOf(l);
    const prev = ed.lines[idx - 1];
    const next = ed.lines[idx + 1];
    const lo = prev ? prev.sec + MIN_GAP : 0;
    const hi = next ? next.sec - MIN_GAP : duration() || l.sec + delta;
    const sec = Math.round(clamp(l.sec + delta, lo, hi) * 100) / 100;
    if (sec === l.sec) return true;
    l.sec = sec;
    syncRowTime(l.id);
    setPlayhead(sec);
    if (xOf(sec) < 20 || xOf(sec) > ed.cssW - 20) centerOn(sec);
    updateToolbar();
    draw();
    return true;
  }

  let nudgeTimer = null;

  function onKeydown(e) {
    if (!ed.visible) return;
    const tag = (e.target && e.target.tagName) || "";
    const typing = tag === "INPUT" || tag === "TEXTAREA";
    if ((e.ctrlKey || e.metaKey) && !typing && e.key.toLowerCase() === "z") {
      e.preventDefault();
      if (e.shiftKey) redo();
      else undo();
      return;
    }
    if (typing) {
      // Alt+стрелки в поле строки двигают выбранную метку (обычные стрелки остаются для текста)
      if (e.altKey && (e.key === "ArrowLeft" || e.key === "ArrowRight") && ed.selectedId != null) {
        e.preventDefault();
        nudgeSelected((e.shiftKey ? 0.01 : 0.1) * (e.key === "ArrowLeft" ? -1 : 1));
        clearTimeout(nudgeTimer);
        nudgeTimer = setTimeout(commit, 400);
      }
      return;
    }
    if (e.key === "Escape") {
      deselect();
    } else if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      if (ed.selectedId == null) return;
      e.preventDefault();
      const step = (e.shiftKey ? 0.01 : 0.1) * (e.key === "ArrowLeft" ? -1 : 1);
      nudgeSelected(step);
      clearTimeout(nudgeTimer);
      nudgeTimer = setTimeout(commit, 400); // серия нажатий — одна запись в истории
    }
  }

  // ---------------------------------------------------------------------------
  // Показ/скрытие, цикл анимации курсора
  // ---------------------------------------------------------------------------
  function loop() {
    if (!ed.visible) {
      ed.raf = 0;
      return;
    }
    const a = state.audioEl;
    if (a && state.isPlaying && !a.paused) {
      state.currentSec = a.currentTime;
      const x = xOf(state.currentSec);
      // подкрутка окна, когда курсор подходит к правому краю
      if (!ed.drag && (x > ed.cssW - 40 || x < 0)) {
        ed.viewStart = clampView(state.currentSec - 40 / ed.pxPerSec);
      }
      draw();
    }
    ed.raf = requestAnimationFrame(loop);
  }

  function show() {
    ed.visible = true;
    resizeCanvas();
    if (ed.track) startWave();
    ed.viewStart = clampView(ed.viewStart);
    syncZoomSlider();
    draw();
    if (!ed.raf) ed.raf = requestAnimationFrame(loop);
  }

  function hide() {
    ed.visible = false;
  }

  function init() {
    ed.canvas = document.getElementById("tl-canvas");
    if (!ed.canvas) return;
    ed.canvas.addEventListener("pointerdown", onPointerDown);
    ed.canvas.addEventListener("pointermove", onPointerMove);
    ed.canvas.addEventListener("pointerup", onPointerUp);
    ed.canvas.addEventListener("pointercancel", onPointerUp);
    ed.canvas.addEventListener("wheel", onWheel, { passive: false });
    const rows = rowsEl();
    rows?.addEventListener("click", onRowsClick);
    rows?.addEventListener("focusin", onRowsFocusIn);
    rows?.addEventListener("input", onRowsInput);
    rows?.addEventListener("change", onRowsChange);
    rows?.addEventListener("keydown", onRowsKeydown);
    document.getElementById("tl-add")?.addEventListener("click", addLine);
    document.getElementById("tl-undo")?.addEventListener("click", undo);
    document.getElementById("tl-redo")?.addEventListener("click", redo);
    document.getElementById("tl-save")?.addEventListener("click", save);
    document.getElementById("tl-revert")?.addEventListener("click", revert);
    document.getElementById("tl-play-marker")?.addEventListener("click", playFromMarker);
    document.getElementById("tl-zoom")?.addEventListener("input", onZoomSlider);
    document.getElementById("tl-zoom-in")?.addEventListener("click", () => setZoom(ed.pxPerSec * 1.5));
    document.getElementById("tl-zoom-out")?.addEventListener("click", () => setZoom(ed.pxPerSec / 1.5));
    document.getElementById("tl-open-raw")?.addEventListener("click", () => {
      if (typeof openRawEditor === "function") openRawEditor();
    });
    state.audioEl?.addEventListener("loadedmetadata", () => {
      if (state.currentSec > 0 && state.audioEl.currentTime === 0) state.audioEl.currentTime = state.currentSec;
      draw();
    });
    window.addEventListener("keydown", onKeydown);
    window.addEventListener("resize", () => draw());
    window.addEventListener("beforeunload", (e) => {
      if (isDirty()) {
        e.preventDefault();
        e.returnValue = "";
      }
    });
    if (typeof ResizeObserver !== "undefined") {
      new ResizeObserver(() => draw()).observe(ed.canvas.parentElement);
    }
  }

  document.addEventListener("DOMContentLoaded", init);

  window.TimelineEditor = { load, show, hide, isDirty, currentLrc: () => buildLrc(ed.lines), _state: ed, _fmt: fmtTime, _parse: parseTimeInput, _build: buildLrc };
})();
