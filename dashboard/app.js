'use strict';

// ── State ──────────────────────────────────────────────────────────────────
let allAgents     = [];
let deployTargets = new Set();
let uploadedFile  = null;
let expandedRows  = new Set();
let deployPollers = {};
let refreshTimer  = null;
let meshBaseURL   = '';

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
           recovery:'Online Kembali', blacklisted_app:'Aplikasi Terlarang' }[type] || type;
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
    if (tbody) tbody.innerHTML = `<tr><td colspan="8" class="empty" style="color:#dc2626">Gagal memuat data: ${esc(e.message)}</td></tr>`;
  }
}

function renderAgents(agents) {
  const tbody = document.getElementById('agents-tbody');
  if (!agents.length) {
    tbody.innerHTML = '<tr><td colspan="8" class="empty">Belum ada agent yang terhubung</td></tr>';
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
      <td>${barHtml(ag.cpu)}</td>
      <td>${barHtml(ag.ram)}</td>
      <td class="proc-name" title="${esc(ag.top_process)}">${esc(ag.top_process || '—')}</td>
      <td style="font-size:12px;color:var(--muted)">${timeSince(ag.last_seen)}</td>
      <td class="actions">
        ${meshLink}
        <button class="btn-sm"
          onclick="openAgentLogs('${esc(ag.id)}','${esc(ag.hostname)}');event.stopPropagation()">Logs</button>
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
  tr.innerHTML = `<td colspan="8"><div class="detail-content"><p class="no-data">Memuat…</p></div></td>`;
  return tr;
}

async function loadAgentDetail(id, tr) {
  try {
    const [procs, metrics] = await Promise.all([
      api('GET', `/agents/${id}/processes`),
      api('GET', `/agents/${id}/metrics`),
    ]);
    tr.querySelector('.detail-content').innerHTML = `
      <div class="detail-grid">
        <div>
          <h4>CPU &amp; RAM — 24 Jam Terakhir</h4>
          ${buildSparklines(metrics)}
        </div>
        <div>
          <h4>Proses Aktif (${(procs||[]).length})</h4>
          ${buildProcTable(procs, id)}
        </div>
      </div>`;
  } catch (e) {
    tr.querySelector('.detail-content').innerHTML =
      `<p class="no-data" style="color:var(--red)">${esc(e.message)}</p>`;
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
    const job = await api('POST', '/deploy', { type, payload, args, targets });
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
    return `
      <div class="job-card">
        <div class="job-card-hdr" onclick="toggleJobCard('${esc(j.id)}')">
          <span class="badge badge-type">${esc(j.type)}</span>
          <span class="job-payload" title="${esc(j.payload)}">${esc(shortPayload)}</span>
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
    el.innerHTML = results.map(r => `
      <div class="result-row">
        <span class="result-status ${r.status}">${r.status}</span>
        <span class="result-agent">${esc(agentName(r.agent_id))}</span>
        <span class="result-output">${esc(r.output || '(menunggu…)')}</span>
      </div>`).join('');
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
    const allDone = (data.results || []).every(r => r.status !== 'pending');
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
