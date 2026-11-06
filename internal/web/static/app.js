"use strict";

// Archify 产物库前端：零构建 vanilla JS，原生窗口与浏览器共用同一份（design D-7）。
// 只做「读索引 → 列表 → 查看原始 HTML」；不渲染、不校验、不改写产物。
//
// 交互契约：
//   1. 列表 ⇄ 查看 ⇄ 设置 三个视图在同一文档内切换，location.hash 是视图状态的唯一来源；
//      不新开窗口、不使用对话框或遮罩层，避免弹窗打断浏览连续性。
//   2. iframe.src 仍是 /a/<id> 白名单寻址，字节由服务端原样透传；前端不解析产物内容，
//      因此「只读产物」「只认 ID」「静态资产白名单」三条红线均不受影响。
//   3. 状态由 state 单点持有，render() 是唯一渲染入口；加载 / 就绪 / 失败 / 陈旧 /
//      空库 / 无匹配 六态互斥且各自可恢复，两个横幅职责分离、互不改写样式。
//   4. 设置改动即时保存（后端只写用户配置文件），成功后再重建索引快照，
//      目录列表、关闭行为与底部状态提示共用同一份 settings 快照，不各自缓存。

const TIMEOUT_MS = 15000; // 项目约定：API 请求带超时（建议 15s）
const ID_RE = /^[0-9a-f]{12}$/;
const HASH_PREFIX = "#a/";
const SETTINGS_HASH = "#settings";
const THEME_KEY = "archify-theme";

const state = {
  index: null,
  q: "", type: "", root: "", sort: "mtime",
  status: "loading", // loading | refreshing | ready | error
  error: "",
  loading: false,
  seq: 0,
  stale: false,
  view: "list", // list | viewer | settings
  activeId: "",
  loadedId: "",
  viewerSeq: 0,
  frameReady: false,
  frameTimer: null,
  listScroll: 0,
  returnFocusId: "",
  autoCleared: [], // 索引刷新时自动失效的筛选条件，合并进同一次提示，避免被后续提示吞掉
  settings: null,
  settingsError: "",
  settingsBusy: false,
  settingsSeq: 0,
  theme: (function () { try { return localStorage.getItem(THEME_KEY) || "system"; } catch (_) { return "system"; } })(),
};

const el = (id) => document.getElementById(id);
const DELIVERY_LABEL = { current: "已交付", failed: "交付失败", unknown: "未记录" };
const SOURCE_LABEL = { flag: "启动参数 -root", discover: "自动发现 .archify/", config: "配置文件", none: "未配置" };
const ORIGIN_LABEL = { flag: "启动参数", discover: "自动发现", config: "配置", none: "临时" };
const THEME_LABEL = { system: "跟随系统", light: "浅色", dark: "深色" };

function bytes(n) {
  if (!Number.isFinite(n)) return "-";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

function when(unix) {
  if (!unix) return "";
  const d = new Date(unix * 1000);
  const pad = (x) => String(x).padStart(2, "0");
  return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) +
    " " + pad(d.getHours()) + ":" + pad(d.getMinutes());
}

// esc 同时覆盖文本与属性上下文（引号必须转义，否则标题里的双引号会截断 data-copy 属性）。
function esc(text) {
  const map = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  return String(text == null ? "" : text).replace(/[&<>"']/g, (c) => map[c]);
}

function message(err) { return String((err && err.message) || err || "未知错误"); }
function shortText(text, n) { const s = String(text); return s.length > n ? s.slice(0, n) + "…" : s; }

/* ---------- 全局轻提示：无遮罩、不抢焦点、自动消失（不是弹窗） ---------- */

let toastTimer = null;
function toast(text, kind) {
  const t = el("toast");
  t.textContent = text;
  t.className = "toast toast-" + (kind || "info");
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, 2800);
}

const flashTimers = new WeakMap();
function flash(btn, text, kind) {
  if (!btn) return;
  clearTimeout(flashTimers.get(btn));
  const origin = btn.dataset.flashOrigin || btn.textContent;
  btn.dataset.flashOrigin = origin;
  btn.textContent = text;
  btn.classList.remove("is-ok", "is-err");
  if (kind) btn.classList.add("is-" + kind);
  flashTimers.set(btn, setTimeout(() => {
    btn.textContent = origin;
    btn.classList.remove("is-ok", "is-err");
  }, 1400));
}

/* ---------- 数据读取：超时、竞态、陈旧数据标记 ---------- */

function normalizeFetchError(err) {
  if (err && err.name === "AbortError") {
    return new Error("请求超时（" + TIMEOUT_MS / 1000 + " 秒），本地服务可能正忙");
  }
  if (err instanceof TypeError) {
    return new Error("无法连接本地服务，请确认 archify-view 仍在运行");
  }
  return err;
}

async function fetchIndex(rescan) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), TIMEOUT_MS);
  try {
    const res = await fetch("/api/index" + (rescan ? "?rescan=1" : ""), { cache: "no-store", signal: ctrl.signal });
    if (!res.ok) {
      let detail = "";
      try {
        const body = await res.json();
        detail = body.detail || body.error || "";
      } catch (_) { /* 非 JSON 错误体，用状态码兜底 */ }
      throw new Error("索引读取失败（HTTP " + res.status + "）" + (detail ? "：" + detail : ""));
    }
    return await res.json();
  } catch (err) {
    throw normalizeFetchError(err);
  } finally {
    clearTimeout(timer);
  }
}

