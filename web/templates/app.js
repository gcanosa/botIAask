let logCatalog = null;
let logState = {
    channelLabel: null,
    joined: false,
    selectedDate: null,
    eventSource: null,
    stickToBottom: true,
    calendarMonth: null,
};
let logPanelListenersBound = false;
let currentStatsSource = null;
let activityChart = null;
let cryptoMarketChart = null;
let cryptoChartRange = '1w';
let forexChartRange = '1w';
const cryptoChartColors = ['#38bdf8', '#c084fc', '#22c55e', '#f472b6', '#fbbf24', '#a78bfa', '#2dd4bf', '#fb7185', '#94a3b8', '#e879f9'];
let lastIsAdmin = false;
let lastStaffAdmin = false;

const VALID_THEMES = ['dark', 'light', 'mono'];
const UI_THEME_STORAGE_KEY = 'botIAask_ui_theme';

// Stroke icons (24×24, width 2) — same as sidebar .nav-link svg; .row-action__icon → 20×20
const _icon24 = (paths) => `<svg class="row-action__icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths}</svg>`;
const rowIcons = {
    download: _icon24('<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line>'),
    view: _icon24('<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path><circle cx="12" cy="12" r="3"></circle>'),
    trash: _icon24('<polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path><line x1="10" y1="11" x2="10" y2="17"></line><line x1="14" y1="11" x2="14" y2="17"></line>'),
    archive: _icon24('<polyline points="21 8 21 16 3 16 3 8"></polyline><line x1="1" y1="8" x2="23" y2="8"></line><path d="M10 3h4a2 2 0 0 1 2 2v1H8V5a2 2 0 0 1 2-2z"></path>'),
    check: _icon24('<polyline points="20 6 9 17 4 12"></polyline>'),
    x: _icon24('<line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line>'),
    linkOut: _icon24('<path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path><polyline points="15 3 21 3 21 9"></polyline><line x1="10" y1="14" x2="21" y2="3"></line>'),
    copy: _icon24('<rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>'),
    /** Article / feed page (distinct from short URL link) */
    globe: _icon24('<circle cx="12" cy="12" r="10"></circle><line x1="2" y1="12" x2="22" y2="12"></line><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"></path>'),
    /** Shortened / companion URL */
    linkChain: _icon24('<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"></path><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"></path>'),
};

/** RSS source key from API → mark before title (extend when adding feeds) */
function newsSourceBadgeFor(source) {
    const s = source != null ? String(source) : '';
    if (s === 'hacker-news') {
        return '<span class="news-source-badge news-source-badge--hn" title="Hacker News" aria-label="Source: Hacker News"><span class="news-hn-y" aria-hidden="true">Y</span></span>';
    }
    return '';
}

function normalizeTheme(t, fallback = 'light') {
    return VALID_THEMES.includes(t) ? t : fallback;
}

function readGuestTheme() {
    try {
        return normalizeTheme(localStorage.getItem(UI_THEME_STORAGE_KEY), 'light');
    } catch {
        return 'light';
    }
}

function applyTheme(theme) {
    const t = normalizeTheme(theme, 'light');
    document.documentElement.setAttribute('data-theme', t);
    syncActivityChartColors();
}

function syncThemeSelectFromState(isAuthenticated, serverTheme) {
    const sel = document.getElementById('theme-select');
    if (!sel) return;
    sel.disabled = false;
    const t = isAuthenticated
        ? normalizeTheme(serverTheme, 'dark')
        : readGuestTheme();
    sel.value = t;
    applyTheme(t);
}

const MOBILE_SIDEBAR_MAX_WIDTH = 1024;

function isMobileNavLayout() {
    return window.innerWidth <= MOBILE_SIDEBAR_MAX_WIDTH;
}

function updateMobileSidebarAria() {
    const sidebar = document.getElementById('sidebar');
    const toggle = document.getElementById('sidebar-toggle');
    if (!sidebar) return;
    if (isMobileNavLayout()) {
        const open = sidebar.classList.contains('open');
        sidebar.setAttribute('aria-hidden', open ? 'false' : 'true');
        if (toggle) {
            toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
            toggle.setAttribute('aria-label', open ? 'Close menu' : 'Open menu');
        }
    } else {
        sidebar.removeAttribute('aria-hidden');
        if (toggle) {
            toggle.setAttribute('aria-expanded', 'false');
            toggle.setAttribute('aria-label', 'Open menu');
        }
    }
}

function openSidebar() {
    if (!isMobileNavLayout()) return;
    const sidebar = document.getElementById('sidebar');
    const backdrop = document.getElementById('sidebar-backdrop');
    if (!sidebar) return;
    sidebar.classList.add('open');
    backdrop?.classList.remove('hidden');
    backdrop?.setAttribute('aria-hidden', 'false');
    document.body.style.overflow = 'hidden';
    updateMobileSidebarAria();
}

function closeSidebar() {
    const sidebar = document.getElementById('sidebar');
    const backdrop = document.getElementById('sidebar-backdrop');
    sidebar?.classList.remove('open');
    backdrop?.classList.add('hidden');
    backdrop?.setAttribute('aria-hidden', 'true');
    document.body.style.overflow = '';
    updateMobileSidebarAria();
}

function toggleSidebar() {
    const sidebar = document.getElementById('sidebar');
    if (sidebar?.classList.contains('open')) {
        closeSidebar();
    } else {
        openSidebar();
    }
}

