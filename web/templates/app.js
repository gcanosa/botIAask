let logSources = {};
let activeLogChannel = null;
let currentStatsSource = null;
let activityChart = null;
let lastIsAdmin = false;

// Routing logic
function showPanel(panelId) {
    // Update links
    document.querySelectorAll('.nav-link').forEach(link => {
        link.classList.toggle('active', link.getAttribute('href') === `#${panelId}`);
    });

    // Update panels
    document.querySelectorAll('.panel').forEach(panel => {
        panel.classList.toggle('active', panel.id === `panel-${panelId}`);
    });

    // Specific panel initializers
    if (panelId === 'bookmarks') fetchBookmarks(1);
    if (panelId === 'pastes') fetchApprovedPastes(1);
    if (panelId === 'admin') fetchUsers();
    if (panelId === 'dashboard') {
         if (!activityChart) initChart();
         fetchHistory(currentTimeframe);
    }
}

// Handle browser navigation
window.addEventListener('hashchange', () => {
    const hash = window.location.hash.substring(1) || 'dashboard';
    showPanel(hash);
});

// Initialization
document.addEventListener('DOMContentLoaded', () => {
    const initialHash = window.location.hash.substring(1) || 'dashboard';
    showPanel(initialHash);
    
    fetchStatus();
    setInterval(fetchStatus, 30000); // 30s status refresh
    
    // Start background tasks
    fetchFinance();
    setInterval(fetchFinance, 60000); // 1m finance refresh
    
    // Poll for pending/approved pastes every 5s if admin and on pastes panel
    setInterval(() => {
        if (lastIsAdmin && document.getElementById('panel-pastes').classList.contains('active')) {
            fetchPendingPastes();
            fetchApprovedPastes(pastePage);
        }
    }, 5000);
});

async function fetchStatus() {
    try {
        const res = await fetch('/api/status');
        if (!res.ok) throw new Error('Status fetch failed');
        const data = await res.json();
        
        document.getElementById('uptime').textContent = data.uptime;
        document.getElementById('server').textContent = (data.nickname || 'Bot') + ' @ ' + data.server;
        document.getElementById('ai_requests').textContent = data.ai_requests || 0;
        document.getElementById('ai_model').textContent = data.ai_model || 'Unknown';
        
        // Features
        updateNewsStatus(data.rss_enabled, data.is_admin);
        updateStatsStatus(data.stats_enabled, data.is_admin);
        
        // State updates
        updateAdminView(data.is_admin);
        updateIRCAuthStatus(data.irc_authenticated);
        lastIsAdmin = data.is_admin;
        
        const statusText = document.getElementById('status-text');
        const statusBadge = document.getElementById('status-badge');
        statusText.textContent = 'Operational';
        statusBadge.classList.add('badge-online');

        if (data.needs_password_change && data.is_admin) {
            showForcePassword();
        }

        const channelsContainer = document.getElementById('channels');
        channelsContainer.innerHTML = '';
        if (data.channels) {
            data.channels.forEach(ch => {
                const btn = document.createElement('button');
                btn.className = 'btn btn-ghost mono';
                btn.textContent = ch;
                btn.onclick = () => openLogs(ch);
                channelsContainer.appendChild(btn);
            });
        }

        if (data.admin_nicknames) {
            updateAdminsUI(data.admin_nicknames, data.channel_admins);
        }
    } catch (e) {
        console.error("Status check failed", e);
        document.getElementById('status-text').textContent = 'Disconnected';
        document.getElementById('status-badge').classList.remove('badge-online');
    }
}

function updateAdminView(isAdmin) {
    const adminNav = document.getElementById('admin-nav');
    const adminBadge = document.getElementById('admin-badge');
    const loginBtn = document.getElementById('login-btn');
    const logoutBtn = document.getElementById('logout-btn');
    const pendingSec = document.getElementById('pending-pastes-section');

    if (isAdmin) {
        adminNav.classList.remove('hidden');
        adminBadge.classList.remove('hidden');
        loginBtn.classList.add('hidden');
        logoutBtn.classList.remove('hidden');
        if (pendingSec) {
            pendingSec.classList.remove('hidden');
            fetchPendingPastes();
        }
    } else {
        adminNav.classList.add('hidden');
        adminBadge.classList.add('hidden');
        loginBtn.classList.remove('hidden');
        logoutBtn.classList.add('hidden');
        if (pendingSec) pendingSec.classList.add('hidden');
    }
}

