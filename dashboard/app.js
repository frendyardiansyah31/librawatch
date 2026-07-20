'use strict';

// ── State ──────────────────────────────────────────────────────────────────
let allAgents     = [];
let deployTargets = new Set();
let uploadedFile  = null;
let expandedRows  = new Set();
let deployPollers = {};
let refreshTimer  = null;
let meshBaseURL   = '';
let allCategories = [];
let appFilterStatus = 'pending_review';

// ── Auth ───────────────────────────────────────────────────────────────────
function getToken() { return localStorage.getItem('auth_token') || ''; }
function setToken(t) { localStorage.setItem('auth_token', t); }
function clearToken() { localStorage.removeItem('auth_token'); }

function showLogin() {
  const el = document.getElementById('login-overlay');
  if (el) { el.style.display = 'flex'; }
  const btn = document.getElementById('btn-logout');
  if (btn) btn.style.display = 'none';
}
function hideLogin() {
  const el = document.getElementById('login-overlay');
  if (el) { el.style.display = 'none'; }
  const btn = document.getElementById('btn-logout');
  if (btn) btn.style.display = '';
}

async function doLogout() {
  try { await api('POST', '/logout'); } catch (_) { /* ignore */ }
  clearToken();
  showLogin();
}

async function doLogin() {
  const user  = document.getElementById('login-user').value.trim();
  const pass  = document.getElementById('login-pass').value;
  const errEl = document.getElementById('login-error');
  errEl.textContent = '';
  const btn = document.getElementById('login-btn');
  btn.disabled = true;
  try {
    const r = await fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: user, password: pass }),
    });
    const data = await r.json().catch(() => ({}));
    if (!r.ok) {
      errEl.textContent = data.error || 'Login gagal';
      return;
    }
    setToken(data.token);
    hideLogin();
    await loadAgents();
    refreshTimer = setInterval(loadAgents, 10000);
  } catch (e) {
    errEl.textContent = 'Koneksi gagal: ' + e.message;
  } finally {
    btn.disabled = false;
  }
}

// ── API ────────────────────────────────────────────────────────────────────
async function api(method, path, body) {
  const token = getToken();
  const opts = { method, headers: {} };
  if (token) opts.headers['Authorization'] = 'Bearer ' + token;
  if (body instanceof FormData) {
    opts.body = body;
  } else if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const r = await fetch('/api' + path, opts);
  if (r.status === 401) {
    clearToken();
    clearInterval(refreshTimer);
    refreshTimer = null;
    Object.values(deployPollers).forEach(clearInterval);
    deployPollers = {};
    showLogin();
    throw new Error('Session berakhir, silakan login kembali');
  }
  const data = await r.json().catch(() => ({ error: r.statusText }));
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}