// requestJSON 用于设置端点：同一套超时与错误归一化，错误体里的 detail 优先展示给用户。
async function requestJSON(url, options) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), TIMEOUT_MS);
  try {
    const res = await fetch(url, Object.assign({ cache: "no-store" }, options || {}, { signal: ctrl.signal }));
    let body = null;
    try { body = await res.json(); } catch (_) { /* 非 JSON 用状态码兜底 */ }
    if (!res.ok) {
      const detail = body && (body.detail || body.error);
      throw new Error((detail || "请求失败") + "（HTTP " + res.status + "）");
    }
    return body;
  } catch (err) {
    throw normalizeFetchError(err);
  } finally {
    clearTimeout(timer);
  }
}

async function reload(rescan) {
  if (state.loading) return; // 防连点：重扫期间禁用按钮，避免并发请求与旧响应覆盖新响应
  state.loading = true;
  const seq = ++state.seq;
  state.status = state.index ? "refreshing" : "loading";
  render();
  try {
    const idx = await fetchIndex(rescan);
    if (seq !== state.seq) return;
    state.index = idx;
    state.error = "";
    state.stale = false;
    state.status = "ready";
    render();
    // 把「筛选被自动清除」与「重扫成功」合并为一条提示，避免后者覆盖前者、反馈被吞。
    const notices = state.autoCleared.splice(0);
    if (rescan) {
      const n = (idx.artifacts || []).length;
      toast("已重新扫描 · 共 " + n + " 件产物" +
        (idx.generated_at ? "（" + when(idx.generated_at) + "）" : "") +
        (notices.length ? " · " + notices.join("、") : ""), "ok");
    } else if (notices.length) {
      toast(notices.join("、"), "info");
    }
  } catch (err) {
    if (seq !== state.seq) return;
    state.autoCleared.length = 0;
    state.error = message(err);
    state.stale = !!state.index;
    state.status = "error";
    render();
    toast(state.stale ? "重新扫描失败，仍显示上次成功的数据" : "索引读取失败", "err");
  } finally {
    if (seq === state.seq) {
      state.loading = false;
      setBusyState();
    }
  }
}

/* ---------- 列表：匹配、排序、卡片 ---------- */

function match(a) {
  if (state.type && a.type !== state.type) return false;
  if (state.root && a.root !== state.root) return false;
  if (!state.q) return true;
  const needle = state.q.toLowerCase();
  const hay = [a.title, a.subtitle, a.slug, a.rel_path, a.repository, a.type, a.id].join(" ").toLowerCase();
  return hay.includes(needle);
}

function sorted(list) {
  const copy = list.slice();
  if (state.sort === "title") copy.sort((x, y) => (x.title || "").localeCompare(y.title || ""));
  else if (state.sort === "size") copy.sort((x, y) => y.size - x.size);
  else copy.sort((x, y) => y.mtime - x.mtime);
  return copy;
}

// 卡片与查看面板共用同一份标签渲染，避免两处状态展示不一致。
function tagsHtml(a) {
  const label = DELIVERY_LABEL[a.delivery] || a.delivery || "";
  return '<span class="tag tag-type">' + esc(a.type) + "</span>" +
    (label ? '<span class="tag tag-' + esc(a.delivery) + '">' + esc(label) + "</span>" : "") +
    (a.quality ? '<span class="tag tag-quality">' + esc(a.quality) + "</span>" : "");
}

