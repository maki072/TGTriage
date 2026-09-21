'use strict';

/* ---------------------------------------------------------------------- */
/* Telegram Mini App bootstrap, theme                                     */
/* ---------------------------------------------------------------------- */

const tg = window.Telegram && window.Telegram.WebApp ? window.Telegram.WebApp : null;
const inTelegram = !!(tg && tg.initData);

// The design system ships its own dark and light themes (checked for contrast); the host only picks which one.
function applyTheme() {
  let scheme = tg && tg.colorScheme;
  if (scheme !== 'light' && scheme !== 'dark') scheme = window.matchMedia && matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  document.documentElement.dataset.theme = scheme;
  const surface = getComputedStyle(document.documentElement).getPropertyValue('--surface').trim();
  if (tg && surface) {
    try { tg.setHeaderColor(surface); } catch (e) {}
    try { tg.setBackgroundColor(surface); } catch (e) {}
  }
}
if (tg) {
  tg.ready();
  tg.expand();
  tg.onEvent('themeChanged', applyTheme);
}
applyTheme();
if (!inTelegram) document.getElementById('devBanner').hidden = false;

function haptic(kind) {
  if (!tg || !tg.HapticFeedback) return;
  try {
    if (kind === 'success' || kind === 'error' || kind === 'warning') tg.HapticFeedback.notificationOccurred(kind);
    else tg.HapticFeedback.impactOccurred(kind || 'light');
  } catch (e) {}
}

function openLink(url) {
  if (!url) return;
  if (tg && url.startsWith('https://t.me/') && tg.openTelegramLink) { try { tg.openTelegramLink(url); return; } catch (e) {} }
  if (tg && tg.openLink && url.startsWith('http')) { try { tg.openLink(url); return; } catch (e) {} }
  window.open(url, '_blank');
}

/* ---------------------------------------------------------------------- */
/* API                                                                    */
/* ---------------------------------------------------------------------- */

async function api(method, path, body) {
  const headers = { 'Content-Type': 'application/json' };
  if (inTelegram) headers['Authorization'] = 'tma ' + tg.initData;
  const res = await fetch(path, { method, headers, body: body !== undefined ? JSON.stringify(body) : undefined });
  let data = null;
  try { data = await res.json(); } catch (e) {}
  if (!res.ok) throw new Error((data && data.error) || ('HTTP ' + res.status));
  return data;
}

/* ---------------------------------------------------------------------- */
/* Icons (line, 24px grid, currentColor)                                  */
/* ---------------------------------------------------------------------- */