function closeSidebarIfMobile() {
    if (isMobileNavLayout()) closeSidebar();
}

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
    if (panelId === 'pastes') {
        fetchApprovedPastes(1);
        fetchPendingPastes();
    }
    if (panelId === 'uploads') {
        fetchUploadSettings();
        fetchPendingFiles();
        fetchApprovedFiles(1);
    }
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
    if (panelId === 'logs') {
        fetchLogCatalog();
        bindLogsPanelListeners();
    }

    closeSidebarIfMobile();
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

    document.getElementById('pending-approvals-goto-pastes')?.addEventListener('click', () => {
        window.location.hash = 'pastes';
        showPanel('pastes');
    });
    document.getElementById('pending-approvals-goto-uploads')?.addEventListener('click', () => {
        window.location.hash = 'uploads';
        showPanel('uploads');
    });

    document.getElementById('sidebar-backdrop')?.addEventListener('click', closeSidebar);
    window.addEventListener('resize', () => {
        if (!isMobileNavLayout()) closeSidebar();
        else updateMobileSidebarAria();
    });
    document.addEventListener('keydown', (e) => {
        if (e.key !== 'Escape') return;
        if (document.getElementById('sidebar')?.classList.contains('open')) {
            closeSidebar();
        }
    });
    updateMobileSidebarAria();

    fetchStatus();
    let statusIntervalId = null;
    const applyStatusPolling = () => {
        if (statusIntervalId) {
            clearInterval(statusIntervalId);
            statusIntervalId = null;
        }
        // Fast refresh while the tab is visible (IRC up/down, auth, pending counts).
        // Throttle in background to avoid idle tabs hammering the server.
        const ms = document.hidden ? 60000 : 5000;
        statusIntervalId = setInterval(fetchStatus, ms);
        if (!document.hidden) {
            fetchStatus();
        }
    };
    applyStatusPolling();
    document.addEventListener('visibilitychange', applyStatusPolling);
    
    // Start background tasks
    fetchFinance();
    setInterval(fetchFinance, 60000); // 1m finance refresh
    
    // Poll for pending/approved pastes and news every 5s if admin and on respective panel
    setInterval(() => {
        if (lastStaffAdmin && document.getElementById('panel-pastes').classList.contains('active')) {
            fetchPendingPastes();
            fetchApprovedPastes(pastePage);
        }
        if (lastStaffAdmin && document.getElementById('panel-uploads').classList.contains('active')) {
            fetchPendingFiles();
            fetchApprovedFiles(filePage);
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
        lastIsAdmin = data.is_admin;
        lastStaffAdmin = data.staff_admin != null ? !!data.staff_admin : !!data.is_admin;

        const appVersionEl = document.getElementById('app-version');
        if (appVersionEl && data.version) {
            appVersionEl.textContent = 'v' + String(data.version);
        }
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
        syncThemeSelectFromState(!!data.is_admin, data.ui_theme);

        const pendBanner = document.getElementById('pending-approvals-banner');
        const pendText = document.getElementById('pending-approvals-banner-text');
        const pendActions = document.getElementById('pending-approvals-actions');
        const btnPastes = document.getElementById('pending-approvals-goto-pastes');
        const btnUploads = document.getElementById('pending-approvals-goto-uploads');
        if (pendBanner && pendText) {
            const pp = data.pending_pastes ?? 0;
            const pu = data.pending_uploads ?? 0;
            const staff = data.staff_admin != null ? !!data.staff_admin : !!data.is_admin;
            if (staff && (pp > 0 || pu > 0)) {
                pendText.textContent = `Reminder: ${pp} pending paste(s) and ${pu} pending file upload(s) awaiting approval.`;
                pendBanner.classList.remove('hidden');
                if (pendActions) {
                    pendActions.classList.remove('hidden');
                    if (btnPastes) btnPastes.classList.toggle('hidden', pp <= 0);
                    if (btnUploads) btnUploads.classList.toggle('hidden', pu <= 0);
                }
            } else {
                pendBanner.classList.add('hidden');
                if (pendActions) pendActions.classList.add('hidden');
            }
        }
        
        const statusText = document.getElementById('status-text');
        const statusBadge = document.getElementById('status-badge');
        if (data.connected) {
            statusText.textContent = 'Operational';
            statusBadge.classList.add('badge-online');
        } else {
            statusText.textContent = 'IRC disconnected';
            statusBadge.classList.remove('badge-online');
        }

        if (data.needs_password_change && data.is_admin) {
            showForcePassword();
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
    const pendingFilesSec = document.getElementById('pending-files-section');
    const uploadSettingsCard = document.getElementById('upload-settings-card');

    if (isAdmin) {
        adminNav.classList.remove('hidden');
        adminBadge.classList.remove('hidden');
        loginBtn.classList.add('hidden');
        logoutBtn.classList.remove('hidden');
        if (pendingSec) pendingSec.classList.toggle('hidden', !lastStaffAdmin);
        if (pendingFilesSec) pendingFilesSec.classList.toggle('hidden', !lastStaffAdmin);
        if (uploadSettingsCard) uploadSettingsCard.classList.toggle('hidden', !lastStaffAdmin);
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
        if (pendingFilesSec) pendingFilesSec.classList.add('hidden');
        if (uploadSettingsCard) uploadSettingsCard.classList.add('hidden');
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

// LOGS SYSTEM (catalog, calendar, SSE / history)
function bindLogsPanelListeners() {
    if (logPanelListenersBound) return;
    logPanelListenersBound = true;

    document.getElementById('logs-channel-filter')?.addEventListener('input', () => renderLogChannelList());
    document.getElementById('logs-close-view')?.addEventListener('click', closeLogsView);

    const calToggle = document.getElementById('log-calendar-toggle');
    const calPop = document.getElementById('log-calendar-popover');
    calToggle?.addEventListener('click', (e) => {
        e.stopPropagation();
        calPop?.classList.toggle('hidden');
        const expanded = calPop && !calPop.classList.contains('hidden');
        calToggle.setAttribute('aria-expanded', expanded ? 'true' : 'false');
        if (expanded) renderLogCalendar();
    });
    document.getElementById('log-cal-prev')?.addEventListener('click', () => {
        if (!logState.calendarMonth) return;
        logState.calendarMonth = new Date(logState.calendarMonth.getFullYear(), logState.calendarMonth.getMonth() - 1, 1);
        renderLogCalendar();
    });
    document.getElementById('log-cal-next')?.addEventListener('click', () => {
        if (!logState.calendarMonth) return;
        logState.calendarMonth = new Date(logState.calendarMonth.getFullYear(), logState.calendarMonth.getMonth() + 1, 1);
        renderLogCalendar();
    });

    document.addEventListener('click', () => {
        calPop?.classList.add('hidden');
        calToggle?.setAttribute('aria-expanded', 'false');
    });
    calPop?.addEventListener('click', (e) => e.stopPropagation());

    document.getElementById('log-jump-latest')?.addEventListener('click', () => {
        logState.stickToBottom = true;
        scrollLogToEnd();
        document.getElementById('log-jump-latest')?.classList.add('hidden');
    });

    const scrollEl = document.getElementById('log-scroll');
    scrollEl?.addEventListener('scroll', () => {
        if (!scrollEl) return;
        const nearBottom = scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight < 80;
        logState.stickToBottom = nearBottom;
        document.getElementById('log-jump-latest')?.classList.toggle('hidden', nearBottom);
    });
}

function parseISODate(s) {
    const p = String(s).split('-').map(Number);
    if (p.length !== 3 || p.some((n) => !Number.isFinite(n))) return null;
    return new Date(p[0], p[1] - 1, p[2]);
}

function dateISOLocal(d) {
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${y}-${m}-${day}`;
}

async function fetchLogCatalog() {
    try {
        const res = await fetch('/api/logs/catalog');
        if (!res.ok) throw new Error('catalog failed');
        logCatalog = await res.json();
        const hint = document.getElementById('logs-retention-hint');
        if (hint && logCatalog.calendar) {
            const c = logCatalog.calendar;
            hint.textContent =
                c.rotation_days > 0
                    ? `Retention window: ${c.min_date} → ${c.max_date} (local)`
                    : `All log files on disk (local today ${c.server_local_today})`;
        }
        renderLogChannelList();
        if (logState.channelLabel) renderLogCalendar();
    } catch (e) {
        console.error('log catalog', e);
    }
}

function getLogChannelEntry(label) {
    if (!logCatalog?.channels) return null;
    return logCatalog.channels.find((c) => c.label === label) || null;
}

function renderLogChannelList() {
    const host = document.getElementById('logs-channel-groups');
    if (!host || !logCatalog?.channels) return;
    const q = (document.getElementById('logs-channel-filter')?.value || '').trim().toLowerCase();
    const joined = logCatalog.channels.filter((c) => c.joined && (!q || c.label.toLowerCase().includes(q)));
    const other = logCatalog.channels.filter((c) => !c.joined && (!q || c.label.toLowerCase().includes(q)));

    const mkRow = (c) => {
        const row = document.createElement('button');
        row.type = 'button';
        row.className = 'logs-channel-row mono' + (logState.channelLabel === c.label ? ' active' : '');
        row.innerHTML = `<span>${financeEscapeHtml(c.label)}</span><span class="logs-channel-meta">${
            c.joined
                ? '<span class="log-badge log-badge-live"><span class="dot pulse"></span>Live</span>'
                : '<span class="log-badge log-badge-disk">Disk</span>'
        }</span>`;
        row.addEventListener('click', () => selectLogChannel(c.label, c.joined));
        return row;
    };

    host.innerHTML = '';
    const addGroup = (title, items) => {
        if (!items.length) return;
        const wrap = document.createElement('div');
        const t = document.createElement('div');
        t.className = 'logs-group-title';
        t.textContent = title;
        wrap.appendChild(t);
        items.forEach((c) => wrap.appendChild(mkRow(c)));
        host.appendChild(wrap);
    };
    addGroup('Joined', joined);
    addGroup('Not joined / archive', other);
    if (!host.children.length) {
        host.innerHTML = '<p class="section-subtitle" style="margin:0;">No channels match.</p>';
    }
}

function selectLogChannel(label, joined) {
    logState.channelLabel = label;
    logState.joined = joined;
    if (logCatalog?.calendar?.server_local_today) {
        logState.selectedDate = logCatalog.calendar.server_local_today;
    }
    logState.calendarMonth = parseISODate(logState.selectedDate) || new Date();

    document.getElementById('logs-viewer-placeholder')?.classList.add('hidden');
    document.getElementById('logs-viewer')?.classList.remove('hidden');
    renderLogChannelList();
    updateLogToolbarBadges();
    updateLogDateLabel();
    reloadLogsForSelection();
}

function updateLogToolbarBadges() {
    const el = document.getElementById('logs-view-badges');
    if (!el) return;
    const today = logCatalog?.calendar?.server_local_today;
    const hist = logState.selectedDate && today && logState.selectedDate < today;
    const parts = [];
    if (logState.joined) {
        parts.push('<span class="log-badge log-badge-live"><span class="dot pulse"></span>Joined</span>');
    } else {
        parts.push('<span class="log-badge log-badge-disk">Not joined</span>');
    }
    if (hist) {
        parts.push('<span class="log-badge log-badge-disk">Historical</span>');
    } else if (logState.selectedDate === today) {
        parts.push('<span class="log-badge log-badge-live"><span class="dot pulse"></span>Today</span>');
    }
    el.innerHTML = parts.join('');
    const title = document.getElementById('logs-view-title');
    if (title) title.textContent = logState.channelLabel || '—';
}

function updateLogDateLabel() {
    const el = document.getElementById('log-selected-date-label');
    if (el) el.textContent = logState.selectedDate || '—';
}

function closeLogsView() {
    teardownLogStream();
    logState.channelLabel = null;
    logState.joined = false;
    logState.selectedDate = null;
    logState.stickToBottom = true;
    document.getElementById('logs-viewer')?.classList.add('hidden');
    document.getElementById('logs-viewer-placeholder')?.classList.remove('hidden');
    document.getElementById('log-scroll').innerHTML = '';
    renderLogChannelList();
}

function teardownLogStream() {
    if (logState.eventSource) {
        logState.eventSource.close();
        logState.eventSource = null;
    }
}

function appendLogLine(text) {
    const scrollEl = document.getElementById('log-scroll');
    if (!scrollEl) return;
    const line = document.createElement('div');
    line.className = 'log-line';
    if (text.includes('***') && text.includes('joined')) line.style.color = 'var(--success)';
    else if (text.includes('***') && (text.includes('left') || text.includes('quit'))) line.style.color = 'var(--error)';
    else if (text.includes('* ') && text.includes('***') === false) line.style.color = 'var(--accent)';
    line.textContent = text;
    scrollEl.appendChild(line);
    if (logState.stickToBottom) scrollLogToEnd();
}

function scrollLogToEnd() {
    const scrollEl = document.getElementById('log-scroll');
    if (!scrollEl) return;
    requestAnimationFrame(() => {
        scrollEl.scrollTop = scrollEl.scrollHeight;
        requestAnimationFrame(() => {
            scrollEl.scrollTop = scrollEl.scrollHeight;
        });
    });
}

function reloadLogsForSelection() {
    if (!logState.channelLabel || !logState.selectedDate) return;
    teardownLogStream();
    const scrollEl = document.getElementById('log-scroll');
    if (scrollEl) scrollEl.innerHTML = '';
    logState.stickToBottom = true;
    document.getElementById('log-jump-latest')?.classList.add('hidden');

    const today = logCatalog?.calendar?.server_local_today;
    updateLogToolbarBadges();

    if (logState.selectedDate === today) {
        const url = `/api/logs/stream?channel=${encodeURIComponent(logState.channelLabel)}&date=${encodeURIComponent(logState.selectedDate)}`;
        const source = new EventSource(url);
        logState.eventSource = source;
        source.onmessage = (e) => {
            appendLogLine(e.data);
        };
        source.onerror = () => {
            /* browser reconnects EventSource; avoid noisy logs */
        };
    } else {
        (async () => {
            try {
                const res = await fetch(
                    `/api/logs/history?channel=${encodeURIComponent(logState.channelLabel)}&date=${encodeURIComponent(logState.selectedDate)}`
                );
                if (!res.ok) {
                    appendLogLine('[ERROR] Failed to load history');
                    return;
                }
                const data = await res.json();
                const lines = Array.isArray(data.lines) ? data.lines : [];
                const frag = document.createDocumentFragment();
                lines.forEach((t) => {
                    const line = document.createElement('div');
                    line.className = 'log-line';
                    const text = String(t);
                    if (text.includes('***') && text.includes('joined')) line.style.color = 'var(--success)';
                    else if (text.includes('***') && (text.includes('left') || text.includes('quit'))) line.style.color = 'var(--error)';
                    else if (text.includes('* ') && !text.includes('***')) line.style.color = 'var(--accent)';
                    line.textContent = text;
                    frag.appendChild(line);
                });
                scrollEl?.appendChild(frag);
                if (data.truncated) {
                    const note = document.createElement('div');
                    note.className = 'log-line';
                    note.style.color = 'var(--text-muted)';
                    note.textContent = '[… truncated to last ' + lines.length + ' lines …]';
                    scrollEl?.appendChild(note);
                }
                scrollLogToEnd();
            } catch (e) {
                console.error(e);
                appendLogLine('[ERROR] History fetch failed');
            }
        })();
    }
}

function renderLogCalendar() {
    if (!logCatalog?.calendar || !logState.channelLabel) return;
    const { min_date, max_date, server_local_today } = logCatalog.calendar;
    const minD = parseISODate(min_date);
    const maxD = parseISODate(max_date);
    if (!logState.calendarMonth) logState.calendarMonth = parseISODate(server_local_today) || new Date();

    const monthLabel = document.getElementById('log-cal-month-label');
    if (monthLabel) {
        monthLabel.textContent = logState.calendarMonth.toLocaleString(undefined, { month: 'long', year: 'numeric' });
    }

    const foot = document.getElementById('log-cal-footnote');
    if (foot) {
        foot.textContent = `Selectable days: ${min_date} — ${max_date} (server local). Dots = log file on disk.`;
    }

    const prev = document.getElementById('log-cal-prev');
    const next = document.getElementById('log-cal-next');
    const firstOfMonth = new Date(logState.calendarMonth.getFullYear(), logState.calendarMonth.getMonth(), 1);
    const firstDow = firstOfMonth.getDay();
    const daysInMonth = new Date(logState.calendarMonth.getFullYear(), logState.calendarMonth.getMonth() + 1, 0).getDate();

    const prevLast = new Date(logState.calendarMonth.getFullYear(), logState.calendarMonth.getMonth(), 0);
    const nextFirst = new Date(logState.calendarMonth.getFullYear(), logState.calendarMonth.getMonth() + 1, 1);
    if (prev) {
        prev.disabled = !!(minD && prevLast < new Date(minD.getFullYear(), minD.getMonth(), minD.getDate()));
    }
    if (next) {
        next.disabled = !!(maxD && nextFirst > new Date(maxD.getFullYear(), maxD.getMonth(), maxD.getDate()));
    }

    const entry = getLogChannelEntry(logState.channelLabel);
    const hasLogSet = new Set(entry?.dates_with_logs || []);

    const grid = document.getElementById('log-cal-grid');
    if (!grid) return;
    grid.innerHTML = '';

    const pad = firstDow;
    for (let i = 0; i < pad; i++) {
        const x = document.createElement('div');
        x.className = 'log-cal-cell';
        x.style.visibility = 'hidden';
        grid.appendChild(x);
    }

    for (let day = 1; day <= daysInMonth; day++) {
        const d = new Date(logState.calendarMonth.getFullYear(), logState.calendarMonth.getMonth(), day);
        const iso = dateISOLocal(d);
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'log-cal-cell';
        btn.textContent = String(day);

        const inRange = (!minD || d >= new Date(minD.getFullYear(), minD.getMonth(), minD.getDate())) &&
            (!maxD || d <= new Date(maxD.getFullYear(), maxD.getMonth(), maxD.getDate()));
        if (!inRange) {
            btn.disabled = true;
        } else {
            btn.addEventListener('click', () => {
                logState.selectedDate = iso;
                logState.calendarMonth = d;
                document.getElementById('log-calendar-popover')?.classList.add('hidden');
                document.getElementById('log-calendar-toggle')?.setAttribute('aria-expanded', 'false');
                updateLogDateLabel();
                updateLogToolbarBadges();
                reloadLogsForSelection();
            });
        }

        if (iso === server_local_today) btn.classList.add('today');
        if (iso === logState.selectedDate) btn.classList.add('selected');
        if (hasLogSet.has(iso)) btn.classList.add('has-log');

        grid.appendChild(btn);
    }
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

const FOREX_PANEL_ORDER = ['eur_usd', 'usd_ars', 'usd_ars_blue', 'eur_ars'];

function sortForexEntries(entries) {
    const rank = new Map(FOREX_PANEL_ORDER.map((k, i) => [k, i]));
    return [...entries].sort(([a], [b]) => {
        const ra = rank.has(a) ? rank.get(a) : FOREX_PANEL_ORDER.length;
        const rb = rank.has(b) ? rank.get(b) : FOREX_PANEL_ORDER.length;
        if (ra !== rb) return ra - rb;
        return a.localeCompare(b);
    });
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
            const entries = sortForexEntries(Object.entries(fx));
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

function clearForexChartsGrid() {
    const grid = document.getElementById('forex-charts-grid');
    if (!grid) return;
    grid.querySelectorAll('canvas').forEach((c) => {
        const ch = typeof Chart !== 'undefined' && Chart.getChart ? Chart.getChart(c) : null;
        if (ch) ch.destroy();
    });
    grid.innerHTML = '';
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
    const grid = document.getElementById('forex-charts-grid');
    if (!grid) return;

    try {
        const res = await fetch(`/api/finance/forex-chart?range=${encodeURIComponent(forexChartRange)}`);
        if (!res.ok) {
            if (subEl) {
                subEl.textContent = 'Chart unavailable.';
            }
            clearForexChartsGrid();
            return;
        }
        const data = await res.json();
        if (subEl) {
            subEl.textContent = data.subtitle || '';
        }

        const labelsMs = Array.isArray(data.labels) ? data.labels.map((t) => Number(t)) : [];
        const series = Array.isArray(data.series) ? data.series : [];

        clearForexChartsGrid();

        if (!labelsMs.length || !series.length) {
            return;
        }

        const xMin = Math.min(...labelsMs);
        const xMax = Math.max(...labelsMs);

        series.forEach((s, i) => {
            const vals = s.values || [];
            const points = [];
            for (let j = 0; j < labelsMs.length && j < vals.length; j++) {
                const v = vals[j];
                if (v == null || typeof v !== 'number' || !Number.isFinite(v)) continue;
                points.push({ x: labelsMs[j], y: v });
            }
            if (points.length < 2) return;

            const m = forexMeta(s.symbol);
            const titleText = m.hint ? `${m.label} (${m.hint})` : (m.label || s.symbol);

            const cell = document.createElement('div');
            cell.className = 'forex-mini-chart-cell';
            cell.setAttribute('data-forex-key', s.symbol);

            const h = document.createElement('h4');
            h.className = 'forex-mini-chart-title';
            h.textContent = titleText;

            const wrap = document.createElement('div');
            wrap.className = 'forex-mini-chart-canvas-wrap';

            const canvas = document.createElement('canvas');
            wrap.appendChild(canvas);
            cell.appendChild(h);
            cell.appendChild(wrap);
            grid.appendChild(cell);

            const color = cryptoChartColors[i % cryptoChartColors.length];

            new Chart(canvas.getContext('2d'), {
                type: 'line',
                data: {
                    datasets: [
                        {
                            label: titleText,
                            data: points,
                            borderColor: color,
                            backgroundColor: 'transparent',
                            tension: 0.3,
                            pointRadius: 0,
                            borderWidth: 1.5,
                            spanGaps: true,
                        },
                    ],
                },
                options: {
                    responsive: true,
                    maintainAspectRatio: false,
                    parsing: false,
                    interaction: { mode: 'index', intersect: false },
                    scales: {
                        x: {
                            type: 'linear',
                            min: xMin,
                            max: xMax,
                            ticks: {
                                color: '#94a3b8',
                                maxRotation: 45,
                                maxTicksLimit: 5,
                                callback(val) {
                                    return formatForexChartLabel(val);
                                },
                            },
                            grid: { color: 'rgba(255,255,255,0.05)' },
                        },
                        y: {
                            ticks: {
                                color: '#94a3b8',
                                callback: (v) =>
                                    typeof v === 'number'
                                        ? v.toLocaleString(undefined, { maximumFractionDigits: 4 })
                                        : v,
                            },
                            grid: { color: 'rgba(255,255,255,0.05)' },
                        },
                    },
                    plugins: {
                        legend: { display: false },
                        tooltip: {
                            callbacks: {
                                title(items) {
                                    const x = items[0]?.parsed?.x;
                                    return x != null ? new Date(x).toLocaleString() : '';
                                },
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
        });
    } catch (e) {
        console.error('Forex chart fetch error', e);
        clearForexChartsGrid();
    }
}

let currentTimeframe = '1h';
let statsEnabledState = false;

function formatActivityChartLabel(d, tf) {
    const t = d instanceof Date ? d : new Date(d);
    if (Number.isNaN(t.getTime())) return '';
    switch (tf) {
        case '1h':
            return t.toLocaleString([], { hour: '2-digit', minute: '2-digit' });
        case '6h':
            return t.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
        case '1d':
            return t.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
        case '5d':
            return t.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit' });
        case '1m':
        case '6m':
            return t.toLocaleString([], { month: 'short', day: 'numeric' });
        case '1y':
            return t.toLocaleString([], { month: 'short', year: 'numeric' });
        default:
            return t.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    }
}

function activityChartLiveMaxPoints() {
    return 60;
}

function syncActivityChartColors() {
    if (!activityChart) return;
    const st = getComputedStyle(document.documentElement);
    const primary = (st.getPropertyValue('--chart-line-a').trim() || '#38bdf8');
    const accent = (st.getPropertyValue('--chart-line-b').trim() || '#c084fc');
    const grid = st.getPropertyValue('--chart-grid').trim() || 'rgba(255,255,255,0.05)';
    const tick = st.getPropertyValue('--chart-tick').trim() || '#94a3b8';
    const fillA = st.getPropertyValue('--chart-fill-a').trim() || 'rgba(56, 189, 248, 0.1)';
    const fillB = st.getPropertyValue('--chart-fill-b').trim() || 'rgba(192, 132, 252, 0.1)';
    activityChart.data.datasets[0].borderColor = primary;
    activityChart.data.datasets[0].backgroundColor = fillA;
    activityChart.data.datasets[1].borderColor = accent;
    activityChart.data.datasets[1].backgroundColor = fillB;
    activityChart.options.scales.y.grid.color = grid;
    activityChart.options.scales.y.ticks.color = tick;
    activityChart.options.scales.x.ticks.color = tick;
    activityChart.update('none');
}

function refreshStatsStreamState() {
    if (!statsEnabledState || !lastIsAdmin || currentTimeframe !== '1h') {
        stopStatsStream();
        return;
    }
    startStatsStream();
}

function updateStatsStatus(enabled, isAdmin) {
    statsEnabledState = enabled;
    const btn = document.getElementById('stats-toggle');
    const indicator = document.getElementById('stats-indicator');
    if (enabled) {
        indicator.classList.add('pulse');
        indicator.style.background = 'var(--primary)';
        refreshStatsStreamState();
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
    syncActivityChartColors();
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

        activityChart.data.labels = data.map(e => formatActivityChartLabel(new Date(e.timestamp), tf));
        activityChart.data.datasets[0].data = data.map(e => e.messages);
        activityChart.data.datasets[1].data = data.map(e => e.ai_requests);
        activityChart._seenTs = new Set(data.map(e => new Date(e.timestamp).getTime()));
        syncActivityChartColors();
        activityChart.update();
        refreshStatsStreamState();
    } catch (err) {
        console.error('Stats history fetch error', err);
    }
}

function startStatsStream() {
    if (currentTimeframe !== '1h' || !statsEnabledState || !lastIsAdmin) return;
    if (currentStatsSource) return;
    currentStatsSource = new EventSource('/api/stats/stream');
    currentStatsSource.onmessage = (e) => {
        if (!activityChart || currentTimeframe !== '1h') return;
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

        activityChart.data.labels.push(formatActivityChartLabel(new Date(entry.timestamp), '1h'));
        activityChart.data.datasets[0].data.push(entry.messages);
        activityChart.data.datasets[1].data.push(entry.ai_requests);

        const limit = activityChartLiveMaxPoints();
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
                        <button type="button" onclick="copyBookmarkUrl(decodeURIComponent('${encodeURIComponent(b.url)}'))" title="Copy URL" class="row-action" aria-label="Copy URL">
                            ${rowIcons.copy}
                        </button>
                    </div>
                </td>
                <td style="padding: 1rem; text-align: right; color: var(--text-muted);">${new Date(b.timestamp).toLocaleDateString()}</td>
                ${lastIsAdmin ? `
                <td style="padding: 1rem; text-align: right;">
                    <div class="row-action-group" style="justify-content: flex-end;">
                        <button type="button" onclick="deleteBookmark(${b.id})" class="row-action row-action--danger" title="Delete" aria-label="Delete bookmark">${rowIcons.trash}</button>
                    </div>
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

function formatApprovedAtSqlNull(approvedAt) {
    if (!approvedAt || !approvedAt.Valid || !approvedAt.Time) return 'N/A';
    return new Date(approvedAt.Time).toLocaleString();
}

function formatSubmittedAt(createdAt) {
    if (!createdAt) return 'N/A';
    return new Date(createdAt).toLocaleString();
}

/** @returns {{ html: string, rowMuted: boolean }} */
function publicExpiryCell(p) {
    const days = Number(p.expires_in_days ?? p.ExpiresInDays ?? 0);
    const iso = p.expires_at || p.ExpiresAt;
    const expired = !!(p.is_expired ?? p.IsExpired);
    if (!days) {
        return { html: '<span style="color: var(--text-muted);">Never</span>', rowMuted: false };
    }
    if (!iso) {
        return { html: '—', rowMuted: expired };
    }
    const dt = new Date(iso);
    const label = Number.isNaN(dt.getTime()) ? String(iso) : dt.toLocaleString();
    const safe = financeEscapeHtml(label);
    if (expired) {
        return {
            html: `<span style="color: var(--error); font-weight: 600;">Expired</span><div style="font-size: 0.65rem; color: var(--text-muted); margin-top: 0.2rem;">${safe}</div>`,
            rowMuted: true,
        };
    }
    return { html: `<span>${safe}</span>`, rowMuted: false };
}

function hideUploadDetail() {
    document.getElementById('upload-detail-modal')?.classList.add('hidden');
    document.getElementById('modal-overlay')?.classList.add('hidden');
}

async function openUploadDetail(ticketId) {
    ['login-modal', 'adduser-modal', 'force-password-modal'].forEach((id) => {
        document.getElementById(id)?.classList.add('hidden');
    });
    document.getElementById('modal-overlay')?.classList.remove('hidden');
    document.getElementById('upload-detail-modal')?.classList.remove('hidden');
    const body = document.getElementById('upload-detail-body');
    const actions = document.getElementById('upload-detail-actions');
    body.innerHTML = '<p style="color: var(--text-muted);">Loading…</p>';
    actions.innerHTML = '';
    try {
        const res = await fetch(`/api/uploads/detail?ticketID=${encodeURIComponent(ticketId)}&t=${Date.now()}`);
        if (res.status === 401) {
            body.innerHTML = '<p style="color: var(--text-muted);">Log in to view full detail.</p>';
            return;
        }
        if (!res.ok) {
            body.innerHTML = '<p style="color: var(--error);">Could not load detail.</p>';
            return;
        }
        const d = await res.json();
        const host = d.client_host ? financeEscapeHtml(String(d.client_host)) : '—';
        const md5 = d.md5_hex ? financeEscapeHtml(String(d.md5_hex)) : '—';
        const sha = d.sha256_hex ? financeEscapeHtml(String(d.sha256_hex)) : '—';
        const when = d.approved_at ? new Date(d.approved_at).toLocaleString() : 'N/A';
        const expDays = Number(d.expires_in_days) || 0;
        let expiryBlock = '';
        if (!expDays) {
            expiryBlock = `<div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Expires (public)</span> <span style="color: var(--text-muted);">Never</span></div>`;
        } else if (d.expires_at) {
            const expWhen = new Date(d.expires_at).toLocaleString();
            const safeExp = financeEscapeHtml(expWhen);
            if (d.is_expired) {
                const staffHint = lastStaffAdmin ? ' <span style="color: var(--text-muted); font-size: 0.75rem;">Staff can still open.</span>' : '';
                expiryBlock = `<div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Expires (public)</span> <span style="color: var(--error); font-weight: 600;">Expired</span> <span class="mono" style="font-size: 0.8rem;">${safeExp}</span>${staffHint}</div>`;
            } else {
                expiryBlock = `<div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Expires (public)</span> <span class="mono" style="font-size: 0.85rem;">${safeExp}</span></div>`;
            }
        }
        const sz = formatFileSize(d.size_bytes);
        const fn = financeEscapeHtml(String(d.display_filename || '—'));
        const ref = financeEscapeHtml(String(d.public_ref || '—'));
        const pasteKind = !d.is_file ? financeEscapeHtml(pasteKindLabel(d)) : '';
        const statusLine = d.status && d.status !== 'approved'
            ? `<div style="margin-bottom: 0.5rem; color: var(--accent);">Status: ${financeEscapeHtml(String(d.status))}</div>`
            : '';
        body.innerHTML = `
            ${statusLine}
            <div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Ticket</span> <span class="mono">${financeEscapeHtml(String(d.ticket_id))}</span></div>
            <div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Ref</span> <span class="mono">${ref}</span></div>
            <div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Title</span> ${financeEscapeHtml(String(d.title || '—'))}</div>
            ${!d.is_file ? `<div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Kind</span> ${pasteKind}</div>` : ''}
            <div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">File</span> <span class="mono">${fn}</span></div>
            <div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Size</span> ${sz}</div>
            <div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Nickname</span> ${financeEscapeHtml(String(d.username || '—'))}</div>
            <div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Host / IP</span> <span class="mono">${host}</span></div>
            <div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Published</span> ${when}</div>
            ${expiryBlock}
            ${d.is_file ? `<div style="margin-bottom: 0.35rem;"><span style="color: var(--text-muted);">Downloads</span> ${Number(d.download_count) || 0}</div>` : ''}
            <div style="margin-top: 0.75rem;"><span style="color: var(--text-muted);">MD5</span><br><span class="mono" style="font-size: 0.7rem; word-break: break-all;">${md5}</span></div>
            <div style="margin-top: 0.5rem;"><span style="color: var(--text-muted);">SHA-256</span><br><span class="mono" style="font-size: 0.7rem; word-break: break-all;">${sha}</span></div>
            ${d.is_file && d.status === 'approved' ? `
            <div style="margin-top: 0.85rem; padding-top: 0.85rem; border-top: 1px solid var(--glass-border);">
                ${lastStaffAdmin ? `<label style="display: flex; align-items: flex-start; gap: 0.55rem; cursor: pointer; color: var(--text-main);">
                    <input type="checkbox" id="upload-detail-public" style="margin-top: 0.2rem;" ${d.is_public ? 'checked' : ''} onchange="setUploadIsPublic('${ticketId}', this.checked)">
                    <span style="font-size: 0.8rem; line-height: 1.35;">Public — anyone can download via <span class="mono">/f/…</span> and see this file on the dashboard without logging in. Off = staff-only.</span>
                </label>` : `<label style="display: flex; align-items: flex-start; gap: 0.55rem; cursor: default; color: var(--text-muted);">
                    <input type="checkbox" style="margin-top: 0.2rem;" ${d.is_public ? 'checked' : ''} disabled aria-readonly="true">
                    <span style="font-size: 0.8rem; line-height: 1.35;">${d.is_public ? 'Public file — listed here and downloadable via <span class="mono">/f/…</span> without an account.' : 'Staff-only file.'}</span>
                </label>`}
            </div>` : ''}`;
        let btns = '';
        if (d.is_file && d.download_path) {
            btns += `<a href="${financeEscapeHtml(d.download_path)}" target="_blank" rel="noopener" class="row-action row-action--primary" title="Download" aria-label="Download">${rowIcons.download}</a>`;
            if (lastStaffAdmin && d.can_compress) {
                btns += `<button type="button" class="row-action" title="Compress to .tgz" aria-label="Compress to .tgz" onclick="compressUploadFile('${ticketId}')">${rowIcons.archive}</button>`;
            }
        } else if (d.is_file && d.is_expired && !lastStaffAdmin) {
            btns += `<span class="row-action" style="opacity: 0.45; cursor: default;" title="This file has expired for public access">${rowIcons.download}</span>`;
        } else if (d.view_path) {
            btns += `<a href="${financeEscapeHtml(d.view_path)}" target="_blank" rel="noopener" class="row-action row-action--primary" title="View" aria-label="View">${rowIcons.view}</a>`;
        } else if (!d.is_file && d.is_expired && !lastStaffAdmin) {
            btns += `<span class="row-action" style="opacity: 0.45; cursor: default;" title="This paste has expired for public access">${rowIcons.view}</span>`;
        }
        if (lastStaffAdmin) {
            btns += `<button type="button" class="row-action row-action--danger" title="Delete" aria-label="Delete" onclick="deletePasteFromDetail('${ticketId}')">${rowIcons.trash}</button>`;
        }
        actions.innerHTML = btns;
    } catch (e) {
        console.error(e);
        body.innerHTML = '<p style="color: var(--error);">Error loading detail.</p>';
    }
}

async function compressUploadFile(ticketId) {
    try {
        const res = await fetch(`/api/uploads/files/compress?ticketID=${encodeURIComponent(ticketId)}&t=${Date.now()}`, { method: 'POST' });
        if (res.ok) {
            await fetchApprovedFiles(filePage);
            await openUploadDetail(ticketId);
        } else {
            const t = await res.text();
            alert(t || 'Compress failed');
        }
    } catch (e) {
        console.error(e);
        alert('Compress failed');
    }
}

async function setUploadIsPublic(ticketId, isPublic) {
    try {
        const res = await fetch('/api/uploads/public', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ticket_id: ticketId, is_public: isPublic }),
        });
        if (!res.ok) {
            alert((await res.text()) || 'Update failed');
            await openUploadDetail(ticketId);
            return;
        }
        await fetchApprovedFiles(filePage);
    } catch (e) {
        console.error(e);
        alert('Update failed');
        await openUploadDetail(ticketId);
    }
}

async function deletePasteFromDetail(ticketId) {
    if (!confirm('Delete this item?')) return;
    try {
        const res = await fetch(`/api/pastes/delete?ticketID=${encodeURIComponent(ticketId)}&t=${Date.now()}`, { method: 'DELETE' });
        if (res.ok) {
            hideUploadDetail();
            await fetchApprovedPastes(pastePage);
            await fetchApprovedFiles(filePage);
        } else {
            alert('Delete failed');
        }
    } catch (e) {
        console.error(e);
    }
}

// PASTES
function pasteKindLabel(p) {
    const k = p.paste_kind != null && String(p.paste_kind).trim() !== '' ? p.paste_kind
        : (p.PasteKind != null && String(p.PasteKind).trim() !== '' ? p.PasteKind : '');
    return k || 'plain text';
}

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
        const kind = financeEscapeHtml(pasteKindLabel(p));
        const host = (p.client_host || p.ClientHost) ? financeEscapeHtml(String(p.client_host || p.ClientHost)) : '—';
        const sz = formatFileSize(p.size_bytes ?? p.SizeBytes);
        const pub = formatApprovedAtSqlNull(p.approved_at || p.ApprovedAt);
        const exp = publicExpiryCell(p);
        const rowBg = exp.rowMuted ? 'background: rgba(220, 60, 60, 0.07);' : '';
        const rowFade = exp.rowMuted ? 'opacity: 0.88;' : '';
        return `
            <tr style="border-bottom: 1px solid var(--glass-border); ${rowBg} ${rowFade}">
                <td style="padding: 1rem;">
                    <button type="button" onclick="openUploadDetail('${tid}')" class="btn btn-ghost" style="padding: 0; font-weight: 700; color: var(--accent); cursor: pointer;">${tid}</button>
                </td>
                <td style="padding: 1rem;">${kind}</td>
                <td style="padding: 1rem;">${financeEscapeHtml(String(title))}</td>
                <td style="padding: 1rem;">${sz}</td>
                <td style="padding: 1rem; color: var(--primary);">${financeEscapeHtml(String(user))}</td>
                <td style="padding: 1rem;" class="mono">${host}</td>
                <td style="padding: 1rem;">${pub}</td>
                <td style="padding: 1rem;">${exp.html}</td>
                <td style="padding: 1rem; text-align: right;">
                    <div class="row-action-group" style="justify-content: flex-end;">
                        <a href="/p/${tid}" target="_blank" rel="noopener" class="row-action row-action--primary" title="View paste" aria-label="View paste">${rowIcons.view}</a>
                        ${lastStaffAdmin ? `<button type="button" class="row-action row-action--danger" title="Delete" aria-label="Delete" onclick="deletePaste('${tid}')">${rowIcons.trash}</button>` : ''}
                    </div>
                </td>
            </tr>
        `;
    }).join('');

    const totalPages = Math.max(0, Number(data.total_pages) || 0);
    const prevBtn = document.getElementById('pastes-prev-page');
    const nextBtn = document.getElementById('pastes-next-page');
    if (prevBtn) prevBtn.disabled = page <= 1;
    if (nextBtn) nextBtn.disabled = totalPages === 0 || page >= totalPages;
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
        const kind = financeEscapeHtml(pasteKindLabel(p));
        const host = (p.client_host || p.ClientHost) ? financeEscapeHtml(String(p.client_host || p.ClientHost)) : '—';
        const sz = formatFileSize(p.size_bytes ?? p.SizeBytes);
        const sub = formatSubmittedAt(p.created_at || p.CreatedAt);
        return `
            <tr style="border-bottom: 1px solid var(--glass-border); background: var(--accent-glow);">
                <td style="padding: 1rem;">
                    <button type="button" onclick="openUploadDetail('${tid}')" class="btn btn-ghost" style="padding: 0; font-weight: 700; color: var(--accent); cursor: pointer;">${tid}</button>
                </td>
                <td style="padding: 1rem;">${kind}</td>
                <td style="padding: 1rem;">
                    <div>${financeEscapeHtml(String(title))}</div>
                    <div style="font-size: 0.65rem; color: var(--text-muted); margin-top: 0.25rem;">Submitted: ${sub}</div>
                </td>
                <td style="padding: 1rem;">${sz}</td>
                <td style="padding: 1rem;">${financeEscapeHtml(String(user))}</td>
                <td style="padding: 1rem;" class="mono">${host}</td>
                <td style="padding: 1rem;">${sub}</td>
                <td style="padding: 1rem; text-align: right;">
                    <div class="row-action-group" style="justify-content: flex-end;">
                        <button type="button" class="row-action row-action--success" title="Approve" aria-label="Approve" onclick="approvePaste('${tid}')">${rowIcons.check}</button>
                        <button type="button" class="row-action row-action--danger" title="Reject" aria-label="Reject" onclick="rejectPaste('${tid}')">${rowIcons.x}</button>
                    </div>
                </td>
            </tr>
        `;
    }).join('') || '<tr><td colspan="8" style="padding: 1rem; text-align: center; color: var(--text-muted);">No pending approvals</td></tr>';
}

