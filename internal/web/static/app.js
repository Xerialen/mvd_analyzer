// MVD Analyzer Dashboard — Pure client-side via WASM

const TEAM_COLORS = ['#ff5050', '#50a0ff', '#4ecdc4', '#ffc107'];

// Derive strong/weak color variants from a hex color for region control displays
function hexToRgb(hex) {
    return [parseInt(hex.slice(1, 3), 16), parseInt(hex.slice(3, 5), 16), parseInt(hex.slice(5, 7), 16)];
}
function teamStrongColor(hex) {
    const [r, g, b] = hexToRgb(hex);
    // Darken by 30% for strong control
    return `rgb(${Math.round(r * 0.7)}, ${Math.round(g * 0.7)}, ${Math.round(b * 0.7)})`;
}
function teamWeakColor(hex) {
    const [r, g, b] = hexToRgb(hex);
    // Lighten towards white for weak control
    return `rgb(${Math.round(r + (255 - r) * 0.5)}, ${Math.round(g + (255 - g) * 0.5)}, ${Math.round(b + (255 - b) * 0.5)})`;
}

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
        const v = e.data.version;
        const tag = document.getElementById('version-tag');
        if (tag && v) {
            tag.textContent = `${v.tag} (${v.hash}) — ${v.date}`;
        }
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

    // Auto-load from hub if URL has ?hub= parameter (wait for WASM to be ready)
    const params = new URLSearchParams(location.search);
    const hubId = params.get('hub');
    if (hubId) {
        document.getElementById('hub-input').value = hubId;
        if (wasmReady) {
            loadFromHub();
        } else {
            // Queue auto-load for when WASM finishes loading
            const origHandler = worker.onmessage;
            worker.onmessage = (e) => {
                origHandler(e);
                if (e.data.type === 'ready') {
                    worker.onmessage = origHandler;
                    loadFromHub();
                }
            };
        }
    }
});

// ─── Current Time ──────────────────────────────────────────────────────────

// Single function to set current time and sync all views
function setCurrentTime(time) {
    mapState.currentTime = Math.max(0, Math.min(time, timelineState.duration || Infinity));
    mapState.renderDirty = true;
    updateUnifiedCursor();
    updateUnifiedTimeDisplay();
    updateTimeIndicators();
    updateTeamStatus();
    updateMapLegend();
    updateRegionStatus();
    renderChatMessages();
    renderMap(mapState.currentTime);
    updateUrlState();
}

// ─── URL State Sharing ─────────────────────────────────────────────────────

let _urlStateTimer = null;
function updateUrlState() {
    if (_urlStateTimer) return;
    _urlStateTimer = setTimeout(() => {
        _urlStateTimer = null;
        if (!currentResult) return;
        const params = new URLSearchParams();

        if (currentResult.hubInfo?.gameId) {
            params.set('hub', currentResult.hubInfo.gameId);
        }

        const activeTab = document.querySelector('.tab-btn.active')?.dataset.tab;
        if (activeTab && activeTab !== 'summary') params.set('tab', activeTab);

        if (mapState.currentTime > 0) {
            params.set('t', Math.round(mapState.currentTime));
        }

        if (timelineState.segment) {
            params.set('seg', `${Math.round(timelineState.segment.start)}-${Math.round(timelineState.segment.end)}`);
        }

        const qs = params.toString();
        history.replaceState(null, '', qs ? `?${qs}` : location.pathname);
    }, 500);
}

function applyUrlState() {
    const params = new URLSearchParams(location.search);

    const seg = params.get('seg');
    if (seg) {
        const [start, end] = seg.split('-').map(Number);
        if (!isNaN(start) && !isNaN(end)) {
            timelineState.segment = { start, end };
            updateSelectionOverlay();
            updateSegmentLabel();
            updateDetailView();
        }
    }

    const t = params.get('t');
    if (t) {
        const time = Number(t);
        if (!isNaN(time)) {
            setCurrentTime(time);
        }
    }

    const tab = params.get('tab');
    if (tab) {
        const btn = document.querySelector(`.tab-btn[data-tab="${tab}"]`);
        if (btn) btn.click();
    }

    updateUrlState();
}

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

const TABS_WITH_TIMELINE = ['timeline', 'chat', 'map', 'keymoments'];

