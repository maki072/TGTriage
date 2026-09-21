'use strict';

/* ---------------------------------------------------------------------- */
/* State & navigation                                                     */
/* ---------------------------------------------------------------------- */

const state = {
  me: null,
  tab: null,
  view: null,            // {type: 'task'|'user'|'settings', id, bot}
  back: [],              // view stack
  rt: 0,                 // render token: async renders bail out when a newer render started
  awaiting: 0,           // dialogs waiting for a reply (tab badge)
  tasksFilter: { status: 'act', prio: 'all', scope: 'all', bot: 'all', q: '', over: false, limit: 50 },
  historyFilter: { status: 'done', prio: 'all', scope: 'all', bot: 'all', q: '', over: false, limit: 50 },
  dialogsFilter: { filter: 'awaiting', q: '', bot: 'all', limit: 50 },
  tl: null,              // last loaded task list {tab, items, total}
  select: { on: false, ids: [] },
  longPressed: false,
  task: null,            // task shown in the detail view
  dialog: null,          // dialog shown in the dialog view
  settings: null,
  chainDraft: null,
  chainDirty: false,
  bots: [],              // additional bots (owner only)
  openGroups: new Set(),
};

function isOwner() { return state.me && state.me.role === 'owner'; }

function tabsForMe() {
  const tabs = [];
  if (!isOwner() || state.me.helpdesk_enabled) tabs.push(['dialogs', 'message', 'Диалоги']);
  tabs.push(['tasks', 'tasks', isOwner() ? 'Задачи' : 'Тикеты']);
  tabs.push(['history', 'history', 'История']);
  if (isOwner()) tabs.push(['settings', 'sliders', 'Настройки']);
  return tabs;
}

function tabbarHtml() {
  return `<nav class="tr-tabbar" aria-label="Разделы">` + tabsForMe().map(([id, icon, label]) =>
    `<button type="button" class="tr-tab" data-act="tab" data-tab="${id}"${state.tab === id ? ' aria-current="page"' : ''}>${I(icon, 24)}${esc(label)}` +
    (id === 'dialogs' && state.awaiting > 0 ? `<span class="tr-tab__badge" aria-label="${state.awaiting} ждут ответа">${state.awaiting > 99 ? '99+' : state.awaiting}</span>` : '') + `</button>`).join('') + `</nav>`;
}
ACT.tab = el => { state.view = null; state.back = []; state.select = { on: false, ids: [] }; switchTab(el.dataset.tab); };

function switchTab(tab) { hideToast(); state.tab = tab; render(); window.scrollTo(0, 0); }

function openView(v) {
  hideToast();
  if (state.view) state.back.push(state.view);
  state.view = v;
  render();
  window.scrollTo(0, 0);
}
function goBack() {
  if (!state.view) return;
  hideToast();
  state.view = state.back.pop() || null;
  render();
}
backBtn.addEventListener('click', goBack);
if (tg && tg.BackButton) tg.BackButton.onClick(goBack);
ACT.refresh = () => render();

function render() {
  const tok = ++state.rt;
  const v = state.view;
  if (tg && tg.BackButton) { if (v) tg.BackButton.show(); else tg.BackButton.hide(); }
  if (v) {
    if (v.type === 'task') return renderTaskDetail(v.id, tok);
    if (v.type === 'user') return renderDialog(v.id, v.bot, tok);
    if (v.type === 'settings') return renderSettings(v.id, tok);
  }
  switch (state.tab) {
    case 'dialogs': return renderDialogs(tok);
    case 'tasks': return renderTaskList('tasks', tok);
    case 'history': return renderTaskList('history', tok);
    case 'settings': return renderSettings(null, tok);
  }
}

async function loadBots() {
  if (!isOwner()) return state.bots;
  try { state.bots = (await api('GET', '/api/bots')).items || []; } catch (e) { /* non-fatal */ }
  return state.bots;
}
function botLabelFor(botID) {
  if (!botID) return 'Основной';
  const b = state.bots.find(x => x.id === botID);
  return b ? (b.label || ('#' + b.id)) : ('#' + botID);
}
async function refreshAwaiting() {
  try {
    const o = await api('GET', '/api/overview?scope=all&bot=all');
    state.awaiting = o.awaiting || 0;
    const nav = bottom.querySelector('.tr-tabbar');
    if (nav) bottom.innerHTML = tabbarHtml();
  } catch (e) { /* non-fatal */ }
}

/* ---------------------------------------------------------------------- */
/* Task list (tabs: tasks / history)                                      */
/* ---------------------------------------------------------------------- */

const STATUS_SEGS_TASKS = [['act', 'Активные'], ['new', 'Новые'], ['snz', 'Отложенные']];
const STATUS_SEGS_HISTORY = [['done', 'Завершённые'], ['fp', 'Не задачи'], ['all', 'Все']];
const PRIO_OPTIONS = [['all', 'Любой'], ['urg', 'Срочные'], ['crit', 'Критический'], ['high', 'Высокий'], ['med', 'Средний'], ['low', 'Низкий']];

function filterFor(tab) { return tab === 'tasks' ? state.tasksFilter : state.historyFilter; }
function filterGroupsFor() {
  const groups = [];
  if (isOwner()) groups.push({ id: 'scope', label: 'Источник', options: [['all', 'Все'], ['helpdesk', 'Тикеты', 'headset'], ['personal', 'Личные', 'user']] });
  if (isOwner() && state.bots.length) groups.push({ id: 'bot', label: 'Бот', options: [['all', 'Все боты'], ['0', 'Основной']].concat(state.bots.map(b => [String(b.id), (b.active ? '' : 'выкл. · ') + (b.label || ('#' + b.id))])) });
  groups.push({ id: 'prio', label: 'Приоритет', options: PRIO_OPTIONS });
  return groups;
}
function activeFilters(f) {
  return filterGroupsFor().map(g => {
    const v = f[g.id];
    if (!v || v === 'all') return null;
    const o = g.options.find(x => x[0] === v);
    return o ? { id: g.id, label: o[1] } : null;
  }).filter(Boolean);
}