// FILE UPLOADS (!upload)
let filePage = 1;

function formatFileSize(bytes) {
    const n = Number(bytes);
    if (!Number.isFinite(n) || n < 0) return '—';
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

async function fetchUploadSettings() {
    if (!lastStaffAdmin) return;
    try {
        const res = await fetch('/api/uploads/settings');
        if (!res.ok) return;
        const data = await res.json();
        const el = document.getElementById('uploads-max-mb');
        if (el && data.max_file_mb != null) el.value = data.max_file_mb;
    } catch (e) {
        console.error('upload settings', e);
    }
}

async function saveUploadSettings() {
    const el = document.getElementById('uploads-max-mb');
    const status = document.getElementById('upload-settings-status');
    if (!el) return;
    const mb = parseInt(el.value, 10);
    if (!Number.isFinite(mb) || mb < 1 || mb > 2048) {
        status.textContent = 'Invalid (1–2048)';
        status.style.color = 'var(--error)';
        return;
    }
    status.textContent = 'Saving...';
    status.style.color = 'var(--text-muted)';
    try {
        const res = await fetch('/api/uploads/settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ max_file_mb: mb }),
        });
        if (res.ok) {
            status.textContent = 'Saved';
            status.style.color = 'var(--success)';
            setTimeout(() => { status.textContent = ''; }, 2500);
        } else {
            status.textContent = 'Save failed';
            status.style.color = 'var(--error)';
        }
    } catch (e) {
        status.textContent = 'Error';
        status.style.color = 'var(--error)';
    }
}

