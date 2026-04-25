// MVD Analyzer Dashboard — Pure client-side via WASM

// ─── Theme & Constants ──────────────────────────────────────────────────────
//
// Single source of truth for colours, magic numbers and shared layout values.
// Anything that used to be a literal sprinkled across the file lives here so
// it can be tweaked in one place — including the values that are duplicated
// in styles.css as :root custom properties (kept in sync by hand: see the
// matching --team-a / --armor-* / --accent-cyan declarations in styles.css).
const TEAM_COLORS = ['#ff5050', '#50a0ff', '#4ecdc4', '#ffc107'];
const ARMOR_COLORS = {
    ra: 'rgb(255, 50, 50)',
    ya: 'rgb(255, 200, 0)',
    ga: 'rgb(0, 180, 0)',
};

// Badge layout / colours used by the map view to draw inventory icons around
// each player marker. Hoisted from the map-rendering section so the palette
// lives next to the rest of the theme.
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

const PLAYER_SYMBOLS = ['*', 'x', '+', 'o', '◆', '▲', '●', '■'];

// Timing / layout constants used by the chat scroller and the map playback.
// CHAT_PX_PER_SEC is the chat-track density: it has to stay in sync with the
// 700px chat viewport in styles.css (see #chat-track) — change one, change
// the other.
const CHAT_PX_PER_SEC   = 17.5; // ~same density as the original 40s/700px window
const CHAT_ITEM_HEIGHT  = 18;
const DEATH_X_DURATION  = 2.0;  // seconds an "X" death marker stays on the map
const PLAYBACK_FPS_MS   = 33;   // map playback throttle (~30 fps = 33 ms/frame)

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

// ─── Table builder ──────────────────────────────────────────────────────────
//
// Six different player/team scoreboard tables used to repeat this exact
// boilerplate: clear tbody, loop, build a <tr>, paint a 3px team-coloured
// border-left, set innerHTML, append. Centralise it.
//
// `getTeamIdx` is optional — pass it for tables whose rows should get a
// team colour stripe. The helper looks up the colour from TEAM_COLORS so
// the palette stays in one place.
function renderTableRows(tbodyId, items, buildRow, getTeamIdx) {
    const tbody = document.getElementById(tbodyId);
    if (!tbody) return;
    tbody.innerHTML = '';
    items.forEach((item, i) => {
        const tr = document.createElement('tr');
        if (getTeamIdx) {
            const teamIdx = getTeamIdx(item, i);
            if (teamIdx >= 0 && teamIdx < TEAM_COLORS.length) {
                tr.style.borderLeft = `3px solid ${TEAM_COLORS[teamIdx]}`;
            }
        }
        tr.innerHTML = buildRow(item, i);
        tbody.appendChild(tr);
    });
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
    updateItemsPanelStatus(mapState.currentTime);
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

            // Sync views on tab switch.
            //
            // Canvases sized from their container's clientWidth render empty
            // when they were first drawn while the tab was hidden
            // (clientWidth === 0 under display:none). Every dimension-
            // sensitive tab therefore re-renders here, after its container
            // is visible, so the first-paint width is always real.
            if (tabName === 'map') {
                renderMap(mapState.currentTime);
            } else if (tabName === 'timeline') {
                if (currentResult) updateDetailView();
                updateTimeIndicators();
            } else if (tabName === 'chat') {
                renderChatMessages();
            } else if (tabName === 'loc-graph') {
                renderLocGraph();
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

    // Match settings + server info from the new metadata analyzer.
    displayMatchSettings(result.metadata?.matchSettings);
    displayServerInfo(result.metadata?.serverInfo);

    // Duel-mode styling: the Go-side `normalizeDuelTeams` pass has
    // already rewritten every team reference to the player's name for
    // 1v1 demos. Now collapse the redundant "Per Team" panels and the
    // Teams summary box in the UI so the viewer only sees the per-player
    // tables. Detected by checking whether every player's team equals
    // their own name (a property only true after the Go-side rewrite).
    applyDuelModeUI(result);

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

    // Key Moments (powerup runs + frag streaks). Call unconditionally so
    // the function gets a chance to clear stale DOM from a previous demo;
    // displayKeyMoments handles empty powerupEvents / fragStreaks on its own.
    if (result.timelineAnalysis) {
        displayKeyMoments(result);
    }

    // Pack Drops — always call so stale rows are cleared between demos.
    displayPackDrops(result);

    // Map View
    if (result.timelineAnalysis) {
        initMapView(result);
    }

    // Loc Graph
    initLocGraphView(result);

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
    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));
    const teamOrder = getTeamOrder(sorted);

    // Show the handicap column only when at least one player on this demo
    // has a non-default handicap. KTX omits the JSON field entirely when the
    // value is 100 (the default), so any non-zero value here means it was
    // actually set.
    const showHandicap = sorted.some(p => (p.handicap || 0) > 0 && p.handicap !== 100);
    document.querySelectorAll('#scoreboard .handicap-col').forEach(el => {
        el.style.display = showHandicap ? '' : 'none';
    });

    renderTableRows('scoreboard-body', sorted, player => {
        const kills = player.stats?.kills || 0;
        const deaths = player.stats?.deaths || 0;
        const rlKills = player.weapons?.rl?.kills?.enemy || 0;
        const lgKills = player.weapons?.lg?.kills?.enemy || 0;
        const efficiency = (kills + deaths) > 0 ? ((kills / (kills + deaths)) * 100).toFixed(1) : '0.0';
        // Bot badge: render the skill level inline when present, since bots
        // in a match are rare enough that seeing "BOT 10" at a glance is
        // more useful than hiding it behind a hover. Fall back to a plain
        // "BOT" when the demoinfo JSON didn't include a skill value.
        let botBadge = '';
        if (player.bot) {
            const skill = player.bot.skill;
            const label = skill !== undefined && skill !== null ? `BOT ${skill}` : 'BOT';
            const tooltip = `Frogbot${skill !== undefined ? ', skill ' + skill : ''}${player.bot.customised ? ' (customised)' : ''}`;
            botBadge = ` <span class="bot-badge" title="${tooltip}">${label}</span>`;
        }
        const handicapCell = `<td class="handicap-col"${showHandicap ? '' : ' style="display: none;"'}>${player.handicap || '-'}</td>`;
        return `
            <td>${escapeHtml(player.name)}${botBadge}</td>
            <td>${escapeHtml(player.team || '')}</td>
            ${handicapCell}
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
    }, player => teamOrder.indexOf(player.team || ''));
}

// applyDuelModeUI toggles the "Per Team" aggregation panels and the
// standalone "Teams" summary off when we're rendering a 1v1 demo.
// Everything else (the per-player scoreboard, weapon stats, item
// pickups) still renders normally.
//
// Detection: the Go `normalizeDuelTeams` pass rewrites every participant
// team field to their own name for duels, so we can detect duel mode
// reliably by checking whether every demoInfo player has `team ===
// name`. This avoids depending on the metadata mode string, which can
// be "duel" / "1on1" / "LGC" / "Hoony" / missing entirely depending on
// the server flavour.
function applyDuelModeUI(result) {
    const players = result.demoInfo?.players || [];
    const isDuel = players.length === 2 && players.every(p => p.team === p.name);

    // Toggle a class on <body> so CSS can drive the hiding. Using a
    // class (instead of inline style writes) means the UI can re-render
    // cleanly on demo reload without leaking stale display:none values
    // onto unrelated elements.
    document.body.classList.toggle('duel-mode', isDuel);
}

// Long-form names for KTX spawn algorithms (k_spw values). Mirrors
// respawn_model_name() in ktx/src/g_utils.c — used as a tooltip on the
// short-form value rendered in the Match Settings panel.
const SPAWN_LONG_NAMES = {
    'QW':  'Normal QW respawns',
    'KTS': 'KT SpawnSafety',
    'KT':  'Kombat Teams respawns',
    'KTX': 'KTX respawns',
    'KT2': 'KTX2 respawns',
};

// displayMatchSettings renders result.metadata.matchSettings as a labelled
// grid of cells in the new Match Settings panel. Cells with empty / zero
// values are skipped so duels don't show "Teamplay 0" etc. The boolean
// modifier flags (Dmgfrags, NoItems, Midair, …) plus Noweapon and SOCDv2
// collapse into a single "Modifiers" row of pill-shaped tags below the
// grid. Hides the whole panel if no settings are available.
function displayMatchSettings(settings) {
    const panel = document.getElementById('match-settings-panel');
    const grid = document.getElementById('match-settings-grid');
    const modifiersRow = document.getElementById('match-modifiers-row');
    const modifiersList = document.getElementById('match-modifiers');

    if (!settings) {
        panel.style.display = 'none';
        return;
    }

    const cells = [];
    const addCell = (label, value, title) => {
        if (value === undefined || value === null || value === '' || value === 0) return;
        const titleAttr = title ? ` title="${escapeHtml(title)}"` : '';
        cells.push(`
            <div class="summary-item"${titleAttr}>
                <label>${escapeHtml(label)}</label>
                <span>${escapeHtml(String(value))}</span>
            </div>
        `);
    };

    addCell('Mode', settings.mode);
    addCell('Deathmatch', settings.deathmatch);
    addCell('Teamplay', settings.teamplay);
    if (settings.timelimit) addCell('Timelimit', `${settings.timelimit} min`);
    if (settings.fraglimit) addCell('Fraglimit', settings.fraglimit);
    if (settings.spawnmodel) {
        const long = SPAWN_LONG_NAMES[settings.spawnmodel] || 'Unknown spawn algorithm';
        const kSuffix = settings.spawnK !== undefined ? ` (k_spw=${settings.spawnK})` : '';
        addCell('Spawnmodel', settings.spawnmodel, `${long}${kSuffix}`);
    }
    if (settings.antilag !== undefined && settings.antilag !== 0) {
        addCell('Antilag', settings.antilag);
    }
    if (settings.overtime) {
        const ot = settings.overtime === 'sd' ? 'sudden death' : `${settings.overtime} min`;
        addCell('Overtime', ot);
    }
    addCell('Powerups', settings.powerups);
    addCell('Match tag', settings.matchtag);

    grid.innerHTML = cells.join('');

    // Modifier pills — boolean flags + special string flags.
    const modifiers = [];
    const addPill = (label, on, title) => {
        if (!on) return;
        const titleAttr = title ? ` title="${escapeHtml(title)}"` : '';
        modifiers.push(`<span class="modifier-tag"${titleAttr}>${escapeHtml(label)}</span>`);
    };
    addPill('Dmgfrags', settings.dmgfrags, 'Damage counts as frags (LGC scoring)');
    addPill('NoItems',  settings.noItems,  'k_noitems: items disabled');
    addPill('Midair',   settings.midair,   'k_midair mode');
    addPill('Instagib', settings.instagib, 'k_instagib mode');
    addPill('Yawnmode', settings.yawnmode, 'k_yawnmode');
    addPill('Airstep',  settings.airstep,  'pm_airstep: stair stepping in air');
    addPill('VWep',     settings.vwep,     'Visible weapon models');
    if (settings.noweapon) {
        modifiers.push(`<span class="modifier-tag" title="Disabled weapons">Noweapon: ${escapeHtml(settings.noweapon)}</span>`);
    }
    if (settings.socdv2) {
        modifiers.push(`<span class="modifier-tag" title="SOCD-cleaning mode">SOCDv2: ${escapeHtml(settings.socdv2)}</span>`);
    }
    if (modifiers.length > 0) {
        modifiersList.innerHTML = modifiers.join('');
        modifiersRow.style.display = '';
    } else {
        modifiersRow.style.display = 'none';
    }

    // Show the panel only if there's anything in it. With the cell-skipping
    // logic above, an entirely empty matchSettings (parser failure) would
    // produce zero cells and zero modifiers — hide the panel in that case.
    panel.style.display = (cells.length > 0 || modifiers.length > 0) ? '' : 'none';
}

// displayServerInfo renders result.metadata.serverInfo as a 2-column
// key/value table inside a collapsed <details> panel. Star keys (server
// system metadata like *version, *admin) sort below regular gameplay
// cvars. Special-cases the `epoch` key to show a human-readable date
// alongside the raw unix timestamp.
function displayServerInfo(serverInfo) {
    const panel = document.getElementById('server-info-panel');
    const tbody = document.getElementById('server-info-body');

    if (!serverInfo || Object.keys(serverInfo).length === 0) {
        panel.style.display = 'none';
        return;
    }

    const keys = Object.keys(serverInfo).filter(k => serverInfo[k] !== '' && serverInfo[k] !== undefined);
    keys.sort((a, b) => {
        const aStar = a.startsWith('*');
        const bStar = b.startsWith('*');
        if (aStar !== bStar) return aStar ? 1 : -1; // star keys go to the bottom
        return a.localeCompare(b);
    });

    tbody.innerHTML = '';
    for (const k of keys) {
        const v = serverInfo[k];
        let displayValue = v;
        if (k === 'epoch') {
            const ts = parseInt(v, 10);
            if (!isNaN(ts)) {
                const dt = new Date(ts * 1000).toISOString().replace('T', ' ').replace(/\.\d+Z$/, ' UTC');
                displayValue = `${v} (${dt})`;
            }
        }
        const tr = document.createElement('tr');
        tr.innerHTML = `<td><code>${escapeHtml(k)}</code></td><td>${escapeHtml(displayValue)}</td>`;
        tbody.appendChild(tr);
    }
    panel.style.display = '';
}

function displayWeaponStatsTable(players) {
    const sorted = [...players].sort((a, b) => (b.dmg?.given || 0) - (a.dmg?.given || 0));
    const teamOrder = getTeamOrder(sorted);
    const wNames = ['sg', 'ssg', 'sng', 'gl', 'rl', 'lg'];

    renderTableRows('weapon-stats-body', sorted, player => {
        const w = player.weapons || {};
        let cells = `<td>${escapeHtml(player.name)}</td>`;
        wNames.forEach(wn => { cells += formatWeaponCells(w[wn]); });
        return cells;
    }, player => teamOrder.indexOf(player.team || ''));
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
    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));
    const teamOrder = getTeamOrder(sorted);

    renderTableRows('items-body', sorted, player => {
        const items = player.items || {};
        const weapons = player.weapons || {};
        return `
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
    }, player => teamOrder.indexOf(player.team || ''));
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
    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));
    const teamOrder = getTeamOrder(sorted);
    const groups = groupByTeam(sorted);

    renderTableRows('player-stats-team-body', teamOrder, team => {
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
        return `
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
    }, (_team, idx) => idx);
}

function displayWeaponStatsTeamsTable(players) {
    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));
    const teamOrder = getTeamOrder(sorted);
    const groups = groupByTeam(sorted);
    const wNames = ['sg', 'ssg', 'sng', 'gl', 'rl', 'lg'];

    renderTableRows('weapon-stats-team-body', teamOrder, team => {
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
        return cells;
    }, (_team, idx) => idx);
}