async function renderTaskList(tab, tok) {
  const f = filterFor(tab);
  const owner = isOwner();
  const scope = owner ? f.scope : 'helpdesk';
  const canSelect = tab === 'tasks';
  setTop({ title: tab === 'history' ? 'История' : owner ? 'Задачи' : 'Тикеты', large: true,
    actions: (canSelect ? ibtn(state.select.on ? 'x' : 'check-circle', state.select.on ? 'Снять выбор' : 'Выбрать', { act: 'sel-toggle' }) : '') + ibtn('refresh', 'Обновить', { act: 'refresh' }) });
  setBottom(state.select.on ? selectBarHtml() : tabbarHtml());
  app.innerHTML = `<div id="tlHead"></div><div id="tlBody">${skeleton(4)}</div>`;

  let ov = null;
  if (tab === 'tasks') {
    try { ov = await api('GET', `/api/overview?scope=${scope}&bot=${f.bot}`); } catch (e) { /* non-fatal */ }
    if (tok !== state.rt) return;
    if (ov) state.awaiting = ov.awaiting || 0;
    if (!state.select.on) setBottom(tabbarHtml());
  }
  document.getElementById('tlHead').innerHTML = taskListHead(tab, f, ov, scope);

  try {
    const data = await api('GET', `/api/tasks?scope=${scope}&bot=${f.bot}&status=${f.status}&priority=${f.prio}&limit=${f.limit}&offset=0`);
    if (tok !== state.rt) return;
    state.tl = { tab, items: data.items || [], total: data.total || 0 };
    paintTaskItems();
  } catch (e) {
    if (tok !== state.rt) return;
    document.getElementById('tlBody').innerHTML = emptyState('alert', 'Не удалось загрузить', e.message, btn('Повторить', { sm: true, icon: 'refresh', act: 'refresh' }));
  }
}

function taskListHead(tab, f, ov, scope) {
  let html = '';
  if (tab === 'tasks' && ov) {
    const conn = isOwner() && f.scope !== 'helpdesk' ? ov.connection : undefined;
    if (isOwner() && f.scope !== 'helpdesk' && !ov.connection) html += banner('Telegram Business не подключён', 'info');
    else if (conn && !conn.enabled) html += banner('Business-подключение отключено', 'danger');
    else if (conn && !conn.can_reply) html += banner('Нет права отвечать за вас — включите его в настройках Telegram Business', 'danger');
    else if (ov.awaiting > 0 && state.me.helpdesk) html += banner(`Ждут ответа: <b>${ov.awaiting}</b>`, 'warning', { act: 'tab', action: 'Открыть' }).replace('data-act="tab"', 'data-act="tab" data-tab="dialogs"');
    const activeTile = f.over ? 'over' : f.status === 'new' ? 'new' : f.status === 'wrk' ? 'wrk' : f.status === 'snz' ? 'snz' : null;
    const tile = (id, label, n, danger) => `<button type="button" class="tr-stat${danger && n ? ' tr-stat--danger' : ''}${n ? '' : ' tr-stat--zero'}" data-act="sum" data-id="${id}" aria-pressed="${activeTile === id}"><b>${n}</b><span>${label}</span></button>`;
    html += `<div class="tr-summary" role="group" aria-label="Сводка">${tile('new', 'Новые', ov.new)}${tile('wrk', 'В работе', ov.in_progress)}${tile('snz', 'Отложено', ov.snoozed)}${tile('over', 'Просрочено', ov.overdue, true)}</div>`;
  }
  const segs = (tab === 'tasks' ? STATUS_SEGS_TASKS : STATUS_SEGS_HISTORY).map(([value, label]) => {
    const o = { value, label };
    if (ov && tab === 'tasks') o.count = value === 'act' ? ov.new + ov.in_progress : value === 'new' ? ov.new : ov.snoozed;
    return o;
  });
  const act = activeFilters(f);
  html += `<div class="tr-filterbar"><div class="tr-filterbar__top"><div class="tr-search">${I('search', 20)}<input class="tr-input" id="tlSearch" type="search" placeholder="Название, отправитель или №" aria-label="Поиск" value="${esc(f.q)}"></div>` +
    `<button type="button" class="tr-filterbtn" data-act="filters" data-active="${act.length > 0}" aria-label="Фильтры">${I('filter', 20)}${act.length ? `<span class="tr-filterbtn__n">${act.length}</span>` : ''}</button></div>` +
    segmented('tstatus', segs, f.status) +
    (act.length ? `<div class="tr-activefilters">${act.map(a => chip(a.label, { remove: true, act: 'unfilter', attrs: `data-id="${a.id}"` })).join('')}</div>` : '') + `</div>`;
  return html;
}

SEG.tstatus = v => { const f = filterFor(state.tab); f.status = v; f.over = false; f.limit = 50; render(); };
ACT.sum = el => {
  const f = state.tasksFilter, id = el.dataset.id;
  const cur = f.over ? 'over' : f.status === 'new' ? 'new' : f.status === 'wrk' ? 'wrk' : f.status === 'snz' ? 'snz' : null;
  f.over = false; f.status = 'act';
  if (cur !== id) { if (id === 'over') f.over = true; else f.status = id; }
  f.limit = 50;
  render();
};
ACT.unfilter = el => { const f = filterFor(state.tab); f[el.dataset.id] = 'all'; f.limit = 50; render(); };
ACT.filters = () => {
  const f = filterFor(state.tab);
  const groups = filterGroupsFor();
  const body = groups.map(g => `<div><h3 class="tr-overline">${esc(g.label)}</h3><div class="tr-chips" data-fgroup="${g.id}">` +
    g.options.map(o => chip(o[1], { selected: (f[g.id] || 'all') === o[0], icon: o[2], attrs: `data-v="${esc(o[0])}"` })).join('') + `</div></div>`).join('');
  showSheet('Фильтры', body, btn('Сбросить', { v: 'ghost', id: 'fReset' }) + btn('Готово', { v: 'primary', id: 'fDone' }), root => {
    root.querySelectorAll('[data-fgroup] .tr-chip').forEach(c => c.addEventListener('click', () => {
      const gid = c.parentElement.dataset.fgroup;
      f[gid] = c.dataset.v; f.limit = 50;
      c.parentElement.querySelectorAll('.tr-chip').forEach(x => x.setAttribute('aria-pressed', String(x === c)));
      dirty = true;
    }));
    let dirty = false;
    root.querySelector('#fReset').addEventListener('click', () => { groups.forEach(g => { f[g.id] = 'all'; }); closeSheet(); render(); });
    root.querySelector('#fDone').addEventListener('click', () => { closeSheet(); if (dirty) render(); });
  });
};

document.addEventListener('input', e => {
  if (e.target.id === 'tlSearch') { filterFor(state.tab).q = e.target.value; paintTaskItems(); }
  else if (e.target.id === 'dlSearch') { state.dialogsFilter.q = e.target.value; clearTimeout(dlTimer); dlTimer = setTimeout(loadDialogItems, 300); }
  else if (e.target.id === 'hdReply') { const b = document.getElementById('hdSend'); if (b) b.disabled = !e.target.value.trim(); }
});
let dlTimer = null;

