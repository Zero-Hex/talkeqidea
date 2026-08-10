// Modern EQ Chat hub management interface.
//
// Plain DOM, no framework and no build step, so the whole UI ships inside the
// binary and the content security policy can forbid every external source.
// Everything user-supplied goes through textContent rather than innerHTML:
// server names and channel patterns are operator input, and one of them is a
// telnet command template, so none of it is ever parsed as markup.

'use strict';

let csrf = '';

// ---------------------------------------------------------------- transport

async function api(path, options = {}) {
  const headers = { 'Content-Type': 'application/json' };
  if (csrf) headers['X-MEQC-CSRF'] = csrf;

  const response = await fetch(path, {
    ...options,
    headers,
    // Loopback only, but be explicit rather than relying on the default.
    credentials: 'same-origin',
  });

  let payload = {};
  try {
    payload = await response.json();
  } catch (err) {
    // A non-JSON body means something below the handler failed.
  }

  if (response.status === 401) {
    showLogin();
    throw new Error(payload.error || 'session expired');
  }
  if (!response.ok) {
    throw new Error(payload.error || `request failed (${response.status})`);
  }
  return payload;
}

const get = (path) => api(path);
const post = (path, body) => api(path, { method: 'POST', body: JSON.stringify(body || {}) });

// ------------------------------------------------------------------ helpers

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function toast(message, isBad) {
  const node = document.getElementById('toast');
  node.textContent = message;
  node.className = isBad ? 'toast bad' : 'toast';
  node.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { node.hidden = true; }, 4000);
}

function fail(err) {
  toast(err.message || String(err), true);
}

// -------------------------------------------------------------------- login

function showLogin() {
  document.getElementById('login').hidden = false;
  document.getElementById('app').hidden = true;
  csrf = '';
}

function showApp() {
  document.getElementById('login').hidden = true;
  document.getElementById('app').hidden = false;
  refreshAll();
}

document.getElementById('login-form').addEventListener('submit', async (event) => {
  event.preventDefault();

  const error = document.getElementById('login-error');
  error.hidden = true;

  try {
    const result = await post('/api/login', {
      password: document.getElementById('password').value,
    });
    csrf = result.csrf;
    document.getElementById('password').value = '';
    showApp();
  } catch (err) {
    error.textContent = err.message;
    error.hidden = false;
  }
});

document.getElementById('logout').addEventListener('click', async () => {
  try { await post('/api/logout'); } catch (err) { /* signing out anyway */ }
  showLogin();
});

// ----------------------------------------------------------------- overview

async function renderOverview() {
  const data = await get('/api/overview');
  const stats = document.getElementById('stats');
  stats.replaceChildren();

  const cards = [
    ['Servers online', `${data.online_count} / ${data.agent_count}`],
    ['Players', data.player_count],
    ['Hub address', data.hub_address],
  ];
  if (data.bans && data.bans.length) {
    cards.push(['Blocked addresses', data.bans.length]);
  }

  for (const [label, value] of cards) {
    const card = el('div', 'stat');
    card.append(el('div', 'value', String(value)));
    card.append(el('div', 'label', label));
    stats.append(card);
  }
}

// ------------------------------------------------------------------- agents

function statusDot(agent) {
  if (!agent.is_connected) return ['down', 'offline'];
  if (!agent.is_source_up) return ['partial', 'relay up, game server down'];
  return ['up', 'online'];
}

async function renderAgents() {
  const data = await get('/api/agents');
  const container = document.getElementById('agents');
  container.replaceChildren();

  if (!data.agents.length) {
    container.append(el('p', 'muted', 'No servers yet. Add one below.'));
    return;
  }

  for (const agent of data.agents) {
    container.append(agentCard(agent));
  }
}

