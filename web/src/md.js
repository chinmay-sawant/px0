// web/src/md.js
import { $, api, S, doc_ } from './state.js';
import { showToast } from './ui.js';
import { layout, render, updateEditorOptionControls } from './renderer.js';
import { updateStatus, fitStatus } from './status.js';
import { renderMermaidBlocks, forgetMermaid } from './mermaid.js';

/* Rendered Markdown preview. One container (#mdview) is shared by every tab;
   each tab keeps its own injected body so switching back is instant and does
   not re-run Mermaid. A tab's mode is 'source' | 'preview': only the explicit
   toggle changes the saved preference, jumps never do. */

let previewShown = null;   // tab whose mdEl currently sits in #mdview, or null

let openInApp = null;   // registered by tabs.js: opens a workspace path in px0

/* Relative links inside a preview must not navigate the app away to a 404.
   tabs.js registers openFile here so this module stays free of an import cycle
   back into the tab layer. */
export function setPreviewLinkOpener(fn) { openInApp = fn; }

/* Rendered HTML arrives from GET /api/md. A failure never breaks opening the
   file: the tab drops to source and says why. */
export async function loadPreview(d) {
  let j;
  try { j = await api('/api/md', { path: d.path }); }
  catch (e) {
    if (!S.tabs.includes(d)) return;   // tab closed while rendering
    d.mdHtml = null;
    d.mode = 'source';
    if (doc_() === d) { syncPreview(); layout(); render(); updateStatus(); }
    showToast('!', d.name + ': ' + (e.message || 'could not render Markdown'));
    return;
  }
  if (!S.tabs.includes(d)) return;   // tab closed while the fragment was in flight
  d.mdHtml = typeof j.html === 'string' ? j.html : '';
  if (doc_() === d && d.mode === 'preview') syncPreview();
}

/* Build the tab's own .md-body once. Mermaid is invoked after the injection
   and runs in the background so the Markdown itself paints first. */
function previewBody(d) {
  if (d.mdEl) return d.mdEl;
  const el = document.createElement('div');
  el.className = 'md-body';
  el.innerHTML = d.mdHtml || '';
  el.addEventListener('click', onPreviewClick);
  d.mdEl = el;
  renderMermaidBlocks(el).catch(() => {});
  return el;
}

/* Resolve a preview link against the tab's directory without letting it climb
   above the workspace root. Returns '' for links that keep browser behavior:
   schemes, protocol-relative URLs, pure anchors, and paths that would escape. */
function resolveLink(d, href) {
  if (!d || !href || href.charAt(0) === '#') return '';
  if (/^[a-z][a-z0-9+.-]*:/i.test(href) || href.slice(0, 2) === '//') return '';
  const clean = href.split('#')[0].split('?')[0];
  if (!clean) return '';
  const dir = d.path.includes('/') ? d.path.slice(0, d.path.lastIndexOf('/')) : '';
  const out = dir ? dir.split('/') : [];
  for (const raw of clean.split('/')) {
    if (!raw || raw === '.') continue;
    if (raw === '..') {
      if (!out.length) return '';   // leaves the workspace: let the browser try
      out.pop();
      continue;
    }
    let seg = raw;
    try { seg = decodeURIComponent(raw); } catch { /* keep the raw segment */ }
    out.push(seg);
  }
  return out.join('/');
}

/* Open workspace files in a real tab; anchors and external links keep their
   defaults (external links already carry target="_blank" from the server). */
function onPreviewClick(e) {
  const a = e.target instanceof Element ? e.target.closest('a') : null;
  if (!a) return;
  const path = resolveLink(doc_(), a.getAttribute('href') || '');
  if (!path || !openInApp) return;
  e.preventDefault();
  openInApp(path);
}

export function showPreview(d) {
  const box = $('#mdview');
  if (!box || d.mdHtml == null) return;
  if (previewShown !== d) {
    if (previewShown) previewShown.mdScroll = box.scrollTop;   // leave it where it was read
    box.replaceChildren(previewBody(d));
    previewShown = d;
    box.scrollTop = d.mdScroll || 0;
  }
  box.hidden = false;
}

export function hidePreview() {
  const box = $('#mdview');
  if (!box || box.hidden) return;
  if (previewShown) previewShown.mdScroll = box.scrollTop;
  box.hidden = true;
}

/* Apply the active tab's mode to the editor chrome. The source view stays
   mounted underneath, so its scroll and rows are untouched; restoring is just
   layout() + render() once the mode flips back. While rendered HTML is still
   in flight the body class already marks preview, but #mdview stays hidden and
   the source rows remain readable. */
export function syncPreview() {
  const d = doc_();
  const preview = !!d && d.mode === 'preview';
  document.body.classList.toggle('md-preview', preview);
  if (preview && d.mdHtml != null) {
    showPreview(d);
    const caret = $('#caret');
    if (caret) caret.hidden = true;
  } else {
    hidePreview();
  }
  updateEditorOptionControls();
  fitStatus();   // the preview button adds or removes width
}

/* Release the rendered DOM a closed tab owned. */
export function forgetPreview(d) {
  if (previewShown === d) {
    $('#mdview')?.replaceChildren();
    previewShown = null;
  }
  forgetMermaid(d.mdEl);
  d.mdEl = null;
  d.mdHtml = null;
}