const ring = (x, y, r) => `M${x - r} ${y}a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0 ${-2 * r} 0`;
const ICONS = {
  'chevron-right': 'M9 6l6 6-6 6', 'chevron-left': 'M15 6l-6 6 6 6', 'chevron-down': 'M6 9l6 6 6-6',
  'check': 'M5 12.5l4.5 4.5L19 7.5', 'x': 'M6 6l12 12M18 6L6 18', 'plus': 'M12 5v14M5 12h14',
  'search': ring(11, 11, 6) + 'M20 20l-4.2-4.2', 'filter': 'M4 5h16l-6.2 7.3V19l-3.6-1.8v-4.9z',
  'trash': 'M5 7h14M10 7V5h4v2M7 7l1 12h8l1-12M10.5 11v5M13.5 11v5',
  'edit': 'M4 20l1-4L16.5 4.5a2 2 0 013 3L8 19zM14.5 6.5l3 3',
  'clock': ring(12, 12, 8) + 'M12 8v4.5l3 1.5', 'bell': 'M6 16v-5a6 6 0 1112 0v5l1.5 2h-15zM10 20.5a2 2 0 004 0',
  'merge': ring(6, 5, 2) + ring(6, 19, 2) + ring(18, 15, 2) + 'M6 7v10M6 7c0 5 12 1 12 6',
  'send': 'M21 3L10 14M21 3l-7 18-4-8-8-4z', 'reply': 'M9 14l-5-5 5-5M4 9h9a7 7 0 017 7v3',
  'message': 'M5 5h14a2 2 0 012 2v8a2 2 0 01-2 2h-7l-5 4v-4H5a2 2 0 01-2-2V7a2 2 0 012-2z',
  'tasks': 'M4 6l1.5 1.5L8 5M4 12l1.5 1.5L8 11M4 18l1.5 1.5L8 17M11 6.5h9M11 12.5h9M11 18.5h6',
  'history': 'M3.5 12a8.5 8.5 0 108.5-8.5A8.5 8.5 0 005.6 6.4L3.5 8.5M3.5 4v4.5H8M12 7.5V12l3 1.8',
  'sliders': 'M4 7h8M17 7h3M4 12h2M11 12h9M4 17h8M17 17h3' + ring(15, 7, 2) + ring(8, 12, 2) + ring(15, 17, 2),
  'alert': 'M12 4l9 16H3zM12 10v4M12 17h.01', 'info': ring(12, 12, 8) + 'M12 11v5M12 8h.01',
  'lock': 'M6 11h12v9H6zM8.5 11V8a3.5 3.5 0 017 0v3', 'unlock': 'M6 11h12v9H6zM8.5 11V8a3.5 3.5 0 016.7-1.4',
  'external': 'M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 01-1 1H5a1 1 0 01-1-1V7a1 1 0 011-1h5',
  'arrow-up': 'M12 19V5M6 11l6-6 6 6', 'arrow-down': 'M12 5v14M6 13l6 6 6-6',
  'refresh': 'M20 12a8 8 0 11-2.3-5.7L20 8.5M20 4v4.5h-4.5', 'user': ring(12, 8, 4) + 'M4 20c0-4 3.6-6 8-6s8 2 8 6',
  'flag': 'M5 21V4M5 5h11l-2 4 2 4H5', 'ban': ring(12, 12, 8) + 'M6.5 6.5l11 11',
  'eye': 'M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z' + ring(12, 12, 3),
  'check-circle': ring(12, 12, 8) + 'M8.5 12.5l2.5 2.5 4.5-5', 'x-circle': ring(12, 12, 8) + 'M9 9l6 6M15 9l-6 6',
  'download': 'M12 4v11M7 11l5 5 5-5M5 20h14',
  'bot': 'M12 4v3M6 7h12a2 2 0 012 2v8a2 2 0 01-2 2H6a2 2 0 01-2-2V9a2 2 0 012-2zM9 13h.01M15 13h.01',
  'sparkles': 'M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8zM19 16l.7 1.8L21.5 18.5l-1.8.7L19 21l-.7-1.8-1.8-.7 1.8-.7z',
  'headset': 'M4 14v-2a8 8 0 0116 0v2M4 14h3v5H5a1 1 0 01-1 1zM20 14h-3v5h2a1 1 0 001-1z',
  'power': 'M12 3v9M6.3 7.5a8 8 0 1011.4 0',
};
function I(name, size, cls) {
  if (name === 'more') return `<svg class="tr-icon${cls ? ' ' + cls : ''}" width="${size || 22}" height="${size || 22}" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="5.5" cy="12" r="1.75"/><circle cx="12" cy="12" r="1.75"/><circle cx="18.5" cy="12" r="1.75"/></svg>`;
  return `<svg class="tr-icon${cls ? ' ' + cls : ''}" width="${size || 20}" height="${size || 20}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${ICONS[name] || ''}"/></svg>`;
}

/* ---------------------------------------------------------------------- */
/* Formatting helpers                                                     */
/* ---------------------------------------------------------------------- */

function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function fmtDT(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  const now = new Date();
  if (d.toDateString() === now.toDateString()) return d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
  return d.toLocaleString('ru-RU', {
    day: '2-digit', month: '2-digit', year: d.getFullYear() === now.getFullYear() ? undefined : '2-digit',
    hour: '2-digit', minute: '2-digit',
  });
}

