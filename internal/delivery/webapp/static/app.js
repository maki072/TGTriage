'use strict';

/* ---------------------------------------------------------------------- */
/* Telegram Mini App bootstrap                                            */
/* ---------------------------------------------------------------------- */

const tg = window.Telegram && window.Telegram.WebApp ? window.Telegram.WebApp : null;
const inTelegram = !!(tg && tg.initData);

if (tg) {
  tg.ready();
  tg.expand();
  applyTheme();
  tg.onEvent('themeChanged', applyTheme);
}
if (!inTelegram) document.getElementById('devBanner').hidden = false;

function applyTheme() {
  const p = tg.themeParams || {};
  const root = document.documentElement.style;
  const map = {
    '--bg': p.bg_color, '--secondary-bg': p.secondary_bg_color || p.bg_color, '--text': p.text_color,
    '--hint': p.hint_color, '--link': p.link_color, '--button': p.button_color, '--button-text': p.button_text_color,
  };
  Object.entries(map).forEach(([k, v]) => { if (v) root.setProperty(k, v); });
  if (tg.setHeaderColor) try { tg.setHeaderColor(p.secondary_bg_color ? 'secondary_bg_color' : 'bg_color'); } catch (e) {}
  if (tg.setBackgroundColor) try { tg.setBackgroundColor(p.bg_color || '#f2f2f7'); } catch (e) {}
}

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
/* API helper                                                             */
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
/* State & navigation                                                     */
/* ---------------------------------------------------------------------- */

const state = {
  me: null,
  tab: null,
  view: null,            // {type: 'task'|'user', id}
  back: [],              // view stack
  tasksFilter: { status: 'act', priority: 'all', scope: 'all', bot: 'all', offset: 0 },
  historyFilter: { status: 'done', priority: 'all', scope: 'all', bot: 'all', offset: 0 },
  dialogsFilter: { filter: 'awaiting', q: '', bot: 'all', offset: 0 },
  settings: null,
  chainDraft: null,
  chainDirty: false,
  bots: [],               // additional bots (owner only) — [] means none configured
  openGroups: new Set(['helpdesk']),
};

const PAGE_SIZE = 20;
const app = document.getElementById('app');
const topTitle = document.getElementById('topTitle');
const backBtn = document.getElementById('backBtn');

backBtn.addEventListener('click', goBack);
document.getElementById('refreshBtn').addEventListener('click', () => render());
if (tg && tg.BackButton) tg.BackButton.onClick(goBack);

function isOwner() { return state.me && state.me.role === 'owner'; }

function tabsForMe() {
  const tabs = [];
  if (!isOwner() || state.me.helpdesk_enabled) tabs.push(['dialogs', '💬', 'Диалоги']);
  tabs.push(['tasks', isOwner() ? '🗂' : '🎫', isOwner() ? 'Задачи' : 'Тикеты']);
  tabs.push(['history', '🕘', 'История']);
  if (isOwner()) tabs.push(['settings', '⚙️', 'Настройки']);
  return tabs;
}

function buildTabbar() {
  const bar = document.getElementById('tabbar');
  bar.innerHTML = tabsForMe().map(([id, icon, label]) =>
    `<button class="tab" data-tab="${id}"><span class="tab-icon">${icon}</span><span>${label}</span></button>`).join('');
  bar.querySelectorAll('.tab').forEach(btn => btn.addEventListener('click', () => {
    state.view = null; state.back = []; switchTab(btn.dataset.tab);
  }));
}

function switchTab(tab) {
  state.tab = tab;
  document.querySelectorAll('.tab').forEach(b => b.classList.toggle('active', b.dataset.tab === tab));
  render();
}

function openView(v) {
  if (state.view) state.back.push(state.view);
  state.view = v;
  render();
  window.scrollTo(0, 0);
}

function goBack() {
  if (!state.view) return;
  state.view = state.back.pop() || null;
  render();
}

function render() {
  const detail = state.view != null;
  backBtn.hidden = !detail;
  if (tg && tg.BackButton) { if (detail) tg.BackButton.show(); else tg.BackButton.hide(); }
  if (detail) {
    if (state.view.type === 'task') { topTitle.textContent = 'Задача #' + state.view.id; renderTaskDetail(state.view.id); }
    else { topTitle.textContent = 'Диалог'; renderDialog(state.view.id, state.view.bot); }
    return;
  }
  switch (state.tab) {
    case 'dialogs': topTitle.textContent = 'Диалоги'; renderDialogs(); break;
    case 'tasks': topTitle.textContent = isOwner() ? 'Задачи' : 'Тикеты'; renderTaskList('tasks'); break;
    case 'history': topTitle.textContent = 'История'; renderTaskList('history'); break;
    case 'settings': topTitle.textContent = 'Настройки'; renderSettings(); break;
  }
}

/* ---------------------------------------------------------------------- */
/* Helpers                                                                */
/* ---------------------------------------------------------------------- */