function card(a) {
  const node = document.createElement("article");
  node.className = "card" + (a.delivery === "failed" ? " card-flag" : "");
  const deepLink = location.origin + "/a/" + a.id;
  const warnings = (a.warnings || []).filter((w) => w !== "CANDIDATE-MISSING" && w !== "SIDECAR-MISSING");

  // 事件统一委托到 grid：渲染重建卡片后无需重新绑定，也不会残留旧监听器。
  node.innerHTML =
    '<div class="card-head">' + tagsHtml(a) + "</div>" +
    '<h3 class="card-title">' + esc(a.title) + "</h3>" +
    (a.subtitle ? '<p class="card-sub">' + esc(a.subtitle) + "</p>" : "") +
    '<p class="card-path"><span class="root-chip">' + esc(a.root) + "</span>" + esc(a.rel_path) + "</p>" +
    (a.repository ? '<p class="card-repo" title="' + esc(a.repository) + '">↳ ' + esc(a.repository) + "</p>" : "") +
    '<p class="card-meta">' + esc(when(a.mtime)) + " · " + bytes(a.size) +
      (warnings.length ? ' · <span class="warn">解析提示 ' + warnings.length + "</span>" : "") +
    "</p>" +
    '<div class="card-actions">' +
      '<button class="btn btn-primary" data-action="open" data-id="' + esc(a.id) + '" title="在当前页面查看，不打开新窗口">查看</button>' +
      '<button class="btn" data-action="copy" data-copy="' + esc(deepLink) + '">复制深链</button>' +
      '<button class="btn" data-action="copy" data-copy="' + esc(a.id) + '">复制 ID</button>' +
    "</div>";
  return node;
}

/* ---------- 筛选项：只在值域变化时重建 ---------- */

let selectSignature = "";
function fillSelect(select, values, keep) {
  select.innerHTML = "";
  const head = document.createElement("option");
  head.value = "";
  head.textContent = select.dataset.head || "全部";
  select.appendChild(head);
  values.forEach((v) => {
    const opt = document.createElement("option");
    opt.value = v;
    opt.textContent = v;
    select.appendChild(opt);
  });
  select.value = keep && values.includes(keep) ? keep : "";
}

function syncFilters(idx) {
  const types = Object.keys(idx.by_type || {}).sort();
  const roots = (idx.roots || []).map((r) => r.label);
  const sig = JSON.stringify([types, roots]);
  if (sig === selectSignature) return; // 搜索输入不再重建下拉框，焦点/展开态不被打断
  selectSignature = sig;
  // 重扫后原筛选值可能已消失：同步回 state，否则「下拉显示全部」却仍按旧值过滤（状态不一致）。
  if (state.type && !types.includes(state.type)) {
    state.type = "";
    state.autoCleared.push("「类型」筛选已自动清除");
  }
  if (state.root && !roots.includes(state.root)) {
    state.root = "";
    state.autoCleared.push("「来源」筛选已自动清除");
  }
  fillSelect(el("f-type"), types, state.type);
  fillSelect(el("f-root"), roots, state.root);
}

function hasFilter() { return !!(state.q || state.type || state.root); }
function filterSummary() {
  const parts = [];
  if (state.q) parts.push("搜索「" + state.q + "」");
  if (state.type) parts.push("类型 " + state.type);
  if (state.root) parts.push("来源 " + state.root);
  return parts.join(" · ") || "无条件";
}

function clearFilters() {
  state.q = ""; state.type = ""; state.root = "";
  el("q").value = "";
  el("f-type").value = "";
  el("f-root").value = "";
  render();
  if (el("q").focus) el("q").focus();
  toast("已清除筛选条件", "info");
}

/* ---------- 主题：首帧已写入，这里只负责运行时切换与按钮状态 ---------- */

function resolveTheme(pref) {
  if (pref === "light" || pref === "dark") return pref;
  return (window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches) ? "light" : "dark";
}

function applyTheme(pref) {
  const valid = THEME_LABEL[pref] ? pref : "system";
  state.theme = valid;
  if (document.documentElement && document.documentElement.setAttribute) {
    document.documentElement.setAttribute("data-theme", resolveTheme(valid));
  }
  try { localStorage.setItem(THEME_KEY, valid); } catch (_) { /* 隐私模式忽略 */ }
  renderThemeButtons();
}

function renderThemeButtons() {
  ["system", "light", "dark"].forEach((v) => {
    const b = el("theme-" + v);
    if (b && b.setAttribute) b.setAttribute("aria-pressed", state.theme === v ? "true" : "false");
  });
  const top = el("btn-theme");
  if (top) top.title = "切换主题（当前：" + (THEME_LABEL[state.theme] || "跟随系统") + "）";
}

