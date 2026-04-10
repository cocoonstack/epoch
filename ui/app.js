// Epoch — Snapshot Registry UI

const $ = (sel, el = document) => el.querySelector(sel);
const $$ = (sel, el = document) => [...el.querySelectorAll(sel)];

// --- API ---
async function api(path) {
  const res = await fetch('/api' + path);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.json();
}

// --- Formatting ---
function humanSize(bytes) {
  if (!bytes || bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  return (bytes / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0) + ' ' + units[i];
}

function timeAgo(dateStr) {
  if (!dateStr) return '\u2014';
  const d = new Date(dateStr);
  if (isNaN(d.getTime())) return '\u2014';
  const diff = Date.now() - d.getTime();
  if (diff < 0) return 'just now';
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return mins + 'm ago';
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return hrs + 'h ago';
  const days = Math.floor(hrs / 24);
  if (days < 30) return days + 'd ago';
  return d.toLocaleDateString('zh-CN');
}

function shortDigest(digest) {
  return digest ? digest.substring(0, 12) : '\u2014';
}

function shortMediaType(mt) {
  if (!mt) return '\u2014';
  return mt.replace('application/vnd.cocoon.', '').replace('application/', '');
}

// platformLabel turns an OCI image-index platform descriptor into a compact
// human label like "linux/amd64" or "linux/arm64/v8". Empty platform falls
// back to an em dash so the cell still aligns.
function platformLabel(p) {
  if (!p) return '\u2014';
  const parts = [p.os, p.architecture].filter(Boolean);
  if (p.variant) parts.push(p.variant);
  return parts.length ? parts.join('/') : '\u2014';
}

// layerTitle returns the human filename of a manifest layer descriptor. OCI
// 1.1 stores it under annotations["org.opencontainers.image.title"] (set by
// epoch's snapshot Pusher); the legacy v1 epoch schema used a top-level
// `filename` field. Read both so old and new manifests render correctly.
function layerTitle(l) {
  if (!l) return '\u2014';
  if (l.filename) return l.filename;
  if (l.annotations && l.annotations['org.opencontainers.image.title']) {
    return l.annotations['org.opencontainers.image.title'];
  }
  return '\u2014';
}

// badgeKinds mirrors the strings emitted by manifest.Kind.String() on the
// server. Anything outside this set falls back to "unknown" so the function
// is safe to call with raw API values without HTML-escaping.
const badgeKinds = new Set(['snapshot', 'cloud-image', 'container-image', 'image-index', 'unknown']);

function badgeHTML(type) {
  const safe = badgeKinds.has(type) ? type : 'unknown';
  return '<span class="badge badge-' + safe + '">' + safe + '</span>';
}

function enc(s) {
  return encodeURIComponent(s);
}

// --- View renderer with entrance animation ---
function render(el, html) {
  el.innerHTML = '<div class="view-enter">' + html + '</div>';
}

// --- Router ---
function route() {
  const hash = location.hash.slice(1) || '/';
  const app = $('#app');

  $$('nav a').forEach(a => {
    const href = a.getAttribute('href');
    const target = href.slice(1); // remove '#'
    a.classList.toggle('active',
      target === '/' ? hash === '/' : hash.startsWith(target));
  });

  if (hash === '/') renderDashboard(app);
  else if (hash === '/tokens') renderTokens(app);
  else if (hash === '/repositories') renderRepositories(app);
  else if (/^\/repositories\/[^/]+\/[^/]+$/.test(hash)) {
    const parts = hash.split('/');
    renderTagDetail(app, decodeURIComponent(parts[2]), decodeURIComponent(parts[3]));
  } else if (/^\/repositories\/[^/]+$/.test(hash)) {
    renderTags(app, decodeURIComponent(hash.split('/')[2]));
  } else {
    renderDashboard(app);
  }
}

// --- Views ---

async function renderDashboard(el) {
  el.innerHTML = '<div class="loading"><span class="spinner"></span>Loading</div>';
  try {
    const [stats, repos] = await Promise.all([api('/stats'), api('/repositories')]);
    render(el, `
      <h1 class="page-title">Dashboard</h1>
      <div class="stats">
        <div class="stat-card">
          <div class="stat-label">Repositories</div>
          <div class="stat-value accent">${stats.repositoryCount}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Tags</div>
          <div class="stat-value">${stats.tagCount}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Blobs</div>
          <div class="stat-value">${stats.blobCount}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Total Size</div>
          <div class="stat-value">${humanSize(stats.totalSize)}</div>
        </div>
      </div>
      <div class="section-title">Repositories</div>
      ${repos.length === 0 ? emptyState('No repositories yet. Push a snapshot with <code>epoch push</code>.') : repoTable(repos)}
    `);
    bindRepoClicks();
  } catch (e) {
    render(el, emptyState('Failed to connect \u2014 ' + e.message));
  }
}

async function renderRepositories(el) {
  el.innerHTML = '<div class="loading"><span class="spinner"></span>Loading</div>';
  try {
    const repos = await api('/repositories');
    render(el, `
      <div class="page-header">
        <h1 class="page-title" style="margin-bottom:0">Repositories</h1>
        <button class="btn btn-primary" onclick="doSync()">Sync Catalog</button>
      </div>
      ${repos.length === 0 ? emptyState('No repositories found.') : repoTable(repos)}
    `);
    bindRepoClicks();
  } catch (e) {
    render(el, emptyState('Failed to load \u2014 ' + e.message));
  }
}

async function renderTags(el, name) {
  el.innerHTML = '<div class="loading"><span class="spinner"></span>Loading</div>';
  try {
    const tags = await api('/repositories/' + enc(name) + '/tags');
    render(el, `
      <div class="breadcrumb">
        <a href="#/repositories">Repositories</a>
        <span class="sep"></span>
        <span>${name}</span>
      </div>
      <h1 class="page-title">${name} <small>${tags.length} tag${tags.length !== 1 ? 's' : ''}</small></h1>
      <div class="pull-cmd"><span class="prompt">$ </span><span class="cmd">epoch pull</span> <span class="arg">${name}</span></div>
      ${tags.length === 0 ? emptyState('No tags found.') : tagTable(tags, name)}
    `);
    bindTagClicks(name);
  } catch (e) {
    render(el, emptyState('Failed to load \u2014 ' + e.message));
  }
}

async function renderTagDetail(el, name, tag) {
  el.innerHTML = '<div class="loading"><span class="spinner"></span>Loading</div>';
  try {
    const data = await api('/repositories/' + enc(name) + '/tags/' + enc(tag));
    const m = data.manifest || {};
    const layers = m.layers || [];
    const baseImages = m.baseImages || [];
    // For OCI image indexes (multi-arch), `manifests[]` lists the per-platform
    // child manifests. Each entry carries platform.{architecture,os,variant}
    // and is content-addressed via its sha256 digest.
    const platforms = m.manifests || [];
    // platformSizes is the server-materialized standalone (config + sum(layers))
    // size of each child manifest, keyed by digest. Inlined only for image-index
    // tags. The OCI spec's manifests[i].size is just the descriptor doc size
    // (~600 B), which is useless to a user — this map carries the real number.
    // Index by digest so we can join against platforms[] without an O(n²) loop.
    const platformSizeByDigest = {};
    for (const ps of (data.platformSizes || [])) {
      platformSizeByDigest[ps.digest] = ps;
    }
    // snapshotConfig is the decoded contents of the snapshot config blob,
    // inlined by the server only when kind == snapshot. Has snapshotId,
    // image, cpu, memory, storage, nics. Other kinds get an empty object so
    // the conditional render below cleanly skips the snapshot-only cards.
    const cfg = data.snapshotConfig || null;
    const maxSize = Math.max(...layers.map(l => l.size || 0), 1);

    render(el, `
      <div class="breadcrumb">
        <a href="#/repositories">Repositories</a>
        <span class="sep"></span>
        <a href="#/repositories/${enc(name)}">${name}</a>
        <span class="sep"></span>
        <span>${tag}</span>
      </div>
      <div class="page-header">
        <h1 class="page-title" style="margin-bottom:0">${name}:${tag}</h1>
        <button class="btn btn-danger" onclick="deleteTag('${name}','${tag}')">Delete Tag</button>
      </div>
      <div class="pull-cmd"><span class="prompt">$ </span><span class="cmd">epoch pull</span> <span class="arg">${name}:${tag}</span></div>

      <div class="detail-grid">
        ${cfg ? `
          <div class="detail-card">
            <div class="detail-label">Snapshot ID</div>
            <div class="detail-value mono">${cfg.snapshotId || '\u2014'}</div>
          </div>
          <div class="detail-card span-2">
            <div class="detail-label">Source Image</div>
            <div class="detail-value mono">${cfg.image || '\u2014'}</div>
          </div>
          <div class="detail-card">
            <div class="detail-label">Resources</div>
            <div class="detail-value">${cfg.cpu || 0} vCPU &middot; ${humanSize(cfg.memory || 0)} RAM &middot; ${humanSize(cfg.storage || 0)} Disk</div>
          </div>
        ` : ''}
        <div class="detail-card">
          <div class="detail-label">Total Size</div>
          <div class="detail-value">${humanSize(data.totalSize)}</div>
        </div>
        <div class="detail-card">
          <div class="detail-label">Pushed</div>
          <div class="detail-value">${data.pushedAt ? new Date(data.pushedAt).toLocaleString('zh-CN') : '\u2014'}</div>
        </div>
      </div>

      ${layers.length > 0 ? `
        <div class="section-title">Layers (${layers.length})</div>
        <div class="table-wrap" style="margin-bottom:28px">
          <table>
            <thead><tr>
              <th>Filename</th>
              <th>Type</th>
              <th>Size</th>
              <th style="width:100px"></th>
              <th>Digest</th>
            </tr></thead>
            <tbody>
              ${layers.map(l => `
                <tr>
                  <td class="cell-name">${layerTitle(l)}</td>
                  <td class="cell-mono">${shortMediaType(l.mediaType)}</td>
                  <td class="cell-size">${humanSize(l.size)}</td>
                  <td><div class="layer-bar"><div class="layer-bar-fill" style="width:${((l.size || 0) / maxSize * 100).toFixed(1)}%"></div></div></td>
                  <td class="cell-mono">${shortDigest(l.digest)}</td>
                </tr>
              `).join('')}
            </tbody>
          </table>
        </div>
      ` : ''}

      ${baseImages.length > 0 ? `
        <div class="section-title">Base Images (${baseImages.length})</div>
        <div class="table-wrap">
          <table>
            <thead><tr><th>Filename</th><th>Type</th><th>Size</th><th>Digest</th></tr></thead>
            <tbody>
              ${baseImages.map(l => `
                <tr>
                  <td class="cell-name">${layerTitle(l)}</td>
                  <td class="cell-mono">${shortMediaType(l.mediaType)}</td>
                  <td class="cell-size">${humanSize(l.size)}</td>
                  <td class="cell-mono">${shortDigest(l.digest)}</td>
                </tr>
              `).join('')}
            </tbody>
          </table>
        </div>
      ` : ''}

      ${platforms.length > 0 ? `
        <div class="section-title">Platforms (${platforms.length})</div>
        <div class="table-wrap">
          <table>
            <thead><tr>
              <th>Platform</th>
              <th>Type</th>
              <th>Size</th>
              <th>Layers</th>
              <th>Digest</th>
            </tr></thead>
            <tbody>
              ${platforms.map(p => {
                const ps = platformSizeByDigest[p.digest];
                return `
                <tr>
                  <td class="cell-name">${platformLabel(p.platform)}</td>
                  <td class="cell-mono">${shortMediaType(p.mediaType)}</td>
                  <td class="cell-size">${ps ? humanSize(ps.size) : '\u2014'}</td>
                  <td class="cell-size">${ps ? ps.layerCount : '\u2014'}</td>
                  <td class="cell-mono">${shortDigest(p.digest)}</td>
                </tr>
                `;
              }).join('')}
            </tbody>
          </table>
        </div>
      ` : ''}
    `);
  } catch (e) {
    render(el, emptyState('Failed to load \u2014 ' + e.message));
  }
}

// --- Table builders ---

function repoTable(repos) {
  return `
    <div class="table-wrap">
      <table>
        <thead><tr><th>Repository</th><th>Type</th><th>Tags</th><th>Total Size</th><th>Updated</th></tr></thead>
        <tbody>
          ${repos.map(r => `
            <tr class="clickable-row" data-repo="${r.name}">
              <td class="cell-name">${r.name}</td>
              <td>${badgeHTML(r.kind || 'unknown')}</td>
              <td><span class="cell-count">${r.tagCount}</span></td>
              <td class="cell-size">${humanSize(r.totalSize)}</td>
              <td class="cell-time">${timeAgo(r.updatedAt)}</td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function tagTable(tags, name) {
  return `
    <div class="table-wrap">
      <table>
        <thead><tr><th>Tag</th><th>Size</th><th>Layers</th><th>Digest</th><th>Pushed</th></tr></thead>
        <tbody>
          ${tags.map(t => `
            <tr class="clickable-row" data-tag="${t.name}">
              <td class="cell-name">${t.name}</td>
              <td class="cell-size">${humanSize(t.totalSize)}</td>
              <td><span class="cell-count">${t.layerCount}</span></td>
              <td class="cell-mono">${shortDigest(t.digest)}</td>
              <td class="cell-time">${timeAgo(t.pushedAt)}</td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function emptyState(msg) {
  return '<div class="empty"><div class="empty-text">' + msg + '</div></div>';
}

// --- Event binding ---

function bindRepoClicks() {
  $$('.clickable-row[data-repo]').forEach(row => {
    row.addEventListener('click', () => {
      location.hash = '/repositories/' + enc(row.dataset.repo);
    });
  });
}

function bindTagClicks(name) {
  $$('.clickable-row[data-tag]').forEach(row => {
    row.addEventListener('click', () => {
      location.hash = '/repositories/' + enc(name) + '/' + enc(row.dataset.tag);
    });
  });
}

// --- Actions ---

async function doSync() {
  const btn = $('button.btn-primary');
  if (!btn) return;
  const original = btn.textContent;
  btn.disabled = true;
  btn.textContent = 'Syncing\u2026';
  try {
    await fetch('/api/catalog/sync', { method: 'POST' });
    route();
  } catch (e) {
    btn.textContent = 'Failed';
    setTimeout(() => { btn.textContent = original; btn.disabled = false; }, 2000);
  }
}

async function deleteTag(name, tag) {
  if (!confirm('Delete ' + name + ':' + tag + '?\nThis removes the manifest from the registry.')) return;
  try {
    const res = await fetch('/api/repositories/' + enc(name) + '/tags/' + enc(tag), { method: 'DELETE' });
    if (!res.ok) throw new Error(res.statusText);
    location.hash = '/repositories/' + enc(name);
  } catch (e) {
    alert('Delete failed: ' + e.message);
  }
}

// --- Tokens View ---

// Hashed at rest, so a freshly-created plaintext token is shown exactly once
// in a reveal banner. Stash it on the window so renderTokens can pick it up
// after the post-create re-render, then clear it so a later navigation back
// to the page does not leak it.
async function renderTokens(el) {
  el.innerHTML = '<div class="loading"><span class="spinner"></span>Loading</div>';
  try {
    const tokens = await api('/tokens');
    const reveal = window.__newToken;
    window.__newToken = null;
    render(el, `
      <div class="page-header">
        <h1 class="page-title" style="margin-bottom:0">Registry Tokens</h1>
        <button class="btn btn-primary" onclick="showCreateToken()">Create Token</button>
      </div>
      ${reveal ? `
      <div style="margin-bottom:16px;padding:12px 16px;background:#1f2335;border:1px solid #e0af68;border-radius:8px">
        <div style="font-size:12px;color:#e0af68;margin-bottom:6px">New token <strong>${reveal.name}</strong> — copy it now, it will not be shown again</div>
        <code style="display:block;font-size:12px;color:#7aa2f7;user-select:all;word-break:break-all;cursor:pointer;padding:8px;background:#1a1b26;border-radius:4px" onclick="navigator.clipboard.writeText(this.textContent)" title="Click to copy">${reveal.token}</code>
      </div>` : ''}
      <div id="create-form" style="display:none;margin-bottom:16px">
        <div style="display:flex;gap:8px;align-items:center">
          <input id="token-name" type="text" placeholder="Token name (e.g. vk-cocoon-prod)" style="padding:6px 10px;border:1px solid #333;border-radius:4px;background:#1a1b26;color:#c0caf5;flex:1;font-size:13px">
          <button class="btn btn-primary" onclick="doCreateToken()">Create</button>
          <button class="btn" onclick="document.getElementById('create-form').style.display='none'">Cancel</button>
        </div>
      </div>
      ${tokens.length === 0
        ? emptyState('No tokens created. Use the button above to create one.')
        : `<table class="table"><thead><tr>
            <th>Name</th><th>Created By</th><th>Created</th><th>Last Used</th><th></th>
          </tr></thead><tbody>
          ${tokens.map(t => `<tr>
            <td><strong>${t.name}</strong></td>
            <td>${t.createdBy || '\u2014'}</td>
            <td>${timeAgo(t.createdAt)}</td>
            <td>${t.lastUsed ? timeAgo(t.lastUsed) : 'never'}</td>
            <td><button class="btn btn-danger" onclick="doDeleteToken(${t.id},'${t.name}')" style="font-size:11px;padding:2px 8px">Delete</button></td>
          </tr>`).join('')}
          </tbody></table>`
      }
      <div style="margin-top:24px;padding:16px;background:#1a1b26;border-radius:8px;border:1px solid #292e42">
        <div style="font-size:12px;color:#565f89;margin-bottom:8px">Usage</div>
        <code style="color:#7aa2f7;font-size:12px">curl -H "Authorization: Bearer &lt;token&gt;" http://epoch:8080/v2/_catalog</code>
      </div>
    `);
  } catch (e) {
    render(el, emptyState('Failed to load tokens \u2014 ' + e.message));
  }
}

function showCreateToken() {
  document.getElementById('create-form').style.display = 'block';
  document.getElementById('token-name').focus();
}

async function doCreateToken() {
  const name = document.getElementById('token-name').value.trim();
  if (!name) { alert('Name required'); return; }
  try {
    const res = await fetch('/api/tokens', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({name})
    });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || res.statusText);
    window.__newToken = {name: data.name, token: data.token};
    renderTokens($('#app'));
  } catch (e) {
    alert('Create failed: ' + e.message);
  }
}

async function doDeleteToken(id, name) {
  if (!confirm('Delete token "' + name + '"? Clients using it will lose access.')) return;
  try {
    const res = await fetch('/api/tokens/' + id, {method: 'DELETE'});
    if (!res.ok) throw new Error(res.statusText);
    renderTokens($('#app'));
  } catch (e) {
    alert('Delete failed: ' + e.message);
  }
}

// --- Init ---
window.addEventListener('hashchange', route);
window.addEventListener('DOMContentLoaded', route);