function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function fmtDT(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  const now = new Date();
  const sameDay = d.toDateString() === now.toDateString();
  if (sameDay) return d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
  return d.toLocaleString('ru-RU', {
    day: '2-digit', month: '2-digit', year: d.getFullYear() === now.getFullYear() ? undefined : '2-digit',
    hour: '2-digit', minute: '2-digit',
  });
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

const PRIORITY_LABEL = { critical: 'Критический', high: 'Высокий', medium: 'Средний', low: 'Низкий' };
const PRIORITY_EMOJI = { critical: '🔴', high: '🟠', medium: '🟡', low: '🟢' };
const PRIORITY_OPTIONS = [['low', 'Низкая'], ['medium', 'Средняя'], ['high', 'Высокая'], ['critical', 'Критическая']];
const IMPORTANCE_LABEL = { critical: 'Критическая', high: 'Высокая', medium: 'Средняя', low: 'Низкая' };
const IMPORTANCE_EMOJI = { critical: '⭐️⭐️⭐️', high: '⭐️⭐️', medium: '⭐️', low: '' };
const CATEGORY_LABEL = { bug: '🐞 Баг', help_request: '🆘 Помощь', task: '📌 Задача', question: '❓ Вопрос', deadline: '⏳ Дедлайн', agreement: '🤝 Договорённость', other: '📎 Другое' };
const STATUS_LABEL = { new: '🆕 Новая', in_progress: '👀 В работе', snoozed: '⏰ Отложена', done: '✅ Завершена', false_positive: '🗑 Ошибка' };
const STRATEGY_LABEL = { confirm: 'подтверждение', clarify: 'уточняющий вопрос', decline: 'отказ', none: 'ответ' };

function toast(msg, isError) {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.classList.toggle('error', !!isError);
  el.hidden = false;
  haptic(isError ? 'error' : 'success');
  clearTimeout(toast._t);
  toast._t = setTimeout(() => { el.hidden = true; }, 2800);
}

function showModal(html, onMount) {
  const root = document.getElementById('modalRoot');
  root.innerHTML = `<div class="modal-backdrop" id="modalBackdrop"><div class="modal-sheet">${html}</div></div>`;
  document.getElementById('modalBackdrop').addEventListener('click', e => { if (e.target.id === 'modalBackdrop') closeModal(); });
  if (onMount) onMount(root);
}
function closeModal() { document.getElementById('modalRoot').innerHTML = ''; }

function confirmModal(title, text, okLabel, danger) {
  return new Promise(resolve => {
    showModal(`<h3>${esc(title)}</h3><p class="hint-text" style="margin:0">${esc(text)}</p>
      <div class="actions"><button class="btn ${danger ? 'danger' : 'primary'} full" id="mOk">${esc(okLabel)}</button>
      <button class="btn ghost full" id="mNo">Отмена</button></div>`, root => {
      root.querySelector('#mOk').addEventListener('click', () => { closeModal(); resolve(true); });
      root.querySelector('#mNo').addEventListener('click', () => { closeModal(); resolve(false); });
    });
  });
}

async function withBusy(btn, fn) {
  if (btn) btn.disabled = true;
  try { await fn(); }
  catch (e) { toast(e.message || String(e), true); }
  finally { if (btn) btn.disabled = false; }
}

async function loadBots() {
  if (!isOwner()) return state.bots;
  try { state.bots = (await api('GET', '/api/bots')).items || []; } catch (e) { /* non-fatal */ }
  return state.bots;
}

// botChips returns the ?bot= filter chips — only meaningful (and only shown) once at least one
// additional bot exists, so a single-bot setup stays exactly as compact as before.
function botChips() {
  const opts = [['all', 'Все боты'], ['0', '🏠 Основной']];
  state.bots.forEach(b => opts.push([String(b.id), (b.active ? '' : '⏸ ') + (b.label || ('#' + b.id))]));
  return opts;
}

function chipRow(name, options, active) {
  return `<div class="chiprow" data-chips="${name}">` +
    options.map(([v, label]) => `<button class="chip${v === active ? ' active' : ''}" data-value="${esc(v)}">${esc(label)}</button>`).join('') +
    `</div>`;
}
function wireChips(name, onPick) {
  document.querySelectorAll(`[data-chips="${name}"] .chip`).forEach(btn => btn.addEventListener('click', () => onPick(btn.dataset.value)));
}

/* ---------------------------------------------------------------------- */
/* Dialogs (helpdesk users)                                               */
/* ---------------------------------------------------------------------- */

async function renderDialogs() {
  const f = state.dialogsFilter;
  if (!state.me.helpdesk) {
    app.innerHTML = `<div class="card empty">🎧 Хелпдеск ${state.me.helpdesk_enabled ? 'включён, но не указана супергруппа' : 'выключен'}.` +
      (isOwner() ? `<br><br><button class="btn primary" id="goSettings">Открыть настройки</button>` : '') + `</div>`;
    const b = document.getElementById('goSettings');
    if (b) b.addEventListener('click', () => { state.openGroups.add('helpdesk'); switchTab('settings'); });
    return;
  }
  const showBotFilter = isOwner() && state.bots.length > 0;
  app.innerHTML = (showBotFilter ? chipRow('bot', botChips(), f.bot) : '') +
    chipRow('dfilter', [['awaiting', '⏳ Ждут ответа'], ['all', 'Все']], f.filter) +
    `<input class="input search" id="dq" type="search" placeholder="Поиск по имени, @username или ID" value="${esc(f.q)}">` +
    `<div id="listBody" class="loading">Загрузка…</div>`;
  if (showBotFilter) wireChips('bot', v => { f.bot = v; f.offset = 0; renderDialogs(); });
  wireChips('dfilter', v => { f.filter = v; f.offset = 0; renderDialogs(); });
  const q = document.getElementById('dq');
  q.addEventListener('change', () => { f.q = q.value.trim(); f.offset = 0; renderDialogs(); });
  try {
    const data = await api('GET', `/api/helpdesk/users?filter=${f.filter}&q=${encodeURIComponent(f.q)}&bot=${f.bot}&limit=${PAGE_SIZE}&offset=${f.offset}`);
    const body = document.getElementById('listBody');
    body.classList.remove('loading');
    if (!data.items.length) {
      body.innerHTML = `<div class="empty">${f.filter === 'awaiting' ? 'Все ответы отправлены 🎉' : 'Пока никто не писал'}</div>`;
      return;
    }
    body.innerHTML = data.items.map(dialogCard).join('') + pager(data, 'dpage');
    body.querySelectorAll('[data-user]').forEach(el => el.addEventListener('click', () =>
      openView({ type: 'user', id: Number(el.dataset.user), bot: Number(el.dataset.bot || 0) })));
    body.querySelectorAll('[data-dpage]').forEach(el => el.addEventListener('click', () => { f.offset = Number(el.dataset.dpage); renderDialogs(); }));
  } catch (e) {
    document.getElementById('listBody').innerHTML = `<div class="empty">⚠️ ${esc(e.message)}</div>`;
  }
}

function userBadges(u) {
  const b = [];
  if (u.awaiting_since) b.push(`<span class="badge warn">⏳ ждёт ${waitedFor(u.awaiting_since)}</span>`);
  if (u.blocked) b.push(`<span class="badge bad">🚫 заблокировал бота</span>`);
  if (u.topic_closed) b.push(`<span class="badge">🔒 тема закрыта</span>`);
  if (u.source) b.push(`<span class="badge">🔗 ${esc(u.source)}</span>`);
  return b.join(' ');
}

function botLabelFor(botID) {
  if (!botID) return '';
  const b = state.bots.find(x => x.id === botID);
  return b ? (b.label || ('#' + b.id)) : ('#' + botID);
}

function dialogCard(u) {
  const botTag = state.bots.length ? ` <span class="muted small">· ${esc(botLabelFor(u.bot_id) || '🏠')}</span>` : '';
  return `<div class="card task-card" data-user="${u.user_id}" data-bot="${u.bot_id}">
    <div class="task-top"><span class="avatar">${esc((u.name || '?').slice(0, 1).toUpperCase())}</span>
      <span class="task-title">${esc(u.name || ('id' + u.user_id))}${u.username ? ` <span class="muted">@${esc(u.username)}</span>` : ''}${botTag}</span>
      <span class="muted small">${fmtDT(u.last_message_at)}</span></div>
    <div class="task-meta">${userBadges(u)}</div>
  </div>`;
}

async function renderDialog(userID, botID) {
  app.innerHTML = `<div class="loading">Загрузка…</div>`;
  let d;
  try { d = await api('GET', `/api/helpdesk/users/${userID}?bot=${botID || 0}`); }
  catch (e) { app.innerHTML = `<div class="empty">⚠️ ${esc(e.message)}</div>`; return; }
  const u = d.user;
  topTitle.textContent = u.name || 'Диалог';
  const open = d.tickets.filter(t => t.status === 'new' || t.status === 'in_progress' || t.status === 'snoozed');
  app.innerHTML = `
  <div class="card">
    <div class="task-top"><span class="avatar big">${esc((u.name || '?').slice(0, 1).toUpperCase())}</span>
      <div style="flex:1"><div class="task-title">${esc(u.name)}</div>
      <div class="muted small">${u.username ? `<a class="plain" href="#" data-link="${esc(u.profile_url)}">@${esc(u.username)}</a> · ` : ''}ID ${u.user_id}${u.language_code ? ' · ' + esc(u.language_code) : ''}</div></div></div>
    <div class="task-meta" style="margin-top:8px">${userBadges(u)}</div>
    <div class="actions">
      ${u.topic_url ? `<button class="btn" data-link="${esc(u.topic_url)}">💬 Тема в группе</button>` : ''}
      <button class="btn" id="topicToggle">${u.topic_closed ? '🔓 Открыть тему' : '🔒 Закрыть тему'}</button>
      <button class="btn" id="makeTicket">🎫 Создать тикет</button>
    </div>
  </div>
  ${d.tickets.length ? `<div class="section-title">Тикеты (${open.length} открыто)</div>` + d.tickets.map(taskCard).join('') : ''}
  <div class="section-title">Переписка</div>
  <div class="card chat" id="chat">${d.messages.length ? d.messages.map(bubble).join('') : '<div class="empty">Сообщений нет</div>'}</div>
  <div class="card detail-section">
    <textarea class="input" id="hdReply" placeholder="Ответ пользователю — уйдёт от имени бота"></textarea>
    <div class="actions"><button class="btn primary full" id="hdSend">📤 Отправить</button></div>
  </div>`;
  const chat = document.getElementById('chat');
  chat.scrollTop = chat.scrollHeight;
  app.querySelectorAll('[data-link]').forEach(el => el.addEventListener('click', e => { e.preventDefault(); openLink(el.dataset.link); }));
  app.querySelectorAll('[data-open-task]').forEach(el => el.addEventListener('click', () => openView({ type: 'task', id: Number(el.dataset.openTask) })));
  document.getElementById('topicToggle').addEventListener('click', e => withBusy(e.target, async () => {
    await api('POST', `/api/helpdesk/users/${userID}/topic?bot=${botID || 0}`, { closed: !u.topic_closed });
    toast(u.topic_closed ? '🔓 Тема открыта' : '🔒 Тема закрыта');
    renderDialog(userID, botID);
  }));
  document.getElementById('makeTicket').addEventListener('click', e => withBusy(e.target, async () => {
    toast('⏳ Оформляю тикет…');
    const t = await api('POST', `/api/helpdesk/users/${userID}/ticket?bot=${botID || 0}`);
    toast('🎫 Тикет #' + t.id + ' создан');
    openView({ type: 'task', id: t.id });
  }));
  document.getElementById('hdSend').addEventListener('click', e => {
    const text = document.getElementById('hdReply').value.trim();
    if (!text) { toast('Введите текст', true); return; }
    withBusy(e.target, async () => {
      await api('POST', `/api/helpdesk/users/${userID}/reply?bot=${botID || 0}`, { text });
      toast('📤 Отправлено');
      renderDialog(userID, botID);
    });
  });
}

function bubble(m) {
  return `<div class="bubble ${m.outgoing ? 'out' : 'in'}"><div>${esc(m.text)}</div><span class="time">${fmtDT(m.sent_at)}</span></div>`;
}

/* ---------------------------------------------------------------------- */
/* Task list (tabs: tasks / history)                                      */
/* ---------------------------------------------------------------------- */

const STATUS_CHIPS_TASKS = [['act', 'Активные'], ['new', 'Новые'], ['wrk', 'В работе'], ['snz', 'Отложенные']];
const STATUS_CHIPS_HISTORY = [['done', 'Завершённые'], ['fp', 'Ошибки'], ['all', 'Все']];
const PRIO_CHIPS = [['all', 'Все'], ['urg', '🔥 Срочные'], ['crit', '🔴'], ['high', '🟠'], ['med', '🟡'], ['low', '🟢']];
const SCOPE_CHIPS = [['all', 'Все'], ['helpdesk', '🎫 Тикеты'], ['personal', '👤 Личные']];

async function renderTaskList(tab) {
  const filter = tab === 'tasks' ? state.tasksFilter : state.historyFilter;
  const chips = tab === 'tasks' ? STATUS_CHIPS_TASKS : STATUS_CHIPS_HISTORY;
  const scope = isOwner() ? filter.scope : 'helpdesk';

  let overviewHtml = '';
  if (tab === 'tasks') {
    try { overviewHtml = renderOverview(await api('GET', `/api/overview?scope=${scope}&bot=${filter.bot}`)); } catch (e) { /* non-fatal */ }
  }
  const showBotFilter = isOwner() && state.bots.length > 0;
  app.innerHTML = overviewHtml +
    (isOwner() ? chipRow('scope', SCOPE_CHIPS, filter.scope) : '') +
    (showBotFilter ? chipRow('bot', botChips(), filter.bot) : '') +
    chipRow('sfilter', chips, filter.status) +
    chipRow('pfilter', PRIO_CHIPS, filter.priority) +
    `<div id="listBody" class="loading">Загрузка…</div>`;

  wireChips('scope', v => { filter.scope = v; filter.offset = 0; renderTaskList(tab); });
  if (showBotFilter) wireChips('bot', v => { filter.bot = v; filter.offset = 0; renderTaskList(tab); });
  wireChips('sfilter', v => { filter.status = v; filter.offset = 0; renderTaskList(tab); });
  wireChips('pfilter', v => { filter.priority = v; filter.offset = 0; renderTaskList(tab); });
  app.querySelectorAll('[data-go-dialogs]').forEach(el => el.addEventListener('click', () => switchTab('dialogs')));

  try {
    const data = await api('GET', `/api/tasks?scope=${scope}&bot=${filter.bot}&status=${filter.status}&priority=${filter.priority}&limit=${PAGE_SIZE}&offset=${filter.offset}`);
    const body = document.getElementById('listBody');
    body.classList.remove('loading');
    if (!data.items || data.items.length === 0) { body.innerHTML = `<div class="empty">Здесь пусто 🎉</div>`; return; }
    body.innerHTML = data.items.map(taskCard).join('') + pager(data, 'page');
    body.querySelectorAll('[data-open-task]').forEach(el => el.addEventListener('click', () => openView({ type: 'task', id: Number(el.dataset.openTask) })));
    body.querySelectorAll('[data-page]').forEach(el => el.addEventListener('click', () => { filter.offset = Number(el.dataset.page); renderTaskList(tab); }));
  } catch (e) {
    document.getElementById('listBody').innerHTML = `<div class="empty">⚠️ ${esc(e.message)}</div>`;
  }
}

function renderOverview(o) {
  let banner = '';
  if (o.awaiting > 0) {
    banner += `<div class="conn-banner warn" data-go-dialogs>⏳ Ждут ответа: <b>${o.awaiting}</b> — открыть диалоги</div>`;
  }
  if (isOwner() && state.tasksFilter.scope !== 'helpdesk') {
    if (!o.connection) banner += `<div class="conn-banner muted">🔌 Telegram Business не подключён</div>`;
    else if (!o.connection.enabled) banner += `<div class="conn-banner bad">⏸ Business-подключение отключено</div>`;
    else if (!o.connection.can_reply) banner += `<div class="conn-banner bad">⚠️ Нет права отвечать за вас</div>`;
  }
  return banner + `<div class="overview">
    <div class="stat"><b>${o.new}</b><span>Новые</span></div>
    <div class="stat"><b>${o.in_progress}</b><span>В работе</span></div>
    <div class="stat"><b>${o.snoozed}</b><span>Отложено</span></div>
    <div class="stat ${o.overdue ? 'warn' : ''}"><b>${o.overdue}</b><span>Просрочено</span></div>
  </div>`;
}

function taskCard(t) {
  const meta = [`${t.helpdesk ? '🎫' : '👤'} ${esc(t.sender_name || '—')}`, CATEGORY_LABEL[t.category] || t.category];
  if (t.helpdesk && state.bots.length) meta.push(esc(botLabelFor(t.bot_id) || '🏠'));
  if (t.status !== 'new' && t.status !== 'in_progress') meta.push(`<span class="badge status-${t.status}">${STATUS_LABEL[t.status] || t.status}</span>`);
  if (t.deadline) meta.push(`📅 ${fmtDT(t.deadline)}` + (t.overdue ? ` <span class="badge overdue">просрочено</span>` : ''));
  if (t.status === 'snoozed' && t.snooze_until) meta.push(`⏰ до ${fmtDT(t.snooze_until)}`);
  return `<div class="card task-card" data-open-task="${t.id}">
    <div class="task-top"><span class="dot ${t.priority}"></span>
      <span class="task-title">#${t.id} ${esc(t.title || '(без заголовка)')}</span></div>
    <div class="task-meta">${meta.join(' · ')}</div>
  </div>`;
}

function pager(data, attr) {
  const hasPrev = data.offset > 0;
  const hasNext = data.offset + data.items.length < data.total;
  if (!hasPrev && !hasNext) return '';
  return `<div class="actions">
    ${hasPrev ? `<button class="btn ghost" data-${attr}="${Math.max(0, data.offset - data.limit)}">◀️ Назад</button>` : ''}
    <span class="btn ghost" style="pointer-events:none">${data.offset + 1}–${data.offset + data.items.length} из ${data.total}</span>
    ${hasNext ? `<button class="btn ghost" data-${attr}="${data.offset + data.limit}">Вперёд ▶️</button>` : ''}
  </div>`;
}

/* ---------------------------------------------------------------------- */
/* Task detail                                                            */
/* ---------------------------------------------------------------------- */

async function renderTaskDetail(id) {
  app.innerHTML = `<div class="loading">Загрузка…</div>`;
  let t;
  try { t = await api('GET', `/api/tasks/${id}`); }
  catch (e) { app.innerHTML = `<div class="empty">⚠️ ${esc(e.message)}</div>`; return; }
  topTitle.textContent = (t.helpdesk ? 'Тикет #' : 'Задача #') + t.id;
  app.innerHTML = taskDetailHtml(t);
  app.querySelectorAll('[data-act]').forEach(btn => btn.addEventListener('click', () => handleTaskAction(t, btn.dataset.act, btn)));
  app.querySelectorAll('[data-link]').forEach(el => el.addEventListener('click', e => { e.preventDefault(); openLink(el.dataset.link); }));
}

function taskDetailHtml(t) {
  const hu = t.helpdesk_user;
  const senderLine = t.sender_username
    ? `<a class="plain" href="#" data-link="${esc(t.profile_url)}">${esc(t.sender_name)}</a> (@${esc(t.sender_username)})`
    : esc(t.sender_name || '—');
  return `
  <div class="card">
    <div class="task-top"><span class="dot ${t.priority}"></span><span class="task-title">${esc(t.title || '(без заголовка)')}</span></div>
    <div class="task-meta" style="margin-top:6px">
      <span class="badge status-${t.status}">${STATUS_LABEL[t.status] || t.status}</span>
      <span class="badge">${PRIORITY_EMOJI[t.priority] || ''} ${PRIORITY_LABEL[t.priority] || t.priority}</span>
      ${t.importance && t.importance !== 'medium' ? `<span class="badge">${IMPORTANCE_EMOJI[t.importance] || ''} важность: ${IMPORTANCE_LABEL[t.importance] || t.importance}</span>` : ''}
      <span class="badge">${CATEGORY_LABEL[t.category] || t.category}</span>
      ${t.helpdesk ? '<span class="badge">🎫 Хелпдеск</span>' : ''}
      ${t.forwarded ? '<span class="badge">📨 Переслано</span>' : ''}
      ${t.merged_into ? `<span class="badge">🔀 объединено в #${t.merged_into}</span>` : ''}
    </div>
    <div class="kv" style="margin-top:10px">
      <div><span>${t.helpdesk ? 'Пользователь' : 'От'}: </span>${senderLine}</div>
      <div><span>Создано: </span>${fmtDT(t.created_at)}</div>
      ${t.deadline ? `<div><span>Срок: </span>${fmtDT(t.deadline)}${t.overdue ? ' ⚠️ просрочено' : ''}</div>` : ''}
      ${t.status === 'snoozed' && t.snooze_until ? `<div><span>Отложено до: </span>${fmtDT(t.snooze_until)}</div>` : ''}
      ${t.remind_at ? `<div><span>🔔 Напомнить: </span>${fmtDT(t.remind_at)}</div>` : ''}
      ${hu && hu.source ? `<div><span>Источник: </span>${esc(hu.source)}</div>` : ''}
    </div>
    ${hu ? `<div class="task-meta" style="margin-top:8px">${userBadges(hu)}</div>
    <div class="actions">
      <button class="btn" data-act="dialog">💬 Переписка</button>
      ${hu.topic_url ? `<button class="btn" data-link="${esc(hu.topic_url)}">↗️ Тема в группе</button>` : ''}
    </div>` : ''}
  </div>
  ${t.description ? `<div class="card detail-section"><h3>Суть</h3><p>${esc(t.description)}</p></div>` : ''}
  ${t.source_text ? `<div class="card detail-section"><h3>Исходный текст</h3><div class="quote">${esc(t.source_text)}</div></div>` : ''}
  ${t.draft_reply && !t.reply_sent_at ? `<div class="card detail-section"><h3>Черновик ответа (${STRATEGY_LABEL[t.reply_strategy] || 'ответ'})</h3><p>${esc(t.draft_reply)}</p></div>` : ''}
  ${t.reply_sent_at ? `<div class="card detail-section"><h3>Ответ отправлен · ${fmtDT(t.reply_sent_at)}</h3><p>${esc(t.reply_text)}</p></div>` : ''}
  ${t.provider ? `<div class="card detail-section muted small">🤖 ${esc(t.provider)} · ${esc(t.model)} · уверенность ${Math.round(t.confidence * 100)}%</div>` : ''}
  <div id="replyBox"></div>
  <div id="snoozeBox"></div>
  <div id="editBox"></div>
  <div id="remindBox"></div>
  <div id="mergeBox"></div>
  <div class="card actions">${isOpen(t.status) ? actionButtons(t) : reopenButtons(t)}</div>`;
}

function isOpen(status) { return status === 'new' || status === 'in_progress' || status === 'snoozed'; }

function actionButtons(t) {
  let html = '';
  if (!t.forwarded) {
    if (t.draft_reply) html += `<button class="btn primary full" data-act="draft">🚀 Ответить черновиком</button>`;
    html += `<button class="btn" data-act="reply-open">✏️ Свой ответ</button>`;
  }
  if (t.status !== 'in_progress') html += `<button class="btn" data-act="work">👀 В работу</button>`;
  html += `<button class="btn" data-act="close">✅ Закрыть</button>`;
  html += `<button class="btn" data-act="snooze-open">⏰ Отложить</button>`;
  html += `<button class="btn" data-act="edit-open">✏️ Редактировать</button>`;
  html += `<button class="btn" data-act="remind-open">🔔 Напомнить отдельно</button>`;
  html += `<button class="btn" data-act="merge-open">🔀 Объединить</button>`;
  html += `<button class="btn danger" data-act="fp">🗑 Ошибка</button>`;
  return html;
}
function reopenButtons(t) {
  return `<button class="btn primary" data-act="reopen">♻️ Вернуть в работу</button>` +
    (t.forwarded ? '' : `<button class="btn" data-act="reply-open">✏️ Написать</button>`);
}

async function handleTaskAction(t, action, btn) {
  const post = (path, body, msg) => withBusy(btn, async () => { await api('POST', `/api/tasks/${t.id}/${path}`, body); toast(msg); renderTaskDetail(t.id); });
  switch (action) {
    case 'draft': return post('draft', undefined, '🚀 Черновик отправлен');
    case 'work': return post('status', { status: 'in_progress' }, '👀 Взято в работу');
    case 'fp': return post('status', { status: 'false_positive' }, '🗑 Отмечено как ошибка');
    case 'reopen': return post('status', { status: 'new' }, '♻️ Возвращено в работу');
    case 'close': return handleClose(t);
    case 'reply-open': return openReplyBox(t);
    case 'snooze-open': return openSnoozeBox(t);
    case 'edit-open': return openEditBox(t);
    case 'remind-open': return openRemindBox(t);
    case 'merge-open': return openMergeBox(t);
    case 'dialog': return openView({ type: 'user', id: t.chat_id, bot: t.bot_id });
  }
}

const HELPDESK_DONE_TEXT = 'Ваше обращение решено. Если остались вопросы — просто напишите нам.';

async function handleClose(t) {
  const closeWith = async body => {
    closeModal();
    try { await api('POST', `/api/tasks/${t.id}/close`, body); toast('✅ Закрыто'); renderTaskDetail(t.id); }
    catch (e) { toast(e.message, true); }
  };
  if (t.helpdesk && !t.forwarded) {
    showModal(`<h3>Закрыть тикет?</h3>
      <textarea class="input" id="closeText">${esc(HELPDESK_DONE_TEXT)}</textarea>
      <div class="actions">
        <button class="btn primary full" id="closeYes">✅ Закрыть и отправить пользователю</button>
        <button class="btn full" id="closeNo">Просто закрыть</button>
        <button class="btn ghost full" id="closeCancel">Отмена</button>
      </div>`, root => {
      root.querySelector('#closeYes').addEventListener('click', () => closeWith({ text: root.querySelector('#closeText').value.trim() || HELPDESK_DONE_TEXT }));
      root.querySelector('#closeNo').addEventListener('click', () => closeWith({ send_message: false }));
      root.querySelector('#closeCancel').addEventListener('click', closeModal);
    });
    return;
  }
  let notify = false;
  if (isOwner() && !t.forwarded) {
    try { notify = (await loadSettings()).fields.find(f => f.key === 'task.notify_done_on_close').value; } catch (e) {}
  }
  if (!notify) return closeWith({ send_message: false });
  showModal(`<h3>Закрыть задачу?</h3>
    <p class="hint-text" style="margin:0">Отправить собеседнику «Готово!» перед закрытием?</p>
    <div class="actions">
      <button class="btn primary full" id="closeYes">✅ Закрыть + отправить «Готово!»</button>
      <button class="btn full" id="closeNo">Просто закрыть</button>
      <button class="btn ghost full" id="closeCancel">Отмена</button>
    </div>`, root => {
    root.querySelector('#closeYes').addEventListener('click', () => closeWith({ send_message: true }));
    root.querySelector('#closeNo').addEventListener('click', () => closeWith({ send_message: false }));
    root.querySelector('#closeCancel').addEventListener('click', closeModal);
  });
}

function openReplyBox(t) {
  const box = document.getElementById('replyBox');
  box.innerHTML = `<div class="card detail-section">
    <h3>${t.helpdesk ? 'Ответ пользователю (от имени бота)' : 'Ваш ответ собеседнику'}</h3>
    <textarea class="input" id="replyText" placeholder="Текст ответа…">${esc(t.draft_reply || '')}</textarea>
    <div class="actions"><button class="btn primary full" id="replySend">📤 Отправить</button></div>
  </div>`;
  box.scrollIntoView({ behavior: 'smooth', block: 'center' });
  document.getElementById('replySend').addEventListener('click', e => {
    const text = document.getElementById('replyText').value.trim();
    if (!text) { toast('Введите текст', true); return; }
    withBusy(e.target, async () => { await api('POST', `/api/tasks/${t.id}/reply`, { text }); toast('📤 Ответ отправлен'); renderTaskDetail(t.id); });
  });
}

const SNOOZE_PRESETS = [['30 мин', 30], ['1 час', 60], ['3 часа', 180], ['3 дня', 4320], ['Неделя', 10080]];

function openSnoozeBox(t) {
  const box = document.getElementById('snoozeBox');
  box.innerHTML = `<div class="card detail-section">
    <h3>На сколько отложить?</h3>
    <div class="chiprow">
      ${SNOOZE_PRESETS.map(([label, min]) => `<button class="chip" data-min="${min}">${label}</button>`).join('')}
      <button class="chip" id="snoozeTomorrow">Завтра 09:00</button>
    </div>
    <input type="datetime-local" class="input" id="snoozeCustom" style="margin-top:8px">
    <div class="actions"><button class="btn primary full" id="snoozeCustomBtn">⏰ Отложить до указанного времени</button></div>
  </div>`;
  box.scrollIntoView({ behavior: 'smooth', block: 'center' });
  const doSnooze = async untilISO => {
    try { await api('POST', `/api/tasks/${t.id}/snooze`, { until: untilISO }); toast('⏰ Отложено'); renderTaskDetail(t.id); }
    catch (e) { toast(e.message, true); }
  };
  box.querySelectorAll('[data-min]').forEach(btn => btn.addEventListener('click', () => doSnooze(new Date(Date.now() + Number(btn.dataset.min) * 60000).toISOString())));
  document.getElementById('snoozeTomorrow').addEventListener('click', () => { const d = new Date(); d.setDate(d.getDate() + 1); d.setHours(9, 0, 0, 0); doSnooze(d.toISOString()); });
  document.getElementById('snoozeCustomBtn').addEventListener('click', () => {
    const v = document.getElementById('snoozeCustom').value;
    if (!v) { toast('Укажите дату и время', true); return; }
    doSnooze(new Date(v).toISOString());
  });
}

function openEditBox(t) {
  const box = document.getElementById('editBox');
  box.innerHTML = `<div class="card detail-section">
    <h3>✏️ Редактировать задачу</h3>
    <input class="input" id="editTitle" placeholder="Заголовок" value="${esc(t.title)}">
    <textarea class="input" id="editDescription" placeholder="Суть задачи">${esc(t.description)}</textarea>
    <div class="setting-row"><div class="label">Срочность</div>
      <select class="input narrow" id="editPriority">${PRIORITY_OPTIONS.map(([v, l]) => `<option value="${v}"${v === t.priority ? ' selected' : ''}>${l}</option>`).join('')}</select></div>
    <div class="setting-row"><div class="label">Важность</div>
      <select class="input narrow" id="editImportance">${PRIORITY_OPTIONS.map(([v, l]) => `<option value="${v}"${v === (t.importance || 'medium') ? ' selected' : ''}>${l}</option>`).join('')}</select></div>
    <div class="actions"><button class="btn primary full" id="editSave">💾 Сохранить</button></div>
  </div>`;
  box.scrollIntoView({ behavior: 'smooth', block: 'center' });
  document.getElementById('editSave').addEventListener('click', e => {
    const title = document.getElementById('editTitle').value.trim();
    if (!title) { toast('Введите заголовок', true); return; }
    const body = {
      title, description: document.getElementById('editDescription').value.trim(),
      priority: document.getElementById('editPriority').value, importance: document.getElementById('editImportance').value,
    };
    withBusy(e.target, async () => { await api('POST', `/api/tasks/${t.id}/edit`, body); toast('💾 Сохранено'); renderTaskDetail(t.id); });
  });
}

const REMIND_PRESETS = [['+1 час', 60], ['+3 часа', 180], ['+1 сутки', 1440]];

function openRemindBox(t) {
  const box = document.getElementById('remindBox');
  box.innerHTML = `<div class="card detail-section">
    <h3>🔔 Напомнить об этой задаче отдельно</h3>
    <div class="chiprow">
      ${REMIND_PRESETS.map(([label, min]) => `<button class="chip" data-min="${min}">${label}</button>`).join('')}
    </div>
    <div class="actions" style="margin-top:8px">
      <input class="input narrow" type="number" min="1" id="remindN" placeholder="N" style="max-width:80px">
      <select class="input narrow" id="remindUnit"><option value="60">часов</option><option value="1440">суток</option></select>
      <button class="btn" id="remindManualBtn">Напомнить</button>
    </div>
    ${t.remind_at ? `<div class="actions"><button class="btn ghost full" id="remindClear">✖️ Отменить напоминание</button></div>` : ''}
  </div>`;
  box.scrollIntoView({ behavior: 'smooth', block: 'center' });
  const doRemind = async atISO => {
    try { await api('POST', `/api/tasks/${t.id}/remind`, { at: atISO }); toast('🔔 Напоминание установлено'); renderTaskDetail(t.id); }
    catch (e) { toast(e.message, true); }
  };
  box.querySelectorAll('[data-min]').forEach(btn => btn.addEventListener('click', () => doRemind(new Date(Date.now() + Number(btn.dataset.min) * 60000).toISOString())));
  document.getElementById('remindManualBtn').addEventListener('click', () => {
    const n = Number(document.getElementById('remindN').value);
    if (!n || n <= 0) { toast('Введите число', true); return; }
    const unit = Number(document.getElementById('remindUnit').value);
    doRemind(new Date(Date.now() + n * unit * 60000).toISOString());
  });
  const clearBtn = document.getElementById('remindClear');
  if (clearBtn) clearBtn.addEventListener('click', () => withBusy(clearBtn, async () => {
    await api('POST', `/api/tasks/${t.id}/remind`, { at: '' }); toast('✖️ Напоминание отменено'); renderTaskDetail(t.id);
  }));
}

function openMergeBox(t) {
  const box = document.getElementById('mergeBox');
  box.innerHTML = `<div class="card detail-section">
    <h3>🔀 Объединить с другой задачей</h3>
    <p class="hint-text" style="margin:0 0 8px">Все данные этой задачи перенесутся в задачу с указанным номером, а эта — закроется.</p>
    <input class="input narrow" type="number" min="1" id="mergeTarget" placeholder="Номер задачи, напр. 42">
    <div class="actions"><button class="btn primary full" id="mergeBtn">🔀 Объединить</button></div>
  </div>`;
  box.scrollIntoView({ behavior: 'smooth', block: 'center' });
  document.getElementById('mergeBtn').addEventListener('click', e => {
    const targetID = Number(document.getElementById('mergeTarget').value);
    if (!targetID || targetID <= 0) { toast('Введите номер задачи', true); return; }
    if (targetID === t.id) { toast('Нельзя объединить задачу с собой', true); return; }
    withBusy(e.target, async () => {
      await api('POST', `/api/tasks/${t.id}/merge`, { target_id: targetID });
      toast('🔀 Объединено в #' + targetID);
      openView({ type: 'task', id: targetID });
    });
  });
}

/* ---------------------------------------------------------------------- */
/* Settings (owner)                                                       */
/* ---------------------------------------------------------------------- */

async function loadSettings() {
  state.settings = await api('GET', '/api/settings');
  return state.settings;
}

const PROVIDERS = [['claude', 'Claude'], ['gemini', 'Gemini'], ['groq', 'Groq'], ['mistral', 'Mistral'], ['openrouter', 'OpenRouter']];
function providerLabel(p) { return (PROVIDERS.find(([id]) => id === p) || [p, p])[1]; }

async function renderSettings() {
  const scrollY = window.scrollY;
  if (!state.settings) app.innerHTML = `<div class="loading">Загрузка…</div>`;
  let s;
  try { s = await loadSettings(); }
  catch (e) { app.innerHTML = `<div class="empty">⚠️ ${esc(e.message)}</div>`; return; }
  if (!state.chainDirty || !state.chainDraft) state.chainDraft = chainFromSettings(s);
  app.innerHTML = settingsHtml(s);
  renderChain();
  setChainDirty(state.chainDirty);
  wireSettings(s);
  window.scrollTo(0, scrollY);
}

function field(s, key) { return s.fields.find(f => f.key === key); }

function settingsHtml(s) {
  const groups = s.groups.map(g => {
    let inner;
    if (g.id === 'ai') {
      const general = s.fields.filter(f => f.group === 'ai' && !PROVIDERS.some(([p]) => f.key.startsWith(`ai.${p}_`)));
      inner = aiChainHtml() + general.map(f => fieldHtml(s, f)).join('') +
        PROVIDERS.map(([p, label]) => `<details class="sub" data-group="ai-${p}" ${state.openGroups.has('ai-' + p) ? 'open' : ''}>
          <summary>${label}</summary>${s.fields.filter(f => f.key.startsWith(`ai.${p}_`)).map(f => fieldHtml(s, f)).join('')}</details>`).join('');
    } else if (g.id === 'bots') {
      inner = botsHtml();
    } else {
      inner = s.fields.filter(f => f.group === g.id).map(f => fieldHtml(s, f)).join('');
    }
    if (g.id === 'helpdesk') inner += `<button class="btn full" id="hdCheck" style="margin-top:10px">🔍 Проверить группу</button><div id="hdCheckResult"></div>`;
    if (g.id === 'backup') inner += `<div class="actions"><button class="btn primary full" id="backupRun">💾 Сделать бэкап сейчас</button></div><div id="backupList" class="loading">Загрузка…</div>`;
    return `<details class="card group" data-group="${g.id}" ${state.openGroups.has(g.id) ? 'open' : ''}>
      <summary class="section-title">${esc(g.title)}</summary>${inner}</details>`;
  }).join('');
  return groups + `<div class="card">
    <div class="section-title" style="margin-top:0">🛠 Система</div>
    <div class="hint-text">В .env остаются только токен бота, OWNER_ID, путь к БД, адрес веб-панели и SOCKS-прокси. Всё остальное хранится в базе.</div>
    <div class="actions">
      <button class="btn full" id="restartBtn">♻️ Перезапустить сервис</button>
      <button class="btn danger full" id="resetSettings">Сбросить настройки к .env</button>
    </div>
  </div>`;
}

function aiChainHtml() {
  return `<div class="hint-text">Ключи пробуются сверху вниз: если ключ не отвечает, берётся следующий.</div>
    <div id="chainBox"></div>
    <div class="actions"><button class="btn" id="chainAdd">＋ Добавить ключ</button><button class="btn primary" id="chainSave">💾 Сохранить</button></div>
    <button class="btn full" id="testProvider" style="margin-top:8px">🧪 Проверить все ключи</button>
    <div id="testResult"></div>`;
}

/* ---------------------------------------------------------------------- */
/* Bots (additional, per-organization support bots)                       */
/* ---------------------------------------------------------------------- */

const SENSITIVITY_OPTIONS = [['', 'Как в общих настройках'], ['low', 'Низкая'], ['medium', 'Средняя'], ['high', 'Высокая']];

const BOT_HD_FIELDS = [
  ['group_id', 'int', 'ID супергруппы', 'Супергруппа с включёнными темами, бот — админ с правом «Управление темами». ID вида -100…'],
  ['triage_enabled', 'bool', 'Автоматические тикеты', 'LLM анализирует сообщения пользователей и заводит тикеты'],
  ['about', 'text', 'О сервисе поддержки', 'Чем занимается эта организация — помогает модели отличать обращения от шума'],
  ['greeting_enabled', 'bool', 'Приветствие на /start', ''],
  ['greeting_text', 'text', 'Текст приветствия', ''],
  ['autoreply_enabled', 'bool', 'Автоответ на обращение', 'Отправляется на первое сообщение, пока оператор не ответил'],
  ['autoreply_text', 'text', 'Текст автоответа', ''],
  ['hours_enabled', 'bool', 'Рабочие часы', 'Вне рабочих часов уходит отдельный автоответ'],
  ['hours_start', 'time', 'Начало рабочего дня', ''],
  ['hours_end', 'time', 'Конец рабочего дня', ''],
  ['hours_days', 'days', 'Рабочие дни', ''],
  ['offhours_text', 'text', 'Автоответ вне рабочих часов', '{hours} заменяется на рабочие часы'],
  ['reminder_minutes', 'int', 'Напоминание о неотвеченных, мин', '0 — выключено'],
];

function botsHtml() {
  const bots = state.bots;
  return `<div class="hint-text">Отдельный бот и супергруппа на каждую организацию-клиента. Настройки ИИ ниже — общие на все боты; переопределить чувствительность можно у конкретного бота.</div>
    ${bots.length ? bots.map(botRowHtml).join('') : `<div class="empty" style="padding:14px 0">Дополнительных ботов пока нет</div>`}
    <div class="actions"><button class="btn primary full" id="botAddBtn">＋ Добавить бота</button></div>`;
}

function botRowHtml(b) {
  const groupOk = !!b.helpdesk.group_id;
  return `<div class="card bot-row" data-bot="${b.id}" style="margin-top:10px">
    <div class="bot-line">
      <input class="input bot-label" data-bot-field="label" value="${esc(b.label)}" placeholder="Название организации">
      <button class="switch${b.active ? ' on' : ''}" data-bot-active title="включён / выключен"><span class="knob"></span></button>
    </div>
    <div class="task-meta" style="margin-top:6px">
      ${b.username ? `<span class="muted small">@${esc(b.username)}</span>` : ''}
      <span class="badge${groupOk ? '' : ' warn'}">${groupOk ? '✅ группа настроена' : '⚠️ группа не указана'}</span>
    </div>
    <details class="sub" ${state.openGroups.has('bot-' + b.id) ? 'open' : ''} data-bot-adv="${b.id}">
      <summary>Отдельные настройки</summary>
      <div class="setting-row"><div class="label">Чувствительность</div>
        <select class="input narrow" data-bot-field="sensitivity">${SENSITIVITY_OPTIONS.map(([v, l]) => `<option value="${v}"${v === b.sensitivity ? ' selected' : ''}>${esc(l)}</option>`).join('')}</select></div>
      ${BOT_HD_FIELDS.map(f => botFieldHtml(b, f)).join('')}
      <div class="actions"><button class="btn danger" data-bot-del>🗑 Удалить бота</button></div>
    </details>
  </div>`;
}

function botFieldHtml(b, [key, kind, label, desc]) {
  const v = b.helpdesk[key];
  const lbl = `<div class="label">${esc(label)}</div>${desc ? `<div class="desc">${esc(desc)}</div>` : ''}`;
  const attr = `data-bot-hd="${key}" data-kind="${kind}"`;
  switch (kind) {
    case 'bool':
      return `<div class="setting-row"><div>${lbl}</div><button class="switch${v ? ' on' : ''}" ${attr}><span class="knob"></span></button></div>`;
    case 'int':
      return `<div class="setting-row"><div>${lbl}</div><input class="input narrow" type="number" ${attr} value="${esc(v)}"></div>`;
    case 'time':
      return `<div class="setting-row"><div>${lbl}</div><input class="input narrow" type="time" ${attr} value="${esc(v)}"></div>`;
    case 'days': {
      const names = ['пн', 'вт', 'ср', 'чт', 'пт', 'сб', 'вс'];
      return `<div class="setting-block">${lbl}<div class="chiprow days" ${attr}>${names.map((n, i) =>
        `<button class="chip${(v || '').includes(String(i + 1)) ? ' active' : ''}" data-day="${i + 1}">${n}</button>`).join('')}</div></div>`;
    }
    default:
      return `<div class="setting-block">${lbl}<textarea class="input" ${attr}>${esc(v)}</textarea></div>`;
  }
}

async function patchBot(id, body, okMsg) {
  try {
    const b = await api('POST', `/api/bots/${id}`, body);
    const idx = state.bots.findIndex(x => x.id === id);
    if (idx >= 0) state.bots[idx] = b; else state.bots.push(b);
    if (okMsg) toast(okMsg);
  } catch (e) { toast(e.message, true); }
  renderSettings();
}

function wireBots() {
  const addBtn = document.getElementById('botAddBtn');
  if (addBtn) addBtn.addEventListener('click', openAddBotModal);

  app.querySelectorAll('.bot-row').forEach(row => {
    const id = Number(row.dataset.bot);
    const bot = state.bots.find(b => b.id === id);
    if (!bot) return;

    row.addEventListener('toggle', e => {
      if (e.target.matches('[data-bot-adv]')) {
        const key = 'bot-' + id;
        if (e.target.open) state.openGroups.add(key); else state.openGroups.delete(key);
      }
    }, true);

    row.querySelector('[data-bot-active]').addEventListener('click', () => patchBot(id, { active: !bot.active }));
    row.querySelector('[data-bot-field="label"]').addEventListener('change', e => patchBot(id, { label: e.target.value.trim() }));
    const sens = row.querySelector('[data-bot-field="sensitivity"]');
    if (sens) sens.addEventListener('change', e => patchBot(id, { sensitivity: e.target.value }));
    const delBtn = row.querySelector('[data-bot-del]');
    if (delBtn) delBtn.addEventListener('click', async () => {
      if (!await confirmModal('Удалить бота?', `«${bot.label}» перестанет отвечать. История его тикетов останется.`, 'Удалить', true)) return;
      try {
        await api('DELETE', `/api/bots/${id}`);
        state.bots = state.bots.filter(b => b.id !== id);
        toast('🗑 Бот удалён');
        renderSettings();
      } catch (e) { toast(e.message, true); }
    });

    row.querySelectorAll('[data-bot-hd]').forEach(el => {
      const key = el.dataset.botHd;
      switch (el.dataset.kind) {
        case 'bool':
          el.addEventListener('click', () => patchBot(id, { helpdesk: { [key]: !bot.helpdesk[key] } }));
          break;
        case 'days':
          el.querySelectorAll('[data-day]').forEach(chip => chip.addEventListener('click', () => {
            const day = chip.dataset.day;
            const cur = bot.helpdesk[key] || '';
            patchBot(id, { helpdesk: { [key]: cur.includes(day) ? cur.replace(day, '') : cur + day } });
          }));
          break;
        case 'int':
          el.addEventListener('change', () => patchBot(id, { helpdesk: { [key]: Number(el.value) } }));
          break;
        default:
          el.addEventListener('change', () => patchBot(id, { helpdesk: { [key]: el.value } }));
      }
    });
  });
}

function openAddBotModal() {
  showModal(`<h3>Добавить бота</h3>
    <p class="hint-text" style="margin:0 0 8px">Токен от @BotFather для нового, отдельного бота этой организации.</p>
    <input class="input" id="addBotToken" placeholder="Токен бота" autocomplete="off" autocapitalize="off" spellcheck="false">
    <input class="input" id="addBotLabel" placeholder="Название организации (необязательно)" style="margin-top:8px">
    <div class="actions"><button class="btn primary full" id="addBotOk">＋ Добавить</button>
    <button class="btn ghost full" id="addBotCancel">Отмена</button></div>`, root => {
    root.querySelector('#addBotCancel').addEventListener('click', closeModal);
    root.querySelector('#addBotOk').addEventListener('click', e => {
      const token = root.querySelector('#addBotToken').value.trim();
      const label = root.querySelector('#addBotLabel').value.trim();
      if (!token) { toast('Введите токен', true); return; }
      withBusy(e.target, async () => {
        const b = await api('POST', '/api/bots', { token, label });
        state.bots.push(b);
        closeModal();
        toast('✅ Бот «' + (b.label || b.username) + '» добавлен');
        renderSettings();
      });
    });
  });
}

function restartMark(f) { return f.restart ? ' <span class="badge">после перезапуска</span>' : ''; }

function fieldHtml(s, f) {
  const label = `<div class="label">${esc(f.label)}${restartMark(f)}</div>${f.desc ? `<div class="desc">${esc(f.desc)}</div>` : ''}`;
  const k = esc(f.key);
  switch (f.kind) {
    case 'bool':
      return `<div class="setting-row"><div>${label}</div><button class="switch${f.value ? ' on' : ''}" data-field="${k}" data-kind="bool"><span class="knob"></span></button></div>`;
    case 'int':
      return `<div class="setting-row"><div>${label}</div><input class="input narrow" type="number" data-field="${k}" data-kind="int" value="${esc(f.value)}"${f.min ? ` min="${f.min}"` : ''}${f.max ? ` max="${f.max}"` : ''}></div>`;
    case 'time':
      return `<div class="setting-row"><div>${label}</div><input class="input narrow" type="time" data-field="${k}" data-kind="string" value="${esc(f.value)}"></div>`;
    case 'select':
      return `<div class="setting-row"><div>${label}</div><select class="input narrow" data-field="${k}" data-kind="string">${f.options.map(o => `<option value="${esc(o.value)}"${o.value === f.value ? ' selected' : ''}>${esc(o.label)}</option>`).join('')}</select></div>`;
    case 'text':
      return `<div class="setting-block">${label}<textarea class="input" data-field="${k}" data-kind="string">${esc(f.value)}</textarea></div>`;
    case 'list':
      return `<div class="setting-block">${label}<textarea class="input mono" data-field="${k}" data-kind="list" placeholder="по одному на строку">${esc(f.value.join('\n'))}</textarea></div>`;
    case 'days': {
      const names = ['пн', 'вт', 'ср', 'чт', 'пт', 'сб', 'вс'];
      return `<div class="setting-block">${label}<div class="chiprow days" data-field="${k}" data-kind="days">${names.map((n, i) =>
        `<button class="chip${f.value.includes(String(i + 1)) ? ' active' : ''}" data-day="${i + 1}">${n}</button>`).join('')}</div></div>`;
    }
    case 'model': {
      const presets = (field(s, `ai.${f.provider}_presets`) || { value: [] }).value;
      return `<div class="setting-block">${label}<input class="input mono" list="dl-${k}" data-field="${k}" data-kind="string" value="${esc(f.value)}">
        <datalist id="dl-${k}">${presets.map(p => `<option value="${esc(p)}">`).join('')}</datalist></div>`;
    }
    default:
      return `<div class="setting-block">${label}<input class="input" type="text" data-field="${k}" data-kind="string" value="${esc(f.value)}"></div>`;
  }
}

async function patchSettings(body, okMsg) {
  try {
    state.settings = await api('POST', '/api/settings', body);
    if (okMsg) toast(okMsg);
    if (body.values && Object.keys(body.values).some(k => k.startsWith('helpdesk.'))) await refreshMe();
  } catch (e) { toast(e.message, true); }
  renderSettings();
}

async function refreshMe() {
  try { state.me = await api('GET', '/api/me'); buildTabbar(); document.querySelectorAll('.tab').forEach(b => b.classList.toggle('active', b.dataset.tab === state.tab)); } catch (e) {}
}

function wireSettings(s) {
  app.querySelectorAll('details[data-group]').forEach(d => d.addEventListener('toggle', () => {
    if (d.open) state.openGroups.add(d.dataset.group); else state.openGroups.delete(d.dataset.group);
    if (d.open && d.dataset.group === 'backup') loadBackups();
  }));
  if (state.openGroups.has('backup')) loadBackups();

  app.querySelectorAll('[data-field]').forEach(el => {
    const key = el.dataset.field;
    const f = field(s, key);
    switch (el.dataset.kind) {
      case 'bool':
        el.addEventListener('click', () => patchSettings({ values: { [key]: !f.value } }));
        break;
      case 'int':
        el.addEventListener('change', () => patchSettings({ values: { [key]: Number(el.value) } }, '✓ ' + f.label));
        break;
      case 'list':
        el.addEventListener('change', () => patchSettings({ values: { [key]: el.value.split('\n').map(x => x.trim()).filter(Boolean) } }, '✓ ' + f.label));
        break;
      case 'days':
        el.querySelectorAll('[data-day]').forEach(chip => chip.addEventListener('click', () => {
          const day = chip.dataset.day;
          const next = f.value.includes(day) ? f.value.replace(day, '') : f.value + day;
          patchSettings({ values: { [key]: next } });
        }));
        break;
      default:
        el.addEventListener('change', () => patchSettings({ values: { [key]: el.value } }, '✓ ' + f.label));
    }
  });

  document.getElementById('chainAdd').addEventListener('click', () => {
    const d = state.chainDraft;
    d.push(newChainRow(d.length ? d[d.length - 1].provider : ''));
    setChainDirty(true);
    renderChain();
    const inputs = app.querySelectorAll('.chain-key');
    if (inputs.length) inputs[inputs.length - 1].focus();
  });
  document.getElementById('chainSave').addEventListener('click', e => {
    const d = state.chainDraft;
    const bad = d.findIndex(r => !r.key.trim() && !r.key_id);
    if (bad >= 0) { toast(`Строка ${bad + 1}: введите API-ключ`, true); return; }
    const ai_chain = d.map(r => r.key.trim() ? { provider: r.provider, key: r.key.trim() } : { provider: r.provider, key_id: r.key_id });
    withBusy(e.target, async () => {
      state.settings = await api('POST', '/api/settings', { ai_chain });
      state.chainDraft = null;
      state.chainDirty = false;
      toast('💾 Ключи AI сохранены');
      renderSettings();
    });
  });
  document.getElementById('testProvider').addEventListener('click', e => {
    if (state.chainDirty) { toast('Сначала сохраните ключи — проверяются сохранённые', true); return; }
    const out = document.getElementById('testResult');
    out.innerHTML = `<div class="loading">Проверяю…</div>`;
    withBusy(e.target, async () => {
      try { out.innerHTML = testResultHtml(await api('POST', '/api/provider/test')); }
      catch (err) { out.innerHTML = `<div class="empty">❌ ${esc(err.message)}</div>`; }
    });
  });

  wireBots();

  document.getElementById('hdCheck').addEventListener('click', e => withBusy(e.target, async () => {
    const out = document.getElementById('hdCheckResult');
    try {
      const r = await api('POST', '/api/helpdesk/check');
      const line = (ok, text) => `<p class="probe ${ok ? 'ok' : 'bad'}">${ok ? '✅' : '❌'} ${text}</p>`;
      out.innerHTML = `<div class="card detail-section inset">
        ${line(true, 'Группа: ' + esc(r.title || '—'))}
        ${line(r.is_forum, 'Темы (форум) включены')}
        ${line(r.bot_admin, 'Бот — администратор')}
        ${line(r.can_manage_topics, 'Право «Управление темами»')}
        ${line(r.can_pin_messages, 'Право закреплять сообщения (карточки тикетов)')}
        ${line(r.can_delete_messages, 'Право удалять сообщения (скрывать команду /1)')}
      </div>`;
    } catch (err) { out.innerHTML = `<div class="empty">❌ ${esc(err.message)}</div>`; }
  }));

  document.getElementById('backupRun').addEventListener('click', e => withBusy(e.target, async () => {
    const r = await api('POST', '/api/backups');
    if (r.warning) toast(r.warning, true); else toast('💾 Бэкап создан: ' + fmtSize(r.backup.size));
    loadBackups();
  }));

  document.getElementById('restartBtn').addEventListener('click', async () => {
    if (!await confirmModal('Перезапустить сервис?', 'Бот будет недоступен несколько секунд.', 'Перезапустить')) return;
    try {
      await api('POST', '/api/system/restart');
      toast('♻️ Перезапуск… обновите через 10 секунд');
    } catch (e) { toast(e.message, true); }
  });

  document.getElementById('resetSettings').addEventListener('click', async () => {
    if (!await confirmModal('Сбросить настройки?', 'Все настройки, включая хелпдеск и ключи AI, вернутся к значениям из .env, а если их там нет — к значениям по умолчанию.', 'Да, сбросить', true)) return;
    try { await api('POST', '/api/settings/reset'); toast('♻️ Настройки сброшены'); renderSettings(); }
    catch (e) { toast(e.message, true); }
  });
}

async function loadBackups() {
  const box = document.getElementById('backupList');
  if (!box) return;
  try {
    const r = await api('GET', '/api/backups');
    box.classList.remove('loading');
    box.innerHTML = r.items.length
      ? `<div class="hint-text" style="margin-top:8px">${esc(r.dir)}</div>` + r.items.map(b =>
        `<div class="setting-row"><div class="mono small">${esc(b.name)}</div><div class="muted small">${fmtSize(b.size)}</div></div>`).join('')
      : `<div class="empty" style="padding:14px 0">Бэкапов пока нет</div>`;
  } catch (e) { box.innerHTML = `<div class="empty">⚠️ ${esc(e.message)}</div>`; }
}

/* AI chain editor: rows are edited locally (state.chainDraft) and sent together on "Сохранить".
   Stored keys never reach the browser — a row keeps the server's key_id until a new key is typed. */

function chainFromSettings(s) {
  return s.ai_chain.map(e => ({ provider: e.provider, key: '', key_id: e.key_id, key_masked: e.key_masked }));
}

function newChainRow(provider) { return { provider: provider || 'gemini', key: '', key_id: '', key_masked: '' }; }

function renderChain() {
  const box = document.getElementById('chainBox');
  if (!box) return;
  const draft = state.chainDraft;
  box.innerHTML = !draft.length
    ? `<div class="empty" style="padding:14px 0">Ключей нет — AI не работает. Добавьте хотя бы один.</div>`
    : draft.map((e, i) => `<div class="chain-row" data-i="${i}">
      <div class="chain-line">
        <span class="chain-num">${i + 1}</span>
        <select class="input chain-provider">${PROVIDERS.map(([p, l]) => `<option value="${p}"${p === e.provider ? ' selected' : ''}>${l}</option>`).join('')}</select>
        <button class="icon-btn" data-chain="up"${i === 0 ? ' disabled' : ''}>▲</button>
        <button class="icon-btn" data-chain="down"${i === draft.length - 1 ? ' disabled' : ''}>▼</button>
        <button class="icon-btn" data-chain="add">＋</button>
        <button class="icon-btn danger" data-chain="del">−</button>
      </div>
      <input class="input chain-key" type="text" autocomplete="off" autocapitalize="off" spellcheck="false"
        placeholder="${e.key_id ? 'сохранён ' + esc(e.key_masked) + ' · новый заменит' : 'API-ключ'}" value="${esc(e.key)}">
    </div>`).join('');
  box.querySelectorAll('.chain-row').forEach(rowEl => {
    const i = Number(rowEl.dataset.i);
    rowEl.querySelector('.chain-provider').addEventListener('change', ev => { state.chainDraft[i].provider = ev.target.value; setChainDirty(true); });
    rowEl.querySelector('.chain-key').addEventListener('input', ev => { state.chainDraft[i].key = ev.target.value; setChainDirty(true); });
    rowEl.querySelectorAll('[data-chain]').forEach(btn => btn.addEventListener('click', () => chainOp(btn.dataset.chain, i)));
  });
}

function chainOp(op, i) {
  const d = state.chainDraft;
  if (op === 'up' && i > 0) [d[i - 1], d[i]] = [d[i], d[i - 1]];
  else if (op === 'down' && i < d.length - 1) [d[i + 1], d[i]] = [d[i], d[i + 1]];
  else if (op === 'add') d.splice(i + 1, 0, newChainRow(d[i].provider));
  else if (op === 'del') d.splice(i, 1);
  else return;
  haptic('light');
  setChainDirty(true);
  renderChain();
}

function setChainDirty(dirty) {
  state.chainDirty = dirty;
  const btn = document.getElementById('chainSave');
  if (btn) { btn.disabled = !dirty; btn.textContent = dirty ? '💾 Сохранить' : '✓ Сохранено'; }
}

function testResultHtml(r) {
  return `<div class="card detail-section inset">` + r.results.map(x => {
    const head = `${x.index}. ${esc(providerLabel(x.provider))} · ${esc(x.key_masked)} · ${esc(x.model)} · ${(x.latency_ms / 1000).toFixed(1)} с`;
    if (x.error) return `<p class="probe bad"><b>❌ ${head}</b><br>${esc(x.error)}</p>`;
    return `<p class="probe ok"><b>✅ ${head}</b><br>работает · задача: ${x.analysis.is_task ? 'да' : 'нет'} · уверенность ${Math.round(x.analysis.confidence * 100)}%</p>`;
  }).join('') + `</div>`;
}

/* ---------------------------------------------------------------------- */
/* Boot                                                                   */
/* ---------------------------------------------------------------------- */

(async function boot() {
  try {
    state.me = await api('GET', '/api/me');
  } catch (e) {
    topTitle.textContent = 'Нет доступа';
    app.innerHTML = `<div class="card empty">⛔ ${esc(e.message)}</div>`;
    return;
  }
  await loadBots();
  buildTabbar();
  const params = new URLSearchParams(location.search);
  const startParam = tg && tg.initDataUnsafe ? tg.initDataUnsafe.start_param : '';
  const taskID = Number(params.get('task') || (startParam && startParam.startsWith('t') ? startParam.slice(1) : 0));
  const first = tabsForMe()[0][0];
  state.tab = taskID ? 'tasks' : first;
  document.querySelectorAll('.tab').forEach(b => b.classList.toggle('active', b.dataset.tab === state.tab));
  if (taskID) openView({ type: 'task', id: taskID });
  else render();
})();