function displayItemsTeamsTable(players) {
    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));
    const teamOrder = getTeamOrder(sorted);
    const groups = groupByTeam(sorted);
    const fmtPu = (took, time) => time > 0 ? `${took} (${time}s)` : `${took}`;

    renderTableRows('items-team-body', teamOrder, team => {
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
        return `
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
    }, (_team, idx) => idx);
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

    // Get hub info for viewer links (from currentResult which may have hubInfo set)
    const hubInfo = currentResult?.hubInfo;

    const powerupEvents = result.timelineAnalysis?.powerupEvents || [];

    // Powerups don't exist on duel / 2v2 maps, so powerupEvents is routinely
    // empty. Show the empty-state for the powerup table but DO NOT return —
    // the frag-streaks section below is independent and must still render.
    if (powerupEvents.length === 0) {
        emptyMsg.style.display = 'block';
    } else {
        emptyMsg.style.display = 'none';

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
    }

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

// Pack Drops table — joins result.backpacks (the drop side from
// //ktx drop) with the backpack-sourced entries in result.weaponPickups
// (the pickup side from //ktx bp) by (backpackEnt, dropTime). A drop
// with no matching pickup is shown as "expired" — the pack despawned
// or fell into a lava pit before anyone touched it. The filter row
// above the table narrows rows by dropper team, picker team, or
// status label; filter state lives in the select elements themselves
// so switching tabs and coming back preserves the view.
const packDropsState = { rows: [], hubInfo: null, playerUserIDs: null };

function packDropStatusFor(drop, pickup) {
    if (!pickup) return { label: 'expired', cls: 'status-expired' };
    const sameTeam = pickup.team && drop.team && pickup.team === drop.team;
    const weaponUpper = drop.weapon.toUpperCase();
    if (sameTeam) {
        if (pickup.hadBefore) return { label: `xfer ${weaponUpper}`, cls: 'status-xfer-had' };
        return { label: 'xfer', cls: 'status-xfer' };
    }
    if (pickup.hadBefore) return { label: `enemy ${weaponUpper}`, cls: 'status-enemy-had' };
    return { label: 'enemy', cls: 'status-enemy' };
}

function populateFilterSelect(selectId, values) {
    const sel = document.getElementById(selectId);
    if (!sel) return;
    const prev = sel.value;
    // Keep the "All" option; replace the rest.
    while (sel.options.length > 1) sel.remove(1);
    for (const v of values) {
        const opt = document.createElement('option');
        opt.value = v;
        opt.textContent = v;
        sel.appendChild(opt);
    }
    // Preserve selection across demo reload when possible.
    if (values.includes(prev)) sel.value = prev;
    else sel.value = '';
}

function displayPackDrops(result) {
    const tbody = document.getElementById('packdrops-body');
    const emptyMsg = document.getElementById('packdrops-empty');
    if (!tbody) return;
    tbody.innerHTML = '';

    const drops = result.backpacks || [];
    if (drops.length === 0) {
        emptyMsg.style.display = 'block';
        document.getElementById('packdrops-count').textContent = '';
        return;
    }
    emptyMsg.style.display = 'none';

    const pickupByKey = {};
    for (const p of (result.weaponPickups || [])) {
        if (p.source === 'backpack' && p.backpackEnt) {
            pickupByKey[`${p.backpackEnt}@${p.dropTime}`] = p;
        }
    }

    const rows = drops.map(drop => {
        const pickup = pickupByKey[`${drop.entNum}@${drop.time}`] || null;
        return { drop, pickup, status: packDropStatusFor(drop, pickup) };
    });

    packDropsState.rows = rows;
    packDropsState.hubInfo = currentResult?.hubInfo || null;
    packDropsState.playerUserIDs = currentResult?.timelineAnalysis?.playerUserIDs || {};

    const dropPlayers = new Set();
    const pickPlayers = new Set();
    const dropTeams = new Set();
    const pickTeams = new Set();
    const statuses = new Set();
    for (const r of rows) {
        if (r.drop.player) dropPlayers.add(r.drop.player);
        if (r.drop.team) dropTeams.add(r.drop.team);
        if (r.pickup?.player) pickPlayers.add(r.pickup.player);
        if (r.pickup?.team) pickTeams.add(r.pickup.team);
        statuses.add(r.status.label);
    }
    const cmp = (a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' });
    populateFilterSelect('packdrops-filter-dropplayer', [...dropPlayers].sort(cmp));
    populateFilterSelect('packdrops-filter-dropteam', [...dropTeams].sort(cmp));
    populateFilterSelect('packdrops-filter-pickplayer', [...pickPlayers].sort(cmp));
    populateFilterSelect('packdrops-filter-pickteam', [...pickTeams].sort(cmp));
    populateFilterSelect('packdrops-filter-status', [...statuses].sort(cmp));

    // Install filter-change handlers once. onchange is overwrite-safe
    // — rebinding on each new demo replaces the previous closure rather
    // than stacking listeners.
    const filterIds = [
        'packdrops-filter-dropplayer',
        'packdrops-filter-dropteam',
        'packdrops-filter-pickplayer',
        'packdrops-filter-pickteam',
        'packdrops-filter-status',
    ];
    for (const id of filterIds) {
        const el = document.getElementById(id);
        if (el) el.onchange = renderPackDropRows;
    }

    renderPackDropRows();
}

function renderPackDropRows() {
    const tbody = document.getElementById('packdrops-body');
    if (!tbody) return;
    tbody.innerHTML = '';

    const dropPlayer = document.getElementById('packdrops-filter-dropplayer').value;
    const dropTeam = document.getElementById('packdrops-filter-dropteam').value;
    const pickPlayer = document.getElementById('packdrops-filter-pickplayer').value;
    const pickTeam = document.getElementById('packdrops-filter-pickteam').value;
    const status = document.getElementById('packdrops-filter-status').value;

    const { rows, hubInfo, playerUserIDs } = packDropsState;
    const demoOff = timelineState.demoOffset || 0;

    const hubAnchor = (from, to, trackName) => {
        if (!hubInfo || !hubInfo.gameId) return '-';
        const trackId = playerUserIDs[trackName];
        if (!trackId) return '-';
        const f = Math.max(0, Math.floor(from + demoOff));
        const t = Math.floor(to + demoOff);
        const url = `https://hub.quakeworld.nu/games/?gameId=${hubInfo.gameId}&from=${f}&to=${t}&track=${trackId}`;
        return `<a href="${url}" target="_blank" class="viewer-link">Hub</a>`;
    };

    let shown = 0;
    for (const r of rows) {
        if (dropPlayer && r.drop.player !== dropPlayer) continue;
        if (dropTeam && r.drop.team !== dropTeam) continue;
        if (pickPlayer && (r.pickup?.player || '') !== pickPlayer) continue;
        if (pickTeam && (r.pickup?.team || '') !== pickTeam) continue;
        if (status && r.status.label !== status) continue;

        const { drop, pickup } = r;
        const tr = document.createElement('tr');

        const dropHub = hubAnchor(drop.time - 10, drop.time + 2, drop.player);

        let runHub = '-';
        let pickerLabel = '-';
        let pickTeamLabel = '-';
        let killsCell = '-';
        if (pickup) {
            const endTime = pickup.nextDeathTime > 0 ? pickup.nextDeathTime : pickup.time + 15;
            runHub = hubAnchor(pickup.time - 3, endTime, pickup.player);
            pickerLabel = escapeHtml(pickup.player || '?');
            pickTeamLabel = escapeHtml(pickup.team || '-');
            killsCell = pickup.hadBefore
                ? `<span class="kills-redundant">${pickup.kills}</span>`
                : String(pickup.kills);
        }

        const statusCell = `<span class="pack-status ${r.status.cls}">${escapeHtml(r.status.label)}</span>`;

        tr.innerHTML = `
            <td class="time-cell time-link">${formatDuration(drop.time)}</td>
            <td>${escapeHtml(drop.player || '?')}</td>
            <td>${escapeHtml(drop.team || '-')}</td>
            <td class="weapon-cell weapon-${drop.weapon}">${drop.weapon.toUpperCase()}</td>
            <td>${dropHub}</td>
            <td>${statusCell}</td>
            <td>${pickerLabel}</td>
            <td>${pickTeamLabel}</td>
            <td class="kills-cell">${killsCell}</td>
            <td>${runHub}</td>
        `;

        tr.querySelector('.time-link').addEventListener('click', () => {
            setCurrentTime(drop.time);
        });

        tbody.appendChild(tr);
        shown++;
    }

    const countEl = document.getElementById('packdrops-count');
    if (countEl) {
        countEl.textContent = shown === rows.length
            ? `${rows.length} drops`
            : `${shown} of ${rows.length} drops`;
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
        'tl-axis', 'kill-messages', 'team-a-messages', 'team-b-messages'
    ];
    containers.forEach(id => {
        const el = document.getElementById(id);
        if (el) el.innerHTML = '';
    });
    // Clear canvases
    for (const cid of ['detail-graph-canvas', 'health-armor-canvas', 'frags-canvas', 'score-canvas']) {
        const c = document.getElementById(cid);
        if (c && c.getContext) {
            const ctx = c.getContext('2d');
            ctx.clearRect(0, 0, c.width, c.height);
        }
    }
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

    timelineState.highResBuckets = timeline?.highResBuckets || [];
    timelineState.highResDuration = timeline?.highResDuration || 0.05;
    timelineState.matchStartTime = timeline?.matchStartTime || 0;
    timelineState.demoOffset = timeline?.demoOffset || 0;
    timelineState.duration = result.duration || 600;
    timelineState.events = result.messages?.events || [];
    timelineState.fragEvents = timeline?.fragEvents || []; // Frag events from stat tracking
    timelineState.backpacks = result.backpacks || [];      // RL/LG drops from KTX hint
    timelineState.powerupEvents = timeline?.powerupEvents || []; // per-run records: player, team, frags, duration

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

// Binary search for first high-res bucket with t >= targetTime
function binarySearchBucketStart(buckets, targetTime) {
    let lo = 0, hi = buckets.length;
    while (lo < hi) {
        const mid = (lo + hi) >>> 1;
        if (buckets[mid].t < targetTime) lo = mid + 1;
        else hi = mid;
    }
    return lo;
}

// ─── Unified Canvas Graph Renderer ──────────────────────────────────────────
//
// All timeline graphs (weapons, health/armor, frags, score) share a single
// canvas-based diverging-bar renderer. Each graph type provides a data
// preparation function that returns an array of data points in a common
// format. The renderer draws them at full resolution on a <canvas>.

const GRAPH_COLORS = {
    RL:     'rgba(255, 107, 107, 0.9)',
    LG:     'rgba(0, 217, 255, 0.9)',
    RLLG:   'rgba(156, 39, 176, 0.9)',
    QUAD:   'rgba(0, 150, 255, 0.9)',
    PENT:   'rgba(255, 0, 0, 0.9)',
    RING:   'rgba(255, 235, 59, 0.9)',
    HEALTH: 'rgba(0, 200, 83, 0.9)',
    RA:     'rgba(255, 50, 50, 0.9)',
    YA:     'rgba(255, 200, 0, 0.9)',
    GA:     'rgba(0, 180, 0, 0.6)',
};

const NICE_TICK_INTERVALS = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 1200, 1800, 3600];

function pickTickInterval(duration, maxTicks) {
    const target = duration / maxTicks;
    for (const iv of NICE_TICK_INTERVALS) {
        if (iv >= target) return iv;
    }
    return NICE_TICK_INTERVALS[NICE_TICK_INTERVALS.length - 1];
}

// Render a diverging bar graph on a canvas.
//   dataPoints: [{t, dt, up: [{h, color}], down: [{h, color}]}]
//   dropMarks:  [{time, color, isTop}] (optional, e.g. RL/LG backpack drops
//               on the weapon graph). Renders as small dots in a reserved
//               strip zone so they never overlap the bars.
function renderDivergingGraph(canvasId, {
    startTime, endTime,
    dataPoints,
    maxValue,
    yAxisId,
    yTopLabel, yBottomLabel,
    dropMarks,
}) {
    const canvas = document.getElementById(canvasId);
    if (!canvas || !canvas.getContext) return;

    const container = canvas.parentElement;
    const W = container.clientWidth;
    const H = 200;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = W * dpr;
    canvas.height = H * dpr;
    canvas.style.width = W + 'px';
    canvas.style.height = H + 'px';
    const ctx = canvas.getContext('2d');
    ctx.scale(dpr, dpr);

    const AXIS_H = 20;
    const PAD = 4;
    const graphH = H - AXIS_H;
    // Drop-mark strips live in a reserved zone at the top and bottom of the
    // plot area so weapon bars can never grow into them — the weapons bar
    // height scales with max players-per-team, so without this reservation
    // a high-rollout 5v5 snapshot could paint bars straight through the
    // dots. Sized for one row of ~6 px dots.
    const DROP_STRIP_H = 8;
    const hasDropMarks = !!(dropMarks && dropMarks.length);
    const stripZone = hasDropMarks ? DROP_STRIP_H + 2 : 0;
    const midY = PAD + (graphH - PAD) / 2;
    const barH = midY - PAD - stripZone;
    const duration = endTime - startTime;

    // Background
    ctx.fillStyle = '#16213e';
    ctx.fillRect(0, 0, W, graphH);

    // Grid lines at ±50% (drawn first so bars overlay them)
    ctx.strokeStyle = 'rgba(255,255,255,0.06)';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(0, midY - barH * 0.5); ctx.lineTo(W, midY - barH * 0.5);
    ctx.moveTo(0, midY + barH * 0.5); ctx.lineTo(W, midY + barH * 0.5);
    ctx.stroke();

    if (duration > 0 && dataPoints && dataPoints.length > 0) {
        // Tile adjacent bars pixel-perfectly: compute each bar's right edge
        // from the next bucket's start, and round both edges to integers.
        // Fractional fillRect widths create anti-aliased edges that don't
        // cancel between neighbours, so the dark background bleeds through
        // as moiré banding when zoomed in. Integer-aligned tiling eliminates
        // that with no gaps.
        for (let i = 0; i < dataPoints.length; i++) {
            const pt = dataPoints[i];
            const xRaw = ((pt.t - startTime) / duration) * W;
            const xNextRaw = (i + 1 < dataPoints.length)
                ? ((dataPoints[i + 1].t - startTime) / duration) * W
                : ((pt.t + (pt.dt || 0.05) - startTime) / duration) * W;
            const x = Math.round(xRaw);
            const bw = Math.max(1, Math.round(xNextRaw) - x);
            if (x + bw < 0 || x > W) continue;

            // Up segments (team A, above center)
            let y = midY;
            if (pt.up) {
                for (const seg of pt.up) {
                    if (seg.h > 0) {
                        const h = (seg.h / maxValue) * barH;
                        ctx.fillStyle = seg.color;
                        ctx.fillRect(x, y - h, bw, h);
                        y -= h;
                    }
                }
            }

            // Down segments (team B, below center)
            y = midY;
            if (pt.down) {
                for (const seg of pt.down) {
                    if (seg.h > 0) {
                        const h = (seg.h / maxValue) * barH;
                        ctx.fillStyle = seg.color;
                        ctx.fillRect(x, y, bw, h);
                        y += h;
                    }
                }
            }
        }

    }

    // Drop-mark dots — small filled circles in the reserved strip zone.
    // Top strip = team A drops, bottom strip = team B drops; color is
    // weapon-coded by the caller (e.g. RL red, LG cyan).
    if (hasDropMarks && duration > 0) {
        const dotR = 3;
        const topY    = PAD + DROP_STRIP_H / 2;
        const bottomY = graphH - PAD - DROP_STRIP_H / 2;
        for (const m of dropMarks) {
            const x = ((m.time - startTime) / duration) * W;
            if (x < -dotR || x > W + dotR) continue;
            const y = m.isTop ? topY : bottomY;
            ctx.fillStyle = m.color;
            ctx.beginPath();
            ctx.arc(x, y, dotR, 0, Math.PI * 2);
            ctx.fill();
        }
    }

    // Zero-y divider — drawn on top of bars so the upper/lower split is
    // always clearly visible. Integer-aligned to avoid anti-aliasing.
    ctx.fillStyle = 'rgba(255, 255, 255, 0.85)';
    ctx.fillRect(0, Math.round(midY), W, 1);

    // X-axis ticks (adaptive)
    if (duration > 0) {
        const targetTicks = Math.max(4, Math.min(12, Math.floor(W / 100)));
        const interval = pickTickInterval(duration, targetTicks);
        ctx.fillStyle = '#888';
        ctx.font = '10px monospace';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'top';
        ctx.strokeStyle = 'rgba(255,255,255,0.08)';
        const firstTick = Math.ceil(startTime / interval) * interval;
        for (let t = firstTick; t <= endTime; t += interval) {
            const x = ((t - startTime) / duration) * W;
            ctx.beginPath(); ctx.moveTo(x, graphH); ctx.lineTo(x, graphH + 4); ctx.stroke();
            ctx.fillText(formatDuration(t), x, graphH + 5);
        }
    }

    // Y-axis labels
    if (yAxisId) {
        const el = document.getElementById(yAxisId);
        if (el) {
            const top = el.querySelector('.y-top');
            const bot = el.querySelector('.y-bottom');
            if (top) top.textContent = yTopLabel !== undefined ? yTopLabel : maxValue;
            if (bot) bot.textContent = yBottomLabel !== undefined ? yBottomLabel : maxValue;
        }
    }
}

// ─── Data preparation: Weapons ──────────────────────────────────────────────

function prepWeaponsData(startTime, endTime, teams) {
    const hrBuckets = timelineState.highResBuckets;
    if (!hrBuckets || !hrBuckets.length) return { points: [], max: 4 };
    const hrDur = timelineState.highResDuration || 0.05;
    const points = [];
    let maxVal = 4;
    const idx0 = binarySearchBucketStart(hrBuckets, startTime);
    for (let i = idx0; i < hrBuckets.length; i++) {
        const b = hrBuckets[i];
        if (b.t > endTime) break;
        const tdA = b.td?.[teams[0]] || {};
        const tdB = b.td?.[teams[1]] || {};
        const upT = (tdA.rl || 0) + (tdA.lg || 0) + (tdA.rllg || 0);
        const dnT = (tdB.rl || 0) + (tdB.lg || 0) + (tdB.rllg || 0);
        maxVal = Math.max(maxVal, upT, dnT);
        points.push({
            t: b.t, dt: hrDur,
            up: [
                { h: tdA.rllg || 0, color: GRAPH_COLORS.RLLG },
                { h: tdA.lg || 0, color: GRAPH_COLORS.LG },
                { h: tdA.rl || 0, color: GRAPH_COLORS.RL },
            ],
            down: [
                { h: tdB.rllg || 0, color: GRAPH_COLORS.RLLG },
                { h: tdB.lg || 0, color: GRAPH_COLORS.LG },
                { h: tdB.rl || 0, color: GRAPH_COLORS.RL },
            ],
        });
    }
    return { points, max: maxVal };
}

// computeBackpackDrops returns dot markers for the weapon timeline:
// every RL or LG backpack dropped within the [startTime, endTime]
// window, with isTop=true for team-A drops and false for team-B.
// Reads from timelineState.backpacks (populated from result.backpacks
// in displayTimelineAnalysis). Each mark carries the source `drop`
// entry so hover tooltips can show player, loc, and time.
function computeBackpackDrops(startTime, endTime, teams) {
    const drops = timelineState.backpacks;
    if (!drops || drops.length === 0) return [];
    const teamA = teams[0], teamB = teams[1];
    const out = [];
    for (const d of drops) {
        if (d.time < startTime || d.time > endTime) continue;
        let isTop;
        if (d.team === teamA)      isTop = true;
        else if (d.team === teamB) isTop = false;
        else continue; // unknown team (spectator drop, mid-substitution, etc.)
        const color = d.weapon === 'rl' ? GRAPH_COLORS.RL
                    : d.weapon === 'lg' ? GRAPH_COLORS.LG
                    : null;
        if (!color) continue;
        out.push({ time: d.time, color, isTop, drop: d });
    }
    return out;
}

// Hit-test state for the weapon-graph drop dots — populated by
// updateDetailGraph after each render so the canvas mousemove handler
// can find the dot under the cursor without re-running the data prep.
const weaponGraphHitState = {
    startTime: 0,
    endTime:   0,
    W:         0,
    dropMarks: [],
};

// ─── Data preparation: Health/Armor ─────────────────────────────────────────

function prepHealthArmorData(startTime, endTime, teams) {
    const hrBuckets = timelineState.highResBuckets;
    if (!hrBuckets || !hrBuckets.length) return { points: [], max: 400 };
    const hrDur = timelineState.highResDuration || 0.05;
    const points = [];
    let maxVal = 400;
    const idx0 = binarySearchBucketStart(hrBuckets, startTime);
    for (let i = idx0; i < hrBuckets.length; i++) {
        const b = hrBuckets[i];
        if (b.t > endTime) break;
        const tdA = b.td?.[teams[0]] || {};
        const tdB = b.td?.[teams[1]] || {};
        maxVal = Math.max(maxVal, (tdA.th || 0) + (tdA.ta || 0), (tdB.th || 0) + (tdB.ta || 0));
        points.push({
            t: b.t, dt: hrDur,
            up: buildHASegments(tdA),
            down: buildHASegments(tdB),
        });
    }
    return { points, max: maxVal };
}

function buildHASegments(td) {
    const segs = [];
    if ((td.th || 0) > 0) segs.push({ h: td.th, color: GRAPH_COLORS.HEALTH });
    const armor = td.ta || 0;
    const abt = td.abt || {};
    const ra = abt.ra || 0, ya = abt.ya || 0, ga = abt.ga || 0;
    const total = ra + ya + ga;
    if (total > 0 && armor > 0) {
        if (ga > 0) segs.push({ h: (ga / total) * armor, color: GRAPH_COLORS.GA });
        if (ya > 0) segs.push({ h: (ya / total) * armor, color: GRAPH_COLORS.YA });
        if (ra > 0) segs.push({ h: (ra / total) * armor, color: GRAPH_COLORS.RA });
    } else if (armor > 0) {
        segs.push({ h: armor, color: GRAPH_COLORS.YA });
    }
    return segs;
}

// ─── Data preparation: Frags ────────────────────────────────────────────────

const FRAG_BIN_SIZE = 10; // seconds; fixed so bars stay put when you zoom/pan

