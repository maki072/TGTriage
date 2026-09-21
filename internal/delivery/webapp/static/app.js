'use strict';

/* Boot: profile → bots → tabs → (deep link | first tab). Screens live in screens.js, settings in settings.js, shared UI in ui.js. */

(async function boot() {
  try {
    state.me = await api('GET', '/api/me');
  } catch (e) {
    setTop({ title: 'Нет доступа' });
    app.innerHTML = emptyState('lock', 'Нет доступа', e.message);
    return;
  }
  await loadBots();
  const params = new URLSearchParams(location.search);
  const startParam = tg && tg.initDataUnsafe ? tg.initDataUnsafe.start_param : '';
  const taskID = Number(params.get('task') || (startParam && startParam.startsWith('t') ? startParam.slice(1) : 0));
  state.tab = taskID ? 'tasks' : tabsForMe()[0][0];
  if (taskID) openView({ type: 'task', id: taskID });
  else render();
  refreshAwaiting();
})();