function taskCard(t, o) {
  o = o || {};
  const meta = [`<span>${esc(t.sender_name || '—')}</span>`, `<span class="tr-sep">${esc(CATEGORY_LABEL[t.category] || t.category)}</span>`, `<span class="tr-sep">#${t.id}</span>`];
  if (t.helpdesk && state.bots.length) meta.splice(1, 0, `<span class="tr-sep">${esc(botLabelFor(t.bot_id))}</span>`);
  const badges = [];
  if (t.status !== 'new' && t.status !== 'in_progress') badges.push(statusBadge(t.status));
  if (t.overdue && t.deadline) badges.push(badge('Просрочено · ' + esc(fmtDT(t.deadline)), { tone: 'danger', icon: 'flag' }));
  else if (t.deadline) badges.push(badge('до ' + esc(fmtDT(t.deadline)), { icon: 'flag' }));
  if (t.status === 'snoozed' && t.snooze_until) badges.push(badge('до ' + esc(fmtDT(t.snooze_until)), { icon: 'clock' }));
  const sel = state.select.on;
  const on = state.select.ids.includes(t.id);
  return `<button type="button" class="tr-task" data-act="${sel ? 'sel-task' : 'open-task'}" data-id="${t.id}"${sel ? ` aria-selected="${on}"` : ''}>` +
    (sel ? `<span class="tr-task__lead"><span class="tr-check">${I('check', 14)}</span></span>` : '') +
    `<span class="tr-task__body"><span class="tr-task__top"><span style="padding-top:4px">${prioMark(t.priority)}</span><span class="tr-task__title">${esc(t.title || '(без заголовка)')}</span>` +
    `<span class="tr-task__time">${esc(fmtDT(t.created_at))}</span></span><span class="tr-task__meta">${meta.join('')}${badges.join('')}</span></span></button>`;
}
ACT['open-task'] = el => openView({ type: 'task', id: Number(el.dataset.id) });

