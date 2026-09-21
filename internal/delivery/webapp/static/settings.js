'use strict';

/* ---------------------------------------------------------------------- */
/* Settings (owner): a list of sections, each on its own screen           */
/* ---------------------------------------------------------------------- */

async function loadSettings() {
  state.settings = await api('GET', '/api/settings');
  return state.settings;
}

const PROVIDERS = [['claude', 'Claude'], ['gemini', 'Gemini'], ['groq', 'Groq'], ['mistral', 'Mistral'], ['openrouter', 'OpenRouter']];
function providerLabel(p) { return (PROVIDERS.find(([id]) => id === p) || [p, p])[1]; }
function field(s, key) { return s.fields.find(f => f.key === key); }
function stripEmoji(title) { return String(title || '').replace(/^[^\p{L}\p{N}]+/u, '').trim(); }
const GROUP_ICON = { helpdesk: 'headset', bots: 'bot', ai: 'sparkles', triage: 'tasks', general: 'sliders', backup: 'download' };
const DAY_NAMES = ['пн', 'вт', 'ср', 'чт', 'пт', 'сб', 'вс'];

function keepScroll() { state.keepScroll = window.scrollY; }
function restoreScroll() { if (state.keepScroll != null) { window.scrollTo(0, state.keepScroll); state.keepScroll = null; } }

async function renderSettings(id, tok) {
  if (!id) { setTop({ title: 'Настройки', large: true }); setBottom(tabbarHtml()); }
  else { setBottom(''); }
  if (!state.settings) app.innerHTML = skeleton(4);
  let s;
  try { s = await loadSettings(); await loadBots(); }
  catch (e) { if (tok !== state.rt) return; app.innerHTML = emptyState('alert', 'Не удалось загрузить', e.message, btn('Повторить', { sm: true, icon: 'refresh', act: 'refresh' })); return; }
  if (tok !== state.rt) return;
  if (!state.chainDirty || !state.chainDraft) state.chainDraft = chainFromSettings(s);

  if (!id) { app.innerHTML = settingsRootHtml(s); restoreScroll(); return; }
  if (id === 'bots') { setTop({ title: 'Боты организаций', subtitle: 'Отдельный бот и группа на каждого клиента', back: true }); app.innerHTML = botsHtml(); }
  else if (id.startsWith('bot:')) {
    const b = state.bots.find(x => x.id === Number(id.slice(4)));
    if (!b) { goBack(); return; }
    setTop({ title: b.label || ('@' + b.username), subtitle: b.username ? '@' + b.username : '', back: true });
    app.innerHTML = botScreenHtml(b);
  } else {
    const g = s.groups.find(x => x.id === id);
    setTop({ title: stripEmoji(g ? g.title : id), back: true });
    app.innerHTML = settingsGroupHtml(s, id);
    if (id === 'ai') renderChain();
    if (id === 'backup') loadBackups();
  }
  restoreScroll();
}

function groupValue(s, id) {
  if (id === 'ai') { const n = s.ai_chain.length; return n ? `${n} ${plural(n, 'ключ', 'ключа', 'ключей')}` : 'нет ключей'; }
  if (id === 'helpdesk') { const f = field(s, 'helpdesk.enabled'); return f && f.value ? 'Включён' : 'Выключен'; }
  if (id === 'bots') return state.bots.length ? String(state.bots.length) : 'нет';
  return null;
}
function settingsRootHtml(s) {
  return group(s.groups.map(g => navRow({ icon: GROUP_ICON[g.id] || 'sliders', label: stripEmoji(g.title), value: groupValue(s, g.id), act: 'settings-open', attrs: `data-id="${g.id}"` }))) +
    group([navRow({ icon: 'power', label: 'Перезапустить сервис', act: 'restart' }), navRow({ icon: 'refresh', label: 'Сбросить настройки к .env', danger: true, act: 'reset-settings' })]) +
    `<p class="tr-cap" style="padding:0 4px">В .env остаются только токен бота, OWNER_ID, путь к БД, адрес веб-панели и SOCKS-прокси. Всё остальное хранится в базе.</p>`;
}
ACT['settings-open'] = el => openView({ type: 'settings', id: el.dataset.id });