async function fetchApprovedFiles(page) {
    if (page < 1) page = 1;
    filePage = page;
    const res = await fetch(`/api/uploads/files?page=${page}&t=${Date.now()}`);
    if (!res.ok) return;
    const data = await res.json();
    document.getElementById('files-count').textContent = `${data.total_count} items`;
    document.getElementById('files-list').innerHTML = (data.files || []).map((p) => {
        const tid = p.ticket_id || p.TicketID;
        const title = p.title || p.Title;
        const user = p.username || p.Username;
        const fname = p.original_filename || p.OriginalFilename || '—';
        const sz = formatFileSize(p.size_bytes ?? p.SizeBytes);
        const dl = p.download_count ?? p.DownloadCount ?? 0;
        const host = (p.client_host || p.ClientHost) ? financeEscapeHtml(String(p.client_host || p.ClientHost)) : '—';
        const pub = formatApprovedAtSqlNull(p.approved_at || p.ApprovedAt);
        const pubTick = (p.is_public === true || p.IsPublic === true) ? '✓' : '—';
        const exp = publicExpiryCell(p);
        const expired = !!(p.is_expired ?? p.IsExpired);
        const rowBg = exp.rowMuted ? 'background: rgba(220, 60, 60, 0.07);' : '';
        const rowFade = exp.rowMuted ? 'opacity: 0.88;' : '';
        const canDlPublic = lastStaffAdmin || !expired;
        const dlCtrl = canDlPublic
            ? `<a href="/f/${tid}" target="_blank" rel="noopener" class="row-action row-action--primary" title="Download" aria-label="Download">${rowIcons.download}</a>`
            : `<span class="row-action" style="opacity: 0.4; cursor: default;" title="Expired — not available publicly">${rowIcons.download}</span>`;
        return `
            <tr style="border-bottom: 1px solid var(--glass-border); ${rowBg} ${rowFade}">
                <td style="padding: 1rem;">
                    <button type="button" onclick="openUploadDetail('${tid}')" class="btn btn-ghost" style="padding: 0; font-weight: 700; color: var(--accent); cursor: pointer;">${tid}</button>
                </td>
                <td style="padding: 1rem;">
                    <div>${financeEscapeHtml(String(title))}</div>
                    <div style="font-size: 0.65rem; color: var(--text-muted); margin-top: 0.25rem;">${dl} download(s) · ${financeEscapeHtml(String(fname))}</div>
                </td>
                <td style="padding: 1rem;">${sz}</td>
                <td style="padding: 1rem; color: var(--primary);">${financeEscapeHtml(String(user))}</td>
                <td style="padding: 1rem;" class="mono">${host}</td>
                <td style="padding: 1rem;">${pub}</td>
                <td style="padding: 1rem;">${exp.html}</td>
                <td style="padding: 1rem; text-align: center; color: var(--success);" title="Public = visible without login">${pubTick}</td>
                <td style="padding: 1rem; text-align: right;">
                    <div class="row-action-group" style="justify-content: flex-end;">
                        ${dlCtrl}
                        ${lastStaffAdmin ? `<button type="button" class="row-action row-action--danger" title="Delete" aria-label="Delete" onclick="deletePaste('${tid}')">${rowIcons.trash}</button>` : ''}
                    </div>
                </td>
            </tr>
        `;
    }).join('');
    document.getElementById('files-prev-page').disabled = page <= 1;
    document.getElementById('files-next-page').disabled = page >= data.total_pages;
}