function updateIRCAuthStatus(authenticated) {
    const badge = document.getElementById('irc-auth-badge');
    const text = document.getElementById('irc-auth-text');
    if (authenticated) {
        badge.classList.remove('hidden');
        text.textContent = 'Identified';
    } else {
        badge.classList.add('hidden');
    }
}

// LOGS SYSTEM
function openLogs(channel) {
    const container = document.getElementById('log-container');
    container.classList.remove('hidden');

    if (logSources[channel]) {
        switchLogTab(channel);
        return;
    }

    // Create Tab
    const tabsContainer = document.getElementById('log-tabs');
    const tab = document.createElement('button');
    tab.id = `tab-${channel.replace(/#/g, '')}`;
    tab.className = 'btn btn-ghost active mono';
    tab.style.padding = '0.25rem 0.75rem';
    tab.innerHTML = `<span>${channel}</span> <small onclick="event.stopPropagation(); closeLogTab('${channel}')" style="margin-left: 0.5rem; color: var(--error);">×</small>`;
    tab.onclick = () => switchLogTab(channel);
    tabsContainer.appendChild(tab);

    // Create Output
    const wrapper = document.getElementById('log-outputs-wrapper');
    const output = document.createElement('div');
    output.id = `output-${channel.replace(/#/g, '')}`;
    output.className = 'log-view-pane hidden';
    output.innerHTML = '<div class="text-slate-500 italic">Connecting to stream...</div>';
    wrapper.appendChild(output);

    // Start Stream
    const source = new EventSource(`/api/logs/stream?channel=${encodeURIComponent(channel)}`);
    logSources[channel] = source;

    source.onmessage = (e) => {
        const line = document.createElement('div');
        line.className = 'log-entry';
        
        let text = e.data;
        if (text.includes('[MESSAGE]')) line.style.color = 'var(--text-main)';
        else if (text.includes('[JOIN]')) line.style.color = 'var(--success)';
        else if (text.includes('[PART]')) line.style.color = 'var(--error)';
        else if (text.includes('[ACTION]')) line.style.color = 'var(--accent)';
        
        line.textContent = text;
        output.appendChild(line);
        output.scrollTop = output.scrollHeight;
    };

    switchLogTab(channel);
}

function switchLogTab(channel) {
    activeLogChannel = channel;
    document.querySelectorAll('#log-tabs button').forEach(b => {
        b.classList.toggle('active', b.id === `tab-${channel.replace(/#/g, '')}`);
    });
    document.querySelectorAll('#log-outputs-wrapper > div').forEach(d => {
        d.classList.toggle('hidden', d.id !== `output-${channel.replace(/#/g, '')}`);
    });
    document.getElementById('current-channel-title').textContent = `Logs: ${channel}`;
}

function closeLogTab(channel) {
    if (logSources[channel]) { logSources[channel].close(); delete logSources[channel]; }
    document.getElementById(`tab-${channel.replace(/#/g, '')}`)?.remove();
    document.getElementById(`output-${channel.replace(/#/g, '')}`)?.remove();
    
    const remaining = Object.keys(logSources);
    if (remaining.length > 0) switchLogTab(remaining[0]);
    else closeAllLogs();
}

function closeAllLogs() {
    Object.keys(logSources).forEach(closeLogTab);
    document.getElementById('log-container').classList.add('hidden');
}

function updateNewsStatus(enabled, isAdmin) {
    const indicator = document.getElementById('news-indicator');
    if (enabled) {
        indicator.classList.add('pulse');
        indicator.style.background = 'var(--primary)';
    } else {
        indicator.classList.remove('pulse');
        indicator.style.background = 'var(--text-muted)';
    }
}

async function toggleNews() {
    try {
        const res = await fetch('/api/rss/toggle', { method: 'POST' });
        const data = await res.json();
        updateNewsStatus(data.rss_enabled);
    } catch (e) {
        console.error("Failed to toggle news", e);
    }
}