function cycleTheme() {
  const order = ["system", "light", "dark"];
  const next = order[(order.indexOf(state.theme) + 1) % order.length] || "system";
  applyTheme(next);
  toast("主题：" + THEME_LABEL[next], "info");
}

/* ---------- 设置：单一快照驱动目录列表 / 关闭行为 / 底部提示 ---------- */

function renderTrayHint() {
  const hint = el("tray-hint");
  if (!hint) return;
  const action = (state.settings && state.settings.close_action) || "tray";
  const map = {
    tray: "关闭窗口 → 最小化到托盘，后台继续运行",
    ask: "关闭窗口 → 每次询问托盘 / 退出",
    quit: "关闭窗口 → 直接退出",
  };
  hint.textContent = map[action] || map.tray;
}

function renderSettings() {
  const s = state.settings;
  if (!s) return;
  el("settings-source").textContent = SOURCE_LABEL[s.source] || s.source || "—";
  el("settings-addr").textContent = s.listen_addr || "—";
  el("settings-config-path").textContent = s.config_path || "—";
  el("btn-copy-config").dataset.copy = s.config_path || "";

  const err = el("settings-error");
  err.hidden = !state.settingsError;
  err.textContent = state.settingsError || "";

  const note = el("settings-priority-note");
  note.hidden = !s.managed;
  note.textContent = s.managed
    ? "当前会话由启动参数或自动发现接管。这里的改动已立即生效并写入配置文件；下次启动时，工作目录里若仍存在 .archify/ 或再次传入 -root，会优先于配置。"
    : "";

  ["tray", "ask", "quit"].forEach((v) => {
    const b = el("close-" + v);
    if (b && b.setAttribute) b.setAttribute("aria-pressed", s.close_action === v ? "true" : "false");
  });

  const list = el("roots-list");
  list.innerHTML = "";
  if (!s.roots || s.roots.length === 0) {
    const empty = document.createElement("div");
    empty.className = "roots-empty";
    empty.textContent = "尚未登记目录，索引会是空的。把 .archify 目录路径粘贴到下面即可添加。";
    list.appendChild(empty);
  } else {
    (s.roots || []).forEach((r) => {
      const row = document.createElement("div");
      row.className = "root-row";
      const badge = document.createElement("span");
      badge.className = "root-badge";
      badge.textContent = ORIGIN_LABEL[r.origin] || r.origin || "配置";
      const info = document.createElement("div");
      info.className = "root-info";
      const label = document.createElement("strong");
      label.className = "root-label";
      label.textContent = r.label || "（未命名）";
      const p = document.createElement("code");
      p.className = "root-path";
      p.textContent = r.path || "";
      info.appendChild(label);
      info.appendChild(p);
      const btn = document.createElement("button");
      btn.className = "btn btn-quiet btn-sm";
      btn.dataset.remove = r.path || "";
      btn.disabled = state.settingsBusy;
      btn.textContent = "移除";
      row.appendChild(badge);
      row.appendChild(info);
      row.appendChild(btn);
      list.appendChild(row);
    });
  }
  renderThemeButtons();
}

async function loadSettings(silent) {
  const seq = ++state.settingsSeq;
  try {
    const view = await requestJSON("/api/settings");
    if (seq !== state.settingsSeq) return;
    state.settings = view;
    state.settingsError = "";
  } catch (err) {
    if (seq !== state.settingsSeq) return;
    state.settingsError = silent ? "" : message(err);
    if (!silent) toast("设置读取失败：" + message(err), "err");
  }
  renderSettings();
  renderTrayHint();
}

function setSettingsBusy(busy) {
  const add = el("btn-root-add");
  add.disabled = busy;
  add.textContent = busy ? "保存中…" : "添加目录";
  ["tray", "ask", "quit"].forEach((v) => {
    const b = el("close-" + v);
    if (b) b.disabled = busy;
  });
}