function changeFilePage(d) {
    if (filePage + d < 1) return;
    fetchApprovedFiles(filePage + d);
}

async function fetchPendingFiles() {
    const res = await fetch(`/api/uploads/files/pending?t=${Date.now()}`);
    if (!res.ok) return;
    const data = await res.json();
    document.getElementById('pending-files-list').innerHTML = data.map((p) => {
        const tid = p.ticket_id || p.TicketID;
        const title = p.title || p.Title;
        const user = p.username || p.Username;
        const fname = p.original_filename || p.OriginalFilename || '—';
        const sz = formatFileSize(p.size_bytes ?? p.SizeBytes);
        const host = (p.client_host || p.ClientHost) ? financeEscapeHtml(String(p.client_host || p.ClientHost)) : '—';
        const sub = formatSubmittedAt(p.created_at || p.CreatedAt);
        return `
            <tr style="border-bottom: 1px solid var(--glass-border); background: var(--accent-glow);">
                <td style="padding: 1rem;">
                    <button type="button" onclick="openUploadDetail('${tid}')" class="btn btn-ghost" style="padding: 0; font-weight: 700; color: var(--accent); cursor: pointer;">${tid}</button>
                </td>
                <td style="padding: 1rem;">
                    <div>${financeEscapeHtml(String(title))}</div>
                    <div style="font-size: 0.65rem; color: var(--text-muted); margin-top: 0.25rem;">${financeEscapeHtml(String(fname))}</div>
                </td>
                <td style="padding: 1rem;">${sz}</td>
                <td style="padding: 1rem;">${financeEscapeHtml(String(user))}</td>
                <td style="padding: 1rem;" class="mono">${host}</td>
                <td style="padding: 1rem;">${sub}</td>
                <td style="padding: 1rem; text-align: right;">
                    <div class="row-action-group" style="justify-content: flex-end;">
                        <button type="button" class="row-action row-action--success" title="Approve" aria-label="Approve" onclick="approvePaste('${tid}')">${rowIcons.check}</button>
                        <button type="button" class="row-action row-action--danger" title="Reject" aria-label="Reject" onclick="rejectPaste('${tid}')">${rowIcons.x}</button>
                    </div>
                </td>
            </tr>
        `;
    }).join('') || '<tr><td colspan="7" style="padding: 1rem; text-align: center; color: var(--text-muted);">No pending file uploads</td></tr>';
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
            list.innerHTML = '<tr class="news-row news-row--empty"><td colspan="3" class="news-empty" style="padding: 2rem; text-align: center; color: var(--text-muted);">No news found.</td></tr>';
            return;
        }

        list.innerHTML = data.news.map(n => `
            <tr class="news-row" style="border-bottom: 1px solid var(--glass-border);">
                <td class="news-td news-td--title" style="padding: 1rem;">
                    <div class="news-item-head">
                        <div class="news-item-head__main">
                            ${newsSourceBadgeFor(n.Source)}
                            <div class="news-item-head__title">${n.Title}</div>
                        </div>
                        <div class="row-action-group">
                            <a href="${n.Link}" target="_blank" rel="noopener" class="row-action row-action--primary" title="Open article" aria-label="Open article">${rowIcons.globe}</a>
                            ${n.ShortLink ? `<a href="${n.ShortLink}" target="_blank" rel="noopener" class="row-action row-action--accent" title="Short link" aria-label="Short link">${rowIcons.linkChain}</a>` : ''}
                        </div>
                    </div>
                </td>
                <td class="news-td news-td--date" data-label="Date" style="padding: 1rem; text-align: right; color: var(--text-muted);">
                    ${new Date(n.PubDate).toLocaleDateString()} ${new Date(n.PubDate).toLocaleTimeString([], {hour: '2-digit', minute:'2-digit'})}
                </td>
                ${lastIsAdmin ? `
                <td class="news-td news-td--actions" data-label="Actions" style="padding: 1rem; text-align: right;">
                    <div class="row-action-group" style="justify-content: flex-end;">
                        <button type="button" class="row-action row-action--danger" title="Delete" aria-label="Delete news item" onclick="deleteNews('${n.GUID}')">${rowIcons.trash}</button>
                    </div>
                </td>` : '<td class="news-td news-td--actions hidden"></td>'}
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

function showRehashToast(message, isError) {
    const el = document.getElementById('rehash-toast');
    if (!el) return;
    el.textContent = message;
    el.style.background = isError ? 'var(--error, #dc2626)' : 'var(--primary, #0ea5e9)';
    el.style.color = '#fff';
    el.classList.remove('hidden');
    if (window._rehashToastTimer) clearTimeout(window._rehashToastTimer);
    window._rehashToastTimer = setTimeout(() => el.classList.add('hidden'), 4500);
}

async function rehashConfigFromDisk() {
    try {
        const res = await fetch('/api/rehash', { method: 'POST', credentials: 'same-origin' });
        const text = await res.text();
        if (!res.ok) throw new Error(text || res.statusText);
        showRehashToast('Configuration reloaded. Logged-in IRC admins were notified.', false);
    } catch (e) {
        showRehashToast('Rehash failed: ' + e.message, true);
    }
}

async function fetchUsers() {
    const res = await fetch('/api/users');
    const users = await res.json();
    document.getElementById('users-list').innerHTML = users.map(u => `
        <tr style="border-bottom: 1px solid var(--glass-border);">
            <td style="padding: 1rem;">${u.id}</td>
            <td style="padding: 1rem;" class="font-bold">${u.username}</td>
            <td style="padding: 1rem;"><span class="badge badge-admin">${u.role}</span></td>
            <td style="padding: 1rem; text-align: right;">
                <div class="row-action-group" style="justify-content: flex-end;">
                    <button type="button" class="row-action row-action--danger" title="Delete user" aria-label="Delete user" onclick="deleteUser(${u.id})">${rowIcons.trash}</button>
                </div>
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

async function onThemeSelectChange() {
    const sel = document.getElementById('theme-select');
    if (!sel) return;
    const v = normalizeTheme(sel.value, 'dark');
    applyTheme(v);
    if (lastIsAdmin) {
        try {
            const res = await fetch('/api/me/ui-theme', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ theme: v }),
            });
            if (!res.ok) await fetchStatus();
        } catch (e) {
            console.error(e);
            await fetchStatus();
        }
    } else {
        try {
            localStorage.setItem(UI_THEME_STORAGE_KEY, v);
        } catch (e) {
            console.error(e);
        }
    }
}