// fmtWhen renders a future moment relative to today: "сегодня, 18:30", "завтра, 09:00", "25 сен, 09:00".
const MONTHS = ['янв', 'фев', 'мар', 'апр', 'мая', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'];
function fmtWhen(d, now) {
  now = now || new Date();
  const pad = n => (n < 10 ? '0' : '') + n;
  const day = x => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const t = pad(d.getHours()) + ':' + pad(d.getMinutes());
  const dd = Math.round((day(d) - day(now)) / 864e5);
  if (dd === 0) return 'сегодня, ' + t;
  if (dd === 1) return 'завтра, ' + t;
  return d.getDate() + ' ' + MONTHS[d.getMonth()] + ', ' + t;
}

function waitedFor(iso) {
  if (!iso) return '';
  const m = Math.max(1, Math.round((Date.now() - new Date(iso).getTime()) / 60000));
  if (m < 60) return m + ' мин';
  if (m < 1440) return Math.floor(m / 60) + ' ч ' + (m % 60) + ' мин';
  return Math.floor(m / 1440) + ' дн';
}

function fmtSize(n) {
  if (n >= 1048576) return (n / 1048576).toFixed(1) + ' МБ';
  if (n >= 1024) return Math.round(n / 1024) + ' КБ';
  return n + ' Б';
}

function plural(n, one, few, many) {
  const m = n % 100, d = n % 10;
  if (m > 10 && m < 15) return many;
  return d === 1 ? one : d >= 2 && d <= 4 ? few : many;
}

const PRIORITY_LABEL = { critical: 'Критический', high: 'Высокий', medium: 'Средний', low: 'Низкий' };
const PRIORITY_BARS = { critical: 4, high: 3, medium: 2, low: 1 };
const PRIORITY_OPTIONS = [['low', 'Низкая'], ['medium', 'Средняя'], ['high', 'Высокая'], ['critical', 'Критическая']];
const IMPORTANCE_LABEL = { critical: 'критическая', high: 'высокая', medium: 'средняя', low: 'низкая' };
const CATEGORY_LABEL = { bug: 'Баг', help_request: 'Помощь', task: 'Задача', question: 'Вопрос', deadline: 'Дедлайн', agreement: 'Договорённость', other: 'Другое' };
const STATUS = {
  new: { label: 'Новая', tone: 'accent', icon: 'sparkles' },
  in_progress: { label: 'В работе', tone: 'warning', icon: 'eye' },
  snoozed: { label: 'Отложена', tone: 'neutral', icon: 'clock' },
  done: { label: 'Завершена', tone: 'success', icon: 'check-circle' },
  false_positive: { label: 'Не задача', tone: 'neutral', icon: 'ban' },
};
const STRATEGY_LABEL = { confirm: 'подтверждение', clarify: 'уточняющий вопрос', decline: 'отказ', none: 'ответ' };

/* ---------------------------------------------------------------------- */
/* HTML builders for design-system components                             */
/* ---------------------------------------------------------------------- */

function btn(label, o) {
  o = o || {};
  const cls = ['tr-btn', o.v && 'tr-btn--' + o.v, o.sm && 'tr-btn--sm', o.block && 'tr-btn--block', o.cls].filter(Boolean).join(' ');
  return `<button type="button" class="${cls}"${o.id ? ` id="${o.id}"` : ''}${o.act ? ` data-act="${o.act}"` : ''}${o.attrs ? ' ' + o.attrs : ''}${o.disabled ? ' disabled' : ''}>` +
    `${o.icon ? I(o.icon, o.sm ? 18 : 20) : ''}${esc(label)}</button>`;
}
function ibtn(icon, label, o) {
  o = o || {};
  return `<button type="button" class="tr-iconbtn${o.tone ? ' tr-iconbtn--' + o.tone : ''}"${o.id ? ` id="${o.id}"` : ''} aria-label="${esc(label)}" title="${esc(label)}"` +
    `${o.act ? ` data-act="${o.act}"` : ''}${o.attrs ? ' ' + o.attrs : ''}${o.disabled ? ' disabled' : ''}>${I(icon, o.size || 22)}</button>`;
}
function badge(text, o) {
  o = o || {};
  const tag = o.act ? 'button type="button"' : 'span';
  return `<${tag} class="tr-badge${o.tone && o.tone !== 'neutral' ? ' tr-badge--' + o.tone : ''}"${o.act ? ` data-act="${o.act}"` : ''}>` +
    `${o.icon ? I(o.icon, 14) : ''}${text}${o.act ? I('chevron-down', 14) : ''}</${o.act ? 'button' : 'span'}>`;
}
function statusBadge(status, act) {
  const s = STATUS[status] || { label: status, tone: 'neutral' };
  return badge(esc(s.label), { tone: s.tone, icon: s.icon, act });
}
function prioMark(level, label) {
  const n = PRIORITY_BARS[level] || 2;
  const bars = [5, 8, 11, 14].map((bh, i) => `<rect x="${i * 5}" y="${16 - bh}" width="3" height="${bh}" rx="1" class="${i < n ? 'on' : 'off'}"/>`).join('');
  const svg = `<svg class="tr-prio tr-prio--${esc(level || 'medium')}" width="18" height="16" viewBox="0 0 18 16" role="img" aria-label="Приоритет: ${esc((PRIORITY_LABEL[level] || '').toLowerCase())}">${bars}</svg>`;
  return label ? `<span class="tr-row" style="gap:6px;flex-wrap:nowrap">${svg}<span>${esc(PRIORITY_LABEL[level] || level)}</span></span>` : svg;
}
function chip(label, o) {
  o = o || {};
  return `<button type="button" class="tr-chip"${o.selected == null ? '' : ` aria-pressed="${!!o.selected}"`}${o.act ? ` data-act="${o.act}"` : ''}${o.attrs ? ' ' + o.attrs : ''}>` +
    `${o.icon ? I(o.icon, 16) : ''}${esc(label)}${o.remove ? I('x', 14) : ''}</button>`;
}
function segmented(name, options, value) {
  return `<div class="tr-seg" role="group" data-seg="${name}">` + options.map(o =>
    `<button type="button" data-value="${esc(o.value)}" aria-pressed="${o.value === value}">${esc(o.label)}${o.count != null ? `<span class="tr-seg__count">${o.count}</span>` : ''}</button>`).join('') + `</div>`;
}
function switchBtn(checked, attrs, label) {
  return `<button type="button" role="switch" class="tr-switch" aria-checked="${!!checked}" aria-label="${esc(label || '')}" ${attrs || ''}></button>`;
}
function navRow(o) {
  const inner = (o.icon ? `<span class="tr-navrow__icon">${I(o.icon, 18)}</span>` : '') +
    `<span class="tr-navrow__text"><span class="tr-navrow__label">${o.labelHtml != null ? o.labelHtml : esc(o.label)}</span>${o.desc ? `<span class="tr-navrow__desc">${esc(o.desc)}</span>` : ''}</span>` +
    (o.value != null ? `<span class="tr-navrow__value">${esc(o.value)}</span>` : '');
  const cls = 'tr-navrow' + (o.danger ? ' tr-navrow--danger' : '');
  if (o.act && !o.trailing) return `<button type="button" class="${cls}" data-clickable="true" data-act="${o.act}"${o.attrs ? ' ' + o.attrs : ''}>${inner}${I('chevron-right', 18, 'tr-navrow__chev')}</button>`;
  if (o.act && o.trailing) return `<div class="${cls}"><button type="button" class="tr-navrow__hit" data-act="${o.act}"${o.attrs ? ' ' + o.attrs : ''}>${inner}</button>${o.trailing}</div>`;
  return `<div class="${cls}">${inner}${o.trailing || ''}</div>`;
}
function group(rows) { return `<div class="tr-group">${rows.join('')}</div>`; }
function banner(text, tone, o) {
  o = o || {};
  const icon = o.icon || ({ warning: 'clock', danger: 'alert', success: 'check-circle' }[tone] || 'info');
  const body = `${I(icon, 20)}<div class="tr-banner__text">${text}</div>${o.action ? `<span class="tr-banner__act">${esc(o.action)}${I('chevron-right', 16)}</span>` : ''}`;
  const cls = 'tr-banner' + (tone && tone !== 'info' ? ' tr-banner--' + tone : '');
  return o.act ? `<button type="button" class="${cls}" data-act="${o.act}">${body}</button>` : `<div class="${cls}"${tone === 'danger' ? ' role="alert"' : ''}>${body}</div>`;
}
function emptyState(icon, title, text, action) {
  return `<div class="tr-empty">${I(icon, 32)}<b>${esc(title)}</b>${text ? `<span>${esc(text)}</span>` : ''}${action || ''}</div>`;
}
function avatar(name, size) { return `<span class="tr-avatar${size ? ' tr-avatar--' + size : ''}" aria-hidden="true">${esc((name || '?').slice(0, 1).toUpperCase())}</span>`; }
function checkList(items) {
  const icon = { ok: 'check-circle', bad: 'x-circle', warn: 'alert' };
  const word = { ok: 'Успешно', bad: 'Ошибка', warn: 'Внимание' };
  return `<div class="tr-checks">` + items.map(it => `<div class="tr-check-row tr-check-row--${it.tone || 'ok'}">${I(icon[it.tone || 'ok'], 20)}<span class="tr-sr">${word[it.tone || 'ok']}: </span><div>${it.text}${it.detail ? `<small>${it.detail}</small>` : ''}</div></div>`).join('') + `</div>`;
}
function skeleton(n) { return Array.from({ length: n || 3 }, () => '<div class="tr-skel"></div>').join(''); }

/* ---------------------------------------------------------------------- */
/* Shell: top bar, bottom bar, toast, sheets                              */
/* ---------------------------------------------------------------------- */

const app = document.getElementById('app');
const topbar = document.getElementById('topbar');
const topTitle = document.getElementById('topTitle');
const topSub = document.getElementById('topSub');
const topActions = document.getElementById('topActions');
const backBtn = document.getElementById('backBtn');
const bottom = document.getElementById('bottom');
backBtn.innerHTML = I('chevron-left', 22);

function setTop(o) {
  topbar.className = 'tr-topbar' + (o.large ? ' tr-topbar--large' : '');
  topTitle.textContent = o.title || '';
  topSub.textContent = o.subtitle || '';
  topSub.hidden = !o.subtitle;
  backBtn.hidden = !o.back;
  topActions.innerHTML = o.actions || '';
  document.title = o.title || 'Хелпдеск';
}
function setBottom(html) { bottom.innerHTML = html || ''; }

let toastTimer = null, toastUndo = null;
function toast(msg, o) {
  o = o || {};
  const el = document.getElementById('toast');
  toastUndo = o.undo || null;
  el.className = 'tr-toast' + (o.error ? ' tr-toast--error' : '');
  el.innerHTML = `${I(o.error ? 'alert' : 'check-circle', 20)}<span class="tr-toast__msg">${esc(msg)}</span>` +
    (toastUndo ? `<button type="button" class="tr-btn tr-btn--ghost tr-btn--sm" data-act="toast-undo">Отменить</button>` : '');
  el.hidden = false;
  haptic(o.error ? 'error' : 'success');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; toastUndo = null; }, o.error ? 6000 : toastUndo ? 6000 : 3000);
}
function hideToast() { document.getElementById('toast').hidden = true; toastUndo = null; clearTimeout(toastTimer); }

