// MVD Analyzer Dashboard — Pure client-side via WASM

let currentResult = null;

// ─── WASM Worker ────────────────────────────────────────────────────────────

const worker = new Worker('worker.js');
let wasmReady = false;
let analyzeResolve = null;
let analyzeReject = null;

worker.onmessage = (e) => {
    if (e.data.type === 'ready') {
        wasmReady = true;
        const overlay = document.getElementById('wasm-loading');
        if (overlay) overlay.style.display = 'none';
    } else if (e.data.type === 'result') {
        if (analyzeResolve) {
            analyzeResolve(e.data.json);
            analyzeResolve = null;
            analyzeReject = null;
        }
    } else if (e.data.type === 'error') {
        if (analyzeReject) {
            analyzeReject(new Error(e.data.message));
            analyzeResolve = null;
            analyzeReject = null;
        }
    }
};

function analyzeInWorker(bytes, filename) {
    return new Promise((resolve, reject) => {
        analyzeResolve = resolve;
        analyzeReject = reject;
        // Transfer the ArrayBuffer (zero-copy)
        worker.postMessage(
            { type: 'analyze', bytes: bytes.buffer, filename },
            [bytes.buffer]
        );
    });
}

// ─── QuakeWorld Hub Client (JS) ─────────────────────────────────────────────

const SUPABASE_URL = 'https://ncsphkjfominimxztjip.supabase.co/rest/v1/v1_games';
const SUPABASE_API_KEY = 'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6Im5jc3Boa2pmb21pbmlteHp0amlwIiwicm9sZSI6ImFub24iLCJpYXQiOjE2OTY5Mzg1NjMsImV4cCI6MjAxMjUxNDU2M30.NN6hjlEW-qB4Og9hWAVlgvUdwrbBO13s8OkAJuBGVbo';

function parseGameId(input) {
    input = input.trim();
    const asNum = parseInt(input, 10);
    if (!isNaN(asNum) && String(asNum) === input) return asNum;
    try {
        const url = new URL(input);
        const gid = url.searchParams.get('gameId');
        if (gid) return parseInt(gid, 10);
    } catch {}
    const match = input.match(/gameId=(\d+)/);
    if (match) return parseInt(match[1], 10);
    throw new Error('Could not parse game ID from: ' + input);
}

async function fetchGameFromHub(gameId) {
    const resp = await fetch(`${SUPABASE_URL}?select=*&id=eq.${gameId}`, {
        headers: {
            'apikey': SUPABASE_API_KEY,
            'Authorization': `Bearer ${SUPABASE_API_KEY}`,
            'accept-profile': 'public'
        }
    });
    if (!resp.ok) throw new Error(`Hub API error: ${resp.status}`);
    const games = await resp.json();
    if (games.length === 0) throw new Error(`Game ID ${gameId} not found`);
    return games[0];
}

async function downloadDemoFromHub(game) {
    const sha = game.demo_sha256;
    // Try CDN first
    if (sha && sha.length >= 3) {
        const cdnUrl = `https://d.quake.world/${sha.slice(0,3)}/${sha}.mvd.gz`;
        try {
            const resp = await fetch(cdnUrl);
            if (resp.ok) return new Uint8Array(await resp.arrayBuffer());
        } catch {}
    }
    // Fallback to direct server URL
    if (game.demo_source_url) {
        const resp = await fetch(game.demo_source_url);
        if (resp.ok) return new Uint8Array(await resp.arrayBuffer());
        throw new Error('Failed to download demo from server');
    }
    throw new Error('No download URL available for this game');
}

function generateDemoFilename(game) {
    const teams = (game.teams || []).map(t => (t.name || '').replace(/[^a-zA-Z0-9_-]/g, '_'));
    const teamsStr = teams.join('_vs_') || 'unknown';
    const mapName = (game.map || 'unknown').replace(/[^a-zA-Z0-9_-]/g, '_');
    const ts = new Date(game.timestamp).toISOString().replace(/[-:T]/g, '').slice(0, 13);
    return `${game.mode || 'unknown'}_${teamsStr}[${mapName}]${ts}.mvd.gz`;
}

// ─── Setup ──────────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
    setupFileUpload();
    setupTabs();
});

function setupFileUpload() {
    const dropZone = document.getElementById('drop-zone');
    const fileInput = document.getElementById('file-input');

    fileInput.addEventListener('change', (e) => {
        if (e.target.files.length > 0) {
            uploadFile(e.target.files[0]);
        }
    });

    dropZone.addEventListener('dragover', (e) => {
        e.preventDefault();
        dropZone.classList.add('dragover');
    });

    dropZone.addEventListener('dragleave', () => {
        dropZone.classList.remove('dragover');
    });

    dropZone.addEventListener('drop', (e) => {
        e.preventDefault();
        dropZone.classList.remove('dragover');
        if (e.dataTransfer.files.length > 0) {
            uploadFile(e.dataTransfer.files[0]);
        }
    });
}

function setupTabs() {
    const tabButtons = document.querySelectorAll('.tab-btn');
    tabButtons.forEach(btn => {
        btn.addEventListener('click', () => {
            const tabName = btn.dataset.tab;
            tabButtons.forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
            document.getElementById(`tab-${tabName}`).classList.add('active');

            // Stop playback on tab switch
            if (mapState.isPlaying) {
                mapState.isPlaying = false;
                if (mapState.animationFrameId) {
                    cancelAnimationFrame(mapState.animationFrameId);
                    mapState.animationFrameId = null;
                }
                const mapBtn = document.getElementById('map-play-pause');
                if (mapBtn) mapBtn.textContent = '▶';
            }

            // Sync current time between tabs
            if (tabName === 'map') {
                const mapSlider = document.getElementById('map-timeline-slider');
                if (mapSlider) mapSlider.value = mapState.currentTime;
                renderMap(mapState.currentTime);
                const mapTimeDisplay = document.getElementById('map-current-time');
                if (mapTimeDisplay) {
                    const matchStart = timelineState.matchStartTime;
                    mapTimeDisplay.textContent = formatTime(Math.max(0, mapState.currentTime - matchStart));
                }
            } else if (tabName === 'timeline') {
                updateTimelineCursor();
                updateTimelineTimeDisplay();
                updateTimeIndicators();
                snapMessagesToCurrentTime();
            }
        });
    });
}

// ─── File Upload (via WASM Worker) ──────────────────────────────────────────

async function uploadFile(file) {
    const status = document.getElementById('upload-status');
    status.textContent = 'Analyzing...';
    status.className = 'status loading';

    try {
        if (!wasmReady) throw new Error('Analyzer is still loading, please wait...');

        const buffer = await file.arrayBuffer();
        const bytes = new Uint8Array(buffer);
        const jsonStr = await analyzeInWorker(bytes, file.name);
        const result = JSON.parse(jsonStr);
        if (result.error) throw new Error(result.error);

        status.textContent = 'Analysis complete!';
        status.className = 'status success';
        currentResult = result;
        displayResults(result);
    } catch (error) {
        status.textContent = 'Error: ' + error.message;
        status.className = 'status error';
    }
}

// ─── Hub Loading (JS fetch + WASM Worker) ───────────────────────────────────

async function loadFromHub() {
    const input = document.getElementById('hub-input').value.trim();
    if (!input) {
        alert('Please enter a game ID or URL');
        return;
    }

    const status = document.getElementById('upload-status');
    const btn = document.getElementById('hub-load-btn');
    status.textContent = 'Fetching from QuakeWorld Hub...';
    status.className = 'status loading';
    btn.disabled = true;

    try {
        if (!wasmReady) throw new Error('Analyzer is still loading, please wait...');

        const gameId = parseGameId(input);

        status.textContent = 'Fetching game info...';
        const game = await fetchGameFromHub(gameId);

        status.textContent = 'Downloading demo...';
        const demoBytes = await downloadDemoFromHub(game);

        status.textContent = 'Analyzing...';
        const filename = generateDemoFilename(game);
        const jsonStr = await analyzeInWorker(demoBytes, filename);
        const result = JSON.parse(jsonStr);
        if (result.error) throw new Error(result.error);

        status.textContent = 'Analysis complete!';
        status.className = 'status success';
        currentResult = result;

        // Attach hub info for viewer links
        currentResult.hubInfo = {
            gameId: gameId,
            viewerUrl: `https://hub.quakeworld.nu/games/?gameId=${gameId}`,
            players: game.players
        };

        displayResults(result);
    } catch (error) {
        status.textContent = 'Error: ' + error.message;
        status.className = 'status error';
    } finally {
        btn.disabled = false;
    }
}

function displayResults(result) {
    // Reset timeline state before loading new demo
    resetTimelineState();

    document.getElementById('results-section').style.display = 'block';

    const demoInfo = result.demoInfo;

    // Match info from demoInfo
    if (demoInfo) {
        document.getElementById('map-name').textContent = demoInfo.map || result.match?.map || '-';
        document.getElementById('duration').textContent = formatDuration(demoInfo.duration || result.duration);
        document.getElementById('mode').textContent = demoInfo.mode || '-';
        document.getElementById('hostname').textContent = demoInfo.hostname || '-';
        document.getElementById('match-date').textContent = demoInfo.date || '-';
    } else if (result.match) {
        document.getElementById('map-name').textContent = result.match.map || '-';
        document.getElementById('duration').textContent = formatDuration(result.duration);
    }

    // Teams from demoInfo
    if (demoInfo && demoInfo.teams) {
        displayTeamsFromDemoInfo(demoInfo);
    } else if (result.match && result.match.teams) {
        displayTeams(result.match.teams);
    }

    // Player stats from demoInfo
    if (demoInfo && demoInfo.players) {
        displayPlayerStats(demoInfo.players);
        displayWeaponStatsTable(demoInfo.players);
        displayItemsTable(demoInfo.players);
        displayPerformanceTable(demoInfo.players);
    } else if (result.frags && result.frags.byPlayer) {
        displayScoreboardFallback(result.frags.byPlayer, result.match ? result.match.players : []);
    }

    // Weapons chart from frags
    if (result.frags && result.frags.byWeapon) {
        displayWeaponsChart(result.frags.byWeapon);
    }

    // Timeline Analysis (new graphical view)
    if (result.timelineAnalysis || result.messages?.events) {
        displayTimelineAnalysis(result);
    }

    // Key Moments (powerup runs)
    if (result.timelineAnalysis?.powerupEvents) {
        displayKeyMoments(result);
    }

    // Map View
    if (result.timelineAnalysis) {
        initMapView(result);
    }
}

