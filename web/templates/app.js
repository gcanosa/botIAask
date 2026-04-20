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

// Initial load
fetchStatus();
// Refresh stats every 10 seconds
setInterval(fetchStatus, 10000);