function paintTaskItems() {
  const body = document.getElementById('tlBody');
  if (!body || !state.tl) return;
  const f = filterFor(state.tl.tab);
  const q = f.q.trim().toLowerCase().replace(/^#/, '');
  let items = state.tl.items;
  if (f.over) items = items.filter(t => t.overdue);
  if (q) items = items.filter(t => `${t.title} ${t.sender_name}`.toLowerCase().includes(q) || String(t.id).startsWith(q));
  if (!items.length) {
    body.innerHTML = q ? emptyState('search', 'Ничего не нашлось', 'Попробуйте часть названия или имя отправителя.') : emptyState('check-circle', 'Всё разобрано', 'В этом списке задач нет.');
    return;
  }
  const more = state.tl.items.length < state.tl.total;
  body.innerHTML = `<div class="tr-list">${items.map(t => taskCard(t)).join('')}</div>` +
    (more ? `<div class="tr-more">${btn('Показать ещё', { v: 'ghost', act: 'more-tasks' })}<span class="tr-cap">${state.tl.items.length} из ${state.tl.total}</span></div>` : '');
}
ACT['more-tasks'] = () => { const f = filterFor(state.tab); f.limit += 50; render(); };

/* selection mode: pick two tasks to merge */
function selectBarHtml() {
  const n = state.select.ids.length;
  return `<div class="tr-actionbar" role="toolbar"><div class="tr-actionbar__info">${n ? 'Выбрано ' + n : 'Отметьте 2 задачи'}</div>` +
    btn('Объединить', { v: 'primary', icon: 'merge', act: 'merge-sel', disabled: n !== 2 }) + `</div>`;
}
function setSelect(on, ids) {
  state.select = { on, ids: ids || [] };
  setBottom(on ? selectBarHtml() : tabbarHtml());
  topActions.innerHTML = ibtn(on ? 'x' : 'check-circle', on ? 'Снять выбор' : 'Выбрать', { act: 'sel-toggle' }) + ibtn('refresh', 'Обновить', { act: 'refresh' });
  paintTaskItems();
}
ACT['sel-toggle'] = () => setSelect(!state.select.on);
ACT['sel-task'] = el => {
  const id = Number(el.dataset.id);
  let ids = state.select.ids.includes(id) ? state.select.ids.filter(x => x !== id) : state.select.ids.concat([id]);
  if (ids.length > 2) ids = ids.slice(-2);
  haptic('light');
  setSelect(true, ids);
};
// long press on a card enters selection mode
(function () {
  let timer = null, sx = 0, sy = 0;
  const cancel = () => { clearTimeout(timer); timer = null; };
  document.addEventListener('pointerdown', e => {
    const c = e.target.closest('[data-act="open-task"]');
    if (!c || state.tab !== 'tasks' || state.view) return;
    sx = e.clientX; sy = e.clientY;
    timer = setTimeout(() => { timer = null; state.longPressed = true; haptic('medium'); setSelect(true, [Number(c.dataset.id)]); setTimeout(() => { state.longPressed = false; }, 400); }, 500);
  });
  document.addEventListener('pointermove', e => { if (timer && Math.hypot(e.clientX - sx, e.clientY - sy) > 10) cancel(); });
  ['pointerup', 'pointercancel', 'pointerleave'].forEach(n => document.addEventListener(n, cancel));
  document.addEventListener('contextmenu', e => { if (e.target.closest('.tr-task')) e.preventDefault(); });
})();

ACT['merge-sel'] = () => {
  const tasks = state.select.ids.map(id => (state.tl.items.find(t => t.id === id))).filter(Boolean);
  if (tasks.length !== 2) return;
  openMergeSheet(tasks[0], tasks[1]);
};

function pickRow(t, o) {
  o = o || {};
  return `<button type="button" role="radio" aria-checked="${!!o.checked}" class="tr-pick" data-id="${t.id}">${prioMark(t.priority)}` +
    `<span class="tr-pick__body"><span class="tr-pick__title">${esc(t.title || '(без заголовка)')}</span><span class="tr-pick__meta">${esc(t.sender_name || '—')} · #${t.id} · ${esc(fmtDT(t.created_at))}${o.why ? `<span class="tr-pick__why">${esc(o.why)}</span>` : ''}</span></span>` +
    `<span class="tr-check" data-on="${!!o.checked}">${I('check', 14)}</span></button>`;
}
function mergePlan(from, into) {
  return `<div class="tr-mergeplan"><div class="tr-mergeplan__row">${I('merge', 18)}<span><b>#${from.id}</b> закроется и перейдёт в <b>#${into.id}</b></span></div>` +
    `<div class="tr-cap">Переписка, исходный текст и напоминания переносятся. Действие необратимо.</div></div>`;
}
async function doMerge(from, into, btnEl) {
  await withBusy(btnEl, async () => {
    await api('POST', `/api/tasks/${from.id}/merge`, { target_id: into.id });
    closeSheet();
    toast(`Объединено в #${into.id}`);
    state.select = { on: false, ids: [] };
    if (state.view && state.view.type === 'task') state.view = { type: 'task', id: into.id };
    render();
  });
}
// MergeSheet: two selected tasks; the older one is kept unless the user picks the other.
function openMergeSheet(a, b) {
  let keep = (a.id < b.id ? a : b).id;
  const paint = root => {
    const into = keep === a.id ? a : b, from = keep === a.id ? b : a;
    root.querySelector('.tr-sheet__body').innerHTML = `<div><h3 class="tr-overline">Какую оставить</h3><div role="radiogroup" class="tr-stack" style="gap:8px">${[a, b].map(t => pickRow(t, { checked: keep === t.id, why: t.id === Math.min(a.id, b.id) ? 'старшая' : '' })).join('')}</div></div>${mergePlan(from, into)}`;
    root.querySelector('#mergeOk').textContent = `Объединить в #${into.id}`;
    root.querySelectorAll('.tr-pick').forEach(p => p.addEventListener('click', () => { keep = Number(p.dataset.id); paint(root); }));
  };
  showSheet('Объединить 2 задачи', '', btn('Объединить', { v: 'primary', block: true, id: 'mergeOk' }), root => {
    paint(root);
    root.querySelector('#mergeOk').addEventListener('click', e => doMerge(keep === a.id ? b : a, keep === a.id ? a : b, e.currentTarget));
  });
}

/* ---------------------------------------------------------------------- */
/* Task detail                                                            */
/* ---------------------------------------------------------------------- */

function isOpen(status) { return status === 'new' || status === 'in_progress' || status === 'snoozed'; }

async function renderTaskDetail(id, tok) {
  setTop({ title: 'Задача #' + id, back: true });
  setBottom('');
  app.innerHTML = skeleton(3);
  let t;
  try { t = await api('GET', `/api/tasks/${id}`); }
  catch (e) { if (tok !== state.rt) return; app.innerHTML = emptyState('alert', 'Не удалось загрузить', e.message, btn('Повторить', { sm: true, icon: 'refresh', act: 'refresh' })); return; }
  if (tok !== state.rt) return;
  state.task = t;
  const hu = t.helpdesk_user;
  setTop({ title: (t.helpdesk ? 'Тикет #' : 'Задача #') + t.id, back: true,
    subtitle: (t.helpdesk && state.bots.length ? botLabelFor(t.bot_id) + ' · ' : '') + (t.sender_name || ''),
    actions: hu || (t.helpdesk && t.chat_id) ? ibtn('message', 'Переписка', { act: 'open-dialog' }) : '' });
  app.innerHTML = taskDetailHtml(t);
  setBottom(taskActionBar(t));
}

function taskDetailHtml(t) {
  const hu = t.helpdesk_user;
  const sender = t.sender_username ? `<a class="tr-link" href="#" data-act="link" data-link="${esc(t.profile_url)}">${esc(t.sender_name)}</a> (@${esc(t.sender_username)})` : esc(t.sender_name || '—');
  const kv = [[t.helpdesk ? 'Пользователь' : 'От', sender], ['Создано', esc(fmtDT(t.created_at))]];
  if (t.deadline) kv.push(['Срок', esc(fmtDT(t.deadline)) + (t.overdue ? ' · просрочено' : '')]);
  if (t.status === 'snoozed' && t.snooze_until) kv.push(['Отложено до', esc(fmtDT(t.snooze_until))]);
  if (t.remind_at) kv.push(['Напомнить', esc(fmtDT(t.remind_at))]);
  if (hu && hu.source) kv.push(['Источник', esc(hu.source)]);
  const section = (title, inner) => `<section class="tr-card"><h3 class="tr-overline" style="margin:0 0 8px">${title}</h3>${inner}</section>`;
  const badges = [statusBadge(t.status, isOpen(t.status) || t.status === 'done' ? 'status-menu' : null), prioMark(t.priority, true),
    badge(esc(CATEGORY_LABEL[t.category] || t.category))];
  if (t.importance && t.importance !== 'medium') badges.push(badge('важность: ' + esc(IMPORTANCE_LABEL[t.importance] || t.importance)));
  if (t.forwarded) badges.push(badge('переслано'));
  if (t.merged_into) badges.push(badge('объединено в #' + t.merged_into, { icon: 'merge' }));
  return `<div class="tr-card tr-cardhead"><h2>${esc(t.title || '(без заголовка)')}</h2><div class="tr-row">${badges.join('')}</div>` +
    `<dl class="tr-kv">${kv.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('')}</dl>` +
    (hu ? `<div class="tr-row">${userBadges(hu)}</div>` : '') + `</div>` +
    (t.description ? section('Суть', `<p class="tr-textblock">${esc(t.description)}</p>`) : '') +
    (t.source_text ? section('Исходный текст', `<div class="tr-quote">${esc(t.source_text)}</div>`) : '') +
    (t.draft_reply && !t.reply_sent_at ? section(`Черновик ответа · ${esc(STRATEGY_LABEL[t.reply_strategy] || 'ответ')}`, `<p class="tr-textblock">${esc(t.draft_reply)}</p>`) : '') +
    (t.reply_sent_at ? section(`Ответ отправлен · ${esc(fmtDT(t.reply_sent_at))}`, `<p class="tr-textblock">${esc(t.reply_text)}</p>`) : '') +
    (t.provider ? `<div class="tr-cap" style="padding:0 4px">${esc(t.provider)} · ${esc(t.model)} · уверенность ${Math.round(t.confidence * 100)}%</div>` : '');
}

// The whole action set of the old screen lives behind three controls: leading action, Close, and "More".
function taskActionBar(t) {
  const more = ibtn('more', 'Ещё', { tone: 'tonal', act: 'more-menu' });
  if (!isOpen(t.status)) {
    return `<div class="tr-actionbar" role="toolbar"><div class="tr-actionbar__lead">${btn('Вернуть в работу', { v: 'primary', block: true, icon: 'refresh', act: 'reopen' })}</div>` +
      (t.forwarded ? '' : ibtn('reply', 'Написать', { tone: 'tonal', act: 'reply-open' })) + `</div>`;
  }
  let lead;
  if (t.forwarded) lead = btn('Закрыть', { v: 'primary', block: true, icon: 'check', act: 'close-open' });
  else if (t.draft_reply) lead = `<div class="tr-split"><button type="button" class="tr-btn tr-split__main" data-act="draft" aria-label="${t.reply_sent_at ? 'Отправить черновик ещё раз' : 'Отправить черновик'}">Отправить</button><button type="button" class="tr-btn tr-split__more" data-act="reply-open" aria-label="Изменить черновик перед отправкой">${I('chevron-down', 18)}</button></div>`;
  else lead = btn('Ответить', { v: 'primary', block: true, icon: 'reply', act: 'reply-open' });
  return `<div class="tr-actionbar" role="toolbar"><div class="tr-actionbar__lead">${lead}</div>` +
    (t.forwarded ? '' : btn('Закрыть', { icon: 'check', act: 'close-open' })) + more + `</div>`;
}

ACT['open-dialog'] = () => { const t = state.task; openView({ type: 'user', id: t.chat_id, bot: t.bot_id }); };

/* --- helpers for actions with undo --- */
function reloadTask(id) { if (state.view && state.view.type === 'task' && state.view.id === id) render(); }
async function restoreStatus(id, prev) {
  try { await api('POST', `/api/tasks/${id}/status`, { status: prev }); toast('Отменено'); } catch (e) { toast(e.message, { error: true }); }
  reloadTask(id);
}
async function setStatus(t, status, msg) {
  const prev = t.status;
  try {
    await api('POST', `/api/tasks/${t.id}/status`, { status });
    reloadTask(t.id);
    toast(msg, { undo: prev === 'new' || prev === 'in_progress' ? () => restoreStatus(t.id, prev) : null });
  } catch (e) { toast(e.message, { error: true }); }
}

ACT.reopen = () => setStatus(state.task, 'new', 'Возвращено в работу');
ACT.draft = el => withBusy(el, async () => {
  const t = state.task;
  await api('POST', `/api/tasks/${t.id}/draft`);
  reloadTask(t.id);
  toast('Черновик отправлен');
});

ACT['status-menu'] = () => {
  const t = state.task;
  openMenu('Статус', [
    { label: 'Новая', icon: 'sparkles', checked: t.status === 'new', onSelect: () => t.status !== 'new' && setStatus(t, 'new', 'Статус: новая') },
    { label: 'В работе', icon: 'eye', checked: t.status === 'in_progress', onSelect: () => t.status !== 'in_progress' && setStatus(t, 'in_progress', 'Взято в работу') },
    { label: 'Отложить…', icon: 'clock', checked: t.status === 'snoozed', onSelect: () => openWhen(t, 'snooze') },
    { label: 'Завершена', icon: 'check-circle', checked: t.status === 'done', onSelect: () => t.status !== 'done' && closeFlow(t) },
  ]);
};

ACT['more-menu'] = () => {
  const t = state.task, hu = t.helpdesk_user;
  const items = [];
  if (t.status !== 'in_progress') items.push({ label: 'Взять в работу', icon: 'eye', onSelect: () => setStatus(t, 'in_progress', 'Взято в работу') });
  items.push({ label: 'Отложить…', icon: 'clock', onSelect: () => openWhen(t, 'snooze') }, { divider: true },
    { label: 'Напомнить…', icon: 'bell', hint: t.remind_at ? fmtDT(t.remind_at) : '', onSelect: () => openWhen(t, 'remind') },
    { label: 'Редактировать', icon: 'edit', onSelect: () => openEdit(t) },
    { label: 'Объединить с…', icon: 'merge', onSelect: () => openMergePicker(t) });
  if (hu && hu.topic_url) items.push({ label: 'Тема в группе', icon: 'external', onSelect: () => openLink(hu.topic_url) });
  items.push({ divider: true }, { label: 'Не задача', icon: 'ban', danger: true, onSelect: () => setStatus(t, 'false_positive', 'Отмечено как «не задача»') });
  openMenu((t.helpdesk ? 'Тикет #' : 'Задача #') + t.id, items);
};

/* --- reply --- */
ACT['reply-open'] = () => {
  const t = state.task;
  showSheet(t.helpdesk ? 'Ответ пользователю' : 'Ответ собеседнику',
    `<div class="tr-field"><label class="tr-field__label" for="replyText">Текст ответа</label><textarea class="tr-input" id="replyText" style="min-height:140px" placeholder="Текст ответа…">${esc(t.draft_reply && !t.reply_sent_at ? t.draft_reply : '')}</textarea>` +
    `<div class="tr-field__hint">${t.helpdesk ? 'Уйдёт от имени бота' : 'Уйдёт от вашего имени'}</div></div>`,
    btn('Отправить', { v: 'primary', block: true, icon: 'send', id: 'replySend' }), root => {
      const ta = root.querySelector('#replyText'), b = root.querySelector('#replySend');
      const sync = () => { b.disabled = !ta.value.trim(); }; sync();
      ta.addEventListener('input', sync);
      b.addEventListener('click', () => withBusy(b, async () => {
        await api('POST', `/api/tasks/${t.id}/reply`, { text: ta.value.trim() });
        closeSheet(); toast('Ответ отправлен'); reloadTask(t.id);
      }));
    });
};

/* --- close --- */
const HELPDESK_DONE_TEXT = 'Ваше обращение решено. Если остались вопросы — просто напишите нам.';
ACT['close-open'] = () => closeFlow(state.task);

async function closeFlow(t) {
  const finish = async (body, withMessage) => {
    const prev = t.status;
    try {
      await api('POST', `/api/tasks/${t.id}/close`, body);
      closeSheet(); reloadTask(t.id);
      toast(withMessage ? 'Закрыто, сообщение отправлено' : 'Закрыто', { undo: !withMessage && (prev === 'new' || prev === 'in_progress') ? () => restoreStatus(t.id, prev) : null });
    } catch (e) { toast(e.message, { error: true }); }
  };
  const askSheet = (title, switchLabel, switchDesc, text, onSubmit) => {
    let on = true;
    showSheet(title,
      group([navRow({ label: switchLabel, desc: switchDesc, trailing: switchBtn(true, 'id="closeSwitch"', switchLabel) })]) +
      (text != null ? `<div class="tr-field" id="closeTextWrap"><label class="tr-field__label" for="closeText">Текст сообщения</label><textarea class="tr-input" id="closeText">${esc(text)}</textarea></div>` : ''),
      btn('Закрыть и отправить', { v: 'primary', block: true, id: 'closeGo' }), root => {
        const sw = root.querySelector('#closeSwitch'), go = root.querySelector('#closeGo'), wrap = root.querySelector('#closeTextWrap');
        sw.addEventListener('click', () => { on = !on; sw.setAttribute('aria-checked', String(on)); go.textContent = on ? 'Закрыть и отправить' : 'Закрыть'; if (wrap) wrap.hidden = !on; });
        go.addEventListener('click', () => withBusy(go, () => onSubmit(on, (root.querySelector('#closeText') || {}).value)));
      });
  };
  if (t.helpdesk && !t.forwarded) {
    askSheet('Закрыть тикет #' + t.id, 'Сообщить пользователю', 'Уйдёт от имени бота', HELPDESK_DONE_TEXT,
      (on, text) => on ? finish({ text: (text || '').trim() || HELPDESK_DONE_TEXT }, true) : finish({ send_message: false }, false));
    return;
  }
  let notify = false;
  if (isOwner() && !t.forwarded) {
    try { notify = (await loadSettings()).fields.find(f => f.key === 'task.notify_done_on_close').value; } catch (e) { /* ask nothing */ }
  }
  if (!notify) return finish({ send_message: false }, false);
  askSheet('Закрыть задачу #' + t.id, 'Отправить «Готово»', 'Собеседник получит сообщение перед закрытием', null,
    on => finish({ send_message: on }, on));
}

/* --- snooze / remind: one time picker, two modes --- */
function whenPresets(now) {
  const tom = new Date(now); tom.setDate(tom.getDate() + 1); tom.setHours(9, 0, 0, 0);
  const mon = new Date(now); mon.setDate(mon.getDate() + ((8 - mon.getDay()) % 7 || 7)); mon.setHours(9, 0, 0, 0);
  return [['30 минут', new Date(now.getTime() + 30 * 60000)], ['1 час', new Date(now.getTime() + 3600000)], ['3 часа', new Date(now.getTime() + 3 * 3600000)],
    ['Завтра, 09:00', tom], ['В понедельник', mon], ['Через неделю', new Date(now.getTime() + 7 * 864e5)]];
}
function openWhen(t, mode) {
  const now = new Date();
  const presets = whenPresets(now);
  const remind = mode === 'remind';
  const foot = (t.remind_at && remind ? btn('Убрать', { v: 'ghost', id: 'whenClear' }) : '') + btn('Выберите время', { v: 'primary', id: 'whenGo', disabled: true });
  const body = (isOpen(t.status) ? segmented('when', [{ value: 'snooze', label: 'Отложить задачу' }, { value: 'remind', label: 'Только напомнить' }], mode) : '') +
    `<div class="tr-when__grid">${presets.map((p, i) => `<button type="button" class="tr-chip" data-p="${i}">${esc(p[0])}</button>`).join('')}</div>` +
    `<div class="tr-field"><label class="tr-field__label" for="whenCustom">Другое время</label><input type="datetime-local" class="tr-input" id="whenCustom"></div>`;
  showSheet(remind ? 'Когда напомнить?' : 'Отложить до…', body, foot, root => {
    SEG.when = v => openWhen(t, v);
    const go = root.querySelector('#whenGo'), inp = root.querySelector('#whenCustom');
    let picked = null;
    inp.addEventListener('input', () => {
      const d = new Date(inp.value);
      picked = inp.value && !isNaN(d) && d > new Date() ? d : null;
      go.disabled = !picked;
      go.textContent = picked ? (remind ? 'Напомнить — ' : 'Отложить — ') + fmtWhen(picked, new Date()) : 'Выберите время';
    });
    go.addEventListener('click', () => picked && applyWhen(t, remind, picked));
    root.querySelectorAll('[data-p]').forEach(b => b.addEventListener('click', () => applyWhen(t, remind, presets[Number(b.dataset.p)][1])));
    const clr = root.querySelector('#whenClear');
    if (clr) clr.addEventListener('click', async () => {
      try { await api('POST', `/api/tasks/${t.id}/remind`, { at: '' }); closeSheet(); toast('Напоминание убрано'); reloadTask(t.id); } catch (e) { toast(e.message, { error: true }); }
    });
  });
}
async function applyWhen(t, remind, d) {
  const prevStatus = t.status, prevRemind = t.remind_at || '';
  try {
    if (remind) {
      await api('POST', `/api/tasks/${t.id}/remind`, { at: d.toISOString() });
      closeSheet(); reloadTask(t.id);
      toast('Напомним ' + fmtWhen(d), { undo: async () => { try { await api('POST', `/api/tasks/${t.id}/remind`, { at: prevRemind }); toast('Отменено'); } catch (e) { toast(e.message, { error: true }); } reloadTask(t.id); } });
    } else {
      await api('POST', `/api/tasks/${t.id}/snooze`, { until: d.toISOString() });
      closeSheet(); reloadTask(t.id);
      toast('Отложено до ' + fmtWhen(d), { undo: prevStatus === 'new' || prevStatus === 'in_progress' ? () => restoreStatus(t.id, prevStatus) : null });
    }
  } catch (e) { toast(e.message, { error: true }); }
}

/* --- edit --- */
function openEdit(t) {
  const opts = cur => PRIORITY_OPTIONS.map(([v, l]) => `<option value="${v}"${v === cur ? ' selected' : ''}>${l}</option>`).join('');
  showSheet('Редактировать',
    `<div class="tr-field"><label class="tr-field__label" for="editTitle">Заголовок</label><input class="tr-input" id="editTitle" value="${esc(t.title)}"></div>` +
    `<div class="tr-field"><label class="tr-field__label" for="editDescription">Суть</label><textarea class="tr-input" id="editDescription">${esc(t.description)}</textarea></div>` +
    `<div class="tr-row" style="flex-wrap:nowrap;align-items:flex-start"><div class="tr-field" style="flex:1"><label class="tr-field__label" for="editPriority">Срочность</label><select class="tr-input" id="editPriority">${opts(t.priority)}</select></div>` +
    `<div class="tr-field" style="flex:1"><label class="tr-field__label" for="editImportance">Важность</label><select class="tr-input" id="editImportance">${opts(t.importance || 'medium')}</select></div></div>`,
    btn('Сохранить', { v: 'primary', block: true, id: 'editSave' }), root => {
      root.querySelector('#editSave').addEventListener('click', e => {
        const title = root.querySelector('#editTitle').value.trim();
        if (!title) { toast('Введите заголовок', { error: true }); return; }
        withBusy(e.currentTarget, async () => {
          await api('POST', `/api/tasks/${t.id}/edit`, { title, description: root.querySelector('#editDescription').value.trim(),
            priority: root.querySelector('#editPriority').value, importance: root.querySelector('#editImportance').value });
          closeSheet(); toast('Сохранено'); reloadTask(t.id);
        });
      });
    });
}

/* --- merge picker: choose from a list, no IDs to remember --- */
const words = s => new Set(String(s || '').toLowerCase().replace(/[^\p{L}\p{N}\s]/gu, ' ').split(/\s+/).filter(w => w.length > 3));
function similarTo(t, other) {
  if (t.chat_id && t.chat_id === other.chat_id && (t.bot_id || 0) === (other.bot_id || 0)) return 'тот же отправитель';
  const a = words(t.title), b = words(other.title);
  let shared = 0; a.forEach(w => { if (b.has(w)) shared++; });
  if (shared >= 2 || (shared >= 1 && shared / Math.min(a.size, b.size) >= 0.5)) return 'похожее название';
  return '';
}
async function openMergePicker(t) {
  const scope = isOwner() ? 'all' : 'helpdesk';
  let tasks = [];
  try {
    const [a, b] = await Promise.all([api('GET', `/api/tasks?scope=${scope}&status=act&limit=200`), api('GET', `/api/tasks?scope=${scope}&status=snz&limit=200`)]);
    tasks = (a.items || []).concat(b.items || []).filter(x => x.id !== t.id && !x.merged_into);
  } catch (e) { toast(e.message, { error: true }); return; }
  const sug = tasks.map(x => ({ t: x, why: similarTo(t, x) })).filter(s => s.why).sort((x, y) => (x.why === 'тот же отправитель' ? 0 : 1) - (y.why === 'тот же отправитель' ? 0 : 1)).slice(0, 4);
  const sugIds = new Set(sug.map(s => s.t.id));
  const recent = tasks.filter(x => !sugIds.has(x.id)).slice(0, 8);
  let chosen = null;
  showSheet('Объединить с…',
    `<div class="tr-search">${I('search', 20)}<input class="tr-input" id="mpSearch" type="search" placeholder="Название, отправитель или №" aria-label="Поиск"></div><div id="mpList" role="radiogroup" aria-label="Задача" class="tr-stack" style="gap:8px"></div>`,
    `<div id="mpPlan" style="flex:1 0 100%" hidden></div>` + btn('Выберите задачу', { v: 'primary', block: true, id: 'mpOk', disabled: true }), root => {
      const list = root.querySelector('#mpList'), ok = root.querySelector('#mpOk'), plan = root.querySelector('#mpPlan');
      const paint = () => {
        const q = root.querySelector('#mpSearch').value.trim().toLowerCase().replace(/^#/, '');
        let html;
        if (q) {
          const found = tasks.filter(x => `${x.title} ${x.sender_name}`.toLowerCase().includes(q) || String(x.id).startsWith(q));
          html = found.length ? `<h3 class="tr-overline">Найдено: ${found.length}</h3>` + found.map(x => pickRow(x, { checked: chosen === x.id })).join('') : emptyState('search', 'Ничего не нашлось', 'Попробуйте часть названия или имя отправителя.');
        } else {
          html = (sug.length ? `<h3 class="tr-overline">Похожие</h3>` + sug.map(s => pickRow(s.t, { checked: chosen === s.t.id, why: s.why })).join('') : '') +
            (recent.length ? `<h3 class="tr-overline" style="margin-top:8px">Недавние</h3>` + recent.map(x => pickRow(x, { checked: chosen === x.id })).join('') : '') ||
            emptyState('check-circle', 'Других открытых задач нет', '');
        }
        list.innerHTML = html;
      };
      paint();
      root.querySelector('#mpSearch').addEventListener('input', paint);
      list.addEventListener('click', e => {
        const p = e.target.closest('.tr-pick'); if (!p) return;
        chosen = Number(p.dataset.id);
        const target = tasks.find(x => x.id === chosen);
        plan.hidden = false; plan.innerHTML = mergePlan(t, target);
        ok.disabled = false; ok.textContent = `Объединить с #${chosen}`;
        paint();
      });
      ok.addEventListener('click', () => { const target = tasks.find(x => x.id === chosen); if (target) doMerge(t, target, ok); });
    });
}

/* ---------------------------------------------------------------------- */
/* Dialogs (helpdesk users)                                               */
/* ---------------------------------------------------------------------- */

function userBadges(u) {
  const b = [];
  if (u.awaiting_since) b.push(badge('ждёт ' + esc(waitedFor(u.awaiting_since)), { tone: 'warning', icon: 'clock' }));
  if (u.blocked) b.push(badge('заблокировал бота', { tone: 'danger', icon: 'ban' }));
  if (u.topic_closed) b.push(badge('тема закрыта', { icon: 'lock' }));
  if (u.source) b.push(badge(esc(u.source), { icon: 'external' }));
  return b.join('');
}

async function renderDialogs(tok) {
  const f = state.dialogsFilter;
  setTop({ title: 'Диалоги', large: true, actions: ibtn('refresh', 'Обновить', { act: 'refresh' }) });
  setBottom(tabbarHtml());
  if (!state.me.helpdesk) {
    app.innerHTML = emptyState('headset', 'Хелпдеск ' + (state.me.helpdesk_enabled ? 'включён, но не указана супергруппа' : 'выключен'), '',
      isOwner() ? btn('Открыть настройки', { v: 'primary', act: 'goto-helpdesk-settings' }) : '');
    return;
  }
  const showBot = isOwner() && state.bots.length > 0;
  app.innerHTML = `<div class="tr-filterbar"><div class="tr-filterbar__top"><div class="tr-search">${I('search', 20)}<input class="tr-input" id="dlSearch" type="search" placeholder="Имя, @username или ID" aria-label="Поиск" value="${esc(f.q)}"></div>` +
    (showBot ? `<button type="button" class="tr-filterbtn" data-act="dl-filters" data-active="${f.bot !== 'all'}" aria-label="Фильтры">${I('filter', 20)}${f.bot !== 'all' ? '<span class="tr-filterbtn__n">1</span>' : ''}</button>` : '') + `</div>` +
    segmented('dfilter', [{ value: 'awaiting', label: 'Ждут ответа', count: state.awaiting || undefined }, { value: 'all', label: 'Все' }], f.filter) + `</div><div id="dlBody">${skeleton(4)}</div>`;
  await loadDialogItems(tok || state.rt);
}
SEG.dfilter = v => { state.dialogsFilter.filter = v; state.dialogsFilter.limit = 50; render(); };
ACT['goto-helpdesk-settings'] = () => { state.openGroups.add('helpdesk'); state.tab = 'settings'; openView({ type: 'settings', id: 'helpdesk' }); };
ACT['dl-filters'] = () => {
  const f = state.dialogsFilter;
  const opts = [['all', 'Все боты'], ['0', 'Основной']].concat(state.bots.map(b => [String(b.id), b.label || ('#' + b.id)]));
  showSheet('Фильтры', `<div><h3 class="tr-overline">Бот</h3><div class="tr-chips">${opts.map(o => chip(o[1], { selected: f.bot === o[0], attrs: `data-v="${esc(o[0])}"` })).join('')}</div></div>`,
    btn('Готово', { v: 'primary', block: true, id: 'dfDone' }), root => {
      root.querySelectorAll('.tr-chip').forEach(c => c.addEventListener('click', () => { f.bot = c.dataset.v; f.limit = 50; closeSheet(); render(); }));
      root.querySelector('#dfDone').addEventListener('click', closeSheet);
    });
};

async function loadDialogItems(tok) {
  tok = typeof tok === 'number' ? tok : state.rt;
  const f = state.dialogsFilter;
  const body = document.getElementById('dlBody');
  if (!body) return;
  try {
    const data = await api('GET', `/api/helpdesk/users?filter=${f.filter}&q=${encodeURIComponent(f.q.trim())}&bot=${f.bot}&limit=${f.limit}&offset=0`);
    if (tok !== state.rt || !document.getElementById('dlBody')) return;
    if (f.filter === 'awaiting' && !f.q.trim()) state.awaiting = data.total;
    if (!data.items.length) {
      document.getElementById('dlBody').innerHTML = f.filter === 'awaiting' && !f.q.trim() ? emptyState('check-circle', 'Все ответы отправлены', 'Новые обращения появятся здесь.') : emptyState('search', 'Никого не нашлось', '');
      return;
    }
    const more = data.items.length < data.total;
    document.getElementById('dlBody').innerHTML = `<div class="tr-list">${data.items.map(dialogRow).join('')}</div>` +
      (more ? `<div class="tr-more">${btn('Показать ещё', { v: 'ghost', act: 'more-dialogs' })}<span class="tr-cap">${data.items.length} из ${data.total}</span></div>` : '');
    const nav = bottom.querySelector('.tr-tabbar'); if (nav) bottom.innerHTML = tabbarHtml();
  } catch (e) {
    if (tok === state.rt && document.getElementById('dlBody')) document.getElementById('dlBody').innerHTML = emptyState('alert', 'Не удалось загрузить', e.message, btn('Повторить', { sm: true, icon: 'refresh', act: 'refresh' }));
  }
}
ACT['more-dialogs'] = () => { state.dialogsFilter.limit += 50; render(); };

function dialogRow(u) {
  const bot = state.bots.length ? `<span>${esc(botLabelFor(u.bot_id))}</span>` : '';
  const badges = userBadges(u);
  return `<button type="button" class="tr-task tr-dialog" data-act="open-user" data-id="${u.user_id}" data-bot="${u.bot_id}">${avatar(u.name)}` +
    `<span class="tr-task__body"><span class="tr-task__top"><span class="tr-dialog__name" style="flex:1 1 auto">${esc(u.name || ('id' + u.user_id))}${u.username ? `<span> @${esc(u.username)}</span>` : ''}</span><span class="tr-task__time">${esc(fmtDT(u.last_message_at))}</span></span>` +
    (bot || badges ? `<span class="tr-task__meta">${bot}${badges}</span>` : '') + `</span></button>`;
}
ACT['open-user'] = el => openView({ type: 'user', id: Number(el.dataset.id), bot: Number(el.dataset.bot || 0) });

async function renderDialog(userID, botID, tok) {
  setTop({ title: 'Диалог', back: true });
  setBottom('');
  app.innerHTML = skeleton(3);
  let d;
  try { d = await api('GET', `/api/helpdesk/users/${userID}?bot=${botID || 0}`); }
  catch (e) { if (tok !== state.rt) return; app.innerHTML = emptyState('alert', 'Не удалось загрузить', e.message, btn('Повторить', { sm: true, icon: 'refresh', act: 'refresh' })); return; }
  if (tok !== state.rt) return;
  state.dialog = { d, userID, botID: botID || 0 };
  const u = d.user;
  setTop({ title: u.name || 'Диалог', subtitle: (u.username ? '@' + u.username + ' · ' : '') + 'ID ' + u.user_id, back: true, actions: ibtn('more', 'Ещё', { act: 'dialog-menu' }) });
  const open = d.tickets.filter(t => isOpen(t.status));
  app.innerHTML =
    `<div class="tr-card"><div class="tr-profile">${avatar(u.name, 'lg')}<div class="tr-profile__text"><b>${esc(u.name)}</b><span class="tr-cap">${u.username ? `<a class="tr-link" href="#" data-act="link" data-link="${esc(u.profile_url)}">@${esc(u.username)}</a>` : 'без username'}${u.language_code ? ' · ' + esc(u.language_code) : ''}</span></div></div>` +
    (userBadges(u) ? `<div class="tr-row" style="margin-top:12px">${userBadges(u)}</div>` : '') + `</div>` +
    (d.tickets.length ? `<section><h3 class="tr-overline">Тикеты · ${open.length} открыто</h3><div class="tr-list">${d.tickets.map(t => taskCard(t)).join('')}</div></section>` : '') +
    `<section><h3 class="tr-overline">Переписка</h3><div class="tr-chat" id="chat">${d.messages.length ? d.messages.map(bubble).join('') : emptyState('message', 'Сообщений нет', '')}</div></section>`;
  setBottom(`<div class="tr-composer"><textarea id="hdReply" rows="1" placeholder="Ответ — уйдёт от имени бота" aria-label="Сообщение"></textarea>${ibtn('send', 'Отправить', { tone: 'fill', id: 'hdSend', act: 'hd-send', disabled: true })}</div>`);
  window.scrollTo(0, document.body.scrollHeight);
}
function bubble(m) { return `<div class="tr-bubble ${m.outgoing ? 'tr-bubble--out' : 'tr-bubble--in'}"><div>${esc(m.text)}</div><time>${esc(fmtDT(m.sent_at))}</time></div>`; }

ACT['hd-send'] = el => {
  const { userID, botID } = state.dialog;
  const ta = document.getElementById('hdReply');
  const text = ta.value.trim();
  if (!text) return;
  withBusy(el, async () => {
    await api('POST', `/api/helpdesk/users/${userID}/reply?bot=${botID}`, { text });
    toast('Отправлено');
    render();
  });
};
ACT['dialog-menu'] = () => {
  const { d, userID, botID } = state.dialog, u = d.user;
  const items = [];
  if (u.topic_url) items.push({ label: 'Тема в группе', icon: 'external', onSelect: () => openLink(u.topic_url) });
  items.push({ label: u.topic_closed ? 'Открыть тему' : 'Закрыть тему', icon: u.topic_closed ? 'unlock' : 'lock', onSelect: async () => {
    try { await api('POST', `/api/helpdesk/users/${userID}/topic?bot=${botID}`, { closed: !u.topic_closed }); toast(u.topic_closed ? 'Тема открыта' : 'Тема закрыта'); render(); } catch (e) { toast(e.message, { error: true }); }
  } });
  items.push({ label: 'Создать тикет', icon: 'plus', onSelect: async () => {
    try { toast('Оформляю тикет…'); const t = await api('POST', `/api/helpdesk/users/${userID}/ticket?bot=${botID}`); openView({ type: 'task', id: t.id }); toast('Тикет #' + t.id + ' создан'); } catch (e) { toast(e.message, { error: true }); }
  } });
  openMenu(u.name || 'Диалог', items);
};