function prepFragsData(startTime, endTime, teams) {
    const fragEvents = timelineState.fragEvents || [];
    if (teams.length < 2) return { points: [], max: 5 };
    // Align bins to absolute multiples of FRAG_BIN_SIZE so a given frag
    // always falls in the same bin regardless of the current view range.
    const firstBin = Math.floor(startTime / FRAG_BIN_SIZE) * FRAG_BIN_SIZE;
    const lastBin  = Math.ceil(endTime  / FRAG_BIN_SIZE) * FRAG_BIN_SIZE;
    const numBins  = Math.max(0, Math.round((lastBin - firstBin) / FRAG_BIN_SIZE));
    if (numBins === 0) return { points: [], max: 5 };
    const aFrags = new Float32Array(numBins);
    const bFrags = new Float32Array(numBins);
    for (const f of fragEvents) {
        if (f.time < firstBin || f.time >= lastBin) continue;
        const bin = Math.floor((f.time - firstBin) / FRAG_BIN_SIZE);
        if (f.team === teams[0]) aFrags[bin] += (f.delta || 1);
        else if (f.team === teams[1]) bFrags[bin] += (f.delta || 1);
    }
    const [rA, gA, bA2] = hexToRgb(TEAM_COLORS[0]);
    const [rB, gB, bB2] = hexToRgb(TEAM_COLORS[1]);
    const cA = `rgba(${rA},${gA},${bA2},0.8)`;
    const cB = `rgba(${rB},${gB},${bB2},0.8)`;
    let maxVal = 5;
    const points = [];
    for (let i = 0; i < numBins; i++) {
        maxVal = Math.max(maxVal, aFrags[i], bFrags[i]);
        points.push({
            t: firstBin + i * FRAG_BIN_SIZE, dt: FRAG_BIN_SIZE,
            up: [{ h: aFrags[i], color: cA }],
            down: [{ h: bFrags[i], color: cB }],
        });
    }
    return { points, max: maxVal };
}

// ─── Data preparation: Score ────────────────────────────────────────────────

function prepScoreData(startTime, endTime, teams) {
    const fragEvents = (timelineState.fragEvents || []).slice().sort((a, b) => a.time - b.time);
    if (teams.length < 2) return { points: [], max: 10 };
    let score = 0;
    for (const f of fragEvents) {
        if (f.time >= startTime) break;
        if (f.team === teams[0]) score += (f.delta || 1);
        else if (f.team === teams[1]) score -= (f.delta || 1);
    }
    // Build cumulative score change points
    const scoreAt = [{ time: startTime, score }];
    let s = score;
    for (const f of fragEvents) {
        if (f.time < startTime) continue;
        if (f.time > endTime) break;
        if (f.team === teams[0]) s += (f.delta || 1);
        else if (f.team === teams[1]) s -= (f.delta || 1);
        scoreAt.push({ time: f.time, score: s });
    }
    scoreAt.push({ time: endTime, score: s });

    const duration = endTime - startTime;
    const sampleRate = Math.max(0.5, duration / 400);
    const [rA, gA, bA2] = hexToRgb(TEAM_COLORS[0]);
    const [rB, gB, bB2] = hexToRgb(TEAM_COLORS[1]);
    const cA = `rgba(${rA},${gA},${bA2},0.8)`;
    const cB = `rgba(${rB},${gB},${bB2},0.8)`;
    let maxVal = 10;
    const points = [];
    let si = 0;
    for (let t = startTime; t < endTime; t += sampleRate) {
        while (si + 1 < scoreAt.length && scoreAt[si + 1].time <= t) si++;
        const v = scoreAt[si].score;
        maxVal = Math.max(maxVal, Math.abs(v));
        points.push({
            t, dt: sampleRate,
            up: v > 0 ? [{ h: v, color: cA }] : [],
            down: v < 0 ? [{ h: -v, color: cB }] : [],
        });
    }
    return { points, max: maxVal };
}

// ─── Graph pan/zoom (shared view range) ─────────────────────────────────────
//
// All four diverging graphs and the region-control timeline share
// timelineState.segment as their view range. Ctrl+wheel zooms around the
// cursor; left-click drag pans horizontally. The unified timeline bar's
// range-select still feeds the same state, so both entry points stay in sync.

const MIN_VIEW_SPAN = 2; // seconds — don't zoom past this

function currentViewRange() {
    const duration = timelineState.duration || 0;
    const seg = timelineState.segment;
    return seg ? [seg.start, seg.end] : [0, duration];
}

function setViewRange(start, end) {
    const duration = timelineState.duration || 0;
    if (duration <= 0) return;
    if (end - start < MIN_VIEW_SPAN) {
        const mid = (start + end) / 2;
        start = mid - MIN_VIEW_SPAN / 2;
        end = mid + MIN_VIEW_SPAN / 2;
    }
    // Slide the window back inside [0, duration] without shrinking it.
    if (start < 0) { end -= start; start = 0; }
    if (end > duration) { start -= (end - duration); end = duration; }
    start = Math.max(0, start);
    end = Math.min(duration, end);
    if (start <= 0 && end >= duration) {
        timelineState.segment = null;
    } else {
        timelineState.segment = { start, end };
    }
    updateSelectionOverlay();
    updateSegmentLabel();
    updateDetailView();
    updateUrlState();
}

function graphMouseToTime(canvas, clientX) {
    const rect = canvas.getBoundingClientRect();
    if (rect.width <= 0) return null;
    const [start, end] = currentViewRange();
    return start + ((clientX - rect.left) / rect.width) * (end - start);
}

// One global drag tracker shared by all installed canvases — avoids attaching
// a mousemove listener per canvas.
const graphPanState = { canvas: null, lastX: 0 };
let graphPanGlobalsInstalled = false;

function ensureGraphPanGlobals() {
    if (graphPanGlobalsInstalled) return;
    graphPanGlobalsInstalled = true;
    document.addEventListener('mousemove', (e) => {
        const c = graphPanState.canvas;
        if (!c) return;
        const rect = c.getBoundingClientRect();
        if (rect.width <= 0) return;
        const [start, end] = currentViewRange();
        const secPerPx = (end - start) / rect.width;
        const dx = e.clientX - graphPanState.lastX;
        graphPanState.lastX = e.clientX;
        setViewRange(start - dx * secPerPx, end - dx * secPerPx);
    });
    document.addEventListener('mouseup', () => {
        if (!graphPanState.canvas) return;
        graphPanState.canvas.style.cursor = 'grab';
        graphPanState.canvas = null;
    });
}

function installGraphPanZoom(canvasId) {
    const canvas = document.getElementById(canvasId);
    if (!canvas || canvas._panZoomInstalled) return;
    canvas._panZoomInstalled = true;
    ensureGraphPanGlobals();

    canvas.addEventListener('wheel', (e) => {
        if (!e.ctrlKey && !e.metaKey) return;  // plain wheel = page scroll
        e.preventDefault();                    // stop browser pinch-zoom
        const centerT = graphMouseToTime(canvas, e.clientX);
        if (centerT === null) return;
        // Exponential factor — deltaY > 0 scrolls toward the user → zoom out.
        const factor = Math.exp(e.deltaY * 0.0015);
        const [start, end] = currentViewRange();
        setViewRange(
            centerT - (centerT - start) * factor,
            centerT + (end - centerT) * factor,
        );
    }, { passive: false });

    canvas.addEventListener('mousedown', (e) => {
        if (e.button !== 0) return;
        graphPanState.canvas = canvas;
        graphPanState.lastX = e.clientX;
        canvas.style.cursor = 'grabbing';
        e.preventDefault();
    });

    canvas.addEventListener('dblclick', () => {
        // Quick reset to full match
        setViewRange(0, timelineState.duration || 0);
    });

    canvas.style.cursor = 'grab';
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
    document.getElementById('tl-slow').addEventListener('click', () => startPlaybackAtSpeed(0.2));
    document.getElementById('tl-play-pause').addEventListener('click', () => startPlaybackAtSpeed(1));
    document.getElementById('tl-5x').addEventListener('click', () => startPlaybackAtSpeed(5));

    // --- Pan/zoom on every diverging graph + spans timelines ---
    ['detail-graph-canvas', 'powerup-canvas', 'region-control-canvas',
     'health-armor-canvas', 'frags-canvas', 'score-canvas'].forEach(installGraphPanZoom);

    // --- Hover tooltip on weapon-graph drop dots ---
    attachWeaponGraphTooltip();

    // --- Hover tooltip on powerup-timeline spans ---
    attachPowerupTimelineTooltip();

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
        'powerup-time-indicator',
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
            // Indicator shares the canvas's time-to-pixel mapping:
            // x = (t - start) / (end - start) * W, spanning the full
            // container width. No inset — the legacy 10 px padding
            // came from a removed axis-strip sibling and causes the
            // cursor to drift away from the bars near the edges.
            el.style.left = `${pct}%`;
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

    // Update all detail panels (axes are drawn on canvas by the unified renderer)
    updateDetailGraph(start, end);
    updatePowerupTimeline(start, end);
    updateRegionControlTimeline(start, end);
    updateHealthArmorGraph(start, end);
    updateFragsGraph(start, end);
    updateScoreTimeline(start, end);
}

// ─── Chat Tab ──────────────────────────────────────────────────────────────

// Chat: pixels per second for the full-match scrollable layout
// (CHAT_PX_PER_SEC and CHAT_ITEM_HEIGHT now live with the rest of the
// theme constants at the top of this file.)

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
    const teams = timelineState.teams;
    if (teams.length < 2) return;
    const { points, max } = prepWeaponsData(startTime, endTime, teams);
    const dropMarks = computeBackpackDrops(startTime, endTime, teams);
    const legendA = document.getElementById('legend-weapons-team-a');
    const legendB = document.getElementById('legend-weapons-team-b');
    if (legendA) legendA.textContent = `${teams[0]} ↑`;
    if (legendB) legendB.textContent = `${teams[1]} ↓`;
    renderDivergingGraph('detail-graph-canvas', {
        startTime, endTime, dataPoints: points, maxValue: max,
        yAxisId: 'detail-y-axis', dropMarks,
    });
    // Refresh the hit-test cache so the tooltip can find dots after
    // pan/zoom and after segment selection.
    const canvas = document.getElementById('detail-graph-canvas');
    weaponGraphHitState.startTime = startTime;
    weaponGraphHitState.endTime   = endTime;
    weaponGraphHitState.W         = canvas ? canvas.clientWidth : 0;
    weaponGraphHitState.dropMarks = dropMarks;
}

// Mousemove tooltip on the weapon-graph canvas: highlights the drop
// dot under the cursor and shows {player, weapon, loc, time}. Layout
// constants must match renderDivergingGraph (PAD, AXIS_H, DROP_STRIP_H).
function attachWeaponGraphTooltip() {
    const canvas = document.getElementById('detail-graph-canvas');
    if (!canvas || canvas._weaponTipAttached) return;
    canvas._weaponTipAttached = true;

    const wrapper = canvas.parentElement; // .detail-graph-outer (positioned)
    const tip = document.createElement('div');
    tip.className = 'canvas-tooltip';
    tip.style.display = 'none';
    wrapper.appendChild(tip);

    const HIT_R     = 6;   // hit radius (slightly larger than dot radius=3)
    const HIT_DY    = 8;   // vertical tolerance — generous so users can hover near
    const PAD       = 4;
    const AXIS_H    = 20;
    const H         = 200;
    const graphH    = H - AXIS_H;
    const DROP_STRIP_H = 8;
    const topY    = PAD + DROP_STRIP_H / 2;
    const bottomY = graphH - PAD - DROP_STRIP_H / 2;

    canvas.addEventListener('mousemove', (e) => {
        const s = weaponGraphHitState;
        if (!s.W || !s.dropMarks.length) { tip.style.display = 'none'; return; }
        const rect = canvas.getBoundingClientRect();
        const mx = e.clientX - rect.left;
        const my = e.clientY - rect.top;
        const duration = s.endTime - s.startTime;
        if (duration <= 0) { tip.style.display = 'none'; return; }

        let best = null;
        let bestDx = HIT_R + 1;
        for (const m of s.dropMarks) {
            const x = ((m.time - s.startTime) / duration) * s.W;
            const y = m.isTop ? topY : bottomY;
            const dx = Math.abs(mx - x);
            const dy = Math.abs(my - y);
            if (dy <= HIT_DY && dx <= bestDx) {
                bestDx = dx;
                best = m;
            }
        }

        if (!best) { tip.style.display = 'none'; return; }

        const d = best.drop;
        const weapon = (d.weapon || '').toUpperCase();
        const locLine = d.loc ? `<div>Loc: ${escapeHtml(d.loc)}</div>` : '';
        tip.innerHTML = `<div><strong>${escapeHtml(d.player || '?')}</strong> dropped <strong>${weapon}</strong></div>
${locLine}<div>Time: ${formatDuration(d.time)}</div>`;
        tip.style.display = 'block';
        // Position offset from cursor; clamp inside the wrapper so the tip
        // doesn't get cut off near the right edge.
        const tipW = tip.offsetWidth || 200;
        const wrapW = wrapper.clientWidth;
        let left = mx + 12;
        if (left + tipW > wrapW) left = mx - tipW - 12;
        tip.style.left = left + 'px';
        tip.style.top  = (my + 12) + 'px';
    });
    canvas.addEventListener('mouseleave', () => { tip.style.display = 'none'; });
}

function updateHealthArmorGraph(startTime, endTime) {
    const teams = timelineState.teams;
    if (teams.length < 2) return;
    const { points, max } = prepHealthArmorData(startTime, endTime, teams);
    const legendA = document.getElementById('legend-health-team-a');
    const legendB = document.getElementById('legend-health-team-b');
    if (legendA) legendA.textContent = `${teams[0]} ↑`;
    if (legendB) legendB.textContent = `${teams[1]} ↓`;
    renderDivergingGraph('health-armor-canvas', {
        startTime, endTime, dataPoints: points, maxValue: max,
        yAxisId: 'health-y-axis',
    });
}

function updateFragsGraph(startTime, endTime) {
    const teams = timelineState.teams;
    if (teams.length < 2) return;
    const { points, max } = prepFragsData(startTime, endTime, teams);
    const legendA = document.getElementById('legend-frags-team-a');
    const legendB = document.getElementById('legend-frags-team-b');
    if (legendA) legendA.textContent = `${teams[0]} ↑`;
    if (legendB) legendB.textContent = `${teams[1]} ↓`;
    renderDivergingGraph('frags-canvas', {
        startTime, endTime, dataPoints: points, maxValue: max,
        yAxisId: 'frags-y-axis',
    });
}

function updateScoreTimeline(startTime, endTime) {
    const teams = timelineState.teams;
    if (teams.length < 2) return;
    const { points, max } = prepScoreData(startTime, endTime, teams);
    const legendA = document.getElementById('legend-score-team-a');
    const legendB = document.getElementById('legend-score-team-b');
    if (legendA) { legendA.textContent = `${teams[0]} leading ↑`; legendA.style.color = TEAM_COLORS[0]; }
    if (legendB) { legendB.textContent = `${teams[1]} leading ↓`; legendB.style.color = TEAM_COLORS[1]; }
    renderDivergingGraph('score-canvas', {
        startTime, endTime, dataPoints: points, maxValue: max,
        yAxisId: 'score-y-axis', yTopLabel: `+${max}`, yBottomLabel: `-${max}`,
    });
}

// ─── Region Control Timeline ────────────────────────────────────────────────
//
// One row per control region; each row colors contiguous spans according to
// classifyRegionState (single source of truth, shared with the live map).
// Only the "strong" states (solo armed control + armed-vs-armed contested)
// paint pixels — weak states render as gaps to keep the color story readable.

const RC_ROW_H = 20;
const RC_AXIS_H = 20;

function prepRegionControlData(startTime, endTime, teams) {
    const regions = mapState.controlRegions;
    if (!regions || regions.length === 0 || !mapState.locToRegion) return null;
    const hrBuckets = timelineState.highResBuckets;
    if (!hrBuckets || !hrBuckets.length) return null;

    const teamA = teams[0], teamB = teams[1];
    const locations = mapState.locations;
    const symbols = mapState.playerSymbols || {};
    const idx0 = binarySearchBucketStart(hrBuckets, startTime);

    const rows = regions.map(r => ({ name: r.name, spans: [], _cur: null, _start: startTime }));
    const rowByName = new Map(rows.map(r => [r.name, r]));

    for (let i = idx0; i < hrBuckets.length; i++) {
        const b = hrBuckets[i];
        if (b.t > endTime) break;

        // Accumulate per-region armed/unarmed counts for this bucket.
        const perRegion = new Map();
        const pd = b.p;
        if (pd) {
            for (const name in pd) {
                const data = pd[name];
                if (!data || data.d) continue;
                if (data.h !== undefined && data.h <= 0) continue;
                const locName = resolvePlayerLoc(data, locations);
                if (!locName) continue;
                const rName = mapState.locToRegion[locName];
                if (!rName || !rowByName.has(rName)) continue;
                const sym = symbols[name];
                const pTeam = sym ? (teams[sym.teamIdx] || '') : '';
                const hasWpn = data.rl || data.lg;
                let agg = perRegion.get(rName);
                if (!agg) { agg = { aWpn: 0, aNo: 0, bWpn: 0, bNo: 0 }; perRegion.set(rName, agg); }
                if (pTeam === teamA)      { if (hasWpn) agg.aWpn++; else agg.aNo++; }
                else if (pTeam === teamB) { if (hasWpn) agg.bWpn++; else agg.bNo++; }
            }
        }

        for (const row of rows) {
            const agg = perRegion.get(row.name);
            const state = agg
                ? classifyRegionState(agg.aWpn, agg.aNo, agg.bWpn, agg.bNo)
                : 'empty';
            if (state !== row._cur) {
                if (row._cur) row.spans.push({ start: row._start, end: b.t, state: row._cur });
                row._cur = state;
                row._start = b.t;
            }
        }
    }
    for (const row of rows) {
        if (row._cur) row.spans.push({ start: row._start, end: endTime, state: row._cur });
        delete row._cur; delete row._start;
    }
    return { rows, teamA, teamB };
}

// Generic span-timeline renderer. Each row carries a list of
// {start, end, state} spans; stateColors maps state strings to fill
// colors. Used by both the region-control timeline and the powerup
// timeline so they share one renderer instead of two near-copies.
function renderSpansTimeline(canvasId, labelsId, { startTime, endTime, rows, stateColors }) {
    const canvas = document.getElementById(canvasId);
    const labelsEl = document.getElementById(labelsId);
    if (!canvas || !canvas.getContext || !labelsEl) return;

    const container = canvas.parentElement;
    const W = container.clientWidth;
    const H = rows.length * RC_ROW_H + RC_AXIS_H;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = W * dpr;
    canvas.height = H * dpr;
    canvas.style.width = W + 'px';
    canvas.style.height = H + 'px';
    const ctx = canvas.getContext('2d');
    ctx.scale(dpr, dpr);

    // Label column, one DOM element per row, sized to match the canvas row.
    labelsEl.innerHTML = '';
    for (const r of rows) {
        const lab = document.createElement('div');
        lab.className = 'region-timeline-label';
        lab.style.height = RC_ROW_H + 'px';
        lab.style.lineHeight = RC_ROW_H + 'px';
        lab.textContent = r.name;
        lab.title = r.name;
        labelsEl.appendChild(lab);
    }

    const graphH = rows.length * RC_ROW_H;
    ctx.fillStyle = '#16213e';
    ctx.fillRect(0, 0, W, graphH);

    const duration = endTime - startTime;
    if (duration <= 0) return;

    rows.forEach((row, idx) => {
        const y = idx * RC_ROW_H;
        for (const span of row.spans) {
            const color = stateColors[span.state];
            if (!color) continue;
            const x1 = Math.round(((span.start - startTime) / duration) * W);
            const x2 = Math.round(((span.end - startTime) / duration) * W);
            const w = x2 - x1;
            if (w <= 0) continue;
            ctx.fillStyle = color;
            ctx.fillRect(x1, y + 1, w, RC_ROW_H - 2);
        }
    });

    // X-axis ticks (adaptive, same helper as renderDivergingGraph)
    const targetTicks = Math.max(4, Math.min(12, Math.floor(W / 100)));
    const interval = pickTickInterval(duration, targetTicks);
    ctx.fillStyle = '#888';
    ctx.font = '10px monospace';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'top';
    ctx.strokeStyle = 'rgba(255,255,255,0.08)';
    const firstTick = Math.ceil(startTime / interval) * interval;
    for (let t = firstTick; t <= endTime; t += interval) {
        const x = ((t - startTime) / duration) * W;
        ctx.beginPath(); ctx.moveTo(x, graphH); ctx.lineTo(x, graphH + 4); ctx.stroke();
        ctx.fillText(formatDuration(t), x, graphH + 5);
    }
}