function displayTeamsFromDemoInfo(demoInfo) {
    const container = document.getElementById('teams-list');
    container.innerHTML = '';

    // Calculate team scores from players
    const teamScores = {};
    for (const player of demoInfo.players || []) {
        const team = player.team || 'unknown';
        if (!teamScores[team]) {
            teamScores[team] = 0;
        }
        teamScores[team] += player.stats?.frags || 0;
    }

    // Sort by score
    const sorted = Object.entries(teamScores).sort((a, b) => b[1] - a[1]);

    sorted.forEach(([name, frags]) => {
        const div = document.createElement('div');
        div.className = 'team-item';
        div.innerHTML = `
            <span class="team-name">${escapeHtml(name)}</span>
            <span class="team-frags">${frags} frags</span>
        `;
        container.appendChild(div);
    });
}

function displayTeams(teams) {
    const container = document.getElementById('teams-list');
    container.innerHTML = '';

    const sorted = [...teams].sort((a, b) => b.frags - a.frags);

    sorted.forEach(team => {
        const div = document.createElement('div');
        div.className = 'team-item';
        div.innerHTML = `
            <span class="team-name">${escapeHtml(team.name)}</span>
            <span class="team-frags">${team.frags} frags</span>
        `;
        container.appendChild(div);
    });
}

function displayPlayerStats(players) {
    const tbody = document.getElementById('scoreboard-body');
    tbody.innerHTML = '';

    // Sort by frags
    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));

    // Determine team ordering for color assignment
    const teamOrder = [];
    sorted.forEach(p => {
        const t = p.team || '';
        if (t && !teamOrder.includes(t)) teamOrder.push(t);
    });

    sorted.forEach(player => {
        const tr = document.createElement('tr');
        const teamIdx = teamOrder.indexOf(player.team || '');
        const teamColors = ['#ff5050', '#50a0ff', '#4ecdc4', '#ffc107'];
        if (teamIdx >= 0 && teamIdx < teamColors.length) {
            tr.style.borderLeft = `3px solid ${teamColors[teamIdx]}`;
        }
        tr.innerHTML = `
            <td>${escapeHtml(player.name)}</td>
            <td>${escapeHtml(player.team || '')}</td>
            <td>${player.stats?.frags || 0}</td>
            <td>${player.stats?.deaths || 0}</td>
            <td>${player.stats?.tk || 0}</td>
            <td>${player.dmg?.given || 0}</td>
            <td>${player.dmg?.taken || 0}</td>
            <td>${player.ping || 0}</td>
        `;
        tbody.appendChild(tr);
    });
}

function displayWeaponStatsTable(players) {
    const tbody = document.getElementById('weapon-stats-body');
    tbody.innerHTML = '';

    const sorted = [...players].sort((a, b) => (b.dmg?.given || 0) - (a.dmg?.given || 0));

    sorted.forEach(player => {
        const w = player.weapons || {};
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td>${escapeHtml(player.name)}</td>
            <td>${formatWeaponCell(w.sg)}</td>
            <td>${formatWeaponCell(w.ssg)}</td>
            <td>${formatWeaponCell(w.ng)}</td>
            <td>${formatWeaponCell(w.sng)}</td>
            <td>${formatWeaponCell(w.gl)}</td>
            <td>${formatWeaponCell(w.rl)}</td>
            <td>${formatWeaponCell(w.lg)}</td>
        `;
        tbody.appendChild(tr);
    });
}

function formatWeaponCell(weapon) {
    if (!weapon) return '-';

    const parts = [];

    // Accuracy
    if (weapon.acc && weapon.acc.attacks > 0) {
        const acc = ((weapon.acc.hits / weapon.acc.attacks) * 100).toFixed(1);
        parts.push(`<span class="${getAccuracyClass(parseFloat(acc))}">${acc}%</span>`);
    }

    // Kills
    const kills = weapon.kills?.total || weapon.kills?.enemy || 0;
    if (kills > 0) {
        parts.push(`<span class="weapon-kills">${kills}k</span>`);
    }

    // Damage
    const dmg = weapon.damage?.enemy || 0;
    if (dmg > 0) {
        parts.push(`<span class="weapon-dmg">${dmg}d</span>`);
    }

    return parts.length > 0 ? parts.join(' ') : '-';
}

function displayItemsTable(players) {
    const tbody = document.getElementById('items-body');
    tbody.innerHTML = '';

    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));

    sorted.forEach(player => {
        const items = player.items || {};
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td>${escapeHtml(player.name)}</td>
            <td>${items.ra?.took || 0}</td>
            <td>${items.ya?.took || 0}</td>
            <td>${items.ga?.took || 0}</td>
            <td>${items.health_100?.took || 0}</td>
            <td>${formatPowerup(items.q)}</td>
            <td>${formatPowerup(items.p)}</td>
            <td>${formatPowerup(items.r)}</td>
        `;
        tbody.appendChild(tr);
    });
}

function formatPowerup(item) {
    if (!item || !item.took) return '0';
    if (item.time) {
        return `${item.took} (${item.time}s)`;
    }
    return `${item.took}`;
}

function displayPerformanceTable(players) {
    const tbody = document.getElementById('performance-body');
    tbody.innerHTML = '';

    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));

    sorted.forEach(player => {
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td>${escapeHtml(player.name)}</td>
            <td>${player.spree?.max || 0}</td>
            <td>${player.spree?.quad || 0}</td>
            <td>${player.speed?.avg ? player.speed.avg.toFixed(0) : '-'}</td>
            <td>${player.speed?.max ? player.speed.max.toFixed(0) : '-'}</td>
            <td>${player.stats?.['spawn-frags'] || 0}</td>
        `;
        tbody.appendChild(tr);
    });
}

function displayScoreboardFallback(byPlayer, players) {
    const tbody = document.getElementById('scoreboard-body');
    tbody.innerHTML = '';

    const playerData = [];
    for (const [name, stats] of Object.entries(byPlayer)) {
        if (name.includes("'s quad") || name === 'teammate' || name === 'his teammate') {
            continue;
        }

        const playerInfo = players.find(p => p.name === name);
        playerData.push({
            name: name,
            team: playerInfo ? playerInfo.team : '',
            frags: playerInfo ? playerInfo.frags : (stats.kills - stats.deaths),
            deaths: stats.deaths,
            tk: 0,
            dmgGiven: 0,
            dmgTaken: 0,
            ping: 0
        });
    }

    playerData.sort((a, b) => b.frags - a.frags);

    playerData.forEach(player => {
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td>${escapeHtml(player.name)}</td>
            <td>${escapeHtml(player.team)}</td>
            <td>${player.frags}</td>
            <td>${player.deaths}</td>
            <td>${player.tk}</td>
            <td>${player.dmgGiven}</td>
            <td>${player.dmgTaken}</td>
            <td>${player.ping}</td>
        `;
        tbody.appendChild(tr);
    });
}

function displayWeaponsChart(byWeapon) {
    const container = document.getElementById('weapons-chart');
    container.innerHTML = '';

    const sorted = Object.entries(byWeapon).sort((a, b) => b[1] - a[1]);
    const max = sorted.length > 0 ? sorted[0][1] : 1;

    sorted.forEach(([weapon, count]) => {
        const div = document.createElement('div');
        div.className = 'weapon-bar';
        const percentage = (count / max) * 100;
        div.innerHTML = `
            <span class="weapon-name">${getWeaponName(weapon)}</span>
            <div class="bar-container">
                <div class="bar" style="width: ${percentage}%"></div>
            </div>
            <span class="weapon-count">${count}</span>
        `;
        container.appendChild(div);
    });
}

function displayTimeline(events, teams) {
    const container = document.getElementById('timeline-body');
    container.innerHTML = '';

    const teamNames = Array.isArray(teams) ? teams : teams.map(t => t.name);
    if (teamNames.length >= 2) {
        document.getElementById('team1-header').textContent = teamNames[0];
        document.getElementById('team2-header').textContent = teamNames[1];
    }

    const relevantEvents = events.filter(e =>
        e.type === 'frag' || e.type === 'chat' || e.type === 'teamsay'
    ).slice(0, 500);

    relevantEvents.forEach(event => {
        const row = document.createElement('div');
        row.className = 'timeline-row';

        const isTeam1 = teamNames.length >= 1 && event.team === teamNames[0];
        const isTeam2 = teamNames.length >= 2 && event.team === teamNames[1];

        const eventHtml = `<span class="timeline-event ${event.type}">${escapeHtml(event.message)}</span>`;

        row.innerHTML = `
            <div class="timeline-left">${isTeam1 ? eventHtml : ''}</div>
            <div class="timeline-time">${formatTime(event.time)}</div>
            <div class="timeline-right">${isTeam2 ? eventHtml : ''}</div>
        `;

        container.appendChild(row);
    });
}

function getAccuracyClass(acc) {
    if (acc >= 40) return 'accuracy-high';
    if (acc >= 25) return 'accuracy-medium';
    return 'accuracy-low';
}

function displayKeyMoments(result) {
    const tbody = document.getElementById('keymoments-body');
    const emptyMsg = document.getElementById('keymoments-empty');
    tbody.innerHTML = '';

    const powerupEvents = result.timelineAnalysis?.powerupEvents || [];
    const matchStartTime = result.timelineAnalysis?.matchStartTime || 0;

    if (powerupEvents.length === 0) {
        emptyMsg.style.display = 'block';
        return;
    }
    emptyMsg.style.display = 'none';

    // Get hub info for viewer links (from currentResult which may have hubInfo set)
    const hubInfo = currentResult?.hubInfo;

    powerupEvents.forEach(event => {
        const tr = document.createElement('tr');

        // Calculate match-relative time for display
        const relTime = Math.max(0, event.time - matchStartTime);

        // Build viewer URL if hub info available
        let watchCell = '-';
        if (hubInfo && hubInfo.gameId) {
            // Use raw demo time for Hub viewer (includes countdown period)
            // Demo time = event.time (already in demo time, not match-relative)
            const fromTime = Math.max(0, Math.floor(event.time) - 10);
            const toTime = Math.floor(event.endTime) + 5;

            // Use playerUserID from MVD demo data - this is what Hub viewer expects for track param
            // Falls back to playerSlot if UserID is 0 (shouldn't happen in well-formed demos)
            const trackId = event.playerUserID || event.playerSlot;

            const viewerUrl = `https://hub.quakeworld.nu/games/?gameId=${hubInfo.gameId}&from=${fromTime}&to=${toTime}&track=${trackId}`;
            watchCell = `<a href="${viewerUrl}" target="_blank" class="viewer-link">Watch</a>`;
        }

        // Powerup display with color
        const powerupDisplay = getPowerupDisplay(event.powerupType);

        tr.innerHTML = `
            <td class="time-cell">${formatTime(relTime)}</td>
            <td class="powerup-cell ${event.powerupType}">${powerupDisplay}</td>
            <td>${escapeHtml(event.playerName || 'Unknown')}</td>
            <td>${escapeHtml(event.team || '-')}</td>
            <td>${Math.round(event.duration)}s</td>
            <td>${watchCell}</td>
        `;
        tbody.appendChild(tr);
    });
}