function agentCard(agent) {
  const card = el('div', 'agent');
  card.dataset.key = agent.server_key;

  const head = el('div', 'agent-head');
  const title = el('div');

  const [dotClass, dotLabel] = statusDot(agent);
  const dot = el('span', `dot ${dotClass}`);
  dot.title = dotLabel;
  title.append(dot);
  title.append(el('span', 'agent-name', agent.short_name));
  title.append(el('span', 'agent-key', agent.server_key));
  if (agent.is_disabled) {
    title.append(el('span', 'agent-key', '· disabled'));
  }
  head.append(title);

  const actions = el('div', 'agent-actions');
  actions.append(button('Test', () => testAgent(agent.server_key)));
  actions.append(button('Rename', () => renameAgent(agent), 'secondary'));
  actions.append(button(agent.is_disabled ? 'Enable' : 'Disable',
    () => setEnabled(agent.server_key, agent.is_disabled), 'secondary'));
  actions.append(button('Re-enroll', () => rotateAgent(agent.server_key), 'secondary'));
  actions.append(button('Remove', () => removeAgent(agent), 'danger'));
  head.append(actions);

  card.append(head);

  const meta = [dotLabel];
  if (agent.is_connected) {
    meta.push(`${agent.player_count} players`);
    meta.push(`up ${agent.connected_at}`);
    if (agent.dropped > 0) meta.push(`${agent.dropped} dropped`);
  } else {
    meta.push(`last seen ${agent.last_seen}`);
  }
  card.append(el('div', 'agent-meta', meta.join(' · ')));

  return card;
}

function button(label, onClick, className) {
  const node = el('button', className, label);
  node.addEventListener('click', onClick);
  return node;
}

async function testAgent(serverKey) {
  toast(`Testing ${serverKey}...`);
  try {
    const data = await post('/api/agents/test', { server_key: serverKey, with_echo: true });
    showTestResults(data.results);
  } catch (err) { fail(err); }
}

document.getElementById('test-all').addEventListener('click', async () => {
  toast('Testing every server...');
  try {
    const data = await post('/api/agents/test', { with_echo: false });
    showTestResults(data.results);
  } catch (err) { fail(err); }
});

function showTestResults(results) {
  for (const result of results) {
    const card = document.querySelector(`.agent[data-key="${CSS.escape(result.server_key)}"]`);
    if (!card) continue;

    let box = card.querySelector('.test-result');
    if (!box) {
      box = el('div', 'test-result');
      card.append(box);
    }

    const parts = [];
    if (result.is_connected) {
      parts.push(`replied in ${result.round_trip_ms}ms`);
      parts.push(result.source_up ? 'game server reachable' : 'game server NOT reachable');
      if (result.injected_ok) parts.push('test line injected');
      parts.push(`${result.player_count} players`);
    }
    if (result.detail) parts.push(result.detail);

    box.textContent = parts.join(' · ');
  }
  toast('Test complete');
}

async function renameAgent(agent) {
  const name = prompt(`Display name for ${agent.server_key}:`, agent.short_name);
  if (name === null) return;

  try {
    await post('/api/agents/rename', { server_key: agent.server_key, short_name: name });
    toast('Renamed');
    await renderAgents();
  } catch (err) { fail(err); }
}

async function setEnabled(serverKey, isEnabled) {
  try {
    await post('/api/agents/enable', { server_key: serverKey, is_enabled: isEnabled });
    toast(isEnabled ? 'Enabled' : 'Disabled');
    await renderAgents();
  } catch (err) { fail(err); }
}

async function removeAgent(agent) {
  // Removal revokes the token, so the server has to be re-enrolled to come
  // back. Worth a confirmation that names what is being removed.
  if (!confirm(`Remove ${agent.short_name} (${agent.server_key})?\n\nIts token is revoked and it will need a new enrollment code to reconnect.`)) {
    return;
  }

  try {
    await post('/api/agents/remove', { server_key: agent.server_key });
    toast('Removed');
    await refreshAll();
  } catch (err) { fail(err); }
}

async function rotateAgent(serverKey) {
  try {
    const data = await post('/api/agents/rotate', { server_key: serverKey });
    showCode(document.getElementById('enroll-result'), data, `Re-enrollment code for ${serverKey}`);
    toast('Code generated');
  } catch (err) { fail(err); }
}

// --------------------------------------------------------------- enrollment

document.getElementById('enroll-form').addEventListener('submit', async (event) => {
  event.preventDefault();

  const key = document.getElementById('enroll-key').value;
  const name = document.getElementById('enroll-name').value;

  try {
    const data = await post('/api/enroll', { server_key: key, short_name: name });
    showCode(document.getElementById('enroll-result'), data, `Enrollment code for ${name || key}`);
    document.getElementById('enroll-key').value = '';
    document.getElementById('enroll-name').value = '';
    await renderPending();
  } catch (err) { fail(err); }
});

