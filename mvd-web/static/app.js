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
let recomputeResolve = null;
let recomputeReject = null;

// ─── Load-timing instrumentation ────────────────────────────────────────────
// Structured per-stage timing for the demo load path, printed to the console
// (see mvd-web/README.md). `mvdTiming` holds one-time facts (wasm load);
// `loadTiming` is a per-demo accumulator built up across network → WASM →
// render and flushed by finishLoadTiming(). Also stashed on window.__mvdTimings.
let mvdTiming = { wasmLoadMs: null };
let loadTiming = null;
let pendingGameInfoMs = null; // game-info fetch happens before loadTiming starts

function startLoadTiming() {
    loadTiming = { t0: performance.now(), net: {}, render: [], worker: null, parseMs: 0 };
}
function markNet(name, ms) {
    if (loadTiming) loadTiming.net[name] = +ms.toFixed(1);
}
function timeRender(name, fn) {
    if (!loadTiming) return fn();
    const t = performance.now();
    try {
        return fn();
    } finally {
        loadTiming.render.push({ stage: name, ms: +(performance.now() - t).toFixed(2) });
    }
}
function finishLoadTiming() {
    if (!loadTiming) return;
    const lt = loadTiming;
    loadTiming = null; // close the window before any async render fires
    const total = performance.now() - lt.t0;
    const summary = {
        wasmLoadMs: mvdTiming.wasmLoadMs,
        network: lt.net,
        worker: lt.worker,
        parseMs: +lt.parseMs.toFixed(1),
        render: lt.render,
        downloadToUiMs: +total.toFixed(1),
    };
    window.__mvdTimings = summary;
    try {
        console.groupCollapsed(`[mvd-timing] load breakdown — ${total.toFixed(0)} ms download→UI`);
        if (mvdTiming.wasmLoadMs != null) {
            console.log(`wasm load (one-time): ${mvdTiming.wasmLoadMs.toFixed(1)} ms`);
        }
        console.log('network (ms):', lt.net);
        if (lt.worker) {
            const w = lt.worker;
            const locMs = (w.locFetch || []).reduce((s, f) => s + f.ms, 0);
            const bspMs = (w.bspFetch || []).reduce((s, f) => s + f.ms, 0);
            console.log(
                `WASM analyze: ${w.wasmAnalyzeMs.toFixed(1)} ms ` +
                `(incl. loc fetch ${locMs.toFixed(1)} ms, bsp fetch ${bspMs.toFixed(1)} ms — ` +
                `subtract from finalize:timelineAnalysis for pure loc compute)`
            );
            console.table((w.goPhases || []).map(p => ({ phase: p.name, ms: +p.ms.toFixed(2) })));
        }
        console.log(`result JSON.parse (main thread): ${lt.parseMs.toFixed(1)} ms`);
        console.table(lt.render);
        console.groupEnd();
    } catch (e) {
        console.warn('[mvd-timing] log failed:', e);
    }
}

// Hide the wasm-loading overlay. When auto-loading a demo from a URL
// (?gameId=...), keep it up through the demo download/analyse so the
// user never sees a half-populated Search/Summary tab in the
// background — displayResults() calls hideLoadingOverlay() once the
// pipeline has finished.
function hideLoadingOverlay() {
    const overlay = document.getElementById('wasm-loading');
    if (overlay) overlay.style.display = 'none';
}

function setLoadingOverlayMessage(text) {
    const overlay = document.getElementById('wasm-loading');
    if (!overlay) return;
    // The subtitle is the second flex child (the small status line).
    const subtitle = overlay.children[1];
    if (subtitle) subtitle.textContent = text;
}

worker.onmessage = (e) => {
    if (e.data.type === 'ready') {
        wasmReady = true;
        if (typeof e.data.wasmLoadMs === 'number') mvdTiming.wasmLoadMs = e.data.wasmLoadMs;
        const params = new URLSearchParams(location.search);
        const willAutoLoadDemo = !!(params.get('gameId') || params.get('hub'));
        if (willAutoLoadDemo) {
            // Demo download is about to start; keep the overlay up and
            // show the next phase.
            setLoadingOverlayMessage('Loading demo…');
        } else {
            hideLoadingOverlay();
        }
        const v = e.data.version;
        const tag = document.getElementById('version-tag');
        if (tag && v) {
            tag.textContent = `${v.tag} (`;
            const a = document.createElement('a');
            a.href = `https://github.com/galfthan/mvd_analyzer/commit/${encodeURIComponent(v.hash)}`;
            a.target = '_blank';
            a.rel = 'noopener';
            a.textContent = v.hash;
            tag.appendChild(a);
            tag.appendChild(document.createTextNode(`) — ${v.date}`));
        }
    } else if (e.data.type === 'result') {
        if (analyzeResolve) {
            analyzeResolve({
                json: e.data.json,
                timings: e.data.timings,
            });
            analyzeResolve = null;
            analyzeReject = null;
        }
    } else if (e.data.type === 'buckets') {
        // Deferred 50ms bucket + region-control payload — arrives a few
        // seconds after 'result'. Stash it and render the Timeline/Map
        // tabs that displayResults intentionally skipped.
        applyDeferredBuckets(e.data);
    } else if (e.data.type === 'error') {
        if (analyzeReject) {
            analyzeReject(new Error(e.data.message));
            analyzeResolve = null;
            analyzeReject = null;
        }
    } else if (e.data.type === 'recompute_result') {
        if (recomputeResolve) {
            recomputeResolve(e.data.json);
            recomputeResolve = null;
            recomputeReject = null;
        }
    } else if (e.data.type === 'recompute_error') {
        if (recomputeReject) {
            recomputeReject(new Error(e.data.message));
            recomputeResolve = null;
            recomputeReject = null;
        }
    }
};

// analyzeInWorker returns the parsed Result object. The 50ms column-major
// bucket view is built by the worker (calling getDefaultBuckets after
// analyzeMVD, since WASM exports live on the worker's global, not the main
// page) and arrives later via the 'buckets' message, stashed on
// result.timelineAnalysis.bucketView by applyDeferredBuckets.
function analyzeInWorker(bytes, filename) {
    return new Promise((resolve, reject) => {
        analyzeResolve = (payload) => {
            try {
                const tParse = performance.now();
                const result = JSON.parse(payload.json);
                if (loadTiming) {
                    loadTiming.parseMs = performance.now() - tParse;
                    loadTiming.worker = payload.timings || null;
                }
                // Buckets / region states arrive later via the 'buckets'
                // message and are stashed by applyDeferredBuckets — the
                // summary renders without waiting for them.
                resolve(result);
            } catch (e) {
                reject(e);
            }
        };
        analyzeReject = reject;
        // Transfer the ArrayBuffer (zero-copy)
        worker.postMessage(
            { type: 'analyze', bytes: bytes.buffer, filename },
            [bytes.buffer]
        );
    });
}

// applyDeferredBuckets stashes the worker's deferred 50ms bucket + region
// outputs onto the current result, then RE-RUNS the bucket-dependent
// initialisers (region control, timeline, map). They already ran once in
// displayResults with the bucket-derived fields empty (blank timeline
// graph / map trails / region overlay); re-running them now that the data
// is present fills those in. Finally re-render the active tab so a
// Timeline/Map view the user already opened repaints. Called when the
// 'buckets' message arrives, a few seconds after the result.
function applyDeferredBuckets(data) {
    if (!currentResult) return;
    const ta = currentResult.timelineAnalysis;
    if (ta && data.bucketsJSON) {
        try {
            const view = JSON.parse(data.bucketsJSON);
            // Column-major ColumnarBuckets object (not the old array shape).
            if (view && typeof view === 'object' && !view.error) ta.bucketView = view;
        } catch (e) {
            console.warn('parse deferred buckets failed:', e);
        }
    }
    if (ta && ta.regionControl && data.regionStatesJSON) {
        try {
            const rs = JSON.parse(data.regionStatesJSON);
            if (!rs.error && rs.bucketStates) {
                ta.regionControl.bucketStates = rs.bucketStates;
                if (rs.stats) ta.regionControl.stats = rs.stats;
            }
        } catch (e) {
            console.warn('parse deferred region states failed:', e);
        }
    }

    // Re-run the inits, now that bucket-derived data is present. Order
    // matches displayResults: region control feeds timeline + map.
    if (ta && ta.regionControl) initRegionControlData(currentResult);
    if (ta || currentResult.messages?.events) displayTimelineAnalysis(currentResult);
    if (ta) initMapView(currentResult);

    // Re-render whichever tab is active so Timeline/Map paint with the new
    // data (re-clicking re-runs the tab's render path; harmless on Summary).
    const active = document.querySelector('.sidebar-btn.active');
    if (active) active.click();

    if (typeof data.bucketsMs === 'number') {
        const deferred = (data.bucketsMs + (data.regionMs || 0)).toFixed(0);
        console.log(`[mvd-timing] Timeline/Map ready — ${deferred} ms of bucket work ran off the critical path`);
    }
}