// Sheet: the only modal. Dismissed via the close button, the scrim, Escape; never has a "Cancel" button.
function showSheet(title, bodyHtml, footerHtml, onMount) {
  const root = document.getElementById('modalRoot');
  root.innerHTML = `<div class="tr-scrim" data-scrim><div class="tr-sheet" role="dialog" aria-modal="true" aria-label="${esc(title)}">
    <div class="tr-sheet__grab"></div>
    <div class="tr-sheet__head"><h2 class="tr-sheet__title">${esc(title)}</h2>${ibtn('x', 'Закрыть', { id: 'sheetClose' })}</div>
    <div class="tr-sheet__body">${bodyHtml}</div>
    ${footerHtml ? `<div class="tr-sheet__foot">${footerHtml}</div>` : ''}</div></div>`;
  root.querySelector('[data-scrim]').addEventListener('mousedown', e => { if (e.target.dataset.scrim !== undefined) closeSheet(); });
  root.querySelector('#sheetClose').addEventListener('click', closeSheet);
  if (onMount) onMount(root);
  return root;
}
function closeSheet() { document.getElementById('modalRoot').innerHTML = ''; }
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeSheet(); });

// openMenu: a sheet with a list of actions. items: {label, icon, hint, danger, checked, disabled, onSelect} or {divider:true}.
function openMenu(title, items) {
  const body = `<div class="tr-menu" role="menu">` + items.map((it, i) => it.divider ? '<div class="tr-menu__sep" role="separator"></div>' :
    `<button type="button" role="menuitem" class="tr-menu__item${it.danger ? ' tr-menu__item--danger' : ''}" data-i="${i}"${it.checked ? ' aria-checked="true"' : ''}${it.disabled ? ' disabled' : ''}>` +
    `${it.icon ? I(it.icon, 20) : ''}${esc(it.label)}${it.hint ? `<small>${esc(it.hint)}</small>` : ''}${it.checked ? I('check', 18) : ''}</button>`).join('') + `</div>`;
  showSheet(title, body, '', root => root.querySelectorAll('[data-i]').forEach(b => b.addEventListener('click', () => {
    const it = items[Number(b.dataset.i)];
    closeSheet();
    if (it && it.onSelect) it.onSelect();
  })));
}

