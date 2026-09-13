// web/src/tabs.js
import { $, esc, S, doc_, api, LH, CHUNK, withKeys, isMarkdown } from './state.js';
import { vp, sizer, rowsEl, editor, showToast } from './ui.js';
import { render, layout, refineChunk } from './renderer.js';
import { updateStatus, setStatusNote, refreshMetrics } from './status.js';
import { pushHistory } from './history.js';
import { warmLSP } from './lsp.js';
import { loadOutline } from './outline.js';
import { showPanel } from './panels.js';
import { revealDir } from './tree.js';
import { clearLink } from './hover.js';
import { clearFind } from './find.js';
import { clearSelectAll } from './selbar.js';
import { loadPreview, syncPreview, forgetPreview, setPreviewLinkOpener } from './md.js';

// Rendered previews route relative link clicks through openFile; registered
// here so md.js stays free of a circular import back into this module.
setPreviewLinkOpener(openFile);

// Recently closed files, newest last, for Alt+Shift+T. Only path, caret and
// scroll are kept, so a reopened Markdown tab re-seeds its mode from the
// px0.mdPreview preference (see openFile) rather than restoring source/preview.
const closedTabs = [];
const MAX_CLOSED = 20;

export async function openFile(path, opts = {}) {
  const { line, push = true, col, source } = opts;
  let idx = S.tabs.findIndex(t => t.path === path);
  if (idx < 0) {
    let j;
    const start = line ? Math.max(0, Math.floor((line - 1) / CHUNK) * CHUNK) : 0;
    try {
      j = await api('/api/file', { path, start, count: CHUNK });
    } catch (e) {
      setStatusNote(path + ': ' + e.message);
      return;
    }
    if (j.image) {
      showImage(path);
      return;
    }
    const renderPreview = isMarkdown(path) && S.mdPreview && !source;
    const d = {
      path, name: path.split('/').pop(), lang: j.lang, total: j.total, maxCols: j.maxCols,
      size: j.size, lines: new Array(j.total), chunks: new Set([start / CHUNK]),
      pending: new Set(), refining: new Set(), scrollTop: 0, cur: line || 1,
      outline: null, gen: 0,
      // Rendered preview state, only meaningful for .md/.markdown (web/src/md.js).
      mode: renderPreview ? 'preview' : 'source', mdHtml: null, mdEl: null, mdScroll: 0,
    };
    for (let i = 0; i < j.lines.length; i++) d.lines[j.start + i] = j.lines[i];
    d.lsp = j.lsp || { state: 'off', server: '' };
    S.tabs.push(d);
    idx = S.tabs.length - 1;
    if (j.refine) refineChunk(d, start / CHUNK);
    if (d.mode === 'preview') loadPreview(d);
  }
  const prev = doc_();
  if (prev && prev !== S.tabs[idx]) prev.scrollTop = vp.scrollTop;
  if (prev !== S.tabs[idx]) clearSelectAll();
  S.active = idx;
  const d = S.tabs[idx];

  // Callers that jump to a line ask for source: a rendered preview has no line
  // to land on, and the preference has already done its job for the open.
  if (source && d.mode === 'preview') setTabMode(d, 'source', false);

  $('#empty').hidden = true;
  hideImage();
  if (!S.at || S.at.path !== d.path) S.at = null;
  S.lsp.state = (d.lsp && d.lsp.state) || 'off';
  S.lsp.server = (d.lsp && d.lsp.server) || '';
  S.lsp.missing = (d.lsp && d.lsp.missing) || '';
  warmLSP(d);
  drawTabs(); drawCrumbs(); syncPreview(); layout();

  if (line) { d.cur = line; centerLine(line); }
  else vp.scrollTop = d.scrollTop;
  render();
  updateStatus();
  if ($('#panel-outline')?.classList.contains('active')) loadOutline();
  if (push) pushHistory(path, line || d.cur, col);
}

export function centerLine(n) {
  const y = (n - 1) * LH - Math.max(0, vp.clientHeight / 2 - LH * 2);
  vp.scrollTop = Math.max(0, y);
}

/* Alt+M, the footer button and the palette command: flip the active tab
   between source and rendered Markdown, and remember the choice for the next
   .md open. Failure to fetch the render falls back to source in md.js. */
export function togglePreview() {
  const d = doc_();
  if (!d) return;
  if (!isMarkdown(d.path)) { showToast('Preview', 'Markdown preview only applies to .md files'); return; }
  const next = d.mode === 'preview' ? 'source' : 'preview';
  setTabMode(d, next, true);
  // Preview has its own selection model: drop source-side selection chrome.
  if (next === 'preview') { clearLink(); clearFind(); clearSelectAll(); }
  syncPreview();
  layout();
  render();
  updateStatus();
  if (next === 'preview' && d.mdHtml == null) loadPreview(d);
}

/* persist=true stores the preference (the toggle); jump-induced switches pass
   false, so leaving preview to land on a line does not rewrite the default. */
export function setTabMode(d, mode, persist = false) {
  if (!d || d.mode === mode) return;
  d.mode = mode;
  if (!persist) return;
  S.mdPreview = mode === 'preview';
  try { localStorage.setItem('px0.mdPreview', S.mdPreview ? 'true' : 'false'); } catch {}
}

