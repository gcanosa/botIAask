let currentLogSource = null;
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
    } catch (e) {
        console.error("Failed to load status", e);
        const badge = document.getElementById('status-badge');
        badge.textContent = 'Offline';
        badge.classList.add('bg-red-500/10', 'text-red-500');
    }
}

function openLogs(channel) {
    const container = document.getElementById('log-container');
    const output = document.getElementById('log-output');
    const title = document.getElementById('current-channel-title');
    
    container.classList.remove('hidden');
    title.textContent = channel;
    output.innerHTML = '<div class="text-slate-500 italic">Initializing stream...</div>';

    // Close existing stream
    if (currentLogSource) {
        currentLogSource.close();
    }

    // Start new stream
    currentLogSource = new EventSource(`/api/logs/stream?channel=${encodeURIComponent(channel)}`);
    
    currentLogSource.onmessage = (event) => {
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

    currentLogSource.onerror = (err) => {
        console.error("SSE error", err);
        const errDiv = document.createElement('div');
        errDiv.className = 'text-red-400 text-xs italic bg-red-400/5 p-2 rounded mt-2';
        errDiv.textContent = "Connection to log stream lost. Retrying...";
        output.appendChild(errDiv);
    };
}

function closeLogs() {
    if (currentLogSource) {
        currentLogSource.close();
        currentLogSource = null;
    }
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
                    fill: true
                },
                {
                    label: 'AI Requests',
                    data: [],
                    borderColor: '#c084fc',
                    backgroundColor: 'rgba(192, 132, 252, 0.1)',
                    borderWidth: 2,
                    tension: 0.4,
                    fill: true
                },
                {
                    label: 'Users',
                    data: [],
                    borderColor: '#22c55e',
                    backgroundColor: 'rgba(34, 197, 94, 0.1)',
                    borderWidth: 2,
                    tension: 0.4,
                    fill: true
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
                    ticks: { color: '#94a3b8', font: { family: 'JetBrains Mono' } }
                },
                x: {
                    grid: { display: false },
                    ticks: { color: '#94a3b8', font: { family: 'JetBrains Mono' } }
                }
            },
            plugins: {
                legend: { display: false }
            },
            interaction: {
                intersect: false,
                mode: 'index'
            },
            animation: {
                duration: 1000,
                easing: 'easeOutQuart'
            }
        }
    });
}

function startStatsStream() {
    if (currentStatsSource) return;
    initChart();
    
    currentStatsSource = new EventSource('/api/stats/stream');
    currentStatsSource.onmessage = (event) => {
        const entry = JSON.parse(event.data);
        const time = new Date(entry.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
        
        // Keep last 30 entries
        if (activityChart.data.labels.length > 30) {
            activityChart.data.labels.shift();
            activityChart.data.datasets.forEach(ds => ds.data.shift());
        }
        
        activityChart.data.labels.push(time);
        activityChart.data.datasets[0].data.push(entry.messages);
        activityChart.data.datasets[1].data.push(entry.ai_requests);
        activityChart.data.datasets[2].data.push(entry.user_count);
        
        activityChart.update();
    };
}

function stopStatsStream() {
    if (currentStatsSource) {
        currentStatsSource.close();
        currentStatsSource = null;
    }
}

// Initial load
fetchStatus();
// Refresh status metadata every 30 seconds
setInterval(fetchStatus, 30000);