// confirmSheet resolves true only for the single destructive/confirming button; dismissing resolves false.
function confirmSheet(title, text, okLabel, danger) {
  return new Promise(resolve => {
    let done = false;
    const finish = v => { if (!done) { done = true; resolve(v); } };
    showSheet(title, `<p style="margin:0">${esc(text)}</p>`, btn(okLabel, { v: danger ? 'danger-solid' : 'primary', block: true, id: 'confirmOk' }), root => {
      root.querySelector('#confirmOk').addEventListener('click', () => { closeSheet(); finish(true); });
      new MutationObserver((_, obs) => { if (!root.querySelector('#confirmOk')) { obs.disconnect(); finish(false); } }).observe(root, { childList: true });
    });
  });
}

async function withBusy(el, fn) {
  if (el) el.disabled = true;
  try { await fn(); }
  catch (e) { toast(e.message || String(e), { error: true }); }
  finally { if (el && el.isConnected) el.disabled = false; }
}

/* ---------------------------------------------------------------------- */
/* Delegated events                                                       */
/* ---------------------------------------------------------------------- */

const ACT = {};   // data-act handlers: (el, event) => void
document.addEventListener('click', e => {
  const el = e.target.closest('[data-act]');
  if (!el || el.disabled) return;
  if (state.longPressed) { state.longPressed = false; return; }
  const fn = ACT[el.dataset.act];
  if (fn) fn(el, e);
});
ACT['toast-undo'] = () => { const fn = toastUndo; hideToast(); if (fn) fn(); };
ACT['link'] = (el, e) => { e.preventDefault(); openLink(el.dataset.link); };

// Segmented controls: <div data-seg="name"> → SEG[name](value)
const SEG = {};
document.addEventListener('click', e => {
  const b = e.target.closest('[data-seg] button');
  if (!b) return;
  const fn = SEG[b.parentElement.dataset.seg];
  if (fn) fn(b.dataset.value);
});