function setupTabs() {
    const tabButtons = document.querySelectorAll('.tab-btn');
    tabButtons.forEach(btn => {
        btn.addEventListener('click', () => {
            const tabName = btn.dataset.tab;
            tabButtons.forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
            document.getElementById(`tab-${tabName}`).classList.add('active');

            // Show/hide unified timeline
            const tl = document.getElementById('unified-timeline');
            if (tl && currentResult) {
                tl.style.display = TABS_WITH_TIMELINE.includes(tabName) ? '' : 'none';
            }

            // Stop playback when switching to tabs without timeline
            if (!TABS_WITH_TIMELINE.includes(tabName) && mapState.isPlaying) {
                stopPlayback();
            }

            // Sync views on tab switch
            if (tabName === 'map') {
                renderMap(mapState.currentTime);
            } else if (tabName === 'timeline') {
                updateTimeIndicators();
            } else if (tabName === 'chat') {
                renderChatMessages();
            }

            updateUrlState();
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

    // Set team order early (sorted by total frags, highest first) for consistent colors everywhere
    {
        let teams = [];
        if (demoInfo?.teams) {
            teams = [...demoInfo.teams];
        } else if (result.match?.teams) {
            teams = result.match.teams.map(t => t.name);
        }
        if (teams.length >= 2 && demoInfo?.players) {
            const teamFrags = {};
            for (const p of demoInfo.players) {
                const t = p.team || '';
                teamFrags[t] = (teamFrags[t] || 0) + (p.stats?.frags || 0);
            }
            teams.sort((a, b) => (teamFrags[b] || 0) - (teamFrags[a] || 0));
        }
        timelineState.teams = teams;
    }
    // Player stats from demoInfo
    if (demoInfo && demoInfo.players) {
        displayPlayerStatsTeams(demoInfo.players);
        displayPlayerStats(demoInfo.players);
        displayWeaponStatsTeamsTable(demoInfo.players);
        displayWeaponStatsTable(demoInfo.players);
        displayItemsTeamsTable(demoInfo.players);
        displayItemsTable(demoInfo.players);
    } else if (result.frags && result.frags.byPlayer) {
        displayScoreboardFallback(result.frags.byPlayer, result.match ? result.match.players : []);
    }

    // Weapons chart from frags
    if (result.frags && result.frags.byWeapon) {
        displayWeaponsChart(result.frags.byWeapon);
    }

    // Region control data (needed by both timeline and map)
    if (result.timelineAnalysis?.regionControl) {
        initRegionControlData(result);
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

    // Make all static tables sortable
    document.querySelectorAll('.stats-table').forEach(makeSortable);

    // Apply URL state (tab, time, segment) if present
    applyUrlState();
}

// ─── Sortable Tables ──────────────────────────────────────────────────────

function makeSortable(table) {
    const theadRows = table.querySelectorAll('thead tr');
    const allHeaders = table.querySelectorAll('thead th');

    // Build a column index map: for each th, compute which td column it maps to.
    // Handles rowspan and colspan in multi-row headers.
    const grid = []; // grid[row][col] = th element or null (occupied by span)
    theadRows.forEach((tr, rowIdx) => {
        if (!grid[rowIdx]) grid[rowIdx] = [];
        let colPos = 0;
        tr.querySelectorAll('th').forEach(th => {
            // Skip columns already occupied by rowspan from previous rows
            while (grid[rowIdx][colPos]) colPos++;
            const colspan = parseInt(th.getAttribute('colspan')) || 1;
            const rowspan = parseInt(th.getAttribute('rowspan')) || 1;
            // Store mapping on the element
            th._sortColIdx = colPos;
            th._sortColspan = colspan;
            // Mark grid cells as occupied
            for (let r = 0; r < rowspan; r++) {
                if (!grid[rowIdx + r]) grid[rowIdx + r] = [];
                for (let c = 0; c < colspan; c++) {
                    grid[rowIdx + r][colPos + c] = true;
                }
            }
            colPos += colspan;
        });
    });

    allHeaders.forEach(th => {
        // Skip colspan > 1 headers (group headers, not sortable)
        if (th._sortColspan > 1) return;

        const colIdx = th._sortColIdx;
        th.classList.add('sortable');
        th.addEventListener('click', () => {
            const tbody = table.querySelector('tbody');
            if (!tbody) return;
            const rows = Array.from(tbody.querySelectorAll('tr'));

            // Toggle direction (default first click = descending for numbers)
            const wasAsc = th.classList.contains('sort-asc');
            const dir = wasAsc ? 'desc' : 'asc';

            // Reset all headers in this table
            allHeaders.forEach(h => h.classList.remove('sort-asc', 'sort-desc'));
            th.classList.add(dir === 'asc' ? 'sort-asc' : 'sort-desc');

            rows.sort((a, b) => {
                const aText = a.cells[colIdx]?.textContent.trim() || '';
                const bText = b.cells[colIdx]?.textContent.trim() || '';
                // Extract leading number (handles "42", "3.5%", "12 (30s)", etc.)
                const aNum = parseFloat(aText);
                const bNum = parseFloat(bText);
                if (!isNaN(aNum) && !isNaN(bNum)) {
                    return dir === 'asc' ? aNum - bNum : bNum - aNum;
                }
                return dir === 'asc' ? aText.localeCompare(bText) : bText.localeCompare(aText);
            });

            rows.forEach(row => tbody.appendChild(row));
        });
    });
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

    // Use timelineState.teams order for consistent colors, fall back to score sort
    let ordered;
    if (timelineState.teams && timelineState.teams.length >= 2) {
        ordered = timelineState.teams.map(t => [t, teamScores[t] || 0]);
        // Add any teams not in timelineState.teams
        for (const [t, s] of Object.entries(teamScores)) {
            if (!timelineState.teams.includes(t)) ordered.push([t, s]);
        }
    } else {
        ordered = Object.entries(teamScores).sort((a, b) => b[1] - a[1]);
    }

    ordered.forEach(([name, frags]) => {
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

    // Use timelineState.teams order for consistent colors, fall back to score sort
    let sorted;
    if (timelineState.teams && timelineState.teams.length >= 2) {
        const orderMap = {};
        timelineState.teams.forEach((t, i) => { orderMap[t] = i; });
        sorted = [...teams].sort((a, b) => (orderMap[a.name] ?? 999) - (orderMap[b.name] ?? 999));
    } else {
        sorted = [...teams].sort((a, b) => b.frags - a.frags);
    }

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

    const teamOrder = getTeamOrder(sorted);

    sorted.forEach(player => {
        const tr = document.createElement('tr');
        const teamIdx = teamOrder.indexOf(player.team || '');
    
        if (teamIdx >= 0 && teamIdx < TEAM_COLORS.length) {
            tr.style.borderLeft = `3px solid ${TEAM_COLORS[teamIdx]}`;
        }
        const kills = player.stats?.kills || 0;
        const deaths = player.stats?.deaths || 0;
        const rlKills = player.weapons?.rl?.kills?.enemy || 0;
        const lgKills = player.weapons?.lg?.kills?.enemy || 0;
        const efficiency = (kills + deaths) > 0 ? ((kills / (kills + deaths)) * 100).toFixed(1) : '0.0';
        tr.innerHTML = `
            <td>${escapeHtml(player.name)}</td>
            <td>${escapeHtml(player.team || '')}</td>
            <td>${player.stats?.frags || 0}</td>
            <td>${efficiency}%</td>
            <td>${kills}</td>
            <td>${rlKills}</td>
            <td>${lgKills}</td>
            <td>${deaths}</td>
            <td>${player.stats?.tk || 0}</td>
            <td>${player.stats?.suicides || 0}</td>
            <td>${player.dmg?.given || 0}</td>
            <td>${player.dmg?.taken || 0}</td>
            <td>${player.dmg?.['enemy-weapons'] ?? 0}</td>
            <td>${player.dmg?.['taken-to-die'] ?? 0}</td>
            <td>${player.ping || 0}</td>
        `;
        tbody.appendChild(tr);
    });
}

function displayWeaponStatsTable(players) {
    const tbody = document.getElementById('weapon-stats-body');
    tbody.innerHTML = '';

    const sorted = [...players].sort((a, b) => (b.dmg?.given || 0) - (a.dmg?.given || 0));

    const teamOrder = getTeamOrder(sorted);

    const wNames = ['sg', 'ssg', 'sng', 'gl', 'rl', 'lg'];

    sorted.forEach(player => {
        const w = player.weapons || {};
        const tr = document.createElement('tr');
        const teamIdx = teamOrder.indexOf(player.team || '');
        if (teamIdx >= 0 && teamIdx < TEAM_COLORS.length) {
            tr.style.borderLeft = `3px solid ${TEAM_COLORS[teamIdx]}`;
        }
        let cells = `<td>${escapeHtml(player.name)}</td>`;
        wNames.forEach(wn => {
            cells += formatWeaponCells(w[wn]);
        });
        tr.innerHTML = cells;
        tbody.appendChild(tr);
    });
}

function formatWeaponCells(weapon) {
    if (!weapon) return '<td>-</td><td>-</td><td>-</td>';

    let acc = '-';
    if (weapon.acc && weapon.acc.attacks > 0) {
        const pct = ((weapon.acc.hits / weapon.acc.attacks) * 100).toFixed(1);
        acc = `<span class="${getAccuracyClass(parseFloat(pct))}">${pct}%</span>`;
    }

    const kills = weapon.kills?.total || weapon.kills?.enemy || 0;
    const dmg = weapon.damage?.enemy || 0;

    return `<td>${acc}</td><td>${kills || '-'}</td><td>${dmg || '-'}</td>`;
}

function displayItemsTable(players) {
    const tbody = document.getElementById('items-body');
    tbody.innerHTML = '';

    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));

    const teamOrder = getTeamOrder(sorted);


    sorted.forEach(player => {
        const items = player.items || {};
        const weapons = player.weapons || {};
        const tr = document.createElement('tr');
        const teamIdx = teamOrder.indexOf(player.team || '');
        if (teamIdx >= 0 && teamIdx < TEAM_COLORS.length) {
            tr.style.borderLeft = `3px solid ${TEAM_COLORS[teamIdx]}`;
        }
        tr.innerHTML = `
            <td>${escapeHtml(player.name)}</td>
            <td>${items.ra?.took || 0}</td>
            <td>${items.ya?.took || 0}</td>
            <td>${items.ga?.took || 0}</td>
            <td>${items.health_100?.took || 0}</td>
            <td>${formatPowerup(items.q)}</td>
            <td>${formatPowerup(items.p)}</td>
            <td>${formatPowerup(items.r)}</td>
            <td>${weapons.rl?.pickups?.taken || 0}</td>
            <td>${weapons.rl?.pickups?.dropped || 0}</td>
            <td>${player.xferRL || 0}</td>
            <td>${weapons.lg?.pickups?.taken || 0}</td>
            <td>${weapons.lg?.pickups?.dropped || 0}</td>
            <td>${player.xferLG || 0}</td>
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
            <td>-</td>
            <td>-</td>
            <td>${player.deaths}</td>
            <td>${player.tk}</td>
            <td>-</td>
            <td>${player.dmgGiven}</td>
            <td>${player.dmgTaken}</td>
            <td>-</td>
            <td>-</td>
            <td>${player.ping}</td>
        `;
        tbody.appendChild(tr);
    });
}

// ─── Team helpers ──────────────────────────────────────────────────────────

function getTeamOrder(sortedPlayers) {
    // Canonical order set early in displayResults(), sorted by total frags
    if (timelineState.teams && timelineState.teams.length >= 2) {
        return [...timelineState.teams];
    }
    // Fallback: preserve order from input (already frag-sorted)
    const seen = new Set();
    const order = [];
    for (const p of sortedPlayers) {
        const t = p.team || '';
        if (t && !seen.has(t)) { seen.add(t); order.push(t); }
    }
    return order;
}

function groupByTeam(players) {
    const groups = {};
    players.forEach(p => {
        const t = p.team || '';
        if (!groups[t]) groups[t] = [];
        groups[t].push(p);
    });
    return groups;
}

// ─── Per-team aggregate tables ─────────────────────────────────────────────

function displayPlayerStatsTeams(players) {
    const tbody = document.getElementById('player-stats-team-body');
    tbody.innerHTML = '';

    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));
    const teamOrder = getTeamOrder(sorted);

    const groups = groupByTeam(sorted);

    teamOrder.forEach((team, idx) => {
        const members = groups[team] || [];
        const frags = members.reduce((s, p) => s + (p.stats?.frags || 0), 0);
        const kills = members.reduce((s, p) => s + (p.stats?.kills || 0), 0);
        const rlKills = members.reduce((s, p) => s + (p.weapons?.rl?.kills?.enemy || 0), 0);
        const lgKills = members.reduce((s, p) => s + (p.weapons?.lg?.kills?.enemy || 0), 0);
        const deaths = members.reduce((s, p) => s + (p.stats?.deaths || 0), 0);
        const tk = members.reduce((s, p) => s + (p.stats?.tk || 0), 0);
        const suicides = members.reduce((s, p) => s + (p.stats?.suicides || 0), 0);
        const dmgGiven = members.reduce((s, p) => s + (p.dmg?.given || 0), 0);
        const dmgTaken = members.reduce((s, p) => s + (p.dmg?.taken || 0), 0);
        const ewep = members.reduce((s, p) => s + (p.dmg?.['enemy-weapons'] ?? 0), 0);
        const toDie = members.length > 0
            ? (members.reduce((s, p) => s + (p.dmg?.['taken-to-die'] ?? 0), 0) / members.length).toFixed(0)
            : 0;
        const ping = members.length > 0
            ? (members.reduce((s, p) => s + (p.ping || 0), 0) / members.length).toFixed(0)
            : 0;
        const efficiency = (kills + deaths) > 0 ? ((kills / (kills + deaths)) * 100).toFixed(1) : '0.0';

        const tr = document.createElement('tr');
        if (idx < TEAM_COLORS.length) {
            tr.style.borderLeft = `3px solid ${TEAM_COLORS[idx]}`;
        }
        tr.innerHTML = `
            <td>${escapeHtml(team)}</td>
            <td>${frags}</td>
            <td>${efficiency}%</td>
            <td>${kills}</td>
            <td>${rlKills}</td>
            <td>${lgKills}</td>
            <td>${deaths}</td>
            <td>${tk}</td>
            <td>${suicides}</td>
            <td>${dmgGiven}</td>
            <td>${dmgTaken}</td>
            <td>${ewep}</td>
            <td>${toDie}</td>
            <td>${ping}</td>
        `;
        tbody.appendChild(tr);
    });
}

function displayWeaponStatsTeamsTable(players) {
    const tbody = document.getElementById('weapon-stats-team-body');
    tbody.innerHTML = '';

    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));
    const teamOrder = getTeamOrder(sorted);

    const groups = groupByTeam(sorted);
    const wNames = ['sg', 'ssg', 'sng', 'gl', 'rl', 'lg'];

    teamOrder.forEach((team, idx) => {
        const members = groups[team] || [];
        let cells = `<td>${escapeHtml(team)}</td>`;
        wNames.forEach(wn => {
            let totalAtk = 0, totalHits = 0, totalKills = 0, totalDmg = 0;
            members.forEach(p => {
                const w = (p.weapons || {})[wn];
                if (!w) return;
                totalAtk += w.acc?.attacks || 0;
                totalHits += w.acc?.hits || 0;
                totalKills += w.kills?.total || w.kills?.enemy || 0;
                totalDmg += w.damage?.enemy || 0;
            });
            let acc = '-';
            if (totalAtk > 0) {
                const pct = ((totalHits / totalAtk) * 100).toFixed(1);
                acc = `<span class="${getAccuracyClass(parseFloat(pct))}">${pct}%</span>`;
            }
            cells += `<td>${acc}</td><td>${totalKills || '-'}</td><td>${totalDmg || '-'}</td>`;
        });

        const tr = document.createElement('tr');
        if (idx < TEAM_COLORS.length) {
            tr.style.borderLeft = `3px solid ${TEAM_COLORS[idx]}`;
        }
        tr.innerHTML = cells;
        tbody.appendChild(tr);
    });
}

function displayItemsTeamsTable(players) {
    const tbody = document.getElementById('items-team-body');
    tbody.innerHTML = '';

    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));
    const teamOrder = getTeamOrder(sorted);

    const groups = groupByTeam(sorted);

    teamOrder.forEach((team, idx) => {
        const members = groups[team] || [];
        const ra = members.reduce((s, p) => s + (p.items?.ra?.took || 0), 0);
        const ya = members.reduce((s, p) => s + (p.items?.ya?.took || 0), 0);
        const ga = members.reduce((s, p) => s + (p.items?.ga?.took || 0), 0);
        const mh = members.reduce((s, p) => s + (p.items?.health_100?.took || 0), 0);
        const quad = members.reduce((s, p) => s + (p.items?.q?.took || 0), 0);
        const quadTime = members.reduce((s, p) => s + (p.items?.q?.time || 0), 0);
        const pent = members.reduce((s, p) => s + (p.items?.p?.took || 0), 0);
        const pentTime = members.reduce((s, p) => s + (p.items?.p?.time || 0), 0);
        const ring = members.reduce((s, p) => s + (p.items?.r?.took || 0), 0);
        const ringTime = members.reduce((s, p) => s + (p.items?.r?.time || 0), 0);
        const rlPickup = members.reduce((s, p) => s + (p.weapons?.rl?.pickups?.taken || 0), 0);
        const rlDrop = members.reduce((s, p) => s + (p.weapons?.rl?.pickups?.dropped || 0), 0);
        const rlXfer = members.reduce((s, p) => s + (p.xferRL || 0), 0);
        const lgPickup = members.reduce((s, p) => s + (p.weapons?.lg?.pickups?.taken || 0), 0);
        const lgDrop = members.reduce((s, p) => s + (p.weapons?.lg?.pickups?.dropped || 0), 0);
        const lgXfer = members.reduce((s, p) => s + (p.xferLG || 0), 0);

        const fmtPu = (took, time) => time > 0 ? `${took} (${time}s)` : `${took}`;

        const tr = document.createElement('tr');
        if (idx < TEAM_COLORS.length) {
            tr.style.borderLeft = `3px solid ${TEAM_COLORS[idx]}`;
        }
        tr.innerHTML = `
            <td>${escapeHtml(team)}</td>
            <td>${ra}</td>
            <td>${ya}</td>
            <td>${ga}</td>
            <td>${mh}</td>
            <td>${fmtPu(quad, quadTime)}</td>
            <td>${fmtPu(pent, pentTime)}</td>
            <td>${fmtPu(ring, ringTime)}</td>
            <td>${rlPickup}</td>
            <td>${rlDrop}</td>
            <td>${rlXfer}</td>
            <td>${lgPickup}</td>
            <td>${lgDrop}</td>
            <td>${lgXfer}</td>
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

    if (powerupEvents.length === 0) {
        emptyMsg.style.display = 'block';
        return;
    }
    emptyMsg.style.display = 'none';

    // Get hub info for viewer links (from currentResult which may have hubInfo set)
    const hubInfo = currentResult?.hubInfo;

    powerupEvents.forEach(event => {
        const tr = document.createElement('tr');

        // Build viewer URL if hub info available
        let watchCell = '-';
        if (hubInfo && hubInfo.gameId) {
            const demoOff = timelineState.demoOffset || 0;
            const fromTime = Math.max(0, Math.floor(event.time + demoOff) - 10);
            const toTime = Math.floor(event.endTime + demoOff) + 5;
            const trackId = event.playerUserID || event.playerSlot;
            const viewerUrl = `https://hub.quakeworld.nu/games/?gameId=${hubInfo.gameId}&from=${fromTime}&to=${toTime}&track=${trackId}`;
            watchCell = `<a href="${viewerUrl}" target="_blank" class="viewer-link">Hub</a>`;
        }

        const powerupDisplay = getPowerupDisplay(event.powerupType);

        tr.innerHTML = `
            <td class="time-cell time-link">${formatDuration(event.time)}</td>
            <td class="powerup-cell ${event.powerupType}">${powerupDisplay}</td>
            <td>${escapeHtml(event.playerName || 'Unknown')}</td>
            <td>${escapeHtml(event.team || '-')}</td>
            <td>${event.frags || 0}</td>
            <td>${Math.round(event.duration)}s</td>
            <td>${watchCell}</td>
        `;

        // Click on time to jump there
        tr.querySelector('.time-link').addEventListener('click', () => {
            setCurrentTime(event.time);
        });

        tbody.appendChild(tr);
    });

    // Display frag streaks
    const streakBody = document.getElementById('fragstreaks-body');
    const streakEmpty = document.getElementById('fragstreaks-empty');
    streakBody.innerHTML = '';

    const fragStreaks = result.timelineAnalysis?.fragStreaks || [];

    if (fragStreaks.length === 0) {
        streakEmpty.style.display = 'block';
    } else {
        streakEmpty.style.display = 'none';

        fragStreaks.forEach(streak => {
            const tr = document.createElement('tr');

            let watchCell = '-';
            if (hubInfo && hubInfo.gameId) {
                const demoOff = timelineState.demoOffset || 0;
                const fromTime = Math.max(0, Math.floor(streak.time + demoOff));
                const toTime = Math.floor(streak.endTime + demoOff) + 3;
                const trackId = streak.playerUserID || 0;
                const viewerUrl = `https://hub.quakeworld.nu/games/?gameId=${hubInfo.gameId}&from=${fromTime}&to=${toTime}&track=${trackId}`;
                watchCell = `<a href="${viewerUrl}" target="_blank" class="viewer-link">Hub</a>`;
            }

            const mainWepDisplay = streak.ewep ? streak.ewep.toUpperCase() : '-';
            const durationSecs = Math.round(streak.duration);

            tr.innerHTML = `
                <td class="time-cell time-link">${formatDuration(streak.time)}</td>
                <td>${escapeHtml(streak.playerName || 'Unknown')}</td>
                <td>${escapeHtml(streak.team || '-')}</td>
                <td>${streak.frags}</td>
                <td>${escapeHtml(mainWepDisplay)}</td>
                <td>${durationSecs}s</td>
                <td>${watchCell}</td>
            `;

            tr.querySelector('.time-link').addEventListener('click', () => {
                setCurrentTime(streak.time);
            });

            streakBody.appendChild(tr);
        });
    }
}

function getPowerupDisplay(type) {
    switch(type) {
        case 'quad': return 'Quad';
        case 'pent': return 'Pent';
        case 'ring': return 'Ring';
        default: return type;
    }
}

function formatDuration(seconds) {
    const mins = Math.floor(seconds / 60);
    const secs = Math.floor(seconds % 60);
    return `${mins}:${secs.toString().padStart(2, '0')}`;
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
    segment: null, // { start, end } or null for full match - selected time segment
    dragging: false, // Is user dragging to select a segment on overview?
    dragStartTime: 0 // Time at drag start
};

