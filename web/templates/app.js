let logSources = {};
let activeLogChannel = null;
let currentStatsSource = null;
let activityChart = null;

async function fetchStatus() {
    try {
        const res = await fetch('/api/status');
        if (!res.ok) throw new Error('Status fetch failed');
        const data = await res.json();
        
        document.getElementById('uptime').textContent = data.uptime;
        document.getElementById('server').textContent = (data.nickname || 'Bot') + ' @ ' + data.server;
        document.getElementById('ai_requests').textContent = data.ai_requests || 0;
        document.getElementById('ai_model').textContent = data.ai_model || 'Unknown';
        
        updateNewsStatus(data.rss_enabled);
        updateStatsStatus(data.stats_enabled);
        
        const badge = document.getElementById('status-badge');
        badge.textContent = 'Online';
        badge.classList.remove('bg-primary/10', 'text-primary');
        badge.classList.add('bg-green-500/10', 'text-green-500');

        const channelsContainer = document.getElementById('channels');
        channelsContainer.innerHTML = '';
        if (data.channels) {
            data.channels.forEach(ch => {
                const btn = document.createElement('button');
                btn.className = 'px-4 py-2 bg-slate-800 hover:bg-slate-700 text-primary border border-white/5 rounded-lg mono text-sm transition-all hover:scale-105 active:scale-95 cursor-pointer';
                btn.textContent = ch;
                btn.onclick = () => openLogs(ch);
                channelsContainer.appendChild(btn);
            });
        }

        if (data.admin_nicknames) {
            updateAdminsUI(data.admin_nicknames, data.channel_admins);
        }
    } catch (e) {
        console.error("Failed to load status", e);
        const badge = document.getElementById('status-badge');
        badge.textContent = 'Offline';
        badge.classList.add('bg-red-500/10', 'text-red-500');
    }
}

function openLogs(channel) {
    const container = document.getElementById('log-container');
    container.classList.remove('hidden');

    // If already open, just switch
    if (logSources[channel]) {
        switchTab(channel);
        return;
    }

    // Create Tab
    const tabsContainer = document.getElementById('log-tabs');
    const tab = document.createElement('div');
    tab.id = `tab-${channel.replace('#', '')}`;
    tab.className = 'flex items-center gap-2 px-3 py-1.5 rounded-t-lg bg-slate-900 border border-b-0 border-white/5 cursor-pointer transition-all hover:bg-slate-800';
    tab.innerHTML = `
        <span class="mono text-xs font-bold text-slate-400">${channel}</span>
        <button onclick="event.stopPropagation(); closeTab('${channel}')" class="text-slate-600 hover:text-red-400 transition-colors">
            <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"></path></svg>
        </button>
    `;
    tab.onclick = () => switchTab(channel);
    tabsContainer.appendChild(tab);

    // Create Output Div
    const wrapper = document.getElementById('log-outputs-wrapper');
    const output = document.createElement('div');
    output.id = `output-${channel.replace('#', '')}`;
    output.className = 'absolute inset-0 p-6 overflow-y-auto mono text-sm space-y-1 scrollbar-thin scrollbar-thumb-white/10 scrollbar-track-transparent hidden';
    output.innerHTML = '<div class="text-slate-500 italic">Initializing stream...</div>';
    wrapper.appendChild(output);

    // Start Stream
    const source = new EventSource(`/api/logs/stream?channel=${encodeURIComponent(channel)}`);
    logSources[channel] = source;

    source.onmessage = (event) => {
        const line = document.createElement('div');
        line.className = 'hover:bg-white/5 px-2 py-0.5 rounded transition-colors';
        
        let text = event.data;
        if (text.includes('[MESSAGE]')) line.classList.add('text-slate-200');
        else if (text.includes('[JOIN]')) line.classList.add('text-green-400/80');
        else if (text.includes('[PART]')) line.classList.add('text-red-400/80');
        else if (text.includes('[ACTION]')) line.classList.add('text-accent');
        
        line.textContent = text;
        output.appendChild(line);
        output.scrollTop = output.scrollHeight;
    };

    source.onerror = (err) => {
        console.error(`SSE error for ${channel}`, err);
        const errDiv = document.createElement('div');
        errDiv.className = 'text-red-400 text-xs italic bg-red-400/5 p-2 rounded mt-2';
        errDiv.textContent = "Connection lost. Retrying...";
        output.appendChild(errDiv);
    };

    switchTab(channel);
}