function settingsGroupHtml(s, id) {
  if (id === 'ai') {
    const general = s.fields.filter(f => f.group === 'ai' && !PROVIDERS.some(([p]) => f.key.startsWith(`ai.${p}_`)));
    return `<p class="tr-cap" style="padding:0 4px;margin:0">Ключи пробуются сверху вниз: если ключ не отвечает, берётся следующий.</p>` +
      `<div class="tr-group"><div style="padding:0 16px" id="chainBox"></div></div>` +
      `<div class="tr-row">${btn('Добавить ключ', { v: 'ghost', icon: 'plus', act: 'chain-add' })}${btn('Сохранить', { v: 'primary', id: 'chainSave', act: 'chain-save' })}</div>` +
      `<div>${btn('Проверить все ключи', { block: true, icon: 'check-circle', act: 'chain-test' })}<div id="testResult" style="margin-top:12px"></div></div>` +
      group(general.map(f => fieldHtml(s, f))) +
      PROVIDERS.map(([p, label]) => `<details class="tr-group tr-sub" data-group="ai-${p}"${state.openGroups.has('ai-' + p) ? ' open' : ''}><summary>${label}</summary>${s.fields.filter(f => f.key.startsWith(`ai.${p}_`)).map(f => fieldHtml(s, f)).join('')}</details>`).join('');
  }
  let html = group(s.fields.filter(f => f.group === id).map(f => fieldHtml(s, f)));
  if (id === 'helpdesk') html += `<div>${btn('Проверить группу', { block: true, icon: 'check-circle', act: 'hd-check' })}<div id="hdCheckResult" style="margin-top:12px"></div></div>`;
  if (id === 'backup') html += btn('Сделать копию сейчас', { v: 'primary', block: true, icon: 'download', act: 'backup-run' }) + `<div id="backupList"></div>`;
  return html;
}

document.addEventListener('toggle', e => {
  const d = e.target;
  if (d && d.matches && d.matches('details[data-group]')) { if (d.open) state.openGroups.add(d.dataset.group); else state.openGroups.delete(d.dataset.group); }
}, true);

function restartMark(f) { return f.restart ? ' ' + badge('после перезапуска') : ''; }
function fieldHtml(s, f) {
  const k = esc(f.key);
  const lbl = { labelHtml: esc(f.label) + restartMark(f), desc: f.desc };
  switch (f.kind) {
    case 'bool': return navRow(Object.assign({}, lbl, { trailing: switchBtn(f.value, `data-act="fld-bool" data-field="${k}"`, f.label) }));
    case 'int': return navRow(Object.assign({}, lbl, { trailing: `<input class="tr-input narrow" type="number" data-field="${k}" data-kind="int" value="${esc(f.value)}"${f.min ? ` min="${f.min}"` : ''}${f.max ? ` max="${f.max}"` : ''}>` }));
    case 'time': return navRow(Object.assign({}, lbl, { trailing: `<input class="tr-input narrow" type="time" data-field="${k}" data-kind="string" value="${esc(f.value)}">` }));
    case 'select': return navRow(Object.assign({}, lbl, { trailing: `<select class="tr-input narrow" data-field="${k}" data-kind="string">${f.options.map(o => `<option value="${esc(o.value)}"${o.value === f.value ? ' selected' : ''}>${esc(o.label)}</option>`).join('')}</select>` }));
    case 'text': return blockRow(lbl, `<textarea class="tr-input" data-field="${k}" data-kind="string">${esc(f.value)}</textarea>`);
    case 'list': return blockRow(lbl, `<textarea class="tr-input tr-input--mono" data-field="${k}" data-kind="list" placeholder="по одному на строку">${esc(f.value.join('\n'))}</textarea>`);
    case 'days': return blockRow(lbl, daysHtml(f.value, `data-field="${k}" data-kind="days"`));
    case 'model': {
      const presets = (field(s, `ai.${f.provider}_presets`) || { value: [] }).value;
      return blockRow(lbl, `<input class="tr-input tr-input--mono" list="dl-${k}" data-field="${k}" data-kind="string" value="${esc(f.value)}"><datalist id="dl-${k}">${presets.map(p => `<option value="${esc(p)}">`).join('')}</datalist>`);
    }
    default: return blockRow(lbl, `<input class="tr-input" type="text" data-field="${k}" data-kind="string" value="${esc(f.value)}">`);
  }
}
function blockRow(lbl, control) {
  return `<div class="tr-navrow tr-navrow--block"><span class="tr-navrow__text"><span class="tr-navrow__label">${lbl.labelHtml}</span>${lbl.desc ? `<span class="tr-navrow__desc">${esc(lbl.desc)}</span>` : ''}</span>${control}</div>`;
}
function daysHtml(value, attrs) {
  return `<div class="tr-chips tr-chips--days" ${attrs}>${DAY_NAMES.map((n, i) => chip(n, { selected: String(value || '').includes(String(i + 1)), act: 'fld-day', attrs: `data-day="${i + 1}"` })).join('')}</div>`;
}