// Global functions exposed to window
window.showPanel = showPanel;
window.toggleSidebar = toggleSidebar;
window.closeSidebar = closeSidebar;
window.showLogin = showLogin;
window.hideLogin = hideLogin;
window.login = login;
window.logout = logout;
window.onThemeSelectChange = onThemeSelectChange;
window.toggleStats = toggleStats;
window.toggleNews = toggleNews;
window.updatePassword = updatePassword;
async function deletePaste(id) {
    if(!confirm('Are you sure you want to delete this paste?')) return;
    try {
        const res = await fetch(`/api/pastes/delete?ticketID=${id}&t=${Date.now()}`, { method: 'DELETE' });
        if (res.ok) {
            await fetchApprovedPastes(pastePage);
            await fetchApprovedFiles(filePage);
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
            if (btn && btn.closest('tr')) btn.closest('tr').remove();
            await fetchPendingPastes();
            await fetchApprovedPastes(1);
            await fetchPendingFiles();
            await fetchApprovedFiles(1);
        } else {
            const err = await res.text();
            alert('Approval failed: ' + err);
            fetchPendingPastes();
            fetchPendingFiles();
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
            await fetchPendingFiles();
        } else {
            const err = await res.text();
            alert('Rejection failed: ' + err);
            fetchPendingPastes();
            fetchPendingFiles();
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

// Global functions exposed to window (re-export for late-loaded handlers)
window.showPanel = showPanel;
window.toggleSidebar = toggleSidebar;
window.closeSidebar = closeSidebar;
window.showLogin = showLogin;
window.hideLogin = hideLogin;
window.login = login;
window.logout = logout;
window.onThemeSelectChange = onThemeSelectChange;
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
window.openUploadDetail = openUploadDetail;
window.hideUploadDetail = hideUploadDetail;
window.compressUploadFile = compressUploadFile;
window.setUploadIsPublic = setUploadIsPublic;
window.deletePasteFromDetail = deletePasteFromDetail;
window.changeFilePage = changeFilePage;
window.saveUploadSettings = saveUploadSettings;
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
        const announce = document.getElementById('rss-announce-irc');
        if (announce) announce.checked = !!data.announce_to_irc;
    } catch (e) { console.error("Failed to fetch RSS settings", e); }
}

async function saveRSSSettings() {
    const status = document.getElementById('rss-settings-status');
    status.textContent = 'Saving...';
    status.style.color = 'var(--text-muted)';

    const interval = parseInt(document.getElementById('rss-interval').value);
    const retention = parseInt(document.getElementById('rss-retention').value);
    const urls = document.getElementById('rss-urls').value.split('\n').map(u => u.trim()).filter(u => u !== '');
    const announceEl = document.getElementById('rss-announce-irc');
    const announce_to_irc = announceEl ? announceEl.checked : true;

    try {
        const res = await fetch('/api/rss/settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                interval_minutes: interval,
                retention_count: retention,
                feed_urls: urls,
                announce_to_irc
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