function showCode(container, data, heading) {
  container.replaceChildren();

  const box = el('div', 'code-box');
  box.append(el('div', 'muted', heading));
  box.append(el('div', 'code-value', data.code));

  const lines = [`Hub address: ${data.hub_address}`];
  if (data.expires_in) lines.push(`Expires in ${data.expires_in}, single use`);
  if (data.note) lines.push(data.note);
  lines.push('Run modern-eq-chat-agent on that server and enter the address and code.');

  for (const line of lines) {
    box.append(el('div', 'muted', line));
  }

  container.append(box);
}

async function renderPending() {
  const data = await get('/api/enroll');
  const container = document.getElementById('enroll-pending');
  container.replaceChildren();

  if (!data.pending || !data.pending.length) return;

  const table = el('table');
  const head = el('tr');
  for (const label of ['Server', 'Name', 'Expires', '']) {
    head.append(el('th', null, label));
  }
  table.append(head);

  for (const entry of data.pending) {
    const row = el('tr');
    row.append(el('td', null, entry.server_key));
    row.append(el('td', null, entry.short_name));

    const remaining = Math.max(0, Math.round(entry.expires_at - Date.now() / 1000));
    row.append(el('td', null, `${Math.floor(remaining / 60)}m ${remaining % 60}s`));

    const cell = el('td');
    cell.append(button('Revoke', async () => {
      try {
        await post('/api/enroll/revoke', { id: entry.id });
        toast('Revoked');
        await renderPending();
      } catch (err) { fail(err); }
    }, 'secondary'));
    row.append(cell);

    table.append(row);
  }

  container.append(el('div', 'muted', 'Outstanding codes'));
  container.append(table);
}

// ----------------------------------------------------------------- channels

async function renderChannels() {
  const data = await get('/api/channels');
  const container = document.getElementById('channels');
  container.replaceChildren();

  for (const channel of data.channels) {
    container.append(channelCard(channel));
  }
}

function channelCard(channel) {
  const card = el('div', 'channel');
  card.dataset.name = channel.name;

  card.append(el('div', 'agent-name', channel.name));

  const toggles = el('div', 'toggles');
  toggles.append(checkbox('enabled', 'Relayed', channel.enabled));
  toggles.append(checkbox('cross', 'Between game servers', channel.cross_server));
  card.append(toggles);

  card.append(field('discord', 'Discord channel ID', channel.discord_channel_id));
  card.append(field('pattern', 'Discord message pattern', channel.discord_pattern));

  return card;
}

function checkbox(name, label, checked) {
  const wrapper = el('label');
  const input = document.createElement('input');
  input.type = 'checkbox';
  input.dataset.field = name;
  input.checked = checked;
  wrapper.append(input);
  wrapper.append(document.createTextNode(label));
  return wrapper;
}

function field(name, label, value) {
  const wrapper = el('div', 'field');
  wrapper.append(el('label', null, label));

  const input = document.createElement('input');
  input.type = 'text';
  input.dataset.field = name;
  input.value = value || '';
  wrapper.append(input);

  return wrapper;
}

document.getElementById('save-channels').addEventListener('click', async () => {
  const channels = [];

  for (const card of document.querySelectorAll('.channel')) {
    channels.push({
      name: card.dataset.name,
      enabled: card.querySelector('[data-field=enabled]').checked,
      cross_server: card.querySelector('[data-field=cross]').checked,
      discord_channel_id: card.querySelector('[data-field=discord]').value,
      discord_pattern: card.querySelector('[data-field=pattern]').value,
    });
  }

  try {
    await post('/api/channels', { channels });
    toast('Channels saved and applied');
  } catch (err) { fail(err); }
});

// ------------------------------------------------------------------ refresh

async function refreshAll() {
  try {
    await renderOverview();
    await renderAgents();
    await renderPending();
    await renderChannels();
  } catch (err) {
    // A 401 has already bounced us to the login screen; anything else is
    // worth surfacing.
    if (csrf) fail(err);
  }
}

document.getElementById('refresh').addEventListener('click', refreshAll);

// Poll while the tab is visible. Paused when hidden so a forgotten tab does
// not keep the hub busy overnight.
setInterval(() => {
  if (!document.hidden && csrf) {
    renderOverview().catch(() => {});
    renderAgents().catch(() => {});
  }
}, 10000);

// A cookie may still be valid from an earlier visit, but the CSRF token lives
// only in memory, so a reload always starts at the login screen.
showLogin();