// FINANCE
async function fetchFinance() {
    try {
        const res = await fetch('/api/finance');
        const data = await res.json();
        
        const cryptoDiv = document.getElementById('crypto-prices');
        const cryptoUpdate = document.getElementById('crypto-last-update');
        cryptoDiv.innerHTML = '';
        
        if (data.crypto_last_update && data.crypto_last_update !== '0001-01-01T00:00:00Z') {
            cryptoUpdate.textContent = `Updated: ${new Date(data.crypto_last_update).toLocaleTimeString()}`;
        }
        
        if (data.crypto) {
            data.crypto.forEach(c => {
            const up = c.change_24h >= 0;
            cryptoDiv.innerHTML += `
                <div class="price-tag">
                    <span class="mono font-bold">${c.symbol.toUpperCase()}</span>
                    <div>
                        <span class="mono">$${c.price.toLocaleString()}</span>
                        <span class="mono ${up ? 'price-up' : 'price-down'}" style="font-size: 0.7rem; margin-left: 0.5rem;">${up ? '▲' : '▼'} ${Math.abs(c.change_24h).toFixed(1)}%</span>
                    </div>
                </div>
            `;
        });
        }

        const forexDiv = document.getElementById('forex-rates');
        const forexUpdate = document.getElementById('forex-last-update');
        forexDiv.innerHTML = '';
        
        if (data.forex_last_update) {
            forexUpdate.textContent = `Updated: ${new Date(data.forex_last_update).toLocaleTimeString()}`;
        }

        const labels = {
            'eur_usd': 'EUR/USD',
            'usd_ars': 'USD/ARS (Official)',
            'usd_ars_blue': 'USD/ARS (Blue)',
            'eur_ars': 'EUR/ARS'
        };
        Object.entries(data.forex).forEach(([k, v]) => {
            forexDiv.innerHTML += `
                <div class="price-tag">
                    <span class="mono font-bold">${labels[k] || k.toUpperCase()}</span>
                    <span class="mono">$${v.toLocaleString(undefined, {minimumFractionDigits: 2, maximumFractionDigits: 2})}</span>
                </div>
            `;
        });
    } catch (e) { console.error("Finance fetch error", e); }
}

let currentTimeframe = '1h';
function updateStatsStatus(enabled, isAdmin) {
    const btn = document.getElementById('stats-toggle');
    const indicator = document.getElementById('stats-indicator');
    if (enabled) {
        indicator.classList.add('pulse');
        indicator.style.background = 'var(--primary)';
        if (isAdmin) startStatsStream();
    } else {
        indicator.classList.remove('pulse');
        indicator.style.background = 'var(--text-muted)';
        stopStatsStream();
    }
}

async function toggleStats() {
    const res = await fetch('/api/stats/toggle', { method: 'POST' });
    const data = await res.json();
    updateStatsStatus(data.stats_enabled, true);
}

function initChart() {
    const ctx = document.getElementById('activityChart').getContext('2d');
    activityChart = new Chart(ctx, {
        type: 'line',
        data: {
            labels: [],
            datasets: [
                { label: 'Msgs', data: [], borderColor: '#38bdf8', backgroundColor: 'rgba(56, 189, 248, 0.1)', fill: true, tension: 0.4 },
                { label: 'AI', data: [], borderColor: '#c084fc', backgroundColor: 'rgba(192, 132, 252, 0.1)', fill: true, tension: 0.4 }
            ]
        },
        options: {
            responsive: true, maintainAspectRatio: false,
            scales: {
                y: { grid: { color: 'rgba(255,255,255,0.05)' }, ticks: { color: '#94a3b8' } },
                x: { grid: { display: false }, ticks: { color: '#94a3b8' } }
            },
            plugins: { legend: { display: false } }
        }
    });
}

async function fetchHistory(tf) {
    currentTimeframe = tf;
    const res = await fetch(`/api/stats/history?timeframe=${tf}`);
    const data = await res.json();
    
    const format = tf === '1h' ? { hour: '2-digit', minute:'2-digit'} : { month: 'short', day: 'numeric', hour: '2-digit', minute:'2-digit'};
    activityChart.data.labels = data.map(e => new Date(e.timestamp).toLocaleTimeString([], format));
    activityChart.data.datasets[0].data = data.map(e => e.messages);
    activityChart.data.datasets[1].data = data.map(e => e.ai_requests);
    activityChart.update();
}

