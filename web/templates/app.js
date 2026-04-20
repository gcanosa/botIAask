let currentEventSource = null;

async function fetchStatus() {
    try {
        const res = await fetch('/api/status');
        if (!res.ok) throw new Error('Status fetch failed');
        const data = await res.json();
        
        document.getElementById('uptime').textContent = data.uptime;
        document.getElementById('server').textContent = data.nickname + ' @ ' + data.server;
        document.getElementById('ai_requests').textContent = data.ai_requests || 0;
        document.getElementById('ai_model').textContent = data.ai_model;
        
        updateNewsStatus(data.rss_enabled);
        
        const badge = document.getElementById('status-badge');
        badge.textContent = 'Online';
        badge.classList.remove('bg-primary/10', 'text-primary');
        badge.classList.add('bg-green-500/10', 'text-green-500');

        const channelsContainer = document.getElementById('channels');
        channelsContainer.innerHTML = '';
        data.channels.forEach(ch => {
            const btn = document.createElement('button');
            btn.className = 'px-4 py-2 bg-slate-800 hover:bg-slate-700 text-primary border border-white/5 rounded-lg mono text-sm transition-all hover:scale-105 active:scale-95';
            btn.textContent = ch;
            btn.onclick = () => openLogs(ch);
            channelsContainer.appendChild(btn);
        });
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
    if (currentEventSource) {
        currentEventSource.close();
    }

    // Start new stream
    currentEventSource = new EventSource(`/api/logs/stream?channel=${encodeURIComponent(channel)}`);
    
    currentEventSource.onmessage = (event) => {
        const line = document.createElement('div');
        line.className = 'hover:bg-white/5 px-2 py-0.5 rounded transition-colors';
        
        // Basic IRC log colorizing (optional)
        let text = event.data;
        if (text.includes('[MESSAGE]')) line.classList.add('text-slate-200');
        else if (text.includes('[JOIN]')) line.classList.add('text-green-400/80');
        else if (text.includes('[PART]')) line.classList.add('text-red-400/80');
        else if (text.includes('[ACTION]')) line.classList.add('text-accent');
        
        line.textContent = text;
        output.appendChild(line);
        output.scrollTop = output.scrollHeight;
    };

    currentEventSource.onerror = (err) => {
        console.error("SSE error", err);
        const errDiv = document.createElement('div');
        errDiv.className = 'text-red-400 text-xs italic bg-red-400/5 p-2 rounded mt-2';
        errDiv.textContent = "Connection to log stream lost. Retrying...";
        output.appendChild(errDiv);
    };
}

function closeLogs() {
    if (currentEventSource) {
        currentEventSource.close();
        currentEventSource = null;
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

// Initial load
fetchStatus();
// Refresh stats every 10 seconds
setInterval(fetchStatus, 10000);
