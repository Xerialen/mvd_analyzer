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
//   powerupSpans: [{start, end, color, isTop, lane}] (optional, for weapons)
function renderDivergingGraph(canvasId, {
    startTime, endTime,
    dataPoints,
    maxValue,
    yAxisId,
    yTopLabel, yBottomLabel,
    powerupSpans,
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
    const midY = PAD + (graphH - PAD) / 2;
    const barH = midY - PAD;
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

        // Powerup overlay spans (thin colored strips at top/bottom of bar area)
        if (powerupSpans) {
            for (const sp of powerupSpans) {
                const x1 = Math.max(0, ((sp.start - startTime) / duration) * W);
                const x2 = Math.min(W, ((sp.end - startTime) / duration) * W);
                if (x2 <= x1) continue;
                const stripH = 3;
                const stripY = sp.isTop
                    ? PAD + sp.lane * (stripH + 1)
                    : graphH - PAD - (sp.lane + 1) * (stripH + 1);
                ctx.fillStyle = sp.color;
                ctx.fillRect(x1, stripY, x2 - x1, stripH);
            }
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
    if (!hrBuckets || !hrBuckets.length) return { points: [], max: 4, powerups: [] };
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
    return { points, max: maxVal, powerups: computePowerupSpans(startTime, endTime, teams) };
}

function computePowerupSpans(startTime, endTime, teams) {
    const hrBuckets = timelineState.highResBuckets;
    if (!hrBuckets || !hrBuckets.length) return [];
    const spans = [];
    const types = [
        { key: 'q', color: GRAPH_COLORS.QUAD, lane: 0 },
        { key: 'pe', color: GRAPH_COLORS.PENT, lane: 1 },
        { key: 'r', color: GRAPH_COLORS.RING, lane: 2 },
    ];
    for (let ti = 0; ti < 2; ti++) {
        const team = teams[ti];
        const isTop = ti === 0;
        for (const pu of types) {
            let spanStart = null;
            const idx0 = binarySearchBucketStart(hrBuckets, startTime);
            for (let i = idx0; i < hrBuckets.length; i++) {
                const b = hrBuckets[i];
                if (b.t > endTime) { break; }
                const has = ((b.td?.[team])?.[pu.key] || 0) > 0;
                if (has && spanStart === null) spanStart = b.t;
                else if (!has && spanStart !== null) {
                    spans.push({ start: spanStart, end: b.t, color: pu.color, isTop, lane: pu.lane });
                    spanStart = null;
                }
            }
            if (spanStart !== null) spans.push({ start: spanStart, end: endTime, color: pu.color, isTop, lane: pu.lane });
        }
    }
    return spans;
}

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

    // Update all detail panels (axes are drawn on canvas by the unified renderer)
    updateDetailGraph(start, end);
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
    const { points, max, powerups } = prepWeaponsData(startTime, endTime, teams);
    const legendA = document.getElementById('legend-weapons-team-a');
    const legendB = document.getElementById('legend-weapons-team-b');
    if (legendA) legendA.textContent = `${teams[0]} ↑`;
    if (legendB) legendB.textContent = `${teams[1]} ↓`;
    renderDivergingGraph('detail-graph-canvas', {
        startTime, endTime, dataPoints: points, maxValue: max,
        yAxisId: 'detail-y-axis', powerupSpans: powerups,
    });
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

const RC_ROW_H = 16;
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

function renderRegionControlTimeline(canvasId, labelsId, { startTime, endTime, rows }) {
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

    const stateColors = {
        teamAControl: teamStrongColor(TEAM_COLORS[0]),
        contested:    'rgb(255, 255, 255)',
        teamBControl: teamStrongColor(TEAM_COLORS[1]),
    };

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

    renderRegionControlTimeline('region-control-canvas', 'region-timeline-labels', {
        startTime, endTime, rows: data.rows,
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
    // prerenderLocationBackground as a neutral underlay.
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
// Shared by loc-group fills and the unnamed backdrop underlay.
function drawTriangleListFill(ctx, tris, fillStyle, worldToCanvasFunc) {
    if (!tris || tris.length < 6) return;
    ctx.fillStyle = fillStyle;
    for (let i = 0; i + 5 < tris.length; i += 6) {
        const a = worldToCanvasFunc(tris[i],     tris[i + 1]);
        const b = worldToCanvasFunc(tris[i + 2], tris[i + 3]);
        const c = worldToCanvasFunc(tris[i + 4], tris[i + 5]);
        ctx.beginPath();
        ctx.moveTo(a.x, a.y);
        ctx.lineTo(b.x, b.y);
        ctx.lineTo(c.x, c.y);
        ctx.closePath();
        ctx.fill();
    }
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
    ctx.font = 'bold 12px monospace';
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

// Draw control overlay for regions based on current control state
function drawRegionControlOverlay(ctx, controlStates) {


    for (const [regionName, state] of Object.entries(controlStates)) {
        const groups = mapState.regionToGroups[regionName];
        if (!groups || groups.length === 0) continue;

        let color;
        switch (state) {
            case 'teamAControl':
                color = hexToRgba(TEAM_COLORS[0], 0.24);
                break;
            case 'teamAWeakControl':
                color = hexToRgba(TEAM_COLORS[0], 0.14);
                break;
            case 'teamBControl':
                color = hexToRgba(TEAM_COLORS[1], 0.24);
                break;
            case 'teamBWeakControl':
                color = hexToRgba(TEAM_COLORS[1], 0.14);
                break;
            case 'contested':
                color = 'rgba(255, 255, 255, 0.14)';
                break;
            case 'weakContested':
                // Both teams present but neither has RL/LG — dimmer than
                // contested, mirroring the weak team-control variants.
                color = 'rgba(255, 255, 255, 0.07)';
                break;
            default: // empty
                continue;
        }

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
    locationCanvas: null, // Pre-rendered location background layer
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
    renderDirty: false        // Force redraw on track toggle/reset/etc
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
    mapState.locationCanvas = null;
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
                // neutral underlay by prerenderLocationBackground; cache its
                // triangle list separately so it isn't confused with loc
                // groups keyed by name.
                const backdrop = geom.locs.find(l => l && l.name === '');
                geom.backdropTris = backdrop && Array.isArray(backdrop.tris) ? backdrop.tris : null;
                mapState.mapGeometry = geom;
                // Rebuild groups with tris attached, then refresh region->group
                // references so the control overlay doesn't keep pointing at
                // the pre-fetch (tris-less) group objects.
                mapState.locationGroups = processLocationGroups(mapState.locations);
                mapState.locationCanvas = null;
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

        // Circle background — opaque so the map underneath doesn't bleed
        // through and fight the letter for legibility.
        octx.beginPath();
        octx.arc(cx, cy, r, 0, Math.PI * 2);
        octx.fillStyle = '#0a0a15';
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
    if (!mapState.canvas) return;
    const groups = mapState.locationGroups || [];
    const backdropTris = mapState.mapGeometry && mapState.mapGeometry.backdropTris;
    if (groups.length === 0 && (!backdropTris || backdropTris.length < 6)) return;

    const w = mapState.canvas.width;
    const h = mapState.canvas.height;
    const offscreen = document.createElement('canvas');
    offscreen.width = w;
    offscreen.height = h;
    const octx = offscreen.getContext('2d');

    // Draw the unnamed backdrop first (if present) so named loc regions
    // paint on top. Kept dim and neutral so real region tinting remains
    // readable when both are present.
    if (backdropTris && backdropTris.length >= 6) {
        drawTriangleListFill(octx, backdropTris, 'rgba(70, 80, 110, 0.35)', worldToCanvasNew);
    }

    for (const group of groups) {
        if (group.tris && group.tris.length >= 6) {
            drawLocationRegionFromGeometry(octx, group, worldToCanvasNew);
        }
    }

    // Thin grey outlines around each traced region — drawn after all fills so
    // they sit on top and stay visible regardless of adjacent region tinting.
    for (const group of groups) {
        if (group.tris && group.tris.length >= 6) {
            drawLocationRegionOutline(octx, group, worldToCanvasNew, 'rgba(180, 180, 180, 0.5)', 1);
        }
    }

    octx.font = '12px monospace';
    octx.textAlign = 'center';
    octx.textBaseline = 'middle';
    for (const group of groups) {
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
    // Sorted-by-time list of death frames in canvas space, used by renderMap
    // to draw a fading red "X" at the death location for a couple of seconds.
    mapState.deathEvents = [];
    const buckets = timelineState.highResBuckets;
    if (!buckets || buckets.length === 0) return;

    const MAX_MOVE_PER_BUCKET = 2500 * (timelineState.highResDuration || 0.05);
    const lastWorldPos = {};

    for (const bucket of buckets) {
        const playerData = bucket.p;
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

            // Death frames also get added to the standalone deathEvents list
            // so renderMap can find them without scanning every player trail.
            // teamIdx is captured so the X is painted in the dead player's
            // own team color rather than a generic red.
            if (isDeath) {
                mapState.deathEvents.push({ t, x: pos.x, y: pos.y, teamIdx: symbolInfo.teamIdx });
            }

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

    // Draw pre-rendered location background (or render it first time).
    // Also runs when we have only the unnamed backdrop from mapGeometry
    // (loc-less maps), so the floor plan still shows as a neutral underlay.
    const hasBackdrop = !!(mapState.mapGeometry && mapState.mapGeometry.backdropTris);
    if (mapState.locationGroups || hasBackdrop) {
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

    // Highlight regions that currently contain at least one player so the
    // viewer can tell which loc each symbol belongs to without squinting.
    const occupancyData = bucket ? (bucket.p) : null;
    if (occupancyData) {
        drawOccupiedRegionsOverlay(ctx, occupancyData);
    }

    // Draw tracks (per-player visibility controlled by enabledPlayers)
    drawTracks(ctx, time);

    const playerData = bucket ? bucket.p : null;
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
            drawDeathX(ctx, e.x, e.y, e.teamIdx, alpha);
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