function startStatsStream() {
    if (currentStatsSource) return;
    currentStatsSource = new EventSource('/api/stats/stream');
    currentStatsSource.onmessage = (e) => {
        const entry = JSON.parse(e.data);
        const format = currentTimeframe === '1h' ? { hour: '2-digit', minute:'2-digit'} : { month: 'short', day: 'numeric', hour: '2-digit', minute:'2-digit'};
        
        activityChart.data.labels.push(new Date(entry.timestamp).toLocaleTimeString([], format));
        activityChart.data.datasets[0].data.push(entry.messages);
        activityChart.data.datasets[1].data.push(entry.ai_requests);
        
        const limit = currentTimeframe === '1h' ? 30 : 100;
        if (activityChart.data.labels.length > limit) {
             activityChart.data.labels.shift();
             activityChart.data.datasets.forEach(d => d.data.shift());
        }
        activityChart.update('none');
    };
}

function stopStatsStream() { if (currentStatsSource) { currentStatsSource.close(); currentStatsSource = null; } }

// BOOKMARKS
let bookmarkPage = 1;
let searchTimeout = null;

async function fetchBookmarks(page) {
    if (page < 1) page = 1;
    bookmarkPage = page;
    
    const searchInput = document.getElementById('bookmark-search');
    const q = searchInput ? searchInput.value : '';
    
    try {
        const res = await fetch(`/api/bookmarks?page=${page}&q=${encodeURIComponent(q)}`);
        const data = await res.json();
        
        document.getElementById('bookmarks-count').textContent = `${data.total_count} items`;
        const list = document.getElementById('bookmarks-list');
        
        if (!data.bookmarks || data.bookmarks.length === 0) {
            list.innerHTML = '<tr><td colspan="3" style="padding: 2rem; text-align: center; color: var(--text-muted);">No bookmarks found matching your search.</td></tr>';
            return;
        }

        list.innerHTML = data.bookmarks.map(b => `
            <tr style="border-bottom: 1px solid var(--glass-border);">
                <td style="padding: 1rem; color: var(--primary); font-weight: 700;">${b.nickname}</td>
                <td style="padding: 1rem;">
                    <a href="${b.url}" target="_blank" style="color: var(--text-main); text-decoration: none; border-bottom: 1px dashed var(--glass-border);">
                        ${b.url.length > 50 ? b.url.substring(0, 47) + '...' : b.url}
                    </a>
                </td>
                <td style="padding: 1rem; text-align: right; color: var(--text-muted);">${new Date(b.timestamp).toLocaleDateString()}</td>
            </tr>
        `).join('');
        
        // Update pagination buttons state
        document.getElementById('prev-page').disabled = page <= 1;
        document.getElementById('next-page').disabled = page >= data.total_pages;
        
    } catch (e) {
        console.error("Failed to fetch bookmarks", e);
    }
}

function debounceSearch() {
    clearTimeout(searchTimeout);
    searchTimeout = setTimeout(() => fetchBookmarks(1), 300);
}

function changeBookmarkPage(d) {
    const target = bookmarkPage + d;
    if (target < 1) return;
    fetchBookmarks(target);
}

// PASTES
let pastePage = 1;
async function fetchApprovedPastes(page) {
    pastePage = page;
    const res = await fetch(`/api/pastes?page=${page}&t=${Date.now()}`);
    const data = await res.json();
    document.getElementById('pastes-count').textContent = `${data.total_count} items`;
    document.getElementById('pastes-list').innerHTML = data.pastes.map(p => {
        const tid = p.ticket_id || p.TicketID;
        const title = p.title || p.Title;
        const user = p.username || p.Username;
        const date = p.approved_at && p.approved_at.Valid ? new Date(p.approved_at.Time).toLocaleDateString() : 'N/A';
        return `
            <tr style="border-bottom: 1px solid var(--glass-border);">
                <td style="padding: 1rem;" class="font-bold">${tid}</td>
                <td style="padding: 1rem;">
                    <div>${title}</div>
                    <div style="font-size: 0.65rem; color: var(--text-muted); margin-top: 0.25rem;">Published: ${date}</div>
                </td>
                <td style="padding: 1rem; color: var(--primary);">${user}</td>
                <td style="padding: 1rem; text-align: right;">
                    <a href="/p/${tid}" target="_blank" class="text-primary">View</a>
                    ${lastIsAdmin ? `<button onclick="deletePaste('${tid}')" style="color: var(--error); background: none; border: none; cursor: pointer; margin-left: 1rem;">Delete</button>` : ''}
                </td>
            </tr>
        `;
    }).join('');
}