// Reset all timeline state for loading a new demo
function resetTimelineState() {
    if (mapState.isPlaying) stopPlayback();
    timelineState.buckets = [];
    timelineState.highResBuckets = [];
    timelineState.highResDuration = 0.05;
    timelineState.events = [];
    timelineState.fragEvents = [];
    timelineState.duration = 0;
    timelineState.matchStartTime = 0;
    timelineState.demoOffset = 0;
    timelineState.teams = [];
    timelineState.segment = null;
    timelineState.dragging = false;
    precomputedFrags = [];
    chatRendered = false;
    chatUserScrolling = false;

    // Clear all timeline graph containers
    const containers = [
        'tl-axis', 'detail-graph', 'detail-axis',
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

    // Teams already set (frag-sorted) in displayResults; only set if missing
    if (!timelineState.teams || timelineState.teams.length === 0) {
        if (demoInfo?.teams) {
            timelineState.teams = demoInfo.teams;
        } else if (result.match?.teams) {
            timelineState.teams = result.match.teams.map(t => t.name);
        }
    }
    const teams = timelineState.teams;

    timelineState.buckets = timeline?.buckets || [];
    timelineState.highResBuckets = timeline?.highResBuckets || [];
    timelineState.highResDuration = timeline?.highResDuration || 0.05;
    timelineState.matchStartTime = timeline?.matchStartTime || 0;
    timelineState.demoOffset = timeline?.demoOffset || 0;
    timelineState.duration = result.duration || 600;
    timelineState.events = result.messages?.events || [];
    timelineState.fragEvents = timeline?.fragEvents || []; // Frag events from stat tracking

    // Set shared current time to start (all times are now match-relative, starting at 0)
    mapState.currentTime = 0;

    // Update legend team names
    if (teams.length >= 2) {
        const setTextIfExists = (id, text) => { const el = document.getElementById(id); if (el) el.textContent = text; };
        setTextIfExists('legend-team-a', teams[0] + ' ↑');
        setTextIfExists('legend-team-b', teams[1] + ' ↓');
        setTextIfExists('team-a-chat-title', `${teams[0]} Chat`);
        setTextIfExists('team-b-chat-title', `${teams[1]} Chat`);
        setTextIfExists('legend-health-team-a', teams[0] + ' ↑');
        setTextIfExists('legend-health-team-b', teams[1] + ' ↓');
        setTextIfExists('legend-weapons-team-a', teams[0] + ' ↑');
        setTextIfExists('legend-weapons-team-b', teams[1] + ' ↓');
    }

    precomputeFragCounts();
    setupUnifiedTimeline();

    // Show the unified timeline on applicable tabs
    const activeTab = document.querySelector('.tab-btn.active')?.dataset.tab;
    const tl = document.getElementById('unified-timeline');
    if (tl) tl.style.display = TABS_WITH_TIMELINE.includes(activeTab) ? '' : 'none';

    updateUnifiedCursor();
    updateDetailView();
    updateTimeIndicators();
    updateTeamStatus();
    renderChatMessages();
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

// ─── Unified Timeline Widget ──────────────────────────────────────────────

let unifiedTimelineInitialized = false;

function setupUnifiedTimeline() {
    if (unifiedTimelineInitialized) {
        updateUnifiedCursor();
        updateUnifiedTimeDisplay();
        renderUnifiedAxis();
        return;
    }

    const bar = document.getElementById('tl-bar');
    const caret = document.getElementById('tl-caret');

    renderUnifiedAxis();

    // --- Caret drag: sets current time ---
    let caretDragging = false;

    caret.addEventListener('mousedown', (e) => {
        caretDragging = true;
        e.preventDefault();
        e.stopPropagation();
    });

    document.addEventListener('mousemove', (e) => {
        if (caretDragging) {
            const time = tlBarClickToTime(e);
            if (time === null) return;
            setCurrentTime(time);
            return;
        }

        if (!timelineState.dragging) return;
        const time = tlBarClickToTime(e);
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
            updateUrlState();
            return;
        }

        if (!timelineState.dragging) return;
        timelineState.dragging = false;

        const time = tlBarClickToTime(e);
        if (time === null) return;

        const start = Math.min(timelineState.dragStartTime, time);
        const end = Math.max(timelineState.dragStartTime, time);

        if (end - start <= 2) {
            timelineState.segment = null;
            updateSelectionOverlay();
            updateSegmentLabel();
            setCurrentTime(time);
        } else {
            timelineState.segment = { start, end };
            updateSelectionOverlay();
            updateSegmentLabel();
            updateDetailView();
        }
        updateUrlState();
    });

    bar.addEventListener('mousedown', (e) => {
        const time = tlBarClickToTime(e);
        if (time === null) return;

        timelineState.dragging = true;
        timelineState.dragStartTime = time;
        timelineState.segment = null;
        updateSelectionOverlay();
        updateSegmentLabel();

        e.preventDefault();
    });

    bar.addEventListener('dblclick', () => {
        timelineState.segment = null;
        updateSelectionOverlay();
        updateSegmentLabel();
        updateDetailView();
        updateUrlState();
    });

    // --- Playback controls ---
    document.getElementById('tl-rev').addEventListener('click', () => startPlaybackAtSpeed(-1));
    document.getElementById('tl-play-pause').addEventListener('click', () => startPlaybackAtSpeed(1));
    document.getElementById('tl-5x').addEventListener('click', () => startPlaybackAtSpeed(5));
    document.getElementById('tl-20x').addEventListener('click', () => startPlaybackAtSpeed(20));

    unifiedTimelineInitialized = true;
}

function tlBarClickToTime(e) {
    const bar = document.getElementById('tl-bar');
    if (!bar) return null;
    const rect = bar.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const width = rect.width;
    if (width <= 0) return null;
    const frac = Math.max(0, Math.min(1, x / width));
    return frac * timelineState.duration;
}

function updateSelectionOverlay() {
    const overlay = document.getElementById('tl-selection-overlay');
    if (!overlay) return;

    if (!timelineState.segment) {
        overlay.style.display = 'none';
        return;
    }

    const duration = timelineState.duration;
    if (duration <= 0) return;

    const startPct = (timelineState.segment.start / duration) * 100;
    const endPct = (timelineState.segment.end / duration) * 100;

    overlay.style.display = 'block';
    overlay.style.left = `${startPct}%`;
    overlay.style.width = `${endPct - startPct}%`;
}

function updateSegmentLabel() {
    const label = document.getElementById('tl-segment-label');
    if (!label) return;

    if (!timelineState.segment) {
        label.textContent = '';
        return;
    }

    label.textContent = `${formatDuration(timelineState.segment.start)} – ${formatDuration(timelineState.segment.end)}`;
}

function updateUnifiedCursor() {
    const cursor = document.getElementById('tl-cursor');
    const caret = document.getElementById('tl-caret');

    const duration = timelineState.duration;
    if (duration <= 0) return;

    const pct = Math.max(0, Math.min(100, (mapState.currentTime / duration) * 100));
    if (cursor) cursor.style.left = `${pct}%`;
    if (caret) caret.style.left = `${pct}%`;
}

function updateUnifiedTimeDisplay() {
    const display = document.getElementById('tl-current-time');
    if (!display) return;
    display.textContent = formatDuration(Math.max(0, mapState.currentTime));
}

function renderUnifiedAxis() {
    const container = document.getElementById('tl-axis');
    if (!container) return;
    container.innerHTML = '';

    const duration = timelineState.duration;
    if (duration <= 0) return;

    const tickCount = Math.min(10, Math.max(4, Math.floor(duration / 60)));
    for (let i = 0; i <= tickCount; i++) {
        const time = (duration / tickCount) * i;
        const pct = (i / tickCount) * 100;
        const span = document.createElement('span');
        span.textContent = formatDuration(time);
        span.style.left = `${pct}%`;
        container.appendChild(span);
    }
}

function updateTimeIndicators() {
    updateUnifiedCursor();

    // Detail graphs show either the segment or the full match
    const seg = timelineState.segment;
    const rangeStart = seg ? seg.start : 0;
    const rangeEnd = seg ? seg.end : timelineState.duration;
    const range = rangeEnd - rangeStart;

    const detailIndicators = [
        'detail-time-indicator',
        'region-time-indicator',
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

    // Update team status table
    updateTeamStatus();
}

function updateDetailView() {
    const duration = timelineState.duration;

    // Use segment if selected, otherwise full match
    const start = timelineState.segment ? timelineState.segment.start : 0;
    const end = timelineState.segment ? timelineState.segment.end : duration;

    // Show range in label
    if (timelineState.segment) {
        document.getElementById('time-range-label').textContent =
            `(${formatDuration(start)} - ${formatDuration(end)})`;
    } else {
        document.getElementById('time-range-label').textContent = '';
    }

    // Update all detail panels
    updateDetailGraph(start, end);
    updateDetailAxis(start, end);
    updateRegionControlTimeline(start, end);
    updateHealthArmorGraph(start, end);
    updateFragsGraph(start, end);
    updateScoreTimeline(start, end);
}

// ─── Chat Tab ──────────────────────────────────────────────────────────────

// Chat: pixels per second for the full-match scrollable layout
const CHAT_PX_PER_SEC = 17.5; // ~same density as original 40s/700px window
const CHAT_ITEM_HEIGHT = 18;

let chatRendered = false;
let chatUserScrolling = false;
let _chatScrollTimer = null;
let chatContentHeight = 0;
let _chatProgrammaticScroll = false; // flag to distinguish our scrollTop writes from user scrolls

function renderChatMessages() {
    if (chatRendered) {
        updateChatTimeLine();
        scrollChatToCurrentTime();
        return;
    }
    buildFullChat();
}

function buildFullChat() {
    const viewport = document.getElementById('chat-scroll-viewport');
    const killContainer = document.getElementById('kill-messages');
    const teamAContainer = document.getElementById('team-a-messages');
    const teamBContainer = document.getElementById('team-b-messages');
    const axisContainer = document.getElementById('chat-time-axis');
    if (!viewport || !killContainer || !teamAContainer || !teamBContainer) return;

    const teams = timelineState.teams;
    const duration = timelineState.duration || 600;
    chatContentHeight = Math.round(duration * CHAT_PX_PER_SEC);

    killContainer.innerHTML = '';
    teamAContainer.innerHTML = '';
    teamBContainer.innerHTML = '';

    if (!currentResult?.messages?.events || teams.length < 2) return;

    const seen = new Map();
    const events = currentResult.messages.events.filter(e => {
        if (e.time < 0 || e.time > duration) return false;
        const key = e.message;
        const prevTime = seen.get(key);
        if (prevTime !== undefined && Math.abs(e.time - prevTime) < 3) return false;
        seen.set(key, e.time);
        return true;
    });

    const killEvents = [];
    const teamAEvents = [];
    const teamBEvents = [];

    for (const event of events) {
        if (event.type === 'frag') {
            killEvents.push(event);
        } else if (event.type === 'teamsay' || event.type === 'chat') {
            if (event.team === teams[0]) teamAEvents.push(event);
            else if (event.team === teams[1]) teamBEvents.push(event);
        }
    }

    renderChatColumnFull(killContainer, killEvents);
    renderChatColumnFull(teamAContainer, teamAEvents);
    renderChatColumnFull(teamBContainer, teamBEvents);

    if (axisContainer) {
        renderChatTimeAxisFull(axisContainer);
    }

    // Add current-time line inside the scroll inner (scrolls with content)
    const scrollInner = viewport.querySelector('.chat-scroll-inner');
    if (scrollInner) {
        const line = document.createElement('div');
        line.className = 'chat-current-time-line';
        line.id = 'chat-current-time-line';
        scrollInner.appendChild(line);
    }

    // Scroll listener: only mark user scrolling if it's not our programmatic scroll
    viewport.addEventListener('scroll', () => {
        if (_chatProgrammaticScroll) return;
        chatUserScrolling = true;
        if (_chatScrollTimer) clearTimeout(_chatScrollTimer);
        _chatScrollTimer = setTimeout(() => { chatUserScrolling = false; }, 2000);
    }, { passive: true });

    chatRendered = true;
    updateChatTimeLine();
    scrollChatToCurrentTime();
}

function updateChatTimeLine() {
    const line = document.getElementById('chat-current-time-line');
    if (!line) return;
    line.style.top = `${mapState.currentTime * CHAT_PX_PER_SEC}px`;
}

function scrollChatToCurrentTime() {
    if (chatUserScrolling) return;
    const viewport = document.getElementById('chat-scroll-viewport');
    if (!viewport) return;

    const topPx = mapState.currentTime * CHAT_PX_PER_SEC;
    const targetScroll = Math.max(0, topPx - viewport.clientHeight / 2);

    _chatProgrammaticScroll = true;
    viewport.scrollTop = targetScroll;
    // Reset flag after browser processes the scroll event
    requestAnimationFrame(() => { _chatProgrammaticScroll = false; });
}

function renderChatTimeAxisFull(container) {
    container.innerHTML = '';
    const duration = timelineState.duration || 600;

    const inner = document.createElement('div');
    inner.style.position = 'relative';
    inner.style.height = `${chatContentHeight}px`;

    const tickInterval = 5;
    for (let t = 0; t <= duration; t += tickInterval) {
        const topPx = Math.round(t * CHAT_PX_PER_SEC);
        const tick = document.createElement('div');
        tick.className = 'chat-tick';
        tick.style.top = `${topPx}px`;
        tick.textContent = formatDuration(t);
        inner.appendChild(tick);
    }

    container.appendChild(inner);
}

function renderChatColumnFull(container, events) {
    const inner = document.createElement('div');
    inner.style.position = 'relative';
    inner.style.height = `${chatContentHeight}px`;

    let lastBottom = -Infinity;

    for (const event of events) {
        let topPx = Math.round(event.time * CHAT_PX_PER_SEC);

        let displaced = false;
        if (topPx < lastBottom) {
            topPx = lastBottom;
            displaced = true;
        }

        const marker = document.createElement('div');
        marker.className = 'chat-time-marker' + (displaced ? ' chat-displaced' : '');
        marker.style.top = `${topPx}px`;

        const prefix = displaced ? '<span class="chat-displaced-dots">...</span>' : '';
        marker.innerHTML = `${prefix}<span class="chat-time-marker-msg ${event.type}">${formatQuakeMessage(event.message)}</span>`;

        inner.appendChild(marker);
        lastBottom = topPx + CHAT_ITEM_HEIGHT;
    }

    container.appendChild(inner);
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

// ─── Region Control Timeline ─────────────────────────────────────────────

function updateRegionControlTimeline(startTime, endTime) {
    const panel = document.getElementById('region-control-timeline-panel');
    const labelsContainer = document.getElementById('region-timeline-labels');
    const stripsContainer = document.getElementById('region-timeline-strips');
    if (!panel || !labelsContainer || !stripsContainer) return;

    if (!mapState.controlRegions || mapState.controlRegions.length === 0 ||
        !mapState.locToRegion || !timelineState.teams || timelineState.teams.length < 2) {
        panel.style.display = 'none';
        return;
    }
    panel.style.display = '';

    // Update legend team names
    const teamA = timelineState.teams[0], teamB = timelineState.teams[1];
    const teamALabel = document.getElementById('rc-tl-teamA');
    const teamBLabel = document.getElementById('rc-tl-teamB');
    if (teamALabel) teamALabel.textContent = teamA;
    if (teamBLabel) teamBLabel.textContent = teamB;

    // Update legend color swatches to match strip colors exactly
    const setLegend = (id, color) => { const el = document.getElementById(id); if (el) el.style.background = color; };
    setLegend('rc-legend-a-ctrl', teamStrongColor(TEAM_COLORS[0]));
    setLegend('rc-legend-a-weak', teamWeakColor(TEAM_COLORS[0]));
    setLegend('rc-legend-b-weak', teamWeakColor(TEAM_COLORS[1]));
    setLegend('rc-legend-b-ctrl', teamStrongColor(TEAM_COLORS[1]));

    labelsContainer.innerHTML = '';
    stripsContainer.innerHTML = '';

    const regions = mapState.controlRegions;
    const buckets = timelineState.buckets;
    if (!buckets || buckets.length === 0) return;

    const duration = endTime - startTime;
    if (duration <= 0) return;

    const locations = mapState.locations;

    // Control state colors derived from TEAM_COLORS
    const stateColors = {
        teamAControl:     teamStrongColor(TEAM_COLORS[0]),
        teamAWeakControl: teamWeakColor(TEAM_COLORS[0]),
        contested:        'rgb(255, 255, 255)',
        empty:            'transparent',
        teamBWeakControl: teamWeakColor(TEAM_COLORS[1]),
        teamBControl:     teamStrongColor(TEAM_COLORS[1]),
    };

    for (const region of regions) {
        // Label
        const label = document.createElement('div');
        label.className = 'region-timeline-label';
        label.textContent = region.name;
        label.title = region.name;
        labelsContainer.appendChild(label);

        // Strip
        const strip = document.createElement('div');
        strip.className = 'region-strip';

        // Compute control state per bucket and find contiguous spans
        let currentState = null;
        let spanStart = startTime;

        for (let i = 0; i < buckets.length; i++) {
            const bucket = buckets[i];
            if (bucket.endTime <= startTime || bucket.startTime >= endTime) continue;

            // Determine control state for this region at this bucket
            const playerData = bucket.playerData;
            let aWpn = 0, aNo = 0, bWpn = 0, bNo = 0;

            if (playerData) {
                for (const [name, data] of Object.entries(playerData)) {
                    if (!data || (data.health !== undefined && data.health <= 0)) continue;
                    const loc = data.location || '';
                    const rName = mapState.locToRegion[loc];
                    if (rName !== region.name) continue;

                    const playerTeam = data.team || '';
                    const hasWpn = data.hasRL || data.hasLG;

                    if (playerTeam === teamA) { if (hasWpn) aWpn++; else aNo++; }
                    else if (playerTeam === teamB) { if (hasWpn) bWpn++; else bNo++; }
                }
            }

            const aT = aWpn + aNo, bT = bWpn + bNo;
            let state;
            if (aT === 0 && bT === 0) state = 'empty';
            else if (aT > 0 && bT === 0) state = aWpn > 0 ? 'teamAControl' : 'teamAWeakControl';
            else if (bT > 0 && aT === 0) state = bWpn > 0 ? 'teamBControl' : 'teamBWeakControl';
            else if (aWpn > 0 && bWpn === 0) state = 'teamAControl';
            else if (bWpn > 0 && aWpn === 0) state = 'teamBControl';
            else state = 'contested';

            if (state !== currentState) {
                // Emit previous span
                if (currentState && currentState !== 'empty') {
                    addRegionSegment(strip, spanStart, bucket.startTime, startTime, duration, stateColors[currentState]);
                }
                currentState = state;
                spanStart = bucket.startTime;
            }
        }
        // Final span
        if (currentState && currentState !== 'empty') {
            addRegionSegment(strip, spanStart, endTime, startTime, duration, stateColors[currentState]);
        }

        stripsContainer.appendChild(strip);
    }

    // Render time axis with 2-minute intervals
    const axisContainer = document.getElementById('region-timeline-axis');
    if (axisContainer) {
        axisContainer.innerHTML = '';
        const interval = 120; // 2 minutes
        for (let t = 0; t <= duration; t += interval) {
            const time = startTime + t;
            if (time > endTime) break;
            const span = document.createElement('span');
            span.textContent = formatDuration(time);
            axisContainer.appendChild(span);
        }
    }
}

function addRegionSegment(strip, segStart, segEnd, viewStart, viewDuration, color) {
    const leftPct = ((segStart - viewStart) / viewDuration) * 100;
    const widthPct = ((segEnd - segStart) / viewDuration) * 100;
    if (widthPct > 0) {
        const seg = document.createElement('div');
        seg.className = 'region-strip-seg';
        seg.style.left = `${leftPct}%`;
        seg.style.width = `${widthPct}%`;
        seg.style.background = color;
        strip.appendChild(seg);
    }
}

function updateDetailAxis(startTime, endTime) {
    const container = document.getElementById('detail-axis');
    container.innerHTML = '';

    const duration = endTime - startTime;
    const interval = 120; // 2 minutes

    for (let t = 0; t <= duration; t += interval) {
        const time = startTime + t;
        if (time > endTime) break;
        const span = document.createElement('span');
        span.textContent = formatDuration(time);
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

    const duration = endTime - startTime;
    const interval = 120;

    for (let t = 0; t <= duration; t += interval) {
        const time = startTime + t;
        if (time > endTime) break;
        const span = document.createElement('span');
        span.textContent = formatDuration(time);
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
        const delta = frag.delta || 1;
        if (bucketIdx >= 0 && bucketIdx < numBuckets) {
            if (frag.team === teams[0]) {
                teamAFrags[bucketIdx] += delta;
            } else if (frag.team === teams[1]) {
                teamBFrags[bucketIdx] += delta;
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

    const duration = endTime - startTime;
    const interval = 120;

    for (let t = 0; t <= duration; t += interval) {
        const time = startTime + t;
        if (time > endTime) break;
        const span = document.createElement('span');
        span.textContent = formatDuration(time);
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
        const delta = frag.delta || 1;
        if (frag.team === teams[0]) {
            scoreAtStart += delta;
        } else if (frag.team === teams[1]) {
            scoreAtStart -= delta;
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
        const delta = frag.delta || 1;
        if (frag.team === teams[0]) {
            scoreDiff += delta;
        } else if (frag.team === teams[1]) {
            scoreDiff -= delta;
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

    const duration = endTime - startTime;
    const interval = 120;

    for (let t = 0; t <= duration; t += interval) {
        const time = startTime + t;
        if (time > endTime) break;
        const span = document.createElement('span');
        span.textContent = formatDuration(time);
        container.appendChild(span);
    }
}

// ─── Team Status Panel ──────────────────────────────────────────────────────

function updateTeamStatus() {
    const containerA = document.getElementById('team-status-a');
    const containerB = document.getElementById('team-status-b');
    if (!containerA || !containerB) return;

    const teams = timelineState.teams;
    const buckets = timelineState.buckets;
    if (!buckets || buckets.length === 0 || teams.length < 2) {
        containerA.innerHTML = '';
        containerB.innerHTML = '';
        return;
    }

    // Find bucket at current time — prefer high-res (50ms) buckets when available
    // so stats match the map view exactly
    const time = mapState.currentTime;
    const hrBucket = findBucketAtTime(time);
    let pd;
    if (hrBucket) {
        pd = hrBucket.p || hrBucket.playerData || {};
    } else {
        let bucket = null;
        for (const b of buckets) {
            if (time >= b.startTime && time < b.endTime) { bucket = b; break; }
        }
        if (!bucket) bucket = buckets[buckets.length - 1];
        pd = bucket.playerData || {};
    }
    const fragCounts = getFragsAtTime(time);

    for (let ti = 0; ti < 2; ti++) {
        const team = teams[ti];
        const container = ti === 0 ? containerA : containerB;

        // Collect ALL players for this team — show dead/respawning players with '-' stats
        // (matching the map legend behavior)
        const players = [];
        const allPlayerNames = new Set();

        // Get all known players for this team from demoInfo/playerSymbols
        const demoPlayers = currentResult?.demoInfo?.players || [];
        for (const dp of demoPlayers) {
            if (dp.team === team) allPlayerNames.add(dp.name);
        }
        for (const [name, info] of Object.entries(mapState.playerSymbols || {})) {
            if (info.team === team) allPlayerNames.add(name);
        }
        // Also include any players present in bucket data for this team
        for (const [name, data] of Object.entries(pd)) {
            const t = data.team || mapState.playerSymbols?.[name]?.team;
            if (t === team) allPlayerNames.add(name);
        }

        for (const name of allPlayerNames) {
            const data = pd[name];
            const isDead = !data || (data.d ?? data.dead) || (data.h ?? data.health ?? 0) <= 0;
            players.push({
                name,
                dead: isDead,
                health: isDead ? 0 : (data.h ?? data.health ?? 0),
                armor: isDead ? 0 : (data.a ?? data.armor ?? 0),
                armorType: isDead ? '' : (data.at ?? data.armorType ?? ''),
                hasRL: isDead ? false : (data.rl ?? data.hasRL ?? false),
                hasLG: isDead ? false : (data.lg ?? data.hasLG ?? false),
                hasQuad: isDead ? false : (data.q ?? data.hasQuad ?? false),
                hasPent: isDead ? false : (data.pent ?? data.hasPent ?? false),
                hasRing: isDead ? false : (data.r ?? data.hasRing ?? false),
                frags: fragCounts[name] || 0,
            });
        }

        // Sort by frags desc
        players.sort((a, b) => b.frags - a.frags);

        const teamFrags = players.reduce((s, p) => s + p.frags, 0);
        const teamHealth = players.reduce((s, p) => s + (p.health || 0), 0);
        const teamArmor = players.reduce((s, p) => s + (p.armor || 0), 0);

        const hubInfo = currentResult?.hubInfo;
        const playerUserIDs = currentResult?.timelineAnalysis?.playerUserIDs || {};

        let html = `<h4>${team} — ${teamFrags} frags</h4>`;
        html += `<table class="team-status-table">`;
        html += `<tr><th>Player</th><th>Frags</th><th>Health</th><th>Armor</th><th>Weapons</th><th>View</th></tr>`;

        for (const p of players) {
            const hubLink = buildHubWatchLink(p.name, time, hubInfo, playerUserIDs);

            if (p.dead) {
                html += `<tr>`;
                html += `<td>${escapeHtml(p.name)}</td>`;
                html += `<td>${p.frags}</td>`;
                html += `<td>-</td>`;
                html += `<td>-</td>`;
                html += `<td>-</td>`;
                html += `<td>${hubLink}</td>`;
                html += `</tr>`;
            } else {
                const hp = p.health || 0;
                const arm = p.armor || 0;
                const at = p.armorType || '';
                const armorClass = at ? `armor-${at}` : '';
                const armorStr = arm > 0 ? `<span class="${armorClass}">${arm} ${at.toUpperCase()}</span>` : '0';

                const weps = [];
                if (p.hasRL && p.hasLG) weps.push('RL+LG');
                else if (p.hasRL) weps.push('RL');
                else if (p.hasLG) weps.push('LG');
                if (p.hasQuad) weps.push('Quad');
                if (p.hasPent) weps.push('Pent');
                if (p.hasRing) weps.push('Ring');

                html += `<tr>`;
                html += `<td>${escapeHtml(p.name)}</td>`;
                html += `<td>${p.frags}</td>`;
                html += `<td>${hp}</td>`;
                html += `<td>${armorStr}</td>`;
                html += `<td>${weps.join(', ') || '-'}</td>`;
                html += `<td>${hubLink}</td>`;
                html += `</tr>`;
            }
        }

        // Totals row
        html += `<tr class="totals-row">`;
        html += `<td>Total</td>`;
        html += `<td>${teamFrags}</td>`;
        html += `<td>${teamHealth}</td>`;
        html += `<td>${teamArmor}</td>`;
        html += `<td></td>`;
        html += `<td></td>`;
        html += `</tr>`;

        html += `</table>`;
        container.innerHTML = html;
    }
}

// ─── Hub Watch Link Helper ──────────────────────────────────────────────────

function buildHubWatchLink(playerName, time, hubInfo, playerUserIDs) {
    if (!hubInfo || !hubInfo.gameId) return '';
    const trackId = playerUserIDs[playerName];
    if (!trackId) return '';
    // Our times are match-relative (0 = match start). Hub uses demo-relative time
    // (includes countdown/warmup), so add demoOffset to convert.
    const from = Math.floor(time + (timelineState.demoOffset || 0));
    const url = `https://hub.quakeworld.nu/games/?gameId=${hubInfo.gameId}&from=${from}&track=${trackId}`;
    return `<a href="${url}" target="_blank" class="hub-watch-link" title="Watch in Hub">hub</a>`;
}

// ─── Location Lookup ────────────────────────────────────────────────────────

function findNearestLocation(x, y, locations) {
    if (!locations || locations.length === 0) return '';
    let bestDist = Infinity;
    let bestName = '';
    for (const loc of locations) {
        const dx = x - loc.x, dy = y - loc.y;
        const d = dx * dx + dy * dy;
        if (d < bestDist) {
            bestDist = d;
            bestName = loc.name;
        }
    }
    return bestName;
}

// ─── Precomputed Frag Counts ────────────────────────────────────────────────

// Sorted array of { time, cumulative: { player: frags } }
// Built once per demo load; looked up via binary search.
let precomputedFrags = []; // [{ time, cumulative }]

function precomputeFragCounts() {
    const fragEvents = timelineState.fragEvents || [];
    precomputedFrags = [];
    if (fragEvents.length === 0) return;

    const sorted = fragEvents.slice().sort((a, b) => a.time - b.time);
    const running = {}; // player -> cumulative frags

    for (const fe of sorted) {
        running[fe.player] = (running[fe.player] || 0) + (fe.delta || 1);
        precomputedFrags.push({ time: fe.time, cumulative: { ...running } });
    }
}

function getFragsAtTime(time) {
    if (precomputedFrags.length === 0) return {};
    // Binary search for last entry with time <= target
    let lo = 0, hi = precomputedFrags.length - 1;
    if (time < precomputedFrags[0].time) return {};
    while (lo < hi) {
        const mid = (lo + hi + 1) >> 1;
        if (precomputedFrags[mid].time <= time) lo = mid;
        else hi = mid - 1;
    }
    return precomputedFrags[lo].cumulative;
}

// =============================================================================
// Map Visualization
// =============================================================================

// Item keywords that should remain uppercase in location names
const ITEM_KEYWORDS = ['RA', 'YA', 'GA', 'MH', 'RL', 'LG', 'GL', 'NG', 'SNG', 'SSG', 'SG', 'MEGA', 'QUAD', 'PENT', 'RING'];

// Normalize location name: "RA.below" → "RA.below", "Quad low" → "QUAD.low"
function normalizeLocationName(name) {
    return name
        .trim()
        .replace(/[\s-]+/g, '.')
        .split('.')
        .map(part => {
            const upper = part.toUpperCase();
            return ITEM_KEYWORDS.includes(upper) ? upper : part.toLowerCase();
        })
        .join('.');
}

// Get color for location based on item type in name
function getLocationColor(name) {
    const nameLower = name.toLowerCase();

    // Powerups - bright colors (dimmed 50%)
    if (nameLower.includes('quad'))  return { fill: 'rgba(80, 120, 255, 0.075)', stroke: 'rgba(80, 120, 255, 0.5)', text: 'rgba(112, 144, 255, 0.5)' };
    if (nameLower.includes('pent'))  return { fill: 'rgba(255, 0, 255, 0.075)', stroke: 'rgba(255, 0, 255, 0.5)', text: 'rgba(255, 102, 255, 0.5)' };
    if (nameLower.includes('ring'))  return { fill: 'rgba(255, 255, 0, 0.075)', stroke: 'rgba(255, 255, 0, 0.5)', text: 'rgba(255, 255, 102, 0.5)' };

    // Armors
    if (nameLower.includes('ra'))    return { fill: 'rgba(255, 80, 80, 0.075)', stroke: 'rgba(255, 80, 80, 0.5)', text: 'rgba(255, 128, 128, 0.5)' };
    if (nameLower.includes('ya'))    return { fill: 'rgba(255, 200, 50, 0.075)', stroke: 'rgba(255, 200, 50, 0.5)', text: 'rgba(255, 216, 102, 0.5)' };
    if (nameLower.includes('ga'))    return { fill: 'rgba(80, 200, 80, 0.075)', stroke: 'rgba(80, 200, 80, 0.5)', text: 'rgba(128, 216, 128, 0.5)' };

    // Health
    if (nameLower.includes('mh'))    return { fill: 'rgba(80, 200, 255, 0.075)', stroke: 'rgba(80, 200, 255, 0.5)', text: 'rgba(128, 216, 255, 0.5)' };

    // Weapons
    if (nameLower.includes('rl'))    return { fill: 'rgba(200, 100, 50, 0.06)', stroke: 'rgba(200, 100, 50, 0.5)', text: 'rgba(216, 128, 80, 0.5)' };
    if (nameLower.includes('lg'))    return { fill: 'rgba(150, 150, 255, 0.06)', stroke: 'rgba(150, 150, 255, 0.5)', text: 'rgba(176, 176, 255, 0.5)' };
    if (nameLower.includes('gl'))    return { fill: 'rgba(100, 180, 100, 0.06)', stroke: 'rgba(100, 180, 100, 0.5)', text: 'rgba(128, 200, 128, 0.5)' };
    if (nameLower.includes('sng') || nameLower.includes('ng'))
                                     return { fill: 'rgba(180, 140, 80, 0.06)', stroke: 'rgba(180, 140, 80, 0.5)', text: 'rgba(200, 160, 96, 0.5)' };

    // Default - subtle gray
    return { fill: 'rgba(100, 100, 120, 0.04)', stroke: 'rgba(68, 68, 68, 0.5)', text: 'rgba(102, 102, 102, 0.5)' };
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
        const pos = worldToCanvasFunc(group.points[0].x, group.points[0].y);
        ctx.beginPath();
        ctx.arc(pos.x, pos.y, 12, 0, Math.PI * 2);
        ctx.fillStyle = group.color.fill;
        ctx.fill();
    } else {
        const canvasPoints = group.points.map(p => worldToCanvasFunc(p.x, p.y));
        const hull = computeConvexHull(canvasPoints);

        if (hull.length < 3) {
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
        } else {
            const expanded = expandPolygon(hull, 15);
            ctx.beginPath();
            ctx.moveTo(expanded[0].x, expanded[0].y);
            for (let i = 1; i < expanded.length; i++) {
                ctx.lineTo(expanded[i].x, expanded[i].y);
            }
            ctx.closePath();
            ctx.fillStyle = group.color.fill;
            ctx.fill();
        }
    }
}

// Draw control overlay for regions based on current control state
function drawRegionControlOverlay(ctx, controlStates) {


    for (const [regionName, state] of Object.entries(controlStates)) {
        const groups = mapState.regionToGroups[regionName];
        if (!groups || groups.length === 0) continue;

        let color;
        switch (state) {
            case 'teamAControl':
                color = hexToRgba(TEAM_COLORS[0], 0.15);
                break;
            case 'teamAWeakControl':
                color = hexToRgba(TEAM_COLORS[0], 0.08);
                break;
            case 'teamBControl':
                color = hexToRgba(TEAM_COLORS[1], 0.15);
                break;
            case 'teamBWeakControl':
                color = hexToRgba(TEAM_COLORS[1], 0.08);
                break;
            case 'contested':
                color = 'rgba(255, 255, 255, 0.08)';
                break;
            default: // empty
                continue;
        }

        for (const group of groups) {
            drawLocationRegionFill(ctx, group, color);
        }
    }
}

function drawLocationRegionFill(ctx, group, fillColor) {
    if (group.points.length === 1) {
        const pos = worldToCanvasNew(group.points[0].x, group.points[0].y);
        ctx.beginPath();
        ctx.arc(pos.x, pos.y, 14, 0, Math.PI * 2);
        ctx.fillStyle = fillColor;
        ctx.fill();
    } else {
        const canvasPoints = group.points.map(p => worldToCanvasNew(p.x, p.y));
        const hull = computeConvexHull(canvasPoints);
        if (hull.length < 3) {
            let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
            for (const p of canvasPoints) {
                minX = Math.min(minX, p.x); maxX = Math.max(maxX, p.x);
                minY = Math.min(minY, p.y); maxY = Math.max(maxY, p.y);
            }
            const pad = 17;
            ctx.fillStyle = fillColor;
            ctx.fillRect(minX - pad, minY - pad, maxX - minX + pad*2, maxY - minY + pad*2);
        } else {
            const expanded = expandPolygon(hull, 17);
            ctx.beginPath();
            ctx.moveTo(expanded[0].x, expanded[0].y);
            for (let i = 1; i < expanded.length; i++) ctx.lineTo(expanded[i].x, expanded[i].y);
            ctx.closePath();
            ctx.fillStyle = fillColor;
            ctx.fill();
        }
    }
}

function hexToRgba(hex, alpha) {
    const [r, g, b] = hexToRgb(hex);
    return `rgba(${r}, ${g}, ${b}, ${alpha})`;
}

// Map View State
let mapState = {
    canvas: null,
    ctx: null,
    locations: [],
    locationGroups: null, // Cached processed location groups
    locationCanvas: null, // Pre-rendered location background layer
    bounds: { minX: 0, maxX: 0, minY: 0, maxY: 0 },
    currentTime: 0,
    isPlaying: false,
    playbackSpeed: 1,
    animationFrameId: null,
    lastRenderTime: 0,
    trailDuration: 10,          // Current trail window in seconds
    fullTrails: {},             // playerName -> [{x, y, t, teamIdx, tp}] — pre-computed from all buckets
    trailStartTimes: {},        // playerName -> time when trail tracking started (for extending forward)
    enabledPlayers: {},         // playerName -> boolean — per-player trail toggle
    teams: [],
    playerSymbols: {}, // playerName -> { symbol, team, teamIdx }
    initialized: false,
    lastRenderedBucket: null, // Skip redundant redraws
    renderDirty: false        // Force redraw on track toggle/reset/etc
};

const PLAYER_SYMBOLS = ['*', 'x', '+', 'o', '◆', '▲', '●', '■'];

// Badge definitions: angle (0=up, clockwise), letter, color (from timeline legend)
const BADGE_DEFS = [
    { angle:   0, key: 'q',   letter: 'Q', color: 'rgb(0, 150, 255)' },
    { angle:  45, key: 'rl',  letter: 'R', color: 'rgb(255, 107, 107)' },
    { angle:  90, key: 'lg',  letter: 'L', color: 'rgb(0, 217, 255)' },
    { angle: 135, key: 'sng', letter: 'N', color: 'rgb(180, 140, 100)' },
    { angle: 180, key: 'mh',  letter: 'M', color: 'rgb(0, 200, 83)' },
    { angle: 225, key: 'arm', letter: 'A', color: null },
    { angle: 270, key: 'pe',  letter: 'P', color: 'rgb(255, 0, 0)' },
    { angle: 315, key: 'r',   letter: 'I', color: 'rgb(255, 235, 59)' },
];
const ARMOR_COLORS = { ra: 'rgb(255, 50, 50)', ya: 'rgb(255, 200, 0)', ga: 'rgb(0, 180, 0)' };

function getActiveBadges(data) {
    const badges = [];
    for (const def of BADGE_DEFS) {
        let active = false, color = def.color, letter = def.letter;
        switch (def.key) {
            case 'q':   active = !!data.q; break;
            case 'rl':  active = !!data.rl; break;
            case 'lg':  active = !!data.lg; break;
            case 'sng':
                if (data.sng) { active = true; letter = 'N'; }
                else if (data.ssg) { active = true; letter = 'S'; }
                break;
            case 'mh':  active = data.h > 100; break;
            case 'arm':
                if (data.at) {
                    active = true;
                    color = ARMOR_COLORS[data.at] || 'rgb(180, 180, 180)';
                    letter = data.at.toUpperCase();
                }
                break;
            case 'pe':  active = !!data.pe; break;
            case 'r':   active = !!data.r; break;
        }
        if (active) badges.push({ angle: def.angle, letter, color });
    }
    return badges;
}

function drawBadge(ctx, letter, color, x, y, radius) {
    ctx.beginPath();
    ctx.arc(x, y, radius, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();
    ctx.font = `bold ${Math.round(radius * 1.2)}px monospace`;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillStyle = '#000';
    ctx.fillText(letter, x, y);
}

function drawBadgesAroundCenter(ctx, badges, cx, cy, orbitRadius, badgeRadius) {
    for (const b of badges) {
        const rad = (b.angle - 90) * Math.PI / 180;
        const bx = cx + orbitRadius * Math.cos(rad);
        const by = cy + orbitRadius * Math.sin(rad);
        drawBadge(ctx, b.letter, b.color, bx, by, badgeRadius);
    }
}

function markMapDirty() {
    mapState.renderDirty = true;
}

function initMapView(result) {
    if (!result.timelineAnalysis) return;

    mapState.canvas = document.getElementById('map-canvas');
    if (!mapState.canvas) return;
    mapState.ctx = mapState.canvas.getContext('2d');

    // Get location data from timeline analysis
    const timeline = result.timelineAnalysis;
    mapState.locations = timeline.locationData || [];
    mapState.locationGroups = null; // Clear cached groups for new demo
    mapState.locationCanvas = null;

    // Show/hide no-data message
    const noDataMsg = document.getElementById('map-no-data');
    if (noDataMsg) {
        noDataMsg.style.display = mapState.locations.length === 0 ? 'block' : 'none';
    }

    // Calculate bounds from locations and player positions
    calculateMapBounds(result);

    // Size canvas to fit map content at full width
    const worldW = mapState.bounds.maxX - mapState.bounds.minX;
    const worldH = mapState.bounds.maxY - mapState.bounds.minY;
    const canvasW = 850;
    const canvasH = worldW > 0 ? Math.round(Math.max(400, Math.min(850, canvasW * (worldH / worldW)))) : 700;
    mapState.canvas.width = canvasW;
    mapState.canvas.height = canvasH;
    updateWorldToCanvasTransform();

    // Use the canonical frag-sorted team order set in displayResults
    if (timelineState.teams && timelineState.teams.length >= 2) {
        mapState.teams = [...timelineState.teams];
    } else if (result.demoInfo?.teams) {
        mapState.teams = result.demoInfo.teams;
    } else if (result.match?.teams) {
        mapState.teams = result.match.teams.map(t => t.name);
    } else {
        mapState.teams = [];
    }

    // Assign symbols to players
    assignPlayerSymbols(result);

    // Set up trail controls (only once)
    if (!mapState.initialized) {
        setupMapTrailControls();
        mapState.initialized = true;
    }

    // Pre-compute full trails from high-res bucket data
    precomputeFullTrails();

    // Build powerup event list
    buildMapPowerupList(result);

    // Reset trail checkboxes
    document.querySelectorAll('.map-player-trail-cb').forEach(cb => { cb.checked = false; });

    // Initial render at match start
    mapState.currentTime = 0;
    const slider = document.getElementById('map-timeline-slider');
    if (slider) slider.value = 0;

    // Initialize region control data
    initRegionControl(result);

    renderMap(mapState.currentTime);
}

// Early init of region control data (before timeline renders, before map init)
function initRegionControlData(result) {
    const rc = result.timelineAnalysis?.regionControl;
    if (!rc || !rc.regions || rc.regions.length === 0) return;

    // Ensure locations are available
    if (!mapState.locations || mapState.locations.length === 0) {
        mapState.locations = result.timelineAnalysis?.locationData || [];
    }

    // Set control regions and locToRegion from backend definitions
    mapState.controlRegions = rc.regions;
    mapState.rcResult = rc;
    mapState.locToRegion = {};
    for (const region of rc.regions) {
        for (const pt of region.points) {
            mapState.locToRegion[pt.name] = region.name;
        }
    }
}

function initRegionControl(result) {
    const rc = result.timelineAnalysis?.regionControl;
    const panel = document.getElementById('region-control-panel');
    const statusPanel = document.getElementById('region-status-panel');
    if (!rc || !rc.regions || rc.regions.length === 0) {
        if (panel) panel.style.display = 'none';
        if (statusPanel) statusPanel.style.display = 'none';
        mapState.controlRegions = null;
        return;
    }

    // Store the original backend result and all locations for recomputation
    mapState.rcResult = rc;

    // Ensure location groups are processed
    if (!mapState.locationGroups && mapState.locations.length > 0) {
        mapState.locationGroups = processLocationGroups(mapState.locations);
    }

    // Build region config UI (editable text fields per region)
    buildRegionConfig(rc.regions);

    // Apply regions (builds lookups, computes stats, renders table)
    applyRegionConfig();

    if (panel) panel.style.display = '';
    if (statusPanel) statusPanel.style.display = '';
}

function buildRegionConfig(regions) {
    const container = document.getElementById('region-config');
    if (!container) return;
    container.innerHTML = '';

    for (const region of regions) {
        const locNames = [...new Set(region.points.map(p => p.name))].join(', ');
        const row = document.createElement('div');
        row.className = 'region-config-row';
        row.innerHTML = `
            <label>${escapeHtml(region.name)}:</label>
            <input type="text" class="region-locs-input" data-region="${escapeHtml(region.name)}" value="${escapeHtml(locNames)}">
        `;
        container.appendChild(row);
    }

    // On change, recompute
    container.querySelectorAll('.region-locs-input').forEach(input => {
        input.addEventListener('change', () => applyRegionConfig());
    });
}

function applyRegionConfig() {
    const rc = mapState.rcResult;
    if (!rc) return;

    // Read current region definitions from the text inputs
    const regions = [];
    document.querySelectorAll('.region-locs-input').forEach(input => {
        const regionName = input.dataset.region;
        const locNames = input.value.split(',').map(s => s.trim()).filter(s => s);

        // Find matching locations from the full loc list
        const locSet = new Set(locNames);
        const points = [];
        let sumX = 0, sumY = 0;
        for (const loc of mapState.locations) {
            if (locSet.has(loc.name)) {
                points.push({ x: loc.x, y: loc.y, z: loc.z, name: loc.name });
                sumX += loc.x;
                sumY += loc.y;
            }
        }
        if (points.length > 0) {
            regions.push({
                name: regionName,
                points: points,
                centroidX: sumX / points.length,
                centroidY: sumY / points.length,
            });
        }
    });

    mapState.controlRegions = regions;

    // Build loc-name-to-region lookup
    mapState.locToRegion = {};
    for (const region of regions) {
        for (const pt of region.points) {
            mapState.locToRegion[pt.name] = region.name;
        }
    }

    // Build region-to-location-group mapping for coloring
    mapState.regionToGroups = {};
    if (mapState.locationGroups) {
        for (const group of mapState.locationGroups) {
            for (const region of regions) {
                let matched = false;
                for (const gpt of group.points) {
                    for (const rpt of region.points) {
                        const dx = gpt.x - rpt.x, dy = gpt.y - rpt.y;
                        if (dx * dx + dy * dy < 1) {
                            matched = true;
                            break;
                        }
                    }
                    if (matched) break;
                }
                if (matched) {
                    if (!mapState.regionToGroups[region.name]) {
                        mapState.regionToGroups[region.name] = [];
                    }
                    mapState.regionToGroups[region.name].push(group);
                }
            }
        }
    }

    // Recompute stats from high-res buckets
    recomputeRegionStats(regions);

    // Force map redraw
    mapState.renderDirty = true;
    renderMap(mapState.currentTime);

    // Re-render timeline region control graph
    updateDetailView();
}

function recomputeRegionStats(regions) {
    const buckets = timelineState.highResBuckets;
    if (!buckets || buckets.length === 0 || regions.length === 0) return;

    const teams = mapState.teams || [];
    if (teams.length < 2) return;
    const teamA = teams[0], teamB = teams[1];

    // Initialize counters
    const counters = {};
    for (const r of regions) {
        counters[r.name] = { aC: 0, aW: 0, con: 0, emp: 0, bW: 0, bC: 0 };
    }

    let total = 0;
    const locations = mapState.locations;

    for (const bucket of buckets) {
        const playerData = bucket.p || bucket.playerData;
        if (!playerData) continue;
        total++;

        // Per-region presence
        const presence = {};
        for (const r of regions) {
            presence[r.name] = { aWpn: 0, aNo: 0, bWpn: 0, bNo: 0 };
        }

        for (const [name, data] of Object.entries(playerData)) {
            if (data.d || (data.h !== undefined && data.h <= 0)) continue;
            if (data.x === 0 && data.y === 0) continue;

            const nearest = findNearestLocation(data.x, data.y, locations);
            if (!nearest) continue;
            const regionName = mapState.locToRegion[nearest];
            if (!regionName || !presence[regionName]) continue;

            const sym = mapState.playerSymbols[name];
            const playerTeam = sym ? teams[sym.teamIdx] : null;
            const hasWeapon = data.rl || data.lg;
            const p = presence[regionName];

            if (playerTeam === teamA) {
                if (hasWeapon) p.aWpn++; else p.aNo++;
            } else if (playerTeam === teamB) {
                if (hasWeapon) p.bWpn++; else p.bNo++;
            }
        }

        for (const r of regions) {
            const p = presence[r.name];
            const c = counters[r.name];
            const aT = p.aWpn + p.aNo, bT = p.bWpn + p.bNo;

            if (aT === 0 && bT === 0) { c.emp++; }
            else if (aT > 0 && bT === 0) { if (p.aWpn > 0) c.aC++; else c.aW++; }
            else if (bT > 0 && aT === 0) { if (p.bWpn > 0) c.bC++; else c.bW++; }
            else if (p.aWpn > 0 && p.bWpn === 0) { c.aC++; }
            else if (p.bWpn > 0 && p.aWpn === 0) { c.bC++; }
            else { c.con++; }
        }
    }

    if (total === 0) return;

    // Build stats and display
    const pct = (v) => Math.round(v / total * 1000) / 10;
    const stats = {};
    for (const r of regions) {
        const c = counters[r.name];
        stats[r.name] = {
            teamAControl: pct(c.aC), teamAWeakControl: pct(c.aW),
            contested: pct(c.con), empty: pct(c.emp),
            teamBWeakControl: pct(c.bW), teamBControl: pct(c.bC),
            teamA, teamB,
        };
    }

    mapState.controlStats = stats;
    displayRegionControlTable(regions, stats);
}

function displayRegionControlTable(regions, stats) {
    const tbody = document.getElementById('region-control-body');
    if (!tbody) return;
    tbody.innerHTML = '';

    const firstStats = Object.values(stats)[0];
    if (firstStats) {
        const teamA = firstStats.teamA || 'Team A';
        const teamB = firstStats.teamB || 'Team B';
        document.getElementById('rc-teamA-hdr').textContent = teamA;
        document.getElementById('rc-teamA-weak-hdr').textContent = teamA + ' weak';
        document.getElementById('rc-teamB-hdr').textContent = teamB;
        document.getElementById('rc-teamB-weak-hdr').textContent = teamB + ' weak';
    }



    for (const region of regions) {
        const s = stats[region.name];
        if (!s) continue;
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td><strong>${escapeHtml(region.name)}</strong></td>
            <td style="background: ${cellBg(TEAM_COLORS[0], s.teamAControl)}">${s.teamAControl}%</td>
            <td style="background: ${cellBg(TEAM_COLORS[0], s.teamAWeakControl, 0.5)}">${s.teamAWeakControl}%</td>
            <td style="background: ${cellBg('#888', s.contested)}">${s.contested}%</td>
            <td>${s.empty}%</td>
            <td style="background: ${cellBg(TEAM_COLORS[1], s.teamBWeakControl, 0.5)}">${s.teamBWeakControl}%</td>
            <td style="background: ${cellBg(TEAM_COLORS[1], s.teamBControl)}">${s.teamBControl}%</td>
        `;
        tbody.appendChild(tr);
    }
}

function cellBg(color, pct, intensityScale) {
    if (!pct || pct <= 0) return 'transparent';
    // Parse hex color to RGB
    const r = parseInt(color.slice(1, 3), 16);
    const g = parseInt(color.slice(3, 5), 16);
    const b = parseInt(color.slice(5, 7), 16);
    const alpha = Math.min(0.4, (pct / 100) * 0.6) * (intensityScale || 1);
    return `rgba(${r}, ${g}, ${b}, ${alpha.toFixed(2)})`;
}

// Compute real-time region control state at a given time
function getRegionControlAtTime(time) {
    if (!mapState.controlRegions || !mapState.locToRegion) return null;

    const bucket = findBucketAtTime(time);
    if (!bucket) return null;

    const playerData = bucket.p || bucket.playerData;
    if (!playerData) return null;

    const teams = mapState.teams || [];
    if (teams.length < 2) return null;
    const teamA = teams[0], teamB = teams[1];

    // Per-region presence
    const presence = {};
    for (const region of mapState.controlRegions) {
        presence[region.name] = { aWpn: 0, aNo: 0, bWpn: 0, bNo: 0 };
    }

    // Check each player
    const locations = mapState.locations;
    for (const [name, data] of Object.entries(playerData)) {
        if (data.d || (data.h !== undefined && data.h <= 0)) continue; // dead
        if (data.x === 0 && data.y === 0) continue;

        // Find nearest location
        const nearest = findNearestLocation(data.x, data.y, locations);
        if (!nearest) continue;

        const regionName = mapState.locToRegion[nearest];
        if (!regionName) continue;

        const p = presence[regionName];
        if (!p) continue;

        const hasWeapon = data.rl || data.lg;
        const sym = mapState.playerSymbols[name];
        const playerTeam = sym ? teams[sym.teamIdx] : null;

        if (playerTeam === teamA) {
            if (hasWeapon) p.aWpn++; else p.aNo++;
        } else if (playerTeam === teamB) {
            if (hasWeapon) p.bWpn++; else p.bNo++;
        }
    }

    // Determine state per region
    const states = {};
    for (const region of mapState.controlRegions) {
        const p = presence[region.name];
        const aTotal = p.aWpn + p.aNo;
        const bTotal = p.bWpn + p.bNo;

        if (aTotal === 0 && bTotal === 0) {
            states[region.name] = 'empty';
        } else if (aTotal > 0 && bTotal === 0) {
            states[region.name] = p.aWpn > 0 ? 'teamAControl' : 'teamAWeakControl';
        } else if (bTotal > 0 && aTotal === 0) {
            states[region.name] = p.bWpn > 0 ? 'teamBControl' : 'teamBWeakControl';
        } else if (p.aWpn > 0 && p.bWpn === 0) {
            states[region.name] = 'teamAControl';
        } else if (p.bWpn > 0 && p.aWpn === 0) {
            states[region.name] = 'teamBControl';
        } else {
            states[region.name] = 'contested';
        }
    }
    return states;
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
    updateWorldToCanvasTransform();
}

// Precomputed transform parameters — call updateWorldToCanvasTransform() when bounds/canvas change
let _wtc = { scale: 1, offsetX: 0, offsetY: 0, minX: 0, minY: 0, canvasH: 0 };

function updateWorldToCanvasTransform() {
    const { minX, maxX, minY, maxY } = mapState.bounds;
    const canvas = mapState.canvas;
    if (!canvas) return;
    const worldWidth = maxX - minX;
    const worldHeight = maxY - minY;
    const scale = Math.min(canvas.width / worldWidth, canvas.height / worldHeight);
    _wtc.scale = scale;
    _wtc.offsetX = (canvas.width - worldWidth * scale) / 2;
    _wtc.offsetY = (canvas.height - worldHeight * scale) / 2;
    _wtc.minX = minX;
    _wtc.minY = minY;
    _wtc.canvasH = canvas.height;
}

// Reusable point to avoid GC — only use for immediate consumption, not storage
const _tmpPt = { x: 0, y: 0 };

function worldToCanvas(x, y) {
    _tmpPt.x = _wtc.offsetX + (x - _wtc.minX) * _wtc.scale;
    _tmpPt.y = _wtc.canvasH - (_wtc.offsetY + (y - _wtc.minY) * _wtc.scale);
    return _tmpPt;
}

// Allocating version for cases where result is stored (e.g., tracks, caching)
function worldToCanvasNew(x, y) {
    return {
        x: _wtc.offsetX + (x - _wtc.minX) * _wtc.scale,
        y: _wtc.canvasH - (_wtc.offsetY + (y - _wtc.minY) * _wtc.scale)
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

    // Assign unique first-letter symbols and pre-render to offscreen canvases
    const usedLetters = new Set();
    const allPlayers = [];
    for (let teamIdx = 0; teamIdx < mapState.teams.length; teamIdx++) {
        const team = mapState.teams[teamIdx];
        for (const name of (teamPlayers[team] || [])) {
            allPlayers.push({ name, team, teamIdx });
        }
    }

    // Assign unique letter per player: first unused letter from their name
    for (const player of allPlayers) {
        let letter = '?';
        for (const ch of player.name) {
            const upper = ch.toUpperCase();
            if (upper >= 'A' && upper <= 'Z' && !usedLetters.has(upper)) {
                letter = upper;
                usedLetters.add(upper);
                break;
            }
        }
        if (letter === '?') letter = player.name[0]?.toUpperCase() || '?';

        const teamColor = TEAM_COLORS[player.teamIdx] || TEAM_COLORS[0];
        // Pre-render letter with circle to offscreen canvas
        const size = 32;
        const offscreen = document.createElement('canvas');
        offscreen.width = size;
        offscreen.height = size;
        const octx = offscreen.getContext('2d');
        const cx = size / 2, cy = size / 2, r = 13;

        // Circle background
        octx.beginPath();
        octx.arc(cx, cy, r, 0, Math.PI * 2);
        octx.fillStyle = hexToRgba(teamColor, 0.25);
        octx.fill();
        octx.strokeStyle = teamColor;
        octx.lineWidth = 2;
        octx.stroke();

        // Letter
        octx.font = 'bold 16px monospace';
        octx.textAlign = 'center';
        octx.textBaseline = 'middle';
        octx.fillStyle = teamColor;
        octx.fillText(letter, cx, cy);

        mapState.playerSymbols[player.name] = {
            symbol: letter,
            team: player.team,
            teamIdx: player.teamIdx,
            symbolCanvas: offscreen
        };
    }

    // Build legend
    buildMapLegend();
}

function buildMapLegend() {
    const legend = document.getElementById('map-legend');
    if (!legend) return;

    legend.innerHTML = '';

    for (let teamIdx = 0; teamIdx < mapState.teams.length; teamIdx++) {
        const team = mapState.teams[teamIdx];
        const teamHex = TEAM_COLORS[teamIdx] || TEAM_COLORS[0];

        const title = document.createElement('h4');
        title.style.color = teamHex;
        title.id = `map-legend-team-title-${teamIdx}`;
        title.textContent = `${team} — 0 frags`;
        legend.appendChild(title);

        const table = document.createElement('table');
        table.className = 'team-status-table';
        table.innerHTML = `<thead><tr><th></th><th>Player</th><th>Trail</th><th>H</th><th>A</th><th>Wpn</th><th>View</th></tr></thead>`;
        const tbody = document.createElement('tbody');
        tbody.className = 'map-legend-tbody';

        for (const [name, info] of Object.entries(mapState.playerSymbols)) {
            if (info.team === team) {
                const tr = document.createElement('tr');
                tr.dataset.player = name;
                const escapedName = escapeHtml(name);
                tr.innerHTML = `
                    <td><span class="map-legend-symbol" style="color: ${teamHex}">${info.symbol}</span></td>
                    <td>${escapedName}</td>
                    <td class="map-trail-cell"><input type="checkbox" class="map-player-trail-cb" data-player="${escapedName}"></td>
                    <td class="map-legend-health" data-player="${escapedName}">-</td>
                    <td class="map-legend-armor" data-player="${escapedName}">-</td>
                    <td class="map-legend-wpn" data-player="${escapedName}">-</td>
                    <td class="map-legend-hub" data-player="${escapedName}"></td>
                `;
                tbody.appendChild(tr);
            }
        }

        table.appendChild(tbody);
        legend.appendChild(table);
    }

    // Attach per-player trail toggle handlers
    legend.querySelectorAll('.map-player-trail-cb').forEach(cb => {
        cb.addEventListener('change', (e) => {
            const playerName = e.target.dataset.player;
            mapState.enabledPlayers[playerName] = e.target.checked;
            if (e.target.checked) {
                mapState.trailStartTimes[playerName] = mapState.currentTime;
            }
            mapState.renderDirty = true;
            renderMap(mapState.currentTime);
        });
    });

    // Make tables sortable
    legend.querySelectorAll('.team-status-table').forEach(makeSortable);
}

function updateMapLegend() {
    const legend = document.getElementById('map-legend');
    if (!legend) return;

    const time = mapState.currentTime;
    const bucket = findBucketAtTime(time);
    const playerData = bucket ? (bucket.p || bucket.playerData) : null;
    const hubInfo = currentResult?.hubInfo;
    const playerUserIDs = currentResult?.timelineAnalysis?.playerUserIDs || {};
    const fragCounts = typeof getFragsAtTime === 'function' ? getFragsAtTime(time) : {};

    // Update team titles with frag counts
    for (let ti = 0; ti < mapState.teams.length; ti++) {
        const titleEl = document.getElementById(`map-legend-team-title-${ti}`);
        if (!titleEl) continue;
        const team = mapState.teams[ti];
        let teamFrags = 0;
        for (const [name, info] of Object.entries(mapState.playerSymbols)) {
            if (info.team === team) teamFrags += fragCounts[name] || 0;
        }
        titleEl.textContent = `${team} — ${teamFrags} frags`;
    }

    // Update per-player cells
    const healthCells = legend.querySelectorAll('.map-legend-health');
    for (const cell of healthCells) {
        const name = cell.dataset.player;
        const data = playerData?.[name];
        cell.textContent = data ? (data.h ?? data.health ?? '-') : '-';
    }

    const armorCells = legend.querySelectorAll('.map-legend-armor');
    for (const cell of armorCells) {
        const name = cell.dataset.player;
        const data = playerData?.[name];
        if (data && (data.a ?? data.armor) > 0) {
            const armorVal = data.a ?? data.armor;
            const armorType = data.at ?? data.armorType ?? '';
            cell.innerHTML = armorType
                ? `<span class="armor-${armorType}">${armorVal} ${armorType.toUpperCase()}</span>`
                : `${armorVal}`;
        } else {
            cell.textContent = '-';
        }
    }

    const wpnCells = legend.querySelectorAll('.map-legend-wpn');
    for (const cell of wpnCells) {
        const name = cell.dataset.player;
        const data = playerData?.[name];
        if (data) {
            const wpns = [];
            if (data.rl ?? data.hasRL) wpns.push('RL');
            if (data.lg ?? data.hasLG) wpns.push('LG');
            cell.textContent = wpns.length > 0 ? wpns.join(' ') : '-';
        } else {
            cell.textContent = '-';
        }
    }

    const hubCells = legend.querySelectorAll('.map-legend-hub');
    for (const cell of hubCells) {
        const name = cell.dataset.player;
        cell.innerHTML = buildHubWatchLink(name, time, hubInfo, playerUserIDs);
    }
}

function updateRegionStatus() {
    const container = document.getElementById('region-status-body');
    if (!container || !mapState.controlRegions || mapState.controlRegions.length === 0) return;
    container.innerHTML = '';

    const time = mapState.currentTime;
    const controlStates = getRegionControlAtTime(time);
    if (!controlStates) return;

    const bucket = findBucketAtTime(time);
    const playerData = bucket ? (bucket.p || bucket.playerData) : null;
    const teams = mapState.teams || [];

    const locations = mapState.locations;

    // Build per-region player lists
    const regionPlayers = {};
    for (const r of mapState.controlRegions) {
        regionPlayers[r.name] = [];
    }

    if (playerData) {
        for (const [name, data] of Object.entries(playerData)) {
            if (data.d || (data.h !== undefined && data.h <= 0)) continue;
            if (data.x === 0 && data.y === 0) continue;

            const nearest = findNearestLocation(data.x, data.y, locations);
            if (!nearest) continue;
            const regionName = mapState.locToRegion?.[nearest];
            if (!regionName || !regionPlayers[regionName]) continue;

            const sym = mapState.playerSymbols[name];
            regionPlayers[regionName].push({
                name, data, sym,
                teamIdx: sym ? sym.teamIdx : -1,
                hasRL: data.rl || false,
                hasLG: data.lg || false,
            });
        }
    }

    // Build HTML
    let html = '';
    for (const region of mapState.controlRegions) {
        const state = controlStates[region.name] || 'empty';
        const players = regionPlayers[region.name] || [];

        // Status label and color
        let statusLabel, statusColor;
        switch (state) {
            case 'teamAControl':
                statusLabel = teams[0] || 'A';
                statusColor = TEAM_COLORS[0];
                break;
            case 'teamAWeakControl':
                statusLabel = (teams[0] || 'A') + ' (weak)';
                statusColor = TEAM_COLORS[0];
                break;
            case 'teamBControl':
                statusLabel = teams[1] || 'B';
                statusColor = TEAM_COLORS[1];
                break;
            case 'teamBWeakControl':
                statusLabel = (teams[1] || 'B') + ' (weak)';
                statusColor = TEAM_COLORS[1];
                break;
            case 'contested':
                statusLabel = 'Contested';
                statusColor = '#ffffff';
                break;
            default:
                statusLabel = 'Empty';
                statusColor = '#555';
                break;
        }

        // Build row
        const row = document.createElement('div');
        row.className = 'region-status-row';

        const nameSpan = document.createElement('span');
        nameSpan.className = 'region-status-name';
        nameSpan.textContent = region.name;
        row.appendChild(nameSpan);

        const stateSpan = document.createElement('span');
        stateSpan.className = 'region-status-state';
        stateSpan.style.color = statusColor;
        stateSpan.textContent = statusLabel;
        row.appendChild(stateSpan);

        const playersSpan = document.createElement('span');
        playersSpan.className = 'region-status-players';

        // Sort: team A first, then team B
        players.sort((a, b) => a.teamIdx - b.teamIdx);
        if (players.length === 0) {
            playersSpan.textContent = '-';
        } else {
            for (const p of players) {
                const icon = buildPlayerRegionIcon(p);
                icon.title = p.name;
                playersSpan.appendChild(icon);
            }
        }
        row.appendChild(playersSpan);
        container.appendChild(row);
    }
}

// Build a composited canvas icon: player circle+letter with RL/LG weapon icons in corners
function buildPlayerRegionIcon(player) {
    const sym = player.sym;
    const symCanvas = sym ? sym.symbolCanvas : null;

    const size = 40;
    const canvas = document.createElement('canvas');
    canvas.width = size;
    canvas.height = size;
    canvas.className = 'region-player-icon';
    const ctx = canvas.getContext('2d');

    // Draw player symbol centered
    if (symCanvas) {
        const ox = (size - symCanvas.width) / 2;
        const oy = (size - symCanvas.height) / 2;
        ctx.drawImage(symCanvas, ox, oy);
    } else {
        // Fallback: draw letter
        const color = TEAM_COLORS[player.teamIdx] || TEAM_COLORS[0];
        ctx.font = 'bold 16px monospace';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'middle';
        ctx.fillStyle = color;
        ctx.fillText(player.name.charAt(0).toUpperCase(), size / 2, size / 2);
    }

    // Draw status badges around player symbol
    const badges = getActiveBadges(player.data);
    if (badges.length > 0) {
        drawBadgesAroundCenter(ctx, badges, size / 2, size / 2, 14, 5);
    }

    return canvas;
}

function prerenderLocationBackground() {
    if (!mapState.locationGroups || !mapState.canvas) return;

    const w = mapState.canvas.width;
    const h = mapState.canvas.height;
    const offscreen = document.createElement('canvas');
    offscreen.width = w;
    offscreen.height = h;
    const octx = offscreen.getContext('2d');

    for (const group of mapState.locationGroups) {
        drawLocationRegion(octx, group, worldToCanvasNew);
    }

    octx.font = '12px monospace';
    octx.textAlign = 'center';
    octx.textBaseline = 'middle';
    for (const group of mapState.locationGroups) {
        const pos = worldToCanvasNew(group.centroid.x, group.centroid.y);
        octx.fillStyle = group.color.text;
        octx.fillText(group.name, pos.x, pos.y);
    }

    mapState.locationCanvas = offscreen;
}

// Pre-compute full trails for all players from high-res bucket data.
// Stores canvas-coordinate points with timestamps and teleport flags.
function precomputeFullTrails() {
    mapState.fullTrails = {};
    const buckets = timelineState.highResBuckets;
    if (!buckets || buckets.length === 0) return;

    const MAX_MOVE_PER_BUCKET = 2500 * (timelineState.highResDuration || 0.05);
    const lastWorldPos = {};

    for (const bucket of buckets) {
        const playerData = bucket.p || bucket.playerData;
        if (!playerData) continue;
        const t = bucket.t;

        for (const [name, data] of Object.entries(playerData)) {
            if (data.x === 0 && data.y === 0) continue;

            const pos = worldToCanvasNew(data.x, data.y);
            const symbolInfo = mapState.playerSymbols[name];
            if (!symbolInfo) continue;

            if (!mapState.fullTrails[name]) mapState.fullTrails[name] = [];
            const track = mapState.fullTrails[name];
            const last = track[track.length - 1];

            const isDeath = !!data.d;
            const isSpawn = !!data.sp;

            // Always include death/spawn markers regardless of pixel distance
            if (!isDeath && !isSpawn) {
                // Only add if moved more than 2 canvas pixels
                if (last && Math.abs(last.x - pos.x) <= 2 && Math.abs(last.y - pos.y) <= 2) {
                    lastWorldPos[name] = { x: data.x, y: data.y };
                    continue;
                }
            }

            // Teleport detection in world units (scale-independent)
            const lw = lastWorldPos[name];
            const isTeleport = !isDeath && !isSpawn && lw && (Math.abs(data.x - lw.x) > MAX_MOVE_PER_BUCKET || Math.abs(data.y - lw.y) > MAX_MOVE_PER_BUCKET);

            lastWorldPos[name] = { x: data.x, y: data.y };
            track.push({ x: pos.x, y: pos.y, t, teamIdx: symbolInfo.teamIdx, tp: isTeleport, death: isDeath, spawn: isSpawn });
        }
    }

    // Initialize all players as disabled (user enables via All button or per-player checkboxes)
    mapState.enabledPlayers = {};
    mapState.trailStartTimes = {};
    for (const name of Object.keys(mapState.fullTrails)) {
        mapState.enabledPlayers[name] = false;
        mapState.trailStartTimes[name] = 0;
    }
}

function renderMap(time) {
    const ctx = mapState.ctx;
    const canvas = mapState.canvas;

    if (!ctx || !canvas) return;

    // Skip redraw if same data bucket and nothing else changed
    const bucket = findBucketAtTime(time);
    if (bucket === mapState.lastRenderedBucket && !mapState.renderDirty) return;
    mapState.lastRenderedBucket = bucket;
    mapState.renderDirty = false;

    // Clear
    ctx.fillStyle = '#0a0a15';
    ctx.fillRect(0, 0, canvas.width, canvas.height);

    // Process location groups once (cache in mapState)
    if (!mapState.locationGroups && mapState.locations.length > 0) {
        mapState.locationGroups = processLocationGroups(mapState.locations);
    }

    // Draw pre-rendered location background (or render it first time)
    if (mapState.locationGroups) {
        if (!mapState.locationCanvas) {
            prerenderLocationBackground();
        }
        if (mapState.locationCanvas) {
            ctx.drawImage(mapState.locationCanvas, 0, 0);
        }
    }

    // Draw region control overlay (colored by controlling team)
    if (mapState.controlRegions && mapState.regionToGroups) {
        const controlStates = getRegionControlAtTime(time);
        if (controlStates) {
            drawRegionControlOverlay(ctx, controlStates);
        }
    }

    // Draw tracks (per-player visibility controlled by enabledPlayers)
    drawTracks(ctx, time);

    // Draw players (bucket.p = compact high-res format, bucket.playerData = 1s fallback)
    const playerData = bucket ? (bucket.p || bucket.playerData) : null;
    if (playerData) {
        const halfSymbol = 16;

        for (const [name, data] of Object.entries(playerData)) {
            if (data.x === 0 && data.y === 0) continue;

            const pos = worldToCanvas(data.x, data.y);
            const symbolInfo = mapState.playerSymbols[name];

            if (symbolInfo && symbolInfo.symbolCanvas) {
                ctx.drawImage(symbolInfo.symbolCanvas, pos.x - halfSymbol, pos.y - halfSymbol);

                // Draw status badges around player symbol
                const badges = getActiveBadges(data);
                if (badges.length > 0) {
                    drawBadgesAroundCenter(ctx, badges, pos.x, pos.y, 14, 5);
                }
            }
        }
    }
}

// Binary search: find index of last point with t <= time
function trailIndexAtTime(points, time) {
    let low = 0, high = points.length - 1;
    if (high < 0 || points[0].t > time) return -1;
    while (low < high) {
        const mid = (low + high + 1) >> 1;
        if (points[mid].t <= time) low = mid;
        else high = mid - 1;
    }
    return low;
}

function drawTracks(ctx, time) {
    const trailDuration = mapState.trailDuration;

    for (const [name, points] of Object.entries(mapState.fullTrails)) {
        if (!mapState.enabledPlayers[name]) continue;
        if (points.length < 2) continue;

        // If current time is before trail start, pull start back so trail grows from here
        if (time < (mapState.trailStartTimes[name] || 0)) {
            mapState.trailStartTimes[name] = time;
        }

        // Find the end index: last point at or before current time
        const endIdx = trailIndexAtTime(points, time);
        if (endIdx < 1) continue;

        // Find start: trail window starts at max(time - trailDuration, trailStartTime)
        const trailStart = Math.max(time - trailDuration, mapState.trailStartTimes[name] || 0);
        let startIdx = trailIndexAtTime(points, trailStart);
        if (startIdx < 0) startIdx = 0;

        if (endIdx - startIdx < 1) continue;

        const teamHex = TEAM_COLORS[points[0].teamIdx] || TEAM_COLORS[0];
        const solidColor = hexToRgba(teamHex, 0.4);
        const dashColor = hexToRgba(teamHex, 0.2);
        const markerColor = hexToRgba(teamHex, 0.8);

        // Collect death/spawn markers to draw after lines
        const markers = [];

        let inDash = false;
        let afterDeath = false; // suppress line from death to next spawn
        ctx.lineWidth = 3;
        ctx.strokeStyle = solidColor;
        ctx.setLineDash([]);
        ctx.beginPath();
        ctx.moveTo(points[startIdx].x, points[startIdx].y);

        if (points[startIdx].spawn) markers.push({ x: points[startIdx].x, y: points[startIdx].y, type: 'spawn' });

        for (let i = startIdx + 1; i <= endIdx; i++) {
            const pt = points[i];

            if (pt.spawn) {
                // Spawn: start a new line segment (gap from death)
                ctx.stroke();
                ctx.beginPath();
                ctx.setLineDash([]);
                ctx.strokeStyle = solidColor;
                inDash = false;
                afterDeath = false;
                ctx.moveTo(pt.x, pt.y);
                markers.push({ x: pt.x, y: pt.y, type: 'spawn' });
                continue;
            }

            if (pt.death) {
                // Death: draw line to death point, then mark it
                ctx.lineTo(pt.x, pt.y);
                ctx.stroke();
                ctx.beginPath();
                afterDeath = true;
                markers.push({ x: pt.x, y: pt.y, type: 'death' });
                continue;
            }

            if (afterDeath) {
                // Between death and spawn — don't draw
                ctx.moveTo(pt.x, pt.y);
                continue;
            }

            const needDash = !!pt.tp;
            if (needDash !== inDash) {
                ctx.stroke();
                ctx.beginPath();
                ctx.moveTo(points[i - 1].x, points[i - 1].y);
                if (needDash) {
                    ctx.setLineDash([4, 6]);
                    ctx.strokeStyle = dashColor;
                } else {
                    ctx.setLineDash([]);
                    ctx.strokeStyle = solidColor;
                }
                inDash = needDash;
            }
            ctx.lineTo(pt.x, pt.y);
        }
        ctx.stroke();
        ctx.setLineDash([]);

        // Draw death (✕) and spawn (●) markers on top
        ctx.fillStyle = markerColor;
        ctx.strokeStyle = markerColor;
        ctx.lineWidth = 2;
        for (const m of markers) {
            if (m.type === 'death') {
                // Draw ✕
                const s = 5;
                ctx.beginPath();
                ctx.moveTo(m.x - s, m.y - s);
                ctx.lineTo(m.x + s, m.y + s);
                ctx.moveTo(m.x + s, m.y - s);
                ctx.lineTo(m.x - s, m.y + s);
                ctx.stroke();
            } else {
                // Draw ●
                ctx.beginPath();
                ctx.arc(m.x, m.y, 3, 0, Math.PI * 2);
                ctx.fill();
            }
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

    // Return high-res bucket directly — renderMap uses compact format (x, y)
    return bucket;
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

function setupMapTrailControls() {
    const allBtn = document.getElementById('map-trails-all');
    if (allBtn) {
        allBtn.addEventListener('click', () => {
            for (const name of Object.keys(mapState.fullTrails)) {
                // Only reset start time for newly-enabled players
                if (!mapState.enabledPlayers[name]) {
                    mapState.trailStartTimes[name] = mapState.currentTime;
                }
                mapState.enabledPlayers[name] = true;
            }
            // Sync legend checkboxes
            document.querySelectorAll('.map-player-trail-cb').forEach(cb => { cb.checked = true; });
            mapState.renderDirty = true;
            renderMap(mapState.currentTime);
        });
    }

    const noneBtn = document.getElementById('map-trails-none');
    if (noneBtn) {
        noneBtn.addEventListener('click', () => {
            for (const name of Object.keys(mapState.fullTrails)) {
                mapState.enabledPlayers[name] = false;
            }
            document.querySelectorAll('.map-player-trail-cb').forEach(cb => { cb.checked = false; });
            mapState.renderDirty = true;
            renderMap(mapState.currentTime);
        });
    }

    const resetTracksBtn = document.getElementById('map-reset-tracks');
    if (resetTracksBtn) {
        resetTracksBtn.addEventListener('click', () => {
            for (const name of Object.keys(mapState.fullTrails)) {
                mapState.trailStartTimes[name] = mapState.currentTime;
            }
            mapState.renderDirty = true;
            renderMap(mapState.currentTime);
        });
    }

    const durationSelect = document.getElementById('map-trail-duration');
    if (durationSelect) {
        durationSelect.addEventListener('change', (e) => {
            mapState.trailDuration = parseInt(e.target.value, 10);
            mapState.renderDirty = true;
            renderMap(mapState.currentTime);
        });
    }
}

// ─── Playback Engine ──────────────────────────────────────────────────────

const PLAYBACK_BUTTON_LABELS = {
    'tl-rev': '-1x',
    'tl-play-pause': '1x',
    'tl-5x': '5x',
    'tl-20x': '20x'
};

function updatePlaybackButtons() {
    const buttons = {
        'tl-rev': -1,
        'tl-play-pause': 1,
        'tl-5x': 5,
        'tl-20x': 20
    };
    for (const [id, speed] of Object.entries(buttons)) {
        const btn = document.getElementById(id);
        if (!btn) continue;
        if (mapState.isPlaying && mapState.playbackSpeed === speed) {
            btn.classList.add('active');
            btn.textContent = '⏸';
        } else {
            btn.classList.remove('active');
            btn.textContent = PLAYBACK_BUTTON_LABELS[id];
        }
    }
}

function startPlaybackAtSpeed(speed) {
    if (mapState.isPlaying && mapState.playbackSpeed === speed) {
        // Toggle off — pause
        stopPlayback();
        return;
    }

    mapState.playbackSpeed = speed;
    if (!mapState.isPlaying) {
        mapState.isPlaying = true;
        mapState.lastRenderTime = performance.now();
        animatePlayback();
    }
    updatePlaybackButtons();
}

function stopPlayback() {
    mapState.isPlaying = false;
    if (mapState.animationFrameId) {
        cancelAnimationFrame(mapState.animationFrameId);
        mapState.animationFrameId = null;
    }
    updatePlaybackButtons();
    setCurrentTime(mapState.currentTime);
}

let _lastFullSyncTime = 0;

function animatePlayback() {
    if (!mapState.isPlaying) {
        mapState.animationFrameId = null;
        return;
    }

    mapState.animationFrameId = requestAnimationFrame(animatePlayback);

    const now = performance.now();
    const elapsed = (now - mapState.lastRenderTime) / 1000;

    // Throttle to ~30fps
    if (elapsed < 0.033) return;

    mapState.currentTime += elapsed * mapState.playbackSpeed;
    mapState.lastRenderTime = now;

    const duration = timelineState.duration || 600;

    // Forward past end: wrap to 0
    if (mapState.currentTime > duration) {
        mapState.currentTime = 0;
        mapState.renderDirty = true;
    }

    // Reverse past start: stop at 0
    if (mapState.currentTime < 0) {
        mapState.currentTime = 0;
        stopPlayback();
        return;
    }

    // Lightweight sync every frame
    updateUnifiedCursor();
    updateUnifiedTimeDisplay();
    renderMap(mapState.currentTime);
    updateChatTimeLine();
    scrollChatToCurrentTime();

    // Full sync every 200ms
    if (now - _lastFullSyncTime > 200) {
        _lastFullSyncTime = now;
        mapState.renderDirty = true;
        updateTimeIndicators();
        updateTeamStatus();
        updateMapLegend();
        updateRegionStatus();
    }
}

function buildMapPowerupList(result) {
    const list = document.getElementById('map-powerup-events');
    if (!list) return;

    list.innerHTML = '';

    const events = result.timelineAnalysis?.powerupEvents || [];

    if (events.length === 0) {
        list.innerHTML = '<li style="color: #666; font-style: italic;">No powerup events</li>';
        return;
    }

    for (const event of events) {
        const li = document.createElement('li');
        li.innerHTML = `
            <span class="time-cell">${formatDuration(event.time)}</span>
            <span class="powerup-cell ${event.powerupType}">${getPowerupDisplay(event.powerupType)}</span>
            <span>${escapeHtml(event.playerName || 'Unknown')}</span>
        `;
        li.addEventListener('click', () => {
            setCurrentTime(event.time);
            markMapDirty();
            renderMap(mapState.currentTime);
        });
        list.appendChild(li);
    }
}