async function postSettings(url, body, okText, onSuccess) {
  if (state.settingsBusy) return;
  state.settingsBusy = true;
  const seq = ++state.settingsSeq;
  setSettingsBusy(true);
  try {
    const view = await requestJSON(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (seq !== state.settingsSeq) return;
    state.settings = view;
    state.settingsError = "";
    renderSettings();
    renderTrayHint();
    toast(okText, "ok");
    if (onSuccess) onSuccess();
    // 目录变更后立即重建索引，用户不必再点一次「重新扫描」。
    if (url.indexOf("/roots") >= 0) reload(true);
  } catch (err) {
    if (seq !== state.settingsSeq) return;
    state.settingsError = message(err);
    renderSettings();
    toast("设置保存失败：" + state.settingsError, "err");
  } finally {
    if (seq === state.settingsSeq) {
      state.settingsBusy = false;
      setSettingsBusy(false);
    }
  }
}

function addRoot() {
  const input = el("root-path");
  const path = String((input && input.value) || "").trim();
  if (!path) {
    toast("请先粘贴一个目录路径", "err");
    if (input && input.focus) input.focus();
    return;
  }
  postSettings("/api/settings/roots", { action: "add", path }, "目录已添加并重新扫描", () => {
    if (input) input.value = "";
  });
}

function removeRoot(path) {
  if (!path) return;
  postSettings("/api/settings/roots", { action: "remove", path }, "目录已移除并重新扫描");
}

function setCloseAction(value) {
  postSettings("/api/settings/close-action", { value: value }, "关闭行为已更新");
}

/* ---------- 状态展示 ---------- */

function metaText(idx) {
  if (state.status === "loading") return "正在读取索引…";
  if (state.status === "refreshing") return "正在重新扫描产物根…";
  if (state.status === "error") return idx ? "索引读取失败 · 正在显示上次成功的数据" : "索引读取失败";
  if (!idx) return "正在读取索引…";
  return "来源 " + (idx.root_source || "none") +
    " · 根 " + ((idx.roots || []).length) +
    " · 产物 " + ((idx.artifacts || []).length) +
    (idx.parse_warnings ? " · " + idx.parse_warnings + " 项未能完全解析" : "");
}

function setBusyState() {
  const busy = state.loading;
  const btn = el("btn-refresh");
  btn.disabled = busy;
  btn.setAttribute("aria-busy", busy ? "true" : "false");
  el("refresh-label").textContent = busy ? "扫描中…" : "重新扫描";
  el("refresh-spinner").hidden = !busy;
  el("btn-retry").disabled = busy;
  el("btn-empty-rescan").disabled = busy;
}

function renderBanners() {
  // 旧实现把 root-errors 的 className 改成 banner-error 后从不恢复；错误恢复后仍显示红底。
  // 现在两个横幅各管一件事，className 在此恒定重置，杜绝串味。
  const status = el("status-banner");
  status.hidden = state.status !== "error";
  el("status-text").textContent = state.error;

  const errs = ((state.index && state.index.roots) || [])
    .filter((r) => r.error).map((r) => r.label + "：" + r.error);
  const roots = el("root-errors");
  roots.className = "banner banner-warn";
  roots.hidden = errs.length === 0;
  roots.textContent = errs.length
    ? "部分产物根未能读取（其余产物不受影响）：" + errs.join("  |  ")
    : "";

  const fresh = el("freshness");
  fresh.hidden = !state.stale;
  fresh.textContent = state.stale ? "显示的是上次成功读取的数据" : "";
}

function render() {
  const idx = state.index;
  el("meta").textContent = metaText(idx);
  setBusyState();
  renderBanners();
  renderTrayHint();
  el("grid").setAttribute("aria-busy", state.status === "loading" ? "true" : "false");

  const pill = el("count-pill");
  pill.hidden = !idx;
  if (idx) el("count-text").textContent = ((idx.artifacts || []).length) + " 件产物";

  if (!idx) {
    // 首屏尚无快照：骨架表示「正在加载」，错误时则由 status-banner 说明原因。
    el("grid").hidden = true;
    el("grid").innerHTML = "";
    el("skeleton").hidden = state.status === "error";
    el("empty").hidden = true;
    el("no-match").hidden = true;
    el("stats").textContent = "";
    if (state.view === "viewer") ensureViewerLoaded();
    return;
  }

  syncFilters(idx);
  const arts = idx.artifacts || [];
  const visible = sorted(arts.filter(match));
  const grid = el("grid");
  grid.innerHTML = "";
  const frag = document.createDocumentFragment();
  visible.forEach((a) => frag.appendChild(card(a)));
  grid.appendChild(frag);

  const noItems = arts.length === 0;
  const noMatch = !noItems && visible.length === 0;
  el("skeleton").hidden = true;
  el("empty").hidden = !noItems;
  el("no-match").hidden = !noMatch;
  grid.hidden = visible.length === 0; // 依赖 [hidden] 全局规则，旧 CSS 的 display:grid 会覆盖它
  if (noMatch) el("no-match-detail").textContent = "当前条件：" + filterSummary();
  el("btn-clear").hidden = !hasFilter();
  el("stats").textContent = hasFilter()
    ? "显示 " + visible.length + " / 共 " + arts.length + " 件 · 已筛选"
    : "共 " + arts.length + " 件";
  el("generated").textContent = idx.generated_at ? "索引生成于 " + when(idx.generated_at) : "";

  if (state.view === "viewer") {
    renderViewerMeta();
    ensureViewerLoaded();
  }
}

/* ---------- 查看视图：应用内切换，不弹窗口 ---------- */

function findArtifact(id) {
  const list = (state.index && state.index.artifacts) || [];
  for (const a of list) if (a.id === id) return a;
  return null;
}

function renderViewerMeta() {
  const art = findArtifact(state.activeId);
  const extra = el("btn-error-newtab");
  if (!art) {
    el("viewer-tags").innerHTML = "";
    el("viewer-name").textContent = state.status === "ready" ? "产物不在当前索引中" : "正在读取索引…";
    el("viewer-path").textContent = state.activeId ? "ID " + state.activeId : "";
    el("btn-copy-link").disabled = true;
    el("btn-copy-id").disabled = false;
    el("btn-copy-id").dataset.copy = state.activeId;
    el("btn-newtab").hidden = true;
    extra.hidden = true;
    return;
  }
  el("viewer-tags").innerHTML = tagsHtml(art);
  el("viewer-name").textContent = art.title || art.slug || art.id;
  el("viewer-path").textContent = art.root + " / " + art.rel_path +
    " · " + bytes(art.size) + " · " + when(art.mtime);
  el("btn-copy-link").dataset.copy = location.origin + "/a/" + art.id;
  el("btn-copy-id").dataset.copy = art.id;
  el("btn-copy-link").disabled = false;
  el("btn-copy-id").disabled = false;
  el("btn-newtab").href = "/a/" + art.id;
  el("btn-newtab").hidden = false;
  extra.href = "/a/" + art.id;
  extra.hidden = false;
}

function showViewerLoading(text) {
  clearTimeout(state.frameTimer);
  el("viewer-error").hidden = true;
  el("frame").hidden = true;
  el("viewer-status-text").textContent = text;
  el("viewer-status").hidden = false;
}

function showViewerLoaded() {
  state.frameReady = true;
  clearTimeout(state.frameTimer);
  el("viewer-status").hidden = true;
  el("viewer-error").hidden = true;
  el("frame").hidden = false;
}

function showViewerError(text) {
  clearTimeout(state.frameTimer);
  el("viewer-status").hidden = true;
  el("frame").hidden = true;
  el("viewer-error-text").textContent = text;
  el("viewer-error").hidden = false;
}

function ensureViewerLoaded() {
  if (state.view !== "viewer" || state.loadedId === state.activeId) return;
  const art = findArtifact(state.activeId);
  if (!art) {
    if (state.status === "error" && !state.index) {
      showViewerError("索引尚未就绪，无法查看产物。请重试读取索引。");
    } else if (state.status === "ready") {
      showViewerError("产物 " + state.activeId + " 不在当前索引中，可能已被移动或删除；可先重新扫描。");
    } else {
      showViewerLoading("正在读取索引…");
    }
    return;
  }
  loadViewer(art);
}

async function loadViewer(art) {
  const seq = ++state.viewerSeq;
  const url = "/a/" + art.id;
  showViewerLoading("正在载入产物…");
  let res;
  try {
    // iframe 的 onerror 不可靠，且 404 也会触发 onload；先用 HEAD 探测拿到明确状态再挂 src。
    res = await fetch(url, { method: "HEAD", cache: "no-store" });
  } catch (err) {
    if (seq !== state.viewerSeq) return;
    showViewerError("无法连接本地服务，请确认 archify-view 仍在运行。" + message(err));
    return;
  }
  if (seq !== state.viewerSeq || state.activeId !== art.id) return;
  if (!res.ok) {
    showViewerError("产物读取失败（HTTP " + res.status + "）：文件可能已被移动或删除，或已不在当前索引中。");
    return;
  }
  state.loadedId = art.id;
  state.frameReady = false;
  const frame = el("frame");
  frame.onload = () => {
    if (state.view === "viewer" && state.activeId === art.id) showViewerLoaded();
  };
  frame.src = url;
  clearTimeout(state.frameTimer);
  state.frameTimer = setTimeout(() => {
    if (!state.frameReady && state.activeId === art.id && state.view === "viewer") {
      showViewerError("产物载入超时，可重试或在新标签打开。");
    }
  }, TIMEOUT_MS);
}

function showViewer(id) {
  const changed = state.view !== "viewer" || state.activeId !== id;
  if (state.view !== "viewer") state.listScroll = window.scrollY || 0;
  state.view = "viewer";
  state.activeId = id;
  if (changed) { state.loadedId = ""; state.frameReady = false; }
  el("view-list").hidden = true;
  el("view-settings").hidden = true;
  el("view-viewer").hidden = false;
  renderViewerMeta();
  ensureViewerLoaded();
  if (window.scrollTo) window.scrollTo(0, 0);
  if (changed && el("btn-back").focus) el("btn-back").focus();
}

// resetViewer 释放 iframe 与查看态资源；返回列表与进入设置都走它，避免后台脚本继续运行。
function resetViewer() {
  state.activeId = "";
  state.loadedId = "";
  state.frameReady = false;
  state.viewerSeq++;
  clearTimeout(state.frameTimer);
  const frame = el("frame");
  frame.onload = null;
  frame.src = "about:blank";
  frame.hidden = true;
  el("viewer-status").hidden = true;
  el("viewer-error").hidden = true;
  el("view-viewer").hidden = true;
}

function restoreFocus() {
  const id = state.returnFocusId;
  const grid = el("grid");
  const btn = id && grid.querySelector
    ? grid.querySelector('[data-action="open"][data-id="' + id + '"]')
    : null;
  if (btn && btn.focus) btn.focus();
  else if (el("q").focus) el("q").focus();
}

function showList() {
  const wasOther = state.view === "viewer" || state.view === "settings";
  state.view = "list";
  resetViewer();
  el("view-settings").hidden = true;
  el("view-list").hidden = false;
  if (wasOther) {
    const y = state.listScroll || 0;
    const restore = () => { if (window.scrollTo) window.scrollTo(0, y); };
    if (window.requestAnimationFrame) window.requestAnimationFrame(restore);
    else restore();
    restoreFocus();
  }
}

function showSettings() {
  if (state.view !== "settings") state.listScroll = window.scrollY || 0;
  state.view = "settings";
  resetViewer();
  el("view-list").hidden = true;
  el("view-settings").hidden = false;
  if (window.scrollTo) window.scrollTo(0, 0);
  loadSettings(false);
  if (el("btn-settings-back").focus) el("btn-settings-back").focus();
}

/* ---------- hash 路由：三视图共用唯一入口 ---------- */

function parseHash() {
  const h = location.hash || "";
  if (!h.startsWith(HASH_PREFIX)) return "";
  const id = h.slice(HASH_PREFIX.length).split(/[?&#]/)[0];
  return ID_RE.test(id) ? id : "";
}

function parseRoute() {
  const h = location.hash || "";
  if (h === SETTINGS_HASH || h.startsWith(SETTINGS_HASH + "?")) return { kind: "settings" };
  const id = parseHash();
  return id ? { kind: "viewer", id: id } : { kind: "list" };
}

// hash 变化必然触发 hashchange；不另听 popstate，避免同一次导航被派发两遍。
function syncViewFromHash() {
  const r = parseRoute();
  if (r.kind === "settings") showSettings();
  else if (r.kind === "viewer") showViewer(r.id);
  else showList();
}

function openViewer(id) {
  if (!ID_RE.test(id)) { toast("无效的产物 ID", "err"); return; }
  state.returnFocusId = id;
  const next = HASH_PREFIX + id;
  if (location.hash !== next) location.hash = next;
  syncViewFromHash(); // 立即生效，不等 hashchange 事件（重复点同一张卡片也能正常响应）
}

function backToList() {
  if (location.hash) location.hash = "";
  showList(); // 直接切回，不依赖 hashchange 的异步派发；随后的 hashchange 会幂等复调一次
}

function openSettings() {
  if (location.hash !== SETTINGS_HASH) location.hash = SETTINGS_HASH;
  showSettings();
}

function backFromSettings() {
  if (location.hash) location.hash = "";
  showList();
}

/* ---------- 复制：主路径 + 兼容降级 ---------- */

async function copyText(text) {
  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch (_) { /* 权限或非安全上下文被拒，走 execCommand 降级 */ }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    if (ta.select) ta.select();
    const ok = document.execCommand && document.execCommand("copy");
    if (ta.remove) ta.remove();
    return !!ok;
  } catch (_) { return false; }
}

async function doCopy(text, btn) {
  if (!text) return;
  const ok = await copyText(text);
  if (ok) {
    flash(btn, "已复制", "ok");
    toast("已复制：" + shortText(text, 48), "ok");
  } else {
    flash(btn, "复制失败", "err");
    toast("复制失败，可手动复制：" + text, "err");
  }
}

/* ---------- 事件绑定 ---------- */

function targetFor(e, selector) {
  const t = e && e.target;
  return t && t.closest ? t.closest(selector) : null;
}

function onGridClick(e) {
  const t = targetFor(e, "[data-action]");
  if (!t) return;
  if (t.dataset.action === "open") openViewer(t.dataset.id);
  else if (t.dataset.action === "copy") doCopy(t.dataset.copy, t);
}

function onViewerClick(e) {
  const t = targetFor(e, "[data-action]");
  if (!t) return;
  if (t.dataset.action === "back") backToList();
  else if (t.dataset.action === "copy") doCopy(t.dataset.copy, t);
}

function onSettingsClick(e) {
  const t = targetFor(e, "[data-remove],[data-close-action],[data-theme],[data-action]");
  if (!t) return;
  if (t.dataset.remove !== undefined) removeRoot(t.dataset.remove);
  else if (t.dataset.closeAction) setCloseAction(t.dataset.closeAction);
  else if (t.dataset.theme) applyTheme(t.dataset.theme);
  else if (t.dataset.action === "copy") {
    doCopy(t.dataset.copy || (state.settings && state.settings.config_path) || "", t);
  }
}

function onKeyDown(e) {
  const t = e.target || {};
  const tag = String(t.tagName || "").toLowerCase();
  const editing = tag === "input" || tag === "textarea" || tag === "select" || !!t.isContentEditable;

  if ((e.ctrlKey || e.metaKey) && (e.key === "s" || e.key === "S")) {
    if (state.view === "settings") {
      if (e.preventDefault) e.preventDefault();
      addRoot();
    }
    return;
  }
  if (e.key === "Escape") {
    if (state.view === "viewer") {
      if (e.preventDefault) e.preventDefault();
      backToList();
      return;
    }
    if (state.view === "settings") {
      if (e.preventDefault) e.preventDefault();
      backFromSettings();
      return;
    }
    const q = el("q");
    if (document.activeElement === q) {
      if (q.value) clearFilters();
      else if (q.blur) q.blur();
    }
    return;
  }
  // 只在非输入上下文抢焦点，避免打断输入。
  if (e.key === "," && !editing && !e.ctrlKey && !e.metaKey && !e.altKey && state.view !== "settings") {
    if (e.preventDefault) e.preventDefault();
    openSettings();
    return;
  }
  if (e.key === "/" && !editing && !e.ctrlKey && !e.metaKey && !e.altKey && state.view === "list") {
    if (e.preventDefault) e.preventDefault();
    const q = el("q");
    if (q.focus) q.focus();
    if (q.select) q.select();
  }
}

function bind() {
  el("q").addEventListener("input", (e) => { state.q = String((e.target && e.target.value) || "").trim(); render(); });
  el("f-type").addEventListener("change", (e) => { state.type = (e.target && e.target.value) || ""; render(); });
  el("f-root").addEventListener("change", (e) => { state.root = (e.target && e.target.value) || ""; render(); });
  el("f-sort").addEventListener("change", (e) => { state.sort = (e.target && e.target.value) || "mtime"; render(); });
  el("btn-refresh").addEventListener("click", () => { reload(true); });
  el("btn-empty-rescan").addEventListener("click", () => { reload(true); });
  el("btn-empty-settings").addEventListener("click", openSettings);
  el("btn-retry").addEventListener("click", () => { reload(!!state.index); });
  el("btn-clear").addEventListener("click", clearFilters);
  el("btn-clear-2").addEventListener("click", clearFilters);
  el("btn-back").addEventListener("click", backToList);
  el("btn-viewer-retry").addEventListener("click", () => { state.loadedId = ""; ensureViewerLoaded(); });
  el("btn-settings").addEventListener("click", openSettings);
  el("btn-theme").addEventListener("click", cycleTheme);
  el("btn-settings-back").addEventListener("click", backFromSettings);
  el("btn-root-add").addEventListener("click", addRoot);
  el("root-path").addEventListener("keydown", (e) => {
    if (e.key === "Enter") { if (e.preventDefault) e.preventDefault(); addRoot(); }
  });
  el("grid").addEventListener("click", onGridClick);
  el("view-viewer").addEventListener("click", onViewerClick);
  el("view-settings").addEventListener("click", onSettingsClick);
  document.addEventListener("keydown", onKeyDown);
  window.addEventListener("hashchange", syncViewFromHash);
}

bind();
applyTheme(state.theme);
syncViewFromHash();
loadSettings(true); // 只用于底部关闭行为提示；失败时静默，不打扰首屏
reload(false);