// ─── Powerup Timeline ───────────────────────────────────────────────────────
//
// One row per powerup type (Quad / Pent / Ring); each row colors contiguous
// spans by which team currently holds the powerup. Reuses renderSpansTimeline,
// the same renderer the region-control timeline uses — only the input rows
// and the state→color map differ.
//
// Sourced from result.timelineAnalysis.powerupEvents (one record per run,
// already containing player/team/frags/duration), not from per-bucket
// aggregates: that gives us exact span boundaries plus the metadata the
// hover tooltip needs.

const POWERUP_TYPES = [
    { key: 'quad', name: 'Quad' },
    { key: 'pent', name: 'Pent' },
    { key: 'ring', name: 'Ring' },
];

function prepPowerupRowsData(startTime, endTime, teams) {
    const events = timelineState.powerupEvents;
    if (!events || events.length === 0) return null;
    const teamA = teams[0], teamB = teams[1];
    if (!teamA || !teamB) return null;

    const rowByKey = new Map();
    const rows = POWERUP_TYPES.map(pu => {
        const r = { name: pu.name, spans: [] };
        rowByKey.set(pu.key, r);
        return r;
    });

    for (const ev of events) {
        const row = rowByKey.get(ev.powerupType);
        if (!row) continue;
        // Clip the run to the visible window so the bar doesn't extend
        // beyond the canvas; record the original endpoints in the meta
        // so the tooltip still shows the full duration.
        const start = Math.max(startTime, ev.time);
        const end   = Math.min(endTime,   ev.endTime);
        if (end <= start) continue;
        let state;
        if      (ev.team === teamA) state = 'teamA';
        else if (ev.team === teamB) state = 'teamB';
        else                        state = 'other'; // mid-game team change / spectator
        row.spans.push({ start, end, state, event: ev });
    }
    return { rows, teamA, teamB };
}

// Hit-test state for the powerup-timeline span hover tooltip — populated
// by updatePowerupTimeline after each render so the canvas mousemove
// handler can identify which row + span sits under the cursor.
const powerupGraphHitState = {
    startTime: 0,
    endTime:   0,
    W:         0,
    rows:      [], // [{name, spans}]
};

function updatePowerupTimeline(startTime, endTime) {
    const panel = document.getElementById('powerup-timeline-panel');
    if (!panel) return;
    const teams = timelineState.teams;
    if (teams.length < 2) { panel.style.display = 'none'; return; }

    const data = prepPowerupRowsData(startTime, endTime, teams);
    if (!data) { panel.style.display = 'none'; return; }

    // Hide if no powerup activity at all in this window — keeps the
    // panel out of the way for maps without powerups.
    const hasAny = data.rows.some(r => r.spans.length > 0);
    if (!hasAny) { panel.style.display = 'none'; return; }
    panel.style.display = '';

    const teamAEl = document.getElementById('pu-tl-teamA');
    const teamBEl = document.getElementById('pu-tl-teamB');
    if (teamAEl) teamAEl.textContent = data.teamA;
    if (teamBEl) teamBEl.textContent = data.teamB;
    const setLegend = (id, color) => {
        const el = document.getElementById(id);
        if (el) el.style.background = color;
    };
    setLegend('pu-legend-a', teamStrongColor(TEAM_COLORS[0]));
    setLegend('pu-legend-b', teamStrongColor(TEAM_COLORS[1]));

    renderSpansTimeline('powerup-canvas', 'powerup-timeline-labels', {
        startTime, endTime, rows: data.rows,
        stateColors: {
            teamA: teamStrongColor(TEAM_COLORS[0]),
            teamB: teamStrongColor(TEAM_COLORS[1]),
            other: 'rgba(180, 180, 180, 0.85)',
        },
    });

    // Refresh hit-test cache for the hover tooltip.
    const canvas = document.getElementById('powerup-canvas');
    powerupGraphHitState.startTime = startTime;
    powerupGraphHitState.endTime   = endTime;
    powerupGraphHitState.W         = canvas ? canvas.clientWidth : 0;
    powerupGraphHitState.rows      = data.rows;
}

// Mousemove tooltip on the powerup-canvas. Hit-tests against the
// last-rendered row layout (RC_ROW_H per row) and shows the source
// PowerupEvent metadata (player, team, frags, duration).
function attachPowerupTimelineTooltip() {
    const canvas = document.getElementById('powerup-canvas');
    if (!canvas || canvas._powerupTipAttached) return;
    canvas._powerupTipAttached = true;

    const wrapper = canvas.parentElement; // .region-timeline-outer (positioned)
    const tip = document.createElement('div');
    tip.className = 'canvas-tooltip';
    tip.style.display = 'none';
    wrapper.appendChild(tip);

    canvas.addEventListener('mousemove', (e) => {
        const s = powerupGraphHitState;
        if (!s.W || !s.rows.length) { tip.style.display = 'none'; return; }
        const rect = canvas.getBoundingClientRect();
        const mx = e.clientX - rect.left;
        const my = e.clientY - rect.top;
        const duration = s.endTime - s.startTime;
        if (duration <= 0) { tip.style.display = 'none'; return; }

        // Row index by Y; gracefully ignore the axis strip below the rows.
        const rowIdx = Math.floor(my / RC_ROW_H);
        if (rowIdx < 0 || rowIdx >= s.rows.length) { tip.style.display = 'none'; return; }
        const row = s.rows[rowIdx];

        // Find the span whose [start, end] window contains the cursor x.
        let hit = null;
        for (const sp of row.spans) {
            const x1 = ((sp.start - s.startTime) / duration) * s.W;
            const x2 = ((sp.end   - s.startTime) / duration) * s.W;
            if (mx >= x1 && mx <= x2) { hit = sp; break; }
        }
        if (!hit || !hit.event) { tip.style.display = 'none'; return; }

        const ev = hit.event;
        const player = escapeHtml(ev.playerName || 'Unknown');
        const team   = ev.team ? `<div>Team: ${escapeHtml(ev.team)}</div>` : '';
        const dur    = (ev.duration != null) ? `${Math.round(ev.duration)}s` : '?';
        tip.innerHTML = `<div><strong>${escapeHtml(row.name)}</strong> · <strong>${player}</strong></div>
${team}<div>Frags: ${ev.frags || 0}</div>
<div>Duration: ${dur}</div>`;
        tip.style.display = 'block';
        const tipW = tip.offsetWidth || 200;
        const wrapW = wrapper.clientWidth;
        let left = mx + 12;
        if (left + tipW > wrapW) left = mx - tipW - 12;
        tip.style.left = left + 'px';
        tip.style.top  = (my + 12) + 'px';
    });
    canvas.addEventListener('mouseleave', () => { tip.style.display = 'none'; });
}

function updateRegionControlTimeline(startTime, endTime) {
    const panel = document.getElementById('region-control-timeline-panel');
    if (!panel) return;
    const teams = timelineState.teams;
    if (teams.length < 2) { panel.style.display = 'none'; return; }

    const data = prepRegionControlData(startTime, endTime, teams);
    if (!data) { panel.style.display = 'none'; return; }
    panel.style.display = '';

    const teamAEl = document.getElementById('rc-tl-teamA');
    const teamBEl = document.getElementById('rc-tl-teamB');
    if (teamAEl) teamAEl.textContent = data.teamA;
    if (teamBEl) teamBEl.textContent = data.teamB;
    const setLegend = (id, color) => {
        const el = document.getElementById(id);
        if (el) el.style.background = color;
    };
    setLegend('rc-legend-a-ctrl', teamStrongColor(TEAM_COLORS[0]));
    setLegend('rc-legend-b-ctrl', teamStrongColor(TEAM_COLORS[1]));

    renderSpansTimeline('region-control-canvas', 'region-timeline-labels', {
        startTime, endTime, rows: data.rows,
        stateColors: {
            teamAControl: teamStrongColor(TEAM_COLORS[0]),
            contested:    'rgb(255, 255, 255)',
            teamBControl: teamStrongColor(TEAM_COLORS[1]),
        },
    });
}

// ─── Team Status Panel ──────────────────────────────────────────────────────