function getPowerupDisplay(type) {
    switch(type) {
        case 'quad': return 'Quad';
        case 'pent': return 'Pent';
        case 'ring': return 'Ring';
        default: return type;
    }
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

function formatDuration(seconds) {
    const mins = Math.floor(seconds / 60);
    const secs = Math.floor(seconds % 60);
    return `${mins}:${secs.toString().padStart(2, '0')}`;
}

function formatTime(seconds) {
    return formatDuration(seconds);
}

function getWeaponName(code) {
    const names = {
        'rl': 'Rocket Launcher',
        'lg': 'Lightning Gun',
        'gl': 'Grenade Launcher',
        'ssg': 'Super Shotgun',
        'sg': 'Shotgun',
        'sng': 'Super Nailgun',
        'ng': 'Nailgun',
        'axe': 'Axe',
        'tele': 'Telefrag',
        'suicide': 'Suicide',
        'teamkill': 'Team Kill',
        'fall': 'Fall',
        'water': 'Drowning',
        'lava': 'Lava',
        'slime': 'Slime'
    };
    return names[code] || code;
}

function escapeHtml(text) {
    if (!text) return '';
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// Escape a single character for HTML
function escapeHtmlChar(char) {
    switch (char) {
        case '<': return '&lt;';
        case '>': return '&gt;';
        case '&': return '&amp;';
        case '"': return '&quot;';
        case "'": return '&#39;';
        default: return char;
    }
}

// Format Quake chat messages with colors
// Based on ezQuake source code character rendering:
// - Characters 0-127: Normal white text
// - Characters 128-255: "Gold/Brown" alternate text (same glyphs as 0-127)
// - &cRGB: Set color to RGB (hex digits 0-F, each multiplied by 16)
// - &r: Reset color to white
function formatQuakeMessage(text) {
    if (!text) return '';

    // Remove sound triggers at end of messages (!K, !H, !G, !C, etc.)
    let result = text.replace(/![A-Z]$/g, '');

    let output = '';
    let currentColor = null;
    let i = 0;

    while (i < result.length) {
        const charCode = result.charCodeAt(i);

        // Check for &cRGB color code
        if (result.slice(i, i + 2) === '&c') {
            // Check for &cfff (white/reset)
            if (result.slice(i, i + 5).toLowerCase() === '&cfff') {
                if (currentColor) output += '</span>';
                currentColor = null;
                i += 5;
                continue;
            }
            // Check for 3-digit hex color
            const colorMatch = result.slice(i + 2, i + 5).match(/^[0-9a-fA-F]{3}/);
            if (colorMatch) {
                if (currentColor) output += '</span>';
                // ezQuake uses r * 16 for each hex digit (0-240 range)
                const r = parseInt(colorMatch[0][0], 16) * 16;
                const g = parseInt(colorMatch[0][1], 16) * 16;
                const b = parseInt(colorMatch[0][2], 16) * 16;
                currentColor = `rgb(${r},${g},${b})`;
                output += `<span style="color:${currentColor}">`;
                i += 5;
                continue;
            }
        }

        // Check for &r reset
        if (result.slice(i, i + 2) === '&r') {
            if (currentColor) output += '</span>';
            currentColor = null;
            i += 2;
            continue;
        }

        // High-bit gold characters (128-255)
        if (charCode >= 128 && charCode <= 255) {
            const baseChar = String.fromCharCode(charCode - 128);
            if (currentColor === null) {
                output += `<span class="quake-gold">${escapeHtmlChar(baseChar)}</span>`;
            } else {
                output += escapeHtmlChar(baseChar);
            }
            i++;
            continue;
        }

        // Skip macro delimiters (curly braces and square brackets)
        // These are Quake client markup, not displayed text
        if (result[i] === '{' || result[i] === '}' ||
            result[i] === '[' || result[i] === ']') {
            i++;
            continue;
        }

        // Regular character
        output += escapeHtmlChar(result[i]);
        i++;
    }

    if (currentColor) output += '</span>';
    return output;
}

// Timeline Analysis State
let timelineState = {
    buckets: [],
    highResBuckets: [],    // High-res buckets for map (50ms)
    highResDuration: 0.05, // High-res bucket interval
    events: [],
    duration: 0,
    matchStartTime: 0,
    teams: [],
    overviewBucketSize: 5, // Aggregate to 5-second buckets for overview
    controlsInitialized: false, // Track if timeline control handlers are set up
    segment: null, // { start, end } or null for full match - selected time segment
    dragging: false, // Is user dragging to select a segment on overview?
    dragStartTime: 0 // Time at drag start
};

// Reset all timeline state for loading a new demo
function resetTimelineState() {
    timelineState.buckets = [];
    timelineState.highResBuckets = [];
    timelineState.highResDuration = 0.05;
    timelineState.events = [];
    timelineState.fragEvents = [];
    timelineState.duration = 0;
    timelineState.matchStartTime = 0;
    timelineState.teams = [];
    timelineState.segment = null;
    timelineState.dragging = false;

    // Clear all timeline graph containers
    const containers = [
        'timeline-nav-axis', 'detail-graph', 'detail-axis',
        'powerup-lines-top', 'powerup-lines-bottom',
        'health-armor-graph', 'health-axis', 'frags-graph', 'frags-axis',
        'score-graph', 'score-axis', 'kill-messages', 'team-a-messages', 'team-b-messages'
    ];
    containers.forEach(id => {
        const el = document.getElementById(id);
        if (el) el.innerHTML = '';
    });
}

function displayTimelineAnalysis(result) {
    const timeline = result.timelineAnalysis;
    const demoInfo = result.demoInfo;

    // Get teams
    let teams = [];
    if (demoInfo?.teams) {
        teams = demoInfo.teams;
    } else if (result.match?.teams) {
        teams = result.match.teams.map(t => t.name);
    }

    timelineState.buckets = timeline?.buckets || [];
    timelineState.highResBuckets = timeline?.highResBuckets || [];
    timelineState.highResDuration = timeline?.highResDuration || 0.05;
    timelineState.matchStartTime = timeline?.matchStartTime || 0;
    timelineState.duration = result.duration || 600;
    timelineState.teams = teams;
    timelineState.events = result.messages?.events || [];
    timelineState.fragEvents = timeline?.fragEvents || []; // Frag events from stat tracking

    // Set shared current time to match start (0:00 relative)
    mapState.currentTime = timelineState.matchStartTime;

    // Update legend team names
    if (teams.length >= 2) {
        const setTextIfExists = (id, text) => { const el = document.getElementById(id); if (el) el.textContent = text; };
        setTextIfExists('legend-team-a', teams[0] + ' ↑');
        setTextIfExists('legend-team-b', teams[1] + ' ↓');
        setTextIfExists('team-a-chat-title', `${teams[0]} Chat`);
        setTextIfExists('team-b-chat-title', `${teams[1]} Chat`);
        setTextIfExists('legend-health-team-a', teams[0] + ' ↑');
        setTextIfExists('legend-health-team-b', teams[1] + ' ↓');
    }

    setupTimelineControls();
    updateTimelineCursor();
    updateDetailView();
    updateTimeIndicators();
}

// Calculate optimal bin size based on selection duration
function getOptimalBinSize(selectionDuration) {
    if (selectionDuration <= 300) return 1;   // ≤5min: 1s bins
    if (selectionDuration <= 600) return 2;   // ≤10min: 2s bins
    if (selectionDuration <= 900) return 3;   // ≤15min: 3s bins
    if (selectionDuration <= 1200) return 4;  // ≤20min: 4s bins
    return 5;                                  // >20min: 5s bins
}

// Aggregate 1-second detail buckets into larger bins for graphs
function aggregateDetailBuckets(buckets, binSize, teams) {
    if (buckets.length === 0 || binSize <= 1) return buckets;

    const result = [];
    const startTime = buckets[0].startTime;
    const endTime = buckets[buckets.length - 1].endTime;

    for (let t = startTime; t < endTime; t += binSize) {
        const binBuckets = buckets.filter(b => b.startTime >= t && b.startTime < t + binSize);
        if (binBuckets.length === 0) continue;

        // Aggregate team data using max for player counts, average for health/armor
        const aggregated = {
            startTime: t,
            endTime: t + binSize,
            teamData: {}
        };

        for (const team of teams) {
            const teamBuckets = binBuckets.map(b => (b.teamData || {})[team] || {});

            // Aggregate armorByType - use max for each armor type count
            const armorByType = {};
            for (const tb of teamBuckets) {
                const abt = tb.armorByType || {};
                for (const armorType of ['ra', 'ya', 'ga']) {
                    if (abt[armorType]) {
                        armorByType[armorType] = Math.max(armorByType[armorType] || 0, abt[armorType]);
                    }
                }
            }

            aggregated.teamData[team] = {
                // Use max within bin for player counts (shows peak control)
                playersWithRL: Math.max(...teamBuckets.map(tb => tb.playersWithRL || 0)),
                playersWithLG: Math.max(...teamBuckets.map(tb => tb.playersWithLG || 0)),
                playersWithRLLG: Math.max(...teamBuckets.map(tb => tb.playersWithRLLG || 0)),
                playersWithQuad: Math.max(...teamBuckets.map(tb => tb.playersWithQuad || 0)),
                playersWithPent: Math.max(...teamBuckets.map(tb => tb.playersWithPent || 0)),
                playersWithRing: Math.max(...teamBuckets.map(tb => tb.playersWithRing || 0)),
                // Use average for health/armor totals
                totalHealth: Math.round(teamBuckets.reduce((sum, tb) => sum + (tb.totalHealth || 0), 0) / teamBuckets.length),
                totalArmor: Math.round(teamBuckets.reduce((sum, tb) => sum + (tb.totalArmor || 0), 0) / teamBuckets.length),
                // Preserve armor type breakdown
                armorByType: armorByType
            };
        }

        result.push(aggregated);
    }

    return result;
}

function setupTimelineControls() {
    if (timelineState.controlsInitialized) {
        updateTimelineCursor();
        updateTimelineTimeDisplay();
        renderTimelineNavAxis();
        return;
    }

    const bar = document.getElementById('timeline-nav-bar');
    const caret = document.getElementById('timeline-nav-caret');

    renderTimelineNavAxis();

    // --- Caret drag: sets current time ---
    let caretDragging = false;

    caret.addEventListener('mousedown', (e) => {
        caretDragging = true;
        e.preventDefault();
        e.stopPropagation();
    });

    document.addEventListener('mousemove', (e) => {
        if (caretDragging) {
            const time = navBarClickToTime(e);
            if (time === null) return;
            mapState.currentTime = Math.max(timelineState.matchStartTime, Math.min(time, timelineState.duration));
            updateTimelineCursor();
            updateTimelineTimeDisplay();
            updateTimeIndicators();
            snapMessagesToCurrentTime();
            const mapSlider = document.getElementById('map-timeline-slider');
            if (mapSlider) mapSlider.value = mapState.currentTime;
            return;
        }

        if (!timelineState.dragging) return;
        const time = navBarClickToTime(e);
        if (time === null) return;

        const start = Math.min(timelineState.dragStartTime, time);
        const end = Math.max(timelineState.dragStartTime, time);

        if (end - start > 2) {
            timelineState.segment = { start, end };
            updateSelectionOverlay();
            updateSegmentLabel();
        }
    });

    document.addEventListener('mouseup', (e) => {
        if (caretDragging) {
            caretDragging = false;
            return;
        }

        if (!timelineState.dragging) return;
        timelineState.dragging = false;

        const time = navBarClickToTime(e);
        if (time === null) return;

        const start = Math.min(timelineState.dragStartTime, time);
        const end = Math.max(timelineState.dragStartTime, time);

        if (end - start <= 2) {
            // Click on bar — set current time, clear segment
            mapState.currentTime = Math.max(timelineState.matchStartTime, Math.min(time, timelineState.duration));
            timelineState.segment = null;
            updateTimelineCursor();
            updateTimelineTimeDisplay();
            updateTimeIndicators();
            updateSelectionOverlay();
            updateSegmentLabel();
            snapMessagesToCurrentTime();
            const mapSlider = document.getElementById('map-timeline-slider');
            if (mapSlider) mapSlider.value = mapState.currentTime;
        } else {
            // Drag on bar complete — apply segment to detail views
            timelineState.segment = { start, end };
            updateSelectionOverlay();
            updateSegmentLabel();
            updateDetailView();
        }
    });

    // Drag on bar to select segment
    bar.addEventListener('mousedown', (e) => {
        const time = navBarClickToTime(e);
        if (time === null) return;

        timelineState.dragging = true;
        timelineState.dragStartTime = time;
        timelineState.segment = null;
        updateSelectionOverlay();
        updateSegmentLabel();

        e.preventDefault();
    });

    // Double-click bar to clear segment
    bar.addEventListener('dblclick', () => {
        timelineState.segment = null;
        updateSelectionOverlay();
        updateSegmentLabel();
        updateDetailView();
    });

    timelineState.controlsInitialized = true;
}

function navBarClickToTime(e) {
    const bar = document.getElementById('timeline-nav-bar');
    if (!bar) return null;
    const rect = bar.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const width = rect.width;
    if (width <= 0) return null;
    const frac = Math.max(0, Math.min(1, x / width));
    return timelineState.matchStartTime + frac * (timelineState.duration - timelineState.matchStartTime);
}

function updateSelectionOverlay() {
    const overlay = document.getElementById('timeline-selection-overlay');
    if (!overlay) return;

    if (!timelineState.segment) {
        overlay.style.display = 'none';
        return;
    }

    const matchStart = timelineState.matchStartTime;
    const duration = timelineState.duration;
    const range = duration - matchStart;
    if (range <= 0) return;

    const startPct = ((timelineState.segment.start - matchStart) / range) * 100;
    const endPct = ((timelineState.segment.end - matchStart) / range) * 100;

    overlay.style.display = 'block';
    overlay.style.left = `${startPct}%`;
    overlay.style.width = `${endPct - startPct}%`;
}

function updateSegmentLabel() {
    const label = document.getElementById('timeline-segment-label');
    if (!label) return;

    if (!timelineState.segment) {
        label.textContent = '';
        return;
    }

    const matchStart = timelineState.matchStartTime;
    const relStart = timelineState.segment.start - matchStart;
    const relEnd = timelineState.segment.end - matchStart;
    label.textContent = `${formatTime(Math.max(0, relStart))} – ${formatTime(Math.max(0, relEnd))}`;
}

function updateTimelineCursor() {
    const cursor = document.getElementById('timeline-nav-cursor');
    const caret = document.getElementById('timeline-nav-caret');

    const matchStart = timelineState.matchStartTime;
    const duration = timelineState.duration;
    const range = duration - matchStart;
    if (range <= 0) return;

    const pct = Math.max(0, Math.min(100, ((mapState.currentTime - matchStart) / range) * 100));
    if (cursor) cursor.style.left = `${pct}%`;
    if (caret) caret.style.left = `${pct}%`;
}

function updateTimelineTimeDisplay() {
    const display = document.getElementById('timeline-current-time');
    if (!display) return;
    const matchStart = timelineState.matchStartTime;
    const relTime = mapState.currentTime - matchStart;
    display.textContent = formatTime(Math.max(0, relTime));
}

function renderTimelineNavAxis() {
    const container = document.getElementById('timeline-nav-axis');
    if (!container) return;
    container.innerHTML = '';

    const matchStart = timelineState.matchStartTime;
    const duration = timelineState.duration;
    const matchDuration = duration - matchStart;
    if (matchDuration <= 0) return;

    const tickCount = Math.min(10, Math.max(4, Math.floor(matchDuration / 60)));
    for (let i = 0; i <= tickCount; i++) {
        const time = (matchDuration / tickCount) * i;
        const span = document.createElement('span');
        span.textContent = formatTime(time);
        container.appendChild(span);
    }
}

function updateTimeIndicators() {
    // Update nav bar cursor (always relative to full match)
    updateTimelineCursor();

    // Detail graphs show either the segment or the full match
    const seg = timelineState.segment;
    const rangeStart = seg ? seg.start : timelineState.matchStartTime;
    const rangeEnd = seg ? seg.end : timelineState.duration;
    const range = rangeEnd - rangeStart;

    const detailIndicators = [
        'detail-time-indicator',
        'health-time-indicator',
        'frags-time-indicator',
        'score-time-indicator'
    ];

    if (range <= 0) return;

    const pct = Math.max(0, Math.min(100, ((mapState.currentTime - rangeStart) / range) * 100));

    for (const id of detailIndicators) {
        const el = document.getElementById(id);
        if (el) {
            el.style.left = `calc(10px + (100% - 20px) * ${pct / 100})`;
        }
    }
}

function updateDetailView() {
    const matchStart = timelineState.matchStartTime;
    const duration = timelineState.duration;

    // Use segment if selected, otherwise full match
    const start = timelineState.segment ? timelineState.segment.start : matchStart;
    const end = timelineState.segment ? timelineState.segment.end : duration;

    // Show range in label
    const relStart = start - matchStart;
    const relEnd = end - matchStart;
    if (timelineState.segment) {
        document.getElementById('time-range-label').textContent =
            `(${formatTime(Math.max(0, relStart))} - ${formatTime(Math.max(0, relEnd))})`;
    } else {
        document.getElementById('time-range-label').textContent = '';
    }

    // Update all detail panels
    updateDetailMessages(start, end);
    updateDetailGraph(start, end);
    updateDetailAxis(start, end);
    updateHealthArmorGraph(start, end);
    updateFragsGraph(start, end);
    updateScoreTimeline(start, end);
}

function updateDetailMessages(startTime, endTime) {
    const killContainer = document.getElementById('kill-messages');
    const teamAContainer = document.getElementById('team-a-messages');
    const teamBContainer = document.getElementById('team-b-messages');

    killContainer.innerHTML = '';
    teamAContainer.innerHTML = '';
    teamBContainer.innerHTML = '';

    if (!currentResult?.messages?.events) {
        const emptyMsg = '<div style="color: #888; padding: 20px; text-align: center;">No events</div>';
        killContainer.innerHTML = emptyMsg;
        teamAContainer.innerHTML = emptyMsg;
        teamBContainer.innerHTML = emptyMsg;
        return;
    }

    const teams = timelineState.teams;
    const matchStart = timelineState.matchStartTime;

    // Filter events in time range
    const events = currentResult.messages.events.filter(e =>
        e.time >= startTime && e.time <= endTime
    );

    // Deduplicate (same message within 1 second)
    const seen = new Map();
    const deduped = events.filter(e => {
        const key = `${Math.floor(e.time)}:${e.message}`;
        if (seen.has(key)) return false;
        seen.set(key, true);
        return true;
    });

    let killCount = 0, teamACount = 0, teamBCount = 0;

    // Sort into three categories
    deduped.forEach(event => {
        const relTime = event.time - matchStart;
        const item = document.createElement('div');
        item.className = 'timeline-message-item';
        item.dataset.time = event.time;
        item.innerHTML = `
            <span class="timeline-message-time">${formatTime(Math.max(0, relTime))}</span>
            <span class="timeline-message-content ${event.type}">${formatQuakeMessage(event.message)}</span>
        `;

        if (event.type === 'frag') {
            if (killCount < 100) {
                killContainer.appendChild(item);
                killCount++;
            }
        } else if (event.type === 'teamsay' || event.type === 'chat') {
            if (teams.length >= 2 && event.team === teams[0]) {
                if (teamACount < 50) {
                    teamAContainer.appendChild(item);
                    teamACount++;
                }
            } else if (teams.length >= 2 && event.team === teams[1]) {
                if (teamBCount < 50) {
                    teamBContainer.appendChild(item);
                    teamBCount++;
                }
            }
        }
    });

    if (killCount === 0) {
        killContainer.innerHTML = '<div style="color: #888; padding: 10px; text-align: center;">No kills</div>';
    }
    if (teamACount === 0) {
        teamAContainer.innerHTML = '<div style="color: #888; padding: 10px; text-align: center;">No messages</div>';
    }
    if (teamBCount === 0) {
        teamBContainer.innerHTML = '<div style="color: #888; padding: 10px; text-align: center;">No messages</div>';
    }

    // Snap to current time after populating
    snapMessagesToCurrentTime();
}

function snapMessagesToCurrentTime() {
    const currentTime = mapState.currentTime;
    const containers = ['kill-messages', 'team-a-messages', 'team-b-messages'];

    for (const containerId of containers) {
        const scrollContainer = document.getElementById(containerId);
        if (!scrollContainer) continue;

        const items = scrollContainer.querySelectorAll('.timeline-message-item[data-time]');
        if (items.length === 0) continue;

        // Find the message closest to current time
        let closestItem = null;
        let closestDist = Infinity;
        for (const item of items) {
            const dist = Math.abs(parseFloat(item.dataset.time) - currentTime);
            if (dist < closestDist) {
                closestDist = dist;
                closestItem = item;
            }
        }

        if (closestItem) {
            // Scroll so the closest item is roughly centered
            const containerHeight = scrollContainer.clientHeight;
            const itemTop = closestItem.offsetTop;
            const itemHeight = closestItem.offsetHeight;
            scrollContainer.scrollTop = itemTop - (containerHeight / 2) + (itemHeight / 2);
        }
    }
}

function updateDetailGraph(startTime, endTime) {
    const container = document.getElementById('detail-graph');
    container.innerHTML = '';

    const buckets = timelineState.buckets;
    const teams = timelineState.teams;

    if (!buckets || buckets.length === 0 || teams.length < 2) return;

    // Filter buckets within range (1-second resolution)
    const filteredBuckets = buckets.filter(b =>
        b.startTime >= startTime && b.endTime <= endTime
    );

    if (filteredBuckets.length === 0) return;

    // Apply dynamic binning based on selection duration
    const selectionDuration = endTime - startTime;
    const binSize = getOptimalBinSize(selectionDuration);
    const displayBuckets = aggregateDetailBuckets(filteredBuckets, binSize, teams);

    // Find max value for scaling (weapons only, use 4 as typical max for 4v4)
    let maxTeamValue = 4;
    for (const bucket of displayBuckets) {
        const td = bucket.teamData || {};
        const teamA = td[teams[0]] || {};
        const teamB = td[teams[1]] || {};
        // Only count weapons, not powerups
        const teamATotal = (teamA.playersWithRL || 0) + (teamA.playersWithLG || 0) + (teamA.playersWithRLLG || 0);
        const teamBTotal = (teamB.playersWithRL || 0) + (teamB.playersWithLG || 0) + (teamB.playersWithRLLG || 0);
        maxTeamValue = Math.max(maxTeamValue, teamATotal, teamBTotal);
    }

    // Update Y-axis labels
    document.querySelector('#detail-y-axis .y-top').textContent = maxTeamValue;
    document.querySelector('#detail-y-axis .y-bottom').textContent = maxTeamValue;

    const barHeight = 90; // pixels for max value

    // Create diverging bars (Team A up, Team B down) - weapons only
    for (const bucket of displayBuckets) {
        const bar = document.createElement('div');
        bar.className = 'diverging-bar';

        const td = bucket.teamData || {};
        const teamAData = td[teams[0]] || {};
        const teamBData = td[teams[1]] || {};

        // Build team data objects (weapons only)
        const teamA = {
            rl: teamAData.playersWithRL || 0,
            lg: teamAData.playersWithLG || 0,
            rllg: teamAData.playersWithRLLG || 0
        };
        const teamB = {
            rl: teamBData.playersWithRL || 0,
            lg: teamBData.playersWithLG || 0,
            rllg: teamBData.playersWithRLLG || 0
        };

        // Team A goes up (above center axis)
        const topContainer = document.createElement('div');
        topContainer.className = 'diverging-bar-top';
        addWeaponSegments(topContainer, teamA, maxTeamValue, barHeight);

        // Team B goes down (below center axis)
        const bottomContainer = document.createElement('div');
        bottomContainer.className = 'diverging-bar-bottom';
        addWeaponSegments(bottomContainer, teamB, maxTeamValue, barHeight);

        bar.appendChild(topContainer);
        bar.appendChild(bottomContainer);
        container.appendChild(bar);
    }

    // Render powerup lines separately
    updatePowerupLines(filteredBuckets, startTime, endTime, teams);
}

// Add weapon segments only (no powerups)
function addWeaponSegments(container, data, maxValue, maxHeight) {
    const segments = [
        { value: data.rl, className: 'rl' },
        { value: data.lg, className: 'lg' },
        { value: data.rllg, className: 'rllg' }
    ];

    for (const seg of segments) {
        if (seg.value > 0) {
            const el = document.createElement('div');
            el.className = `bar-segment ${seg.className}`;
            el.style.height = `${(seg.value / maxValue) * maxHeight}px`;
            container.appendChild(el);
        }
    }
}

// Render powerup lines as horizontal spans showing duration
function updatePowerupLines(buckets, startTime, endTime, teams) {
    const topContainer = document.getElementById('powerup-lines-top');
    const bottomContainer = document.getElementById('powerup-lines-bottom');
    topContainer.innerHTML = '';
    bottomContainer.innerHTML = '';

    if (buckets.length === 0 || teams.length < 2) return;

    const duration = endTime - startTime;
    const powerupTypes = ['quad', 'pent', 'ring'];

    // For each team, find contiguous spans where powerup is active
    for (let teamIdx = 0; teamIdx < 2; teamIdx++) {
        const team = teams[teamIdx];
        const container = teamIdx === 0 ? topContainer : bottomContainer;

        for (const powerup of powerupTypes) {
            const field = `playersWith${powerup.charAt(0).toUpperCase() + powerup.slice(1)}`;

            // Find spans where this powerup is active
            let spanStart = null;
            for (let i = 0; i < buckets.length; i++) {
                const bucket = buckets[i];
                const td = bucket.teamData || {};
                const teamData = td[team] || {};
                const hasIt = (teamData[field] || 0) > 0;

                if (hasIt && spanStart === null) {
                    spanStart = bucket.startTime;
                } else if (!hasIt && spanStart !== null) {
                    // End of span
                    addPowerupLine(container, spanStart, bucket.startTime, startTime, duration, powerup);
                    spanStart = null;
                }
            }
            // Handle span that extends to end
            if (spanStart !== null) {
                addPowerupLine(container, spanStart, endTime, startTime, duration, powerup);
            }
        }
    }
}

function addPowerupLine(container, spanStart, spanEnd, viewStart, viewDuration, powerupType) {
    const leftPct = ((spanStart - viewStart) / viewDuration) * 100;
    const widthPct = ((spanEnd - spanStart) / viewDuration) * 100;

    if (widthPct > 0) {
        const line = document.createElement('div');
        line.className = `powerup-line ${powerupType}`;
        line.style.left = `${leftPct}%`;
        line.style.width = `${widthPct}%`;
        container.appendChild(line);
    }
}

function updateDetailAxis(startTime, endTime) {
    const container = document.getElementById('detail-axis');
    container.innerHTML = '';

    const matchStart = timelineState.matchStartTime;
    const relStart = Math.max(0, startTime - matchStart);
    const relEnd = Math.max(0, endTime - matchStart);
    const tickCount = 5;

    for (let i = 0; i <= tickCount; i++) {
        const time = relStart + ((relEnd - relStart) / tickCount) * i;
        const span = document.createElement('span');
        span.textContent = formatTime(time);
        container.appendChild(span);
    }
}

function updateHealthArmorGraph(startTime, endTime) {
    const container = document.getElementById('health-armor-graph');
    container.innerHTML = '';

    const buckets = timelineState.buckets;
    const teams = timelineState.teams;

    if (!buckets || buckets.length === 0 || teams.length < 2) return;

    // Filter buckets within range
    const filteredBuckets = buckets.filter(b =>
        b.startTime >= startTime && b.endTime <= endTime
    );

    if (filteredBuckets.length === 0) return;

    // Apply dynamic binning based on selection duration
    const selectionDuration = endTime - startTime;
    const binSize = getOptimalBinSize(selectionDuration);
    const displayBuckets = aggregateDetailBuckets(filteredBuckets, binSize, teams);

    // Find max value for scaling (team total health + armor)
    // For 4 players: max health ~400 (4*100), max armor ~800 (4*200)
    let maxValue = 400; // Use 400 as reasonable default for team total

    for (const bucket of displayBuckets) {
        const td = bucket.teamData || {};
        const teamA = td[teams[0]] || {};
        const teamB = td[teams[1]] || {};
        const teamATotal = (teamA.totalHealth || 0) + (teamA.totalArmor || 0);
        const teamBTotal = (teamB.totalHealth || 0) + (teamB.totalArmor || 0);
        maxValue = Math.max(maxValue, teamATotal, teamBTotal);
    }

    // Update Y-axis labels
    document.getElementById('health-y-top').textContent = maxValue;
    document.getElementById('health-y-bottom').textContent = maxValue;

    const barHeight = 90; // pixels for max value

    // Create diverging bars (Team A up, Team B down)
    for (const bucket of displayBuckets) {
        const bar = document.createElement('div');
        bar.className = 'diverging-bar';

        const td = bucket.teamData || {};
        const teamA = td[teams[0]] || {};
        const teamB = td[teams[1]] || {};

        // Helper to add armor segments by type
        const addArmorSegments = (teamData, container) => {
            const armorByType = teamData.armorByType || {};
            const totalArmor = teamData.totalArmor || 0;
            const raCount = armorByType.ra || 0;
            const yaCount = armorByType.ya || 0;
            const gaCount = armorByType.ga || 0;
            const totalPlayers = raCount + yaCount + gaCount;

            if (totalPlayers > 0 && totalArmor > 0) {
                // Distribute armor proportionally by type
                const raArmor = (raCount / totalPlayers) * totalArmor;
                const yaArmor = (yaCount / totalPlayers) * totalArmor;
                const gaArmor = (gaCount / totalPlayers) * totalArmor;

                // Add RA first (closest to axis), then YA, then GA
                if (gaArmor > 0) {
                    const seg = document.createElement('div');
                    seg.className = 'bar-segment ga';
                    seg.style.height = `${(gaArmor / maxValue) * barHeight}px`;
                    container.appendChild(seg);
                }
                if (yaArmor > 0) {
                    const seg = document.createElement('div');
                    seg.className = 'bar-segment ya';
                    seg.style.height = `${(yaArmor / maxValue) * barHeight}px`;
                    container.appendChild(seg);
                }
                if (raArmor > 0) {
                    const seg = document.createElement('div');
                    seg.className = 'bar-segment ra';
                    seg.style.height = `${(raArmor / maxValue) * barHeight}px`;
                    container.appendChild(seg);
                }
            } else if (totalArmor > 0) {
                // Fallback to generic armor if no type breakdown
                const seg = document.createElement('div');
                seg.className = 'bar-segment armor';
                seg.style.height = `${(totalArmor / maxValue) * barHeight}px`;
                container.appendChild(seg);
            }
        };

        // Team A goes up (above center axis) - health closer to axis, armor on top
        const topContainer = document.createElement('div');
        topContainer.className = 'diverging-bar-top';

        if ((teamA.totalHealth || 0) > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment health';
            seg.style.height = `${((teamA.totalHealth || 0) / maxValue) * barHeight}px`;
            topContainer.appendChild(seg);
        }
        addArmorSegments(teamA, topContainer);

        // Team B goes down (below center axis)
        const bottomContainer = document.createElement('div');
        bottomContainer.className = 'diverging-bar-bottom';

        if ((teamB.totalHealth || 0) > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment health';
            seg.style.height = `${((teamB.totalHealth || 0) / maxValue) * barHeight}px`;
            bottomContainer.appendChild(seg);
        }
        addArmorSegments(teamB, bottomContainer);

        bar.appendChild(topContainer);
        bar.appendChild(bottomContainer);
        container.appendChild(bar);
    }

    // Update health axis
    updateHealthAxis(startTime, endTime);
}

function updateHealthAxis(startTime, endTime) {
    const container = document.getElementById('health-axis');
    container.innerHTML = '';

    const matchStart = timelineState.matchStartTime;
    const relStart = Math.max(0, startTime - matchStart);
    const relEnd = Math.max(0, endTime - matchStart);
    const tickCount = 5;

    for (let i = 0; i <= tickCount; i++) {
        const time = relStart + ((relEnd - relStart) / tickCount) * i;
        const span = document.createElement('span');
        span.textContent = formatTime(time);
        container.appendChild(span);
    }
}

function updateFragsGraph(startTime, endTime) {
    const container = document.getElementById('frags-graph');
    if (!container) return;
    container.innerHTML = '';

    const teams = timelineState.teams;
    if (teams.length < 2) return;

    // Use frag events from timeline analysis (from stat tracking)
    const fragEvents = timelineState.fragEvents || [];

    // Filter frags to selection window
    const filteredFrags = fragEvents.filter(f => f.time >= startTime && f.time <= endTime);

    // Calculate dynamic bucket duration based on selection (use 15s bins for frags)
    // For frag counts, we want larger bins than the activity graph
    const selectionDuration = endTime - startTime;
    const baseBinSize = getOptimalBinSize(selectionDuration);
    const bucketDuration = Math.max(15, baseBinSize * 5); // 15s minimum, scale with selection

    const startBucket = Math.floor(startTime / bucketDuration);
    const endBucket = Math.ceil(endTime / bucketDuration);
    const numBuckets = endBucket - startBucket;

    if (numBuckets <= 0) return;

    // Count frags per bucket per team
    const teamAFrags = new Array(numBuckets).fill(0);
    const teamBFrags = new Array(numBuckets).fill(0);

    for (const frag of filteredFrags) {
        const bucketIdx = Math.floor(frag.time / bucketDuration) - startBucket;
        if (bucketIdx >= 0 && bucketIdx < numBuckets) {
            if (frag.team === teams[0]) {
                teamAFrags[bucketIdx]++;
            } else if (frag.team === teams[1]) {
                teamBFrags[bucketIdx]++;
            }
        }
    }

    // Find max frags for scaling
    let maxFrags = 5;
    for (let i = 0; i < numBuckets; i++) {
        maxFrags = Math.max(maxFrags, teamAFrags[i], teamBFrags[i]);
    }

    // Update Y-axis labels
    const yTop = document.getElementById('frags-y-top');
    const yBottom = document.getElementById('frags-y-bottom');
    if (yTop) yTop.textContent = maxFrags;
    if (yBottom) yBottom.textContent = maxFrags;

    // Update legend team names
    const legendA = document.getElementById('legend-frags-team-a');
    const legendB = document.getElementById('legend-frags-team-b');
    if (legendA) legendA.textContent = `${teams[0]} ↑`;
    if (legendB) legendB.textContent = `${teams[1]} ↓`;

    const barHeight = 90;

    // Create diverging bars
    for (let i = 0; i < numBuckets; i++) {
        const bar = document.createElement('div');
        bar.className = 'diverging-bar';

        // Team A up
        const topContainer = document.createElement('div');
        topContainer.className = 'diverging-bar-top';
        if (teamAFrags[i] > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment frags';
            seg.style.height = `${(teamAFrags[i] / maxFrags) * barHeight}px`;
            topContainer.appendChild(seg);
        }

        // Team B down
        const bottomContainer = document.createElement('div');
        bottomContainer.className = 'diverging-bar-bottom';
        if (teamBFrags[i] > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment frags';
            seg.style.height = `${(teamBFrags[i] / maxFrags) * barHeight}px`;
            bottomContainer.appendChild(seg);
        }

        bar.appendChild(topContainer);
        bar.appendChild(bottomContainer);
        container.appendChild(bar);
    }

    // Update axis for selection window
    updateFragsAxis(startTime, endTime);
}

function updateFragsAxis(startTime, endTime) {
    const container = document.getElementById('frags-axis');
    if (!container) return;
    container.innerHTML = '';

    const matchStart = timelineState.matchStartTime;
    const relStart = Math.max(0, startTime - matchStart);
    const relEnd = Math.max(0, endTime - matchStart);
    const tickCount = 5;

    for (let i = 0; i <= tickCount; i++) {
        const time = relStart + ((relEnd - relStart) / tickCount) * i;
        const span = document.createElement('span');
        span.textContent = formatTime(time);
        container.appendChild(span);
    }
}

function updateScoreTimeline(startTime, endTime) {
    const container = document.getElementById('score-graph');
    if (!container) return;
    container.innerHTML = '';

    const teams = timelineState.teams;
    if (teams.length < 2) return;

    // Use frag events from timeline analysis (from stat tracking), sorted by time
    const fragEvents = (timelineState.fragEvents || []).slice().sort((a, b) => a.time - b.time);

    // Calculate score at the start of selection (based on all frags before startTime)
    let scoreAtStart = 0;
    for (const frag of fragEvents) {
        if (frag.time >= startTime) break;
        if (frag.team === teams[0]) {
            scoreAtStart++;
        } else if (frag.team === teams[1]) {
            scoreAtStart--;
        }
    }

    // Calculate cumulative score within selection window
    // Positive = Team A leading, Negative = Team B leading
    let scoreDiff = scoreAtStart;
    const scorePoints = [];

    // Add initial point at selection start
    scorePoints.push({ time: startTime, diff: scoreDiff });

    for (const frag of fragEvents) {
        if (frag.time < startTime) continue;
        if (frag.time > endTime) break;
        if (frag.team === teams[0]) {
            scoreDiff++;
        } else if (frag.team === teams[1]) {
            scoreDiff--;
        }
        scorePoints.push({ time: frag.time, diff: scoreDiff });
    }

    // Add final point
    scorePoints.push({ time: endTime, diff: scoreDiff });

    if (scorePoints.length === 0) return;

    // Find max absolute difference within selection for scaling
    let maxDiff = 10;
    for (const pt of scorePoints) {
        maxDiff = Math.max(maxDiff, Math.abs(pt.diff));
    }

    // Update Y-axis labels
    const yTop = document.getElementById('score-y-top');
    const yBottom = document.getElementById('score-y-bottom');
    if (yTop) yTop.textContent = `+${maxDiff}`;
    if (yBottom) yBottom.textContent = `-${maxDiff}`;

    // Update legend team names
    const legendA = document.getElementById('legend-score-team-a');
    const legendB = document.getElementById('legend-score-team-b');
    if (legendA) legendA.textContent = `${teams[0]} leading ↑`;
    if (legendB) legendB.textContent = `${teams[1]} leading ↓`;

    // Calculate dynamic bucket duration based on selection
    const selectionDuration = endTime - startTime;
    const baseBinSize = getOptimalBinSize(selectionDuration);
    const bucketDuration = Math.max(5, baseBinSize * 2); // 5s minimum for score, scale with selection

    const numBuckets = Math.ceil((endTime - startTime) / bucketDuration);
    const barHeight = 90; // pixels for max value

    for (let i = 0; i < numBuckets; i++) {
        const bucketStart = startTime + i * bucketDuration;

        // Find score at bucket midpoint
        const bucketMid = bucketStart + bucketDuration / 2;
        let bucketScore = scoreAtStart;
        for (const pt of scorePoints) {
            if (pt.time <= bucketMid) {
                bucketScore = pt.diff;
            } else {
                break;
            }
        }

        const bar = document.createElement('div');
        bar.className = 'score-bar';

        const fill = document.createElement('div');
        fill.className = 'score-bar-fill';

        const heightPct = Math.abs(bucketScore) / maxDiff;
        const heightPx = heightPct * barHeight;

        if (bucketScore > 0) {
            fill.classList.add('positive');
            fill.style.height = `${heightPx}px`;
        } else if (bucketScore < 0) {
            fill.classList.add('negative');
            fill.style.height = `${heightPx}px`;
        }

        bar.appendChild(fill);
        container.appendChild(bar);
    }

    // Update axis for selection window
    updateScoreAxis(startTime, endTime);
}

function updateScoreAxis(startTime, endTime) {
    const container = document.getElementById('score-axis');
    if (!container) return;
    container.innerHTML = '';

    const matchStart = timelineState.matchStartTime;
    const relStart = Math.max(0, startTime - matchStart);
    const relEnd = Math.max(0, endTime - matchStart);
    const tickCount = 5;

    for (let i = 0; i <= tickCount; i++) {
        const time = relStart + ((relEnd - relStart) / tickCount) * i;
        const span = document.createElement('span');
        span.textContent = formatTime(time);
        container.appendChild(span);
    }
}

// =============================================================================
// Map Visualization
// =============================================================================

// Item keywords that should remain uppercase in location names
const ITEM_KEYWORDS = ['RA', 'YA', 'GA', 'MH', 'RL', 'LG', 'GL', 'NG', 'SNG', 'QUAD', 'PENT', 'RING'];

// Normalize location name: "RA MH" → "RA-MH", "Quad low" → "QUAD-low", "big stairs" → "big-stairs"
function normalizeLocationName(name) {
    return name
        .trim()
        .replace(/\s+/g, '-')
        .split('-')
        .map(part => {
            const upper = part.toUpperCase();
            return ITEM_KEYWORDS.includes(upper) ? upper : part.toLowerCase();
        })
        .join('-');
}

// Get color for location based on item type in name
function getLocationColor(name) {
    const nameLower = name.toLowerCase();

    // Powerups - bright colors
    if (nameLower.includes('quad'))  return { fill: 'rgba(80, 120, 255, 0.15)', stroke: '#5078ff', text: '#7090ff' };
    if (nameLower.includes('pent'))  return { fill: 'rgba(255, 0, 255, 0.15)', stroke: '#ff00ff', text: '#ff66ff' };
    if (nameLower.includes('ring'))  return { fill: 'rgba(255, 255, 0, 0.15)', stroke: '#ffff00', text: '#ffff66' };

    // Armors
    if (nameLower.includes('ra'))    return { fill: 'rgba(255, 80, 80, 0.15)', stroke: '#ff5050', text: '#ff8080' };
    if (nameLower.includes('ya'))    return { fill: 'rgba(255, 200, 50, 0.15)', stroke: '#ffc832', text: '#ffd866' };
    if (nameLower.includes('ga'))    return { fill: 'rgba(80, 200, 80, 0.15)', stroke: '#50c850', text: '#80d880' };

    // Health
    if (nameLower.includes('mh'))    return { fill: 'rgba(80, 200, 255, 0.15)', stroke: '#50c8ff', text: '#80d8ff' };

    // Weapons
    if (nameLower.includes('rl'))    return { fill: 'rgba(200, 100, 50, 0.12)', stroke: '#c86432', text: '#d88050' };
    if (nameLower.includes('lg'))    return { fill: 'rgba(150, 150, 255, 0.12)', stroke: '#9696ff', text: '#b0b0ff' };
    if (nameLower.includes('gl'))    return { fill: 'rgba(100, 180, 100, 0.12)', stroke: '#64b464', text: '#80c880' };
    if (nameLower.includes('sng') || nameLower.includes('ng'))
                                     return { fill: 'rgba(180, 140, 80, 0.12)', stroke: '#b48c50', text: '#c8a060' };

    // Default - subtle gray
    return { fill: 'rgba(100, 100, 120, 0.08)', stroke: '#444', text: '#666' };
}

// Group locations by normalized name and calculate centroid
function processLocationGroups(locations) {
    const groups = {};

    for (const loc of locations) {
        const normalizedName = normalizeLocationName(loc.name);
        if (!groups[normalizedName]) {
            groups[normalizedName] = {
                name: normalizedName,
                points: [],
                centroid: { x: 0, y: 0 },
                color: getLocationColor(normalizedName)
            };
        }
        groups[normalizedName].points.push({ x: loc.x, y: loc.y, z: loc.z });
    }

    // Calculate centroid for each group
    for (const group of Object.values(groups)) {
        let sumX = 0, sumY = 0;
        for (const p of group.points) {
            sumX += p.x;
            sumY += p.y;
        }
        group.centroid = {
            x: sumX / group.points.length,
            y: sumY / group.points.length
        };
    }

    return Object.values(groups);
}

// Compute convex hull using Graham scan algorithm
function computeConvexHull(points) {
    if (points.length < 3) return points;

    // Find lowest point (highest Y in canvas coords)
    let start = 0;
    for (let i = 1; i < points.length; i++) {
        if (points[i].y > points[start].y ||
            (points[i].y === points[start].y && points[i].x < points[start].x)) {
            start = i;
        }
    }

    // Sort by polar angle from start point
    const startPoint = points[start];
    const sorted = points.slice().sort((a, b) => {
        const angleA = Math.atan2(a.y - startPoint.y, a.x - startPoint.x);
        const angleB = Math.atan2(b.y - startPoint.y, b.x - startPoint.x);
        return angleA - angleB;
    });

    // Build hull with cross-product check
    const hull = [];
    for (const p of sorted) {
        while (hull.length >= 2 && crossProduct(hull[hull.length-2], hull[hull.length-1], p) <= 0) {
            hull.pop();
        }
        hull.push(p);
    }
    return hull;
}

function crossProduct(o, a, b) {
    return (a.x - o.x) * (b.y - o.y) - (a.y - o.y) * (b.x - o.x);
}

function expandPolygon(points, distance) {
    if (points.length === 0) return points;

    let cx = 0, cy = 0;
    for (const p of points) { cx += p.x; cy += p.y; }
    cx /= points.length;
    cy /= points.length;

    return points.map(p => {
        const dx = p.x - cx;
        const dy = p.y - cy;
        const len = Math.sqrt(dx*dx + dy*dy) || 1;
        return { x: p.x + (dx / len) * distance, y: p.y + (dy / len) * distance };
    });
}

// Draw a location region (convex hull or circle for single point)
function drawLocationRegion(ctx, group, worldToCanvasFunc) {
    if (group.points.length === 1) {
        // Single point - draw small circle
        const pos = worldToCanvasFunc(group.points[0].x, group.points[0].y);
        ctx.beginPath();
        ctx.arc(pos.x, pos.y, 12, 0, Math.PI * 2);
        ctx.fillStyle = group.color.fill;
        ctx.fill();
        ctx.strokeStyle = group.color.stroke;
        ctx.lineWidth = 1;
        ctx.stroke();
    } else {
        // Multiple points - compute and draw convex hull
        const canvasPoints = group.points.map(p => worldToCanvasFunc(p.x, p.y));
        const hull = computeConvexHull(canvasPoints);

        if (hull.length < 3) {
            // Degenerate case - draw bounding rect with padding
            let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
            for (const p of canvasPoints) {
                minX = Math.min(minX, p.x);
                maxX = Math.max(maxX, p.x);
                minY = Math.min(minY, p.y);
                maxY = Math.max(maxY, p.y);
            }
            const pad = 15;
            ctx.fillStyle = group.color.fill;
            ctx.fillRect(minX - pad, minY - pad, maxX - minX + pad*2, maxY - minY + pad*2);
            ctx.strokeStyle = group.color.stroke;
            ctx.lineWidth = 1;
            ctx.strokeRect(minX - pad, minY - pad, maxX - minX + pad*2, maxY - minY + pad*2);
        } else {
            // Expand hull outward and draw
            const expanded = expandPolygon(hull, 15);
            ctx.beginPath();
            ctx.moveTo(expanded[0].x, expanded[0].y);
            for (let i = 1; i < expanded.length; i++) {
                ctx.lineTo(expanded[i].x, expanded[i].y);
            }
            ctx.closePath();
            ctx.fillStyle = group.color.fill;
            ctx.fill();
            ctx.strokeStyle = group.color.stroke;
            ctx.lineWidth = 1;
            ctx.stroke();
        }
    }
}

// Map View State
let mapState = {
    canvas: null,
    ctx: null,
    locations: [],
    locationGroups: null, // Cached processed location groups
    bounds: { minX: 0, maxX: 0, minY: 0, maxY: 0 },
    currentTime: 0,
    isPlaying: false,
    animationFrameId: null,
    lastRenderTime: 0,
    showTracks: false,
    tracks: {}, // playerName -> [{x, y}]
    teams: [],
    playerSymbols: {}, // playerName -> { symbol, team, teamIdx }
    initialized: false
};

const PLAYER_SYMBOLS = ['*', 'x', '+', 'o', '◆', '▲', '●', '■'];

function initMapView(result) {
    if (!result.timelineAnalysis) return;

    mapState.canvas = document.getElementById('map-canvas');
    if (!mapState.canvas) return;
    mapState.ctx = mapState.canvas.getContext('2d');

    // Get location data from timeline analysis
    const timeline = result.timelineAnalysis;
    mapState.locations = timeline.locationData || [];
    mapState.locationGroups = null; // Clear cached groups for new demo

    // Show/hide no-data message
    const noDataMsg = document.getElementById('map-no-data');
    if (noDataMsg) {
        noDataMsg.style.display = mapState.locations.length === 0 ? 'block' : 'none';
    }

    // Calculate bounds from locations and player positions
    calculateMapBounds(result);

    // Get teams from demoInfo or match
    if (result.demoInfo?.teams) {
        mapState.teams = result.demoInfo.teams;
    } else if (result.match?.teams) {
        mapState.teams = result.match.teams.map(t => t.name);
    } else {
        mapState.teams = [];
    }

    // Assign symbols to players
    assignPlayerSymbols(result);

    // Set up time controls (only once)
    if (!mapState.initialized) {
        setupMapTimeControls(result);
        mapState.initialized = true;
    } else {
        // Update slider for new demo
        updateMapSliderRange(result);
    }

    // Build powerup event list
    buildMapPowerupList(result);

    // Reset tracks
    mapState.tracks = {};
    const showTracksCheckbox = document.getElementById('map-show-tracks');
    if (showTracksCheckbox) {
        showTracksCheckbox.checked = false;
        mapState.showTracks = false;
    }

    // Initial render at match start
    mapState.currentTime = timeline.matchStartTime || 0;
    const slider = document.getElementById('map-timeline-slider');
    if (slider) slider.value = mapState.currentTime;

    renderMap(mapState.currentTime);
}

function calculateMapBounds(result) {
    let minX = Infinity, maxX = -Infinity;
    let minY = Infinity, maxY = -Infinity;

    // From locations
    for (const loc of mapState.locations) {
        minX = Math.min(minX, loc.x);
        maxX = Math.max(maxX, loc.x);
        minY = Math.min(minY, loc.y);
        maxY = Math.max(maxY, loc.y);
    }

    // From high-res buckets (if available) - more accurate bounds
    const highResBuckets = result.timelineAnalysis?.highResBuckets || [];
    if (highResBuckets.length > 0) {
        for (const bucket of highResBuckets) {
            for (const data of Object.values(bucket.p || {})) {
                if (data.x !== 0 || data.y !== 0) {
                    minX = Math.min(minX, data.x);
                    maxX = Math.max(maxX, data.x);
                    minY = Math.min(minY, data.y);
                    maxY = Math.max(maxY, data.y);
                }
            }
        }
    } else {
        // Fallback to 1s bucket data if no high-res data
        const buckets = result.timelineAnalysis?.buckets || [];
        for (const bucket of buckets) {
            for (const [name, data] of Object.entries(bucket.playerData || {})) {
                if (data.x !== 0 || data.y !== 0) {
                    minX = Math.min(minX, data.x);
                    maxX = Math.max(maxX, data.x);
                    minY = Math.min(minY, data.y);
                    maxY = Math.max(maxY, data.y);
                }
            }
        }
    }

    // Handle case where no data found
    if (minX === Infinity) {
        minX = -1000; maxX = 1000;
        minY = -1000; maxY = 1000;
    }

    // Add padding (10%)
    const padX = (maxX - minX) * 0.1;
    const padY = (maxY - minY) * 0.1;

    mapState.bounds = {
        minX: minX - padX,
        maxX: maxX + padX,
        minY: minY - padY,
        maxY: maxY + padY
    };
}

function worldToCanvas(x, y) {
    const { minX, maxX, minY, maxY } = mapState.bounds;
    const canvas = mapState.canvas;

    const worldWidth = maxX - minX;
    const worldHeight = maxY - minY;

    const scaleX = canvas.width / worldWidth;
    const scaleY = canvas.height / worldHeight;
    const scale = Math.min(scaleX, scaleY);

    const offsetX = (canvas.width - worldWidth * scale) / 2;
    const offsetY = (canvas.height - worldHeight * scale) / 2;

    return {
        x: offsetX + (x - minX) * scale,
        y: canvas.height - (offsetY + (y - minY) * scale) // Flip Y
    };
}

function assignPlayerSymbols(result) {
    const demoInfo = result.demoInfo;
    const players = demoInfo?.players || [];

    mapState.playerSymbols = {};

    // Group players by team
    const teamPlayers = {};
    for (const team of mapState.teams) {
        teamPlayers[team] = [];
    }

    for (const player of players) {
        if (player.team && teamPlayers[player.team]) {
            teamPlayers[player.team].push(player.name);
        }
    }

    // Assign symbols
    for (let teamIdx = 0; teamIdx < mapState.teams.length; teamIdx++) {
        const team = mapState.teams[teamIdx];
        const playerList = teamPlayers[team] || [];
        playerList.forEach((name, i) => {
            mapState.playerSymbols[name] = {
                symbol: PLAYER_SYMBOLS[i % PLAYER_SYMBOLS.length],
                team: team,
                teamIdx: teamIdx
            };
        });
    }

    // Build legend
    buildMapLegend();
}

function buildMapLegend() {
    const legend = document.getElementById('map-legend');
    if (!legend) return;

    legend.innerHTML = '<h4>Players</h4>';

    for (let teamIdx = 0; teamIdx < mapState.teams.length; teamIdx++) {
        const team = mapState.teams[teamIdx];
        const teamDiv = document.createElement('div');
        teamDiv.className = 'map-legend-team';

        const teamColor = teamIdx === 0 ? 'player-red' : 'player-blue';
        teamDiv.innerHTML = `<div class="map-legend-team-name ${teamColor}">${escapeHtml(team)}</div>`;

        for (const [name, info] of Object.entries(mapState.playerSymbols)) {
            if (info.team === team) {
                const item = document.createElement('div');
                item.className = 'map-legend-item';
                item.innerHTML = `
                    <span class="map-legend-symbol ${teamColor}">${info.symbol}</span>
                    <span>${escapeHtml(name)}</span>
                `;
                teamDiv.appendChild(item);
            }
        }

        legend.appendChild(teamDiv);
    }
}

function renderMap(time) {
    const ctx = mapState.ctx;
    const canvas = mapState.canvas;

    if (!ctx || !canvas) return;

    // Clear
    ctx.fillStyle = '#0a0a15';
    ctx.fillRect(0, 0, canvas.width, canvas.height);

    // Process location groups once (cache in mapState)
    if (!mapState.locationGroups && mapState.locations.length > 0) {
        mapState.locationGroups = processLocationGroups(mapState.locations);
    }

    // Draw location regions first (background layer)
    if (mapState.locationGroups) {
        for (const group of mapState.locationGroups) {
            drawLocationRegion(ctx, group, worldToCanvas);
        }

        // Draw single label at centroid for each location
        ctx.font = '10px monospace';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'middle';

        for (const group of mapState.locationGroups) {
            const pos = worldToCanvas(group.centroid.x, group.centroid.y);
            ctx.fillStyle = group.color.text;
            ctx.fillText(group.name, pos.x, pos.y);
        }
    }

    // Get player positions at this time
    const bucket = findBucketAtTime(time);

    // Draw tracks if enabled
    if (mapState.showTracks) {
        drawTracks(ctx);
    }

    // Draw players
    if (bucket) {
        ctx.font = 'bold 18px monospace';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'middle';

        for (const [name, data] of Object.entries(bucket.playerData || {})) {
            if (data.x === 0 && data.y === 0) continue;

            const pos = worldToCanvas(data.x, data.y);
            const symbolInfo = mapState.playerSymbols[name];

            if (symbolInfo) {
                // Team color with glow
                const teamColor = symbolInfo.teamIdx === 0 ? '#ff5050' : '#50a0ff';
                ctx.save();
                ctx.shadowColor = teamColor;
                ctx.shadowBlur = 12;
                ctx.fillStyle = teamColor;
                ctx.fillText(symbolInfo.symbol, pos.x, pos.y);
                ctx.restore();

                // Add to track if showing tracks
                if (mapState.showTracks) {
                    if (!mapState.tracks[name]) mapState.tracks[name] = [];
                    const lastPos = mapState.tracks[name][mapState.tracks[name].length - 1];
                    // Only add if moved significantly
                    if (!lastPos || Math.abs(lastPos.x - pos.x) > 2 || Math.abs(lastPos.y - pos.y) > 2) {
                        mapState.tracks[name].push({
                            x: pos.x,
                            y: pos.y,
                            teamIdx: symbolInfo.teamIdx
                        });
                    }
                }
            }
        }
    }

    // Update time display
    const matchStart = currentResult?.timelineAnalysis?.matchStartTime || 0;
    const relTime = time - matchStart;
    const timeDisplay = document.getElementById('map-current-time');
    if (timeDisplay) {
        timeDisplay.textContent = formatTime(Math.max(0, relTime));
    }
}

function drawTracks(ctx) {
    for (const [name, points] of Object.entries(mapState.tracks)) {
        if (points.length < 2) continue;

        const isRed = points[0].teamIdx === 0;
        const total = points.length;

        // Draw segments with fading opacity (older = more transparent)
        for (let i = 1; i < total; i++) {
            const alpha = 0.08 + 0.5 * (i / total);
            ctx.beginPath();
            ctx.strokeStyle = isRed
                ? `rgba(255, 80, 80, ${alpha})`
                : `rgba(80, 160, 255, ${alpha})`;
            ctx.lineWidth = 1.5 + 0.5 * (i / total);
            ctx.moveTo(points[i - 1].x, points[i - 1].y);
            ctx.lineTo(points[i].x, points[i].y);
            ctx.stroke();
        }
    }
}

// Binary search for high-res bucket at or before time
function findHighResBucketAtTime(time) {
    const buckets = timelineState.highResBuckets;
    if (!buckets || buckets.length === 0) {
        // Fallback to 1s bucket data
        return findBucketAtTimeFallback(time);
    }

    let low = 0, high = buckets.length - 1;
    while (low < high) {
        const mid = Math.floor((low + high + 1) / 2);
        if (buckets[mid].t <= time) {
            low = mid;
        } else {
            high = mid - 1;
        }
    }

    const bucket = buckets[low];
    if (!bucket) return null;

    // Convert high-res bucket format to expected format for renderMap
    return {
        startTime: bucket.t,
        endTime: bucket.t + timelineState.highResDuration,
        playerData: convertHighResPlayerData(bucket.p)
    };
}

// Convert compact high-res player data to standard format
function convertHighResPlayerData(p) {
    if (!p) return {};
    const result = {};
    for (const [name, data] of Object.entries(p)) {
        result[name] = {
            x: data.x,
            y: data.y,
            health: data.h,
            armor: data.a,
            armorType: data.at,
            hasRL: data.rl,
            hasLG: data.lg,
            hasQuad: data.q,
            hasPent: data.pe,
            hasRing: data.r,
            rockets: data.rk,
            cells: data.cl
        };
    }
    return result;
}

// Fallback to 1s bucket when no high-res data
function findBucketAtTimeFallback(time) {
    const buckets = currentResult?.timelineAnalysis?.buckets || [];
    for (const bucket of buckets) {
        if (time >= bucket.startTime && time < bucket.endTime) {
            return bucket;
        }
    }
    // Return last bucket if past end
    return buckets.length > 0 ? buckets[buckets.length - 1] : null;
}

function findBucketAtTime(time) {
    // Use high-res buckets if available (for map), otherwise fall back
    if (timelineState.highResBuckets && timelineState.highResBuckets.length > 0) {
        return findHighResBucketAtTime(time);
    }
    return findBucketAtTimeFallback(time);
}

function setupMapTimeControls(result) {
    updateMapSliderRange(result);

    const slider = document.getElementById('map-timeline-slider');
    if (slider) {
        slider.addEventListener('input', (e) => {
            mapState.currentTime = parseFloat(e.target.value);
            renderMap(mapState.currentTime);
            // Sync timeline cursor
            updateTimelineCursor();
            updateTimelineTimeDisplay();
        });
    }

    const playPauseBtn = document.getElementById('map-play-pause');
    if (playPauseBtn) {
        playPauseBtn.addEventListener('click', toggleMapPlayback);
    }

    const jumpBackBtn = document.getElementById('map-jump-back');
    if (jumpBackBtn) {
        jumpBackBtn.addEventListener('click', () => jumpMapTime(-10));
    }

    const jumpForwardBtn = document.getElementById('map-jump-forward');
    if (jumpForwardBtn) {
        jumpForwardBtn.addEventListener('click', () => jumpMapTime(10));
    }

    const showTracksCheckbox = document.getElementById('map-show-tracks');
    if (showTracksCheckbox) {
        showTracksCheckbox.addEventListener('change', (e) => {
            mapState.showTracks = e.target.checked;
            if (!mapState.showTracks) {
                mapState.tracks = {};
            }
            renderMap(mapState.currentTime);
        });
    }

    const resetTracksBtn = document.getElementById('map-reset-tracks');
    if (resetTracksBtn) {
        resetTracksBtn.addEventListener('click', () => {
            mapState.tracks = {};
            renderMap(mapState.currentTime);
        });
    }
}

function updateMapSliderRange(result) {
    const slider = document.getElementById('map-timeline-slider');
    if (!slider) return;

    const duration = result.duration || 600;
    const matchStart = result.timelineAnalysis?.matchStartTime || 0;

    slider.min = matchStart;
    slider.max = duration;
    slider.value = matchStart;
    mapState.currentTime = matchStart;
}

function toggleMapPlayback() {
    const btn = document.getElementById('map-play-pause');
    if (!btn) return;

    if (mapState.isPlaying) {
        mapState.isPlaying = false;
        if (mapState.animationFrameId) {
            cancelAnimationFrame(mapState.animationFrameId);
            mapState.animationFrameId = null;
        }
        btn.textContent = '▶';
    } else {
        mapState.isPlaying = true;
        mapState.lastRenderTime = performance.now();
        btn.textContent = '⏸';
        animateMapPlayback();
    }
}

function animateMapPlayback() {
    if (!mapState.isPlaying) {
        mapState.animationFrameId = null;
        return;
    }

    const now = performance.now();
    const elapsed = (now - mapState.lastRenderTime) / 1000;
    mapState.currentTime += elapsed;
    mapState.lastRenderTime = now;

    const slider = document.getElementById('map-timeline-slider');
    if (slider) {
        if (mapState.currentTime > parseFloat(slider.max)) {
            mapState.currentTime = parseFloat(slider.min);
            mapState.tracks = {};
        }
        slider.value = mapState.currentTime;
    }

    renderMap(mapState.currentTime);

    // Sync timeline cursor
    updateTimelineCursor();
    updateTimelineTimeDisplay();
    updateTimeIndicators();

    mapState.animationFrameId = requestAnimationFrame(animateMapPlayback);
}

function jumpMapTime(delta) {
    const slider = document.getElementById('map-timeline-slider');
    if (!slider) return;

    mapState.currentTime = Math.max(
        parseFloat(slider.min),
        Math.min(parseFloat(slider.max), mapState.currentTime + delta)
    );
    slider.value = mapState.currentTime;
    renderMap(mapState.currentTime);

    // Sync timeline cursor
    updateTimelineCursor();
    updateTimelineTimeDisplay();
}

function buildMapPowerupList(result) {
    const list = document.getElementById('map-powerup-events');
    if (!list) return;

    list.innerHTML = '';

    const events = result.timelineAnalysis?.powerupEvents || [];
    const matchStart = result.timelineAnalysis?.matchStartTime || 0;

    if (events.length === 0) {
        list.innerHTML = '<li style="color: #666; font-style: italic;">No powerup events</li>';
        return;
    }

    for (const event of events) {
        const li = document.createElement('li');
        const relTime = Math.max(0, event.time - matchStart);
        li.innerHTML = `
            <span class="time-cell">${formatTime(relTime)}</span>
            <span class="powerup-cell ${event.powerupType}">${getPowerupDisplay(event.powerupType)}</span>
            <span>${escapeHtml(event.playerName || 'Unknown')}</span>
        `;
        li.addEventListener('click', () => {
            mapState.currentTime = event.time;
            const slider = document.getElementById('map-timeline-slider');
            if (slider) slider.value = event.time;
            renderMap(event.time);
        });
        list.appendChild(li);
    }
}