async function patchSettings(body, okMsg) {
  keepScroll();
  try {
    state.settings = await api('POST', '/api/settings', body);
    if (okMsg) toast(okMsg);
    if (body.values && Object.keys(body.values).some(k => k.startsWith('helpdesk.'))) await refreshMe();
  } catch (e) { toast(e.message, { error: true }); }
  render();
}
async function refreshMe() { try { state.me = await api('GET', '/api/me'); } catch (e) { /* keep the old profile */ } }

// Settings fields save themselves the moment they change.
document.addEventListener('change', e => {
  const el = e.target.closest('[data-field]');
  if (el && el.dataset.kind) {
    const key = el.dataset.field, f = field(state.settings, key);
    if (el.dataset.kind === 'int') return patchSettings({ values: { [key]: Number(el.value) } }, f.label + ' — сохранено');
    if (el.dataset.kind === 'list') return patchSettings({ values: { [key]: el.value.split('\n').map(x => x.trim()).filter(Boolean) } }, f.label + ' — сохранено');
    if (el.dataset.kind === 'string') return patchSettings({ values: { [key]: el.value } }, f.label + ' — сохранено');
  }
  const bh = e.target.closest('[data-bot-hd]');
  if (bh) { const id = currentBotID(); const key = bh.dataset.botHd; return patchBot(id, { helpdesk: { [key]: bh.dataset.kind === 'int' ? Number(bh.value) : bh.value } }); }
  const bf = e.target.closest('[data-bot-field]');
  if (bf) { const id = currentBotID(); return patchBot(id, { [bf.dataset.botField]: bf.dataset.botField === 'label' ? bf.value.trim() : bf.value }); }
  if (e.target.classList.contains('chain-provider')) { const i = Number(e.target.closest('.tr-chain').dataset.i); state.chainDraft[i].provider = e.target.value; setChainDirty(true); }
});
ACT['fld-bool'] = el => { const key = el.dataset.field, f = field(state.settings, key); patchSettings({ values: { [key]: !f.value } }); };
ACT['fld-day'] = el => {
  const box = el.parentElement, day = el.dataset.day;
  if (box.dataset.botHd) { const id = currentBotID(), bot = state.bots.find(b => b.id === id), cur = bot.helpdesk[box.dataset.botHd] || ''; return patchBot(id, { helpdesk: { [box.dataset.botHd]: cur.includes(day) ? cur.replace(day, '') : cur + day } }); }
  const key = box.dataset.field, f = field(state.settings, key);
  patchSettings({ values: { [key]: f.value.includes(day) ? f.value.replace(day, '') : f.value + day } });
};
function currentBotID() { return state.view && state.view.type === 'settings' && String(state.view.id).startsWith('bot:') ? Number(state.view.id.slice(4)) : 0; }

/* --- AI key chain: rows are edited locally and saved together --- */
function chainFromSettings(s) { return s.ai_chain.map(e => ({ provider: e.provider, key: '', key_id: e.key_id, key_masked: e.key_masked })); }
function newChainRow(provider) { return { provider: provider || 'gemini', key: '', key_id: '', key_masked: '' }; }