function updateTeamStatus() {
    const containerA = document.getElementById('team-status-a');
    const containerB = document.getElementById('team-status-b');
    if (!containerA || !containerB) return;

    const teams = timelineState.teams;
    const hrBuckets = timelineState.highResBuckets;
    if (!hrBuckets || hrBuckets.length === 0 || teams.length < 2) {
        containerA.innerHTML = '';
        containerB.innerHTML = '';
        return;
    }

    // Find high-res bucket at current time
    const time = mapState.currentTime;
    const hrBucket = findBucketAtTime(time);
    const pd = hrBucket ? (hrBucket.p || {}) : {};
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

        // Color the team name + frag header in the team's identity color so
        // the two sides are visually distinct at a glance and match the
        // colors used everywhere else (map legend, score timeline, etc.).
        const teamColor = TEAM_COLORS[ti] || '#ccc';
        let html = `<h4 style="color: ${teamColor}">${escapeHtml(team)} — ${teamFrags} frags</h4>`;
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

// Compute the 2nd / 98th percentile of z across all map locations. These
// endpoints are used to scale player-symbol size by "height on the map": a
// player at the lo end renders at base size, one at the hi end 25% larger.
// Percentiles (not min / max) so a single out-of-bounds loc doesn't squash
// the useful range.
function computeMapZRange(locations) {
    if (!locations || locations.length === 0) return { lo: 0, hi: 0 };
    const zs = [];
    for (const loc of locations) zs.push(loc.z || 0);
    zs.sort((a, b) => a - b);
    const n = zs.length;
    const lo = zs[Math.floor(n * 0.02)];
    const hi = zs[Math.min(n - 1, Math.floor(n * 0.98))];
    return { lo, hi };
}

// Classify region control state from per-team head counts. Single source of
// truth for everywhere on the page that derives a state from raw presence:
// the strip, the per-frame map overlay, the stats table, and the status panel.
//
// States:
//   empty            — no living players in the region
//   teamAControl     — team A present and team A has at least one RL/LG, AND
//                      team B has no RL/LG holders here (B may be absent or
//                      present but unarmed; an unarmed B player is dominated)
//   teamAWeakControl — team A present without RL/LG and team B fully absent
//   teamBControl / teamBWeakControl — mirror of the above
//   contested        — both teams present AND each team has at least one
//                      RL/LG holder here (real fight)
//   weakContested    — both teams present, neither team has any RL/LG
//                      (skirmish without major weapons)
function classifyRegionState(aWpn, aNo, bWpn, bNo) {
    const aT = aWpn + aNo, bT = bWpn + bNo;
    if (aT === 0 && bT === 0) return 'empty';
    if (aT > 0 && bT === 0)   return aWpn > 0 ? 'teamAControl' : 'teamAWeakControl';
    if (bT > 0 && aT === 0)   return bWpn > 0 ? 'teamBControl' : 'teamBWeakControl';
    // Both teams present.
    if (aWpn > 0 && bWpn === 0) return 'teamAControl';
    if (bWpn > 0 && aWpn === 0) return 'teamBControl';
    if (aWpn > 0 && bWpn > 0)   return 'contested';
    return 'weakContested';
}

// Prefer the server-resolved loc name (3D nearest, matches ezQuake exactly).
// High-res buckets carry an integer index `li` into mapState.locTable; older
// 1s buckets carry the resolved name in `data.location`. Falls back to the
// 2D nearest-neighbor only when neither field is present (e.g. demos with
// no .loc file). The 2D fallback is harmless in that case because there is
// no stacked-loc disambiguation to do without a loc file in the first place.
function resolvePlayerLoc(data, locations) {
    if (data) {
        if (data.li && mapState.locTable) {
            return mapState.locTable[data.li] || '';
        }
        if (data.location) return data.location;
    }
    return findNearestLocation(data ? data.x : 0, data ? data.y : 0, locations);
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

    // Default - neutral gray (brightened so passageways like cemetary.tele
    // stay legible against the dark background).
    return { fill: 'rgba(170, 170, 190, 0.12)', stroke: 'rgba(150, 150, 160, 0.6)', text: 'rgba(180, 180, 190, 0.7)' };
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

    // If BSP-derived geometry is loaded, attach per-loc triangle lists so
    // the renderer can draw real floor shapes instead of convex-hull blobs.
    // Keys must match NormalizeLocationName (Go) <-> normalizeLocationName (JS).
    // Entries with an empty name are the unnamed backdrop bucket (faces
    // that couldn't be matched to a loc); they're handled separately by
    // drawLocationLayer as a neutral underlay.
    if (mapState.mapGeometry && Array.isArray(mapState.mapGeometry.locs)) {
        const geomByName = {};
        for (const l of mapState.mapGeometry.locs) {
            if (l.name === '') continue;
            geomByName[l.name] = l;
        }
        for (const group of Object.values(groups)) {
            const g = geomByName[group.name];
            group.tris = g && Array.isArray(g.tris) && g.tris.length >= 6 ? g.tris : null;
        }
    }

    // Cache normalized-name → group lookup for per-frame occupancy highlighting.
    mapState.locationGroupByName = groups;

    return Object.values(groups);
}

// Draw a location region from a pre-generated BSP-derived triangle list.
// tris is a flat Float array: 6 numbers per triangle (x1,y1,x2,y2,x3,y3).
// Groups with no tris (map JSON absent or loc unmatched) simply don't
// render — the legacy convex-hull fallback was removed now that mapgen
// output is the only source of region shapes.
function drawLocationRegionFromGeometry(ctx, group, worldToCanvasFunc) {
    drawTriangleListFill(ctx, group.tris, group.color.fill, worldToCanvasFunc);
}

// Fill a flat triangle list (6 numbers per triangle) with the given style.
// Shared by loc-group fills and the unnamed backdrop underlay. All triangles
// are added to a single path and filled once so this stays fast when called
// every frame with thousands of tris. Uses the non-allocating worldToCanvas
// variant (shared _tmpPt) — safe because each point's x/y is consumed by
// ctx.moveTo/lineTo before the next call overwrites the buffer.
function drawTriangleListFill(ctx, tris, fillStyle, worldToCanvasFunc) {
    if (!tris || tris.length < 6) return;
    ctx.fillStyle = fillStyle;
    ctx.beginPath();
    for (let i = 0; i + 5 < tris.length; i += 6) {
        let p = worldToCanvasFunc(tris[i],     tris[i + 1]);
        ctx.moveTo(p.x, p.y);
        p = worldToCanvasFunc(tris[i + 2], tris[i + 3]);
        ctx.lineTo(p.x, p.y);
        p = worldToCanvasFunc(tris[i + 4], tris[i + 5]);
        ctx.lineTo(p.x, p.y);
        ctx.closePath();
    }
    ctx.fill();
}

// Compute boundary edges of a triangle soup: edges that belong to exactly one
// triangle are on the outline; edges shared by two triangles are interior and
// cancel. Returns a flat Float array of world-space segment endpoints
// (x1,y1,x2,y2, ...). Cached on the group for reuse.
function computeRegionOutline(group) {
    if (group.outline !== undefined) return group.outline;
    const tris = group.tris;
    if (!tris || tris.length < 6) {
        group.outline = null;
        return null;
    }
    const edgeCount = new Map();
    const keyFor = (x1, y1, x2, y2) => {
        // Canonical order so (a,b) and (b,a) hash equally.
        if (x1 < x2 || (x1 === x2 && y1 <= y2)) {
            return x1 + ',' + y1 + '|' + x2 + ',' + y2;
        }
        return x2 + ',' + y2 + '|' + x1 + ',' + y1;
    };
    for (let i = 0; i + 5 < tris.length; i += 6) {
        const ax = tris[i],     ay = tris[i + 1];
        const bx = tris[i + 2], by = tris[i + 3];
        const cx = tris[i + 4], cy = tris[i + 5];
        const e1 = keyFor(ax, ay, bx, by);
        const e2 = keyFor(bx, by, cx, cy);
        const e3 = keyFor(cx, cy, ax, ay);
        edgeCount.set(e1, (edgeCount.get(e1) || 0) + 1);
        edgeCount.set(e2, (edgeCount.get(e2) || 0) + 1);
        edgeCount.set(e3, (edgeCount.get(e3) || 0) + 1);
    }
    const outline = [];
    for (const [key, count] of edgeCount) {
        if (count !== 1) continue;
        const [p1, p2] = key.split('|');
        const [x1, y1] = p1.split(',').map(Number);
        const [x2, y2] = p2.split(',').map(Number);
        outline.push(x1, y1, x2, y2);
    }
    group.outline = outline;
    return outline;
}

// Stroke the outline of a location region as a set of boundary line segments.
function drawLocationRegionOutline(ctx, group, worldToCanvasFunc, strokeStyle, lineWidth) {
    const outline = computeRegionOutline(group);
    if (!outline || outline.length < 4) return;
    ctx.strokeStyle = strokeStyle;
    ctx.lineWidth = lineWidth;
    ctx.beginPath();
    for (let i = 0; i + 3 < outline.length; i += 4) {
        const a = worldToCanvasFunc(outline[i],     outline[i + 1]);
        const b = worldToCanvasFunc(outline[i + 2], outline[i + 3]);
        ctx.moveTo(a.x, a.y);
        ctx.lineTo(b.x, b.y);
    }
    ctx.stroke();
}

// Fill a location region using its BSP-derived triangle list. Groups
// with no tris (map JSON absent, or a loc that didn't match any face)
// silently no-op. Used by the region-control overlay.
function fillLocationRegion(ctx, group, fillColor, worldToCanvasFunc) {
    drawTriangleListFill(ctx, group.tris, fillColor, worldToCanvasFunc);
}

// Compute the set of loc-group names currently occupied by at least one
// living player at this bucket. Uses the server-resolved 3D-nearest loc
// (matches ezQuake) via resolvePlayerLoc.
function computeOccupiedGroupNames(playerData) {
    const occupied = new Set();
    if (!playerData) return occupied;
    const locations = mapState.locations;
    for (const data of Object.values(playerData)) {
        if (!data) continue;
        if (data.d || (data.h !== undefined && data.h <= 0)) continue;
        if (data.x === 0 && data.y === 0) continue;
        const locName = resolvePlayerLoc(data, locations);
        if (!locName) continue;
        occupied.add(normalizeLocationName(locName));
    }
    return occupied;
}

// Highlight loc regions that contain at least one player. Drawn on top of
// the prerendered background and the team-control tint, so the player's
// current region is always identifiable at a glance.
function drawOccupiedRegionsOverlay(ctx, playerData) {
    const groupsByName = mapState.locationGroupByName;
    if (!groupsByName) return;
    const occupied = computeOccupiedGroupNames(playerData);
    if (occupied.size === 0) return;

    // Brighter outline pass.
    for (const name of occupied) {
        const group = groupsByName[name];
        if (!group || !group.tris || group.tris.length < 6) continue;
        drawLocationRegionOutline(ctx, group, worldToCanvasNew, 'rgba(220, 220, 220, 0.7)', 1);
    }

    // Bold label pass — draw over the dimmer prerendered label so it pops.
    const boldPx = Math.round(12 * mapIconScale());
    ctx.font = `bold ${boldPx}px monospace`;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    for (const name of occupied) {
        const group = groupsByName[name];
        if (!group) continue;
        const pos = worldToCanvasNew(group.centroid.x, group.centroid.y);
        // Soft shadow so the label stays legible against any underlying tint.
        ctx.fillStyle = 'rgba(0, 0, 0, 0.65)';
        ctx.fillText(group.name, pos.x + 1, pos.y + 1);
        ctx.fillStyle = 'rgba(255, 255, 255, 0.95)';
        ctx.fillText(group.name, pos.x, pos.y);
    }
}

// Stack-aware opacity boost: regions with no overlapping, higher-z region
// currently occupied are drawn at this multiple of their base alpha, so a
// lower deck standing alone reads cleanly rather than washing out against an
// empty upper deck's tint. Clamped final alpha to 0.5 so regions never
// become opaque.
const REGION_OPACITY_BOOST = 1.9;
const REGION_STACK_Z_EPS = 32;      // world units — roughly one step height
const REGION_STACK_OVERLAP_FRAC = 0.25;

// Precompute per-region bbox, median z, and the list of regions stacked
// above it (XY-overlapping and higher in z). Called from applyRegionConfig
// after mapState.controlRegions is refreshed.
function computeRegionStacking(regions) {
    for (const r of regions) {
        let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
        const zs = [];
        for (const pt of r.points) {
            if (pt.x < minX) minX = pt.x;
            if (pt.x > maxX) maxX = pt.x;
            if (pt.y < minY) minY = pt.y;
            if (pt.y > maxY) maxY = pt.y;
            zs.push(pt.z ?? 0);
        }
        r._bbox = { minX, maxX, minY, maxY };
        zs.sort((a, b) => a - b);
        r._z = zs.length > 0 ? zs[zs.length >> 1] : 0;
        r._bboxArea = Math.max(1, (maxX - minX) * (maxY - minY));
    }
    for (const r of regions) {
        const above = [];
        for (const r2 of regions) {
            if (r2 === r) continue;
            if (r2._z <= r._z + REGION_STACK_Z_EPS) continue;
            const ox = Math.max(0, Math.min(r._bbox.maxX, r2._bbox.maxX) - Math.max(r._bbox.minX, r2._bbox.minX));
            const oy = Math.max(0, Math.min(r._bbox.maxY, r2._bbox.maxY) - Math.max(r._bbox.minY, r2._bbox.minY));
            if ((ox * oy) / r._bboxArea >= REGION_STACK_OVERLAP_FRAC) {
                above.push(r2);
            }
        }
        r._above = above;
    }
}

// Draw control overlay for regions based on current control state
function drawRegionControlOverlay(ctx, controlStates) {
    const regions = mapState.controlRegions;
    if (!regions) return;

    // Build the set of regions that are occupied (any non-empty state).
    // Used by the stacking rule: a region is boosted when no region above it
    // in its stack is currently occupied.
    const occupied = new Set();
    for (const [name, state] of Object.entries(controlStates)) {
        if (state !== 'empty') occupied.add(name);
    }

    // Index regions by name for the boost lookup.
    const regionByName = {};
    for (const r of regions) regionByName[r.name] = r;

    for (const [regionName, state] of Object.entries(controlStates)) {
        const groups = mapState.regionToGroups[regionName];
        if (!groups || groups.length === 0) continue;

        let baseAlpha, hex;
        switch (state) {
            case 'teamAControl':     baseAlpha = 0.24; hex = TEAM_COLORS[0]; break;
            case 'teamAWeakControl': baseAlpha = 0.14; hex = TEAM_COLORS[0]; break;
            case 'teamBControl':     baseAlpha = 0.24; hex = TEAM_COLORS[1]; break;
            case 'teamBWeakControl': baseAlpha = 0.14; hex = TEAM_COLORS[1]; break;
            case 'contested':        baseAlpha = 0.14; hex = '#ffffff'; break;
            case 'weakContested':    baseAlpha = 0.07; hex = '#ffffff'; break;
            default: continue; // empty
        }

        const r = regionByName[regionName];
        let boost = 1.0;
        if (r && r._above && r._above.length > 0) {
            const anyAboveOccupied = r._above.some(ra => occupied.has(ra.name));
            if (!anyAboveOccupied) boost = REGION_OPACITY_BOOST;
        }
        const finalAlpha = Math.min(0.5, baseAlpha * boost);
        const color = hexToRgba(hex, finalAlpha);

        for (const group of groups) {
            fillLocationRegion(ctx, group, color, worldToCanvasNew);
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
    mapGeometry: null,    // BSP-derived per-loc polygons (optional, loaded async)
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
    renderDirty: false,       // Force redraw on track toggle/reset/etc
    followPlayer: null,       // Name of the player the camera re-centers on each frame, or null
    fullscreen: false         // True while the map panel is in fullscreen (set by fullscreenchange)
};

// (PLAYER_SYMBOLS, BADGE_DEFS and ARMOR_COLORS now live with the rest of
// the theme constants at the top of this file.)

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
    // Interned loc-name table — index 0 is the empty/no-loc sentinel.
    // High-res player records carry an integer Li indexing into this; the
    // resolvePlayerLoc helper hides the indirection from call sites.
    mapState.locTable = (timeline && timeline.locTable) ? timeline.locTable : [''];
    mapState.locationGroups = null; // Clear cached groups for new demo
    mapState.mapGeometry = null;    // Reset BSP-derived geometry for new demo

    // Fire-and-forget: try to load pre-generated BSP-derived map geometry.
    // If present, switch from convex-hull blobs to real floor polygons.
    // If absent (404 or fetch error), the existing hull path remains as fallback.
    const rawMapName = result.demoInfo && result.demoInfo.map ? result.demoInfo.map : '';
    const mapBasename = rawMapName.toLowerCase().replace(/^maps\//, '').replace(/\.bsp$/, '');
    if (mapBasename) {
        fetch(`maps/${mapBasename}.json`)
            .then(r => r.ok ? r.json() : null)
            .then(geom => {
                if (!geom || !Array.isArray(geom.locs) || geom.locs.length === 0) return;
                // The unnamed backdrop bucket (name === "") is drawn as a
                // neutral underlay by drawLocationLayer; cache its triangle
                // list separately so it isn't confused with loc groups keyed
                // by name.
                const backdrop = geom.locs.find(l => l && l.name === '');
                geom.backdropTris = backdrop && Array.isArray(backdrop.tris) ? backdrop.tris : null;
                mapState.mapGeometry = geom;
                // Rebuild groups with tris attached, then refresh region->group
                // references so the control overlay doesn't keep pointing at
                // the pre-fetch (tris-less) group objects.
                mapState.locationGroups = processLocationGroups(mapState.locations);
                if (mapState.rcResult) {
                    applyRegionConfig(); // also calls renderMap
                } else {
                    markMapDirty();
                    renderMap(mapState.currentTime);
                }
            })
            .catch(() => {});
    }

    // Show/hide no-data message
    const noDataMsg = document.getElementById('map-no-data');
    if (noDataMsg) {
        noDataMsg.style.display = mapState.locations.length === 0 ? 'block' : 'none';
    }

    // Calculate bounds from locations and player positions
    calculateMapBounds(result);

    // Size canvas and recompute transform. A fresh demo load resets user pan/zoom.
    _wtc.panX = 0;
    _wtc.panY = 0;
    _wtc.zoomK = 1;
    mapState.followPlayer = null;
    resizeMapCanvas();

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

    // Set up trail controls + map pan/zoom interaction (only once)
    if (!mapState.initialized) {
        setupMapTrailControls();
        installMapInteraction(mapState.canvas);
        mapState.initialized = true;
    }

    // Pre-compute full trails from high-res bucket data
    precomputeFullTrails();

    // Backpack drops — mirrors mapState.deathEvents in shape so renderMap
    // can fade them on the same DEATH_X_DURATION timeline. Only RL/LG drops
    // exist in result.backpacks today (see qwanalytics/result/backpacks.go).
    mapState.dropEvents = (result.backpacks || []).map(d => ({
        t:      d.time,
        wx:     d.origin?.[0] || 0,
        wy:     d.origin?.[1] || 0,
        weapon: d.weapon,
    }));

    // Cache the map's z percentile range — drives per-player z-based size
    // scaling in renderMap (players higher up on the map render up to 25%
    // larger than those on the lowest level).
    mapState.zRange = computeMapZRange(mapState.locations);

    // Populate the Follow-player dropdown with current players.
    rebuildFollowSelect();

    // Build powerup event list
    buildMapPowerupList(result);

    // Build item list panel (armors, weapons, MH, powerups with live
    // up/down status — present for KTX demos, auto-hidden otherwise).
    renderItemsPanel();

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
    computeRegionStacking(mapState.controlRegions);
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
    computeRegionStacking(regions);

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
        counters[r.name] = { aC: 0, aW: 0, con: 0, wcon: 0, emp: 0, bW: 0, bC: 0 };
    }

    let total = 0;
    const locations = mapState.locations;

    for (const bucket of buckets) {
        const playerData = bucket.p;
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

            const nearest = resolvePlayerLoc(data, locations);
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
            switch (classifyRegionState(p.aWpn, p.aNo, p.bWpn, p.bNo)) {
                case 'empty':            c.emp++;  break;
                case 'teamAControl':     c.aC++;   break;
                case 'teamAWeakControl': c.aW++;   break;
                case 'teamBControl':     c.bC++;   break;
                case 'teamBWeakControl': c.bW++;   break;
                case 'contested':        c.con++;  break;
                case 'weakContested':    c.wcon++; break;
            }
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
            contested: pct(c.con), weakContested: pct(c.wcon),
            empty: pct(c.emp),
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
            <td style="background: ${cellBg('#888', s.weakContested, 0.5)}">${s.weakContested}%</td>
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

    const playerData = bucket.p;
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

        // Find loc via authoritative server-resolved name when available
        const nearest = resolvePlayerLoc(data, locations);
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

    // Determine state per region (single source of truth in classifyRegionState).
    const states = {};
    for (const region of mapState.controlRegions) {
        const p = presence[region.name];
        states[region.name] = classifyRegionState(p.aWpn, p.aNo, p.bWpn, p.bNo);
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

    // From high-res buckets - position bounds
    const highResBuckets = result.timelineAnalysis?.highResBuckets || [];
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

// Precomputed transform parameters — call updateWorldToCanvasTransform() when bounds/canvas change.
// panX/panY/zoomK carry user-applied pan and zoom on top of the fit-to-canvas base. They persist
// across transform recomputes (e.g. canvas resize, geometry reload) so the user's view survives.
let _wtc = { scale: 1, offsetX: 0, offsetY: 0, minX: 0, minY: 0, canvasH: 0,
             panX: 0, panY: 0, zoomK: 1 };

// Canvas width used for non-fullscreen rendering. Fullscreen reads the container bbox instead.
const MAP_CANVAS_BASE_WIDTH = 850;

function resizeMapCanvas() {
    const canvas = mapState.canvas;
    if (!canvas) return;
    const worldW = mapState.bounds ? (mapState.bounds.maxX - mapState.bounds.minX) : 0;
    const worldH = mapState.bounds ? (mapState.bounds.maxY - mapState.bounds.minY) : 0;
    const fs = !!(document.fullscreenElement &&
                  document.fullscreenElement.classList &&
                  document.fullscreenElement.classList.contains('map-panel'));
    let cssW, cssH;
    if (fs && canvas.parentElement) {
        const rect = canvas.parentElement.getBoundingClientRect();
        cssW = Math.max(300, Math.floor(rect.width));
        cssH = Math.max(200, Math.floor(rect.height));
    } else {
        cssW = MAP_CANVAS_BASE_WIDTH;
        cssH = worldW > 0
            ? Math.round(Math.max(400, Math.min(850, cssW * (worldH / worldW))))
            : 700;
    }
    // Back the canvas with a physical-pixel bitmap sized for the display DPR
    // so lines and text render at device resolution. All draw code works in
    // CSS pixels; renderMap applies setTransform(dpr, 0, 0, dpr, 0, 0) before
    // each render so ctx operations map from CSS → physical automatically.
    const dpr = window.devicePixelRatio || 1;
    mapState.dpr = dpr;
    mapState.canvasCssW = cssW;
    mapState.canvasCssH = cssH;
    canvas.width = Math.round(cssW * dpr);
    canvas.height = Math.round(cssH * dpr);
    canvas.style.width = cssW + 'px';
    canvas.style.height = cssH + 'px';
    updateWorldToCanvasTransform();
}

function updateWorldToCanvasTransform() {
    const { minX, maxX, minY, maxY } = mapState.bounds;
    const canvas = mapState.canvas;
    if (!canvas) return;
    const cssW = mapState.canvasCssW || canvas.width;
    const cssH = mapState.canvasCssH || canvas.height;
    const worldWidth = maxX - minX;
    const worldHeight = maxY - minY;
    const scale = Math.min(cssW / worldWidth, cssH / worldHeight);
    _wtc.scale = scale;
    _wtc.offsetX = (cssW - worldWidth * scale) / 2;
    _wtc.offsetY = (cssH - worldHeight * scale) / 2;
    _wtc.minX = minX;
    _wtc.minY = minY;
    _wtc.canvasH = cssH;
    // panX, panY, zoomK intentionally preserved across recomputes.
}

function resetMapView() {
    _wtc.panX = 0;
    _wtc.panY = 0;
    _wtc.zoomK = 1;
    if (mapState.followPlayer) {
        mapState.followPlayer = null;
        syncFollowSelectUI();
    }
    mapState.renderDirty = true;
    renderMap(mapState.currentTime);
}

// Reusable point to avoid GC — only use for immediate consumption, not storage
const _tmpPt = { x: 0, y: 0 };

function worldToCanvas(x, y) {
    const sx = _wtc.scale * _wtc.zoomK;
    _tmpPt.x = _wtc.offsetX + (x - _wtc.minX) * sx + _wtc.panX;
    _tmpPt.y = _wtc.canvasH - _wtc.offsetY - (y - _wtc.minY) * sx + _wtc.panY;
    return _tmpPt;
}

// Allocating version for cases where result is stored (e.g., tracks, caching)
function worldToCanvasNew(x, y) {
    const sx = _wtc.scale * _wtc.zoomK;
    return {
        x: _wtc.offsetX + (x - _wtc.minX) * sx + _wtc.panX,
        y: _wtc.canvasH - _wtc.offsetY - (y - _wtc.minY) * sx + _wtc.panY
    };
}

// Inverse of worldToCanvas — canvas pixel to world coord. Needed for zoom-about-cursor and hit-testing.
function canvasToWorld(cx, cy) {
    const sx = _wtc.scale * _wtc.zoomK;
    return {
        x: _wtc.minX + (cx - _wtc.offsetX - _wtc.panX) / sx,
        y: _wtc.minY + (_wtc.canvasH - _wtc.offsetY + _wtc.panY - cy) / sx
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

        mapState.playerSymbols[player.name] = {
            symbol: letter,
            team: player.team,
            teamIdx: player.teamIdx,
        };
    }

    // Build legend and refresh the trail-players dropdown now that the
    // player roster is known for this demo.
    buildMapLegend();
    buildTrailPlayersPanel();
}

// Base size (px) of a player symbol at iconScale=1. The letter circle
// radius / outline width / letter font size all scale proportionally from
// this when we draw for a different iconScale.
const PLAYER_SYMBOL_BASE_SIZE = 32;

// Draw a player symbol (team-colour-bordered circle + letter) directly onto
// the supplied ctx, centered at (cx, cy) in CSS pixels. Fresh-drawn every
// frame so it's always pixel-native at the current zoom and display DPR —
// no bitmap cache, no upscale blur.
function drawPlayerSymbolAt(ctx, letter, teamColor, cx, cy, size) {
    const k = size / PLAYER_SYMBOL_BASE_SIZE;
    const r = 13 * k;

    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, Math.PI * 2);
    ctx.fillStyle = '#0a0a15';
    ctx.fill();
    ctx.strokeStyle = teamColor;
    ctx.lineWidth = 2 * k;
    ctx.stroke();

    ctx.font = `bold ${Math.round(16 * k)}px monospace`;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillStyle = teamColor;
    ctx.fillText(letter, cx, cy);
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
        table.innerHTML = `<thead><tr><th></th><th>Player</th><th>Loc</th><th>H</th><th>A</th><th>Wpn</th><th>View</th></tr></thead>`;
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
                    <td class="map-legend-loc" data-player="${escapedName}">-</td>
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

    // Make tables sortable. The sort indicator is now inline text (see
    // th.sortable in styles.css), so enabling sort here no longer shifts
    // the column headers out of alignment with the body cells.
    legend.querySelectorAll('.team-status-table').forEach(makeSortable);
}

// Build / refresh the Trails → Players dropdown in the top bar. One checkbox
// per player, wired to the same mapState.enabledPlayers / trailStartTimes
// state the legend previously mutated.
function buildTrailPlayersPanel() {
    const panel = document.getElementById('map-trails-players');
    if (!panel) return;
    panel.innerHTML = '';
    const names = Object.keys(mapState.playerSymbols).sort((a, b) => a.localeCompare(b));
    for (const name of names) {
        const info = mapState.playerSymbols[name];
        const teamIdx = info?.teamIdx ?? 0;
        const teamHex = TEAM_COLORS[teamIdx] || TEAM_COLORS[0];
        const label = document.createElement('label');
        const cb = document.createElement('input');
        cb.type = 'checkbox';
        cb.className = 'map-player-trail-cb';
        cb.dataset.player = name;
        cb.checked = !!mapState.enabledPlayers[name];
        cb.addEventListener('change', () => {
            mapState.enabledPlayers[name] = cb.checked;
            if (cb.checked) {
                mapState.trailStartTimes[name] = mapState.currentTime;
            }
            mapState.renderDirty = true;
            renderMap(mapState.currentTime);
        });
        const nameSpan = document.createElement('span');
        nameSpan.className = 'map-trails-player-name';
        nameSpan.style.color = teamHex;
        nameSpan.textContent = name;
        label.appendChild(cb);
        label.appendChild(nameSpan);
        panel.appendChild(label);
    }
}

function updateMapLegend() {
    const legend = document.getElementById('map-legend');
    if (!legend) return;

    const time = mapState.currentTime;
    const bucket = findBucketAtTime(time);
    const playerData = bucket ? (bucket.p) : null;
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
    const locations = mapState.locations;
    const locCells = legend.querySelectorAll('.map-legend-loc');
    for (const cell of locCells) {
        const name = cell.dataset.player;
        const data = playerData?.[name];
        if (data && !(data.x === 0 && data.y === 0)) {
            cell.textContent = resolvePlayerLoc(data, locations) || '';
        } else {
            cell.textContent = '';
        }
    }

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
    const playerData = bucket ? (bucket.p) : null;
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

            const nearest = resolvePlayerLoc(data, locations);
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
            case 'weakContested':
                statusLabel = 'Contested (weak)';
                statusColor = '#bbbbbb';
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
    const dpr = window.devicePixelRatio || 1;
    const size = 40;
    const canvas = document.createElement('canvas');
    canvas.width = Math.round(size * dpr);
    canvas.height = Math.round(size * dpr);
    canvas.style.width = size + 'px';
    canvas.style.height = size + 'px';
    canvas.className = 'region-player-icon';
    const ctx = canvas.getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

    // Draw player symbol centered — fresh-drawn so it's crisp at DPR.
    const letter = sym?.symbol || player.name.charAt(0).toUpperCase();
    const teamColor = TEAM_COLORS[sym?.teamIdx ?? player.teamIdx] || TEAM_COLORS[0];
    drawPlayerSymbolAt(ctx, letter, teamColor, size / 2, size / 2, PLAYER_SYMBOL_BASE_SIZE);

    // Draw status badges around player symbol
    const badges = getActiveBadges(player.data);
    if (badges.length > 0) {
        drawBadgesAroundCenter(ctx, badges, size / 2, size / 2, 14, 5);
    }

    return canvas;
}

// drawLocationLayer: render the floor plan underlay (BSP backdrop triangles,
// per-loc region fills, thin grey outlines, centroid labels) directly through
// worldToCanvas so everything follows user pan / zoom and stays crisp. No
// bitmap cache — at typical loc counts (~30 regions) this is a handful of
// batched path fills / strokes per frame, trivially cheap.
function drawLocationLayer(ctx) {
    const groups = mapState.locationGroups || [];
    const backdropTris = mapState.mapGeometry && mapState.mapGeometry.backdropTris;
    if (groups.length === 0 && (!backdropTris || backdropTris.length < 6)) return;

    if (backdropTris && backdropTris.length >= 6) {
        drawTriangleListFill(ctx, backdropTris, 'rgba(70, 80, 110, 0.35)', worldToCanvas);
    }

    for (const group of groups) {
        if (group.tris && group.tris.length >= 6) {
            drawLocationRegionFromGeometry(ctx, group, worldToCanvas);
        }
    }

    // Thin grey outlines around each traced region — drawn after all fills so
    // they sit on top and stay visible regardless of adjacent region tinting.
    // drawLocationRegionOutline needs the allocating worldToCanvasNew because
    // it holds both endpoints of an edge simultaneously.
    for (const group of groups) {
        if (group.tris && group.tris.length >= 6) {
            drawLocationRegionOutline(ctx, group, worldToCanvasNew, 'rgba(180, 180, 180, 0.5)', 1);
        }
    }

    const labelPx = Math.round(12 * mapIconScale());
    ctx.font = `${labelPx}px monospace`;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    for (const group of groups) {
        const pos = worldToCanvasNew(group.centroid.x, group.centroid.y);
        ctx.fillStyle = group.color.text;
        ctx.fillText(group.name, pos.x, pos.y);
    }
}

// mapIconScale: capped upscale applied to player symbols, item markers,
// loc labels, and any other canvas UI that should stay legible as the user
// zooms in. Linear ramp from 1.0 at zoomK=1, reaching the 1.5x cap around
// zoomK≈4.3 so midrange zooms already show a clear size bump. Cap is
// enforced at 1.5 (user requested "never more than 50% bigger").
function mapIconScale() {
    const k = _wtc.zoomK || 1;
    if (k <= 1) return 1;
    return Math.min(1.5, 1 + (k - 1) * 0.15);
}

// Pre-compute full trails for all players from high-res bucket data.
// Stores world-space (wx, wy) positions — drawTracks converts to canvas via
// worldToCanvas at draw time so trails follow user pan/zoom.
function precomputeFullTrails() {
    mapState.fullTrails = {};
    // Sorted-by-time list of death frames in world space, used by renderMap
    // to draw a fading red "X" at the death location for a couple of seconds.
    mapState.deathEvents = [];
    const buckets = timelineState.highResBuckets;
    if (!buckets || buckets.length === 0) return;

    const MAX_MOVE_PER_BUCKET = 2500 * (timelineState.highResDuration || 0.05);
    // "Meaningful movement" threshold — 2 canvas pixels at the base fit-to-canvas
    // scale, translated to world units so the filter is applied in world space.
    const MIN_MOVE_WORLD = _wtc.scale > 0 ? (2 / _wtc.scale) : 0;
    const lastWorldPos = {};

    for (const bucket of buckets) {
        const playerData = bucket.p;
        if (!playerData) continue;
        const t = bucket.t;

        for (const [name, data] of Object.entries(playerData)) {
            if (data.x === 0 && data.y === 0) continue;

            const symbolInfo = mapState.playerSymbols[name];
            if (!symbolInfo) continue;

            if (!mapState.fullTrails[name]) mapState.fullTrails[name] = [];
            const track = mapState.fullTrails[name];
            const last = track[track.length - 1];

            const isDeath = !!data.d;
            const isSpawn = !!data.sp;

            // Death frames also get added to the standalone deathEvents list
            // so renderMap can find them without scanning every player trail.
            // teamIdx is captured so the X is painted in the dead player's
            // own team color rather than a generic red.
            if (isDeath) {
                mapState.deathEvents.push({ t, wx: data.x, wy: data.y, teamIdx: symbolInfo.teamIdx });
            }

            // Always include death/spawn markers regardless of distance.
            if (!isDeath && !isSpawn) {
                if (last && Math.abs(last.wx - data.x) <= MIN_MOVE_WORLD && Math.abs(last.wy - data.y) <= MIN_MOVE_WORLD) {
                    lastWorldPos[name] = { x: data.x, y: data.y };
                    continue;
                }
            }

            // Teleport detection in world units (scale-independent)
            const lw = lastWorldPos[name];
            const isTeleport = !isDeath && !isSpawn && lw && (Math.abs(data.x - lw.x) > MAX_MOVE_PER_BUCKET || Math.abs(data.y - lw.y) > MAX_MOVE_PER_BUCKET);

            lastWorldPos[name] = { x: data.x, y: data.y };
            track.push({ wx: data.x, wy: data.y, t, teamIdx: symbolInfo.teamIdx, tp: isTeleport, death: isDeath, spawn: isSpawn });
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

// Stroke a fading "X" at a death location, sized to match the player circle.
// Color is the dead player's team color so kills are immediately attributable
// without needing to also draw a label.
// (DEATH_X_DURATION lives with the theme constants at the top of this file.)
function drawDeathX(ctx, x, y, teamIdx, alpha) {
    const r = 8; // a bit smaller than the player symbol circle (radius 13)
    const hex = TEAM_COLORS[teamIdx] || '#ff5050';
    const [rr, gg, bb] = hexToRgb(hex);
    ctx.save();
    ctx.strokeStyle = `rgba(${rr}, ${gg}, ${bb}, ${alpha.toFixed(2)})`;
    ctx.lineWidth = 2.5;
    ctx.lineCap = 'round';
    ctx.beginPath();
    ctx.moveTo(x - r, y - r);
    ctx.lineTo(x + r, y + r);
    ctx.moveTo(x + r, y - r);
    ctx.lineTo(x - r, y + r);
    ctx.stroke();
    ctx.restore();
}

// Draw a fading "D" superimposed on the death-X to mark drops where the
// dying player left an RL or LG backpack. Weapon-coded fill (RL red, LG
// cyan) lets viewers tell the two apart at a glance; a black outline
// keeps the letter readable against the team-colored X behind it. Fades
// in lockstep with the underlying X (same DEATH_X_DURATION). We don't
// yet track pickup time, so the D can't show a "still on ground" state —
// it just fades, same as the death X.
function drawDropD(ctx, x, y, weapon, alpha) {
    const a = alpha.toFixed(2);
    let fill;
    if      (weapon === 'rl') fill = `rgba(255, 107, 107, ${a})`;
    else if (weapon === 'lg') fill = `rgba(0, 217, 255, ${a})`;
    else                      fill = `rgba(255, 255, 255, ${a})`;
    ctx.save();
    ctx.font = 'bold 28px sans-serif';
    ctx.textAlign = 'center';
    // Use the alphabetic baseline + measured glyph metrics to put the
    // letter's *visual* center at (x, y). textBaseline:'middle' is
    // close but not exact for sans-serif "D" — it leaves a few pixels
    // of optical drift between the X center and the D center.
    ctx.textBaseline = 'alphabetic';
    const m = ctx.measureText('D');
    const ascent  = m.actualBoundingBoxAscent  || 20; // sane fallback
    const descent = m.actualBoundingBoxDescent || 0;
    const yDraw = y + (ascent - descent) / 2;
    ctx.lineWidth = 5;
    ctx.strokeStyle = `rgba(0, 0, 0, ${a})`;
    ctx.strokeText('D', x, yDraw);
    ctx.fillStyle = fill;
    ctx.fillText('D', x, yDraw);
    ctx.restore();
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

    // Normalize to CSS pixel coordinates. The canvas backing store is sized
    // to cssDims * devicePixelRatio for sharp rendering on HiDPI displays;
    // setTransform(dpr,...) makes every subsequent draw interpret its
    // coordinates in CSS px while rasterising at physical resolution.
    const dpr = mapState.dpr || 1;
    const cssW = mapState.canvasCssW || canvas.width;
    const cssH = mapState.canvasCssH || canvas.height;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

    // Follow-player: pin the camera on the tracked player this frame by
    // adjusting panX/panY so their symbol lands at canvas center.
    if (mapState.followPlayer && bucket && bucket.p) {
        const fp = bucket.p[mapState.followPlayer];
        if (fp && !(fp.x === 0 && fp.y === 0)) {
            _wtc.panX = 0;
            _wtc.panY = 0;
            const pos = worldToCanvas(fp.x, fp.y);
            _wtc.panX = cssW / 2 - pos.x;
            _wtc.panY = cssH / 2 - pos.y;
        }
    }

    // Clear
    ctx.fillStyle = '#0a0a15';
    ctx.fillRect(0, 0, cssW, cssH);

    // Process location groups once (cache in mapState)
    if (!mapState.locationGroups && mapState.locations.length > 0) {
        mapState.locationGroups = processLocationGroups(mapState.locations);
    }

    // Draw the location underlay (backdrop + per-loc regions + outlines +
    // labels). Fresh each frame so it follows pan / zoom precisely and stays
    // crisp at any zoom level.
    drawLocationLayer(ctx);

    // Draw region control overlay (colored by controlling team)
    if (mapState.controlRegions && mapState.regionToGroups) {
        const controlStates = getRegionControlAtTime(time);
        if (controlStates) {
            drawRegionControlOverlay(ctx, controlStates);
        }
    }

    // Highlight regions that currently contain at least one player so the
    // viewer can tell which loc each symbol belongs to without squinting.
    const occupancyData = bucket ? (bucket.p) : null;
    if (occupancyData) {
        drawOccupiedRegionsOverlay(ctx, occupancyData);
    }

    // Draw tracks (per-player visibility controlled by enabledPlayers)
    drawTracks(ctx, time);

    // Z-depth pass for items + players: overlapping players occlude by z
    // (higher deck on top), and an item whose z is clearly higher than a
    // player also draws on top. Items carry a downward sort bias
    // (ITEM_Z_TOP_THRESHOLD) so they lose the tie when a player stands on
    // them — the common case — but win when they sit a real floor above.
    const playerData = bucket ? bucket.p : null;
    drawItemsAndPlayersZSorted(ctx, time, playerData);

    // Recent-death markers — drawn last so the X sits on top of everything
    // else and stays visible for DEATH_X_DURATION seconds, fading linearly.
    // Linear scan is fine: a long match has on the order of 100-200 deaths
    // and this loop runs at most once per bucket tick.
    const deaths = mapState.deathEvents;
    if (deaths && deaths.length > 0) {
        for (const e of deaths) {
            const dt = time - e.t;
            if (dt < 0 || dt > DEATH_X_DURATION) continue;
            const alpha = 1 - dt / DEATH_X_DURATION;
            const pos = worldToCanvasNew(e.wx, e.wy);
            drawDeathX(ctx, pos.x, pos.y, e.teamIdx, alpha);
        }
    }

    // Drop markers — superimposed on the death X at the same world
    // position (KTX drops the backpack at the dying player's origin).
    // Fades on the same timeline as the X.
    const drops = mapState.dropEvents;
    if (drops && drops.length > 0) {
        for (const e of drops) {
            const dt = time - e.t;
            if (dt < 0 || dt > DEATH_X_DURATION) continue;
            const alpha = 1 - dt / DEATH_X_DURATION;
            const pos = worldToCanvasNew(e.wx, e.wy);
            drawDropD(ctx, pos.x, pos.y, e.weapon, alpha);
        }
    }
}

// ─── Map Items (armor / weapon / MH / powerup overlays) ────────────────────
//
// Draws a small square per tracked item on the map. Armors render as
// solid-filled coloured squares (RA red, YA yellow, GA green). Weapons,
// MH, and powerups render as black squares with a coloured outline +
// short text label that reuses the timeline colour palette so users
// pattern-match weapons across views. Items currently taken are dimmed.

// Display metadata per item kind. Armors render as a solid-coloured
// square with black text; weapons / MH / powerups as a black square
// with a coloured outline and text in the outline colour. Kinds not
// listed here (ammo, small health) are skipped on the map and in the
// sidebar.
const ITEM_MARKER_STYLES = {
    ra:   { fill: 'rgb(255, 50, 50)',   outline: null,                   label: 'RA', textColor: '#000' },
    ya:   { fill: 'rgb(255, 200, 0)',   outline: null,                   label: 'YA', textColor: '#000' },
    ga:   { fill: 'rgb(0, 180, 0)',     outline: null,                   label: 'GA', textColor: '#000' },
    mh:   { fill: '#000',               outline: 'rgb(0, 200, 83)',      label: 'MH' },
    rl:   { fill: '#000',               outline: 'rgb(255, 107, 107)',   label: 'RL' },
    lg:   { fill: '#000',               outline: 'rgb(0, 217, 255)',     label: 'LG' },
    ssg:  { fill: '#000',               outline: '#aaaaaa',              label: 'SS' },
    gl:   { fill: '#000',               outline: '#c78a3a',              label: 'GL' },
    ng:   { fill: '#000',               outline: '#8090a0',              label: 'NG' },
    sng:  { fill: '#000',               outline: 'rgb(180, 140, 100)',   label: 'SN' },
    quad: { fill: '#000',               outline: 'rgb(0, 150, 255)',     label: 'Q'  },
    pent: { fill: '#000',               outline: 'rgb(255, 0, 0)',       label: 'P'  },
    ring: { fill: '#000',               outline: 'rgb(255, 235, 59)',    label: 'I'  },
};

// Kinds surfaced in the sidebar Items panel. Armors, MH, the two
// "fight-over" weapons (RL, LG), and powerups — the core resources
// players actively contest. Other weapons / ammo / small health are
// still rendered on the map but omitted from the scrolling list.
const PANEL_ITEM_KINDS = new Set(['ra', 'ya', 'ga', 'mh', 'rl', 'lg', 'quad', 'pent', 'ring']);

const ITEM_MARKER_SIZE = 20;  // 25% larger than the prior 16 px baseline
const ITEM_DIM_ALPHA = 0.35;  // alpha multiplier when item is taken

// isItemUp returns true if the item is available to be picked up at the
// given time — i.e., we're inside an "available" phase. Handles the MH
// pending-respawn case (phase with TakenAt set but RespawnAt==0 is
// still held).
function isItemUp(item, time) {
    const phases = item.phases;
    if (!phases || phases.length === 0) return true;
    for (let i = 0; i < phases.length; i++) {
        const p = phases[i];
        if (p.availableFrom > time) break;
        const takenAt = p.takenAt || 0;
        if (takenAt === 0) return true; // phase open, not yet taken (this phase is current → up)
        if (time < takenAt) return true; // available window
        // taken at takenAt; respawnAt may be 0 (MH pending) or a future/past value
        const respawnAt = p.respawnAt || 0;
        if (respawnAt > 0 && time >= respawnAt) {
            // Respawned; if this is the last phase or the next phase
            // opens at respawnAt, let the loop continue.
            continue;
        }
        return false;
    }
    return true;
}

// itemStatus returns a small object describing status at the given time:
//   { up: bool, secsToRespawn: number|null, pending: bool }
// secsToRespawn is the wait time in seconds (null when up).
// pending is true for MH in its rot window (TakenAt set, RespawnAt==0).
function itemStatus(item, time) {
    const phases = item.phases;
    if (!phases || phases.length === 0) {
        return { up: true, secsToRespawn: null, pending: false };
    }
    // Find the phase whose window contains `time` (availableFrom <= time < nextAvailableFrom).
    let activePhase = null;
    for (let i = 0; i < phases.length; i++) {
        const p = phases[i];
        const next = phases[i + 1];
        if (p.availableFrom <= time && (!next || next.availableFrom > time)) {
            activePhase = p;
            break;
        }
    }
    if (!activePhase) {
        // Before the first phase opens — treat as up.
        return { up: true, secsToRespawn: null, pending: false };
    }
    const takenAt = activePhase.takenAt || 0;
    if (takenAt === 0 || time < takenAt) {
        return { up: true, secsToRespawn: null, pending: false };
    }
    const respawnAt = activePhase.respawnAt || 0;
    if (respawnAt === 0) {
        // Held with pending respawn (MH during rot).
        return { up: false, secsToRespawn: null, pending: true };
    }
    if (time >= respawnAt) {
        return { up: true, secsToRespawn: null, pending: false };
    }
    return { up: false, secsToRespawn: respawnAt - time, pending: false };
}

// Items are biased this much below their real z when sorting against
// players, so a player standing at the same floor as an item (same z)
// draws on top. An item only occludes a player when its z exceeds the
// player's by at least this clearance — i.e. the item sits on a real
// level above the player.
const ITEM_Z_TOP_THRESHOLD = 48;

// Combined z-sorted items-and-players pass. Building a single list lets
// the draw order mix items and players correctly — two players on
// different decks occlude in z order, an item clearly above a player
// draws on top, and the common case of a player standing on a pickup
// draws the player on top.
function drawItemsAndPlayersZSorted(ctx, time, playerData) {
    const iconScale = mapIconScale();
    const zRange = mapState.zRange || { lo: 0, hi: 0 };
    const zSpan = zRange.hi - zRange.lo;

    const drawables = [];
    const items = currentResult?.items?.items;
    if (items && items.length > 0) {
        for (const item of items) {
            const style = ITEM_MARKER_STYLES[item.kind];
            if (!style) continue;
            drawables.push({
                kind: 'i',
                sortZ: (item.z || 0) - ITEM_Z_TOP_THRESHOLD,
                item, style
            });
        }
    }
    if (playerData) {
        for (const [name, data] of Object.entries(playerData)) {
            if (data.x === 0 && data.y === 0) continue;
            const symbolInfo = mapState.playerSymbols[name];
            if (!symbolInfo) continue;
            drawables.push({
                kind: 'p',
                sortZ: data.z || 0,
                data, symbolInfo
            });
        }
    }
    if (drawables.length === 0) return;

    drawables.sort((a, b) => a.sortZ - b.sortZ);

    const itemSize = ITEM_MARKER_SIZE * iconScale;
    const itemHalf = itemSize / 2;
    const itemFontPx = Math.round(10 * iconScale);

    ctx.save();
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';

    for (const d of drawables) {
        if (d.kind === 'i') {
            drawSingleMapItem(ctx, time, d.item, d.style,
                              itemSize, itemHalf, itemFontPx);
        } else {
            drawSinglePlayer(ctx, d.data, d.symbolInfo,
                             iconScale, zRange, zSpan);
        }
    }

    ctx.globalAlpha = 1.0;
    ctx.restore();
}

function drawSingleMapItem(ctx, time, item, style, size, half, fontPx) {
    const pos = worldToCanvas(item.x, item.y);
    const up = isItemUp(item, time);
    ctx.globalAlpha = up ? 1.0 : ITEM_DIM_ALPHA;

    const x = Math.round(pos.x - half);
    const y = Math.round(pos.y - half);

    ctx.fillStyle = style.fill;
    ctx.fillRect(x, y, size, size);

    if (style.outline) {
        ctx.strokeStyle = style.outline;
        ctx.lineWidth = 1.5;
        ctx.strokeRect(x + 0.5, y + 0.5, size - 1, size - 1);
    }

    if (style.label) {
        ctx.font = `bold ${fontPx}px -apple-system, BlinkMacSystemFont, sans-serif`;
        ctx.fillStyle = style.textColor || style.outline || '#fff';
        ctx.fillText(style.label, pos.x, pos.y + 1);
    }
    ctx.globalAlpha = 1.0;
}

function drawSinglePlayer(ctx, data, symbolInfo, iconScale, zRange, zSpan) {
    const pos = worldToCanvas(data.x, data.y);

    // Per-player z-based size scale: players near the top of the map
    // (98th percentile z) render 25% larger than those near the bottom
    // (2nd percentile), linearly interpolated. Applied on top of the
    // zoom-driven iconScale.
    let zScale = 1;
    if (zSpan > 0) {
        let t = ((data.z || 0) - zRange.lo) / zSpan;
        if (t < 0) t = 0;
        if (t > 1) t = 1;
        zScale = 1 + 0.25 * t;
    }
    const totalScale = iconScale * zScale;
    const symSize = PLAYER_SYMBOL_BASE_SIZE * totalScale;
    const orbitRadius = 14 * totalScale;
    const badgeRadius = 5 * totalScale;

    const teamHex = TEAM_COLORS[symbolInfo.teamIdx] || TEAM_COLORS[0];
    drawPlayerSymbolAt(ctx, symbolInfo.symbol, teamHex, pos.x, pos.y, symSize);

    const badges = getActiveBadges(data);
    if (badges.length > 0) {
        drawBadgesAroundCenter(ctx, badges, pos.x, pos.y, orbitRadius, badgeRadius);
    }
}

// ─── Map Items Panel (sidebar list) ────────────────────────────────────────
//
// Live-updating table of every tracked item with status ("up" / "X.Xs" /
// "held") and region. Shown only when result.items is populated (KTX
// demos); hidden for non-KTX sources that produce no item events.

// Cache the sorted-by-name item list and the <tr>/<td> refs so each
// setCurrentTime tick only updates text, not layout.
const _itemsPanelState = {
    lastResult: null,
    rows: [],       // [{ item, tr, statusTd }]
};

// buildItemSwatch returns a <span> that visually mirrors the on-map
// marker for a given item kind: solid-colour armor squares with a
// black label, or black squares with a coloured outline + matching
// label for weapons / MH / powerups.
function buildItemSwatch(style) {
    const sq = document.createElement('span');
    sq.className = 'item-swatch';
    sq.style.background = style.fill;
    if (style.outline) {
        sq.style.border = `1.5px solid ${style.outline}`;
        sq.style.boxSizing = 'border-box';
    }
    if (style.label) {
        sq.textContent = style.label;
        sq.style.color = style.textColor || style.outline || '#fff';
    }
    return sq;
}

function renderItemsPanel() {
    const panel = document.getElementById('map-items-panel');
    const body = document.getElementById('map-items-body');
    if (!panel || !body) return;

    const items = currentResult?.items?.items;
    if (!items || items.length === 0) {
        panel.style.display = 'none';
        _itemsPanelState.lastResult = null;
        _itemsPanelState.rows = [];
        return;
    }

    // Rebuild rows when the underlying result changes.
    if (_itemsPanelState.lastResult !== currentResult) {
        body.innerHTML = '';
        _itemsPanelState.rows = [];
        // Display order: armors first, then MH, then RL/LG, then
        // powerups. Kinds outside PANEL_ITEM_KINDS are filtered out so
        // the sidebar stays focused on the items players contest.
        const KIND_ORDER = { ra: 0, ya: 1, ga: 2, mh: 3, rl: 4, lg: 5, quad: 6, pent: 7, ring: 8 };
        const sorted = items
            .filter(it => PANEL_ITEM_KINDS.has(it.kind) && ITEM_MARKER_STYLES[it.kind])
            .sort((a, b) => {
                const ka = KIND_ORDER[a.kind] ?? 99;
                const kb = KIND_ORDER[b.kind] ?? 99;
                if (ka !== kb) return ka - kb;
                return a.name.localeCompare(b.name);
            });
        for (const item of sorted) {
            const style = ITEM_MARKER_STYLES[item.kind];
            const tr = document.createElement('tr');
            const swatch = document.createElement('td');
            swatch.appendChild(buildItemSwatch(style));
            const name = document.createElement('td');
            name.className = 'item-name';
            name.textContent = item.name.toUpperCase().replace(/_/g, ' ');
            const loc = document.createElement('td');
            loc.className = 'item-loc';
            loc.textContent = item.loc || '';
            const status = document.createElement('td');
            status.className = 'item-status';
            tr.appendChild(swatch);
            tr.appendChild(name);
            tr.appendChild(loc);
            tr.appendChild(status);
            body.appendChild(tr);
            _itemsPanelState.rows.push({ item, tr, statusTd: status });
        }
        _itemsPanelState.lastResult = currentResult;
    }

    panel.style.display = '';
    updateItemsPanelStatus(mapState.currentTime);
}

function updateItemsPanelStatus(time) {
    for (const row of _itemsPanelState.rows) {
        const s = itemStatus(row.item, time);
        row.tr.classList.toggle('taken', !s.up);
        if (s.up) {
            row.statusTd.textContent = 'up';
            row.statusTd.className = 'item-status up';
        } else if (s.pending) {
            row.statusTd.textContent = 'held';
            row.statusTd.className = 'item-status pending';
        } else {
            row.statusTd.textContent = s.secsToRespawn.toFixed(1) + 's';
            row.statusTd.className = 'item-status respawn';
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

        // Pre-convert the visible window of world-space points into canvas
        // pixels at the current pan / zoom so the inner draw loop stays
        // allocation-free and worldToCanvas's shared _tmpPt isn't clobbered
        // between consecutive reads.
        const cpts = new Array(endIdx - startIdx + 1);
        for (let i = startIdx; i <= endIdx; i++) {
            const pt = points[i];
            const c = worldToCanvasNew(pt.wx, pt.wy);
            cpts[i - startIdx] = { x: c.x, y: c.y, spawn: pt.spawn, death: pt.death, tp: pt.tp };
        }

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
        ctx.moveTo(cpts[0].x, cpts[0].y);

        if (cpts[0].spawn) markers.push({ x: cpts[0].x, y: cpts[0].y, type: 'spawn' });

        for (let i = 1; i < cpts.length; i++) {
            const pt = cpts[i];

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
                const prev = cpts[i - 1];
                ctx.moveTo(prev.x, prev.y);
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
        return null;
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

function findBucketAtTime(time) {
    return findHighResBucketAtTime(time);
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

    const followSel = document.getElementById('map-follow');
    if (followSel) {
        followSel.addEventListener('change', (e) => {
            setFollowPlayer(e.target.value || null);
        });
    }

    const resetViewBtn = document.getElementById('map-reset-view');
    if (resetViewBtn) {
        resetViewBtn.addEventListener('click', () => { resetMapView(); });
    }

    const fsBtn = document.getElementById('map-fullscreen');
    if (fsBtn) {
        fsBtn.addEventListener('click', () => { toggleMapFullscreen(); });
    }

    // React to fullscreen changes regardless of who triggered them (button,
    // Escape key, browser UI). Only one listener is needed for the page.
    if (!setupMapTrailControls.__fsListenerAttached) {
        document.addEventListener('fullscreenchange', onMapFullscreenChange);
        window.addEventListener('resize', onMapWindowResize);
        setupMapTrailControls.__fsListenerAttached = true;
    }
}

// installMapInteraction adds pan / zoom / click handlers to the map canvas.
// Pan: left-drag. Zoom: mouse wheel (centered on cursor). Click (no drag):
// dispatched through handleMapCanvasClick — used by follow-player to toggle
// follow on a player symbol. Double-click resets the view.
function installMapInteraction(canvas) {
    if (!canvas || canvas.__mapInteractionInstalled) return;
    canvas.__mapInteractionInstalled = true;

    const CLICK_MAX_MOTION_PX = 5;
    const ZOOM_MIN = 0.5;
    const ZOOM_MAX = 12;

    const drag = {
        active: false,
        button: -1,
        startX: 0, startY: 0,
        lastX: 0, lastY: 0,
        moved: false,
    };

    function canvasPointFromEvent(ev) {
        // CSS pixel coords relative to the canvas origin — matches what
        // renderMap / worldToCanvas use now that setTransform(dpr) handles
        // the CSS → physical scaling for drawing.
        const rect = canvas.getBoundingClientRect();
        return {
            x: ev.clientX - rect.left,
            y: ev.clientY - rect.top,
        };
    }

    canvas.addEventListener('mousedown', (ev) => {
        if (ev.button !== 0) return;
        const p = canvasPointFromEvent(ev);
        drag.active = true;
        drag.button = ev.button;
        drag.startX = drag.lastX = p.x;
        drag.startY = drag.lastY = p.y;
        drag.moved = false;
        ev.preventDefault();
    });

    window.addEventListener('mousemove', (ev) => {
        if (!drag.active) return;
        const p = canvasPointFromEvent(ev);
        const dx = p.x - drag.lastX;
        const dy = p.y - drag.lastY;
        drag.lastX = p.x;
        drag.lastY = p.y;
        if (!drag.moved) {
            const totalDx = p.x - drag.startX;
            const totalDy = p.y - drag.startY;
            if (Math.abs(totalDx) > CLICK_MAX_MOTION_PX ||
                Math.abs(totalDy) > CLICK_MAX_MOTION_PX) {
                drag.moved = true;
                // Starting a pan drops follow-mode so the user isn't fighting the camera.
                if (mapState.followPlayer) {
                    mapState.followPlayer = null;
                    syncFollowSelectUI();
                }
                canvas.style.cursor = 'grabbing';
            }
        }
        if (drag.moved) {
            _wtc.panX += dx;
            _wtc.panY += dy;
            mapState.renderDirty = true;
            renderMap(mapState.currentTime);
        }
    });

    window.addEventListener('mouseup', (ev) => {
        if (!drag.active) return;
        const wasClick = !drag.moved;
        drag.active = false;
        drag.button = -1;
        canvas.style.cursor = '';
        if (wasClick) {
            const p = canvasPointFromEvent(ev);
            handleMapCanvasClick(p.x, p.y);
        }
    });

    canvas.addEventListener('wheel', (ev) => {
        ev.preventDefault();
        const p = canvasPointFromEvent(ev);
        const worldBefore = canvasToWorld(p.x, p.y);
        let newZoom = _wtc.zoomK * Math.exp(-ev.deltaY * 0.0015);
        if (newZoom < ZOOM_MIN) newZoom = ZOOM_MIN;
        if (newZoom > ZOOM_MAX) newZoom = ZOOM_MAX;
        if (newZoom === _wtc.zoomK) return;
        _wtc.zoomK = newZoom;
        // Adjust pan so the world point under the cursor stays anchored.
        // Follow-mode intentionally survives zoom — renderMap's follow step
        // will re-center on the tracked player using the new zoom level, so
        // zoom becomes "zoom in on the player" rather than dropping follow.
        const sx = _wtc.scale * _wtc.zoomK;
        _wtc.panX = p.x - _wtc.offsetX - (worldBefore.x - _wtc.minX) * sx;
        _wtc.panY = p.y - _wtc.canvasH + _wtc.offsetY + (worldBefore.y - _wtc.minY) * sx;
        mapState.renderDirty = true;
        renderMap(mapState.currentTime);
    }, { passive: false });

    canvas.addEventListener('dblclick', (ev) => {
        ev.preventDefault();
        resetMapView();
    });

    canvas.style.cursor = 'grab';
}

// Dispatched from installMapInteraction on a true click (no drag). Used for
// player-symbol hit-testing to toggle follow-player mode.
function handleMapCanvasClick(cx, cy) {
    const hit = hitTestPlayerSymbol(cx, cy, mapState.currentTime);
    if (hit) {
        setFollowPlayer(mapState.followPlayer === hit ? null : hit);
    }
}

// ─── Follow-player ────────────────────────────────────────────────────────

// Slightly larger than the base symbol radius so the click-to-follow hit
// area stays generous even when a high-deck / max-zoom player renders at
// the 1.5 * 1.25 ≈ 1.88x upper bound.
const FOLLOW_HIT_RADIUS_PX = 24;

function hitTestPlayerSymbol(cx, cy, time) {
    const bucket = findBucketAtTime(time);
    if (!bucket || !bucket.p) return null;
    let best = null;
    let bestD2 = FOLLOW_HIT_RADIUS_PX * FOLLOW_HIT_RADIUS_PX;
    for (const [name, data] of Object.entries(bucket.p)) {
        if (data.x === 0 && data.y === 0) continue;
        const pos = worldToCanvas(data.x, data.y);
        const dx = pos.x - cx;
        const dy = pos.y - cy;
        const d2 = dx * dx + dy * dy;
        if (d2 <= bestD2) {
            bestD2 = d2;
            best = name;
        }
    }
    return best;
}

function setFollowPlayer(name) {
    mapState.followPlayer = name || null;
    if (mapState.followPlayer) {
        // Entering follow mode clears any previous manual pan so the camera
        // lock is relative to a fit-to-canvas baseline. Zoom is preserved.
        _wtc.panX = 0;
        _wtc.panY = 0;
    }
    syncFollowSelectUI();
    mapState.renderDirty = true;
    renderMap(mapState.currentTime);
}

function syncFollowSelectUI() {
    const sel = document.getElementById('map-follow');
    if (!sel) return;
    sel.value = mapState.followPlayer || '';
}

function rebuildFollowSelect() {
    const sel = document.getElementById('map-follow');
    if (!sel) return;
    const prev = mapState.followPlayer || '';
    sel.innerHTML = '';
    const off = document.createElement('option');
    off.value = '';
    off.textContent = 'Off';
    sel.appendChild(off);
    const names = Object.keys(mapState.fullTrails).sort((a, b) => a.localeCompare(b));
    for (const n of names) {
        const opt = document.createElement('option');
        opt.value = n;
        opt.textContent = n;
        sel.appendChild(opt);
    }
    if (prev && !names.includes(prev)) {
        mapState.followPlayer = null;
    }
    sel.value = mapState.followPlayer || '';
}

// ─── Fullscreen ───────────────────────────────────────────────────────────

function toggleMapFullscreen() {
    const panel = document.querySelector('#tab-map .map-panel');
    if (!panel) return;
    if (document.fullscreenElement === panel) {
        document.exitFullscreen().catch(() => {});
    } else {
        const req = panel.requestFullscreen?.bind(panel);
        if (req) req().catch(() => {});
    }
}

// Remembers the unified-timeline's original parent / sibling slot so we can
// put it back after leaving fullscreen. Populated the first time we relocate.
let _fsTimelineHome = null;

function onMapFullscreenChange() {
    const panel = document.querySelector('#tab-map .map-panel');
    if (!panel) return;
    const nowFs = document.fullscreenElement === panel;
    panel.classList.toggle('map-panel--fullscreen', nowFs);
    mapState.fullscreen = nowFs;
    const btn = document.getElementById('map-fullscreen');
    if (btn) btn.textContent = nowFs ? 'Exit fullscreen' : 'Fullscreen';

    // Relocate the shared timeline (playback buttons + scrubber) into the
    // fullscreen map panel so it stays usable. On exit, put it back.
    const tl = document.getElementById('unified-timeline');
    if (tl) {
        if (nowFs) {
            if (!_fsTimelineHome) {
                _fsTimelineHome = { parent: tl.parentNode, next: tl.nextSibling };
            }
            panel.appendChild(tl);
        } else if (_fsTimelineHome && _fsTimelineHome.parent) {
            _fsTimelineHome.parent.insertBefore(tl, _fsTimelineHome.next);
        }
    }

    // Canvas backing store must match the new container size; re-render.
    resizeMapCanvas();
    mapState.renderDirty = true;
    renderMap(mapState.currentTime);
}

let _mapResizeRafId = null;
function onMapWindowResize() {
    // Only active (debounced to next frame) while in fullscreen; the
    // non-fullscreen canvas is fixed-size so the resize listener is a no-op.
    if (!mapState.fullscreen) return;
    if (_mapResizeRafId !== null) return;
    _mapResizeRafId = requestAnimationFrame(() => {
        _mapResizeRafId = null;
        resizeMapCanvas();
        mapState.renderDirty = true;
        renderMap(mapState.currentTime);
    });
}

// ─── Loc Graph (Cytoscape) ────────────────────────────────────────────────
//
// Prototype using Cytoscape.js for the loc-graph rendering. Swap-in
// alternative to the earlier hand-rolled canvas version so we can evaluate
// layout quality, interactivity and future extensibility (route analysis,
// player animation on top of nodes).
//
// FIXME: when per-time player positions are added later, this tab will need
// to be added to TABS_WITH_TIMELINE so the unified scrubber appears.

const locGraphState = {
    cy: null,            // Cytoscape instance
    graph: null,         // { locs, edges } from result.locGraph
    result: null,        // Full result — used for filter repopulation on re-init
    initialized: false,
    tooltip: null        // Hover tooltip DOM node
};

// cytoscape-fcose registers itself on the global `cytoscape` object when
// loaded; guard the registration so re-loading the file during dev doesn't
// throw.
function registerCytoscapeExtensions() {
    if (typeof cytoscape === 'undefined') return;
    if (typeof cytoscapeFcose !== 'undefined' && !cytoscape.__fcoseRegistered) {
        cytoscape.use(cytoscapeFcose);
        cytoscape.__fcoseRegistered = true;
    }
}

function initLocGraphView(result) {
    const graph = result && result.locGraph;
    const container = document.getElementById('locgraph-canvas');
    if (!container) return;

    locGraphState.graph = graph || null;
    locGraphState.result = result;

    const noData = document.getElementById('locgraph-no-data');
    if (!graph || !graph.locs || graph.locs.length === 0) {
        if (noData) noData.style.display = 'block';
        if (locGraphState.cy) {
            locGraphState.cy.destroy();
            locGraphState.cy = null;
        }
        return;
    }
    if (noData) noData.style.display = 'none';

    populateLocGraphFilter(result);

    if (!locGraphState.initialized) {
        setupLocGraphControls();
        locGraphState.initialized = true;
    }

    buildOrRefreshCytoscape();
}

function populateLocGraphFilter(result) {
    const select = document.getElementById('locgraph-filter');
    if (!select) return;

    const opts = [{ value: 'all', label: 'All' }];
    const teams = (result.demoInfo && result.demoInfo.teams) || [];
    const players = (result.demoInfo && result.demoInfo.players) || [];

    // Hide team options in duel mode (team name == player name for every player).
    const isDuel = players.length > 0 && players.every(p => p.team === p.name);
    if (!isDuel) {
        for (const t of teams) opts.push({ value: 'team:' + t, label: 'Team: ' + t });
    }
    for (const p of players) {
        if (!p.name) continue;
        opts.push({ value: 'player:' + p.name, label: p.name });
    }

    const prev = select.value;
    select.innerHTML = '';
    for (const o of opts) {
        const opt = document.createElement('option');
        opt.value = o.value;
        opt.textContent = o.label;
        if (o.value === prev) opt.selected = true;
        select.appendChild(opt);
    }
    if (!opts.some(o => o.value === prev)) select.value = 'all';
}

function setupLocGraphControls() {
    const on = (id, ev, fn) => {
        const el = document.getElementById(id);
        if (el) el.addEventListener(ev, fn);
    };
    on('locgraph-filter', 'change', buildOrRefreshCytoscape);
    on('locgraph-edge-mode', 'change', buildOrRefreshCytoscape);
    on('locgraph-min-edge', 'change', buildOrRefreshCytoscape);
    on('locgraph-layout', 'change', buildOrRefreshCytoscape);
    on('locgraph-show-labels', 'change', applyLocGraphStyle);
    on('locgraph-label-size', 'change', applyLocGraphStyle);
    on('locgraph-relayout', 'click', () => runLocGraphLayout(true));
    on('locgraph-fit', 'click', () => { if (locGraphState.cy) locGraphState.cy.fit(undefined, 30); });
}

function getLocGraphFilter() {
    const sel = document.getElementById('locgraph-filter');
    const val = sel ? sel.value : 'all';
    if (val.startsWith('team:')) return { kind: 'team', key: val.slice(5) };
    if (val.startsWith('player:')) return { kind: 'player', key: val.slice(7) };
    return { kind: 'all', key: '' };
}

function nodeWeightFor(node, filter) {
    if (filter.kind === 'player') return node.byPlayer?.[filter.key] || 0;
    if (filter.kind === 'team') return node.byTeam?.[filter.key] || 0;
    return node.total || 0;
}

function edgeWeightFor(edge, filter) {
    if (filter.kind === 'player') return edge.byPlayer?.[filter.key] || 0;
    if (filter.kind === 'team') return edge.byTeam?.[filter.key] || 0;
    return edge.total || 0;
}

// Build Cytoscape elements from the current graph + filter + edge-mode.
// Nodes/edges carry their filtered weight in data so styles can reference it
// directly instead of re-computing in the mapper.
function buildCytoscapeElements() {
    const { graph } = locGraphState;
    if (!graph) return [];
    const filter = getLocGraphFilter();
    const edgeModeSel = document.getElementById('locgraph-edge-mode');
    const edgeMode = edgeModeSel ? edgeModeSel.value : 'all';
    const minEdgeSel = document.getElementById('locgraph-min-edge');
    const minEdge = minEdgeSel ? (parseInt(minEdgeSel.value, 10) || 1) : 1;

    let maxNodeWeight = 0;
    for (const n of graph.locs) {
        const w = nodeWeightFor(n, filter);
        if (w > maxNodeWeight) maxNodeWeight = w;
    }
    let maxEdgeWeight = 0;
    for (const e of graph.edges) {
        if (edgeMode === 'normal' && e.kind !== 'normal') continue;
        if (edgeMode === 'teleport' && e.kind !== 'teleport') continue;
        const w = edgeWeightFor(e, filter);
        if (w < minEdge) continue;
        if (w > maxEdgeWeight) maxEdgeWeight = w;
    }

    const elements = [];

    for (const n of graph.locs) {
        const w = nodeWeightFor(n, filter);
        const norm = maxNodeWeight > 0 ? w / maxNodeWeight : 0;
        elements.push({
            group: 'nodes',
            data: {
                id: 'n:' + n.name,
                name: n.name,
                weight: w,
                weightNorm: norm,
                total: n.total,
                byPlayer: n.byPlayer,
                byTeam: n.byTeam || {},
                // Per-filter dim state: mostly grey out nodes with zero
                // contribution in non-"all" filters so the subgraph is
                // visually clear but context is preserved.
                dim: filter.kind !== 'all' && w === 0
            },
            // Preset/geographic layout reads world coords directly from data.
            // Invert Y so "up" on screen matches "up" in map (QW Y is up).
            position: { x: n.x || 0, y: -(n.y || 0) }
        });
    }

    for (const e of graph.edges) {
        if (edgeMode === 'normal' && e.kind !== 'normal') continue;
        if (edgeMode === 'teleport' && e.kind !== 'teleport') continue;
        const w = edgeWeightFor(e, filter);
        if (w === 0) continue; // Prune invisible edges for this filter
        if (w < minEdge) continue; // UI minimum-edge-count filter
        const norm = maxEdgeWeight > 0 ? w / maxEdgeWeight : 0;
        elements.push({
            group: 'edges',
            data: {
                id: 'e:' + e.from + '->' + e.to,
                source: 'n:' + e.from,
                target: 'n:' + e.to,
                weight: w,
                weightNorm: norm,
                kind: e.kind,
                total: e.total,
                byPlayer: e.byPlayer,
                byTeam: e.byTeam || {}
            }
        });
    }

    return elements;
}

function buildLocGraphStyle() {
    const showLabels = document.getElementById('locgraph-show-labels')?.checked ?? true;
    const labelSizeSel = document.getElementById('locgraph-label-size');
    const labelSize = labelSizeSel ? parseInt(labelSizeSel.value, 10) || 14 : 14;
    const filter = getLocGraphFilter();

    // Pick a node fill based on the filter so "Team: red" nodes are tinted
    // with the team colour. In the "all" view, fall back to a neutral blue.
    let nodeFill = '#8fb3ff';
    if (filter.kind === 'team') {
        const teams = timelineState.teams || [];
        const idx = teams.indexOf(filter.key);
        nodeFill = (idx >= 0 && idx < TEAM_COLORS.length) ? TEAM_COLORS[idx] : '#8fb3ff';
    } else if (filter.kind === 'player') {
        nodeFill = '#ffc107';
    }

    return [
        {
            selector: 'node',
            style: {
                'background-color': nodeFill,
                'border-color': 'rgba(0, 217, 255, 0.6)',
                'border-width': 1.5,
                // sqrt so diameter scales with sqrt(time); "eye" area ≈ time.
                'width':  'mapData(weightNorm, 0, 1, 16, 60)',
                'height': 'mapData(weightNorm, 0, 1, 16, 60)',
                'label': showLabels ? 'data(name)' : '',
                'color': '#dfe6f5',
                'font-size': labelSize,
                'font-family': 'Inter, sans-serif',
                'font-weight': 500,
                'text-valign': 'bottom',
                'text-margin-y': Math.max(4, labelSize * 0.3),
                'text-outline-color': '#0a0a15',
                'text-outline-width': Math.max(2, labelSize * 0.18)
            }
        },
        {
            selector: 'node[?dim]',
            style: {
                'background-opacity': 0.25,
                'border-opacity': 0.2,
                'text-opacity': 0.35
            }
        },
        {
            selector: 'edge',
            style: {
                'curve-style': 'bezier',
                'control-point-step-size': 40,
                'width': 'mapData(weightNorm, 0, 1, 1, 7)',
                'line-color': '#8fb3ff',
                'target-arrow-color': '#8fb3ff',
                'target-arrow-shape': 'triangle',
                'arrow-scale': 1.1,
                'opacity': 0.8
            }
        },
        {
            selector: 'edge[kind = "teleport"]',
            style: {
                'line-color': '#00d9ff',
                'target-arrow-color': '#00d9ff',
                'line-style': 'dashed',
                'line-dash-pattern': [8, 4]
            }
        },
        {
            selector: ':selected',
            style: {
                'border-color': '#ffc107',
                'border-width': 3,
                'line-color': '#ffc107',
                'target-arrow-color': '#ffc107'
            }
        },
        {
            selector: '.highlight',
            style: {
                'opacity': 1,
                'z-index': 50
            }
        },
        {
            selector: '.faded',
            style: {
                'opacity': 0.15
            }
        }
    ];
}

// Build the cytoscape instance on first call, otherwise swap in the new
// element set + layout. Keeping the instance alive preserves pan/zoom state
// when the user toggles a filter.
function buildOrRefreshCytoscape() {
    if (!locGraphState.graph) return;
    registerCytoscapeExtensions();
    const container = document.getElementById('locgraph-canvas');
    if (!container || typeof cytoscape === 'undefined') return;

    const elements = buildCytoscapeElements();

    if (!locGraphState.cy) {
        locGraphState.cy = cytoscape({
            container,
            elements,
            style: buildLocGraphStyle(),
            layout: { name: 'preset' },
            wheelSensitivity: 0.2,
            minZoom: 0.1,
            maxZoom: 4
        });
        attachLocGraphInteractions(locGraphState.cy);
        locGraphState.cy.on('zoom', scheduleLabelSizeUpdate);
    } else {
        locGraphState.cy.batch(() => {
            locGraphState.cy.elements().remove();
            locGraphState.cy.add(elements);
            locGraphState.cy.style(buildLocGraphStyle());
        });
    }

    runLocGraphLayout(false);
}

// Run the chosen layout. `animate` is false on filter changes (the common
// case) so layout runs instantly; the re-layout button passes true to get
// the animated effect.
function runLocGraphLayout(animate) {
    const cy = locGraphState.cy;
    if (!cy) return;
    const sel = document.getElementById('locgraph-layout');
    const name = sel ? sel.value : 'preset';

    let opts;
    if (name === 'preset') {
        opts = { name: 'preset', fit: true, padding: 30 };
    } else if (name === 'fcose') {
        opts = {
            name: 'fcose',
            quality: 'default',
            randomize: false,       // start from current positions
            animate: animate,
            animationDuration: 600,
            nodeRepulsion: 8000,
            idealEdgeLength: 80,
            edgeElasticity: 0.45,
            gravity: 0.25,
            fit: true,
            padding: 30
        };
    } else if (name === 'cose') {
        opts = { name: 'cose', animate: animate, padding: 30, fit: true };
    } else if (name === 'circle') {
        opts = { name: 'circle', animate: animate, padding: 30, fit: true };
    } else if (name === 'concentric') {
        opts = {
            name: 'concentric',
            animate: animate,
            padding: 30,
            fit: true,
            // Higher time spent = more central; Cytoscape expects larger
            // numbers to be more central.
            concentric: (node) => node.data('weight'),
            levelWidth: () => 1
        };
    } else {
        opts = { name: 'preset' };
    }

    const layout = cy.layout(opts);
    layout.one('layoutstop', updateDynamicLabelSize);
    layout.run();
    // Non-animated layouts don't always emit layoutstop synchronously;
    // run once now so the first paint already has correct sizing.
    updateDynamicLabelSize();
}

function applyLocGraphStyle() {
    if (!locGraphState.cy) return;
    locGraphState.cy.style(buildLocGraphStyle());
    updateDynamicLabelSize();
}

// Cytoscape font-size scales with zoom, so on wide maps (geographic
// preset) the fit-to-viewport zoom drops the effective pixel size below
// readability. Counter-act by recomputing font-size inversely proportional
// to the current zoom, clamped to a sensible range.
let _labelSizeRaf = 0;
function scheduleLabelSizeUpdate() {
    if (_labelSizeRaf) return;
    _labelSizeRaf = requestAnimationFrame(() => {
        _labelSizeRaf = 0;
        updateDynamicLabelSize();
    });
}
function updateDynamicLabelSize() {
    const cy = locGraphState.cy;
    if (!cy) return;
    const sel = document.getElementById('locgraph-label-size');
    const userSize = sel ? (parseInt(sel.value, 10) || 14) : 14;
    const zoom = cy.zoom() || 1;
    const target = Math.max(10, Math.min(48, userSize / zoom));
    cy.batch(() => {
        cy.nodes().style({
            'font-size': target,
            'text-margin-y': Math.max(4, target * 0.3),
            'text-outline-width': Math.max(2, target * 0.18)
        });
    });
}

// Click: show a tooltip with top-5 connections. Hover: fade the rest of the
// graph so the node's neighborhood is clear.
function attachLocGraphInteractions(cy) {
    // Tooltip DOM — created lazily, reused across hovers.
    const container = document.getElementById('locgraph-canvas');
    if (!locGraphState.tooltip) {
        const tip = document.createElement('div');
        tip.className = 'locgraph-tooltip';
        tip.style.display = 'none';
        container.parentElement.appendChild(tip);
        locGraphState.tooltip = tip;
    }
    const tip = locGraphState.tooltip;

    const hideTip = () => { tip.style.display = 'none'; };
    const showTipAt = (evt, html) => {
        tip.textContent = '';
        tip.innerHTML = html;
        tip.style.display = 'block';
        // Position via renderedPosition so it tracks pan/zoom.
        const rect = container.getBoundingClientRect();
        const x = evt.renderedPosition ? evt.renderedPosition.x : 0;
        const y = evt.renderedPosition ? evt.renderedPosition.y : 0;
        tip.style.left = (x + 12) + 'px';
        tip.style.top = (rect.height - y - 12) + 'px'; // bottom-up coords
        // Simpler: use originalEvent client position if available.
        if (evt.originalEvent) {
            tip.style.left = (evt.originalEvent.clientX - rect.left + 12) + 'px';
            tip.style.top = (evt.originalEvent.clientY - rect.top + 12) + 'px';
        }
    };

    cy.on('mouseover', 'node', (evt) => {
        const node = evt.target;
        cy.elements().addClass('faded');
        node.removeClass('faded').addClass('highlight');
        node.connectedEdges().removeClass('faded').addClass('highlight');
        node.connectedEdges().connectedNodes().removeClass('faded').addClass('highlight');
        showTipAt(evt, nodeTooltipHtml(node));
    });
    cy.on('mouseover', 'edge', (evt) => {
        const edge = evt.target;
        cy.elements().addClass('faded');
        edge.removeClass('faded').addClass('highlight');
        edge.source().removeClass('faded').addClass('highlight');
        edge.target().removeClass('faded').addClass('highlight');
        showTipAt(evt, edgeTooltipHtml(edge));
    });
    cy.on('mouseout', 'node, edge', () => {
        cy.elements().removeClass('faded').removeClass('highlight');
        hideTip();
    });
    cy.on('tap', (evt) => {
        if (evt.target === cy) hideTip();
    });
}

function nodeTooltipHtml(node) {
    const name = node.data('name');
    const total = node.data('total') || 0;
    const byPlayer = node.data('byPlayer') || {};
    const top = Object.entries(byPlayer).sort((a, b) => b[1] - a[1]).slice(0, 5);
    const rows = top.map(([p, t]) => `<div>· ${escapeHtml(p)}: ${t.toFixed(1)}s</div>`).join('');
    return `<div><strong>${escapeHtml(name)}</strong></div>
<div>Total time: ${total.toFixed(1)}s</div>
${rows}`;
}

function edgeTooltipHtml(edge) {
    const from = edge.data('source').replace(/^n:/, '');
    const to = edge.data('target').replace(/^n:/, '');
    const kind = edge.data('kind');
    const total = edge.data('total') || 0;
    const byPlayer = edge.data('byPlayer') || {};
    const top = Object.entries(byPlayer).sort((a, b) => b[1] - a[1]).slice(0, 5);
    const rows = top.map(([p, c]) => `<div>· ${escapeHtml(p)}: ${c}</div>`).join('');
    return `<div><strong>${escapeHtml(from)} → ${escapeHtml(to)}</strong> (${kind})</div>
<div>Total transitions: ${total}</div>
${rows}`;
}

function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, c => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[c]));
}

// Kept as a thin compatibility shim: the tab-switch handler and old callers
// invoke renderLocGraph(); route them to the new refresh path.
function renderLocGraph() {
    if (locGraphState.cy) {
        locGraphState.cy.resize();
        locGraphState.cy.fit(undefined, 30);
    } else if (locGraphState.graph) {
        buildOrRefreshCytoscape();
    }
}

// ─── Playback Engine ──────────────────────────────────────────────────────

const PLAYBACK_BUTTON_LABELS = {
    'tl-rev': '-1x',
    'tl-slow': '0.2x',
    'tl-play-pause': '1x',
    'tl-5x': '5x'
};

function updatePlaybackButtons() {
    const buttons = {
        'tl-rev': -1,
        'tl-slow': 0.2,
        'tl-play-pause': 1,
        'tl-5x': 5
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

    // Throttle map redraws to PLAYBACK_FPS_MS (~30 fps).
    if (elapsed < PLAYBACK_FPS_MS / 1000) return;

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
        updateItemsPanelStatus(mapState.currentTime);
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