// recomputeInWorker round-trips an edited regions definition through
// the WASM bridge living on the worker. The Go function is exported on
// the worker's self (js.Global()), unreachable from the main page —
// that's why edited region stats stayed stale before this lane existed.
function recomputeInWorker(overrideJSON) {
    return new Promise((resolve, reject) => {
        recomputeResolve = resolve;
        recomputeReject = reject;
        worker.postMessage({ type: 'recomputeRegions', overrideJSON });
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

const SEARCH_FIELDS = ['player', 'team', 'map', 'mode', 'tag', 'from', 'to'];
const SEARCH_SELECT = 'id,timestamp,mode,matchtag,map,teams,players,demo_sha256,demo_source_url';

async function searchHub(filters) {
    const parts = [
        `select=${encodeURIComponent(SEARCH_SELECT)}`,
        `order=timestamp.desc`,
        `limit=20`,
    ];
    if (filters.player) parts.push(`players_fts=fts.'${encodeURIComponent(filters.player)}'`);
    if (filters.team)   parts.push(`team_names=cs.{${encodeURIComponent(filters.team)}}`);
    if (filters.map)    parts.push(`map=eq.${encodeURIComponent(filters.map)}`);
    if (filters.mode)   parts.push(`mode=eq.${encodeURIComponent(filters.mode)}`);
    if (filters.tag)    parts.push(`matchtag=ilike.%25${encodeURIComponent(filters.tag)}%25`);
    if (filters.from)   parts.push(`timestamp=gte.${encodeURIComponent(filters.from)}`);
    if (filters.to)     parts.push(`timestamp=lte.${encodeURIComponent(filters.to + 'T23:59:59')}`);

    const resp = await fetch(`${SUPABASE_URL}?${parts.join('&')}`, {
        headers: {
            'apikey': SUPABASE_API_KEY,
            'Authorization': `Bearer ${SUPABASE_API_KEY}`,
            'accept-profile': 'public'
        }
    });
    if (!resp.ok) throw new Error(`Hub API error: ${resp.status}`);
    return await resp.json();
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
    setupSearch();

    const params = new URLSearchParams(location.search);
    const hubId = params.get('gameId') || params.get('hub');
    const requestedTab = params.get('tab');

    // Pick the initial active tab. When deep-linking to a demo we want
    // the destination tab to be active even before the demo finishes
    // loading, so the wasm-loading overlay covers the right pane and
    // the user never glimpses the Search panel mid-flight.
    if (hubId) {
        switchTab(requestedTab || 'summary');
    } else if (requestedTab) {
        switchTab(requestedTab);
    } // else: leave the HTML default (Search) active.

    // Auto-load demo if URL has ?gameId= (canonical) or ?hub= (legacy) param
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

    // Auto-populate search filters from the URL. Only auto-RUN the
    // query when there's no demo also being deep-loaded — when the
    // user shares e.g. ?gameId=…&player=nexus, the player filter is
    // incidental URL state from however they originally found the
    // demo, not a request to browse the search results.
    const urlFilters = {};
    let hasSearch = false;
    for (const f of SEARCH_FIELDS) {
        const v = params.get(f);
        if (v) { urlFilters[f] = v; hasSearch = true; }
    }
    if (hasSearch) {
        writeSearchFiltersToForm(urlFilters);
        if (!hubId) {
            if (!requestedTab) switchTab('search');
            runSearch();
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
let lastExecutedSearch = null;
function updateUrlState() {
    if (_urlStateTimer) return;
    _urlStateTimer = setTimeout(() => {
        _urlStateTimer = null;
        const params = new URLSearchParams();

        if (currentResult) {
            if (currentResult.hubInfo?.gameId) {
                params.set('gameId', currentResult.hubInfo.gameId);
            }

            const activeTab = document.querySelector('.sidebar-btn.active')?.dataset.tab;
            if (activeTab && activeTab !== 'summary') {
                params.set('tab', TAB_INTERNAL_TO_URL[activeTab] || activeTab);
            }

            if (mapState.learnMode) {
                params.set('learn', '1');
            }

            if (mapState.currentTime > 0) {
                params.set('t', Math.round(mapState.currentTime));
            }

            if (timelineState.segment) {
                params.set('seg', `${Math.round(timelineState.segment.start)}-${Math.round(timelineState.segment.end)}`);
            }
        }

        if (lastExecutedSearch) {
            for (const f of SEARCH_FIELDS) {
                const v = lastExecutedSearch[f];
                if (v) params.set(f, v);
            }
        }

        const qs = params.toString();
        if (qs) {
            history.replaceState(null, '', `?${qs}`);
        } else if (currentResult || lastExecutedSearch) {
            // We've actually loaded something but happen to have no
            // params worth keeping (e.g. fresh load on Summary at t=0
            // with no segment). Clear the URL.
            history.replaceState(null, '', location.pathname);
        }
        // else: nothing yet — leave the URL alone. There may still be
        // an in-flight ?gameId=… deep-link auto-load whose params we
        // would otherwise wipe before WASM finishes booting and
        // applyUrlState() can read them.
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
    if (tab) switchTab(tab); // resolves the locs-regions / loc-graph alias

    // Deep-link into the map's "learn" study view (only if this map has a
    // static entity corpus to show).
    if (params.get('learn') === '1' && mapState.mapEntities && mapState.mapEntities.length > 0) {
        setLearnMode(true);
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

// Tab URL aliases. The loc tab's internal data-tab stayed "loc-graph", but
// the tab is now labelled "Locs & Regions" and the URL prefers the matching
// "locs-regions" slug. Old "loc-graph" links still resolve; new URLs are
// written as "locs-regions" (see updateUrlState).
const TAB_URL_TO_INTERNAL = { 'locs-regions': 'loc-graph' };
const TAB_INTERNAL_TO_URL = { 'loc-graph': 'locs-regions' };
function resolveTabName(name) { return TAB_URL_TO_INTERNAL[name] || name; }

function switchTab(name) {
    const btn = document.querySelector(`.sidebar-btn[data-tab="${resolveTabName(name)}"]`);
    if (btn) btn.click();
}

function setupTabs() {
    const tabButtons = document.querySelectorAll('.sidebar-btn');
    tabButtons.forEach(btn => {
        btn.addEventListener('click', () => {
            const tabName = btn.dataset.tab;
            tabButtons.forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
            document.getElementById(`tab-${tabName}`).classList.add('active');

            // Show/hide the unified-timeline. It now lives outside the
            // scroll container as a sibling row of .main-scroll, so a
            // simple display toggle is all that's needed — no sticky,
            // no compositor games.
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
            // Canvases sized from container.clientWidth render empty
            // when first drawn while the tab was hidden (clientWidth
            // === 0 under display:none). Force a synchronous reflow
            // of the now-active tab content so the display:none →
            // block transition commits before we measure or draw.
            //
            // Even after that, Firefox specifically composites the
            // newly-revealed tab subtree through a layer snapshot for
            // a brief window — canvas writes during that window stay
            // invisible until the user does anything else. The two
            // backup re-renders (next frame + 120 ms) catch it
            // without us having to know exactly when the snapshot
            // releases. (Restructuring the layout to drop position:
            // sticky on the unified-timeline did not eliminate this
            // — the snapshot trigger appears to be the tab-content
            // display change itself, not the sticky element.)
            const tabContentEl = document.getElementById(`tab-${tabName}`);
            if (tabContentEl) void tabContentEl.offsetHeight;

            const renderForTab = () => {
                if (tabName === 'map') {
                    mapState.renderDirty = true;
                    mapState.lastRenderedBucket = null;
                    renderMap(mapState.currentTime);
                } else if (tabName === 'timeline') {
                    if (currentResult) updateDetailView();
                    updateTimeIndicators();
                } else if (tabName === 'chat') {
                    renderChatMessages();
                } else if (tabName === 'loc-graph') {
                    renderLocGraph();
                }
            };
            renderForTab();
            requestAnimationFrame(renderForTab);
            setTimeout(renderForTab, 120);

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

        startLoadTiming();
        const buffer = await file.arrayBuffer();
        const bytes = new Uint8Array(buffer);
        const result = await analyzeInWorker(bytes, file.name);
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
        const tInfo = performance.now();
        const game = await fetchGameFromHub(gameId);
        pendingGameInfoMs = performance.now() - tInfo;

        await loadGameFromHub(game);
    } catch (error) {
        status.textContent = 'Error: ' + error.message;
        status.className = 'status error';
        // If we were holding the loading overlay up for a deep-link auto-load,
        // drop it so the user sees the error message instead of a stuck spinner.
        hideLoadingOverlay();
    } finally {
        btn.disabled = false;
    }
}

// Given a game object already fetched from Supabase, download the demo,
// analyse it, and render the results. Shared by loadFromHub() (which
// fetches by ID first) and the search-result click handler (which
// already has the row in hand).
async function loadGameFromHub(game) {
    const status = document.getElementById('upload-status');
    if (!wasmReady) throw new Error('Analyzer is still loading, please wait...');

    document.getElementById('hub-input').value = game.id;

    startLoadTiming();
    if (pendingGameInfoMs != null) {
        markNet('gameInfoFetch', pendingGameInfoMs);
        pendingGameInfoMs = null;
    }
    status.textContent = 'Downloading demo...';
    status.className = 'status loading';
    const tDownload = performance.now();
    const demoBytes = await downloadDemoFromHub(game);
    markNet('demoDownload', performance.now() - tDownload);

    status.textContent = 'Analyzing...';
    const filename = generateDemoFilename(game);
    const result = await analyzeInWorker(demoBytes, filename);
    if (result.error) throw new Error(result.error);

    status.textContent = 'Analysis complete!';
    status.className = 'status success';
    currentResult = result;

    currentResult.hubInfo = {
        gameId: game.id,
        viewerUrl: `https://hub.quakeworld.nu/games/?gameId=${game.id}`,
        players: game.players
    };

    displayResults(result);
}

// ─── Demo Search Panel ──────────────────────────────────────────────────────

function readSearchFilters() {
    return {
        player: document.getElementById('search-player').value.trim(),
        team:   document.getElementById('search-team').value.trim(),
        map:    document.getElementById('search-map').value.trim(),
        mode:   document.getElementById('search-mode').value,
        tag:    document.getElementById('search-tag').value.trim(),
        from:   document.getElementById('search-from').value,
        to:     document.getElementById('search-to').value,
    };
}

function writeSearchFiltersToForm(filters) {
    if (filters.player !== undefined) document.getElementById('search-player').value = filters.player;
    if (filters.team   !== undefined) document.getElementById('search-team').value   = filters.team;
    if (filters.map    !== undefined) document.getElementById('search-map').value    = filters.map;
    if (filters.mode   !== undefined) document.getElementById('search-mode').value   = filters.mode;
    if (filters.tag    !== undefined) document.getElementById('search-tag').value    = filters.tag;
    if (filters.from   !== undefined) document.getElementById('search-from').value   = filters.from;
    if (filters.to     !== undefined) document.getElementById('search-to').value     = filters.to;
}

function setupSearch() {
    document.getElementById('search-form').addEventListener('submit', (e) => {
        e.preventDefault();
        runSearch();
    });
}

async function runSearch() {
    const filters = readSearchFilters();
    const status = document.getElementById('search-status');
    const submit = document.getElementById('search-submit');
    const resultsEl = document.getElementById('search-results');

    status.textContent = 'Searching…';
    status.className = 'status loading';
    submit.disabled = true;

    try {
        const games = await searchHub(filters);
        status.textContent = '';
        status.className = 'status';
        renderSearchResults(games);
        lastExecutedSearch = filters;
        updateUrlState();
    } catch (err) {
        status.textContent = 'Error: ' + err.message;
        status.className = 'status error';
        resultsEl.innerHTML = '';
    } finally {
        submit.disabled = false;
    }
}

const SEARCH_MODE_LABELS = {
    '1on1': '1v1',
    '2on2': '2v2',
    '4on4': '4v4',
    'ffa':  'FFA',
    'ctf':  'CTF',
};

function escapeHtml(s) {
    if (s == null) return '';
    return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}

function updateTopbarDemoInfo(result) {
    const el = document.getElementById('topbar-demo-info');
    if (!el) return;

    const demoInfo = result?.demoInfo;
    const map = demoInfo?.map || result?.match?.map || '';
    const date = demoInfo?.date || '';

    let teams = (timelineState.teams && timelineState.teams.length >= 2)
        ? [...timelineState.teams]
        : [];
    if (teams.length < 2 && demoInfo?.teams) teams = [...demoInfo.teams];
    if (teams.length < 2 && result?.match?.teams) teams = result.match.teams.map(t => t.name);

    const teamScores = {};
    if (demoInfo?.players) {
        for (const p of demoInfo.players) {
            const t = p.team || '';
            teamScores[t] = (teamScores[t] || 0) + (p.stats?.frags || 0);
        }
    } else if (result?.match?.teams) {
        for (const t of result.match.teams) teamScores[t.name] = t.frags || 0;
    }

    const parts = [];
    if (teams.length >= 2) {
        const a = teams[0];
        const b = teams[1];
        parts.push(
            `<span class="topbar-team-a">${escapeHtml(a)}</span>` +
            ` <span class="topbar-vs">vs</span> ` +
            `<span class="topbar-team-b">${escapeHtml(b)}</span>`
        );
        if (a in teamScores || b in teamScores) {
            parts.push(
                `<span class="topbar-score">` +
                `${teamScores[a] ?? 0} - ${teamScores[b] ?? 0}` +
                `</span>`
            );
        }
    }
    if (map) parts.push(`<span class="topbar-map">${escapeHtml(map)}</span>`);
    if (date) parts.push(`<span class="topbar-date">${escapeHtml(date)}</span>`);

    el.innerHTML = parts.join('<span class="topbar-sep">·</span>');

    const titleParts = [];
    if (teams.length >= 2) titleParts.push(`${teams[0]} ${teams[1]}`);
    if (map) titleParts.push(map);
    document.title = titleParts.length
        ? `MVD | ${titleParts.join(' ')}`
        : 'MVD Analyzer';
}

function renderSearchResults(games) {
    const el = document.getElementById('search-results');
    el.innerHTML = '';
    if (!games || games.length === 0) {
        el.innerHTML = '<div class="search-empty">No demos match these filters.</div>';
        return;
    }
    for (const game of games) {
        const row = document.createElement('button');
        row.type = 'button';
        row.className = 'search-result';
        row.dataset.gameId = game.id;

        const modeText = SEARCH_MODE_LABELS[game.mode] || game.mode || '-';
        const mapText  = game.map || '-';
        const dateText = (game.timestamp || '').slice(0, 10);

        // Pick contestants: top-2 teams for team modes, top-2 players otherwise.
        const isTeamMode = game.mode && game.mode !== '1on1' && game.mode !== 'ffa';
        let contestants = '—';
        if (isTeamMode && Array.isArray(game.teams) && game.teams.length >= 2) {
            const t = [...game.teams].sort((a, b) => (b.frags || 0) - (a.frags || 0));
            contestants = `${t[0].name} ${t[0].frags} : ${t[1].frags} ${t[1].name}`;
        } else if (Array.isArray(game.players) && game.players.length >= 2) {
            const p = [...game.players].sort((a, b) => (b.frags || 0) - (a.frags || 0));
            contestants = `${p[0].name} ${p[0].frags} : ${p[1].frags} ${p[1].name}`;
        } else if (Array.isArray(game.players) && game.players.length === 1) {
            const p = game.players[0];
            contestants = `${p.name} ${p.frags || 0}`;
        }

        const tagHtml = game.matchtag
            ? `<span class="search-result-tag">${escapeHtml(game.matchtag)}</span>`
            : '<span class="search-result-tag"></span>';

        row.innerHTML =
            `<span class="search-result-mode">${escapeHtml(modeText)}</span>` +
            `<span class="search-result-map">${escapeHtml(mapText)}</span>` +
            `<span class="search-result-contest">${escapeHtml(contestants)}</span>` +
            tagHtml +
            `<span class="search-result-date">${escapeHtml(dateText)}</span>`;

        row.addEventListener('click', () => loadGameFromSearch(game));
        el.appendChild(row);
    }
}

async function loadGameFromSearch(game) {
    try {
        await loadGameFromHub(game);
        // displayResults() switches to the Summary tab as part of finishing a load.
    } catch (error) {
        const status = document.getElementById('upload-status');
        status.textContent = 'Error: ' + error.message;
        status.className = 'status error';
    }
}

function displayResults(result) {
    // Wipe every panel, table, canvas, and JS-side state object that
    // could carry data from a previous demo. Critical when swapping
    // between demos with different shapes (e.g. 4on4 → FFA where
    // teams/regions/etc. are absent and would otherwise survive).
    resetUIToCleanState();

    document.body.classList.remove('no-demo');

    // Land the user on the Summary tab after a fresh load, unless the URL
    // explicitly asks for a different tab (deep links like ?tab=map should
    // win — applyUrlState() is called later and will switch tabs again).
    if (!new URLSearchParams(location.search).get('tab')) {
        switchTab('summary');
    }

    const demoInfo = result.demoInfo;

    // Match info from demoInfo. demoInfo.duration is verbatim from the
    // KTX JSON (integer seconds, untransformed by design); result.match
    // .duration flipped to int32 ms in schema v8 — convert to seconds for
    // formatDuration.
    if (demoInfo) {
        document.getElementById('map-name').textContent = demoInfo.map || result.match?.map || '-';
        document.getElementById('duration').textContent = formatDuration(demoInfo.duration || ((result.match?.duration || 0) * 0.001));
        document.getElementById('mode').textContent = demoInfo.mode || '-';
        document.getElementById('hostname').textContent = demoInfo.hostname || '-';
        document.getElementById('match-date').textContent = demoInfo.date || '-';
    } else if (result.match) {
        document.getElementById('map-name').textContent = result.match.map || '-';
        document.getElementById('duration').textContent = formatDuration((result.match.duration || 0) * 0.001);
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

    updateTopbarDemoInfo(result);

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

    // Region control data (needed by both timeline and map). Cheap on the
    // main thread; the multi-second cost was the worker's bucket build,
    // which now runs after the result is delivered. These run here with the
    // bucket-derived fields still empty (timeline graph / map trails /
    // region overlay blank) and applyDeferredBuckets() re-runs them once the
    // 'buckets' message lands. displayTimelineAnalysis also populates the
    // Chat tab's events and the timeline strip, so it must run now.
    if (result.timelineAnalysis?.regionControl) {
        initRegionControlData(result);
    }

    // Timeline Analysis (new graphical view)
    if (result.timelineAnalysis || result.messages?.events) {
        timeRender('displayTimelineAnalysis', () => displayTimelineAnalysis(result));
    }

    // Key Moments (powerup runs + frag streaks). Call unconditionally so
    // the function gets a chance to clear stale DOM from a previous demo;
    // displayKeyMoments handles empty powerupEvents / fragStreaks on its own.
    if (result.timelineAnalysis) {
        timeRender('displayKeyMoments', () => displayKeyMoments(result));
    }

    // Pack Drops — always call so stale rows are cleared between demos.
    timeRender('displayPackDrops', () => displayPackDrops(result));

    // Pickups — per-entity item pickups + KTX-tooks verification.
    timeRender('displayPickupsTab', () => displayPickupsTab(result));

    // Map View
    if (result.timelineAnalysis) {
        timeRender('initMapView', () => initMapView(result));
    }

    // Loc Graph
    timeRender('initLocGraphView', () => initLocGraphView(result));

    // Make all static tables sortable
    document.querySelectorAll('.stats-table').forEach(makeSortable);

    // Apply URL state (tab, time, segment) if present
    applyUrlState();

    // Reveal the now-populated UI (overlay may already be hidden if the
    // demo was loaded interactively rather than via ?gameId=).
    hideLoadingOverlay();

    // Flush the consolidated load-timing breakdown to the console.
    finishLoadTiming();
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
        // Bind once per element. Dynamically rebuilt tables (e.g. the loc /
        // region heatmaps) call makeSortable again after replacing their <th>
        // nodes; fresh nodes lack the flag and bind, while the load-time
        // querySelectorAll('.stats-table') pass won't double-bind existing ones
        // (a second listener would cancel every toggle).
        if (th._sortBound) return;
        th._sortBound = true;

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
                const aCell = a.cells[colIdx];
                const bCell = b.cells[colIdx];
                // Cells can opt-in to a custom sort key via data-sort-value
                // (e.g. the Stack column shows "70 120 RA" but sorts on H+A).
                const aText = (aCell?.dataset.sortValue ?? aCell?.textContent.trim() ?? '');
                const bText = (bCell?.dataset.sortValue ?? bCell?.textContent.trim() ?? '');
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

// Per-player suicides counted from our frag log (every self / world-dealt
// death prints a suicide obituary), keyed by victim. KTX demoinfo
// stats.suicides books world-dealt deaths (falls, trigger_hurt) on the `world`
// entity instead of the victim, so it undercounts; the frag log is the
// complete record. See MVD_FORMAT.md "World-dealt deaths". Returns null when no
// frag log is present so callers can fall back to demoinfo.
function suicidesFromFragLog() {
    const log = currentResult?.frags?.frags;
    if (!log) return null;
    const out = {};
    for (const f of log) if (f.isSuicide) out[f.victim] = (out[f.victim] || 0) + 1;
    return out;
}

function displayPlayerStats(players) {
    const sorted = [...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0));
    const teamOrder = getTeamOrder(sorted);
    // Prefer our analysis counts over KTX demoinfo: frags.byPlayer fixes
    // stats.kills + the per-weapon kills (both over-count pentagram-deflect
    // telefrags / dtTELE2 and reset after a reconnect), and the frag log fixes
    // stats.suicides (drops world-dealt deaths). Deaths agree with KTX but we
    // read ours so kills/deaths/efficiency share one source, and byWeapon is
    // enemy kills only so RL+LG+… reconciles with the total. Fall back to
    // demoinfo per name when a player doesn't join. See MVD_FORMAT.md.
    const byPlayer = currentResult?.frags?.byPlayer || {};
    const suicidesMap = suicidesFromFragLog();

    // Show the handicap column only when at least one player on this demo
    // has a non-default handicap. KTX omits the JSON field entirely when the
    // value is 100 (the default), so any non-zero value here means it was
    // actually set.
    const showHandicap = sorted.some(p => (p.handicap || 0) > 0 && p.handicap !== 100);
    document.querySelectorAll('#scoreboard .handicap-col').forEach(el => {
        el.style.display = showHandicap ? '' : 'none';
    });

    renderTableRows('scoreboard-body', sorted, player => {
        const bp = byPlayer[player.name];
        const kills = bp ? bp.kills : (player.stats?.kills || 0);
        const deaths = bp ? bp.deaths : (player.stats?.deaths || 0);
        const suicides = suicidesMap ? (suicidesMap[player.name] || 0) : (player.stats?.suicides || 0);
        const rlKills = bp ? (bp.byWeapon?.rl || 0) : (player.weapons?.rl?.kills?.enemy || 0);
        const lgKills = bp ? (bp.byWeapon?.lg || 0) : (player.weapons?.lg?.kills?.enemy || 0);
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
            <td>${suicides}</td>
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
    // Same accurate-count sourcing as displayPlayerStats so the team totals
    // reconcile with the per-player rows. See MVD_FORMAT.md.
    const byPlayer = currentResult?.frags?.byPlayer || {};
    const suicidesMap = suicidesFromFragLog();
    const killsOf = p => (byPlayer[p.name] ? byPlayer[p.name].kills : (p.stats?.kills || 0));
    const deathsOf = p => (byPlayer[p.name] ? byPlayer[p.name].deaths : (p.stats?.deaths || 0));
    const suicidesOf = p => (suicidesMap ? (suicidesMap[p.name] || 0) : (p.stats?.suicides || 0));
    const wKillsOf = (p, w) => (byPlayer[p.name] ? (byPlayer[p.name].byWeapon?.[w] || 0) : (p.weapons?.[w]?.kills?.enemy || 0));

    renderTableRows('player-stats-team-body', teamOrder, team => {
        const members = groups[team] || [];
        const frags = members.reduce((s, p) => s + (p.stats?.frags || 0), 0);
        const kills = members.reduce((s, p) => s + killsOf(p), 0);
        const rlKills = members.reduce((s, p) => s + wKillsOf(p, 'rl'), 0);
        const lgKills = members.reduce((s, p) => s + wKillsOf(p, 'lg'), 0);
        const deaths = members.reduce((s, p) => s + deathsOf(p), 0);
        const tk = members.reduce((s, p) => s + (p.stats?.tk || 0), 0);
        const suicides = members.reduce((s, p) => s + suicidesOf(p), 0);
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

    // Schema v8: powerupEvents/fragStreaks time/endTime/duration fields
    // are int32 ms on the raw result. Convert to seconds at intake so
    // the rest of this function (formatDuration, setCurrentTime, hub
    // URL `from`/`to` which expect seconds) is unchanged.
    const powerupEvents = (result.timelineAnalysis?.powerupEvents || []).map(ev => ({
        ...ev,
        time: ev.time * 0.001,
        endTime: (ev.endTime || 0) * 0.001,
        duration: (ev.duration || 0) * 0.001,
    }));

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

    // Same v8 ms→s conversion for fragStreaks (time/endTime/duration are ms).
    const fragStreaks = (result.timelineAnalysis?.fragStreaks || []).map(s => ({
        ...s,
        time: s.time * 0.001,
        endTime: (s.endTime || 0) * 0.001,
        duration: (s.duration || 0) * 0.001,
    }));

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

// ─── Pickups tab ──────────────────────────────────────────────────────
//
// Two tables — Weapon Pickups (RL/LG/GL/SNG with per-entity columns,
// pack column for RL/LG, and a Σ that aggregates the kind) and Item
// Pickups (armors, healths, powerups). Both react to a mode selector:
//
//   "All pickups"          → Σ vs KTX total-taken; pack vs ttooks-sttooks
//   "First pickup per life" → Σ vs KTX taken;       pack vs tooks-stooks
//
// Per-entity columns always show every-touch from items.phases. The
// verify cell renders silently on KTX agreement and red+✗ on diff.
//
// Note: GL/SNG packs aren't tracked on the analyser side (KTX only emits
// `//ktx bp` for RL/LG; ktx/src/items.c:2471), so those weapons compare
// against KTX's entity-only fields (spawn-taken / spawn-total-taken).

const PICKUPS_WEAPON_KINDS = [
    { kind: 'rl',  label: 'RL',  ktxName: 'rl',  hasPack: true  },
    { kind: 'lg',  label: 'LG',  ktxName: 'lg',  hasPack: true  },
    { kind: 'gl',  label: 'GL',  ktxName: 'gl',  hasPack: false },
    { kind: 'sng', label: 'SNG', ktxName: 'sng', hasPack: false },
];

const PICKUPS_ITEM_KINDS = [
    { kind: 'ra',   label: 'RA',   ktxName: 'ra' },
    { kind: 'ya',   label: 'YA',   ktxName: 'ya' },
    { kind: 'ga',   label: 'GA',   ktxName: 'ga' },
    { kind: 'mh',   label: 'MH',   ktxName: 'health_100' },
    { kind: 'quad', label: 'Quad', ktxName: 'q' },
    { kind: 'pent', label: 'Pent', ktxName: 'p' },
    { kind: 'ring', label: 'Ring', ktxName: 'r' },
];

let pickupsMode = 'all'; // 'all' | 'first'

function pickupsWeaponDetail(player, ktxName) {
    const pk = player.weapons?.[ktxName]?.pickups || {};
    return {
        'first pickup, any source': pk['taken'] || 0,
        'every touch, any source':  pk['total-taken'] || 0,
        'first pickup of items':    pk['spawn-taken'] || 0,
        'every item touch':         pk['spawn-total-taken'] || 0,
    };
}
function pickupsSumDetail(into, add) {
    if (!add) return into;
    if (!into) into = {};
    for (const [k, v] of Object.entries(add)) into[k] = (into[k] || 0) + v;
    return into;
}

function displayPickupsTab(result) {
    const sel = document.getElementById('pickups-mode');
    if (sel && !sel.dataset.bound) {
        sel.value = pickupsMode;
        sel.addEventListener('change', () => {
            pickupsMode = sel.value;
            if (currentResult) renderPickupsTables(currentResult);
        });
        sel.dataset.bound = '1';
    }
    renderPickupsTables(result);
}

function renderPickupsTables(result) {
    const state = computePickupsState(result);
    const empty = document.getElementById('pickups-empty');
    const itemsPanel = document.getElementById('pickups-items-panel');

    const hasWeapon = PICKUPS_WEAPON_KINDS.some(k => (state.weaponEntsByKind.get(k.kind) || []).length > 0);
    const hasItem = PICKUPS_ITEM_KINDS.some(k => (state.itemEntsByKind.get(k.kind) || []).length > 0);

    if (!hasWeapon && !hasItem) {
        empty.style.display = '';
        for (const id of ['pickups-weap-team-table', 'pickups-weap-player-table',
                          'pickups-item-team-table', 'pickups-item-player-table']) {
            const t = document.getElementById(id);
            if (t) t.style.display = 'none';
        }
        return;
    }
    empty.style.display = 'none';

    renderPickupsSection(state, 'weap', PICKUPS_WEAPON_KINDS, buildWeaponCols, weaponCellFor);
    if (itemsPanel) itemsPanel.style.display = hasItem ? '' : 'none';
    renderPickupsSection(state, 'item', PICKUPS_ITEM_KINDS, buildItemCols, itemCellFor);
}

function computePickupsState(result) {
    const players = result.demoInfo?.players || [];
    const teamOrder = getTeamOrder([...players].sort((a, b) => (b.stats?.frags || 0) - (a.stats?.frags || 0)));
    const playerByName = new Map(players.map(p => [p.name, p]));

    const items = result.items?.items || [];
    const weaponEntsByKind = new Map();
    const itemEntsByKind = new Map();
    for (const it of items) {
        if (PICKUPS_WEAPON_KINDS.some(k => k.kind === it.kind)) {
            if (!weaponEntsByKind.has(it.kind)) weaponEntsByKind.set(it.kind, []);
            weaponEntsByKind.get(it.kind).push(it);
        } else if (PICKUPS_ITEM_KINDS.some(k => k.kind === it.kind)) {
            if (!itemEntsByKind.has(it.kind)) itemEntsByKind.set(it.kind, []);
            itemEntsByKind.get(it.kind).push(it);
        }
    }
    const sortByLoc = (a, b) => {
        const la = a.loc || ''; const lb = b.loc || '';
        if (la !== lb) return la.localeCompare(lb);
        return a.entNum - b.entNum;
    };
    for (const list of weaponEntsByKind.values()) list.sort(sortByLoc);
    for (const list of itemEntsByKind.values()) list.sort(sortByLoc);

    const entityCountsByPlayer = new Map(); // name -> Map(entNum -> count)
    const entityCountsByTeam = new Map();   // team -> Map(entNum -> count)
    for (const it of items) {
        for (const ph of it.phases || []) {
            const who = ph.takenBy;
            if (!who) continue;
            if (!entityCountsByPlayer.has(who)) entityCountsByPlayer.set(who, new Map());
            const pm = entityCountsByPlayer.get(who);
            pm.set(it.entNum, (pm.get(it.entNum) || 0) + 1);
            const team = ph.team || playerByName.get(who)?.team || '';
            if (!entityCountsByTeam.has(team)) entityCountsByTeam.set(team, new Map());
            const tm = entityCountsByTeam.get(team);
            tm.set(it.entNum, (tm.get(it.entNum) || 0) + 1);
        }
    }

    return {
        result, players, teamOrder, playerByName,
        weaponEntsByKind, itemEntsByKind,
        entityCountsByPlayer, entityCountsByTeam,
        weaponPickups: result.weaponPickups || [],
    };
}

function buildWeaponCols(state) {
    const cols = [];
    for (const spec of PICKUPS_WEAPON_KINDS) {
        const list = state.weaponEntsByKind.get(spec.kind) || [];
        if (list.length === 0) continue;
        if (list.length === 1 && !spec.hasPack) {
            // Single combined cell: kind label, mode-aware verify against KTX.
            cols.push({ type: 'weap-verify', kindSpec: spec, header: spec.label });
            continue;
        }
        if (list.length === 1) {
            cols.push({
                type: 'entity-count',
                kindSpec: spec,
                entNum: list[0].entNum,
                header: spec.label,
            });
        } else {
            for (const it of list) {
                const tag = (it.loc && it.loc.length > 0) ? it.loc : it.name;
                cols.push({
                    type: 'entity-count',
                    kindSpec: spec,
                    entNum: it.entNum,
                    header: `${spec.label} <span class="pickups-loc">@ ${escapeHtml(tag)}</span>`,
                });
            }
        }
        if (spec.hasPack) {
            cols.push({ type: 'weap-pack', kindSpec: spec, header: `${spec.label} pack` });
        }
        cols.push({
            type: 'weap-verify',
            kindSpec: spec,
            header: `${spec.label} <span class="pickups-verify-header">Σ</span>`,
        });
    }
    return cols;
}

function buildItemCols(state) {
    const cols = [];
    for (const spec of PICKUPS_ITEM_KINDS) {
        const list = state.itemEntsByKind.get(spec.kind) || [];
        if (list.length === 0) continue;
        if (list.length === 1) {
            cols.push({ type: 'item-verify', kindSpec: spec, entNums: [list[0].entNum], header: spec.label });
            continue;
        }
        for (const it of list) {
            const tag = (it.loc && it.loc.length > 0) ? it.loc : it.name;
            cols.push({
                type: 'entity-count',
                kindSpec: spec,
                entNum: it.entNum,
                header: `${spec.label} <span class="pickups-loc">@ ${escapeHtml(tag)}</span>`,
            });
        }
        cols.push({
            type: 'item-verify',
            kindSpec: spec,
            entNums: list.map(it => it.entNum),
            header: `${spec.label} <span class="pickups-verify-header">Σ</span>`,
        });
    }
    return cols;
}

function renderPickupsSection(state, idPrefix, kinds, buildCols, cellFor) {
    const teamTable = document.getElementById(`pickups-${idPrefix}-team-table`);
    const playerTable = document.getElementById(`pickups-${idPrefix}-player-table`);
    if (!teamTable || !playerTable) return;
    const teamHead = teamTable.querySelector('thead');
    const teamBody = teamTable.querySelector('tbody');
    const playerHead = playerTable.querySelector('thead');
    const playerBody = playerTable.querySelector('tbody');
    teamHead.innerHTML = ''; teamBody.innerHTML = '';
    playerHead.innerHTML = ''; playerBody.innerHTML = '';

    const cols = buildCols(state);
    if (cols.length === 0) {
        teamTable.style.display = 'none';
        playerTable.style.display = 'none';
        return;
    }
    teamTable.style.display = '';
    playerTable.style.display = '';

    const buildHeader = (label) => {
        const tr = document.createElement('tr');
        tr.appendChild(makeTh(label));
        for (const c of cols) tr.appendChild(makeTh(c.header));
        return tr;
    };
    teamHead.appendChild(buildHeader('Team'));
    playerHead.appendChild(buildHeader('Player'));

    const teamsSorted = [...state.teamOrder.filter(t => t !== '')];
    if (teamsSorted.length === 0 && state.entityCountsByTeam.size > 0) {
        teamsSorted.push(...[...state.entityCountsByTeam.keys()].filter(t => t !== ''));
    }
    for (const team of teamsSorted) {
        const tr = document.createElement('tr');
        const teamIdx = state.teamOrder.indexOf(team);
        if (teamIdx >= 0 && teamIdx < TEAM_COLORS.length) {
            tr.style.setProperty('--row-team-color', TEAM_COLORS[teamIdx]);
        }
        tr.appendChild(makeTd(escapeHtml(team || '(no team)')));
        const teamMap = state.entityCountsByTeam.get(team) || new Map();
        const teamPlayers = state.players.filter(p => p.team === team);
        for (const c of cols) {
            tr.appendChild(cellFor(c, true, team, teamMap, teamPlayers, state));
        }
        teamBody.appendChild(tr);
    }

    const playersSorted = [...state.players].sort((a, b) => {
        const ta = state.teamOrder.indexOf(a.team || '');
        const tb = state.teamOrder.indexOf(b.team || '');
        if (ta !== tb) return ta - tb;
        return (b.stats?.frags || 0) - (a.stats?.frags || 0);
    });
    for (const p of playersSorted) {
        const tr = document.createElement('tr');
        const teamIdx = state.teamOrder.indexOf(p.team || '');
        if (teamIdx >= 0 && teamIdx < TEAM_COLORS.length) {
            tr.style.setProperty('--row-team-color', TEAM_COLORS[teamIdx]);
        }
        tr.appendChild(makeTd(`${escapeHtml(p.name)} <span class="pickups-team-tag">${escapeHtml(p.team || '')}</span>`));
        const pmap = state.entityCountsByPlayer.get(p.name) || new Map();
        for (const c of cols) {
            tr.appendChild(cellFor(c, false, p.name, pmap, [p], state));
        }
        playerBody.appendChild(tr);
    }
}

function weaponCellFor(col, isTeam, key, entMap, scopedPlayers, state) {
    const spec = col.kindSpec;
    if (col.type === 'entity-count') {
        const n = entMap.get(col.entNum) || 0;
        return makeTd(n === 0 ? '<span class="muted">0</span>' : String(n));
    }
    const matches = isTeam
        ? (w => (w.team || '') === key)
        : (w => w.player === key);
    if (col.type === 'weap-pack') {
        const ana = state.weaponPickups.filter(w =>
            w.weapon === spec.kind && matches(w) && w.source === 'backpack'
            && (pickupsMode !== 'first' || !w.hadBefore)).length;
        let primary = 0;
        for (const p of scopedPlayers) {
            const pk = p.weapons?.[spec.ktxName]?.pickups || {};
            primary += pickupsMode === 'first'
                ? ((pk['taken'] || 0) - (pk['spawn-taken'] || 0))
                : ((pk['total-taken'] || 0) - (pk['spawn-total-taken'] || 0));
        }
        return makeVerifyCell(ana, primary, null);
    }
    // weap-verify (Σ for hasPack, or single combined cell otherwise).
    let ana;
    if (pickupsMode === 'first') {
        // first-per-life from weaponPickups (any source for hasPack; world-only entries otherwise).
        ana = state.weaponPickups.filter(w =>
            w.weapon === spec.kind && matches(w) && !w.hadBefore).length;
    } else {
        ana = 0;
        const list = state.weaponEntsByKind.get(spec.kind) || [];
        for (const it of list) ana += (entMap.get(it.entNum) || 0);
        if (spec.hasPack) {
            ana += state.weaponPickups.filter(w =>
                w.weapon === spec.kind && matches(w) && w.source === 'backpack').length;
        }
    }
    let primary = 0; let detail = null;
    for (const p of scopedPlayers) {
        const pk = p.weapons?.[spec.ktxName]?.pickups || {};
        if (spec.hasPack) {
            primary += pickupsMode === 'first' ? (pk['taken'] || 0) : (pk['total-taken'] || 0);
        } else {
            primary += pickupsMode === 'first' ? (pk['spawn-taken'] || 0) : (pk['spawn-total-taken'] || 0);
        }
        detail = pickupsSumDetail(detail, pickupsWeaponDetail(p, spec.ktxName));
    }
    return makeVerifyCell(ana, primary, detail);
}

function itemCellFor(col, isTeam, key, entMap, scopedPlayers, state) {
    const spec = col.kindSpec;
    if (col.type === 'entity-count') {
        const n = entMap.get(col.entNum) || 0;
        return makeTd(n === 0 ? '<span class="muted">0</span>' : String(n));
    }
    // item-verify: single counter on the KTX side (`took`) — mode has no
    // effect here (KTX doesn't expose a first-per-life counter for items).
    let ana = 0;
    for (const ent of col.entNums) ana += (entMap.get(ent) || 0);
    let primary = 0;
    for (const p of scopedPlayers) primary += (p.items?.[spec.ktxName]?.took || 0);
    return makeVerifyCell(ana, primary, null);
}

function makeTh(html) {
    const th = document.createElement('th');
    th.innerHTML = html;
    return th;
}

function makeTd(html) {
    const td = document.createElement('td');
    td.innerHTML = html;
    return td;
}

// makeVerifyCell renders the analyser count silently when it matches
// KTX (cell looks like a regular count) and red+✗ when it disagrees.
// Tooltip is always present so the per-counter breakdown is one hover
// away even on matched cells.
function makeVerifyCell(ana, ktxPrimary, detail) {
    const td = document.createElement('td');
    if (ana === 0 && ktxPrimary === 0 && (!detail || Object.values(detail).every(v => v === 0))) {
        td.innerHTML = '<span class="muted">·</span>';
        return td;
    }
    if (ana === ktxPrimary) {
        td.textContent = String(ana);
    } else {
        td.className = 'pickups-verify-bad';
        const diff = ana - ktxPrimary;
        const sign = diff > 0 ? `+${diff}` : `${diff}`;
        td.innerHTML = `${ana}<span class="pickups-verify-mark"> ✗ ktx ${ktxPrimary} (${sign})</span>`;
    }
    if (detail) {
        const lines = [`analyzer: ${ana}`];
        for (const [field, val] of Object.entries(detail)) {
            lines.push(`ktx ${field}: ${val}`);
        }
        td.title = lines.join('\n');
    } else {
        td.title = `analyzer: ${ana}\nktx: ${ktxPrimary}`;
    }
    return td;
}

function displayPackDrops(result) {
    const tbody = document.getElementById('packdrops-body');
    const emptyMsg = document.getElementById('packdrops-empty');
    if (!tbody) return;
    tbody.innerHTML = '';

    // Schema v8: backpacks[].time, weaponPickups[].time / .nextDeathTime /
    // .dropTime are int32 ms. The pickup↔drop join must happen in the
    // same time space — index against raw ms `dropTime` (an exact int)
    // before converting. After the join, convert ms→s so hubAnchor /
    // formatDuration further down (which expect seconds) is unchanged.
    const rawDrops = result.backpacks || [];
    if (rawDrops.length === 0) {
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

    const rows = rawDrops.map(rawDrop => {
        const rawPickup = pickupByKey[`${rawDrop.entNum}@${rawDrop.time}`] || null;
        // Convert ms→s on the per-row copy so downstream renderers see seconds.
        const drop = { ...rawDrop, time: rawDrop.time * 0.001 };
        const pickup = rawPickup ? {
            ...rawPickup,
            time: rawPickup.time * 0.001,
            nextDeathTime: (rawPickup.nextDeathTime || 0) * 0.001,
            dropTime: (rawPickup.dropTime || 0) * 0.001,
        } : null;
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
    bucketView: null,      // column-major ColumnarBuckets (50ms) from getDefaultBuckets
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
// resetUIToCleanState wipes every panel, table, canvas, and JS-side
// state object that could carry data from a previous demo. Called at
// the top of displayResults() so each load starts from a blank slate.
// Without this, conditionally-populated panels (teams, scoreboards,
// timeline graphs, map view, …) survive a swap from e.g. a 4on4 demo
// to an FFA demo that simply lacks those fields.
function resetUIToCleanState() {
    resetTimelineState();

    document.title = 'MVD Analyzer';

    // Match info fields
    const setText = (id, v) => {
        const el = document.getElementById(id);
        if (el) el.textContent = v;
    };
    const setHTML = (id, v) => {
        const el = document.getElementById(id);
        if (el) el.innerHTML = v;
    };
    const hide = id => {
        const el = document.getElementById(id);
        if (el) el.style.display = 'none';
    };

    setText('map-name', '-');
    setText('duration', '-');
    setText('mode', '-');
    setText('hostname', '-');
    setText('match-date', '-');

    // Topbar demo info
    setHTML('topbar-demo-info', '');

    // Match settings + server info panels
    setHTML('match-settings-grid', '');
    setHTML('match-modifiers', '');
    hide('match-settings-panel');
    hide('match-modifiers-row');
    setHTML('server-info-body', '');
    hide('server-info-panel');

    // Teams
    setHTML('teams-list', '');

    // Player / weapon / item tables (per-team + per-player)
    for (const id of [
        'player-stats-team-body', 'scoreboard-body',
        'weapon-stats-team-body', 'weapon-stats-body',
        'items-team-body', 'items-body',
    ]) setHTML(id, '');

    // Weapons chart
    setHTML('weapons-chart', '');

    // Timeline canvases
    for (const cid of [
        'detail-graph-canvas', 'health-armor-canvas',
        'score-canvas', 'weapons-per-player-canvas',
        'powerup-canvas', 'region-control-canvas',
    ]) {
        const c = document.getElementById(cid);
        if (c && c.getContext) c.getContext('2d').clearRect(0, 0, c.width, c.height);
    }
    hide('powerup-timeline-panel');
    hide('region-control-timeline-panel');
    hide('unified-timeline');
    setText('time-range-label', '');

    // Chat
    for (const id of ['chat-time-axis', 'kill-messages', 'team-a-messages', 'team-b-messages']) {
        setHTML(id, '');
    }
    setText('team-a-chat-title', 'Team A Chat');
    setText('team-b-chat-title', 'Team B Chat');

    // Map view
    const mapCanvas = document.getElementById('map-canvas');
    if (mapCanvas && mapCanvas.getContext) {
        mapCanvas.getContext('2d').clearRect(0, 0, mapCanvas.width, mapCanvas.height);
    }
    setHTML('map-legend', '');
    setHTML('map-items-body', '');
    hide('map-items-panel');
    hide('map-no-data');
    setHTML('region-control-body', '');
    hide('region-control-panel');
    setHTML('region-status-body', '');
    hide('region-status-panel');
    setHTML('region-config', '');

    // Loc graph
    if (locGraphState.cy) {
        try { locGraphState.cy.destroy(); } catch (_) {}
        locGraphState.cy = null;
    }
    locGraphState.graph = null;
    locGraphState.result = null;
    setHTML('locgraph-canvas', '');
    hide('locgraph-no-data');
    setHTML('locheatmap-body', '');
    hide('locheatmap-panel');

    // Key moments
    setHTML('keymoments-body', '');
    hide('keymoments-empty');
    setHTML('fragstreaks-body', '');
    hide('fragstreaks-empty');

    // Pack drops
    setHTML('packdrops-body', '');
    hide('packdrops-empty');
    setText('packdrops-count', '');

    // Pickups tables (thead + tbody both rebuilt from scratch each load)
    for (const id of [
        'pickups-weap-team-table', 'pickups-weap-player-table',
        'pickups-item-team-table', 'pickups-item-player-table',
    ]) {
        const t = document.getElementById(id);
        if (!t) continue;
        const head = t.querySelector('thead');
        const body = t.querySelector('tbody');
        if (head) head.innerHTML = '';
        if (body) body.innerHTML = '';
    }
    hide('pickups-empty');
    // pickups-items-panel is shown by displayPickupsTab when item data exists

    // Body class — duel-mode collapses some panels via CSS; clear it so
    // a non-duel demo loaded after a duel doesn't inherit the layout.
    document.body.classList.remove('duel-mode');

    // JS-side state objects that hold per-demo data
    mapState.locations = [];
    mapState.locationGroups = null;
    mapState.mapGeometry = null;
    mapState.bounds = { minX: 0, maxX: 0, minY: 0, maxY: 0 };
    mapState.currentTime = 0;
    mapState.isPlaying = false;
    mapState.playbackSpeed = 1;
    mapState.animationFrameId = null;
    mapState.lastRenderTime = 0;
    mapState.fullTrails = {};
    mapState.trailStartTimes = {};
    mapState.enabledPlayers = {};
    mapState.teams = [];
    mapState.playerSymbols = {};
    mapState.lastRenderedBucket = null;
    mapState.renderDirty = false;
    mapState.followPlayer = null;
    if ('controlRegions' in mapState) mapState.controlRegions = null;
    if ('rcResult' in mapState) mapState.rcResult = null;
    if ('locToRegion' in mapState) mapState.locToRegion = {};
    if ('dropEvents' in mapState) mapState.dropEvents = [];
    if ('zRange' in mapState) mapState.zRange = null;
    if ('locTable' in mapState) mapState.locTable = [''];

    packDropsState.rows = [];
    packDropsState.hubInfo = null;
    packDropsState.playerUserIDs = null;

    if (typeof weaponGraphHitState !== 'undefined') {
        weaponGraphHitState.W = null;
        weaponGraphHitState.dropMarks = [];
    }
    if (typeof powerupGraphHitState !== 'undefined') {
        powerupGraphHitState.W = null;
        powerupGraphHitState.rows = [];
    }
}

function resetTimelineState() {
    if (mapState.isPlaying) stopPlayback();

    // Map view data is rebuilt by initMapView, which is now deferred until
    // the 50ms buckets arrive (see applyDeferredBuckets). Clear it here so a
    // fast demo-swap can't render the previous demo's map during the window
    // before the new demo's buckets finish building.
    mapState.locations = [];
    mapState.locationGroups = null;
    mapState.mapGeometry = null;
    mapState.fullTrails = {};
    mapState.dropEvents = [];
    mapState.bucketStates = null;
    mapState.lastRenderedBucket = null;
    mapState.renderDirty = true;

    timelineState.bucketView = null;
    timelineState.highResDuration = 0.05;
    timelineState.events = [];
    timelineState.fragEvents = [];
    timelineState.deathEvents = [];
    timelineState.killEvents = [];
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
    for (const cid of ['detail-graph-canvas', 'health-armor-canvas', 'score-canvas']) {
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

    // Schema v7: bucketed data is not a parse-time field; the WASM worker
    // calls getDefaultBuckets after analyzeMVD and applyDeferredBuckets
    // stashes the resulting column-major ColumnarBuckets object on
    // timeline.bucketView. The panels read it via the bucket-view accessors.
    timelineState.bucketView = timeline?.bucketView || null;
    timelineState.highResDuration = 0.05;
    // Schema v8: raw time fields on result/timelineAnalysis flipped from
    // float seconds to int32 ms. The frontend internally still works in
    // seconds (mapState.currentTime, formatDuration, CHAT_PX_PER_SEC,
    // hub URL `from`/`to`, etc. all expect seconds), so convert ms→s once
    // here at intake. Anything copied into timelineState below is in
    // seconds and panels downstream are unchanged.
    timelineState.matchStartTime = (timeline?.matchStartTime || 0) * 0.001;
    timelineState.demoOffset = (timeline?.demoOffset || 0) * 0.001;
    timelineState.duration = (result.match?.duration || 600000) * 0.001;
    timelineState.events = (result.messages?.events || []).map(e => ({ ...e, time: e.time * 0.001 }));
    timelineState.fragEvents = (timeline?.fragEvents || []).map(f => ({ ...f, time: f.time * 0.001 })); // Frag events from stat tracking
    timelineState.deathEvents = (timeline?.deathEvents || []).map(d => ({ ...d, time: d.time * 0.001 })); // Per-player deaths (every death) for the frags/deaths drill-down
    timelineState.killEvents = (timeline?.killEvents || []).map(k => ({ ...k, time: k.time * 0.001 })); // Per-player enemy kills (killer-keyed) for the frags/deaths drill-down
    timelineState.backpacks = (result.backpacks || []).map(d => ({ ...d, time: d.time * 0.001 })); // RL/LG drops from KTX hint
    timelineState.powerupEvents = (timeline?.powerupEvents || []).map(ev => ({ // per-run records: player, team, frags, duration
        ...ev,
        time: ev.time * 0.001,
        endTime: (ev.endTime || 0) * 0.001,
        duration: (ev.duration || 0) * 0.001,
    }));

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
    const activeTab = document.querySelector('.sidebar-btn.active')?.dataset.tab;
    const tl = document.getElementById('unified-timeline');
    if (tl) tl.style.display = TABS_WITH_TIMELINE.includes(activeTab) ? '' : 'none';

    updateUnifiedCursor();
    updateDetailView();
    updateTimeIndicators();
    updateTeamStatus();
    renderChatMessages();
}

// ─── Columnar bucket-view accessors ──────────────────────────────────────────
//
// The worker ships a column-major ColumnarBuckets object (see
// mvd-analytics/view/columnar.go), stored on timelineState.bucketView:
//   { windowMs, startMs, count,
//     players: { name: { first, n, alive:[0/1], validFrom:{f:idx},
//                        h|a|li|sh|nl|rk|cl:[i16], x|y|z:[i32], at:[str],
//                        rl|lg|gl|ssg|sng|q|pe|r|sp|d:[0/1] } },
//     teams:   { name: { rl|lg|rllg|w|gl|q|pe|r|pw|th|ta:[int], abt:{ra|ya|ga:[int]} } } }
// Time axis is implicit: time(i) = (startMs + i*windowMs)/1000 seconds, so
// time→index is O(1) arithmetic (no more per-bucket binary search). Booleans
// and the alive mask are 0/1. A player only "exists" at bucket i while alive[i]
// is set; values carry forward through dead buckets, so callers must gate on
// liveness exactly as the old row shape omitted dead players per bucket.

function bucketTimeSec(view, i) {
    return (view.startMs + i * view.windowMs) / 1000;
}

// Bucket whose half-open span contains tSec (floor), clamped to a valid index.
function bucketIndexAtTime(view, tSec) {
    if (!view || !view.count) return -1;
    let i = Math.floor((tSec * 1000 - view.startMs) / view.windowMs);
    if (i < 0) i = 0;
    if (i >= view.count) i = view.count - 1;
    return i;
}

// First bucket at or after tSec (ceil), clamped to [0, count]. Replaces the old
// binarySearchBucketStart for range scans over a visible window.
function bucketIndexAtOrAfter(view, tSec) {
    if (!view || !view.count) return 0;
    let i = Math.ceil((tSec * 1000 - view.startMs) / view.windowMs);
    if (i < 0) i = 0;
    if (i > view.count) i = view.count;
    return i;
}

// Value of a player's field column at absolute bucket i, or undefined when the
// player is absent there (outside [first,first+n), dead, or before validFrom).
function playerValAt(p, field, i) {
    if (!p) return undefined;
    const rel = i - p.first;
    if (rel < 0 || rel >= p.n) return undefined;
    if (!p.alive[rel]) return undefined;
    const vf = p.validFrom && p.validFrom[field];
    if (vf !== undefined && i < vf) return undefined;
    const arr = p[field];
    if (!arr) return undefined;
    return arr[rel];
}

function playerAliveAt(p, i) {
    if (!p) return false;
    const rel = i - p.first;
    return rel >= 0 && rel < p.n && !!p.alive[rel];
}

// Field codes whose row-shape value is a boolean (emitted 0/1 in the columnar
// wire form) vs a number. armorType ("at") is a string.
const COLUMNAR_NUM_FIELDS = ['x', 'y', 'z', 'h', 'a', 'li', 'sh', 'nl', 'rk', 'cl'];
const COLUMNAR_BOOL_FIELDS = ['rl', 'lg', 'gl', 'ssg', 'sng', 'q', 'pe', 'r', 'sp', 'd'];

// reconstructBucketPlayers rebuilds the old row-shape p{} (player → field map)
// for the players alive at bucket i. Mirrors the Go columnarToRow oracle so the
// panels that still think row-major keep working unchanged.
function reconstructBucketPlayers(view, i) {
    const out = {};
    const players = view.players || {};
    for (const name in players) {
        const cp = players[name];
        if (!playerAliveAt(cp, i)) continue;
        const pd = {};
        for (const f of COLUMNAR_NUM_FIELDS) {
            const v = playerValAt(cp, f, i);
            if (v !== undefined) pd[f] = v;
        }
        for (const f of COLUMNAR_BOOL_FIELDS) {
            const v = playerValAt(cp, f, i);
            if (v !== undefined) pd[f] = !!v;
        }
        const at = playerValAt(cp, 'at', i);
        if (at !== undefined) pd.at = at;
        out[name] = pd;
    }
    return out;
}

// teamSnapshot returns the old row-shape team-data object (counters + abt) for
// one team at bucket i, or {} when the team is absent.
function teamSnapshot(view, team, i) {
    const t = view.teams && view.teams[team];
    if (!t) return {};
    const o = {};
    for (const k in t) {
        if (k === 'abt') {
            const abt = {};
            for (const a in t.abt) abt[a] = t.abt[a][i] || 0;
            o.abt = abt;
            continue;
        }
        o[k] = t[k][i] || 0;
    }
    return o;
}

// reconstructBucketTeams rebuilds the old row-shape td{} (team → data) at i.
function reconstructBucketTeams(view, i) {
    if (!view.teams) return undefined;
    const td = {};
    for (const team in view.teams) td[team] = teamSnapshot(view, team, i);
    return td;
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

// Plot background — slightly different shade than the panel so the
// graph area is visually delineated. Same colour for every detail
// graph so they read as a uniform set.
const PLOT_BG_COLOR = '#16213e';

// Size a canvas for device-pixel rendering and return the drawing
// context plus the dimensions in both CSS and device pixels.
//
// We render directly in device pixels (no ctx.scale) so every fillRect
// edge lands on an integer device pixel by construction. CSS-pixel
// rendering with ctx.scale(dpr, dpr) is fine when dpr is an integer,
// but at fractional dpr (Linux/KDE/GNOME fractional scaling, browser
// zoom != 100 %, some Windows configs) every CSS-pixel fillRect edge
// lands at a non-integer device-pixel offset; the rasteriser then
// splits that edge across two device pixels at ~50 % coverage each,
// producing visible darker boundary pixels (and at one fillRect per
// CSS pixel a regular moiré stripe pattern).
//
// Returns null if the canvas element is missing.
function setupGraphCanvas(canvasId, cssHeight) {
    const canvas = document.getElementById(canvasId);
    if (!canvas || !canvas.getContext) return null;
    const container = canvas.parentElement;
    const Wcss = container.clientWidth;
    const Hcss = cssHeight;
    const dpr = window.devicePixelRatio || 1;
    const W = Math.round(Wcss * dpr);
    const H = Math.round(Hcss * dpr);
    canvas.width = W;
    canvas.height = H;
    canvas.style.width = Wcss + 'px';
    canvas.style.height = Hcss + 'px';
    const ctx = canvas.getContext('2d');
    return { canvas, ctx, Wcss, Hcss, W, H, dpr };
}

// Draw the adaptive x-axis tick line and time labels at the bottom of
// a graph. Tick density keys off Wcss so it doesn't change with dpr.
// All coords are in device pixels (W, graphH, dpr).
function drawXAxisTicks(ctx, { W, Wcss, dpr, graphH, startTime, endTime }) {
    const duration = endTime - startTime;
    if (duration <= 0) return;
    const targetTicks = Math.max(4, Math.min(12, Math.floor(Wcss / 100)));
    const interval = pickTickInterval(duration, targetTicks);
    ctx.fillStyle = '#888';
    ctx.font = `${Math.round(10 * dpr)}px monospace`;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'top';
    ctx.strokeStyle = 'rgba(255,255,255,0.08)';
    const firstTick = Math.ceil(startTime / interval) * interval;
    for (let t = firstTick; t <= endTime; t += interval) {
        const x = ((t - startTime) / duration) * W;
        ctx.beginPath();
        ctx.moveTo(x, graphH);
        ctx.lineTo(x, graphH + 4 * dpr);
        ctx.stroke();
        ctx.fillText(formatDuration(t), x, graphH + 5 * dpr);
    }
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
    const setup = setupGraphCanvas(canvasId, 200);
    if (!setup) return;
    const { ctx, Wcss, W, H, dpr } = setup;

    // Constants below are in device pixels — multiply by dpr so the
    // displayed (CSS-px) size of axes / padding / dots / fonts matches
    // what the previous CSS-px-coordinate version drew.
    const AXIS_H = Math.round(20 * dpr);
    const PAD = Math.round(4 * dpr);
    const graphH = H - AXIS_H;
    // Drop-mark strips live in a reserved zone at the top and bottom of
    // the plot area so weapon bars can never grow into them — the
    // weapons bar height scales with max players-per-team, so without
    // this reservation a high-rollout 5v5 snapshot could paint bars
    // straight through the dots. Sized for one row of ~6 px dots.
    const DROP_STRIP_H = Math.round(8 * dpr);
    const hasDropMarks = !!(dropMarks && dropMarks.length);
    const stripZone = hasDropMarks ? DROP_STRIP_H + Math.round(2 * dpr) : 0;
    const midY = PAD + (graphH - PAD) / 2;
    const barH = midY - PAD - stripZone;
    const duration = endTime - startTime;

    // Plot background — safe again now that the scanline/hold-last
    // renderer below paints every pixel column with the active data
    // point: empty buckets no longer leave bg-coloured vertical stripes
    // through the bars. The bg only shows above/below bars and in the
    // axis strip.
    ctx.fillStyle = PLOT_BG_COLOR;
    ctx.fillRect(0, 0, W, graphH);

    // Grid lines at ±50% (drawn first so bars overlay them)
    ctx.strokeStyle = 'rgba(255,255,255,0.06)';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(0, midY - barH * 0.5); ctx.lineTo(W, midY - barH * 0.5);
    ctx.moveTo(0, midY + barH * 0.5); ctx.lineTo(W, midY + barH * 0.5);
    ctx.stroke();

    if (duration > 0 && dataPoints && dataPoints.length > 0) {
        // Output-driven (scanline) rendering: for each pixel column,
        // sample the data at that pixel's time via a monotonic cursor
        // (step function / hold-last) and paint a 1-pixel-wide column.
        // Replaces the older data-driven loop where we rasterised each
        // bucket as a fillRect of bw px — that was correct when bw ≥ 1
        // but skipped points whose `up` and `down` were both empty,
        // leaving the canvas pixel column un-painted; gaps in the data
        // surfaced as visible vertical stripes. Walking the *output*
        // makes "every column is drawn" structural and gives a draw
        // cost that scales with canvas width, not bucket count.
        let cursor = -1;
        const sampleAt = (tQuery) => {
            while (cursor + 1 < dataPoints.length && dataPoints[cursor + 1].t <= tQuery) {
                cursor++;
            }
            if (cursor < 0) return null;
            const pt = dataPoints[cursor];
            // Last point: cap at its declared dt so we don't paint past
            // the end of the data when the view extends slightly beyond it.
            if (cursor === dataPoints.length - 1) {
                const endT = pt.t + (pt.dt || 0);
                if (endT > 0 && tQuery > endT) return null;
            }
            return pt;
        };

        for (let px = 0; px < W; px++) {
            const tPx = startTime + (px / W) * duration;
            const pt = sampleAt(tPx);
            if (pt == null) continue;

            // Stack segments with integer-aligned y boundaries: track
            // the boundary in floating point but snap each fillRect
            // edge to a pixel row, using the previous segment's snapped
            // edge as the next segment's start. Stacked segments then
            // meet on exact pixel rows instead of anti-aliased
            // fractional ones.

            // Up segments (team A, above center).
            let yAcc = midY;
            let yPrev = Math.round(midY);
            if (pt.up) {
                for (const seg of pt.up) {
                    if (seg.h > 0) {
                        yAcc -= (seg.h / maxValue) * barH;
                        const yCur = Math.round(yAcc);
                        const segH = yPrev - yCur;
                        if (segH > 0) {
                            ctx.fillStyle = seg.color;
                            ctx.fillRect(px, yCur, 1, segH);
                        }
                        yPrev = yCur;
                    }
                }
            }

            // Down segments (team B, below center).
            yAcc = midY;
            yPrev = Math.round(midY);
            if (pt.down) {
                for (const seg of pt.down) {
                    if (seg.h > 0) {
                        yAcc += (seg.h / maxValue) * barH;
                        const yCur = Math.round(yAcc);
                        const segH = yCur - yPrev;
                        if (segH > 0) {
                            ctx.fillStyle = seg.color;
                            ctx.fillRect(px, yPrev, 1, segH);
                        }
                        yPrev = yCur;
                    }
                }
            }
        }
    }

    // Drop-mark dots — small filled circles in the reserved strip zone.
    // Top strip = team A drops, bottom strip = team B drops; color is
    // weapon-coded by the caller (e.g. RL red, LG cyan).
    if (hasDropMarks && duration > 0) {
        const dotR = 3 * dpr;
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
    ctx.fillRect(0, Math.round(midY), W, Math.max(1, Math.round(dpr)));

    drawXAxisTicks(ctx, { W, Wcss, dpr, graphH, startTime, endTime });

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
    const view = timelineState.bucketView;
    if (!view || !view.count) return { points: [], max: 4 };
    const hrDur = timelineState.highResDuration || 0.05;
    const points = [];
    let maxVal = 4;
    const idx0 = bucketIndexAtOrAfter(view, startTime);
    for (let i = idx0; i < view.count; i++) {
        const bt = bucketTimeSec(view, i);
        if (bt > endTime) break;
        const tdA = teamSnapshot(view, teams[0], i);
        const tdB = teamSnapshot(view, teams[1], i);
        const upT = (tdA.rl || 0) + (tdA.lg || 0) + (tdA.rllg || 0);
        const dnT = (tdB.rl || 0) + (tdB.lg || 0) + (tdB.rllg || 0);
        maxVal = Math.max(maxVal, upT, dnT);
        points.push({
            t: bt, dt: hrDur,
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
    const view = timelineState.bucketView;
    if (!view || !view.count) return { points: [], max: 400 };
    const hrDur = timelineState.highResDuration || 0.05;
    const points = [];
    let maxVal = 400;
    const idx0 = bucketIndexAtOrAfter(view, startTime);
    for (let i = idx0; i < view.count; i++) {
        const bt = bucketTimeSec(view, i);
        if (bt > endTime) break;
        const tdA = teamSnapshot(view, teams[0], i);
        const tdB = teamSnapshot(view, teams[1], i);
        maxVal = Math.max(maxVal, (tdA.th || 0) + (tdA.ta || 0), (tdB.th || 0) + (tdB.ta || 0));
        points.push({
            t: bt, dt: hrDur,
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

// ─── Per-player drill-downs (health/armor + weapons) ─────────────────────────
//
// Both the Team Health/Armor and Weapons panels expand to a per-player view.
// All series come from the columnar bucket view already on the client
// (timelineState.bucketView.players[name], read via playerValAt/playerAliveAt)
// plus timelineState.backpacks for weapon-drop dots — no extra data fetch.

// Group the bucket-view players into [teamAPlayers, teamBPlayers] following
// timelineState.teams order. Team membership mirrors updateTeamStatus: prefer
// demoInfo.players, fall back to the map's playerSymbols. Players whose team
// can't be resolved (spectators, mid-game joiners) are dropped.
function timelinePlayersByTeam() {
    const teams = timelineState.teams;
    const view = timelineState.bucketView;
    const out = [[], []];
    if (teams.length < 2 || !view || !view.players) return out;
    const demoPlayers = currentResult?.demoInfo?.players || [];
    const teamOf = (name) => {
        const dp = demoPlayers.find(p => p.name === name);
        if (dp?.team) return dp.team;
        return mapState.playerSymbols?.[name]?.team;
    };
    for (const name of Object.keys(view.players)) {
        const t = teamOf(name);
        const ti = t === teams[0] ? 0 : t === teams[1] ? 1 : -1;
        if (ti < 0) continue;
        out[ti].push(name);
    }
    out[0].sort();
    out[1].sort();
    return out;
}

// Single-player health+armor stack for the mini chart. Health (green) at the
// bottom, then armor coloured by its type (RA/YA/GA).
function buildPlayerHASegments(h, a, at) {
    const segs = [];
    if (h > 0) segs.push({ h, color: GRAPH_COLORS.HEALTH });
    if (a > 0) {
        const color = at === 'ra' ? GRAPH_COLORS.RA
                    : at === 'ga' ? GRAPH_COLORS.GA
                    : GRAPH_COLORS.YA; // ya or unknown
        segs.push({ h: a, color });
    }
    return segs;
}

function prepPlayerHAData(cp, startTime, endTime) {
    const view = timelineState.bucketView;
    const hrDur = timelineState.highResDuration || 0.05;
    const points = [];
    if (!view || !view.count || !cp) return points;
    const idx0 = bucketIndexAtOrAfter(view, startTime);
    for (let i = idx0; i < view.count; i++) {
        const bt = bucketTimeSec(view, i);
        if (bt > endTime) break;
        // playerValAt returns undefined when the player is dead/absent at this
        // bucket — emit an empty stack so the chart shows a gap there.
        const h = playerValAt(cp, 'h', i) || 0;
        const a = playerValAt(cp, 'a', i) || 0;
        const at = playerValAt(cp, 'at', i) || '';
        points.push({ t: bt, dt: hrDur, up: buildPlayerHASegments(h, a, at) });
    }
    return points;
}

// Compact single-direction stacked renderer for the per-player mini charts.
// Reuses setupGraphCanvas + the scanline hold-last sampling from
// renderDivergingGraph, but stacks upward from the baseline only and skips the
// diverging mirror, zero divider, and x-axis (the team chart above carries it).
function renderMiniStack(canvasId, { startTime, endTime, points, maxValue, height }) {
    const setup = setupGraphCanvas(canvasId, height || 44);
    if (!setup) return;
    const { ctx, W, H, dpr } = setup;
    const PAD = Math.round(2 * dpr);
    const baseY = H - PAD;
    const usableH = H - 2 * PAD;

    ctx.fillStyle = PLOT_BG_COLOR;
    ctx.fillRect(0, 0, W, H);

    const duration = endTime - startTime;
    if (duration <= 0 || !points || points.length === 0 || maxValue <= 0) return;

    let cursor = -1;
    const sampleAt = (tQuery) => {
        while (cursor + 1 < points.length && points[cursor + 1].t <= tQuery) cursor++;
        if (cursor < 0) return null;
        const pt = points[cursor];
        if (cursor === points.length - 1) {
            const endT = pt.t + (pt.dt || 0);
            if (endT > 0 && tQuery > endT) return null;
        }
        return pt;
    };

    for (let px = 0; px < W; px++) {
        const tPx = startTime + (px / W) * duration;
        const pt = sampleAt(tPx);
        if (!pt || !pt.up) continue;
        let yAcc = baseY;
        let yPrev = Math.round(baseY);
        for (const seg of pt.up) {
            if (seg.h > 0) {
                yAcc -= (seg.h / maxValue) * usableH;
                const yCur = Math.round(yAcc);
                const segH = yPrev - yCur;
                if (segH > 0) {
                    ctx.fillStyle = seg.color;
                    ctx.fillRect(px, yCur, 1, segH);
                }
                yPrev = yCur;
            }
        }
    }
}

// Render one mini health/armor chart per player under the Team Health/Armor
// panel. The per-player canvases are built lazily and rebuilt only when the
// roster changes (signature check); on every view change we just re-render.
function renderHealthArmorPerPlayer(startTime, endTime) {
    const container = document.getElementById('ha-per-player');
    if (!container) return;
    const teams = timelineState.teams;
    const view = timelineState.bucketView;
    if (!view || !view.count || teams.length < 2) {
        // Clearing the DOM must invalidate the rebuild cache too: otherwise a
        // later render with the same roster signature skips the rebuild and
        // iterates now-detached _cells, rendering nothing. This bit when a new
        // demo's buckets arrive deferred (the first render runs view-less).
        container.innerHTML = '';
        container._sig = null;
        container._cells = [];
        return;
    }

    const grouped = timelinePlayersByTeam();
    const sig = grouped.map(g => g.join('|')).join('#');
    if (container._sig !== sig) {
        container.innerHTML = '';
        container._cells = [];
        for (let ti = 0; ti < 2; ti++) {
            grouped[ti].forEach((name, idx) => {
                const cid = `ha-pp-${ti}-${idx}`;
                const cell = document.createElement('div');
                cell.className = 'per-player-cell';
                const label = document.createElement('div');
                label.className = 'per-player-label';
                label.textContent = name;
                label.title = name;
                label.style.color = TEAM_COLORS[ti] || '#ccc';
                const wrap = document.createElement('div');
                wrap.className = 'per-player-canvas-wrap';
                const canvas = document.createElement('canvas');
                canvas.id = cid;
                canvas.className = 'timeline-canvas';
                const indicator = document.createElement('div');
                indicator.className = 'current-time-indicator pp-time-indicator';
                wrap.appendChild(canvas);
                wrap.appendChild(indicator);
                cell.appendChild(label);
                cell.appendChild(wrap);
                container.appendChild(cell);
                installGraphPanZoom(cid);
                installIndicatorScrub(indicator, wrap);
                container._cells.push({ name, cid });
            });
        }
        container._sig = sig;
    }

    // Shared max across all players (rounded up) so the minis are comparable.
    let maxVal = 300;
    const prepped = {};
    for (const { name } of container._cells) {
        const pts = prepPlayerHAData(view.players[name], startTime, endTime);
        prepped[name] = pts;
        for (const p of pts) {
            let s = 0;
            for (const seg of p.up) s += seg.h;
            if (s > maxVal) maxVal = s;
        }
    }
    for (const { name, cid } of container._cells) {
        const cv = document.getElementById(cid);
        if (cv) cv.style.width = ''; // let the cell reflow on resize
        renderMiniStack(cid, { startTime, endTime, points: prepped[name] || [], maxValue: maxVal, height: 44 });
    }
    renderSharedTimeAxis(container, startTime, endTime);
}

// ─── Per-player frags / deaths drill-down ───────────────────────────────────
//
// One compact +/- chart per player under the Score Timeline, mirroring the team
// Score Timeline above: the player's running plus/minus = cumulative enemy
// kills minus cumulative deaths. When ahead (more kills than deaths) the area
// rises above the divider in the team colour; when behind it drops below in a
// dimmed team colour. Kills come from killEvents (the canonical frag log,
// suicides/teamkills excluded) and deaths from deathEvents (every death counts
// once), so the full-match endpoint equals byPlayer.kills − byPlayer.deaths and
// reconciles with the kills-based efficiency = kills/(kills+deaths)
// (ktx/src/statsTables.c calculateEfficiency) shown on each row.

// Darken a team hex toward black so the behind-half reads as a muted mirror of
// the team-coloured ahead-half (position above/below the divider already
// separates them; the shade keeps team identity).
function dimColor(hex) {
    const [r, g, b] = hexToRgb(hex);
    return `rgb(${Math.round(r * 0.55)},${Math.round(g * 0.55)},${Math.round(b * 0.55)})`;
}

// Build sampled +/- points for one player plus the window-end kill/death
// totals. Mirrors prepScoreData: carry the running totals from match start so
// the window's left edge shows the cumulative plus/minus, then plot the single
// net value (kills − deaths) as up when positive / down when negative.
function prepPlayerFragDeathData(name, startTime, endTime, upColor, downColor) {
    const ke = timelineState.killEvents || [];
    const de = timelineState.deathEvents || [];

    let kills = 0, deaths = 0;
    for (const k of ke) {
        if (k.time >= startTime) break;
        if (k.player === name) kills += 1;
    }
    for (const d of de) {
        if (d.time >= startTime) break;
        if (d.player === name) deaths += 1;
    }

    // Merge this player's in-window kill and death events by time, then
    // accumulate into a net (kills − deaths) step series.
    const evs = [];
    for (const k of ke) {
        if (k.time < startTime) continue;
        if (k.time > endTime) break;
        if (k.player === name) evs.push({ time: k.time, dn: 1 });
    }
    for (const d of de) {
        if (d.time < startTime) continue;
        if (d.time > endTime) break;
        if (d.player === name) evs.push({ time: d.time, dn: -1 });
    }
    evs.sort((a, b) => a.time - b.time);

    const stepAt = [{ time: startTime, net: kills - deaths }];
    let ck = kills, cd = deaths;
    for (const e of evs) {
        if (e.dn > 0) ck += 1; else cd += 1;
        stepAt.push({ time: e.time, net: ck - cd });
    }
    stepAt.push({ time: endTime, net: ck - cd });

    const duration = endTime - startTime;
    const sampleRate = Math.max(0.5, duration / 400);
    let maxVal = 1;
    const points = [];
    let si = 0;
    for (let t = startTime; t < endTime; t += sampleRate) {
        while (si + 1 < stepAt.length && stepAt[si + 1].time <= t) si++;
        const v = stepAt[si].net;
        maxVal = Math.max(maxVal, Math.abs(v));
        points.push({
            t, dt: sampleRate,
            up: v > 0 ? [{ h: v, color: upColor }] : [],
            down: v < 0 ? [{ h: -v, color: downColor }] : [],
        });
    }
    return { points, max: maxVal, kills: ck, deaths: cd };
}

// Compact diverging renderer for the per-player frags/deaths minis: net frags
// stack up from the center, deaths stack down, sharing maxValue. Mirrors
// renderMiniStack's scanline sampling, mirrored across a faint center divider.
function renderMiniDiverging(canvasId, { startTime, endTime, points, maxValue, height }) {
    const setup = setupGraphCanvas(canvasId, height || 44);
    if (!setup) return;
    const { ctx, W, H, dpr } = setup;
    const PAD = Math.round(2 * dpr);
    const midY = Math.round(H / 2);
    const halfH = (H - 2 * PAD) / 2;

    ctx.fillStyle = PLOT_BG_COLOR;
    ctx.fillRect(0, 0, W, H);

    const duration = endTime - startTime;
    if (duration > 0 && points && points.length && maxValue > 0) {
        let cursor = -1;
        const sampleAt = (tQuery) => {
            while (cursor + 1 < points.length && points[cursor + 1].t <= tQuery) cursor++;
            if (cursor < 0) return null;
            const pt = points[cursor];
            if (cursor === points.length - 1) {
                const endT = pt.t + (pt.dt || 0);
                if (endT > 0 && tQuery > endT) return null;
            }
            return pt;
        };
        for (let px = 0; px < W; px++) {
            const tPx = startTime + (px / W) * duration;
            const pt = sampleAt(tPx);
            if (!pt) continue;
            // Up (net frags, above center).
            let yAcc = midY, yPrev = midY;
            if (pt.up) for (const seg of pt.up) {
                if (seg.h > 0) {
                    yAcc -= (seg.h / maxValue) * halfH;
                    const yCur = Math.round(yAcc);
                    const segH = yPrev - yCur;
                    if (segH > 0) { ctx.fillStyle = seg.color; ctx.fillRect(px, yCur, 1, segH); }
                    yPrev = yCur;
                }
            }
            // Down (deaths, below center).
            yAcc = midY; yPrev = midY;
            if (pt.down) for (const seg of pt.down) {
                if (seg.h > 0) {
                    yAcc += (seg.h / maxValue) * halfH;
                    const yCur = Math.round(yAcc);
                    const segH = yCur - yPrev;
                    if (segH > 0) { ctx.fillStyle = seg.color; ctx.fillRect(px, yPrev, 1, segH); }
                    yPrev = yCur;
                }
            }
        }
    }

    // Center divider.
    ctx.fillStyle = 'rgba(255, 255, 255, 0.5)';
    ctx.fillRect(0, midY, W, Math.max(1, Math.round(dpr)));
}

// Render one mini +/- chart per player under the Score Timeline panel. Built
// lazily, rebuilt only when the roster changes (signature check). Each row is
// scaled by its OWN |net| max (0 stays centred) so a player whose plus/minus
// swings little still fills the row instead of collapsing to a few pixels next
// to a high-swing team-mate. The labels track the playhead via
// updateFragsPerPlayerStats.
function renderFragsPerPlayer(startTime, endTime) {
    const container = document.getElementById('frags-per-player');
    if (!container) return;
    const teams = timelineState.teams;
    const view = timelineState.bucketView;
    if (!view || !view.count || teams.length < 2) {
        // Clearing the DOM must invalidate the rebuild cache too: otherwise a
        // later render with the same roster signature skips the rebuild and
        // iterates now-detached _cells, rendering nothing. This bit when a new
        // demo's buckets arrive deferred (the first render runs view-less).
        container.innerHTML = '';
        container._sig = null;
        container._cells = [];
        return;
    }

    const grouped = timelinePlayersByTeam();
    const sig = grouped.map(g => g.join('|')).join('#');
    if (container._sig !== sig) {
        container.innerHTML = '';
        container._cells = [];
        for (let ti = 0; ti < 2; ti++) {
            grouped[ti].forEach((name, idx) => {
                const cid = `fd-pp-${ti}-${idx}`;
                const cell = document.createElement('div');
                cell.className = 'per-player-cell';
                const label = document.createElement('div');
                label.className = 'per-player-label fd-label';
                label.style.color = TEAM_COLORS[ti] || '#ccc';
                const nameEl = document.createElement('div');
                nameEl.className = 'fd-name';
                nameEl.textContent = name;
                nameEl.title = name;
                const statEl = document.createElement('div');
                statEl.className = 'fd-stat';
                label.appendChild(nameEl);
                label.appendChild(statEl);
                const wrap = document.createElement('div');
                wrap.className = 'per-player-canvas-wrap';
                const canvas = document.createElement('canvas');
                canvas.id = cid;
                canvas.className = 'timeline-canvas';
                const indicator = document.createElement('div');
                indicator.className = 'current-time-indicator pp-time-indicator';
                wrap.appendChild(canvas);
                wrap.appendChild(indicator);
                cell.appendChild(label);
                cell.appendChild(wrap);
                container.appendChild(cell);
                installGraphPanZoom(cid);
                installIndicatorScrub(indicator, wrap);
                container._cells.push({ name, cid, ti, statEl });
            });
        }
        container._sig = sig;
    }

    for (const { name, ti, cid } of container._cells) {
        const upColor = TEAM_COLORS[ti] || '#ccc';
        const d = prepPlayerFragDeathData(name, startTime, endTime, upColor, dimColor(upColor));
        const cv = document.getElementById(cid);
        if (cv) cv.style.width = '';
        // Per-player max (symmetric about 0): each row uses its full height.
        renderMiniDiverging(cid, { startTime, endTime, points: d.points, maxValue: d.max, height: 44 });
    }
    updateFragsPerPlayerStats();
    renderSharedTimeAxis(container, startTime, endTime);
}

// Update the per-player frags/deaths labels to the cumulative kills / deaths
// and efficiency AT the current playback time (not whole-match totals), so the
// numbers track the playhead as it moves / scrubs. Counts the kill and death
// event streams in one pass each; no-op when the drill-down isn't built.
function updateFragsPerPlayerStats() {
    const container = document.getElementById('frags-per-player');
    if (!container || !container._cells || !container._cells.length) return;
    const t = mapState.currentTime;
    const kc = {}, dc = {};
    for (const e of (timelineState.killEvents || [])) if (e.time <= t) kc[e.player] = (kc[e.player] || 0) + 1;
    for (const e of (timelineState.deathEvents || [])) if (e.time <= t) dc[e.player] = (dc[e.player] || 0) + 1;
    for (const { name, statEl } of container._cells) {
        if (!statEl) continue;
        const k = kc[name] || 0, d = dc[name] || 0;
        const eff = (k + d) > 0 ? Math.round((k / (k + d)) * 100) : 0;
        statEl.textContent = `${k}/${d} · ${eff}%`;
        statEl.title = `${k} kills / ${d} deaths · ${eff}% efficiency at ${formatDuration(t)}`;
    }
}

// Per-player weapons timeline: one combined row per player coloured by whether
// they hold RL / LG / both, with weapon-drop events as dots. Reuses
// renderSpansTimeline (one row per player) with team-coloured labels.
function prepPlayerWeaponSpans(cp, startTime, endTime) {
    const view = timelineState.bucketView;
    const spans = [];
    if (!view || !view.count || !cp) return spans;
    const idx0 = bucketIndexAtOrAfter(view, startTime);
    let curState = null, curStart = startTime;
    for (let i = idx0; i < view.count; i++) {
        const bt = bucketTimeSec(view, i);
        if (bt > endTime) break;
        let state = null;
        if (playerAliveAt(cp, i)) {
            const rl = !!playerValAt(cp, 'rl', i);
            const lg = !!playerValAt(cp, 'lg', i);
            if (rl && lg) state = 'rllg';
            else if (rl) state = 'rl';
            else if (lg) state = 'lg';
        }
        if (state !== curState) {
            if (curState) spans.push({ start: curStart, end: bt, state: curState });
            curState = state;
            curStart = bt;
        }
    }
    if (curState) spans.push({ start: curStart, end: endTime, state: curState });
    return spans;
}

function renderWeaponsPerPlayer(startTime, endTime) {
    const labelsEl = document.getElementById('weapons-per-player-labels');
    if (!labelsEl) return;
    const teams = timelineState.teams;
    const view = timelineState.bucketView;
    if (!view || !view.count || teams.length < 2) { labelsEl.innerHTML = ''; return; }

    const grouped = timelinePlayersByTeam();
    const rows = [];
    const rowPlayers = [];
    for (let ti = 0; ti < 2; ti++) {
        for (const name of grouped[ti]) {
            rows.push({
                name,
                color: TEAM_COLORS[ti] || '#ccc',
                spans: prepPlayerWeaponSpans(view.players[name], startTime, endTime),
            });
            rowPlayers.push(name);
        }
    }
    if (rows.length === 0) { labelsEl.innerHTML = ''; return; }

    const dropMarks = [];
    for (const d of (timelineState.backpacks || [])) {
        if (d.time < startTime || d.time > endTime) continue;
        const row = rowPlayers.indexOf(d.player);
        if (row < 0) continue;
        const color = d.weapon === 'rl' ? GRAPH_COLORS.RL
                    : d.weapon === 'lg' ? GRAPH_COLORS.LG
                    : null;
        if (!color) continue;
        dropMarks.push({ row, time: d.time, color });
    }

    renderSpansTimeline('weapons-per-player-canvas', 'weapons-per-player-labels', {
        startTime, endTime, rows,
        stateColors: { rl: GRAPH_COLORS.RL, lg: GRAPH_COLORS.LG, rllg: GRAPH_COLORS.RLLG },
        dropMarks,
    });
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
        // Pin the playhead to its on-screen position while panning: shift the
        // current time by the amount the window actually moved (after the
        // [0,duration] clamp), so the indicator line stays put and the unified
        // caret + clock + map follow. Not zoomed ⇒ the window can't move ⇒
        // applied === 0 ⇒ the playhead holds.
        const [newStart] = currentViewRange();
        const applied = newStart - start;
        if (applied !== 0) setCurrentTime(mapState.currentTime + applied);
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

// ─── Current-time scrubbing on the graphs ───────────────────────────────────
//
// Grabbing the current-time line on any timeline graph (team or per-player,
// normal or zoomed) sets the playback time. The indicator div sits above the
// canvas; its parent (the graph-outer / canvas-wrap / spans-outer) holds the
// canvas at full width and so shares the canvas's time→x mapping, which we
// invert through currentViewRange. One global move/up pair, mirroring
// graphPanState.
const graphScrubState = { el: null };
let graphScrubGlobalsInstalled = false;

function ensureGraphScrubGlobals() {
    if (graphScrubGlobalsInstalled) return;
    graphScrubGlobalsInstalled = true;
    document.addEventListener('mousemove', (e) => {
        const el = graphScrubState.el;
        if (!el) return;
        const rect = el.getBoundingClientRect();
        if (rect.width <= 0) return;
        const [start, end] = currentViewRange();
        const frac = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
        setCurrentTime(start + frac * (end - start));
    });
    document.addEventListener('mouseup', () => {
        if (!graphScrubState.el) return;
        graphScrubState.el = null;
        document.body.classList.remove('graph-scrubbing');
    });
}

// Make a current-time indicator line draggable to scrub. measureEl (the
// element whose width maps to the view range) defaults to the indicator's
// parent, which holds the canvas at full width.
function installIndicatorScrub(indicatorEl, measureEl) {
    if (!indicatorEl || indicatorEl._scrubInstalled) return;
    indicatorEl._scrubInstalled = true;
    ensureGraphScrubGlobals();
    indicatorEl.addEventListener('mousedown', (e) => {
        if (e.button !== 0) return;
        graphScrubState.el = measureEl || indicatorEl.parentElement;
        document.body.classList.add('graph-scrubbing');
        e.preventDefault();
        e.stopPropagation();
    });
}

// Render one shared time axis at the bottom of a per-player grid, aligned with
// the graph column (an empty label-width spacer + a tick track spanning the
// canvas column). Rebuilt on every view change since labels depend on the
// window. The team graphs draw their axis on-canvas and the per-player weapons
// spans canvas draws its own; the compact frags/health minis share this single
// axis instead of repeating one per row.
function renderSharedTimeAxis(gridEl, startTime, endTime) {
    if (!gridEl) return;
    let axis = gridEl.querySelector(':scope > .per-player-axis-cell');
    let track;
    if (!axis) {
        axis = document.createElement('div');
        axis.className = 'per-player-cell per-player-axis-cell';
        const spacer = document.createElement('div');
        spacer.className = 'per-player-label';
        track = document.createElement('div');
        track.className = 'per-player-axis-track';
        axis.appendChild(spacer);
        axis.appendChild(track);
    } else {
        track = axis.querySelector('.per-player-axis-track');
    }
    gridEl.appendChild(axis); // keep the axis as the last row
    track.innerHTML = '';
    const duration = endTime - startTime;
    if (duration <= 0) return;
    const wCss = track.clientWidth || 300;
    const targetTicks = Math.max(4, Math.min(10, Math.floor(wCss / 100)));
    const interval = pickTickInterval(duration, targetTicks);
    const firstTick = Math.ceil(startTime / interval) * interval;
    for (let t = firstTick; t <= endTime + 1e-6; t += interval) {
        const span = document.createElement('span');
        span.textContent = formatDuration(t);
        span.style.left = `${((t - startTime) / duration) * 100}%`;
        track.appendChild(span);
    }
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
     'health-armor-canvas', 'score-canvas',
     // Per-player weapons spans canvas — same Ctrl/Cmd+scroll zoom + drag-pan.
     'weapons-per-player-canvas'].forEach(installGraphPanZoom);

    // --- Drag the current-time line on any graph to scrub the clock ---
    ['detail-time-indicator', 'powerup-time-indicator', 'region-time-indicator',
     'health-time-indicator', 'score-time-indicator',
     'weapons-pp-time-indicator'].forEach(id =>
        installIndicatorScrub(document.getElementById(id)));

    // --- Hover tooltip on weapon-graph drop dots ---
    attachWeaponGraphTooltip();

    // --- Hover tooltip on powerup-timeline spans ---
    attachPowerupTimelineTooltip();

    // --- Per-player drill-down toggles: render on first open (and on every
    // re-open) at the current view range. Subsequent view changes re-render
    // via updateDetailGraph / updateHealthArmorGraph while the panel is open.
    // updateTimeIndicators after the first render seats the freshly-created
    // current-time lines at the playhead (they'd otherwise sit at 0% until the
    // next view/time change).
    const haDet = document.getElementById('ha-per-player-details');
    if (haDet && !haDet._toggleWired) {
        haDet._toggleWired = true;
        haDet.addEventListener('toggle', () => {
            if (haDet.open) { renderHealthArmorPerPlayer(...currentViewRange()); updateTimeIndicators(); }
        });
    }
    const wpDet = document.getElementById('weapons-per-player-details');
    if (wpDet && !wpDet._toggleWired) {
        wpDet._toggleWired = true;
        wpDet.addEventListener('toggle', () => {
            if (wpDet.open) { renderWeaponsPerPlayer(...currentViewRange()); updateTimeIndicators(); }
        });
    }
    const fpDet = document.getElementById('frags-per-player-details');
    if (fpDet && !fpDet._toggleWired) {
        fpDet._toggleWired = true;
        fpDet.addEventListener('toggle', () => {
            if (fpDet.open) { renderFragsPerPlayer(...currentViewRange()); updateTimeIndicators(); }
        });
    }

    // --- Window resize: detail-panel canvases are sized in pixels from
    // container.clientWidth, so they don't reflow with the viewport on
    // their own — re-render them when the window resizes. Only the
    // timeline tab has the detail panels; on other tabs the active-tab
    // re-render path handles it on switch. Debounced to a single
    // animation frame per resize burst.
    window.addEventListener('resize', onTimelineWindowResize);

    unifiedTimelineInitialized = true;
}

const TIMELINE_CANVAS_IDS = [
    'detail-graph-canvas', 'powerup-canvas', 'region-control-canvas',
    'health-armor-canvas', 'score-canvas', 'weapons-per-player-canvas',
];

let _timelineResizeRafId = null;
function onTimelineWindowResize() {
    if (!currentResult) return;
    const activeTab = document.querySelector('.sidebar-btn.active')?.dataset.tab;
    if (activeTab !== 'timeline') return;
    if (_timelineResizeRafId !== null) return;
    _timelineResizeRafId = requestAnimationFrame(() => {
        _timelineResizeRafId = null;
        // The renderers set canvas.style.width in pixels, which (combined
        // with the default flex `min-width: auto`) wedges the parent open
        // and prevents shrink-to-fit on window down-size. Clear the inline
        // width before re-measuring so each container reports its true
        // available width via clientWidth.
        for (const id of TIMELINE_CANVAS_IDS) {
            const c = document.getElementById(id);
            if (c) c.style.width = '';
        }
        updateDetailView();
        updateTimeIndicators();
    });
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

    // Per-player drill-down lines share the same view window, so the same
    // pct applies. These exist only while a drill-down is open.
    document.querySelectorAll('.pp-time-indicator').forEach(el => {
        el.style.left = `${pct}%`;
    });

    // Per-player frags/deaths labels read the cumulative score at the playhead.
    updateFragsPerPlayerStats();

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

    // Update all detail panels (axes are drawn on canvas by the unified
    // renderer). Order mirrors the DOM: score, health/armor, weapons, then
    // the optional span timelines.
    updateScoreTimeline(start, end);
    updateHealthArmorGraph(start, end);
    updateDetailGraph(start, end);
    updatePowerupTimeline(start, end);
    updateRegionControlTimeline(start, end);

    // Re-glue the current-time lines to the new window. Without this the
    // indicators keep their last left% after a zoom/pan/select and end up
    // over the wrong time until the next setCurrentTime.
    updateTimeIndicators();
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

    // Schema v8: messages.events[].time is int32 ms on the raw result;
    // timelineState.events was already converted to seconds at intake in
    // displayTimelineAnalysis, so use that pre-converted copy here.
    // `duration` (timelineState.duration) is also seconds.
    const seen = new Map();
    const events = (timelineState.events || []).filter(e => {
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

    if (document.getElementById('weapons-per-player-details')?.open) {
        renderWeaponsPerPlayer(startTime, endTime);
    }
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

    if (document.getElementById('ha-per-player-details')?.open) {
        renderHealthArmorPerPlayer(startTime, endTime);
    }
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

    if (document.getElementById('frags-per-player-details')?.open) {
        renderFragsPerPlayer(startTime, endTime);
    }
}

// ─── Region Control Timeline ────────────────────────────────────────────────
//
// One row per control region; each row colors contiguous spans by indexing
// into mapState.bucketStates (the Go-supplied per-bucket state strings from
// view.RegionControl). Only the "strong" states (solo armed control +
// armed-vs-armed contested) paint pixels — weak states render as gaps to
// keep the color story readable.

const RC_ROW_H = 20;
const RC_AXIS_H = 20;

function prepRegionControlData(startTime, endTime, teams) {
    const regions = mapState.controlRegions;
    const bucketStates = mapState.bucketStates;
    if (!regions || regions.length === 0 || !bucketStates) return null;
    const view = timelineState.bucketView;
    if (!view || !view.count) return null;

    // Team labels come from the Go-supplied regionControl when present
    // (matches the bucketStates A/B encoding); fall back to mapState.teams
    // for the rendering layer that wants to show team names.
    const teamA = mapState.teamA || teams[0];
    const teamB = mapState.teamB || teams[1];

    // bucketStates is computed at the same 50ms-from-match-start grid as the
    // bucket view (recomputeRegionControl uses WindowMs:50), so its per-region
    // string indexes 1:1 with the columnar bucket index.
    const idx0 = bucketIndexAtOrAfter(view, startTime);
    const rows = [];
    for (const r of regions) {
        const s = bucketStates[r.name];
        if (typeof s !== 'string') continue;
        const spans = [];
        let curState = null;
        let curStart = startTime;
        for (let i = idx0; i < view.count; i++) {
            const bt = bucketTimeSec(view, i);
            if (bt > endTime) break;
            const c = i < s.length ? s[i] : '_';
            const state = decodeRegionStateChar(c);
            if (state !== curState) {
                if (curState) spans.push({ start: curStart, end: bt, state: curState });
                curState = state;
                curStart = bt;
            }
        }
        if (curState) spans.push({ start: curStart, end: endTime, state: curState });
        rows.push({ name: r.name, spans });
    }
    return { rows, teamA, teamB };
}

// Generic span-timeline renderer. Each row carries a list of
// {start, end, state} spans; stateColors maps state strings to fill
// colors. Used by both the region-control timeline and the powerup
// timeline so they share one renderer instead of two near-copies.
// dropMarks (optional): [{row, time, color}] — small dots painted in a
// row, e.g. weapon-drop events on the per-player weapons timeline. Callers
// that don't pass it (powerups, region control) render unchanged.
function renderSpansTimeline(canvasId, labelsId, { startTime, endTime, rows, stateColors, dropMarks }) {
    const labelsEl = document.getElementById(labelsId);
    if (!labelsEl) return;
    const setup = setupGraphCanvas(canvasId, rows.length * RC_ROW_H + RC_AXIS_H);
    if (!setup) return;
    const { ctx, Wcss, W, dpr } = setup;

    const ROW_H = Math.round(RC_ROW_H * dpr);
    const ROW_PAD = Math.max(1, Math.round(dpr));

    // Label column, one DOM element per row, sized to match the canvas
    // row in CSS pixels (the labels live outside the canvas).
    labelsEl.innerHTML = '';
    for (const r of rows) {
        const lab = document.createElement('div');
        lab.className = 'region-timeline-label';
        lab.style.height = RC_ROW_H + 'px';
        lab.style.lineHeight = RC_ROW_H + 'px';
        if (r.color) lab.style.color = r.color;
        lab.textContent = r.name;
        lab.title = r.name;
        labelsEl.appendChild(lab);
    }

    const graphH = rows.length * ROW_H;
    ctx.fillStyle = PLOT_BG_COLOR;
    ctx.fillRect(0, 0, W, graphH);

    const duration = endTime - startTime;
    if (duration <= 0) return;

    rows.forEach((row, idx) => {
        const y = idx * ROW_H;
        for (const span of row.spans) {
            const color = stateColors[span.state];
            if (!color) continue;
            const x1 = Math.round(((span.start - startTime) / duration) * W);
            const x2 = Math.round(((span.end - startTime) / duration) * W);
            const w = x2 - x1;
            if (w <= 0) continue;
            ctx.fillStyle = color;
            ctx.fillRect(x1, y + ROW_PAD, w, ROW_H - 2 * ROW_PAD);
        }
    });

    if (dropMarks && dropMarks.length) {
        const dotR = 3 * dpr;
        for (const m of dropMarks) {
            const x = ((m.time - startTime) / duration) * W;
            if (x < -dotR || x > W + dotR) continue;
            const y = m.row * ROW_H + ROW_H / 2;
            ctx.fillStyle = m.color;
            ctx.beginPath();
            ctx.arc(x, y, dotR, 0, Math.PI * 2);
            ctx.fill();
        }
    }

    drawXAxisTicks(ctx, { W, Wcss, dpr, graphH, startTime, endTime });
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
    const view = timelineState.bucketView;
    if (!view || !view.count || teams.length < 2) {
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

// Map the compact one-char-per-bucket state codes emitted by the Go
// analyzer (qwanalytics/analyzer/region_control.go) back to the JS
// state names used by the region-control timeline / drawRegionControlOverlay.
const REGION_STATE_BY_CHAR = {
    '_': 'empty',
    'A': 'teamAControl', 'a': 'teamAWeakControl',
    'B': 'teamBControl', 'b': 'teamBWeakControl',
    'C': 'contested',    'c': 'weakContested',
};

function decodeRegionStateChar(c) {
    return REGION_STATE_BY_CHAR[c] || 'empty';
}

// findHighResBucketIndexAtTime returns the bucket index whose span contains
// `time` — used for cheap O(1) lookups into Go-supplied bucketStates strings
// (which share the bucket view's grid).
function findHighResBucketIndexAtTime(time) {
    return bucketIndexAtTime(timelineState.bucketView, time);
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
    fullscreen: false,        // True while the map panel is in fullscreen (set by fullscreenchange)
    // "Learn map" mode: a static study view that hides players and shows
    // the map's designed entity layout (result.mapEntities) instead.
    learnMode: false,
    mapEntities: [],          // result.mapEntities.entities for the current demo
    teleportArrows: [],       // precomputed {sx,sy,dx,dy} entrance→exit world-coord pairs
    entityFilters: {          // per-category visibility in learn mode
        weapon: true, armor: true, health: true, ammo: true, powerup: true,
        teleporter: true, spawn: false, button: false, door: false
    }
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

    // Static map-entity corpus (result.mapEntities) for the "learn map" view.
    mapState.mapEntities = (result.mapEntities && Array.isArray(result.mapEntities.entities))
        ? result.mapEntities.entities : [];
    buildTeleportArrows();
    // Fresh demo always starts in live mode; only offer learn mode when the
    // map has a static entity corpus.
    mapState.learnMode = false;
    const entPanel = document.getElementById('map-entities-panel');
    if (entPanel) entPanel.style.display = 'none';
    const learnBtn = document.getElementById('map-learn-toggle');
    if (learnBtn) {
        learnBtn.style.display = mapState.mapEntities.length > 0 ? '' : 'none';
        learnBtn.classList.remove('active');
        learnBtn.textContent = 'Learn map';
    }

    // Fire-and-forget: try to load pre-generated BSP-derived map geometry.
    // If present, switch from convex-hull blobs to real floor polygons.
    // If absent (404 or fetch error), the existing hull path remains as fallback.
    const rawMapName = result.demoInfo && result.demoInfo.map ? result.demoInfo.map : '';
    const mapBasename = rawMapName.toLowerCase().replace(/^maps\//, '').replace(/\.bsp$/, '');
    if (mapBasename) {
        const tGeom = performance.now();
        fetch(`maps/${mapBasename}.json`)
            .then(r => r.ok ? r.json() : null)
            .then(geom => {
                console.log(`[mvd-timing] map geometry fetch (async): ${(performance.now() - tGeom).toFixed(1)} ms`);
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
    // Schema v8: d.time is int32 ms; convert to seconds because the renderMap
    // fade compares against mapState.currentTime (seconds) and DEATH_X_DURATION
    // (seconds).
    mapState.dropEvents = (result.backpacks || []).map(d => ({
        t:      d.time * 0.001,
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

    // v7: bucketStates and stats are populated by analyzeInWorker —
    // the worker calls recomputeRegionControl with the default regions
    // after analyzeMVD and the result is stashed back onto the parsed
    // result before displayResults runs. Same code shape as v6.
    mapState.bucketStates      = rc.bucketStates || null;
    mapState.regionControlStats = rc.stats || null;
    mapState.teamA              = rc.teamA || null;
    mapState.teamB              = rc.teamB || null;
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

    // Render the table from the Go-supplied stats that
    // initRegionControlData stashed on mapState. User edits route through
    // applyRegionConfig, which calls the WASM bridge to refresh both.
    renderRegionControlFromGo(rc.regions, mapState.regionControlStats, mapState.teamA, mapState.teamB);
    mapState.renderDirty = true;

    if (panel) panel.style.display = '';
    if (statusPanel) statusPanel.style.display = '';
}

function buildRegionConfig(regions) {
    const container = document.getElementById('region-config');
    if (!container) return;
    container.innerHTML = '';

    const rowsWrap = document.createElement('div');
    rowsWrap.className = 'region-config-rows';
    container.appendChild(rowsWrap);

    for (const region of regions) {
        const locNames = [...new Set(region.points.map(p => p.name))].join(', ');
        rowsWrap.appendChild(buildRegionRow(region.name, locNames));
    }

    const actions = document.createElement('div');
    actions.className = 'region-config-actions';
    actions.innerHTML = `
        <button type="button" class="region-add-btn">+ Add region</button>
        <button type="button" class="region-save-btn" title="Download these region definitions as JSON">Save…</button>
        <button type="button" class="region-load-btn" title="Load region definitions from a JSON file">Load…</button>
        <input type="file" class="region-load-input" accept="application/json,.json" hidden>
    `;
    container.appendChild(actions);

    actions.querySelector('.region-add-btn').addEventListener('click', () => {
        const row = buildRegionRow('', '');
        rowsWrap.appendChild(row);
        const nameInput = row.querySelector('.region-name-input');
        if (nameInput) nameInput.focus();
    });
    actions.querySelector('.region-save-btn').addEventListener('click', saveRegionConfig);
    const loadInput = actions.querySelector('.region-load-input');
    actions.querySelector('.region-load-btn').addEventListener('click', () => loadInput.click());
    loadInput.addEventListener('change', (e) => {
        const file = e.target.files && e.target.files[0];
        if (file) loadRegionConfig(file);
        loadInput.value = '';
    });
}

function buildRegionRow(name, locNames) {
    const row = document.createElement('div');
    row.className = 'region-config-row';
    row.innerHTML = `
        <input type="text" class="region-name-input" placeholder="region name" value="${escapeHtml(name)}">
        <input type="text" class="region-locs-input" placeholder="loc1, loc2, …" value="${escapeHtml(locNames)}">
        <button type="button" class="region-remove-btn" title="Remove region">&times;</button>
    `;
    row.querySelector('.region-name-input').addEventListener('change', () => applyRegionConfig());
    row.querySelector('.region-locs-input').addEventListener('change', () => applyRegionConfig());
    row.querySelector('.region-remove-btn').addEventListener('click', () => {
        row.remove();
        applyRegionConfig();
    });
    return row;
}

function readRegionConfigFromUI() {
    // Walk the per-row inputs in DOM order so the user's region order
    // is preserved when serialised. Empty rows (no name or no locs)
    // are dropped silently — they're transient editing state.
    const out = [];
    document.querySelectorAll('#region-config .region-config-row').forEach(row => {
        const name = (row.querySelector('.region-name-input')?.value || '').trim();
        const locsRaw = row.querySelector('.region-locs-input')?.value || '';
        const locs = locsRaw.split(',').map(s => s.trim()).filter(s => s);
        if (!name || locs.length === 0) return;
        out.push({ name, locs });
    });
    return out;
}

async function applyRegionConfig() {
    const rc = mapState.rcResult;
    if (!rc) return;

    // Read current region definitions from the per-row inputs
    const regions = [];
    const seenNames = new Set();
    for (const def of readRegionConfigFromUI()) {
        // Drop dupes — first occurrence wins so editing a row above
        // doesn't get masked by a stale row below sharing the same name.
        if (seenNames.has(def.name)) continue;
        seenNames.add(def.name);

        // Find matching locations from the full loc list. Match is
        // case-insensitive so a hand-edited "ya" still claims the
        // canonical "YA" loc — saves the user having to memorize the
        // exact post-substitution casing.
        const locSet = new Set(def.locs.map(s => s.toLowerCase()));
        const points = [];
        let sumX = 0, sumY = 0;
        for (const loc of mapState.locations) {
            if (locSet.has(loc.name.toLowerCase())) {
                points.push({ x: loc.x, y: loc.y, z: loc.z, name: loc.name });
                sumX += loc.x;
                sumY += loc.y;
            }
        }
        if (points.length > 0) {
            regions.push({
                name: def.name,
                points: points,
                centroidX: sumX / points.length,
                centroidY: sumY / points.length,
            });
        }
    }

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

    // Recompute control stats via the WASM bridge (view.RegionControl).
    // Built and shipped together with this JS, so the export is always
    // available.
    const overrideJSON = JSON.stringify({
        regions: regions.map(r => ({
            name: r.name,
            locs: r.points.map(p => p.name),
        })),
    });
    let res;
    try {
        const jsonStr = await recomputeInWorker(overrideJSON);
        res = JSON.parse(jsonStr);
    } catch (err) {
        console.warn('recomputeRegionControl:', err.message);
        return;
    }
    if (res.error) {
        console.warn('recomputeRegionControl:', res.error);
        return;
    }
    mapState.bucketStates       = res.bucketStates || null;
    mapState.regionControlStats = res.stats || null;
    mapState.teamA              = res.teamA || mapState.teamA;
    mapState.teamB              = res.teamB || mapState.teamB;
    renderRegionControlFromGo(regions, res.stats, res.teamA, res.teamB);

    // Force map redraw
    mapState.renderDirty = true;
    renderMap(mapState.currentTime);

    // Re-render timeline region control graph
    updateDetailView();
}

// renderRegionControlFromGo decorates Go-supplied per-region stats with
// teamA/teamB strings (the Go shape carries those at the parent level, not on
// each region), stashes them on mapState, and (re)draws the region-control
// matrix — the same canvas UX element as the loc heatmap.
function renderRegionControlFromGo(regions, stats, teamA, teamB) {
    if (!stats) return;
    const decorated = {};
    for (const r of regions) {
        const s = stats[r.name];
        if (!s) continue;
        decorated[r.name] = Object.assign({}, s, { teamA, teamB });
    }
    mapState.controlRegions = regions;
    mapState.controlStats = decorated;
    renderRegionHeatmap();
}

// Map name → safe filename stem for the Save button. Falls back to
// "regions" when no map info is available.
function regionConfigFilename() {
    const raw = currentResult?.demoInfo?.map || currentResult?.match?.map || '';
    let base = String(raw).toLowerCase();
    const slash = base.lastIndexOf('/');
    if (slash >= 0) base = base.slice(slash + 1);
    if (base.endsWith('.bsp')) base = base.slice(0, -4);
    base = base.replace(/[^a-z0-9_-]/g, '');
    return (base || 'regions') + '.json';
}

function saveRegionConfig() {
    const regions = readRegionConfigFromUI();
    const json = JSON.stringify({ regions }, null, 4) + '\n';
    const blob = new Blob([json], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = regionConfigFilename();
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
}

function loadRegionConfig(file) {
    const reader = new FileReader();
    reader.onload = () => {
        let parsed;
        try {
            parsed = JSON.parse(reader.result);
        } catch (err) {
            alert('Failed to parse region JSON: ' + err.message);
            return;
        }
        const regions = Array.isArray(parsed?.regions) ? parsed.regions : null;
        if (!regions) {
            alert('Region JSON must have a top-level "regions" array.');
            return;
        }
        // Rebuild the row UI from the loaded definitions, then re-apply
        // so map render / stats / timeline pick up the change.
        const container = document.getElementById('region-config');
        if (!container) return;
        const rowsWrap = container.querySelector('.region-config-rows');
        if (!rowsWrap) return;
        rowsWrap.innerHTML = '';
        for (const r of regions) {
            const name = String(r.name || '').trim();
            const locs = Array.isArray(r.locs) ? r.locs.map(s => String(s)) : [];
            if (!name) continue;
            rowsWrap.appendChild(buildRegionRow(name, locs.join(', ')));
        }
        applyRegionConfig();
    };
    reader.onerror = () => alert('Failed to read file.');
    reader.readAsText(file);
}

// Look up region control state at a given time by indexing into the
// Go-supplied bucketStates strings (view.RegionControl).
function getRegionControlAtTime(time) {
    if (!mapState.controlRegions || !mapState.bucketStates) return null;
    const idx = findHighResBucketIndexAtTime(time);
    if (idx < 0) return null;
    const states = {};
    for (const region of mapState.controlRegions) {
        const s = mapState.bucketStates[region.name];
        if (typeof s !== 'string' || idx >= s.length) continue;
        states[region.name] = decodeRegionStateChar(s[idx]);
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

    // From the bucket view — position bounds. Walk each player's dense x/y
    // columns over their active span; padding/dead slots read (0,0) and are
    // skipped (matching the old row shape that omitted them).
    const view = timelineState.bucketView;
    if (view && view.players) {
        for (const name in view.players) {
            const cp = view.players[name];
            const xs = cp.x, ys = cp.y;
            if (!xs || !ys) continue;
            for (let rel = 0; rel < cp.n; rel++) {
                if (!cp.alive[rel]) continue;
                const x = xs[rel], y = ys[rel];
                if (x !== 0 || y !== 0) {
                    if (x < minX) minX = x;
                    if (x > maxX) maxX = x;
                    if (y < minY) minY = y;
                    if (y > maxY) maxY = y;
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
        table.innerHTML = `<thead><tr><th></th><th>Player</th><th>Loc</th><th title="Health + armor (sorted by H+A)">Stack</th><th>Wpn</th><th>View</th></tr></thead>`;
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
                    <td class="map-legend-stack" data-player="${escapedName}" data-sort-value="0">-</td>
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

    const stackCells = legend.querySelectorAll('.map-legend-stack');
    for (const cell of stackCells) {
        const name = cell.dataset.player;
        const data = playerData?.[name];
        if (!data) {
            cell.textContent = '-';
            cell.dataset.sortValue = '0';
            continue;
        }
        const h = data.h ?? data.health ?? 0;
        const a = data.a ?? data.armor ?? 0;
        const at = data.at ?? data.armorType ?? '';
        cell.dataset.sortValue = String(h + a);
        if (h <= 0) {
            cell.textContent = '-';
        } else if (a > 0 && at) {
            cell.innerHTML = `<span class="stack-h">${h}</span> <span class="armor-${at}">${a} ${at.toUpperCase()}</span>`;
        } else if (a > 0) {
            cell.innerHTML = `<span class="stack-h">${h}</span> <span>${a}</span>`;
        } else {
            cell.innerHTML = `<span class="stack-h">${h}</span>`;
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
    const view = timelineState.bucketView;
    if (!view || !view.players) return;

    const MAX_MOVE_PER_BUCKET = 2500 * (timelineState.highResDuration || 0.05);
    // "Meaningful movement" threshold — 2 canvas pixels at the base fit-to-canvas
    // scale, translated to world units so the filter is applied in world space.
    const MIN_MOVE_WORLD = _wtc.scale > 0 ? (2 / _wtc.scale) : 0;

    // Walk each player's dense columns over their active span. Dead buckets
    // (alive=0) are skipped, which breaks the trail across death→respawn just
    // as the old row shape did by omitting the player from those buckets.
    for (const name in view.players) {
        const cp = view.players[name];
        const symbolInfo = mapState.playerSymbols[name];
        if (!symbolInfo) continue;
        const xs = cp.x, ys = cp.y, ds = cp.d, sps = cp.sp;
        if (!xs || !ys) continue;

        let lastWorld = null;
        for (let rel = 0; rel < cp.n; rel++) {
            if (!cp.alive[rel]) continue;
            const x = xs[rel], y = ys[rel];
            if (x === 0 && y === 0) continue;

            const i = cp.first + rel;
            const t = bucketTimeSec(view, i);
            const isDeath = ds ? !!ds[rel] : false;
            const isSpawn = sps ? !!sps[rel] : false;

            if (!mapState.fullTrails[name]) mapState.fullTrails[name] = [];
            const track = mapState.fullTrails[name];
            const last = track[track.length - 1];

            // Death frames also get added to the standalone deathEvents list
            // so renderMap can find them without scanning every player trail.
            // teamIdx is captured so the X is painted in the dead player's
            // own team color rather than a generic red.
            if (isDeath) {
                mapState.deathEvents.push({ t, wx: x, wy: y, teamIdx: symbolInfo.teamIdx });
            }

            // Always include death/spawn markers regardless of distance.
            if (!isDeath && !isSpawn) {
                if (last && Math.abs(last.wx - x) <= MIN_MOVE_WORLD && Math.abs(last.wy - y) <= MIN_MOVE_WORLD) {
                    lastWorld = { x, y };
                    continue;
                }
            }

            // Teleport detection in world units (scale-independent)
            const isTeleport = !isDeath && !isSpawn && lastWorld && (Math.abs(x - lastWorld.x) > MAX_MOVE_PER_BUCKET || Math.abs(y - lastWorld.y) > MAX_MOVE_PER_BUCKET);

            lastWorld = { x, y };
            track.push({ wx: x, wy: y, t, teamIdx: symbolInfo.teamIdx, tp: isTeleport, death: isDeath, spawn: isSpawn });
        }
    }

    // deathEvents was filled per-player; renderMap expects it time-ordered.
    mapState.deathEvents.sort((a, b) => a.t - b.t);

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

    // Learn-map mode: static entity study view — keep the floor/loc base,
    // draw the designed entity layout, and skip all player/time-based layers.
    if (mapState.learnMode) {
        drawMapEntities(ctx);
        return;
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
//
// `time` is in seconds (mapState.currentTime). Schema v8: item.phases[]
// .availableFrom / .takenAt / .respawnAt are int32 ms — promote `time`
// to ms once here so all comparisons happen in the phase's native unit.
function isItemUp(item, time) {
    const phases = item.phases;
    if (!phases || phases.length === 0) return true;
    const timeMs = time * 1000;
    for (let i = 0; i < phases.length; i++) {
        const p = phases[i];
        if (p.availableFrom > timeMs) break;
        const takenAt = p.takenAt || 0;
        if (takenAt === 0) return true; // phase open, not yet taken (this phase is current → up)
        if (timeMs < takenAt) return true; // available window
        // taken at takenAt; respawnAt may be 0 (MH pending) or a future/past value
        const respawnAt = p.respawnAt || 0;
        if (respawnAt > 0 && timeMs >= respawnAt) {
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
//
// `time` is in seconds. Schema v8: phase fields are ms — promote `time`
// to ms for comparisons, then convert the result back to seconds before
// returning.
function itemStatus(item, time) {
    const phases = item.phases;
    if (!phases || phases.length === 0) {
        return { up: true, secsToRespawn: null, pending: false };
    }
    const timeMs = time * 1000;
    // Find the phase whose window contains `time` (availableFrom <= timeMs < nextAvailableFrom).
    let activePhase = null;
    for (let i = 0; i < phases.length; i++) {
        const p = phases[i];
        const next = phases[i + 1];
        if (p.availableFrom <= timeMs && (!next || next.availableFrom > timeMs)) {
            activePhase = p;
            break;
        }
    }
    if (!activePhase) {
        // Before the first phase opens — treat as up.
        return { up: true, secsToRespawn: null, pending: false };
    }
    const takenAt = activePhase.takenAt || 0;
    if (takenAt === 0 || timeMs < takenAt) {
        return { up: true, secsToRespawn: null, pending: false };
    }
    const respawnAt = activePhase.respawnAt || 0;
    if (respawnAt === 0) {
        // Held with pending respawn (MH during rot).
        return { up: false, secsToRespawn: null, pending: true };
    }
    if (timeMs >= respawnAt) {
        return { up: true, secsToRespawn: null, pending: false };
    }
    return { up: false, secsToRespawn: (respawnAt - timeMs) * 0.001, pending: false };
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

// ─── Learn-map entity overlay ───────────────────────────────────────────────
//
// Static study view (mapState.learnMode): draws result.mapEntities — the
// map's designed item spawns, spawnpoints, teleporters and buttons — instead
// of players, with per-category toggles (mapState.entityFilters) and arrows
// linking each teleport entrance to its exit. Reuses worldToCanvas so markers
// land exactly where players do.

// Item kind → filter category.
const ITEM_KIND_CATEGORY = {
    rl: 'weapon', lg: 'weapon', gl: 'weapon', ssg: 'weapon', sng: 'weapon', ng: 'weapon',
    ra: 'armor', ya: 'armor', ga: 'armor',
    mh: 'health', h25: 'health', h15: 'health',
    shells: 'ammo', nails: 'ammo', rockets: 'ammo', cells: 'ammo',
    quad: 'powerup', pent: 'powerup', ring: 'powerup', suit: 'powerup',
};

// Item markers reuse the playback palette plus the kinds it omits (ammo,
// small health, suit). Structural entities get their own glyphs.
const LEARN_ITEM_STYLES = Object.assign({}, ITEM_MARKER_STYLES, {
    h25:     { fill: '#000', outline: 'rgb(0, 200, 83)',  label: 'H'  },
    h15:     { fill: '#000', outline: '#6f8f6f',          label: 'h'  },
    shells:  { fill: '#000', outline: '#b0a070',          label: 'sh' },
    nails:   { fill: '#000', outline: '#8090a0',          label: 'nl' },
    rockets: { fill: '#000', outline: 'rgb(255,107,107)', label: 'rk' },
    cells:   { fill: '#000', outline: 'rgb(0,217,255)',   label: 'cl' },
    suit:    { fill: '#000', outline: '#00e676',          label: 'ES' },
});

const TELEPORT_COLOR = '#b388ff';

const STRUCTURAL_STYLES = {
    spawn:       { fill: '#15151f',       outline: '#888',         label: 'S' },
    teleportSrc: { fill: '#1a0a2a',       outline: TELEPORT_COLOR, label: 'T' },
    teleportDst: { fill: TELEPORT_COLOR,  outline: TELEPORT_COLOR, label: '', circle: true },
    button:      { fill: '#000',          outline: '#ff9800',      label: 'B' },
    door:        { fill: '#000',          outline: '#a1887f',      label: 'D' },
};

function entityCategory(e) {
    if (e.type === 'item') return ITEM_KIND_CATEGORY[e.kind] || 'item';
    if (e.type === 'teleportSrc' || e.type === 'teleportDst') return 'teleporter';
    return e.type; // 'spawn' | 'button' | 'door'
}

// buildTeleportArrows pairs each entrance (teleportSrc.target) with its exit
// (teleportDst.targetName), storing world-coord endpoints for the arrows.
function buildTeleportArrows() {
    mapState.teleportArrows = [];
    const dstByName = {};
    for (const e of mapState.mapEntities) {
        if (e.type === 'teleportDst' && e.targetName) dstByName[e.targetName] = e;
    }
    for (const e of mapState.mapEntities) {
        if (e.type !== 'teleportSrc' || !e.target) continue;
        const dst = dstByName[e.target];
        if (!dst) continue;
        mapState.teleportArrows.push({ sx: e.x, sy: e.y, dx: dst.x, dy: dst.y });
    }
}

function drawMapEntities(ctx) {
    const entities = mapState.mapEntities;
    if (!entities || entities.length === 0) return;
    const f = mapState.entityFilters;
    const iconScale = mapIconScale();
    const size = ITEM_MARKER_SIZE * iconScale;
    const half = size / 2;
    const fontPx = Math.round(10 * iconScale);

    // Connection arrows first, beneath the markers.
    if (f.teleporter && mapState.teleportArrows.length > 0) {
        ctx.save();
        ctx.strokeStyle = TELEPORT_COLOR;
        ctx.fillStyle = TELEPORT_COLOR;
        ctx.globalAlpha = 0.55;
        ctx.lineWidth = Math.max(1, 1.5 * iconScale);
        for (const a of mapState.teleportArrows) {
            const s = worldToCanvasNew(a.sx, a.sy);
            const d = worldToCanvasNew(a.dx, a.dy);
            drawArrow(ctx, s.x, s.y, d.x, d.y, 8 * iconScale);
        }
        ctx.restore();
    }

    ctx.save();
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    // Lower decks first so higher floors draw on top.
    const sorted = entities.slice().sort((a, b) => (a.z || 0) - (b.z || 0));
    for (const e of sorted) {
        if (!f[entityCategory(e)]) continue;
        const style = e.type === 'item' ? LEARN_ITEM_STYLES[e.kind] : STRUCTURAL_STYLES[e.type];
        if (style) drawEntityMarker(ctx, e, style, size, half, fontPx);
    }
    ctx.restore();
}

function drawEntityMarker(ctx, e, style, size, half, fontPx) {
    const pos = worldToCanvas(e.x, e.y);
    if (style.circle) {
        ctx.beginPath();
        ctx.arc(pos.x, pos.y, half, 0, Math.PI * 2);
        ctx.fillStyle = style.fill;
        ctx.fill();
        if (style.outline) { ctx.strokeStyle = style.outline; ctx.lineWidth = 1.5; ctx.stroke(); }
    } else {
        const x = Math.round(pos.x - half);
        const y = Math.round(pos.y - half);
        ctx.fillStyle = style.fill;
        ctx.fillRect(x, y, size, size);
        if (style.outline) {
            ctx.strokeStyle = style.outline;
            ctx.lineWidth = 1.5;
            ctx.strokeRect(x + 0.5, y + 0.5, size - 1, size - 1);
        }
    }
    if (style.label) {
        ctx.font = `bold ${fontPx}px -apple-system, BlinkMacSystemFont, sans-serif`;
        ctx.fillStyle = style.textColor || style.outline || '#fff';
        ctx.fillText(style.label, pos.x, pos.y + 1);
    }
}

// drawArrow draws a line with an arrowhead at the (x2,y2) end.
function drawArrow(ctx, x1, y1, x2, y2, headLen) {
    const ang = Math.atan2(y2 - y1, x2 - x1);
    ctx.beginPath();
    ctx.moveTo(x1, y1);
    ctx.lineTo(x2, y2);
    ctx.stroke();
    ctx.beginPath();
    ctx.moveTo(x2, y2);
    ctx.lineTo(x2 - headLen * Math.cos(ang - Math.PI / 6), y2 - headLen * Math.sin(ang - Math.PI / 6));
    ctx.lineTo(x2 - headLen * Math.cos(ang + Math.PI / 6), y2 - headLen * Math.sin(ang + Math.PI / 6));
    ctx.closePath();
    ctx.fill();
}

// setLearnMode swaps the map between live playback and the static
// entity-study view, swapping the sidebar panels accordingly.
function setLearnMode(on) {
    if (on === mapState.learnMode) return;
    mapState.learnMode = on;
    const swapIds = ['map-legend', 'map-items-panel', 'region-status-panel'];
    const entPanel = document.getElementById('map-entities-panel');
    if (on) {
        mapState._preLearnDisplay = {};
        for (const id of swapIds) {
            const el = document.getElementById(id);
            if (el) { mapState._preLearnDisplay[id] = el.style.display; el.style.display = 'none'; }
        }
        if (entPanel) entPanel.style.display = '';
    } else {
        const prev = mapState._preLearnDisplay || {};
        for (const id of swapIds) {
            const el = document.getElementById(id);
            if (el) el.style.display = prev[id] || '';
        }
        if (entPanel) entPanel.style.display = 'none';
    }
    const btn = document.getElementById('map-learn-toggle');
    if (btn) { btn.classList.toggle('active', on); btn.textContent = on ? 'Exit learn' : 'Learn map'; }
    const tableWrap = document.getElementById('map-entities-table-wrap');
    if (tableWrap) tableWrap.style.display = on ? '' : 'none';
    if (on) buildEntityTable();
    mapState.renderDirty = true;
    renderMap(mapState.currentTime);
    updateUrlState(); // reflect ?learn=1 for deep-linking
}

// Cleartext class labels for the entity table.
const ENTITY_CLASS_LABEL = {
    weapon: 'Weapon', armor: 'Armor', health: 'Health', ammo: 'Ammo',
    powerup: 'Powerup', teleporter: 'Teleporter', spawn: 'Spawn',
    button: 'Button', door: 'Door',
};

// buildEntityTable fills the below-map table with every visible entity
// (respecting the category filters). Teleporters are collapsed to one row
// per entrance→exit pair: Loc is the exit, Source is the entrance.
function buildEntityTable() {
    const tbody = document.getElementById('map-entities-table-body');
    if (!tbody) return;
    const f = mapState.entityFilters;
    const ents = mapState.mapEntities || [];
    const rows = [];

    for (const e of ents) {
        if (e.type === 'teleportSrc' || e.type === 'teleportDst') continue;
        const cat = entityCategory(e);
        if (!f[cat]) continue;
        rows.push({
            cls: ENTITY_CLASS_LABEL[cat] || cat,
            type: e.type === 'item' ? (e.kind || '') : '',
            name: e.name || '', loc: e.loc || '', source: '',
        });
    }

    if (f.teleporter) {
        const dstByName = {};
        for (const e of ents) {
            if (e.type === 'teleportDst' && e.targetName) dstByName[e.targetName] = e;
        }
        const pairedDst = new Set();
        for (const e of ents) {
            if (e.type !== 'teleportSrc') continue;
            const dst = e.target ? dstByName[e.target] : null;
            if (dst) pairedDst.add(dst);
            // Loc = entrance (where the trigger sits); Source = exit it leads to.
            rows.push({
                cls: 'Teleporter', type: '', name: e.name || '',
                loc: e.loc || '', source: dst ? (dst.loc || '') : '',
            });
        }
        for (const e of ents) { // exits with no matching entrance
            if (e.type === 'teleportDst' && !pairedDst.has(e)) {
                rows.push({ cls: 'Teleporter', type: '', name: e.name || '', loc: '', source: e.loc || '' });
            }
        }
    }

    rows.sort((a, b) =>
        a.cls.localeCompare(b.cls) || a.type.localeCompare(b.type) || a.name.localeCompare(b.name));

    tbody.innerHTML = rows.map(r =>
        `<tr><td>${escapeHtml(r.cls)}</td><td>${escapeHtml(r.type)}</td>` +
        `<td>${escapeHtml(r.name)}</td><td>${escapeHtml(r.loc)}</td>` +
        `<td>${escapeHtml(r.source)}</td></tr>`
    ).join('');
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
            swatch.className = 'item-swatch-cell';
            swatch.appendChild(buildItemSwatch(style));
            const loc = document.createElement('td');
            loc.className = 'item-loc';
            loc.textContent = item.loc || '';
            const status = document.createElement('td');
            status.className = 'item-status';
            tr.appendChild(swatch);
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

// Reconstruct the row-shape bucket ({t, p, td}) at `time` from the columnar
// view. Memoised on (view, index): repeated calls at the same time return the
// same object so renderMap's `bucket === lastRenderedBucket` redraw-skip and
// the legend/region/hit-test callers all share one reconstruction per frame.
let _bucketReconCache = { view: null, i: -1, bucket: null };
function findHighResBucketAtTime(time) {
    const view = timelineState.bucketView;
    if (!view || !view.count) return null;
    const i = bucketIndexAtTime(view, time);
    if (i < 0) return null;
    if (_bucketReconCache.view === view && _bucketReconCache.i === i) {
        return _bucketReconCache.bucket;
    }
    const bucket = {
        t: bucketTimeSec(view, i),
        idx: i,
        p: reconstructBucketPlayers(view, i),
        td: reconstructBucketTeams(view, i),
    };
    _bucketReconCache = { view, i, bucket };
    return bucket;
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

    const learnBtn = document.getElementById('map-learn-toggle');
    if (learnBtn) {
        learnBtn.addEventListener('click', () => setLearnMode(!mapState.learnMode));
    }
    document.querySelectorAll('.map-entity-cb').forEach(cb => {
        cb.addEventListener('change', (e) => {
            mapState.entityFilters[e.target.dataset.cat] = e.target.checked;
            if (mapState.learnMode) buildEntityTable();
            mapState.renderDirty = true;
            renderMap(mapState.currentTime);
        });
    });

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
            // Pin to the top of the fullscreen panel so the visual order is
            // timeline → canvas → controls (controls live at the bottom of
            // .map-panel via the HTML layout).
            panel.insertBefore(tl, panel.firstChild);
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
        renderLocHeatmap();
        return;
    }
    if (noData) noData.style.display = 'none';

    populateLocGraphFilter(result);
    populateLocMetricOptions();

    if (!locGraphState.initialized) {
        setupLocGraphControls();
        locGraphState.initialized = true;
    }

    buildOrRefreshCytoscape();
    renderLocHeatmap();
}

// Show only the loc-analysis metrics this demo actually has data for —
// "With Quad"/"With Pent" disappear on maps/modes where nobody held that
// powerup (e.g. quad in most 1v1s), so the user can't land on an empty
// graph + table. A node carries the conditioned sub-object only when some
// sample met the condition (omitempty), so presence == availability. If the
// current selection becomes unavailable, fall back to "Full time".
function populateLocMetricOptions() {
    const sel = document.getElementById('locgraph-metric');
    if (!sel) return;
    const avail = { all: true, armed: false, unarmed: false, quad: false, pent: false };
    const locs = (locGraphState.graph && locGraphState.graph.locs) || [];
    for (const n of locs) {
        if (n.armed) avail.armed = true;
        if (n.unarmed) avail.unarmed = true;
        if (n.quad) avail.quad = true;
        if (n.pent) avail.pent = true;
        if (avail.armed && avail.unarmed && avail.quad && avail.pent) break;
    }
    let currentOk = false;
    for (const opt of sel.options) {
        const ok = !!avail[opt.value];
        opt.hidden = !ok;
        opt.disabled = !ok;
        if (ok && opt.value === sel.value) currentOk = true;
    }
    if (!currentOk) sel.value = 'all';
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
    // Metric (all / armed / quad) reweights the graph nodes and the heatmap.
    on('locgraph-metric', 'change', () => { buildOrRefreshCytoscape(); renderLocHeatmap(); });
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

// 'all' | 'armed' | 'quad' — which time breakdown to weight by.
function getLocMetric() {
    const sel = document.getElementById('locgraph-metric');
    return sel ? sel.value : 'all';
}

// Resolve a node's weight bundle for the chosen metric. 'all' is the node's
// own Total/ByPlayer/ByTeam; 'armed'/'quad' read the optional sub-objects
// (absent when no sample met the condition).
function metricWeightsOf(node, metric) {
    if (metric === 'armed') return node.armed || EMPTY_WEIGHTS;
    if (metric === 'unarmed') return node.unarmed || EMPTY_WEIGHTS;
    if (metric === 'quad') return node.quad || EMPTY_WEIGHTS;
    if (metric === 'pent') return node.pent || EMPTY_WEIGHTS;
    return node;
}
const EMPTY_WEIGHTS = { total: 0, byPlayer: {}, byTeam: {} };

function nodeWeightFor(node, filter, metric) {
    const w = metricWeightsOf(node, metric);
    if (filter.kind === 'player') return w.byPlayer?.[filter.key] || 0;
    if (filter.kind === 'team') return w.byTeam?.[filter.key] || 0;
    return w.total || 0;
}

// Edges carry the same metric conditioning as nodes, so each metric is a
// self-contained graph (its own nodes + edges).
function metricEdgeWeightsOf(edge, metric) {
    if (metric === 'armed') return edge.armed || EMPTY_WEIGHTS;
    if (metric === 'unarmed') return edge.unarmed || EMPTY_WEIGHTS;
    if (metric === 'quad') return edge.quad || EMPTY_WEIGHTS;
    if (metric === 'pent') return edge.pent || EMPTY_WEIGHTS;
    return edge;
}

function edgeWeightFor(edge, filter, metric) {
    const w = metricEdgeWeightsOf(edge, metric);
    if (filter.kind === 'player') return w.byPlayer?.[filter.key] || 0;
    if (filter.kind === 'team') return w.byTeam?.[filter.key] || 0;
    return w.total || 0;
}

// Build Cytoscape elements from the current graph + filter + edge-mode.
// Nodes/edges carry their filtered weight in data so styles can reference it
// directly instead of re-computing in the mapper.
function buildCytoscapeElements() {
    const { graph } = locGraphState;
    if (!graph) return [];
    const filter = getLocGraphFilter();
    const metric = getLocMetric();
    const edgeModeSel = document.getElementById('locgraph-edge-mode');
    const edgeMode = edgeModeSel ? edgeModeSel.value : 'all';
    const minEdgeSel = document.getElementById('locgraph-min-edge');
    const minEdge = minEdgeSel ? (parseInt(minEdgeSel.value, 10) || 1) : 1;

    let maxNodeWeight = 0;
    for (const n of graph.locs) {
        const w = nodeWeightFor(n, filter, metric);
        if (w > maxNodeWeight) maxNodeWeight = w;
    }
    let maxEdgeWeight = 0;
    for (const e of graph.edges) {
        if (edgeMode === 'normal' && e.kind !== 'normal') continue;
        if (edgeMode === 'teleport' && e.kind !== 'teleport') continue;
        const w = edgeWeightFor(e, filter, metric);
        if (w < minEdge) continue;
        if (w > maxEdgeWeight) maxEdgeWeight = w;
    }

    const elements = [];

    for (const n of graph.locs) {
        const w = nodeWeightFor(n, filter, metric);
        const norm = maxNodeWeight > 0 ? w / maxNodeWeight : 0;
        const mw = metricWeightsOf(n, metric);
        elements.push({
            group: 'nodes',
            data: {
                id: 'n:' + n.name,
                name: n.name,
                weight: w,
                weightNorm: norm,
                total: mw.total || 0,
                byPlayer: mw.byPlayer || {},
                byTeam: mw.byTeam || {},
                // Grey out nodes with zero contribution once a filter or a
                // non-default metric is active, so each conditioned graph
                // reads clearly while map context is preserved.
                dim: w === 0 && (filter.kind !== 'all' || metric !== 'all')
            },
            // Preset/geographic layout reads world coords directly from data.
            // Invert Y so "up" on screen matches "up" in map (QW Y is up).
            position: { x: n.x || 0, y: -(n.y || 0) }
        });
    }

    for (const e of graph.edges) {
        if (edgeMode === 'normal' && e.kind !== 'normal') continue;
        if (edgeMode === 'teleport' && e.kind !== 'teleport') continue;
        const w = edgeWeightFor(e, filter, metric);
        if (w === 0) continue; // Prune edges absent from this filter+metric subgraph
        if (w < minEdge) continue; // UI minimum-edge-count filter
        const norm = maxEdgeWeight > 0 ? w / maxEdgeWeight : 0;
        const ew = metricEdgeWeightsOf(e, metric);
        elements.push({
            group: 'edges',
            data: {
                id: 'e:' + e.from + '->' + e.to,
                source: 'n:' + e.from,
                target: 'n:' + e.to,
                weight: w,
                weightNorm: norm,
                kind: e.kind,
                total: ew.total || 0,
                byPlayer: ew.byPlayer || {},
                byTeam: ew.byTeam || {}
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
    // The heatmap / region-control tables are plain HTML built once at load
    // (initLocGraphView, renderRegionControlFromGo) — unlike the canvas they
    // don't need a re-render when the tab is revealed, and skipping it keeps
    // the user's column sort intact across tab switches.
}

// ─── Loc Heatmap + Region Control matrices ───────────────────────────────────
//
// Both live in the loc-graph tab and share one renderer (renderHeatmapTable):
// a sortable .stats-table — rows on the y-axis, one column per series, each
// cell viridis-shaded by a precomputed intensity with the % printed in it.
// Using a real table (vs. a canvas) gives crisp text and free column sorting
// via makeSortable, and reuses the common renderTableRows tbody builder.
//
//   - Loc heatmap: rows = locs (busiest first); columns are the team
//     aggregates (every member's time combined) then one per player grouped by
//     team. Intensity = each column's share of its time in the loc, normalised
//     PER COLUMN to its own busiest loc (sqrt-curved).
//   - Region control: rows = regions; columns are the seven control states.
//     Intensity = each state's share, normalised PER ROW to the region's
//     busiest control state (Empty excluded — see buildRegionHeatmap).
//
// Normalisation is baked into each cell's intensity by the build* functions, so
// the renderer is policy-free. Team identity rides on the canonical
// TEAM_COLORS-by-timelineState.teams mapping (see CLAUDE.md "Team colors") as a
// coloured underline on the relevant column headers. Data comes from
// result.locGraph + result.demoInfo (locs) and mapState.controlStats (regions)
// — no extra analyzer pass.

// Sequential colormap stops [t, [r,g,b]] — viridis. Chosen because it is
// perceptually uniform and safe for red/green colour-vision deficiency (no
// red↔green crossover; intensity reads monotonically in greyscale too). Kept
// in sync with the CSS gradient legend (.heatmap-legend-bar) in styles.css.
const HEAT_STOPS = [
    [0.00, [ 68,   1,  84]], // deep purple
    [0.25, [ 59,  82, 139]], // blue
    [0.50, [ 33, 145, 140]], // teal
    [0.75, [ 94, 201,  98]], // green
    [1.00, [253, 231,  37]], // yellow
];

function heatColorRGB(t) {
    t = Math.max(0, Math.min(1, t));
    for (let i = 1; i < HEAT_STOPS.length; i++) {
        if (t <= HEAT_STOPS[i][0]) {
            const [t0, c0] = HEAT_STOPS[i - 1];
            const [t1, c1] = HEAT_STOPS[i];
            const f = (t - t0) / (t1 - t0 || 1);
            return [
                Math.round(c0[0] + (c1[0] - c0[0]) * f),
                Math.round(c0[1] + (c1[1] - c0[1]) * f),
                Math.round(c0[2] + (c1[2] - c0[2]) * f),
            ];
        }
    }
    return HEAT_STOPS[HEAT_STOPS.length - 1][1].slice();
}

// Readable text color over an [r,g,b] fill — dark ink on light cells, white
// on dark ones (Rec. 601 luma).
function contrastInk(rgb) {
    const lum = 0.299 * rgb[0] + 0.587 * rgb[1] + 0.114 * rgb[2];
    return lum > 150 ? '#10131f' : '#f0f0f0';
}

// Canonical team order: TEAM_COLORS is indexed by position in
// timelineState.teams (frag-sorted, set in displayResults) so the header
// underlines match the rest of the app. Fall back to demoInfo order before
// that runs.
function canonicalTeams(result) {
    if (timelineState.teams && timelineState.teams.length) return [...timelineState.teams];
    const dt = result.demoInfo && result.demoInfo.teams;
    return (dt && dt.length) ? [...dt] : [''];
}

// Short display label for a long player name; the full name is kept on the
// column header's title attribute. (QuakeWorld's in-game short name comes from
// the client-side `cl_fakename` cvar, which is only ever injected as a text
// prefix on say_team messages — never carried in userinfo — so there is no
// per-player short name in the demo stream to read.)
function shortName(name, n = 9) {
    name = String(name || '');
    return name.length > n ? name.slice(0, n - 1) + '…' : name;
}

// ── Shared matrix model ──────────────────────────────────────────────────────
//
// Both build* functions return the same shape, rendered by renderHeatmapTable:
//
//   {
//     rows:    [{ name, cells: [{ i, p }, ...] }]    // i: 0..1 intensity, p: %|null
//     columns: [{ label, full, team, teamIdx, ... }] // teamIdx<0 = no underline
//     teamCols,                                       // separator before col teamCols
//     cellTitle(col, row, ci) -> string               // <td> title text
//   }
//
// Normalisation (per-column for locs, per-row for regions) is baked into each
// cell's intensity `i` by the build* functions, so the renderer is policy-free.

// Build the loc matrix for `metric` ('all' | 'armed' | 'quad'): leading
// team-aggregate columns then one per player grouped by team; rows are locs
// (busiest first by the metric). Intensity is each column's share normalised to
// its own busiest loc (sqrt-curved to lift small shares). Returns null when
// there's nothing to show (e.g. nobody ever held a quad).
function buildLocHeatmap(result, metric) {
    const graph = result && result.locGraph;
    if (!graph || !graph.locs || graph.locs.length === 0) return null;
    const players = (result.demoInfo && result.demoInfo.players) || [];
    const teams = canonicalTeams(result);
    const teamIdxOf = (team) => {
        const i = teams.indexOf(team);
        return i >= 0 ? i : teams.length; // unknown teams share the trailing color
    };

    // Weights for the chosen metric (node itself for 'all', the armed/quad
    // sub-object otherwise) drive every time read below.
    const W = (n) => metricWeightsOf(n, metric);

    const playerTotal = new Map();
    for (const node of graph.locs) {
        for (const [p, t] of Object.entries(W(node).byPlayer || {})) {
            playerTotal.set(p, (playerTotal.get(p) || 0) + t);
        }
    }

    const baseLocs = graph.locs
        .filter(n => W(n).byPlayer && Object.keys(W(n).byPlayer).length > 0)
        .slice()
        .sort((a, b) => (W(b).total || 0) - (W(a).total || 0));
    if (baseLocs.length === 0) return null;

    const isDuel = players.length > 0 && players.every(p => p.team === p.name);
    const hasTeams = !isDuel && teams.length >= 2 && !(teams.length === 1 && teams[0] === '');

    // Columns carry `members` so a cell value is uniformly sum(byPlayer[m]);
    // `kind` ('team' | 'player') drives the title wording. `label` is the short
    // header text, `full` the untruncated name kept on the header title.
    const columns = [];
    if (hasTeams) {
        for (const t of teams) {
            const members = players
                .filter(p => (p.team || '') === t && (playerTotal.get(p.name) || 0) > 0)
                .map(p => p.name);
            const total = members.reduce((s, p) => s + (playerTotal.get(p) || 0), 0);
            if (total > 0) columns.push({ kind: 'team', label: shortName(t), full: t, team: t, teamIdx: teamIdxOf(t), members, total });
        }
    }
    const teamCols = columns.length;

    const teamOfPlayer = new Map();
    for (const p of players) if (p.name) teamOfPlayer.set(p.name, p.team || '');
    const seen = new Set();
    for (const team of teams) {
        for (const p of players) {
            if (!p.name || seen.has(p.name)) continue;
            if ((p.team || '') !== team) continue;
            const total = playerTotal.get(p.name) || 0;
            if (total <= 0) continue;
            columns.push({ kind: 'player', label: shortName(p.name), full: p.name, team, teamIdx: teamIdxOf(team), members: [p.name], total });
            seen.add(p.name);
        }
    }
    for (const [p, total] of playerTotal) {
        if (seen.has(p) || total <= 0) continue;
        const team = teamOfPlayer.get(p) || '';
        columns.push({ kind: 'player', label: shortName(p), full: p, team, teamIdx: teamIdxOf(team), members: [p], total });
        seen.add(p);
    }
    if (columns.length === 0) return null;

    // Per-column max share → full intensity at each column's busiest loc.
    const secOf = (n, c) => { const bp = W(n).byPlayer || {}; return c.members.reduce((s, p) => s + (bp[p] || 0), 0); };
    const colMaxFrac = columns.map(c => {
        let m = 0;
        for (const n of baseLocs) {
            const f = c.total > 0 ? secOf(n, c) / c.total : 0;
            if (f > m) m = f;
        }
        return m || 1;
    });

    let rows = baseLocs.map(n => ({
        name: n.name,
        secs: columns.map(c => secOf(n, c)),
        cells: columns.map((c, ci) => {
            const sec = secOf(n, c);
            const share = c.total > 0 ? sec / c.total : 0;
            const norm = colMaxFrac[ci] > 0 ? share / colMaxFrac[ci] : 0;
            return { i: sec > 0 ? Math.sqrt(norm) : 0, p: sec > 0 ? share * 100 : null };
        }),
    }));
    rows = rows.filter(r => r.secs.some(v => v > 0));
    if (rows.length === 0) return null;

    return {
        rows, columns, teamCols,
        cellTitle: (col, row, ci) => {
            const sec = row.secs[ci] || 0;
            const pct = row.cells[ci].p != null ? row.cells[ci].p : 0;
            const suffix = col.kind === 'team' ? 'of team time' : 'of their time';
            const who = col.full + (col.kind === 'player' && col.team && col.team !== col.full ? ` (${col.team})` : '');
            return `${who} · ${row.name}: ${sec.toFixed(1)}s · ${pct.toFixed(1)}% ${suffix}`;
        },
    };
}

// Build the region-control matrix: rows are regions, columns are the seven
// control states. Colour is normalised per-row to the region's busiest control
// state (Empty excluded — it is filler, not a control state, and would swamp
// the scale), so a row uses the full colormap; the printed % stays absolute.
function buildRegionHeatmap(regions, stats) {
    if (!regions || !regions.length || !stats) return null;
    const first = Object.values(stats)[0];
    if (!first) return null;
    const teamA = first.teamA || 'Team A';
    const teamB = first.teamB || 'Team B';

    // Columns: teamA control/weak (red underline) | contested/cont.weak/empty
    // (neutral) | teamB weak/control (blue underline). `label` is the header
    // text, `full` the title; the team-named columns carry the team identity.
    const columns = [
        { label: teamA,        full: `${teamA} control`,      key: 'teamAControl',     team: teamA, teamIdx: 0 },
        { label: `${teamA} wk`, full: `${teamA} weak control`, key: 'teamAWeakControl', team: teamA, teamIdx: 0 },
        { label: 'Cont',       full: 'Contested',             key: 'contested',        team: '',    teamIdx: -1 },
        { label: 'Cont wk',    full: 'Contested (weak)',      key: 'weakContested',    team: '',    teamIdx: -1 },
        { label: 'Empty',      full: 'Empty (no players)',    key: 'empty',            team: '',    teamIdx: -1, noHeat: true },
        { label: `${teamB} wk`, full: `${teamB} weak control`, key: 'teamBWeakControl', team: teamB, teamIdx: 1 },
        { label: teamB,        full: `${teamB} control`,      key: 'teamBControl',     team: teamB, teamIdx: 1 },
    ];

    const rows = [];
    for (const region of regions) {
        const s = stats[region.name];
        if (!s) continue;
        const rowMax = Math.max(
            s.teamAControl, s.teamAWeakControl, s.contested,
            s.weakContested, s.teamBWeakControl, s.teamBControl) || 1;
        rows.push({
            name: region.name,
            cells: columns.map(c => {
                const v = s[c.key] || 0;
                const i = c.noHeat ? 0 : (rowMax > 0 ? v / rowMax : 0);
                return { i, p: v };
            }),
        });
    }
    if (rows.length === 0) return null;

    return {
        rows, columns, teamCols: 0,
        cellTitle: (col, row, ci) => `${row.name} · ${col.full}: ${row.cells[ci].p}% of match`,
    };
}

function renderLocHeatmap() {
    const panel = document.getElementById('locheatmap-panel');
    if (!panel) return;
    const metric = getLocMetric();
    const data = locGraphState.result ? buildLocHeatmap(locGraphState.result, metric) : null;
    const noData = document.getElementById('locheatmap-no-data');
    if (!data) {
        // Clear any stale table from a previous metric so the empty-state
        // isn't shown alongside an outdated grid.
        setHTML('locheatmap-thead-row', '');
        setHTML('locheatmap-body', '');
        const hasGraph = !!(locGraphState.graph && locGraphState.graph.locs && locGraphState.graph.locs.length);
        panel.style.display = hasGraph ? '' : 'none';
        if (noData) noData.style.display = hasGraph ? 'block' : 'none';
        return;
    }
    panel.style.display = '';
    if (noData) noData.style.display = 'none';
    renderHeatmapTable('locheatmap-table', 'locheatmap-thead-row', 'locheatmap-body', data, 'Loc');
}

// Re-render the region-control matrix from the stats stashed on mapState.
// Visibility of the panel itself is owned by initRegionControl.
function renderRegionHeatmap() {
    const data = buildRegionHeatmap(mapState.controlRegions, mapState.controlStats);
    if (!data) return;
    renderHeatmapTable('region-control-table', 'region-control-thead-row', 'region-control-body', data, 'Region');
}

// Render a matrix data model into a .stats-table: a header row (row-axis label
// + one team-underlined <th> per column) and a tbody built with the shared
// renderTableRows helper. Each cell is viridis-shaded by its intensity with the
// % printed and a data-sort-value, so makeSortable can sort any column. The
// table renders crisply and persists across tab switches — no canvas resize
// dance needed.
function renderHeatmapTable(tableId, theadRowId, tbodyId, data, firstColLabel) {
    const table = document.getElementById(tableId);
    const theadRow = document.getElementById(theadRowId);
    if (!table || !theadRow) return;
    const { columns, teamCols } = data;

    const heads = [`<th>${escapeHtml(firstColLabel)}</th>`];
    columns.forEach((col, ci) => {
        const cls = ['heatmap-col'];
        if (col.kind === 'team') cls.push('heatmap-col-team');
        if (teamCols && ci === teamCols) cls.push('heatmap-col-sep');
        const color = col.teamIdx >= 0 ? TEAM_COLORS[col.teamIdx % TEAM_COLORS.length] : '';
        const style = color ? ` style="border-bottom: 3px solid ${color}"` : '';
        heads.push(`<th class="${cls.join(' ')}"${style} title="${escapeHtml(col.full || col.label)}">${escapeHtml(col.label)}</th>`);
    });
    theadRow.innerHTML = heads.join('');

    renderTableRows(tbodyId, data.rows, (row) => {
        const tds = [`<td class="heatmap-rowname"><strong>${escapeHtml(row.name)}</strong></td>`];
        row.cells.forEach((cell, ci) => {
            const cls = ['heatmap-cell'];
            if (teamCols && ci === teamCols) cls.push('heatmap-col-sep');
            if (cell.p == null) { // column not present in this row (e.g. player never here)
                tds.push(`<td class="${cls.join(' ')}" data-sort-value="-1"></td>`);
                return;
            }
            const rgb = cell.i > 0 ? heatColorRGB(cell.i) : null;
            const style = rgb ? ` style="background: rgb(${rgb[0]}, ${rgb[1]}, ${rgb[2]}); color: ${contrastInk(rgb)}"` : '';
            const title = data.cellTitle ? data.cellTitle(columns[ci], row, ci) : '';
            const titleAttr = title ? ` title="${escapeHtml(title)}"` : '';
            tds.push(`<td class="${cls.join(' ')}"${style} data-sort-value="${cell.p}"${titleAttr}>${Math.round(cell.p)}%</td>`);
        });
        return tds.join('');
    });

    makeSortable(table);
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

    // Prefer timelineState.powerupEvents (already converted ms→s at intake
    // in displayTimelineAnalysis). Fall back to the raw schema field with
    // its own conversion so the panel still renders if displayResults runs
    // displayMap before displayTimelineAnalysis on some path.
    const events = timelineState.powerupEvents && timelineState.powerupEvents.length
        ? timelineState.powerupEvents
        : (result.timelineAnalysis?.powerupEvents || []).map(ev => ({
              ...ev,
              time: ev.time * 0.001,
          }));

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