function switchTab(channel) {
    activeLogChannel = channel;
    
    // Update Tab Styles
    document.querySelectorAll('#log-tabs > div').forEach(tab => {
        const isSelected = tab.id === `tab-${channel.replace('#', '')}`;
        if (isSelected) {
            tab.classList.remove('bg-slate-900', 'border-white/5');
            tab.classList.add('bg-card', 'border-primary/20', 'z-10');
            tab.querySelector('span').classList.remove('text-slate-400');
            tab.querySelector('span').classList.add('text-primary');
        } else {
            tab.classList.remove('bg-card', 'border-primary/20', 'z-10');
            tab.classList.add('bg-slate-900', 'border-white/5');
            tab.querySelector('span').classList.remove('text-primary');
            tab.querySelector('span').classList.add('text-slate-400');
        }
    });

    // Update Output Visibility
    document.querySelectorAll('#log-outputs-wrapper > div').forEach(div => {
        const isSelected = div.id === `output-${channel.replace('#', '')}`;
        div.classList.toggle('hidden', !isSelected);
    });

    document.getElementById('current-channel-title').textContent = `Logs: ${channel}`;
}

function closeTab(channel) {
    if (logSources[channel]) {
        logSources[channel].close();
        delete logSources[channel];
    }

    const tab = document.getElementById(`tab-${channel.replace('#', '')}`);
    const output = document.getElementById(`output-${channel.replace('#', '')}`);
    
    if (tab) tab.remove();
    if (output) output.remove();

    if (activeLogChannel === channel) {
        const remainingChannels = Object.keys(logSources);
        if (remainingChannels.length > 0) {
            switchTab(remainingChannels[remainingChannels.length - 1]);
        } else {
            closeAllLogs();
        }
    }
}

function closeAllLogs() {
    Object.keys(logSources).forEach(closeTab);
    document.getElementById('log-container').classList.add('hidden');
}

function updateNewsStatus(enabled) {
    const btn = document.getElementById('news-toggle');
    const indicator = document.getElementById('news-indicator');
    const text = document.getElementById('news-status-text');

    if (enabled) {
        btn.classList.remove('border-white/20', 'text-slate-400');
        btn.classList.add('border-primary/50', 'text-primary', 'bg-primary/5');
        indicator.classList.remove('bg-slate-600');
        indicator.classList.add('bg-primary', 'animate-pulse');
        text.textContent = 'ON';
    } else {
        btn.classList.remove('border-primary/50', 'text-primary', 'bg-primary/5');
        btn.classList.add('border-white/20', 'text-slate-400');
        indicator.classList.remove('bg-primary', 'animate-pulse');
        indicator.classList.add('bg-slate-600');
        text.textContent = 'OFF';
    }
}

async function toggleNews() {
    try {
        const res = await fetch('/api/rss/toggle', { method: 'POST' });
        if (!res.ok) throw new Error('Toggle failed');
        const data = await res.json();
        updateNewsStatus(data.rss_enabled);
    } catch (e) {
        console.error("Failed to toggle news", e);
    }
}

// Statistics Logic
function updateStatsStatus(enabled) {
    const btn = document.getElementById('stats-toggle');
    const indicator = document.getElementById('stats-indicator');
    const text = document.getElementById('stats-status-text');
    const container = document.getElementById('stats-container');

    if (enabled) {
        btn.classList.remove('border-white/20', 'text-slate-400');
        btn.classList.add('border-primary/50', 'text-primary', 'bg-primary/5');
        indicator.classList.remove('bg-slate-600');
        indicator.classList.add('bg-primary', 'animate-pulse');
        text.textContent = 'ON';
        container.classList.remove('hidden');
        startStatsStream();
    } else {
        btn.classList.remove('border-primary/50', 'text-primary', 'bg-primary/5');
        btn.classList.add('border-white/20', 'text-slate-400');
        indicator.classList.remove('bg-primary', 'animate-pulse');
        indicator.classList.add('bg-slate-600');
        text.textContent = 'OFF';
        container.classList.add('hidden');
        stopStatsStream();
    }
}

