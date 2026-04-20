let logSources = {};
let activeLogChannel = null;
let currentStatsSource = null;
let activityChart = null;
let cryptoMarketChart = null;
let cryptoChartRange = '1w';
let forexHistoryChart = null;
let forexChartRange = '1w';
const cryptoChartColors = ['#38bdf8', '#c084fc', '#22c55e', '#f472b6', '#fbbf24', '#a78bfa', '#2dd4bf', '#fb7185', '#94a3b8', '#e879f9'];
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
    if (panelId === 'news') fetchNews(1);
    if (panelId === 'admin') fetchUsers();
    if (panelId === 'dashboard') {
         if (!activityChart) initChart();
         fetchHistory(currentTimeframe);
    }
    if (panelId === 'finance') {
        fetchCryptoChart();
        fetchForexChart();
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
    
    // Poll for pending/approved pastes and news every 5s if admin and on respective panel
    setInterval(() => {
        if (lastIsAdmin && document.getElementById('panel-pastes').classList.contains('active')) {
            fetchPendingPastes();
            fetchApprovedPastes(pastePage);
        }
        if (document.getElementById('panel-news').classList.contains('active')) {
            fetchNews(newsPage);
        }
    }, 5000);

    document.querySelectorAll('.crypto-range-btn:not(.forex-range-btn)').forEach((btn) => {
        btn.addEventListener('click', () => {
            const r = btn.getAttribute('data-range');
            document.querySelectorAll('.crypto-range-btn:not(.forex-range-btn)').forEach((b) => b.classList.remove('active'));
            btn.classList.add('active');
            fetchCryptoChart(r);
        });
    });
    document.querySelectorAll('.forex-range-btn').forEach((btn) => {
        btn.addEventListener('click', () => {
            const r = btn.getAttribute('data-range');
            document.querySelectorAll('.forex-range-btn').forEach((b) => b.classList.remove('active'));
            btn.classList.add('active');
            fetchForexChart(r);
        });
    });
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
        if (pendingSec) pendingSec.classList.remove('hidden');
        document.getElementById('admin-fetch-btn')?.classList.remove('hidden');
        document.getElementById('admin-rss-settings-btn')?.classList.remove('hidden');
        document.getElementById('news-admin-header')?.classList.remove('hidden');
        document.getElementById('bookmarks-admin-header')?.classList.remove('hidden');
    } else {
        adminNav.classList.add('hidden');
        adminBadge.classList.add('hidden');
        loginBtn.classList.remove('hidden');
        logoutBtn.classList.add('hidden');
        if (pendingSec) pendingSec.classList.add('hidden');
        document.getElementById('admin-fetch-btn')?.classList.add('hidden');
        document.getElementById('admin-rss-settings-btn')?.classList.add('hidden');
        document.getElementById('news-admin-header')?.classList.add('hidden');
        document.getElementById('bookmarks-admin-header')?.classList.add('hidden');
        document.getElementById('rss-admin-settings')?.classList.add('hidden');
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
function financeEscapeHtml(str) {
    if (str == null) return '';
    return String(str)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

function cryptoLucideIcon(symbol) {
    const u = String(symbol || '').toUpperCase();
    const map = {
        BTC: 'bitcoin',
        ETH: 'circle-dollar-sign',
        USDT: 'circle-dollar-sign',
        USDC: 'circle-dollar-sign',
        BNB: 'coins',
        SOL: 'coins',
        XRP: 'coins',
        ADA: 'coins',
        DOGE: 'coins',
        DOT: 'coins',
        AVAX: 'coins',
    };
    return map[u] || 'coins';
}

function forexMeta(key) {
    const meta = {
        eur_usd: { label: 'EUR / USD', flags: ['eu', 'us'] },
        usd_ars: { label: 'USD / ARS', flags: ['us', 'ar'], hint: 'Official' },
        usd_ars_blue: { label: 'USD / ARS', flags: ['us', 'ar'], hint: 'Blue' },
        eur_ars: { label: 'EUR / ARS', flags: ['eu', 'ar'] },
    };
    if (meta[key]) return meta[key];
    return {
        label: key.replace(/_/g, ' / ').toUpperCase(),
        flags: [],
    };
}

function refreshLucideFinanceIcons() {
    try {
        if (typeof lucide !== 'undefined' && typeof lucide.createIcons === 'function') {
            lucide.createIcons();
        }
    } catch (e) {
        console.warn('Lucide createIcons failed', e);
    }
}

function normalizeForexObject(raw) {
    if (raw == null || typeof raw !== 'object' || Array.isArray(raw)) {
        return {};
    }
    const out = {};
    Object.entries(raw).forEach(([k, v]) => {
        const n = typeof v === 'number' ? v : Number(v);
        if (Number.isFinite(n)) {
            out[k] = n;
        }
    });
    return out;
}

async function fetchFinance() {
    let data;
    try {
        const res = await fetch('/api/finance');
        data = await res.json();
    } catch (e) {
        console.error('Finance fetch error', e);
        return;
    }

    const cryptoDiv = document.getElementById('crypto-prices');
    const cryptoUpdate = document.getElementById('crypto-last-update');
    const forexDiv = document.getElementById('forex-rates');
    const forexUpdate = document.getElementById('forex-last-update');

    try {
        if (cryptoUpdate) {
            if (data.crypto_last_update && data.crypto_last_update !== '0001-01-01T00:00:00Z') {
                cryptoUpdate.textContent = `Updated: ${new Date(data.crypto_last_update).toLocaleTimeString()}`;
            } else {
                cryptoUpdate.textContent = '';
            }
        }

        if (cryptoDiv) {
            const cryptoList = Array.isArray(data.crypto) ? data.crypto : [];
            if (cryptoList.length) {
                cryptoDiv.innerHTML = cryptoList.map((c) => {
                    const up = Number(c.change_24h) >= 0;
                    const icon = cryptoLucideIcon(c.symbol);
                    const sym = financeEscapeHtml(c.symbol);
                    const name = financeEscapeHtml(c.name || '');
                    const price = Number(c.price);
                    const chg = Number(c.change_24h);
                    const priceStr = Number.isFinite(price) ? price.toLocaleString() : '—';
                    const chgStr = Number.isFinite(chg) ? Math.abs(chg).toFixed(1) : '0.0';
                    return `
                <div class="finance-row finance-row-crypto">
                    <div class="finance-row-left">
                        <span class="finance-icon-slot" aria-hidden="true"><i data-lucide="${icon}" class="finance-lucide"></i></span>
                        <div class="finance-labels">
                            <span class="finance-symbol mono">${sym.toUpperCase()}</span>
                            <span class="finance-name">${name}</span>
                        </div>
                    </div>
                    <div class="finance-row-right">
                        <span class="finance-price mono">$${priceStr}</span>
                        <span class="finance-change-pill mono ${up ? 'price-up' : 'price-down'}">${up ? '▲' : '▼'} ${chgStr}%</span>
                    </div>
                </div>`;
                }).join('');
            } else {
                cryptoDiv.innerHTML = '';
            }
        }
    } catch (e) {
        console.error('Finance crypto render error', e);
    }

    try {
        if (forexUpdate) {
            if (data.forex_last_update) {
                forexUpdate.textContent = `Updated: ${new Date(data.forex_last_update).toLocaleTimeString()}`;
            } else {
                forexUpdate.textContent = '';
            }
        }

        if (forexDiv) {
            const fx = normalizeForexObject(data.forex);
            const entries = Object.entries(fx);
            if (entries.length) {
                forexDiv.innerHTML = entries.map(([k, v]) => {
                    const m = forexMeta(k);
                    const flagsHtml = m.flags.length
                        ? m.flags.map((f) => `<span class="fi fi-${f} fis finance-flag" aria-hidden="true"></span>`).join('')
                        : '';
                    const hint = m.hint ? `<span class="finance-hint">${financeEscapeHtml(m.hint)}</span>` : '';
                    return `
                <div class="finance-row finance-row-forex">
                    <div class="finance-row-left">
                        <div class="finance-flags">${flagsHtml}</div>
                        <div class="finance-labels">
                            <span class="finance-forex-pair mono">${financeEscapeHtml(m.label)}</span>
                            ${hint}
                        </div>
                    </div>
                    <span class="finance-price mono">$${v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}</span>
                </div>`;
                }).join('');
            } else {
                forexDiv.innerHTML = '<div class="finance-row finance-row-forex"><span class="finance-price mono">Rates unavailable (retrying...)</span></div>';
            }
        }
    } catch (e) {
        console.error('Finance forex render error', e);
    }

    refreshLucideFinanceIcons();

    const financePanel = document.getElementById('panel-finance');
    if (financePanel && financePanel.classList.contains('active')) {
        fetchCryptoChart();
        fetchForexChart();
    }
}

function formatCryptoChartLabel(ms) {
    const d = new Date(ms);
    if (cryptoChartRange === '6h' || cryptoChartRange === '1d') {
        return d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    }
    return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
}

function formatForexChartLabel(ms) {
    const d = new Date(ms);
    if (forexChartRange === '6h' || forexChartRange === '1d') {
        return d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    }
    return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
}

async function fetchCryptoChart(range) {
    if (range) {
        cryptoChartRange = range;
    }
    const subEl = document.getElementById('crypto-chart-subtitle');
    const canvas = document.getElementById('cryptoMarketChart');
    if (!canvas) return;

    try {
        const res = await fetch(`/api/finance/crypto-chart?range=${encodeURIComponent(cryptoChartRange)}`);
        if (!res.ok) {
            if (subEl) {
                subEl.textContent = 'Chart unavailable (upstream rate limit or error). Try again in a minute.';
            }
            if (cryptoMarketChart) {
                cryptoMarketChart.data.labels = [];
                cryptoMarketChart.data.datasets = [];
                cryptoMarketChart.update();
            }
            return;
        }
        const data = await res.json();
        if (subEl) {
            subEl.textContent = data.subtitle || '';
        }

        const labels = (data.labels || []).map(formatCryptoChartLabel);
        const datasets = (data.series || []).map((s, i) => ({
            label: s.symbol,
            data: (s.values || []).map((v) => (v == null ? null : v)),
            borderColor: cryptoChartColors[i % cryptoChartColors.length],
            backgroundColor: 'transparent',
            tension: 0.3,
            pointRadius: 0,
            borderWidth: 1.5,
            spanGaps: true,
        }));

        if (!cryptoMarketChart) {
            cryptoMarketChart = new Chart(canvas.getContext('2d'), {
                type: 'line',
                data: { labels, datasets },
                options: {
                    responsive: true,
                    maintainAspectRatio: false,
                    interaction: { mode: 'index', intersect: false },
                    scales: {
                        x: {
                            ticks: { color: '#94a3b8', maxRotation: 45, maxTicksLimit: 10 },
                            grid: { color: 'rgba(255,255,255,0.05)' },
                        },
                        y: {
                            ticks: { color: '#94a3b8', callback: (v) => `${v}%` },
                            grid: { color: 'rgba(255,255,255,0.05)' },
                        },
                    },
                    plugins: {
                        legend: {
                            display: true,
                            position: 'bottom',
                            labels: { color: '#94a3b8', boxWidth: 10, padding: 8, font: { size: 10 } },
                        },
                        tooltip: {
                            callbacks: {
                                label(ctx) {
                                    const v = ctx.parsed.y;
                                    if (v == null) return `${ctx.dataset.label}: —`;
                                    return `${ctx.dataset.label}: ${v.toFixed(2)}%`;
                                },
                            },
                        },
                    },
                },
            });
        } else {
            cryptoMarketChart.data.labels = labels;
            cryptoMarketChart.data.datasets = datasets;
            cryptoMarketChart.update();
        }
    } catch (e) {
        console.error('Crypto chart fetch error', e);
    }
}

async function fetchForexChart(range) {
    if (range) {
        forexChartRange = range;
    }
    const subEl = document.getElementById('forex-chart-subtitle');
    const canvas = document.getElementById('forexHistoryChart');
    if (!canvas) return;

    try {
        const res = await fetch(`/api/finance/forex-chart?range=${encodeURIComponent(forexChartRange)}`);
        if (!res.ok) {
            if (subEl) {
                subEl.textContent = 'Chart unavailable.';
            }
            if (forexHistoryChart) {
                forexHistoryChart.data.labels = [];
                forexHistoryChart.data.datasets = [];
                forexHistoryChart.update();
            }
            return;
        }
        const data = await res.json();
        if (subEl) {
            subEl.textContent = data.subtitle || '';
        }

        const labels = (data.labels || []).map(formatForexChartLabel);
        const datasets = (data.series || []).map((s, i) => {
            const m = forexMeta(s.symbol);
            const pairLabel = m.hint ? `${m.label} (${m.hint})` : (m.label || s.symbol);
            return {
            label: pairLabel,
            data: (s.values || []).map((v) => (v == null ? null : v)),
            borderColor: cryptoChartColors[i % cryptoChartColors.length],
            backgroundColor: 'transparent',
            tension: 0.3,
            pointRadius: 0,
            borderWidth: 1.5,
            spanGaps: true,
        };
        });

        if (!forexHistoryChart) {
            forexHistoryChart = new Chart(canvas.getContext('2d'), {
                type: 'line',
                data: { labels, datasets },
                options: {
                    responsive: true,
                    maintainAspectRatio: false,
                    interaction: { mode: 'index', intersect: false },
                    scales: {
                        x: {
                            ticks: { color: '#94a3b8', maxRotation: 45, maxTicksLimit: 10 },
                            grid: { color: 'rgba(255,255,255,0.05)' },
                        },
                        y: {
                            ticks: {
                                color: '#94a3b8',
                                callback: (v) => (typeof v === 'number' ? v.toLocaleString(undefined, { maximumFractionDigits: 4 }) : v),
                            },
                            grid: { color: 'rgba(255,255,255,0.05)' },
                        },
                    },
                    plugins: {
                        legend: {
                            display: true,
                            position: 'bottom',
                            labels: { color: '#94a3b8', boxWidth: 10, padding: 8, font: { size: 10 } },
                        },
                        tooltip: {
                            callbacks: {
                                label(ctx) {
                                    const v = ctx.parsed.y;
                                    if (v == null) return `${ctx.dataset.label}: —`;
                                    return `${ctx.dataset.label}: ${v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 4 })}`;
                                },
                            },
                        },
                    },
                },
            });
        } else {
            forexHistoryChart.data.labels = labels;
            forexHistoryChart.data.datasets = datasets;
            forexHistoryChart.update();
        }
    } catch (e) {
        console.error('Forex chart fetch error', e);
    }
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
    if (!document.getElementById('activityChart')) return;
    currentTimeframe = tf;
    if (!activityChart) initChart();
    if (!activityChart) return;

    try {
        const res = await fetch(`/api/stats/history?timeframe=${tf}`);
        if (!res.ok) {
            console.error('Stats history HTTP', res.status);
            return;
        }
        const raw = await res.json();
        const data = Array.isArray(raw) ? raw : [];

        const format = tf === '1h' ? { hour: '2-digit', minute:'2-digit'} : { month: 'short', day: 'numeric', hour: '2-digit', minute:'2-digit'};
        activityChart.data.labels = data.map(e => new Date(e.timestamp).toLocaleTimeString([], format));
        activityChart.data.datasets[0].data = data.map(e => e.messages);
        activityChart.data.datasets[1].data = data.map(e => e.ai_requests);
        activityChart._seenTs = new Set(data.map(e => new Date(e.timestamp).getTime()));
        activityChart.update();
    } catch (err) {
        console.error('Stats history fetch error', err);
    }
}

function startStatsStream() {
    if (currentStatsSource) return;
    currentStatsSource = new EventSource('/api/stats/stream');
    currentStatsSource.onmessage = (e) => {
        if (!activityChart) return;
        let entry;
        try {
            entry = JSON.parse(e.data);
        } catch {
            return;
        }
        const ts = new Date(entry.timestamp).getTime();
        if (Number.isNaN(ts)) return;
        if (activityChart._seenTs && activityChart._seenTs.has(ts)) return;
        if (!activityChart._seenTs) activityChart._seenTs = new Set();
        activityChart._seenTs.add(ts);

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
            const span = lastIsAdmin ? 4 : 3;
            list.innerHTML = `<tr><td colspan="${span}" style="padding: 2rem; text-align: center; color: var(--text-muted);">No bookmarks found matching your search.</td></tr>`;
            return;
        }

        list.innerHTML = data.bookmarks.map(b => `
            <tr style="border-bottom: 1px solid var(--glass-border);">
                <td style="padding: 1rem; color: var(--primary); font-weight: 700;">${b.nickname}</td>
                <td style="padding: 1rem;">
                    <div style="display: flex; align-items: center; gap: 0.35rem; flex-wrap: wrap;">
                        <a href="${b.url}" target="_blank" style="color: var(--text-main); text-decoration: none; border-bottom: 1px dashed var(--glass-border);">
                            ${b.url.length > 50 ? b.url.substring(0, 47) + '...' : b.url}
                        </a>
                        <button type="button" onclick="copyBookmarkUrl(decodeURIComponent('${encodeURIComponent(b.url)}'))" title="Copy URL" class="btn btn-ghost" style="padding: 0.2rem 0.35rem; line-height: 0; flex-shrink: 0; opacity: 0.75;" aria-label="Copy URL">
                            <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                        </button>
                    </div>
                </td>
                <td style="padding: 1rem; text-align: right; color: var(--text-muted);">${new Date(b.timestamp).toLocaleDateString()}</td>
                ${lastIsAdmin ? `
                <td style="padding: 1rem; text-align: right;">
                    <button onclick="deleteBookmark(${b.id})" style="color: var(--error); background: none; border: none; cursor: pointer; font-size: 0.8rem;">Delete</button>
                </td>` : '<td class="hidden"></td>'}
            </tr>
        `).join('');
        
        // Update pagination buttons state
        document.getElementById('prev-page').disabled = page <= 1;
        document.getElementById('next-page').disabled = page >= data.total_pages;
        
    } catch (e) {
        console.error("Failed to fetch bookmarks", e);
    }
}

async function deleteBookmark(id) {
    if (!confirm('Delete this bookmark?')) return;
    const res = await fetch(`/api/bookmarks?id=${encodeURIComponent(id)}`, { method: 'DELETE' });
    if (res.ok) fetchBookmarks(bookmarkPage);
}

async function copyBookmarkUrl(url) {
    try {
        await navigator.clipboard.writeText(url);
    } catch (e) {
        console.error('Copy failed', e);
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

// NEWS
let newsPage = 1;
let newsSearchTimeout = null;

async function fetchNews(page) {
    if (page < 1) page = 1;
    newsPage = page;
    const q = document.getElementById('news-search').value;
    try {
        const res = await fetch(`/api/rss/news?page=${page}&q=${encodeURIComponent(q)}`);
        const data = await res.json();
        
        const fetchInfo = document.getElementById('news-fetch-info');
        if (data.last_fetch && data.last_fetch !== '0001-01-01T00:00:00Z') {
            fetchInfo.textContent = `Last fetched: ${new Date(data.last_fetch).toLocaleString()}`;
        }

        const list = document.getElementById('news-list');
        if (!data.news || data.news.length === 0) {
            list.innerHTML = '<tr><td colspan="3" style="padding: 2rem; text-align: center; color: var(--text-muted);">No news found.</td></tr>';
            return;
        }

        list.innerHTML = data.news.map(n => `
            <tr style="border-bottom: 1px solid var(--glass-border);">
                <td style="padding: 1rem;">
                    <div style="font-weight: 600; color: var(--text-main);">${n.Title}</div>
                    <div style="font-size: 0.7rem; margin-top: 0.25rem;">
                        <a href="${n.Link}" target="_blank" style="color: var(--primary); text-decoration: none;">View Source</a>
                        ${n.ShortLink ? `<span style="color: var(--text-muted); margin: 0 0.5rem;">|</span><a href="${n.ShortLink}" target="_blank" style="color: var(--accent); text-decoration: none;">Short Link</a>` : ''}
                    </div>
                </td>
                <td style="padding: 1rem; text-align: right; color: var(--text-muted); white-space: nowrap;">
                    ${new Date(n.PubDate).toLocaleDateString()} ${new Date(n.PubDate).toLocaleTimeString([], {hour: '2-digit', minute:'2-digit'})}
                </td>
                ${lastIsAdmin ? `
                <td style="padding: 1rem; text-align: right;">
                    <button onclick="deleteNews('${n.GUID}')" style="color: var(--error); background: none; border: none; cursor: pointer; font-size: 0.8rem;">Delete</button>
                </td>` : '<td class="hidden"></td>'}
            </tr>
        `).join('');

        document.getElementById('news-prev-page').disabled = page <= 1;
        document.getElementById('news-next-page').disabled = page >= data.total_pages;

    } catch (e) { console.error("News fetch failed", e); }
}

async function forceFetchNews() {
    const btn = document.getElementById('admin-fetch-btn');
    btn.disabled = true;
    btn.textContent = 'Fetching...';
    try {
        await fetch('/api/rss/fetch', { method: 'POST' });
        setTimeout(() => {
            btn.disabled = false;
            btn.textContent = 'Fetch Now';
            fetchNews(1);
        }, 2000);
    } catch (e) {
        btn.disabled = false;
        btn.textContent = 'Fetch Now';
    }
}

async function deleteNews(guid) {
    if (!confirm('Delete this news entry?')) return;
    const res = await fetch(`/api/rss/news?guid=${encodeURIComponent(guid)}`, { method: 'DELETE' });
    if (res.ok) fetchNews(newsPage);
}

function debounceNewsSearch() {
    clearTimeout(newsSearchTimeout);
    newsSearchTimeout = setTimeout(() => fetchNews(1), 300);
}

function changeNewsPage(d) {
    if (newsPage + d < 1) return;
    fetchNews(newsPage + d);
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
window.forceFetchNews = forceFetchNews;
window.deleteNews = deleteNews;
window.debounceNewsSearch = debounceNewsSearch;
window.changeNewsPage = changeNewsPage;
window.changeTimeframe = (tf) => {
    document.querySelectorAll('#timeframe-selector button').forEach(b => b.classList.toggle('active', b.dataset.time === tf));
    fetchHistory(tf);
};
window.changeBookmarkPage = changeBookmarkPage;
window.deleteBookmark = deleteBookmark;
window.copyBookmarkUrl = copyBookmarkUrl;
window.changePastePage = changePastePage;
window.debounceSearch = debounceSearch;
window.deletePaste = deletePaste;
window.approvePaste = approvePaste;
window.rejectPaste = rejectPaste;
window.deleteUser = deleteUser;
window.showAddUser = () => { document.getElementById('modal-overlay').classList.remove('hidden'); document.getElementById('adduser-modal').classList.remove('hidden'); };
window.hideAddUser = () => { document.getElementById('modal-overlay').classList.add('hidden'); document.getElementById('adduser-modal').classList.add('hidden'); };
window.addUser = addUser;

// RSS SETTINGS
async function toggleRSSSettings() {
    const card = document.getElementById('rss-admin-settings');
    const isHidden = card.classList.contains('hidden');
    
    if (isHidden) {
        card.classList.remove('hidden');
        await fetchRSSSettings();
    } else {
        card.classList.add('hidden');
    }
}

async function fetchRSSSettings() {
    try {
        const res = await fetch('/api/rss/settings');
        const data = await res.json();
        
        document.getElementById('rss-interval').value = data.interval_minutes;
        document.getElementById('rss-retention').value = data.retention_count;
        document.getElementById('rss-urls').value = (data.feed_urls || []).join('\n');
    } catch (e) { console.error("Failed to fetch RSS settings", e); }
}

async function saveRSSSettings() {
    const status = document.getElementById('rss-settings-status');
    status.textContent = 'Saving...';
    status.style.color = 'var(--text-muted)';

    const interval = parseInt(document.getElementById('rss-interval').value);
    const retention = parseInt(document.getElementById('rss-retention').value);
    const urls = document.getElementById('rss-urls').value.split('\n').map(u => u.trim()).filter(u => u !== '');

    try {
        const res = await fetch('/api/rss/settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                interval_minutes: interval,
                retention_count: retention,
                feed_urls: urls
            })
        });

        if (res.ok) {
            status.textContent = '✓ Settings saved and applied';
            status.style.color = 'var(--success)';
            setTimeout(() => { status.textContent = ''; }, 3000);
            fetchNews(1); // Refresh list in case retention reduced
        } else {
            status.textContent = '✕ Save failed';
            status.style.color = 'var(--error)';
        }
    } catch (e) {
        status.textContent = '✕ Error: ' + e.message;
        status.style.color = 'var(--error)';
    }
}

window.toggleRSSSettings = toggleRSSSettings;
window.saveRSSSettings = saveRSSSettings;