// ── Helpers ────────────────────────────────────────────────────────────────
function esc(s) {
  return String(s ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

function timeSince(str) {
  if (!str) return '—';
  const d = new Date(str);
  if (isNaN(d)) return str;
  const s = Math.floor((Date.now() - d) / 1000);
  if (s <  5)   return 'baru saja';
  if (s < 60)   return s + ' dtk lalu';
  if (s < 3600) return Math.floor(s / 60) + ' mnt lalu';
  if (s < 86400) return Math.floor(s / 3600) + ' jam lalu';
  return Math.floor(s / 86400) + ' hari lalu';
}

function fmtTime(str) {
  if (!str) return '—';
  const d = new Date(str);
  if (isNaN(d)) return str;
  return d.toLocaleString('id-ID', {
    timeZone: 'Asia/Jakarta', hour12: false,
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit'
  });
}

function barHtml(pct) {
  const p = Math.min(100, Math.max(0, pct || 0));
  const cls = p >= 85 ? 'high' : p >= 60 ? 'mid' : 'low';
  return `<div class="bar-wrap">
    <div class="bar-track"><div class="bar-fill ${cls}" style="width:${p.toFixed(1)}%"></div></div>
    <span class="bar-pct">${p.toFixed(1)}%</span>
  </div>`;
}

function alertLabel(type) {
  return { cpu_high:'CPU Tinggi', ram_high:'RAM Tinggi', offline:'Offline',
           recovery:'Online Kembali', blacklisted_app:'Aplikasi Terlarang',
           peripheral_removed:'Perangkat Terlepas' }[type] || type;
}

function agentName(id) {
  const ag = allAgents.find(a => a.id === id);
  return ag ? ag.hostname : id.slice(0, 8) + '…';
}

// ── Tab Navigation ─────────────────────────────────────────────────────────
function showTab(name) {
  document.querySelectorAll('.tab-content').forEach(el => el.classList.remove('active'));
  document.querySelectorAll('.tab-btn').forEach(el => el.classList.remove('active'));
  document.getElementById('tab-' + name).classList.add('active');
  document.querySelector(`.tab-btn[data-tab="${name}"]`).classList.add('active');

  clearInterval(refreshTimer);
  refreshTimer = null;

  if (name === 'agents') {
    loadAgents();
    refreshTimer = setInterval(loadAgents, 10000);
  } else if (name === 'deploy') {
    if (!allAgents.length) loadAgents().then(renderDeployAgentList);
    else renderDeployAgentList();
    loadDeployHistory();
    refreshTimer = setInterval(loadDeployHistory, 15000);
  } else if (name === 'alerts') {
    loadAlerts();
    refreshTimer = setInterval(loadAlerts, 10000);
  } else if (name === 'logs') {
    loadLogs();
  } else if (name === 'applications') {
    loadApplications();
  } else if (name === 'events') {
    loadEvents();
    refreshTimer = setInterval(loadEvents, 15000);
  } else if (name === 'floormap') {
    loadFloorMap();
    refreshTimer = setInterval(loadFloorMap, 10000);
  }
}

// ── Agents Tab ─────────────────────────────────────────────────────────────
async function loadAgents() {
  try {
    const [agents, stats] = await Promise.all([
      api('GET', '/agents'),
      api('GET', '/stats'),
    ]);
    allAgents = agents || [];
    const online  = allAgents.filter(a => a.status === 'online').length;
    const offline = allAgents.length - online;
    document.getElementById('stat-online').textContent  = online;
    document.getElementById('stat-offline').textContent = offline;
    document.getElementById('stat-alerts').textContent  = stats.today_alerts ?? '—';
    renderAgents(allAgents);
  } catch (e) {
    console.error('loadAgents:', e);
    const tbody = document.getElementById('agents-tbody');
    if (tbody) tbody.innerHTML = `<tr><td colspan="9" class="empty" style="color:#dc2626">Gagal memuat data: ${esc(e.message)}</td></tr>`;
  }
}

function renderAgents(agents) {
  const tbody = document.getElementById('agents-tbody');
  if (!agents.length) {
    tbody.innerHTML = '<tr><td colspan="9" class="empty">Belum ada agent yang terhubung</td></tr>';
    return;
  }

  const prevExpanded = new Set(expandedRows);
  tbody.innerHTML = '';

  agents.forEach(ag => {
    const tr = document.createElement('tr');
    tr.className = 'agent-row';
    tr.dataset.id = ag.id;

    const meshLink = (meshBaseURL && ag.mesh_id)
      ? `<a class="btn-sm" href="${esc(meshBaseURL)}" target="_blank" rel="noopener">MeshCentral</a>`
      : `<button class="btn-sm" disabled>MeshCentral</button>`;

    tr.innerHTML = `
      <td><span class="dot ${ag.status === 'online' ? 'online' : 'offline'}"></span></td>
      <td><strong>${esc(ag.hostname)}</strong></td>
      <td style="font-family:monospace;font-size:12px">${esc(ag.ip)}</td>
      <td>
        <input type="text" class="device-group-input" value="${esc(ag.device_group || '')}"
          placeholder="—" onclick="event.stopPropagation()"
          onchange="updateDeviceGroup('${esc(ag.id)}', this.value)">
      </td>
      <td>${barHtml(ag.cpu)}</td>
      <td>${barHtml(ag.ram)}</td>
      <td class="proc-name" title="${esc(ag.top_process)}">${esc(ag.top_process || '—')}</td>
      <td style="font-size:12px;color:var(--muted)">${timeSince(ag.last_seen)}</td>
      <td class="actions">
        ${meshLink}
        <button class="btn-sm"
          onclick="openAgentLogs('${esc(ag.id)}','${esc(ag.hostname)}');event.stopPropagation()">Logs</button>
        <button class="btn-sm btn-delete-agent" title="Hapus agent ini"
          onclick="deleteAgent('${esc(ag.id)}','${esc(ag.hostname)}');event.stopPropagation()">
          <svg xmlns="http://www.w3.org/2000/svg" width="13" height="13" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
            <polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>
            <path d="M10 11v6M14 11v6"/><path d="M9 6V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"/>
          </svg>
        </button>
      </td>`;

    tr.addEventListener('click', e => {
      if (e.target.tagName === 'BUTTON' || e.target.tagName === 'A') return;
      toggleAgentRow(ag.id);
    });
    tbody.appendChild(tr);

    if (prevExpanded.has(ag.id)) {
      expandedRows.add(ag.id);
      const detTr = makeDetailRow(ag.id);
      tbody.appendChild(detTr);
      loadAgentDetail(ag.id, detTr);
    }
  });
}

function toggleAgentRow(id) {
  const existing = document.getElementById('detail-' + id);
  if (existing) {
    existing.remove();
    expandedRows.delete(id);
    return;
  }
  expandedRows.add(id);
  const agRow = document.querySelector(`tr.agent-row[data-id="${id}"]`);
  if (!agRow) return;
  const detTr = makeDetailRow(id);
  agRow.insertAdjacentElement('afterend', detTr);
  loadAgentDetail(id, detTr);
}

function makeDetailRow(id) {
  const tr = document.createElement('tr');
  tr.className = 'detail-row';
  tr.id = 'detail-' + id;
  tr.innerHTML = `<td colspan="9"><div class="detail-content"><p class="no-data">Memuat…</p></div></td>`;
  return tr;
}

function buildDeviceProfile(ag) {
  if (!ag) return '<p class="no-data">Data perangkat belum tersedia</p>';
  const rows = [
    ['Agent Version', ag.agent_version || '—'],
    ['Windows Version', ag.windows_version || '—'],
    ['Kapasitas Disk', ag.disk_capacity_gb ? `${ag.disk_capacity_gb.toFixed(0)} GB` : '—'],
    ['Aplikasi Terdeteksi', ag.installed_software_count ?? '—'],
    ['Proses Berjalan', ag.running_process_count ?? '—'],
  ];
  return `<table class="device-profile-table">
    <tbody>${rows.map(([label, val]) => `<tr><td>${label}</td><td>${esc(String(val))}</td></tr>`).join('')}</tbody>
  </table>`;
}

const NETWORK_MODE_LABELS = { ethernet: 'Ethernet', wifi: 'WiFi', both: 'Keduanya' };

function buildNetworkModeControl(ag) {
  if (!ag) return '';
  const desired = ag.desired_network_mode || 'both';
  const buttons = Object.entries(NETWORK_MODE_LABELS).map(([mode, label]) => `
    <button class="btn-sm ${mode === desired ? 'btn-network-mode-active' : ''}"
      onclick="setNetworkMode('${esc(ag.id)}', '${mode}', this)">${label}</button>`).join('');
  const statusText = ag.current_network_mode
    ? `Status: ${esc(NETWORK_MODE_LABELS[ag.current_network_mode] || ag.current_network_mode)}`
      + (ag.network_mode_status && ag.network_mode_status !== 'ok' ? ` (${esc(ag.network_mode_status)})` : '')
    : 'Status: belum ada laporan dari agent';
  return `<div class="network-mode-control">${buttons}</div>
    <p class="no-data" style="margin-top:6px">${statusText}</p>`;
}

async function setNetworkMode(agentID, mode, btn) {
  if (!confirm(`Set mode jaringan PC ini ke "${NETWORK_MODE_LABELS[mode]}"?`)) return;
  btn.disabled = true;
  try {
    const res = await api('POST', `/agents/${agentID}/network-mode`, { mode });
    const ag = allAgents.find(a => a.id === agentID);
    if (ag) {
      ag.desired_network_mode = mode;
      if (res.result) {
        ag.current_network_mode = res.result.network_mode;
        ag.network_mode_status = res.result.status;
      }
    }
    const tr = document.getElementById('detail-' + agentID);
    if (tr) loadAgentDetail(agentID, tr);
  } catch (e) {
    alert('Gagal set mode jaringan: ' + e.message);
  } finally {
    btn.disabled = false;
  }
}

function buildEventList(events) {
  if (!events || !events.length) return '<p class="no-data">Belum ada event tercatat</p>';
  const rows = events.slice(0, 10).map(e => `
    <tr>
      <td style="font-size:11px;color:var(--muted);white-space:nowrap">${fmtTime(e.created_at)}</td>
      <td>${esc(eventTypeLabel(e.type))}</td>
      <td><span class="badge badge-${e.action}">${esc(e.action)}</span></td>
    </tr>`).join('');
  return `<table class="device-profile-table">
    <thead><tr><th>Waktu</th><th>Tipe</th><th>Aksi</th></tr></thead>
    <tbody>${rows}</tbody>
  </table>`;
}

async function loadAgentDetail(id, tr) {
  try {
    const [procs, metrics, events] = await Promise.all([
      api('GET', `/agents/${id}/processes`),
      api('GET', `/agents/${id}/metrics`),
      api('GET', `/agents/${id}/events?limit=10`).catch(() => []),
    ]);
    const ag = allAgents.find(a => a.id === id);
    tr.querySelector('.detail-content').innerHTML = `
      <div class="detail-grid">
        <div>
          <h4>CPU &amp; RAM — 24 Jam Terakhir</h4>
          ${buildSparklines(metrics)}
          <h4 style="margin-top:14px">Profil Perangkat</h4>
          ${buildDeviceProfile(ag)}
          <h4 style="margin-top:14px">Mode Jaringan</h4>
          ${buildNetworkModeControl(ag)}
        </div>
        <div>
          <h4>Proses Aktif (${(procs||[]).length})</h4>
          ${buildProcTable(procs, id)}
          <h4 style="margin-top:14px">Event Terkini</h4>
          ${buildEventList(events)}
        </div>
      </div>`;
  } catch (e) {
    tr.querySelector('.detail-content').innerHTML =
      `<p class="no-data" style="color:var(--red)">${esc(e.message)}</p>`;
  }
}

async function updateDeviceGroup(agentID, group) {
  try {
    await api('PATCH', `/agents/${agentID}`, { device_group: group });
    const ag = allAgents.find(a => a.id === agentID);
    if (ag) ag.device_group = group;
  } catch (e) {
    alert('Gagal update device group: ' + e.message);
  }
}

async function deleteAgent(id, hostname) {
  if (!confirm(`Hapus agent "${hostname}"?\n\nSemua data (metrics, proses, alerts) akan dihapus permanen.`)) return;
  try {
    await api('DELETE', `/agents/${id}`);
    const row = document.querySelector(`tr.agent-row[data-id="${id}"]`);
    const detail = document.getElementById('detail-' + id);
    if (detail) detail.remove();
    if (row) row.remove();
    expandedRows.delete(id);
    allAgents = allAgents.filter(a => a.id !== id);
  } catch (e) {
    alert('Gagal hapus agent: ' + e.message);
  }
}

async function killProcess(agentID, pid, name, btn) {
  if (!confirm(`Kill proses "${name}" (PID ${pid}) di PC ini?`)) return;
  btn.disabled = true;
  btn.classList.add('killing');
  try {
    const res = await api('POST', `/agents/${agentID}/kill`, { pid, name });
    const row = document.getElementById('proc-row-' + pid);
    if (row) {
      row.classList.add('proc-killed');
      setTimeout(() => row.remove(), 600);
    }
  } catch (e) {
    alert('Gagal kill proses: ' + e.message);
    btn.disabled = false;
    btn.classList.remove('killing');
  }
}

function buildSparklines(metrics) {
  if (!metrics || metrics.length < 2)
    return '<p class="no-data">Data metrik belum tersedia</p>';

  const W = 300, H = 50, n = metrics.length;
  const xs = i => (i / (n - 1) * W).toFixed(1);
  const cpuPts = metrics.map((m, i) => `${xs(i)},${(H - m.cpu / 100 * H).toFixed(1)}`).join(' ');
  const ramPts = metrics.map((m, i) => `${xs(i)},${(H - m.ram / 100 * H).toFixed(1)}`).join(' ');
  const last = metrics[n - 1];
  return `
    <svg width="${W}" height="${H}" viewBox="0 0 ${W} ${H}" class="sparkline">
      <line x1="0" y1="${(H*0.15).toFixed(1)}" x2="${W}" y2="${(H*0.15).toFixed(1)}"
            stroke="#fee2e2" stroke-width=".5" stroke-dasharray="4"/>
      <polyline points="${cpuPts}" fill="none" stroke="#2563eb" stroke-width="1.5" stroke-linejoin="round"/>
      <polyline points="${ramPts}" fill="none" stroke="#dc2626" stroke-width="1.5" stroke-linejoin="round"/>
    </svg>
    <div class="spark-legend">
      <span class="legend-cpu">■ CPU: ${last.cpu.toFixed(1)}%</span>
      <span class="legend-ram">■ RAM: ${last.ram.toFixed(1)}%</span>
    </div>`;
}

const KILL_SVG = `<svg xmlns="http://www.w3.org/2000/svg" width="13" height="13" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="7.86 2 16.14 2 22 7.86 22 16.14 16.14 22 7.86 22 2 16.14 2 7.86 7.86 2"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>`;

function buildProcTable(procs, agentID) {
  if (!procs || !procs.length) return '<p class="no-data">Tidak ada proses aktif</p>';
  const rows = procs.slice(0, 25).map(p => `
    <tr class="proc-row" id="proc-row-${p.pid}">
      <td>${esc(p.name)}</td>
      <td style="font-family:monospace">${p.pid}</td>
      <td>${(p.cpu || 0).toFixed(1)}%</td>
      <td>${(p.ram || 0).toFixed(1)}%</td>
      <td>
        <button class="kill-btn" title="Kill ${esc(p.name)} (PID ${p.pid})"
          onclick="killProcess('${agentID}', ${p.pid}, '${esc(p.name)}', this)">
          ${KILL_SVG}
        </button>
      </td>
    </tr>`).join('');
  return `<table class="proc-table">
    <thead><tr><th>Proses</th><th>PID</th><th>CPU</th><th>RAM</th><th></th></tr></thead>
    <tbody>${rows}</tbody>
  </table>`;
}

// ── Deploy Tab ─────────────────────────────────────────────────────────────
function showDTab(name) {
  document.querySelectorAll('.deploy-tab').forEach(el => el.classList.remove('active'));
  document.querySelectorAll('.inner-tab').forEach(el => el.classList.remove('active'));
  document.getElementById('dtab-' + name).classList.add('active');
  document.querySelector(`.inner-tab[data-dtab="${name}"]`).classList.add('active');
}

function renderDeployAgentList() {
  const el = document.getElementById('deploy-agent-list');
  if (!allAgents.length) {
    el.innerHTML = '<p class="no-data" style="padding:12px">Tidak ada agent</p>';
    return;
  }
  el.innerHTML = allAgents.map(ag => `
    <label class="agent-check-item">
      <input type="checkbox" value="${esc(ag.id)}"
        ${deployTargets.has(ag.id) ? 'checked' : ''}
        onchange="toggleTarget('${esc(ag.id)}', this.checked)">
      <span class="dot ${ag.status === 'online' ? 'online' : 'offline'}"></span>
      <span>${esc(ag.hostname)}</span>
      <span class="ip">${esc(ag.ip)}</span>
    </label>`).join('');
}

function toggleTarget(id, on) {
  if (on) deployTargets.add(id);
  else    deployTargets.delete(id);
}

function selectTargets(mode) {
  if      (mode === 'all')    allAgents.forEach(a => deployTargets.add(a.id));
  else if (mode === 'online') allAgents.forEach(a => { if (a.status === 'online') deployTargets.add(a.id); else deployTargets.delete(a.id); });
  else                        deployTargets.clear();
  renderDeployAgentList();
}

// deployAdvancedFields reads the priority/expire/max-retry inputs shared by
// every deploy type — generic to all of them, so it lives once here instead
// of being duplicated into each dtab-* panel.
function deployAdvancedFields() {
  const priority = parseInt(document.getElementById('deploy-priority').value, 10) || 0;
  const expireLocal = document.getElementById('deploy-expire').value;
  const expire_at = expireLocal ? new Date(expireLocal).toISOString() : '';
  const mr = parseInt(document.getElementById('deploy-maxretry').value, 10);
  const fields = { priority, expire_at };
  if (Number.isFinite(mr)) fields.max_retry = mr;
  return fields;
}

async function submitDeploy(mode) {
  const targets = Array.from(deployTargets);
  if (!targets.length) { alert('Pilih minimal satu PC terlebih dahulu.'); return; }

  let type = 'exec', payload = '', args = '';

  if (mode === 'ps') {
    payload = document.getElementById('ps-cmd').value.trim();
    if (!payload) { alert('Masukkan perintah PowerShell.'); return; }

  } else if (mode === 'winget') {
    const pkg    = document.getElementById('wg-pkg').value.trim();
    const action = document.querySelector('input[name="wg-action"]:checked').value;
    if (!pkg) { alert('Masukkan Package ID Winget.'); return; }
    payload = action === 'install'
      ? `winget install --id ${pkg} --silent --accept-source-agreements --accept-package-agreements`
      : `winget uninstall --id ${pkg} --silent`;

  } else if (mode === 'file') {
    if (!uploadedFile) { alert('Upload file terlebih dahulu.'); return; }
    type    = 'file_deploy';
    payload = uploadedFile;
    args    = document.getElementById('file-args').value.trim();
  }

  try {
    const job = await api('POST', '/deploy', { type, payload, args, targets, ...deployAdvancedFields() });
    loadDeployHistory();
    startJobPoller(job.id);
  } catch (e) {
    alert('Gagal deploy: ' + e.message);
  }
}

async function submitDeepFreeze() {
  const targets = Array.from(deployTargets);
  if (!targets.length) { alert('Pilih minimal satu PC terlebih dahulu.'); return; }

  const action   = document.getElementById('df-action').value;
  const password = document.getElementById('df-password').value.trim();

  if (action === 'thaw' || action === 'freeze') {
    const label = action === 'thaw' ? 'THAW (cair)' : 'FREEZE (beku)';
    if (!confirm(`Yakin ingin ${label} ${targets.length} PC?\nPC akan restart segera.`)) return;
  }

  try {
    const job = await api('POST', '/deploy', {
      type: 'deepfreeze',
      payload: action,
      args: password,
      targets,
      ...deployAdvancedFields(),
    });
    loadDeployHistory();
    startJobPoller(job.id);
  } catch (e) {
    alert('Gagal: ' + e.message);
  }
}

async function submitInstallSSH() {
  const targets = Array.from(deployTargets);
  if (!targets.length) { alert('Pilih minimal satu PC terlebih dahulu.'); return; }

  const adminIP = document.getElementById('ssh-admin-ip').value.trim();
  const ipNote  = adminIP ? ` (SSH dibatasi ke ${adminIP})` : ' (SSH terbuka untuk semua IP)';

  if (!confirm(`Install OpenSSH Server ke ${targets.length} PC?${ipNote}`)) return;

  try {
    const job = await api('POST', '/deploy', {
      type: 'install_ssh',
      payload: 'install_ssh',
      args: adminIP,
      targets,
      ...deployAdvancedFields(),
    });
    loadDeployHistory();
    startJobPoller(job.id);
  } catch (e) {
    alert('Gagal: ' + e.message);
  }
}

async function uploadFile() {
  const input  = document.getElementById('file-input');
  const status = document.getElementById('upload-status');
  if (!input.files.length) { alert('Pilih file terlebih dahulu.'); return; }

  status.className = '';
  status.textContent = 'Mengupload…';
  try {
    const fd = new FormData();
    fd.append('file', input.files[0]);
    const r = await api('POST', '/upload', fd);
    uploadedFile = r.filename;
    status.className = 'ok';
    status.textContent = '✓ ' + r.filename;
    document.getElementById('btn-deploy-file').disabled = false;
  } catch (e) {
    status.className = 'err';
    status.textContent = '✗ ' + e.message;
  }
}

async function loadDeployHistory() {
  try {
    const jobs = await api('GET', '/deploy');
    renderDeployHistory(jobs || []);
  } catch (e) { console.error(e); }
}

function renderDeployHistory(jobs) {
  const el = document.getElementById('deploy-history');
  if (!jobs.length) {
    el.innerHTML = '<p class="no-data">Belum ada job deploy</p>';
    return;
  }
  el.innerHTML = jobs.slice(0, 30).map(j => {
    const shortPayload = j.payload.length > 70 ? j.payload.slice(0, 70) + '…' : j.payload;
    const canCancel = j.status === 'pending' || j.status === 'dispatched';
    const cancelBtn = canCancel
      ? `<button class="btn-sm btn-danger" onclick="cancelDeployJob('${esc(j.id)}',event)" title="Batalkan job ini">✕ Batalkan</button>`
      : '';
    const priorityBadge = j.priority ? `<span class="badge badge-priority" title="Priority">P${j.priority}</span>` : '';
    return `
      <div class="job-card">
        <div class="job-card-hdr" onclick="toggleJobCard('${esc(j.id)}')">
          <span class="badge badge-type">${esc(j.type)}</span>
          <span class="job-payload" title="${esc(j.payload)}">${esc(shortPayload)}</span>
          ${priorityBadge}
          <span class="badge badge-${j.status}">${j.status}</span>
          <span style="color:var(--muted);flex-shrink:0">${timeSince(j.created_at)}</span>
          ${cancelBtn}
        </div>
        <div id="jresults-${esc(j.id)}" class="job-results"></div>
      </div>`;
  }).join('');
}

function toggleJobCard(id) {
  const el = document.getElementById('jresults-' + id);
  if (!el) return;
  const isOpen = el.classList.toggle('open');
  if (isOpen && !el.innerHTML.trim()) loadJobResults(id);
}

async function cancelDeployJob(id, event) {
  event.stopPropagation();
  if (!confirm('Batalkan job ini? Semua target yang masih pending akan dibatalkan.')) return;
  try {
    await api('DELETE', '/deploy/' + id);
    await loadDeployHistory();
  } catch (e) {
    alert('Gagal batalkan job: ' + e.message);
  }
}

async function loadJobResults(id) {
  const el = document.getElementById('jresults-' + id);
  if (!el) return;
  try {
    const data    = await api('GET', '/deploy/' + id);
    const results = data.results || [];
    if (!results.length) {
      el.innerHTML = '<p class="no-data" style="padding:10px">Belum ada hasil</p>';
      return;
    }
    el.innerHTML = results.map(r => {
      const meta = [];
      if (r.retry_count)             meta.push(`retry ${r.retry_count}`);
      if (r.exit_code !== undefined && r.exit_code !== null) meta.push(`exit ${r.exit_code}`);
      if (r.duration_ms !== undefined && r.duration_ms !== null) meta.push(`${r.duration_ms}ms`);
      const metaStr = meta.length ? `<span class="result-meta">${esc(meta.join(' · '))}</span>` : '';
      return `
      <div class="result-row">
        <span class="result-status ${r.status}">${r.status}</span>
        <span class="result-agent">${esc(agentName(r.agent_id))}</span>
        <span class="result-output">${esc(r.output || '(menunggu…)')}</span>
        ${metaStr}
      </div>`;
    }).join('');
  } catch (e) {
    if (el) el.innerHTML = `<p class="no-data" style="padding:10px;color:var(--red)">${esc(e.message)}</p>`;
  }
}

function startJobPoller(jobId) {
  if (deployPollers[jobId]) clearInterval(deployPollers[jobId]);
  let ticks = 0;
  deployPollers[jobId] = setInterval(async () => {
    ticks++;
    const data = await api('GET', '/deploy/' + jobId).catch(() => null);
    if (!data) return;
    // Refresh results if card is open
    const el = document.getElementById('jresults-' + jobId);
    if (el && el.classList.contains('open')) loadJobResults(jobId);
    await loadDeployHistory();
    const allDone = (data.results || []).every(r => !['pending', 'running'].includes(r.status));
    if (allDone || ticks > 120) {
      clearInterval(deployPollers[jobId]);
      delete deployPollers[jobId];
    }
  }, 2000);
}

// ── Alerts Tab ─────────────────────────────────────────────────────────────
async function loadAlerts() {
  try {
    const alerts = await api('GET', '/alerts?limit=50');
    const el = document.getElementById('alerts-list');
    if (!alerts.length) {
      el.innerHTML = '<p class="no-data" style="padding:24px;text-align:center">Tidak ada alert</p>';
      return;
    }
    el.innerHTML = alerts.map(a => `
      <div class="alert-item">
        <span class="alert-badge ${esc(a.type)}">${alertLabel(a.type)}</span>
        <span class="alert-msg">${esc(a.message)}</span>
        <span class="alert-time" title="${fmtTime(a.sent_at)}">${timeSince(a.sent_at)}</span>
      </div>`).join('');
  } catch (e) { console.error(e); }
}

// ── Logs Tab ───────────────────────────────────────────────────────────────
async function loadLogs() {
  const el = document.getElementById('logs-content');
  try {
    const data = await api('GET', '/logs?lines=100');
    el.textContent = data.lines || '(log kosong)';
  } catch (e) {
    el.textContent = 'Error: ' + e.message;
  }
}

// ── Settings Modal ─────────────────────────────────────────────────────────
function openSettings() {
  document.getElementById('modal-settings').classList.add('open');
  loadSettings();
}

function closeSettings(e) {
  if (e instanceof Event && e.target !== document.getElementById('modal-settings')) return;
  document.getElementById('modal-settings').classList.remove('open');
}

async function loadSettings() {
  try {
    const s = await api('GET', '/settings');
    document.getElementById('s-cpu').value       = s.cpu_threshold          || '85';
    document.getElementById('s-ram').value       = s.ram_threshold          || '85';
    document.getElementById('s-offline').value   = s.offline_after_minutes  || '5';
    document.getElementById('s-tg-token').value  = s.telegram_token         || '';
    document.getElementById('s-tg-chat').value   = s.telegram_chat_id       || '';
    document.getElementById('s-smtp-host').value = s.smtp_host              || '';
    document.getElementById('s-smtp-port').value = s.smtp_port              || '587';
    document.getElementById('s-smtp-tls').value  = s.smtp_tls               || 'starttls';
    document.getElementById('s-smtp-user').value = s.smtp_user              || '';
    document.getElementById('s-smtp-pass').value = s.smtp_pass              || '';
    document.getElementById('s-smtp-to').value   = s.smtp_to                || '';
    document.getElementById('s-mesh-url').value  = s.mesh_url               || '';
    document.getElementById('s-auto-kill').checked = s.auto_kill_enabled === 'true';
    meshBaseURL = s.mesh_url || '';
    try {
      const bl = JSON.parse(s.blacklist || '[]');
      document.getElementById('s-blacklist').value = bl.join('\n');
    } catch (_) {}
  } catch (e) { console.error(e); }
}

async function saveSettings() {
  const blRaw = document.getElementById('s-blacklist').value.trim();
  const blacklist = blRaw ? blRaw.split('\n').map(s => s.trim()).filter(Boolean) : [];
  try {
    await api('POST', '/settings', {
      cpu_threshold:         document.getElementById('s-cpu').value,
      ram_threshold:         document.getElementById('s-ram').value,
      offline_after_minutes: document.getElementById('s-offline').value,
      blacklist,
      auto_kill_enabled:     document.getElementById('s-auto-kill').checked,
      telegram_token:        document.getElementById('s-tg-token').value,
      telegram_chat_id:      document.getElementById('s-tg-chat').value,
      smtp_host:             document.getElementById('s-smtp-host').value,
      smtp_port:             document.getElementById('s-smtp-port').value,
      smtp_tls:              document.getElementById('s-smtp-tls').value,
      smtp_user:             document.getElementById('s-smtp-user').value,
      smtp_pass:             document.getElementById('s-smtp-pass').value,
      smtp_to:               document.getElementById('s-smtp-to').value,
      mesh_url:              document.getElementById('s-mesh-url').value,
    });
    meshBaseURL = document.getElementById('s-mesh-url').value;
    alert('✓ Pengaturan berhasil disimpan.');
    document.getElementById('modal-settings').classList.remove('open');
  } catch (e) {
    alert('Gagal menyimpan: ' + e.message);
  }
}

async function testTelegram() {
  try {
    await api('POST', '/test/telegram');
    alert('✓ Pesan Telegram berhasil dikirim.');
  } catch (e) { alert('Gagal: ' + e.message); }
}

async function testEmail() {
  try {
    await api('POST', '/test/email');
    alert('✓ Email berhasil dikirim.');
  } catch (e) { alert('Gagal: ' + e.message); }
}

// ── Applications Tab ─────────────────────────────────────────────────────────
const APP_STATUS_LABEL = {
  pending_review: 'Pending Review', allowed: 'Allowed', blocked: 'Blocked', ignored: 'Ignored',
};

async function loadApplications() {
  const tbody = document.getElementById('applications-tbody');
  try {
    if (!allCategories.length) {
      allCategories = await api('GET', '/categories') || [];
    }
    const qs = appFilterStatus ? `?status=${encodeURIComponent(appFilterStatus)}` : '';
    const apps = await api('GET', '/applications' + qs);
    renderApplications(apps || []);

    // Pending-review badge always reflects the true count, regardless of the
    // currently selected filter.
    const pending = appFilterStatus === 'pending_review'
      ? (apps || [])
      : await api('GET', '/applications?status=pending_review');
    document.getElementById('app-pending-count').textContent = (pending || []).length;
  } catch (e) {
    console.error('loadApplications:', e);
    if (tbody) tbody.innerHTML = `<tr><td colspan="8" class="empty" style="color:#dc2626">Gagal memuat data: ${esc(e.message)}</td></tr>`;
  }
}

function setAppFilter(status) {
  appFilterStatus = status;
  document.querySelectorAll('#app-filter-tabs .inner-tab').forEach(el => {
    el.classList.toggle('active', el.dataset.status === status);
  });
  loadApplications();
}

function categoryOptionsHtml(selectedID) {
  const opts = ['<option value="">— Tanpa kategori —</option>']
    .concat(allCategories.map(c =>
      `<option value="${c.id}" ${selectedID === c.id ? 'selected' : ''}>${esc(c.name)}</option>`));
  return opts.join('');
}

function statusOptionsHtml(selected) {
  return Object.entries(APP_STATUS_LABEL)
    .map(([val, label]) => `<option value="${val}" ${selected === val ? 'selected' : ''}>${esc(label)}</option>`)
    .join('');
}

function renderApplications(apps) {
  const tbody = document.getElementById('applications-tbody');
  if (!apps.length) {
    tbody.innerHTML = '<tr><td colspan="8" class="empty">Tidak ada aplikasi pada filter ini</td></tr>';
    return;
  }
  tbody.innerHTML = apps.map(app => `
    <tr data-app-id="${app.id}">
      <td><strong>${esc(app.product_name || app.exe_name)}</strong></td>
      <td>${esc(app.company || '—')}</td>
      <td style="font-family:monospace;font-size:12px">${esc(app.exe_name)}</td>
      <td>
        <select onchange="updateApplication(${app.id}, this.value, null)">
          ${categoryOptionsHtml(app.category_id)}
        </select>
      </td>
      <td>${app.device_count}</td>
      <td>${app.total_executions}</td>
      <td style="font-size:12px;color:var(--muted)">${timeSince(app.last_seen)}</td>
      <td>
        <select class="badge badge-${app.status}" onchange="updateApplication(${app.id}, null, this.value)">
          ${statusOptionsHtml(app.status)}
        </select>
      </td>
    </tr>`).join('');
}

async function updateApplication(id, categoryValue, statusValue) {
  const row = document.querySelector(`tr[data-app-id="${id}"]`);
  const current = row ? {
    category_id: row.querySelector('td:nth-child(4) select').value || null,
    status: row.querySelector('td:nth-child(8) select').value,
  } : {};
  const body = {
    status: statusValue !== null ? statusValue : current.status,
    category_id: categoryValue !== null ? (categoryValue || null) : current.category_id,
  };
  if (body.category_id !== null) body.category_id = Number(body.category_id);
  try {
    await api('PATCH', `/applications/${id}`, body);
    loadApplications();
  } catch (e) {
    alert('Gagal update aplikasi: ' + e.message);
    loadApplications();
  }
}

// ── Events Tab (Phase 2 — Module 7 Event Timeline) ───────────────────────────
let eventFilterType = '';

const EVENT_TYPE_LABEL = {
  usb_inserted: 'USB Terpasang', usb_removed: 'USB Dilepas',
  download_created: 'File Baru', download_deleted: 'File Dihapus',
  wallpaper_changed: 'Wallpaper Diubah', theme_changed: 'Tema Diubah',
  config_changed: 'Konfigurasi Berubah', software_installed: 'Software Terinstall',
  software_removed: 'Software Dihapus', software_updated: 'Software Diperbarui',
  exec_policy: 'Kebijakan Eksekusi',
  peripheral_connected: 'Perangkat Terpasang', peripheral_removed: 'Perangkat Terlepas',
};
function eventTypeLabel(type) { return EVENT_TYPE_LABEL[type] || type; }

async function loadEvents() {
  const tbody = document.getElementById('events-tbody');
  try {
    const qs = eventFilterType ? `?type=${encodeURIComponent(eventFilterType)}&limit=200` : '?limit=200';
    const events = await api('GET', '/events' + qs);
    renderEvents(events || []);
  } catch (e) {
    console.error('loadEvents:', e);
    if (tbody) tbody.innerHTML = `<tr><td colspan="5" class="empty" style="color:#dc2626">Gagal memuat data: ${esc(e.message)}</td></tr>`;
  }
}

function setEventFilter(type) {
  eventFilterType = type;
  document.querySelectorAll('#event-filter-tabs .inner-tab').forEach(el => {
    el.classList.toggle('active', el.dataset.type === type);
  });
  loadEvents();
}

function renderEvents(events) {
  const tbody = document.getElementById('events-tbody');
  if (!events.length) {
    tbody.innerHTML = '<tr><td colspan="5" class="empty">Belum ada event pada filter ini</td></tr>';
    return;
  }
  tbody.innerHTML = events.map(e => {
    let detail = '';
    try { detail = JSON.stringify(JSON.parse(e.metadata)); } catch (_) { detail = e.metadata || ''; }
    return `
    <tr>
      <td style="font-size:12px;color:var(--muted);white-space:nowrap">${fmtTime(e.created_at)}</td>
      <td>${esc(e.hostname || e.agent_id)}</td>
      <td>${esc(eventTypeLabel(e.type))}</td>
      <td class="proc-name" title="${esc(detail)}">${esc(detail)}</td>
      <td><span class="badge badge-${e.action}">${esc(e.action)}</span></td>
    </tr>`;
  }).join('');
}

// ── Floor Map Tab ─────────────────────────────────────────────────────────
let computers = [];
let computerElements = new Map();
let currentComputerId = null;

const STATUS_META = {
  ONLINE_ACTIVE:  { label: 'Aktif' },
  ONLINE_IDLE:    { label: 'Idle' },
  ONLINE_UNUSED:  { label: 'Online (Tidak Dipakai)' },
  OFFLINE:        { label: 'Offline' },
  UNKNOWN:        { label: 'Tidak Diketahui' },
};

const CONNECTION_LABELS = { ethernet: 'LAN', wifi: 'WiFi' };

// The backend has no idle/session tracking, only online/offline + a live
// cpu reading, so activity level is approximated from CPU load rather than
// real usage telemetry.
function deriveStatus(agent) {
  if (agent.status !== 'online') return 'OFFLINE';
  if (agent.cpu === null || agent.cpu === undefined) return 'UNKNOWN';
  if (agent.cpu >= 8) return 'ONLINE_ACTIVE';
  if (agent.cpu > 0)  return 'ONLINE_IDLE';
  return 'ONLINE_UNUSED';
}

// Data source today: the real /api/agents list, reshaped into the floor-map
// computer schema. Swappable later for a dedicated /api/computers endpoint
// or WebSocket feed without touching the renderer below.
function mapAgentsToComputers(agents) {
  const sorted = [...agents].sort((a, b) => (a.hostname || '').localeCompare(b.hostname || ''));
  return sorted.map((ag, i) => {
    const row = Math.floor(i / 8);
    const posInRow = i % 8;
    const x = posInRow < 4 ? 1 + posInRow : 6 + (posInRow - 4);
    const y = 2 + row;
    return {
      id: ag.id,
      name: 'PC-' + String(i + 1).padStart(2, '0'),
      status: deriveStatus(ag),
      x, y,
      hostname: ag.hostname,
      ip: ag.ip,
      cpu: ag.cpu,
      ram: ag.ram,
      session: null,
      idle: null,
      lastSeen: ag.last_seen,
      agentVersion: ag.agent_version,
      os: ag.windows_version || ag.os,
      connection: CONNECTION_LABELS[ag.current_network_mode] || '—',
    };
  });
}

function buildLandmarks(rowCount) {
  const footerY = rowCount + 2;
  return [
    { type: 'entrance',  label: '🚪 Entrance',   x: 1, y: 1 },
    { type: 'window',    label: '🪟 Window',     x: 3, y: 1, colSpan: 5 },
    { type: 'stair',     label: '🪜 Stair',      x: 9, y: 1 },
    { type: 'walkway',   label: 'Walkway',       x: 5, y: 2, rowSpan: rowCount },
    { type: 'helpdesk',  label: '🛎 Help Desk',  x: 2, y: footerY },
    { type: 'printer',   label: '🖨 Printer',    x: 5, y: footerY },
    { type: 'bookshelf', label: '📚 Bookshelf',  x: 8, y: footerY },
  ];
}

function makeLandmarkEl(lm) {
  const div = document.createElement('div');
  div.className = 'landmark' + (lm.type === 'walkway' ? ' landmark-walkway' : '');
  div.textContent = lm.label;
  div.style.gridColumn = lm.colSpan ? `${lm.x} / span ${lm.colSpan}` : String(lm.x);
  div.style.gridRow = lm.rowSpan ? `${lm.y} / span ${lm.rowSpan}` : String(lm.y);
  return div;
}

function makeComputerCard(c) {
  const div = document.createElement('div');
  div.className = 'pc-card status-' + c.status.toLowerCase();
  div.style.gridColumn = String(c.x);
  div.style.gridRow = String(c.y);
  div.title = `${c.hostname || c.name} — ${(STATUS_META[c.status] || {}).label || c.status}`;
  div.innerHTML = `<span class="pc-dot"></span><span class="pc-name">${esc(c.name)}</span>`;
  div.addEventListener('click', () => openComputerModal(c.id));
  return div;
}

// Full rebuild — only called on first load or when the set of agents
// (added/removed) actually changes. Routine refreshes go through
// updateComputer() instead so 100+ cards don't get re-created every poll.
function renderFloorMap() {
  const grid = document.getElementById('floor-map-grid');
  if (!grid) return;
  grid.innerHTML = '';
  computerElements.clear();

  if (!computers.length) {
    grid.style.gridTemplateRows = '';
    grid.innerHTML = '<p class="empty">Belum ada agent yang terhubung</p>';
    return;
  }

  const rowCount = Math.max(...computers.map(c => c.y)) - 1;
  grid.style.gridTemplateRows = `repeat(${rowCount + 2}, 72px)`;

  const frag = document.createDocumentFragment();
  buildLandmarks(rowCount).forEach(lm => frag.appendChild(makeLandmarkEl(lm)));
  computers.forEach(c => {
    const card = makeComputerCard(c);
    computerElements.set(c.id, card);
    frag.appendChild(card);
  });
  grid.appendChild(frag);
}

// Hook point for future real-time updates (WebSocket push etc.) — patches
// one card in place without touching the rest of the grid.
function updateComputer(id, patch) {
  const c = computers.find(x => x.id === id);
  if (!c) return;
  Object.assign(c, patch);
  const el = computerElements.get(id);
  if (!el) return;
  el.className = 'pc-card status-' + c.status.toLowerCase();
  el.title = `${c.hostname || c.name} — ${(STATUS_META[c.status] || {}).label || c.status}`;
}

function updateSummary() {
  const total   = computers.length;
  const offline = computers.filter(c => c.status === 'OFFLINE').length;
  const active  = computers.filter(c => c.status === 'ONLINE_ACTIVE').length;
  const idle    = computers.filter(c => c.status === 'ONLINE_IDLE').length;
  const unused  = computers.filter(c => c.status === 'ONLINE_UNUSED').length;
  document.getElementById('floor-stat-total').textContent   = total;
  document.getElementById('floor-stat-online').textContent  = total - offline;
  document.getElementById('floor-stat-offline').textContent = offline;
  document.getElementById('floor-stat-active').textContent  = active;
  document.getElementById('floor-stat-idle').textContent    = idle;
  document.getElementById('floor-stat-unused').textContent  = unused;
}

function diffAndRenderFloorMap(newComputers) {
  const prevIds = new Set(computers.map(c => c.id));
  const sameSet = prevIds.size === newComputers.length && newComputers.every(c => prevIds.has(c.id));

  computers = newComputers;

  if (sameSet && computerElements.size) {
    computers.forEach(c => updateComputer(c.id, c));
  } else {
    renderFloorMap();
  }
}

async function loadFloorMap() {
  try {
    await loadAgents();
    diffAndRenderFloorMap(mapAgentsToComputers(allAgents));
    updateSummary();
  } catch (e) {
    console.error('loadFloorMap:', e);
    const grid = document.getElementById('floor-map-grid');
    if (grid) grid.innerHTML = `<p class="empty" style="color:#dc2626">Gagal memuat denah: ${esc(e.message)}</p>`;
  }
}

function openComputerModal(id) {
  const c = computers.find(x => x.id === id);
  if (!c) return;
  currentComputerId = c.id;
  const cpuTxt = typeof c.cpu === 'number' ? c.cpu.toFixed(1) + '%' : '—';
  const ramTxt = typeof c.ram === 'number' ? c.ram.toFixed(1) + '%' : '—';
  document.getElementById('cd-title').textContent         = c.name;
  document.getElementById('cd-name').textContent          = c.name;
  document.getElementById('cd-hostname').textContent      = c.hostname || '—';
  document.getElementById('cd-status').textContent        = (STATUS_META[c.status] || {}).label || c.status;
  document.getElementById('cd-session').textContent       = c.session || 'Tidak tersedia';
  document.getElementById('cd-idle').textContent          = c.idle || 'Tidak tersedia';
  document.getElementById('cd-lastseen').textContent      = fmtTime(c.lastSeen);
  document.getElementById('cd-cpu').textContent            = cpuTxt;
  document.getElementById('cd-ram').textContent             = ramTxt;
  document.getElementById('cd-ip').textContent             = c.ip || '—';
  document.getElementById('cd-agent-version').textContent  = c.agentVersion || '—';
  document.getElementById('cd-os').textContent             = c.os || '—';
  document.getElementById('cd-connection').textContent     = c.connection || '—';
  document.getElementById('modal-computer-detail').classList.add('open');
}

function closeComputerModal(e) {
  if (e instanceof Event && e.target !== document.getElementById('modal-computer-detail')) return;
  document.getElementById('modal-computer-detail').classList.remove('open');
  currentComputerId = null;
}

async function sendComputerCommand(action, label) {
  if (!currentComputerId) return;
  const c = computers.find(x => x.id === currentComputerId);
  const name = c ? c.name : currentComputerId;
  if (!confirm(`Yakin ingin ${label} "${name}"?`)) return;
  try {
    await api('POST', '/v1/commands', { target: currentComputerId, action });
    closeComputerModal();
  } catch (e) {
    alert(`Gagal ${label}: ` + e.message);
  }
}

function restartComputer() {
  sendComputerCommand('restart', 'restart');
}

function shutdownComputer() {
  sendComputerCommand('shutdown', 'shutdown');
}

// ── Policy Rules Modal (Phase 2 — Module 8 Policy Engine) ────────────────────

async function openPolicyRules() {
  document.getElementById('modal-policy-rules').classList.add('open');
  if (!allCategories.length) {
    try { allCategories = await api('GET', '/categories') || []; } catch (_) { /* ignore */ }
  }
  const catSelect = document.getElementById('pr-category');
  catSelect.innerHTML = '<option value="">— Semua kategori —</option>' +
    allCategories.map(c => `<option value="${c.id}">${esc(c.name)}</option>`).join('');
  document.getElementById('pr-app-status').innerHTML =
    '<option value="">— Semua status —</option>' + statusOptionsHtml('');
  loadPolicyRules();
}

function closePolicyRules(e) {
  if (e instanceof Event && e.target !== document.getElementById('modal-policy-rules')) return;
  document.getElementById('modal-policy-rules').classList.remove('open');
}

async function loadPolicyRules() {
  const tbody = document.getElementById('policy-rules-tbody');
  try {
    const rules = await api('GET', '/policy-rules');
    renderPolicyRules(rules || []);
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="10" class="empty" style="color:#dc2626">${esc(e.message)}</td></tr>`;
  }
}

function renderPolicyRules(rules) {
  const tbody = document.getElementById('policy-rules-tbody');
  if (!rules.length) {
    tbody.innerHTML = '<tr><td colspan="10" class="empty">Belum ada policy rule</td></tr>';
    return;
  }
  const catName = id => (allCategories.find(c => c.id === id) || {}).name || '—';
  tbody.innerHTML = rules.map(r => `
    <tr>
      <td>${esc(r.name)}</td>
      <td>${esc(r.event_type || '—')}</td>
      <td>${r.category_id ? esc(catName(r.category_id)) : '—'}</td>
      <td>${r.app_status ? esc(APP_STATUS_LABEL[r.app_status] || r.app_status) : '—'}</td>
      <td>${esc(r.file_extension || '—')}</td>
      <td>${esc(r.execution_location || '—')}</td>
      <td>${esc(r.device_group || '—')}</td>
      <td><span class="badge badge-${r.action}">${esc(r.action)}</span></td>
      <td>
        <input type="checkbox" ${r.enabled ? 'checked' : ''}
          onchange="togglePolicyRuleEnabled(${r.id}, this.checked, ${JSON.stringify(r).replace(/"/g,'&quot;')})">
      </td>
      <td><button class="btn-sm" onclick="deletePolicyRule(${r.id})">Hapus</button></td>
    </tr>`).join('');
}

async function createPolicyRule() {
  const name = document.getElementById('pr-name').value.trim();
  if (!name) { alert('Nama rule wajib diisi'); return; }
  const categoryVal = document.getElementById('pr-category').value;
  const body = {
    name,
    event_type: document.getElementById('pr-event-type').value.trim(),
    category_id: categoryVal ? Number(categoryVal) : null,
    app_status: document.getElementById('pr-app-status').value,
    file_extension: document.getElementById('pr-extension').value.trim(),
    execution_location: document.getElementById('pr-location').value.trim(),
    device_group: document.getElementById('pr-device-group').value.trim(),
    action: document.getElementById('pr-action').value,
    enabled: true,
  };
  try {
    await api('POST', '/policy-rules', body);
    document.getElementById('pr-name').value = '';
    document.getElementById('pr-event-type').value = '';
    document.getElementById('pr-extension').value = '';
    document.getElementById('pr-location').value = '';
    document.getElementById('pr-device-group').value = '';
    loadPolicyRules();
  } catch (e) {
    alert('Gagal tambah rule: ' + e.message);
  }
}

async function togglePolicyRuleEnabled(id, enabled, rule) {
  try {
    await api('PATCH', `/policy-rules/${id}`, { ...rule, enabled });
  } catch (e) {
    alert('Gagal update rule: ' + e.message);
    loadPolicyRules();
  }
}

async function deletePolicyRule(id) {
  if (!confirm('Hapus policy rule ini?')) return;
  try {
    await api('DELETE', `/policy-rules/${id}`);
    loadPolicyRules();
  } catch (e) {
    alert('Gagal hapus rule: ' + e.message);
  }
}

// ── Agent Logs Modal ────────────────────────────────────────────────────────
async function openAgentLogs(id, hostname) {
  document.getElementById('agent-logs-title').textContent = 'Log Agent — ' + hostname;
  document.getElementById('modal-agent-logs').classList.add('open');
  const el = document.getElementById('agent-logs-content');
  el.textContent = 'Memuat…';
  try {
    const data = await api('GET', '/agents/' + id + '/logs');
    el.textContent = data.lines || '(log kosong)';
  } catch (e) {
    el.textContent = 'Error: ' + e.message;
  }
}

function closeAgentLogs(e) {
  if (e instanceof Event && e.target !== document.getElementById('modal-agent-logs')) return;
  document.getElementById('modal-agent-logs').classList.remove('open');
}

// ── Keyboard shortcuts ──────────────────────────────────────────────────────
document.addEventListener('keydown', e => {
  if (e.key === 'Escape') {
    closeSettings();
    closeAgentLogs();
    closeComputerModal();
  }
});

// ── Init ───────────────────────────────────────────────────────────────────
(async function init() {
  // support Enter key on login form
  ['login-user', 'login-pass'].forEach(id => {
    const el = document.getElementById(id);
    if (el) el.addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });
  });

  if (!getToken()) {
    showLogin();
    return;
  }
  const btn = document.getElementById('btn-logout');
  if (btn) btn.style.display = '';
  await loadAgents();
  refreshTimer = setInterval(loadAgents, 10000);
})();