async function toggleStats() {
    try {
        const res = await fetch('/api/stats/toggle', { method: 'POST' });
        if (!res.ok) throw new Error('Toggle failed');
        const data = await res.json();
        updateStatsStatus(data.stats_enabled);
    } catch (e) {
        console.error("Failed to toggle stats", e);
    }
}

function initChart() {
    if (activityChart) return;
    
    const ctx = document.getElementById('activityChart').getContext('2d');
    activityChart = new Chart(ctx, {
        type: 'line',
        data: {
            labels: [],
            datasets: [
                {
                    label: 'Messages',
                    data: [],
                    borderColor: '#38bdf8',
                    backgroundColor: 'rgba(56, 189, 248, 0.1)',
                    borderWidth: 2,
                    tension: 0.4,
                    fill: true,
                    pointRadius: 2
                },
                {
                    label: 'AI Requests',
                    data: [],
                    borderColor: '#c084fc',
                    backgroundColor: 'rgba(192, 132, 252, 0.1)',
                    borderWidth: 2,
                    tension: 0.4,
                    fill: true,
                    pointRadius: 2
                },
                {
                    label: 'Users',
                    data: [],
                    borderColor: '#22c55e',
                    backgroundColor: 'rgba(34, 197, 94, 0.1)',
                    borderWidth: 2,
                    tension: 0.4,
                    fill: true,
                    pointRadius: 2
                },
                {
                    label: 'Admin Cmds',
                    data: [],
                    borderColor: '#ef4444',
                    backgroundColor: 'rgba(239, 68, 68, 0.1)',
                    borderWidth: 2,
                    tension: 0.4,
                    fill: true,
                    pointRadius: 2
                },
                {
                    label: 'Auth Fails',
                    data: [],
                    borderColor: '#f59e0b',
                    backgroundColor: 'rgba(245, 158, 11, 0.1)',
                    borderWidth: 2,
                    tension: 0.4,
                    fill: true,
                    pointRadius: 2
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            scales: {
                y: {
                    beginAtZero: true,
                    grid: { color: 'rgba(255, 255, 255, 0.05)' },
                    ticks: { color: '#94a3b8', font: { family: 'JetBrains Mono', size: 10 } }
                },
                x: {
                    grid: { display: false },
                    ticks: { 
                        color: '#94a3b8', 
                        font: { family: 'JetBrains Mono', size: 10 },
                        autoSkip: true,
                        maxTicksLimit: 10
                    }
                }
            },
            plugins: {
                legend: { display: false },
                tooltip: {
                    backgroundColor: '#1e293b',
                    titleFont: { family: 'Inter' },
                    bodyFont: { family: 'JetBrains Mono' },
                    borderColor: 'rgba(255,255,255,0.1)',
                    borderWidth: 1
                }
            },
            interaction: {
                intersect: false,
                mode: 'index'
            }
        }
    });
}

let currentTimeframe = '1h';

async function fetchHistory(timeframe = '1h') {
    initChart();
    try {
        const res = await fetch(`/api/stats/history?timeframe=${timeframe}`);
        if (!res.ok) throw new Error('History fetch failed');
        const data = await res.json();
        
        activityChart.data.labels = [];
        activityChart.data.datasets.forEach(ds => ds.data = []);
        
        data.forEach(entry => {
            const time = formatTimestamp(entry.timestamp, timeframe);
            activityChart.data.labels.push(time);
            activityChart.data.datasets[0].data.push(entry.messages);
            activityChart.data.datasets[1].data.push(entry.ai_requests);
            activityChart.data.datasets[2].data.push(entry.user_count);
            activityChart.data.datasets[3].data.push(entry.admin_commands || 0);
            activityChart.data.datasets[4].data.push(entry.failed_auths || 0);
        });
        
        activityChart.update();
    } catch (e) {
        console.error("Failed to load history", e);
    }
}

function formatTimestamp(ts, timeframe) {
    const date = new Date(ts);
    if (timeframe === '1h' || timeframe === '6h') {
        return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    }
    return date.toLocaleDateString([], { month: 'short', day: 'numeric', hour: '2-digit' });
}

async function changeTimeframe(tf) {
    currentTimeframe = tf;
    
    // Update UI
    document.querySelectorAll('.timeframe-btn').forEach(btn => {
        if (btn.dataset.time === tf) {
            btn.classList.add('active', 'border-primary/50', 'text-primary', 'bg-primary/5');
            btn.classList.remove('border-white/5', 'bg-slate-800', 'text-slate-400');
        } else {
            btn.classList.remove('active', 'border-primary/50', 'text-primary', 'bg-primary/5');
            btn.classList.add('border-white/5', 'bg-slate-800', 'text-slate-400');
        }
    });
    
    await fetchHistory(tf);
    
    // Reset stream if viewing last hour, else maybe stop it to avoid confusion?
    // Actually, keep it running but only add to chart if it's the current timeframe
}

function startStatsStream() {
    if (currentStatsSource) return;
    
    fetchHistory(currentTimeframe);
    
    currentStatsSource = new EventSource('/api/stats/stream');
    currentStatsSource.onmessage = (event) => {
        const entry = JSON.parse(event.data);
        
        // Only append to chart if we are in "recent" view (1h)
        // Otherwise, it might look weird to add a single point to a 30-day chart
        if (currentTimeframe !== '1h') return;

        const time = formatTimestamp(entry.timestamp, currentTimeframe);
        
        // Match keeping logic (e.g. last 60 points for 1h if interval is 1m)
        if (activityChart.data.labels.length > 60) {
            activityChart.data.labels.shift();
            activityChart.data.datasets.forEach(ds => ds.data.shift());
        }
        
        activityChart.data.labels.push(time);
        activityChart.data.datasets[0].data.push(entry.messages);
        activityChart.data.datasets[1].data.push(entry.ai_requests);
        activityChart.data.datasets[2].data.push(entry.user_count);
        activityChart.data.datasets[3].data.push(entry.admin_commands || 0);
        activityChart.data.datasets[4].data.push(entry.failed_auths || 0);

        if (entry.admin_nicknames) {
            updateAdminsUI(entry.admin_nicknames, entry.channel_admins);
        }
        
        activityChart.update('none'); // Update without animation for smooth streaming
    };
}

function stopStatsStream() {
    if (currentStatsSource) {
        currentStatsSource.close();
        currentStatsSource = null;
    }
}

// Bookmarks Logic
let bookmarkPage = 1;
let totalBookmarkPages = 1;

function updateBookmarksUI(show) {
    const btn = document.getElementById('bookmarks-toggle');
    const indicator = document.getElementById('bookmarks-indicator');
    const container = document.getElementById('bookmarks-container');

    if (show) {
        btn.classList.add('border-accent/50', 'text-white', 'bg-accent/5');
        btn.classList.remove('border-white/10', 'text-slate-400');
        indicator.classList.add('bg-accent', 'animate-pulse');
        indicator.classList.remove('bg-slate-500');
        container.classList.remove('hidden');
        fetchBookmarks(1);
    } else {
        btn.classList.remove('border-accent/50', 'text-white', 'bg-accent/5');
        btn.classList.add('border-white/10', 'text-slate-400');
        indicator.classList.remove('bg-accent', 'animate-pulse');
        indicator.classList.add('bg-slate-500');
        container.classList.add('hidden');
    }
}

function toggleBookmarks() {
    const container = document.getElementById('bookmarks-container');
    const isHidden = container.classList.contains('hidden');
    updateBookmarksUI(isHidden);
}

async function fetchBookmarks(page) {
    bookmarkPage = page;
    const query = document.getElementById('bookmark-search')?.value || '';
    try {
        const res = await fetch(`/api/bookmarks?page=${page}&q=${encodeURIComponent(query)}`);
        if (!res.ok) throw new Error('Bookmarks fetch failed');
        const data = await res.json();
        
        totalBookmarkPages = data.total_pages;
        document.getElementById('bookmarks-count').textContent = `${data.total_count} items`;
        
        const list = document.getElementById('bookmarks-list');
        list.innerHTML = '';
        
        if (data.bookmarks && data.bookmarks.length > 0) {
            data.bookmarks.forEach(b => {
                const tr = document.createElement('tr');
                tr.className = 'border-b border-white/5 hover:bg-white/5 transition-colors group';
                
                const date = new Date(b.timestamp).toLocaleString([], { 
                    month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' 
                });

                // Shorten URL for display if too long
                let displayUrl = b.url;
                if (displayUrl.length > 50) {
                    displayUrl = displayUrl.substring(0, 47) + '...';
                }

                tr.innerHTML = `
                    <td class="py-4 text-slate-300 font-bold">${b.nickname}</td>
                    <td class="py-4 text-slate-500 text-[10px]">${b.hostname}</td>
                    <td class="py-4">
                        <a href="${b.url}" target="_blank" class="text-primary hover:text-accent transition-colors flex items-center gap-2">
                            ${displayUrl}
                            <svg class="w-3 h-3 opacity-0 group-hover:opacity-100 transition-opacity" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"></path></svg>
                        </a>
                    </td>
                    <td class="py-4 text-right text-slate-500 text-[10px]">${date}</td>
                `;
                list.appendChild(tr);
            });
        } else {
            list.innerHTML = '<tr><td colspan="4" class="py-8 text-center text-slate-500 italic">No bookmarks found yet. Use !bookmark &lt;URL&gt; in IRC!</td></tr>';
        }
        
        // Update pagination buttons
        document.getElementById('prev-page').disabled = bookmarkPage <= 1;
        document.getElementById('next-page').disabled = bookmarkPage >= totalBookmarkPages;
        
    } catch (e) {
        console.error("Failed to load bookmarks", e);
    }
}

function changeBookmarkPage(delta) {
    const newPage = bookmarkPage + delta;
    if (newPage >= 1 && newPage <= totalBookmarkPages) {
        fetchBookmarks(newPage);
    }
}

let searchTimeout = null;
function debounceSearch() {
    if (searchTimeout) clearTimeout(searchTimeout);
    searchTimeout = setTimeout(() => {
        fetchBookmarks(1);
    }, 300);
}

// Administrators UI Update
function updateAdminsUI(nicks, chans) {
    const container = document.getElementById('admins-container');
    const globalList = document.getElementById('admins-list');
    const presenceList = document.getElementById('admins-by-channel');

    if (!nicks || nicks.length === 0) {
        container.classList.add('hidden');
        return;
    }

    container.classList.remove('hidden');
    globalList.innerHTML = '';
    nicks.forEach(nick => {
        const span = document.createElement('span');
        span.className = 'px-3 py-1 bg-accent/20 text-accent border border-accent/30 rounded-md mono text-[10px] font-bold';
        span.textContent = nick;
        globalList.appendChild(span);
    });

    presenceList.innerHTML = '';
    if (chans) {
        Object.entries(chans).forEach(([channel, admins]) => {
            const card = document.createElement('div');
            card.className = 'bg-slate-900/50 border border-white/5 rounded-lg p-3';
            card.innerHTML = `
                <div class="mono text-xs font-bold text-primary mb-2">${channel}</div>
                <div class="flex flex-wrap gap-1">
                    ${admins.map(a => `<span class="text-[9px] px-1.5 py-0.5 bg-slate-800 rounded text-slate-300 border border-white/5">${a}</span>`).join('')}
                </div>
            `;
            presenceList.appendChild(card);
        });
    } else {
        presenceList.innerHTML = '<div class="text-[10px] text-slate-600 italic">No admins currently present in monitored channels.</div>';
    }
}

// Initial load
fetchStatus();
// Refresh status metadata every 30 seconds
setInterval(fetchStatus, 30000);