function renderChain() {
  const box = document.getElementById('chainBox');
  if (!box) return;
  const d = state.chainDraft;
  box.innerHTML = !d.length ? emptyState('key' in ICONS ? 'key' : 'alert', 'Ключей нет — AI не работает', 'Добавьте хотя бы один.') : d.map((e, i) => `<div class="tr-chain" data-i="${i}">` +
    `<div class="tr-chain__line"><span class="tr-chain__num">${i + 1}</span><select class="tr-input chain-provider" aria-label="Провайдер">${PROVIDERS.map(([p, l]) => `<option value="${p}"${p === e.provider ? ' selected' : ''}>${l}</option>`).join('')}</select>${ibtn('more', 'Действия с ключом', { act: 'chain-menu', attrs: `data-i="${i}"` })}</div>` +
    `<input class="tr-input tr-input--mono chain-key" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" aria-label="API-ключ" placeholder="${e.key_id ? 'Сохранён ' + esc(e.key_masked) + ' · новый заменит' : 'API-ключ'}" value="${esc(e.key)}"></div>`).join('');
  setChainDirty(state.chainDirty);
}
document.addEventListener('input', e => {
  if (e.target.classList.contains('chain-key')) { state.chainDraft[Number(e.target.closest('.tr-chain').dataset.i)].key = e.target.value; setChainDirty(true); }
});
function setChainDirty(dirty) {
  state.chainDirty = dirty;
  const b = document.getElementById('chainSave');
  if (b) { b.disabled = !dirty; b.textContent = dirty ? 'Сохранить' : 'Сохранено'; }
}
function chainOp(op, i) {
  const d = state.chainDraft;
  if (op === 'up' && i > 0) [d[i - 1], d[i]] = [d[i], d[i - 1]];
  else if (op === 'down' && i < d.length - 1) [d[i + 1], d[i]] = [d[i], d[i + 1]];
  else if (op === 'del') d.splice(i, 1);
  else return;
  haptic('light'); setChainDirty(true); renderChain();
}
ACT['chain-menu'] = el => {
  const i = Number(el.dataset.i), n = state.chainDraft.length;
  openMenu('Ключ ' + (i + 1), [
    { label: 'Поднять выше', icon: 'arrow-up', disabled: i === 0, onSelect: () => chainOp('up', i) },
    { label: 'Опустить ниже', icon: 'arrow-down', disabled: i === n - 1, onSelect: () => chainOp('down', i) },
    { divider: true },
    { label: 'Удалить ключ', icon: 'trash', danger: true, onSelect: () => chainOp('del', i) },
  ]);
};
ACT['chain-add'] = () => {
  const d = state.chainDraft;
  d.push(newChainRow(d.length ? d[d.length - 1].provider : ''));
  setChainDirty(true); renderChain();
  const inputs = app.querySelectorAll('.chain-key'); if (inputs.length) inputs[inputs.length - 1].focus();
};
ACT['chain-save'] = el => {
  const d = state.chainDraft;
  const bad = d.findIndex(r => !r.key.trim() && !r.key_id);
  if (bad >= 0) { toast(`Строка ${bad + 1}: введите API-ключ`, { error: true }); return; }
  const ai_chain = d.map(r => r.key.trim() ? { provider: r.provider, key: r.key.trim() } : { provider: r.provider, key_id: r.key_id });
  withBusy(el, async () => {
    state.settings = await api('POST', '/api/settings', { ai_chain });
    state.chainDraft = null; state.chainDirty = false;
    toast('Ключи сохранены');
    keepScroll(); render();
  });
};
ACT['chain-test'] = el => {
  if (state.chainDirty) { toast('Сначала сохраните ключи — проверяются сохранённые', { error: true }); return; }
  const out = document.getElementById('testResult');
  out.innerHTML = '<p class="tr-cap">Проверяю…</p>';
  withBusy(el, async () => {
    try {
      const r = await api('POST', '/api/provider/test');
      out.innerHTML = `<div class="tr-card">` + checkList(r.results.map(x => {
        const head = `${x.index}. ${esc(providerLabel(x.provider))} · ${esc(x.key_masked)} · ${esc(x.model)} · ${(x.latency_ms / 1000).toFixed(1)} с`;
        return x.error ? { tone: 'bad', text: head, detail: esc(x.error) } : { tone: 'ok', text: head, detail: `работает · задача: ${x.analysis.is_task ? 'да' : 'нет'} · уверенность ${Math.round(x.analysis.confidence * 100)}%` };
      })) + `</div>`;
    } catch (err) { out.innerHTML = checkList([{ tone: 'bad', text: esc(err.message) }]); }
  });
};