function changePastePage(d) {
    if (pastePage + d < 1) return;
    fetchApprovedPastes(pastePage + d);
}

async function fetchPendingPastes() {
    const res = await fetch(`/api/pastes/pending?t=${Date.now()}`);
    const data = await res.json();
    document.getElementById('pending-pastes-list').innerHTML = data.map(p => {
        const tid = p.ticket_id || p.TicketID;
        const title = p.title || p.Title;
        const user = p.username || p.Username;
        const date = p.created_at ? new Date(p.created_at).toLocaleDateString() : 'N/A';
        return `
            <tr style="border-bottom: 1px solid var(--glass-border); background: var(--accent-glow);">
                <td style="padding: 1rem;">${tid}</td>
                <td style="padding: 1rem;">
                    <div>${title}</div>
                    <div style="font-size: 0.65rem; color: var(--text-muted); margin-top: 0.25rem;">Submitted: ${date}</div>
                </td>
                <td style="padding: 1rem;">${user}</td>
                <td style="padding: 1rem; text-align: right;">
                    <button onclick="approvePaste('${tid}')" class="text-success">Approve</button>
                    <button onclick="rejectPaste('${tid}')" class="text-error" style="margin-left: 1rem;">Reject</button>
                </td>
            </tr>
        `;
    }).join('') || '<tr><td colspan="4" style="padding: 1rem; text-align: center; color: var(--text-muted);">No pending approvals</td></tr>';
}

// AUTH & ADMIN
function showLogin() { 
    document.getElementById('modal-overlay').classList.remove('hidden');
    document.getElementById('login-modal').classList.remove('hidden'); 
}
function hideLogin() { 
    document.getElementById('modal-overlay').classList.add('hidden');
    document.getElementById('login-modal').classList.add('hidden'); 
}

async function login() {
    const u = document.getElementById('login-username').value;
    const p = document.getElementById('login-password').value;
    const res = await fetch('/api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: u, password: p })
    });
    if (res.ok) { hideLogin(); fetchStatus(); }
    else document.getElementById('login-error').classList.remove('hidden');
}

async function logout() { await fetch('/api/logout', { method: 'POST' }); location.reload(); }

async function fetchUsers() {
    const res = await fetch('/api/users');
    const users = await res.json();
    document.getElementById('users-list').innerHTML = users.map(u => `
        <tr style="border-bottom: 1px solid var(--glass-border);">
            <td style="padding: 1rem;">${u.id}</td>
            <td style="padding: 1rem;" class="font-bold">${u.username}</td>
            <td style="padding: 1rem;"><span class="badge badge-admin">${u.role}</span></td>
            <td style="padding: 1rem; text-align: right;">
                <button onclick="deleteUser(${u.id})" style="color: var(--error);">Delete</button>
            </td>
        </tr>
    `).join('');
}

function updateAdminsUI(nicks, chans) {
    const globalList = document.getElementById('admins-list');
    if (!globalList) return;
    globalList.innerHTML = nicks.map(nick => 
        `<span class="badge badge-admin mono" style="font-size: 0.65rem;">${nick}</span>`
    ).join('');
}

function showForcePassword() {
    document.getElementById('modal-overlay').classList.remove('hidden');
    document.getElementById('force-password-modal').classList.remove('hidden');
}

async function updatePassword() {
    const p = document.getElementById('new-password-input').value;
    const res = await fetch('/api/users/password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: p })
    });
    if (res.ok) {
        document.getElementById('modal-overlay').classList.add('hidden');
        document.getElementById('force-password-modal').classList.add('hidden');
        fetchStatus();
    } else {
        document.getElementById('force-password-error').textContent = "Update failed";
        document.getElementById('force-password-error').classList.remove('hidden');
    }
}

