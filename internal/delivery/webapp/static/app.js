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
} else {
  document.getElementById('devBanner').hidden = false;
}

function applyTheme() {
  const p = tg.themeParams || {};
  const root = document.documentElement.style;
  const map = {
    bg: p.bg_color, secondaryBg: p.secondary_bg_color || p.bg_color, text: p.text_color,
    hint: p.hint_color, link: p.link_color, button: p.button_color, buttonText: p.button_text_color,
  };
  if (map.bg) root.setProperty('--bg', map.bg);
  if (map.secondaryBg) root.setProperty('--secondary-bg', map.secondaryBg);
  if (map.text) root.setProperty('--text', map.text);
  if (map.hint) root.setProperty('--hint', map.hint);
  if (map.link) root.setProperty('--link', map.link);
  if (map.button) root.setProperty('--button', map.button);
  if (map.buttonText) root.setProperty('--button-text', map.buttonText);
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

/* ---------------------------------------------------------------------- */
/* API helper                                                             */
/* ---------------------------------------------------------------------- */

async function api(method, path, body) {
  const headers = { 'Content-Type': 'application/json' };
  if (inTelegram) headers['Authorization'] = 'tma ' + tg.initData;
  const res = await fetch(path, { method, headers, body: body !== undefined ? JSON.stringify(body) : undefined });
  let data = null;
  try { data = await res.json(); } catch (e) {}
  if (!res.ok) {
    const msg = (data && data.error) || ('HTTP ' + res.status);
    throw new Error(msg);
  }
  return data;
}

/* ---------------------------------------------------------------------- */
/* State & navigation                                                     */
/* ---------------------------------------------------------------------- */

const state = {
  tab: 'tasks',
  taskId: null,
  tasksFilter: { status: 'act', priority: 'all', offset: 0 },
  historyFilter: { status: 'done', priority: 'all', offset: 0 },
  settings: null,
  lastTaskList: null,
};

const PAGE_SIZE = 20;

const app = document.getElementById('app');
const topTitle = document.getElementById('topTitle');
const backBtn = document.getElementById('backBtn');
const refreshBtn = document.getElementById('refreshBtn');

document.querySelectorAll('.tab').forEach(btn => {
  btn.addEventListener('click', () => { state.taskId = null; switchTab(btn.dataset.tab); });
});
backBtn.addEventListener('click', closeDetail);
refreshBtn.addEventListener('click', () => render());
if (tg && tg.BackButton) tg.BackButton.onClick(closeDetail);

function switchTab(tab) {
  state.tab = tab;
  document.querySelectorAll('.tab').forEach(b => b.classList.toggle('active', b.dataset.tab === tab));
  render();
}

function openDetail(id) {
  state.taskId = id;
  render();
}

function closeDetail() {
  if (state.taskId == null) return;
  state.taskId = null;
  render();
}

function render() {
  backBtn.hidden = state.taskId == null;
  if (tg && tg.BackButton) { if (state.taskId != null) tg.BackButton.show(); else tg.BackButton.hide(); }

  if (state.taskId != null) { topTitle.textContent = 'Задача #' + state.taskId; renderTaskDetail(state.taskId); return; }
  switch (state.tab) {
    case 'tasks': topTitle.textContent = 'Задачи'; renderTaskList('tasks'); break;
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
  const sameYear = d.getFullYear() === now.getFullYear();
  return d.toLocaleString('ru-RU', {
    day: '2-digit', month: '2-digit', year: sameYear ? undefined : '2-digit',
    hour: '2-digit', minute: '2-digit',
  });
}

const PRIORITY_LABEL = { critical: 'Критический', high: 'Высокий', medium: 'Средний', low: 'Низкий' };
const PRIORITY_EMOJI = { critical: '🔴', high: '🟠', medium: '🟡', low: '🟢' };
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
  toast._t = setTimeout(() => { el.hidden = true; }, 2600);
}

function showModal(html, onMount) {
  const root = document.getElementById('modalRoot');
  root.innerHTML = `<div class="modal-backdrop" id="modalBackdrop"><div class="modal-sheet">${html}</div></div>`;
  document.getElementById('modalBackdrop').addEventListener('click', e => { if (e.target.id === 'modalBackdrop') closeModal(); });
  if (onMount) onMount(root);
}
function closeModal() { document.getElementById('modalRoot').innerHTML = ''; }

async function withBusy(btn, fn) {
  if (btn) btn.disabled = true;
  try { await fn(); }
  catch (e) { toast(e.message || String(e), true); }
  finally { if (btn) btn.disabled = false; }
}

/* ---------------------------------------------------------------------- */
/* Task list (tabs: tasks / history)                                      */
/* ---------------------------------------------------------------------- */

const STATUS_CHIPS_TASKS = [['act', 'Активные'], ['new', 'Новые'], ['wrk', 'В работе'], ['snz', 'Отложенные']];
const STATUS_CHIPS_HISTORY = [['done', 'Завершённые'], ['fp', 'Ошибки'], ['all', 'Все']];
const PRIO_CHIPS = [['all', 'Все'], ['urg', '🔥 Срочные'], ['crit', '🔴'], ['high', '🟠'], ['med', '🟡'], ['low', '🟢']];

async function renderTaskList(tab) {
  const filter = tab === 'tasks' ? state.tasksFilter : state.historyFilter;
  const chips = tab === 'tasks' ? STATUS_CHIPS_TASKS : STATUS_CHIPS_HISTORY;

  let overviewHtml = '';
  if (tab === 'tasks') {
    try {
      const o = await api('GET', '/api/overview');
      overviewHtml = renderOverview(o);
    } catch (e) { /* non-fatal */ }
  }

  app.innerHTML = overviewHtml +
    chipRow('sfilter', chips, filter.status) +
    chipRow('pfilter', PRIO_CHIPS, filter.priority) +
    `<div id="listBody" class="loading">Загрузка…</div>`;

  wireChips('sfilter', v => { filter.status = v; filter.offset = 0; renderTaskList(tab); });
  wireChips('pfilter', v => { filter.priority = v; filter.offset = 0; renderTaskList(tab); });

  try {
    const data = await api('GET', `/api/tasks?status=${filter.status}&priority=${filter.priority}&limit=${PAGE_SIZE}&offset=${filter.offset}`);
    renderTaskListBody(data, filter, tab);
  } catch (e) {
    document.getElementById('listBody').innerHTML = `<div class="empty">⚠️ ${esc(e.message)}</div>`;
  }
}

function renderOverview(o) {
  let conn = '';
  if (!o.connection) {
    conn = `<div class="conn-banner bad">🔌 Telegram Business не подключён</div>`;
  } else if (!o.connection.enabled) {
    conn = `<div class="conn-banner bad">⏸ Business-подключение отключено</div>`;
  } else if (!o.connection.can_reply) {
    conn = `<div class="conn-banner bad">⚠️ Нет права отвечать за вас</div>`;
  } else {
    conn = `<div class="conn-banner ok">✅ Подключено: ${esc(o.connection.name)}</div>`;
  }
  return conn + `<div class="overview">
    <div class="stat"><b>${o.new}</b><span>Новые</span></div>
    <div class="stat"><b>${o.in_progress}</b><span>В работе</span></div>
    <div class="stat"><b>${o.snoozed}</b><span>Отложено</span></div>
    <div class="stat ${o.overdue ? 'warn' : ''}"><b>${o.overdue}</b><span>Просрочено</span></div>
  </div>`;
}

function chipRow(name, options, active) {
  return `<div class="chiprow" data-chips="${name}">` +
    options.map(([v, label]) => `<button class="chip${v === active ? ' active' : ''}" data-value="${v}">${esc(label)}</button>`).join('') +
    `</div>`;
}
function wireChips(name, onPick) {
  document.querySelectorAll(`[data-chips="${name}"] .chip`).forEach(btn => {
    btn.addEventListener('click', () => onPick(btn.dataset.value));
  });
}

function renderTaskListBody(data, filter, tab) {
  const body = document.getElementById('listBody');
  if (!data.items || data.items.length === 0) {
    body.innerHTML = `<div class="empty">Здесь пусто 🎉</div>`;
    return;
  }
  body.innerHTML = data.items.map(taskCard).join('') + pager(data, filter, tab);
  body.querySelectorAll('[data-open-task]').forEach(el => {
    el.addEventListener('click', () => openDetail(Number(el.dataset.openTask)));
  });
  body.querySelectorAll('[data-page]').forEach(el => {
    el.addEventListener('click', () => { filter.offset = Number(el.dataset.page); renderTaskList(tab); });
  });
}

function taskCard(t) {
  const meta = [`👤 ${esc(t.sender_name || '—')}`, CATEGORY_LABEL[t.category] || t.category];
  if (t.status !== 'new' && t.status !== 'in_progress') meta.push(`<span class="badge status-${t.status}">${STATUS_LABEL[t.status] || t.status}</span>`);
  if (t.deadline) meta.push(`📅 ${fmtDT(t.deadline)}` + (t.overdue ? ` <span class="badge overdue">просрочено</span>` : ''));
  if (t.status === 'snoozed' && t.snooze_until) meta.push(`⏰ до ${fmtDT(t.snooze_until)}`);
  return `<div class="card task-card" data-open-task="${t.id}">
    <div class="task-top">
      <span class="dot ${t.priority}"></span>
      <span class="task-title">#${t.id} ${esc(t.title || '(без заголовка)')}</span>
    </div>
    <div class="task-meta">${meta.join(' · ')}</div>
  </div>`;
}

function pager(data, filter, tab) {
  const hasPrev = data.offset > 0;
  const hasNext = data.offset + data.items.length < data.total;
  if (!hasPrev && !hasNext) return '';
  const prevOffset = Math.max(0, data.offset - data.limit);
  const nextOffset = data.offset + data.limit;
  return `<div class="actions">
    ${hasPrev ? `<button class="btn ghost" data-page="${prevOffset}">◀️ Назад</button>` : ''}
    <span class="btn ghost" style="pointer-events:none">${data.offset + 1}–${data.offset + data.items.length} из ${data.total}</span>
    ${hasNext ? `<button class="btn ghost" data-page="${nextOffset}">Вперёд ▶️</button>` : ''}
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
  app.innerHTML = taskDetailHtml(t);
  wireTaskDetail(t);
}

function taskDetailHtml(t) {
  const senderLine = t.sender_username
    ? `<a class="plain" href="${esc(t.profile_url)}" target="_blank" rel="noopener">${esc(t.sender_name)}</a> (@${esc(t.sender_username)})`
    : esc(t.sender_name || '—');
  return `
  <div class="card">
    <div class="task-top">
      <span class="dot ${t.priority}"></span>
      <span class="task-title">${esc(t.title || '(без заголовка)')}</span>
    </div>
    <div class="task-meta" style="margin-top:6px">
      <span class="badge status-${t.status}">${STATUS_LABEL[t.status] || t.status}</span>
      <span class="badge">${PRIORITY_EMOJI[t.priority] || ''} ${PRIORITY_LABEL[t.priority] || t.priority}</span>
      <span class="badge">${CATEGORY_LABEL[t.category] || t.category}</span>
      ${t.forwarded ? '<span class="badge">📨 Переслано</span>' : ''}
    </div>
    <div class="kv" style="margin-top:10px">
      <div><span>От: </span>${senderLine}</div>
      <div><span>Получено: </span>${fmtDT(t.created_at)}</div>
      ${t.deadline ? `<div><span>Дедлайн: </span>${fmtDT(t.deadline)}${t.overdue ? ' ⚠️ просрочено' : ''}</div>` : ''}
      ${t.status === 'snoozed' && t.snooze_until ? `<div><span>Отложено до: </span>${fmtDT(t.snooze_until)}</div>` : ''}
    </div>
  </div>

  ${t.description ? `<div class="card detail-section"><h3>Суть</h3><p>${esc(t.description)}</p></div>` : ''}
  ${t.source_text ? `<div class="card detail-section"><h3>Исходный текст</h3><div class="quote">${esc(t.source_text)}</div></div>` : ''}
  ${t.draft_reply && !t.reply_sent_at ? `<div class="card detail-section"><h3>Черновик ответа (${STRATEGY_LABEL[t.reply_strategy] || ''})</h3><p>${esc(t.draft_reply)}</p></div>` : ''}
  ${t.reply_sent_at ? `<div class="card detail-section"><h3>Ответ отправлен · ${fmtDT(t.reply_sent_at)}</h3><p>${esc(t.reply_text)}</p></div>` : ''}
  ${t.provider ? `<div class="card detail-section" style="color:var(--hint);font-size:12px">🤖 ${esc(t.provider)} · ${esc(t.model)} · уверенность ${Math.round(t.confidence * 100)}%</div>` : ''}

  <div id="replyBox"></div>
  <div id="snoozeBox"></div>

  <div class="card actions">
    ${isOpen(t.status) ? actionButtons(t) : reopenButtons(t)}
  </div>`;
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
  html += `<button class="btn danger" data-act="fp">🗑 Ошибка</button>`;
  return html;
}
function reopenButtons(t) {
  return `<button class="btn primary" data-act="reopen">♻️ Вернуть в работу</button>` +
    (t.forwarded ? '' : `<button class="btn" data-act="reply-open">✏️ Написать</button>`);
}

function wireTaskDetail(t) {
  app.querySelectorAll('[data-act]').forEach(btn => {
    btn.addEventListener('click', () => handleTaskAction(t, btn.dataset.act, btn));
  });
}

async function handleTaskAction(t, action, btn) {
  switch (action) {
    case 'draft':
      await withBusy(btn, async () => { await api('POST', `/api/tasks/${t.id}/draft`); toast('🚀 Черновик отправлен'); renderTaskDetail(t.id); });
      break;
    case 'work':
      await withBusy(btn, async () => { await api('POST', `/api/tasks/${t.id}/status`, { status: 'in_progress' }); toast('👀 Взято в работу'); renderTaskDetail(t.id); });
      break;
    case 'fp':
      await withBusy(btn, async () => { await api('POST', `/api/tasks/${t.id}/status`, { status: 'false_positive' }); toast('🗑 Отмечено как ошибка'); renderTaskDetail(t.id); });
      break;
    case 'reopen':
      await withBusy(btn, async () => { await api('POST', `/api/tasks/${t.id}/status`, { status: 'new' }); toast('♻️ Задача возвращена'); renderTaskDetail(t.id); });
      break;
    case 'close':
      await handleClose(t);
      break;
    case 'reply-open':
      openReplyBox(t);
      break;
    case 'snooze-open':
      openSnoozeBox(t);
      break;
  }
}

async function handleClose(t) {
  const notify = state.settings ? state.settings.notify_done_on_close : (await loadSettings()).notify_done_on_close;
  if (!notify || t.forwarded) {
    await api('POST', `/api/tasks/${t.id}/close`, { send_message: false });
    toast('✅ Задача закрыта');
    renderTaskDetail(t.id);
    return;
  }
  showModal(`
    <h3>Закрыть задачу?</h3>
    <p style="color:var(--hint);font-size:13px;margin:0">Отправить собеседнику «Готово!» перед закрытием?</p>
    <div class="actions">
      <button class="btn primary full" id="closeYes">✅ Закрыть + отправить «Готово!»</button>
      <button class="btn full" id="closeNo">Просто закрыть</button>
      <button class="btn ghost full" id="closeCancel">Отмена</button>
    </div>`, root => {
    root.querySelector('#closeYes').addEventListener('click', async () => {
      closeModal();
      try { await api('POST', `/api/tasks/${t.id}/close`, { send_message: true }); toast('✅ Закрыто, отправлено «Готово!»'); renderTaskDetail(t.id); }
      catch (e) { toast(e.message, true); }
    });
    root.querySelector('#closeNo').addEventListener('click', async () => {
      closeModal();
      try { await api('POST', `/api/tasks/${t.id}/close`, { send_message: false }); toast('✅ Задача закрыта'); renderTaskDetail(t.id); }
      catch (e) { toast(e.message, true); }
    });
    root.querySelector('#closeCancel').addEventListener('click', closeModal);
  });
}

function openReplyBox(t) {
  const box = document.getElementById('replyBox');
  box.innerHTML = `<div class="card detail-section">
    <h3>Ваш ответ собеседнику</h3>
    <textarea class="input" id="replyText" placeholder="Текст ответа…">${esc(t.draft_reply || '')}</textarea>
    <div class="actions">
      <button class="btn primary full" id="replySend">📤 Отправить</button>
    </div>
  </div>`;
  box.scrollIntoView({ behavior: 'smooth', block: 'center' });
  document.getElementById('replySend').addEventListener('click', async (e) => {
    const text = document.getElementById('replyText').value.trim();
    if (!text) { toast('Введите текст', true); return; }
    await withBusy(e.target, async () => {
      await api('POST', `/api/tasks/${t.id}/reply`, { text });
      toast('📤 Ответ отправлен');
      renderTaskDetail(t.id);
    });
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
    <div class="actions">
      <button class="btn primary full" id="snoozeCustomBtn">⏰ Отложить до указанного времени</button>
    </div>
  </div>`;
  box.scrollIntoView({ behavior: 'smooth', block: 'center' });

  const doSnooze = async (untilISO) => {
    try { await api('POST', `/api/tasks/${t.id}/snooze`, { until: untilISO }); toast('⏰ Отложено'); renderTaskDetail(t.id); }
    catch (e) { toast(e.message, true); }
  };
  box.querySelectorAll('[data-min]').forEach(btn => {
    btn.addEventListener('click', () => doSnooze(new Date(Date.now() + Number(btn.dataset.min) * 60000).toISOString()));
  });
  document.getElementById('snoozeTomorrow').addEventListener('click', () => {
    const d = new Date(); d.setDate(d.getDate() + 1); d.setHours(9, 0, 0, 0);
    doSnooze(d.toISOString());
  });
  document.getElementById('snoozeCustomBtn').addEventListener('click', () => {
    const v = document.getElementById('snoozeCustom').value;
    if (!v) { toast('Укажите дату и время', true); return; }
    doSnooze(new Date(v).toISOString());
  });
}

/* ---------------------------------------------------------------------- */
/* Settings                                                                */
/* ---------------------------------------------------------------------- */

async function loadSettings() {
  state.settings = await api('GET', '/api/settings');
  return state.settings;
}

async function renderSettings() {
  app.innerHTML = `<div class="loading">Загрузка…</div>`;
  let s;
  try { s = await loadSettings(); }
  catch (e) { app.innerHTML = `<div class="empty">⚠️ ${esc(e.message)}</div>`; return; }
  app.innerHTML = settingsHtml(s);
  wireSettings(s);
}

const PROVIDERS = [
  ['claude', 'Claude'], ['gemini', 'Gemini'], ['groq', 'Groq'], ['mistral', 'Mistral'], ['openrouter', 'OpenRouter'],
];

function settingsHtml(s) {
  const provBtn = (p, label) => `<button class="${p === s.active_provider ? 'active' : ''}" data-provider="${p}">${label}${s.providers.includes(p) ? '' : ' 🔒'}</button>`;
  const debChip = (v) => `<button class="chip${v === s.debounce_seconds ? ' active' : ''}" data-deb="${v}">${v}с</button>`;
  const sensBtn = (v, label) => `<button class="${v === s.sensitivity ? 'active' : ''}" data-sens="${v}">${label}</button>`;
  const modelRow = (p, label) => `<div class="setting-row">
      <div><div class="label">Модель ${label}</div></div>
      <select class="input" id="model_${p}" data-model-provider="${p}" style="width:auto;max-width:60%">${modelOptions(s[p + '_presets'], s[p + '_model'])}</select>
    </div>`;

  const activeLabel = PROVIDERS.find(([p]) => p === s.active_provider)?.[1] || s.active_provider;

  return `
  <div class="card">
    <div class="section-title" style="margin-top:0">AI-провайдер</div>
    <div class="segmented">${PROVIDERS.slice(0, 3).map(([p, l]) => provBtn(p, l)).join('')}</div>
    <div class="segmented" style="margin-top:6px">${PROVIDERS.slice(3).map(([p, l]) => provBtn(p, l)).join('')}</div>
    ${modelRow(s.active_provider, activeLabel)}
    <button class="btn full" id="testProvider" style="margin-top:8px">🧪 Проверить провайдера</button>
    <div id="testResult"></div>
  </div>

  <div class="card">
    <div class="section-title" style="margin-top:0">Триаж</div>
    <div class="setting-row"><div class="label">Дебаунс (склейка сообщений)</div></div>
    <div class="chiprow">${[10, 20, 30, 60].map(debChip).join('')}</div>
    <div class="setting-row">
      <div><div class="label">Чувствительность</div></div>
    </div>
    <div class="segmented">${sensBtn('low', 'Низкая')}${sensBtn('medium', 'Средняя')}${sensBtn('high', 'Высокая')}</div>
    ${toggleRow('paused', '⏸ Пауза триажа', 'Сообщения сохраняются, но не анализируются', s.triage_paused)}
  </div>

  <div class="card">
    <div class="section-title" style="margin-top:0">Поведение</div>
    ${toggleRow('markread', '👁 Отмечать прочитанным', 'При переводе задачи «В работу»', s.mark_read_on_work)}
    ${toggleRow('notifydone', '💬 Спрашивать про «Готово!»', 'При закрытии задачи предлагать отправить сообщение', s.notify_done_on_close)}
    ${toggleRow('digest', '🌅 Утренний дайджест', 'Список висящих и просроченных задач', s.digest_enabled)}
    <div class="setting-row">
      <div class="label">Время дайджеста</div>
      <input type="time" class="input" id="digestTime" value="${esc(s.digest_time)}" style="width:auto">
    </div>
  </div>

  <div class="card">
    <button class="btn danger full" id="resetSettings">♻️ Сбросить к .env</button>
  </div>`;
}

function modelOptions(presets, current) {
  const all = presets.includes(current) ? presets : [current, ...presets];
  return all.map(m => `<option value="${esc(m)}" ${m === current ? 'selected' : ''}>${esc(m)}</option>`).join('');
}

function toggleRow(id, label, desc, on) {
  return `<div class="setting-row">
    <div><div class="label">${label}</div><div class="desc">${desc}</div></div>
    <button class="switch${on ? ' on' : ''}" data-toggle="${id}"><span class="knob"></span></button>
  </div>`;
}

function wireSettings(s) {
  const patch = async (body, okMsg) => {
    try {
      state.settings = await api('POST', '/api/settings', body);
      if (okMsg) toast(okMsg);
      renderSettings();
    } catch (e) { toast(e.message, true); }
  };

  app.querySelectorAll('[data-provider]').forEach(btn => {
    btn.addEventListener('click', () => {
      if (!s.providers.includes(btn.dataset.provider)) { toast('Нет API-ключа для этого провайдера', true); return; }
      patch({ active_provider: btn.dataset.provider });
    });
  });
  app.querySelectorAll('[data-deb]').forEach(btn => btn.addEventListener('click', () => patch({ debounce_seconds: Number(btn.dataset.deb) })));
  app.querySelectorAll('[data-sens]').forEach(btn => btn.addEventListener('click', () => patch({ sensitivity: btn.dataset.sens })));
  app.querySelectorAll('[data-toggle]').forEach(btn => {
    btn.addEventListener('click', () => {
      const map = { paused: 'triage_paused', markread: 'mark_read_on_work', notifydone: 'notify_done_on_close', digest: 'digest_enabled' };
      const key = map[btn.dataset.toggle];
      const current = btn.classList.contains('on');
      patch({ [key]: !current });
    });
  });

  app.querySelectorAll('[data-model-provider]').forEach(sel => {
    sel.addEventListener('change', () => {
      const p = sel.dataset.modelProvider;
      const label = PROVIDERS.find(([id]) => id === p)?.[1] || p;
      patch({ [p + '_model']: sel.value }, `🧠 Модель ${label} обновлена`);
    });
  });

  const digestTime = document.getElementById('digestTime');
  digestTime.addEventListener('change', () => patch({ digest_time: digestTime.value }, '🕘 Время дайджеста обновлено'));

  document.getElementById('testProvider').addEventListener('click', async (e) => {
    const resultEl = document.getElementById('testResult');
    resultEl.innerHTML = `<div class="loading">Проверяю…</div>`;
    await withBusy(e.target, async () => {
      try {
        const r = await api('POST', '/api/provider/test');
        resultEl.innerHTML = testResultHtml(r);
      } catch (err) {
        resultEl.innerHTML = `<div class="empty">❌ ${esc(err.message)}</div>`;
      }
    });
  });

  document.getElementById('resetSettings').addEventListener('click', () => {
    showModal(`
      <h3>Сбросить настройки?</h3>
      <p style="color:var(--hint);font-size:13px;margin:0">Все изменения, сделанные в боте и веб-панели, вернутся к значениям из .env на сервере.</p>
      <div class="actions">
        <button class="btn danger full" id="resetYes">Да, сбросить</button>
        <button class="btn ghost full" id="resetNo">Отмена</button>
      </div>`, root => {
      root.querySelector('#resetYes').addEventListener('click', async () => {
        closeModal();
        try { await api('POST', '/api/settings/reset'); toast('♻️ Настройки сброшены'); renderSettings(); }
        catch (e) { toast(e.message, true); }
      });
      root.querySelector('#resetNo').addEventListener('click', closeModal);
    });
  });
}

function testResultHtml(r) {
  let html = `<div class="card detail-section" style="margin-top:10px">
    <h3>${esc(r.provider)} · ${esc(r.model)} · ${(r.latency_ms / 1000).toFixed(1)} с</h3>`;
  if (r.analysis) {
    const a = r.analysis;
    html += `<p>Задача: <b>${a.is_task ? 'да' : 'нет'}</b> · уверенность ${Math.round(a.confidence * 100)}%<br>
      Тип: ${esc(a.message_type)} · приоритет: ${esc(a.priority)}<br>
      ${a.title ? 'Заголовок: ' + esc(a.title) + '<br>' : ''}
      ${a.draft_reply ? 'Черновик: <i>' + esc(a.draft_reply) + '</i>' : ''}</p>`;
  }
  html += `</div>`;
  return html;
}

/* ---------------------------------------------------------------------- */
/* Boot — after every screen-render function above is defined             */
/* ---------------------------------------------------------------------- */

switchTab('tasks');