/* --- helpdesk check, backups, system --- */
ACT['hd-check'] = el => withBusy(el, async () => {
  const out = document.getElementById('hdCheckResult');
  try {
    const r = await api('POST', '/api/helpdesk/check');
    out.innerHTML = `<div class="tr-card">` + checkList([
      { tone: 'ok', text: 'Группа: ' + esc(r.title || '—') },
      { tone: r.is_forum ? 'ok' : 'bad', text: 'Темы (форум) включены' },
      { tone: r.bot_admin ? 'ok' : 'bad', text: 'Бот — администратор' },
      { tone: r.can_manage_topics ? 'ok' : 'bad', text: 'Право «Управление темами»', detail: r.can_manage_topics ? '' : 'Откройте права бота в группе и включите его' },
      { tone: r.can_pin_messages ? 'ok' : 'warn', text: 'Право закреплять сообщения', detail: r.can_pin_messages ? '' : 'Карточки тикетов не будут закрепляться' },
      { tone: r.can_delete_messages ? 'ok' : 'warn', text: 'Право удалять сообщения', detail: r.can_delete_messages ? '' : 'Команда /1 останется видимой в теме' },
    ]) + `</div>`;
  } catch (err) { out.innerHTML = checkList([{ tone: 'bad', text: esc(err.message) }]); }
});
ACT['backup-run'] = el => withBusy(el, async () => {
  const r = await api('POST', '/api/backups');
  if (r.warning) toast(r.warning, { error: true }); else toast('Копия создана: ' + fmtSize(r.backup.size));
  loadBackups();
});
async function loadBackups() {
  const box = document.getElementById('backupList');
  if (!box) return;
  try {
    const r = await api('GET', '/api/backups');
    box.innerHTML = r.items.length
      ? `<p class="tr-cap" style="margin:0 4px 8px">${esc(r.dir)}</p>` + group(r.items.map(b => navRow({ labelHtml: `<span class="tr-mono">${esc(b.name)}</span>`, value: fmtSize(b.size) })))
      : emptyState('download', 'Копий пока нет', '');
  } catch (e) { box.innerHTML = emptyState('alert', 'Не удалось загрузить', e.message); }
}
ACT.restart = async () => {
  if (!await confirmSheet('Перезапустить сервис?', 'Бот будет недоступен несколько секунд.', 'Перезапустить')) return;
  try { await api('POST', '/api/system/restart'); toast('Перезапуск… обновите через 10 секунд'); } catch (e) { toast(e.message, { error: true }); }
};
ACT['reset-settings'] = async () => {
  if (!await confirmSheet('Сбросить настройки?', 'Все настройки, включая хелпдеск и ключи AI, вернутся к значениям из .env, а если их там нет — к значениям по умолчанию.', 'Да, сбросить', true)) return;
  try { await api('POST', '/api/settings/reset'); state.chainDraft = null; state.chainDirty = false; toast('Настройки сброшены'); render(); } catch (e) { toast(e.message, { error: true }); }
};

/* ---------------------------------------------------------------------- */
/* Bots (additional, per-organization support bots)                       */
/* ---------------------------------------------------------------------- */

const SENSITIVITY_OPTIONS = [['', 'Как в общих настройках'], ['low', 'Низкая'], ['medium', 'Средняя'], ['high', 'Высокая']];
const BOT_HD_FIELDS = [
  ['group_id', 'int', 'ID супергруппы', 'Супергруппа с включёнными темами, бот — админ с правом «Управление темами». ID вида -100…'],
  ['triage_enabled', 'bool', 'Автоматические тикеты', 'Модель анализирует сообщения пользователей и заводит тикеты'],
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
  return `<p class="tr-cap" style="padding:0 4px;margin:0">Настройки AI ниже общие для всех ботов; чувствительность можно переопределить у конкретного бота.</p>` +
    (bots.length ? group(bots.map(b => navRow({ label: b.label || ('@' + b.username), desc: (b.username ? '@' + b.username + ' · ' : '') + (b.helpdesk.group_id ? 'группа настроена' : 'группа не указана'), act: 'settings-open', attrs: `data-id="bot:${b.id}"`,
      trailing: (b.helpdesk.group_id ? '' : badge('группа', { tone: 'warning', icon: 'alert' })) + switchBtn(b.active, `data-act="bot-active" data-id="${b.id}"`, 'Бот включён') }))) : emptyState('bot', 'Дополнительных ботов пока нет', '')) +
    btn('Добавить бота', { v: 'primary', block: true, icon: 'plus', act: 'bot-add' });
}
function botScreenHtml(b) {
  const rows = [
    blockRow({ labelHtml: 'Название организации' }, `<input class="tr-input" data-bot-field="label" value="${esc(b.label)}" placeholder="Название организации">`),
    navRow({ label: 'Бот включён', trailing: switchBtn(b.active, `data-act="bot-active" data-id="${b.id}"`, 'Бот включён') }),
    navRow({ label: 'Чувствительность', trailing: `<select class="tr-input narrow" data-bot-field="sensitivity">${SENSITIVITY_OPTIONS.map(([v, l]) => `<option value="${v}"${v === b.sensitivity ? ' selected' : ''}>${esc(l)}</option>`).join('')}</select>` }),
  ].concat(BOT_HD_FIELDS.map(f => botFieldHtml(b, f)));
  return group(rows) + group([navRow({ icon: 'trash', label: 'Удалить бота', danger: true, act: 'bot-del', attrs: `data-id="${b.id}"` })]);
}
function botFieldHtml(b, [key, kind, label, desc]) {
  const v = b.helpdesk[key];
  const lbl = { labelHtml: esc(label), desc };
  const attr = `data-bot-hd="${key}" data-kind="${kind}"`;
  switch (kind) {
    case 'bool': return navRow(Object.assign({}, lbl, { trailing: switchBtn(v, `data-act="bot-hd-bool" data-key="${key}"`, label) }));
    case 'int': return navRow(Object.assign({}, lbl, { trailing: `<input class="tr-input narrow" type="number" ${attr} value="${esc(v)}">` }));
    case 'time': return navRow(Object.assign({}, lbl, { trailing: `<input class="tr-input narrow" type="time" ${attr} value="${esc(v)}">` }));
    case 'days': return blockRow(lbl, daysHtml(v, `data-bot-hd="${key}"`));
    default: return blockRow(lbl, `<textarea class="tr-input" ${attr}>${esc(v)}</textarea>`);
  }
}