// Global functions exposed to window
window.showPanel = showPanel;
window.showLogin = showLogin;
window.hideLogin = hideLogin;
window.login = login;
window.logout = logout;
window.openLogs = openLogs;
window.closeLogTab = closeLogTab;
window.closeAllLogs = closeAllLogs;
window.switchLogTab = switchLogTab;
window.toggleStats = toggleStats;
window.toggleNews = toggleNews;
window.updatePassword = updatePassword;
async function deletePaste(id) {
    if(!confirm('Are you sure you want to delete this paste?')) return;
    try {
        const res = await fetch(`/api/pastes/delete?ticketID=${id}&t=${Date.now()}`, { method: 'DELETE' });
        if (res.ok) {
            await fetchApprovedPastes(pastePage);
        } else {
            alert('Delete failed');
        }
    } catch (e) { console.error(e); }
}

async function approvePaste(id) {
    try {
        // Optimistic UI update: find and remove the row immediately
        const btn = document.querySelector(`button[onclick*="approvePaste('${id}')"]`);
        if (btn) {
            const row = btn.closest('tr');
            if (row) row.style.opacity = '0.5'; // Visual cue that action is in progress
        }

        const res = await fetch(`/api/pastes/approve?ticketID=${id}&t=${Date.now()}`, { method: 'POST' });
        if (res.ok) {
            // Remove from DOM immediately
            if (btn && btn.closest('tr')) btn.closest('tr').remove();
            
            // Re-fetch data to sync with server
            await fetchPendingPastes();
            await fetchApprovedPastes(1);
        } else {
            const err = await res.text();
            alert('Approval failed: ' + err);
            fetchPendingPastes(); // Re-sync on failure
        }
    } catch (e) { console.error(e); }
}

async function rejectPaste(id) {
    try {
        const btn = document.querySelector(`button[onclick*="rejectPaste('${id}')"]`);
        if (btn) {
            const row = btn.closest('tr');
            if (row) row.style.opacity = '0.5';
        }

        const res = await fetch(`/api/pastes/reject?ticketID=${id}&t=${Date.now()}`, { method: 'POST' });
        if (res.ok) {
            if (btn && btn.closest('tr')) btn.closest('tr').remove();
            await fetchPendingPastes();
        } else {
            const err = await res.text();
            alert('Rejection failed: ' + err);
            fetchPendingPastes();
        }
    } catch (e) { console.error(e); }
}

async function deleteUser(id) {
    if(!confirm('Delete user?')) return;
    const res = await fetch(`/api/users?id=${id}&t=${Date.now()}`, { method: 'DELETE' });
    if (res.ok) fetchUsers();
}

async function addUser() {
    const u = document.getElementById('add-username').value;
    const p = document.getElementById('add-password').value;
    const res = await fetch('/api/users', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: u, password: p })
    });
    if (res.ok) { window.hideAddUser(); fetchUsers(); }
}

// Global functions exposed to window
window.showPanel = showPanel;
window.showLogin = showLogin;
window.hideLogin = hideLogin;
window.login = login;
window.logout = logout;
window.openLogs = openLogs;
window.closeLogTab = closeLogTab;
window.closeAllLogs = closeAllLogs;
window.switchLogTab = switchLogTab;
window.toggleStats = toggleStats;
window.toggleNews = toggleNews;
window.updatePassword = updatePassword;
window.changeTimeframe = (tf) => {
    document.querySelectorAll('#timeframe-selector button').forEach(b => b.classList.toggle('active', b.dataset.time === tf));
    fetchHistory(tf);
};
window.changeBookmarkPage = changeBookmarkPage;
window.changePastePage = changePastePage;
window.debounceSearch = debounceSearch;
window.deletePaste = deletePaste;
window.approvePaste = approvePaste;
window.rejectPaste = rejectPaste;
window.deleteUser = deleteUser;
window.showAddUser = () => { document.getElementById('modal-overlay').classList.remove('hidden'); document.getElementById('adduser-modal').classList.remove('hidden'); };
window.hideAddUser = () => { document.getElementById('modal-overlay').classList.add('hidden'); document.getElementById('adduser-modal').classList.add('hidden'); };
window.addUser = addUser;