/* Line jumps (outline, inspector, palette, references, history) use this so a
   preview tab shows the line in source instead of silently ignoring it. */
export function gotoLine(line) {
  const d = doc_();
  if (!d) return;
  if (d.mode === 'preview') { setTabMode(d, 'source', false); syncPreview(); layout(); }
  d.cur = Math.max(1, Math.min(line, d.total));
  centerLine(d.cur);
  render();
  updateStatus();
  pushHistory(d.path, d.cur);
}

export function closeTab(i) {
  clearSelectAll();
  const [closed] = S.tabs.splice(i, 1);
  if (closed) {
    forgetPreview(closed);
    if (closed.path) {
      // The active tab's scrollTop is only saved on switch, so read the live one.
      const scrollTop = i === S.active ? vp.scrollTop : closed.scrollTop;
      closedTabs.push({ path: closed.path, cur: closed.cur, scrollTop });
      if (closedTabs.length > MAX_CLOSED) closedTabs.shift();
      api('/api/close', { path: closed.path })
        .then(() => refreshMetrics())
        .catch(() => {});
    }
    // Release large arrays to assist garbage collection
    closed.lines = null;
    closed.chunks?.clear?.();
    closed.pending?.clear?.();
    closed.refining?.clear?.();
    closed.outline = null;
  }
  if (S.tabs.length === 0) {
    S.active = -1;
    rowsEl.innerHTML = ''; sizer.style.height = '0px';
    $('#empty').hidden = false; drawCrumbs();
    drawTabs(); syncPreview(); updateStatus();
    return;
  }
  S.active = Math.min(i, S.tabs.length - 1);
  const d = doc_();
  drawTabs(); drawCrumbs(); syncPreview(); layout();
  vp.scrollTop = d.scrollTop; render(); updateStatus();
}

// Reopens the most recently closed file that is not open already, where it was
// left. A Markdown tab re-seeds source/preview from the px0.mdPreview
// preference: closedTabs stores path, caret and scroll only.
export async function reopenClosedTab() {
  while (closedTabs.length) {
    const t = closedTabs.pop();
    if (S.tabs.some(d => d.path === t.path)) continue;
    await openFile(t.path, { line: t.cur });
    if (doc_()?.path !== t.path) return;
    vp.scrollTop = t.scrollTop;
    render(); updateStatus();
    return;
  }
}

export function drawTabs() {
  $('#tabs').innerHTML = S.tabs.map((t, i) =>
    '<div class="tab' + (i === S.active ? ' active' : '') + '" data-i="' + i + '" title="' + esc(t.path) + '">' +
    '<span class="tn">' + esc(t.name) + '</span><span class="x" data-close="' + i + '" title="' + withKeys('Close tab ({Alt+W})') + '"><svg viewBox="0 0 10 10" aria-hidden="true"><path d="M2 2l6 6M8 2l-6 6"/></svg></span></div>').join('');
  const act = $('#tabs .tab.active');
  if (act) act.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}

export function switchTab(i) {
  if (i === S.active || !S.tabs[i]) return;
  clearLink();
  const prev = doc_();
  if (prev) prev.scrollTop = vp.scrollTop;
  S.active = i;
  clearFind();
  clearSelectAll();
  S.at = null;
  S.lsp.state = (S.tabs[i].lsp && S.tabs[i].lsp.state) || 'off';
  S.lsp.server = (S.tabs[i].lsp && S.tabs[i].lsp.server) || '';
  S.lsp.missing = (S.tabs[i].lsp && S.tabs[i].lsp.missing) || '';
  warmLSP(S.tabs[i]);
  drawTabs(); drawCrumbs(); syncPreview(); layout();
  vp.scrollTop = S.tabs[i].scrollTop;
  render(); updateStatus();
  if ($('#panel-outline')?.classList.contains('active')) loadOutline();
  pushHistory(S.tabs[i].path, S.tabs[i].cur);
}

export function drawCrumbs() {
  const el = $('#crumbs');
  if (el) el.innerHTML = '';
}

export function showImage(path) {
  hideImage();
  const box = document.createElement('div');
  box.id = 'imgview';
  box.innerHTML = '<img src="/api/raw?path=' + encodeURIComponent(path) + '" alt="">';
  editor.appendChild(box);
  $('#empty').hidden = true;
}

export function hideImage() {
  const b = $('#imgview');
  if (b) b.remove();
}

export function initTabs() {
  $('#tabs').addEventListener('click', e => {
    const x = e.target.closest('[data-close]');
    if (x) { closeTab(+x.dataset.close); return; }
    const t = e.target.closest('.tab');
    if (t) switchTab(+t.dataset.i);
  });
  $('#tabs').addEventListener('auxclick', e => {
    const t = e.target.closest('.tab');
    if (t && e.button === 1) { e.preventDefault(); closeTab(+t.dataset.i); }
  });
  const crumbsEl = $('#crumbs');
  if (crumbsEl) {
    crumbsEl.addEventListener('click', e => {
      const c = e.target.closest('[data-dir]');
      if (c) { showPanel('files'); revealDir(c.dataset.dir); }
    });
  }
}