async function patchBot(id, body, okMsg) {
  keepScroll();
  try {
    const b = await api('POST', `/api/bots/${id}`, body);
    const idx = state.bots.findIndex(x => x.id === id);
    if (idx >= 0) state.bots[idx] = b; else state.bots.push(b);
    if (okMsg) toast(okMsg);
  } catch (e) { toast(e.message, { error: true }); }
  render();
}
ACT['bot-active'] = (el, e) => { e.stopPropagation(); const id = Number(el.dataset.id), b = state.bots.find(x => x.id === id); if (b) patchBot(id, { active: !b.active }); };
ACT['bot-hd-bool'] = el => { const id = currentBotID(), b = state.bots.find(x => x.id === id); if (b) patchBot(id, { helpdesk: { [el.dataset.key]: !b.helpdesk[el.dataset.key] } }); };
ACT['bot-del'] = async el => {
  const id = Number(el.dataset.id), bot = state.bots.find(b => b.id === id);
  if (!bot || !await confirmSheet('Удалить бота?', `«${bot.label || bot.username}» перестанет отвечать. История его тикетов останется.`, 'Удалить', true)) return;
  try {
    await api('DELETE', `/api/bots/${id}`);
    state.bots = state.bots.filter(b => b.id !== id);
    state.view = { type: 'settings', id: 'bots' }; state.back = state.back.filter(v => !(v.type === 'settings' && v.id === 'bot:' + id));
    render(); toast('Бот удалён');
  } catch (e) { toast(e.message, { error: true }); }
};
ACT['bot-add'] = () => {
  showSheet('Добавить бота',
    `<p style="margin:0" class="tr-cap">Токен от @BotFather для нового, отдельного бота этой организации.</p>` +
    `<div class="tr-field"><label class="tr-field__label" for="addBotToken">Токен бота</label><input class="tr-input tr-input--mono" id="addBotToken" autocomplete="off" autocapitalize="off" spellcheck="false"></div>` +
    `<div class="tr-field"><label class="tr-field__label" for="addBotLabel">Название организации</label><input class="tr-input" id="addBotLabel" placeholder="Необязательно"></div>`,
    btn('Добавить', { v: 'primary', block: true, id: 'addBotOk' }), root => {
      root.querySelector('#addBotOk').addEventListener('click', e => {
        const token = root.querySelector('#addBotToken').value.trim(), label = root.querySelector('#addBotLabel').value.trim();
        if (!token) { toast('Введите токен', { error: true }); return; }
        withBusy(e.currentTarget, async () => {
          const b = await api('POST', '/api/bots', { token, label });
          state.bots.push(b);
          closeSheet(); render(); toast('Бот «' + (b.label || b.username) + '» добавлен');
        });
      });
    });
};
