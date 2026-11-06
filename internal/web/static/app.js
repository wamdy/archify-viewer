"use strict";

// Archify 产物库前端：零构建 vanilla JS，原生窗口与浏览器共用同一份（design D-7）。
// 只做「读索引 → 列表 → 打开原始 HTML」；不渲染、不校验、不改写产物。

const state = { index: null, q: "", type: "", root: "", sort: "mtime" };

const el = (id) => document.getElementById(id);
const grid = el("grid");

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

async function loadIndex(rescan) {
  el("meta").textContent = "正在读取索引…";
  const url = "/api/index" + (rescan ? "?rescan=1" : "");
  const res = await fetch(url, { cache: "no-store" });
  if (!res.ok) throw new Error("索引读取失败：HTTP " + res.status);
  state.index = await res.json();
}

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

const DELIVERY_LABEL = { current: "已交付", failed: "交付失败", unknown: "未记录" };

function card(a) {
  const node = document.createElement("article");
  node.className = "card" + (a.delivery === "failed" ? " card-flag" : "");

  const deepLink = location.origin + "/a/" + a.id;
  const warnings = (a.warnings || []).filter((w) => w !== "CANDIDATE-MISSING" && w !== "SIDECAR-MISSING");

  node.innerHTML =
    '<div class="card-head">' +
      '<span class="tag tag-type">' + esc(a.type) + "</span>" +
      '<span class="tag tag-' + esc(a.delivery) + '">' + esc(DELIVERY_LABEL[a.delivery] || a.delivery) + "</span>" +
      (a.quality ? '<span class="tag tag-quality">' + esc(a.quality) + "</span>" : "") +
    "</div>" +
    '<h3 class="card-title">' + esc(a.title) + "</h3>" +
    (a.subtitle ? '<p class="card-sub">' + esc(a.subtitle) + "</p>" : "") +
    '<p class="card-path"><span class="root-chip">' + esc(a.root) + "</span>" + esc(a.rel_path) + "</p>" +
    (a.repository ? '<p class="card-repo" title="' + esc(a.repository) + '">↳ ' + esc(a.repository) + "</p>" : "") +
    '<p class="card-meta">' + esc(when(a.mtime)) + " · " + bytes(a.size) +
      (warnings.length ? ' · <span class="warn">解析提示 ' + warnings.length + "</span>" : "") +
    "</p>" +
    '<div class="card-actions">' +
      '<a class="btn btn-primary" href="/a/' + esc(a.id) + '" target="_blank" rel="noopener">打开</a>' +
      '<button class="btn" data-copy="' + esc(deepLink) + '">复制深链</button>' +
      '<button class="btn" data-copy="' + esc(a.id) + '">复制 ID</button>' +
    "</div>";

  node.querySelectorAll("[data-copy]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const text = btn.getAttribute("data-copy");
      try {
        await navigator.clipboard.writeText(text);
        flash(btn, "已复制");
      } catch (err) {
        flash(btn, "复制失败");
      }
    });
  });
  return node;
}

function flash(btn, text) {
  const old = btn.textContent;
  btn.textContent = text;
  btn.disabled = true;
  setTimeout(() => { btn.textContent = old; btn.disabled = false; }, 1200);
}

function fillSelect(select, values, keep) {
  const current = keep === undefined ? select.value : keep;
  select.length = 0;
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
  if (current && values.includes(current)) select.value = current;
}

function render() {
  const idx = state.index;
  if (!idx) return;

  const arts = idx.artifacts || [];
  fillSelect(el("f-type"), Object.keys(idx.by_type || {}).sort(), state.type);
  el("f-type").dataset.head = "全部类型";
  fillSelect(el("f-root"), (idx.roots || []).map((r) => r.label), state.root);
  el("f-root").dataset.head = "全部来源";

  const visible = sorted(arts.filter(match));
  grid.innerHTML = "";
  visible.forEach((a) => grid.appendChild(card(a)));

  el("empty").hidden = arts.length !== 0;
  grid.hidden = arts.length === 0;

  el("stats").textContent = visible.length + " / " + arts.length + " 件";
  el("meta").textContent =
    "来源 " + (idx.root_source || "none") +
    " · 根 " + ((idx.roots || []).length) +
    " · 产物 " + arts.length +
    (idx.parse_warnings ? " · " + idx.parse_warnings + " 项未能完全解析" : "");
  el("generated").textContent = idx.generated_at ? "索引生成于 " + when(idx.generated_at) : "";

  const errs = (idx.roots || []).filter((r) => r.error).map((r) => r.label + "：" + r.error);
  const banner = el("root-errors");
  banner.hidden = errs.length === 0;
  banner.textContent = errs.join("  |  ");
}

async function reload(rescan) {
  try {
    await loadIndex(rescan);
    render();
  } catch (err) {
    el("meta").textContent = String(err.message || err);
    el("root-errors").hidden = false;
    el("root-errors").className = "banner banner-error";
    el("root-errors").textContent = "索引不可用：" + String(err.message || err);
  }
}

function bind() {
  el("q").addEventListener("input", (e) => { state.q = e.target.value.trim(); render(); });
  el("f-type").addEventListener("change", (e) => { state.type = e.target.value; render(); });
  el("f-root").addEventListener("change", (e) => { state.root = e.target.value; render(); });
  el("f-sort").addEventListener("change", (e) => { state.sort = e.target.value; render(); });
  el("btn-refresh").addEventListener("click", () => reload(true));
  document.addEventListener("keydown", (e) => {
    if (e.key === "/" && document.activeElement !== el("q")) {
      e.preventDefault();
      el("q").focus();
    }
  });
}

bind();
reload(false);
