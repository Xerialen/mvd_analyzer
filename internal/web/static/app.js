// MVD Analyzer Dashboard

let currentResult = null;

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
        });
    });
}

// Load demo from QuakeWorld Hub by game ID or URL
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
        const response = await fetch('/api/hub/load', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify({ input })
        });

        if (!response.ok) {
            throw new Error(await response.text());
        }

        const data = await response.json();
        status.textContent = 'Analysis complete!';
        status.className = 'status success';

        currentResult = data.result;
        displayResults(data.result);

        // Store hub info for viewer links
        if (data.hub) {
            currentResult.hubInfo = data.hub;
        }
    } catch (error) {
        status.textContent = 'Error: ' + error.message;
        status.className = 'status error';
    } finally {
        btn.disabled = false;
    }
}

async function uploadFile(file) {
    const status = document.getElementById('upload-status');
    status.textContent = 'Analyzing...';
    status.className = 'status loading';

    const formData = new FormData();
    formData.append('file', file);

    try {
        const response = await fetch('/api/analyze', {
            method: 'POST',
            body: formData
        });

        if (!response.ok) {
            throw new Error(await response.text());
        }

        const data = await response.json();
        status.textContent = 'Analysis complete!';
        status.className = 'status success';

        currentResult = data.result;
        displayResults(data.result);
    } catch (error) {
        status.textContent = 'Error: ' + error.message;
        status.className = 'status error';
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

    // Accuracy tab
    if (result.weaponStats) {
        displayAccuracyTable(result.weaponStats.playerStats, result.match ? result.match.players : [], demoInfo);
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

    sorted.forEach(player => {
        const tr = document.createElement('tr');
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

function displayAccuracyTable(playerStats, players, demoInfo) {
    const tbody = document.getElementById('accuracy-body');
    tbody.innerHTML = '';

    const data = [];

    if (demoInfo && demoInfo.players) {
        for (const player of demoInfo.players) {
            const weapons = player.weapons || {};
            const sg = weapons.sg || {};
            const lg = weapons.lg || {};
            const rl = weapons.rl || {};

            const sgAcc = sg.acc && sg.acc.attacks > 0 ? (sg.acc.hits / sg.acc.attacks) * 100 : 0;
            const lgAcc = lg.acc && lg.acc.attacks > 0 ? (lg.acc.hits / lg.acc.attacks) * 100 : 0;

            const rlDmg = rl.damage?.enemy || 0;
            const totalDmg = player.dmg?.given || 0;

            data.push({
                name: player.name,
                team: player.team || '',
                sgAcc: sgAcc,
                lgAcc: lgAcc,
                sgShots: sg.acc?.attacks || 0,
                sgHits: sg.acc?.hits || 0,
                lgShots: lg.acc?.attacks || 0,
                lgHits: lg.acc?.hits || 0,
                rlDmg: rlDmg,
                totalDmg: totalDmg
            });
        }
    } else {
        for (const [name, stats] of Object.entries(playerStats)) {
            const playerInfo = players.find(p => p.name === name);
            const sg = stats.weapons?.sg || {};
            const lg = stats.weapons?.lg || {};
            const rl = stats.weapons?.rl || {};

            let totalDmg = 0;
            for (const w of Object.values(stats.weapons || {})) {
                totalDmg += w.damage || 0;
            }

            data.push({
                name: name,
                team: playerInfo ? playerInfo.team : '',
                sgAcc: sg.accuracy || 0,
                lgAcc: lg.accuracy || 0,
                sgShots: sg.shots || 0,
                sgHits: sg.hits || 0,
                lgShots: lg.shots || 0,
                lgHits: lg.hits || 0,
                rlDmg: rl.damage || 0,
                totalDmg: totalDmg
            });
        }
    }

    data.sort((a, b) => b.totalDmg - a.totalDmg);

    data.forEach(player => {
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td>${escapeHtml(player.name)}</td>
            <td>${escapeHtml(player.team)}</td>
            <td class="${getAccuracyClass(player.sgAcc)}">${player.sgAcc.toFixed(1)}% <span class="acc-detail">(${player.sgHits}/${player.sgShots})</span></td>
            <td class="${getAccuracyClass(player.lgAcc)}">${player.lgAcc.toFixed(1)}% <span class="acc-detail">(${player.lgHits}/${player.lgShots})</span></td>
            <td>${player.rlDmg}</td>
            <td>${player.totalDmg}</td>
        `;
        tbody.appendChild(tr);
    });
}

function getAccuracyClass(acc) {
    if (acc >= 40) return 'accuracy-high';
    if (acc >= 25) return 'accuracy-medium';
    return 'accuracy-low';
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
    events: [],
    duration: 0,
    matchStartTime: 0,
    teams: [],
    selection: { start: 0, end: 60 },
    brushing: false,
    brushMode: null,
    dragStartX: 0,
    dragStartSelection: null,
    overviewBucketSize: 5, // Aggregate to 5-second buckets for overview
    brushInitialized: false // Track if brush handlers are set up
};

// Reset all timeline state for loading a new demo
function resetTimelineState() {
    timelineState.buckets = [];
    timelineState.events = [];
    timelineState.fragEvents = [];
    timelineState.duration = 0;
    timelineState.matchStartTime = 0;
    timelineState.teams = [];
    timelineState.selection = { start: 0, end: 60 };
    timelineState.brushing = false;
    timelineState.brushMode = null;
    timelineState.dragStartX = 0;
    timelineState.dragStartSelection = null;

    // Clear all timeline graph containers
    const containers = [
        'overview-graph', 'overview-axis', 'detail-graph', 'detail-axis',
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
    timelineState.matchStartTime = timeline?.matchStartTime || 0;
    timelineState.duration = result.duration || 600;
    timelineState.teams = teams;
    timelineState.events = result.messages?.events || [];
    timelineState.fragEvents = timeline?.fragEvents || []; // Frag events from stat tracking

    // Start selection at match start
    const effectiveStart = timelineState.matchStartTime;
    timelineState.selection = {
        start: effectiveStart,
        end: Math.min(effectiveStart + 60, timelineState.duration)
    };

    // Update legend team names
    if (teams.length >= 2) {
        document.getElementById('legend-team-a').textContent = teams[0] + ' ↑';
        document.getElementById('legend-team-b').textContent = teams[1] + ' ↓';
        document.getElementById('team-a-chat-title').textContent = `${teams[0]} Chat`;
        document.getElementById('team-b-chat-title').textContent = `${teams[1]} Chat`;
        document.getElementById('legend-health-team-a').textContent = teams[0] + ' ↑';
        document.getElementById('legend-health-team-b').textContent = teams[1] + ' ↓';
    }

    renderOverviewGraph();
    renderOverviewAxis();
    setupBrush();
    updateDetailView();
}

function renderOverviewGraph() {
    const container = document.getElementById('overview-graph');
    container.innerHTML = '';

    const buckets = timelineState.buckets;
    const teams = timelineState.teams;
    const matchStart = timelineState.matchStartTime;

    if (!buckets || buckets.length === 0 || teams.length < 2) return;

    // Filter to match time only (after warmup)
    const matchBuckets = buckets.filter(b => b.startTime >= matchStart);

    // Aggregate to 5-second buckets for overview
    const aggregated = aggregateBuckets(matchBuckets, timelineState.overviewBucketSize, teams);

    // Find max value for scaling (use 4 as typical max for 4v4)
    let maxTeamValue = 4;
    for (const bucket of aggregated) {
        const teamATotal = bucket.teamA.rl + bucket.teamA.lg + bucket.teamA.rllg +
                          bucket.teamA.quad + bucket.teamA.pent + bucket.teamA.ring;
        const teamBTotal = bucket.teamB.rl + bucket.teamB.lg + bucket.teamB.rllg +
                          bucket.teamB.quad + bucket.teamB.pent + bucket.teamB.ring;
        maxTeamValue = Math.max(maxTeamValue, teamATotal, teamBTotal);
    }

    // Update Y-axis labels
    document.querySelector('#overview-y-axis .y-top').textContent = maxTeamValue;
    document.querySelector('#overview-y-axis .y-bottom').textContent = maxTeamValue;

    const barHeight = 40; // pixels for max value

    // Create diverging bars (Team A up, Team B down)
    for (const bucket of aggregated) {
        const bar = document.createElement('div');
        bar.className = 'diverging-bar';

        // Team A goes up (above center axis) - weapons closer to axis
        const topContainer = document.createElement('div');
        topContainer.className = 'diverging-bar-top';
        addGranularSegments(topContainer, bucket.teamA, maxTeamValue, barHeight);

        // Team B goes down (below center axis)
        const bottomContainer = document.createElement('div');
        bottomContainer.className = 'diverging-bar-bottom';
        addGranularSegments(bottomContainer, bucket.teamB, maxTeamValue, barHeight);

        bar.appendChild(topContainer);
        bar.appendChild(bottomContainer);
        container.appendChild(bar);
    }
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

// Aggregate 1-second buckets into larger time windows (for overview)
function aggregateBuckets(buckets, windowSize, teams) {
    if (buckets.length === 0) return [];

    const result = [];
    let currentWindow = null;
    let windowStart = buckets[0].startTime;

    for (const bucket of buckets) {
        // Start new window if needed
        if (!currentWindow || bucket.startTime >= windowStart + windowSize) {
            if (currentWindow) result.push(currentWindow);
            windowStart = Math.floor(bucket.startTime / windowSize) * windowSize;
            currentWindow = {
                startTime: windowStart,
                endTime: windowStart + windowSize,
                teamA: { rl: 0, lg: 0, rllg: 0, quad: 0, pent: 0, ring: 0, health: 0, armor: 0, count: 0 },
                teamB: { rl: 0, lg: 0, rllg: 0, quad: 0, pent: 0, ring: 0, health: 0, armor: 0, count: 0 }
            };
        }

        // Aggregate data
        const td = bucket.teamData || {};
        const teamAData = td[teams[0]] || {};
        const teamBData = td[teams[1]] || {};

        // Use max within window (not sum) for player counts
        currentWindow.teamA.rl = Math.max(currentWindow.teamA.rl, teamAData.playersWithRL || 0);
        currentWindow.teamA.lg = Math.max(currentWindow.teamA.lg, teamAData.playersWithLG || 0);
        currentWindow.teamA.rllg = Math.max(currentWindow.teamA.rllg, teamAData.playersWithRLLG || 0);
        currentWindow.teamA.quad = Math.max(currentWindow.teamA.quad, teamAData.playersWithQuad || 0);
        currentWindow.teamA.pent = Math.max(currentWindow.teamA.pent, teamAData.playersWithPent || 0);
        currentWindow.teamA.ring = Math.max(currentWindow.teamA.ring, teamAData.playersWithRing || 0);
        currentWindow.teamA.health += teamAData.totalHealth || 0;
        currentWindow.teamA.armor += teamAData.totalArmor || 0;
        currentWindow.teamA.count++;

        currentWindow.teamB.rl = Math.max(currentWindow.teamB.rl, teamBData.playersWithRL || 0);
        currentWindow.teamB.lg = Math.max(currentWindow.teamB.lg, teamBData.playersWithLG || 0);
        currentWindow.teamB.rllg = Math.max(currentWindow.teamB.rllg, teamBData.playersWithRLLG || 0);
        currentWindow.teamB.quad = Math.max(currentWindow.teamB.quad, teamBData.playersWithQuad || 0);
        currentWindow.teamB.pent = Math.max(currentWindow.teamB.pent, teamBData.playersWithPent || 0);
        currentWindow.teamB.ring = Math.max(currentWindow.teamB.ring, teamBData.playersWithRing || 0);
        currentWindow.teamB.health += teamBData.totalHealth || 0;
        currentWindow.teamB.armor += teamBData.totalArmor || 0;
        currentWindow.teamB.count++;
    }

    if (currentWindow) result.push(currentWindow);
    return result;
}

// Add granular weapon/powerup segments to a container
function addGranularSegments(container, data, maxValue, maxHeight) {
    // Order: weapons (RL, LG, RL+LG) closest to axis, then powerups (Quad, Pent, Ring)
    const segments = [
        { value: data.rl, className: 'rl' },
        { value: data.lg, className: 'lg' },
        { value: data.rllg, className: 'rllg' },
        { value: data.quad, className: 'quad' },
        { value: data.pent, className: 'pent' },
        { value: data.ring, className: 'ring' }
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

function renderOverviewAxis() {
    const container = document.getElementById('overview-axis');
    container.innerHTML = '';

    const matchStart = timelineState.matchStartTime;
    const duration = timelineState.duration - matchStart;
    const tickCount = 5;

    for (let i = 0; i <= tickCount; i++) {
        const time = (duration / tickCount) * i;
        const span = document.createElement('span');
        span.textContent = formatTime(time);
        container.appendChild(span);
    }
}

function setupBrush() {
    // Only set up event handlers once - they reference timelineState which updates
    if (timelineState.brushInitialized) {
        updateBrushPosition();
        return;
    }

    const container = document.getElementById('timeline-overview-container');
    const selection = document.getElementById('brush-selection');
    const leftHandle = document.getElementById('brush-handle-left');
    const rightHandle = document.getElementById('brush-handle-right');

    updateBrushPosition();

    // Mouse event handlers - these reference timelineState dynamically
    function startDrag(e, mode) {
        e.preventDefault();
        timelineState.brushing = true;
        timelineState.brushMode = mode;
        timelineState.dragStartX = e.clientX;
        timelineState.dragStartSelection = { ...timelineState.selection };

        document.addEventListener('mousemove', onDrag);
        document.addEventListener('mouseup', endDrag);
    }

    function onDrag(e) {
        if (!timelineState.brushing) return;

        const containerRect = container.getBoundingClientRect();
        const containerWidth = containerRect.width - 20; // Account for padding
        const currentX = e.clientX - containerRect.left - 10;
        const currentTime = (currentX / containerWidth) * timelineState.duration;

        const minWindow = 10; // Minimum 10 second window

        switch (timelineState.brushMode) {
            case 'move':
                const deltaX = e.clientX - timelineState.dragStartX;
                const deltaTime = (deltaX / containerWidth) * timelineState.duration;
                let newStart = timelineState.dragStartSelection.start + deltaTime;
                let newEnd = timelineState.dragStartSelection.end + deltaTime;

                // Clamp to bounds
                if (newStart < 0) {
                    newEnd -= newStart;
                    newStart = 0;
                }
                if (newEnd > timelineState.duration) {
                    newStart -= (newEnd - timelineState.duration);
                    newEnd = timelineState.duration;
                }

                timelineState.selection.start = Math.max(0, newStart);
                timelineState.selection.end = Math.min(timelineState.duration, newEnd);
                break;

            case 'resize-left':
                timelineState.selection.start = Math.max(0,
                    Math.min(currentTime, timelineState.selection.end - minWindow));
                break;

            case 'resize-right':
                timelineState.selection.end = Math.min(timelineState.duration,
                    Math.max(currentTime, timelineState.selection.start + minWindow));
                break;
        }

        updateBrushPosition();
        updateDetailView();
    }

    function endDrag() {
        timelineState.brushing = false;
        timelineState.brushMode = null;
        document.removeEventListener('mousemove', onDrag);
        document.removeEventListener('mouseup', endDrag);
    }

    // Attach event listeners
    selection.addEventListener('mousedown', (e) => startDrag(e, 'move'));
    leftHandle.addEventListener('mousedown', (e) => startDrag(e, 'resize-left'));
    rightHandle.addEventListener('mousedown', (e) => startDrag(e, 'resize-right'));

    // Click on container to jump selection
    container.addEventListener('click', (e) => {
        if (timelineState.brushing) return;
        if (e.target.closest('.brush-selection, .brush-handle')) return;

        const containerRect = container.getBoundingClientRect();
        const containerWidth = containerRect.width - 20;
        const clickX = e.clientX - containerRect.left - 10;
        const clickTime = (clickX / containerWidth) * timelineState.duration;

        const windowSize = timelineState.selection.end - timelineState.selection.start;
        const halfWindow = windowSize / 2;

        timelineState.selection.start = Math.max(0, clickTime - halfWindow);
        timelineState.selection.end = Math.min(timelineState.duration, clickTime + halfWindow);

        // Adjust if we hit boundaries
        if (timelineState.selection.start === 0) {
            timelineState.selection.end = Math.min(timelineState.duration, windowSize);
        }
        if (timelineState.selection.end === timelineState.duration) {
            timelineState.selection.start = Math.max(0, timelineState.duration - windowSize);
        }

        updateBrushPosition();
        updateDetailView();
    });

    timelineState.brushInitialized = true;
}

function updateBrushPosition() {
    const selection = document.getElementById('brush-selection');
    const leftHandle = document.getElementById('brush-handle-left');
    const rightHandle = document.getElementById('brush-handle-right');

    const startPct = (timelineState.selection.start / timelineState.duration) * 100;
    const endPct = (timelineState.selection.end / timelineState.duration) * 100;
    const widthPct = endPct - startPct;

    selection.style.left = `${startPct}%`;
    selection.style.width = `${widthPct}%`;

    leftHandle.style.left = `${startPct}%`;
    rightHandle.style.left = `${endPct}%`;
}

function updateDetailView() {
    const { start, end } = timelineState.selection;
    const matchStart = timelineState.matchStartTime;

    // Update time range label (relative to match start)
    const relStart = start - matchStart;
    const relEnd = end - matchStart;
    document.getElementById('time-range-label').textContent =
        `(${formatTime(Math.max(0, relStart))} - ${formatTime(Math.max(0, relEnd))})`;

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

    // Find max value for scaling (use 4 as typical max for 4v4)
    let maxTeamValue = 4;
    for (const bucket of displayBuckets) {
        const td = bucket.teamData || {};
        const teamA = td[teams[0]] || {};
        const teamB = td[teams[1]] || {};
        const teamATotal = (teamA.playersWithRL || 0) + (teamA.playersWithLG || 0) + (teamA.playersWithRLLG || 0) +
                          (teamA.playersWithQuad || 0) + (teamA.playersWithPent || 0) + (teamA.playersWithRing || 0);
        const teamBTotal = (teamB.playersWithRL || 0) + (teamB.playersWithLG || 0) + (teamB.playersWithRLLG || 0) +
                          (teamB.playersWithQuad || 0) + (teamB.playersWithPent || 0) + (teamB.playersWithRing || 0);
        maxTeamValue = Math.max(maxTeamValue, teamATotal, teamBTotal);
    }

    // Update Y-axis labels
    document.querySelector('#detail-y-axis .y-top').textContent = maxTeamValue;
    document.querySelector('#detail-y-axis .y-bottom').textContent = maxTeamValue;

    const barHeight = 90; // pixels for max value

    // Create diverging bars (Team A up, Team B down)
    for (const bucket of displayBuckets) {
        const bar = document.createElement('div');
        bar.className = 'diverging-bar';

        const td = bucket.teamData || {};
        const teamAData = td[teams[0]] || {};
        const teamBData = td[teams[1]] || {};

        // Build team data objects
        const teamA = {
            rl: teamAData.playersWithRL || 0,
            lg: teamAData.playersWithLG || 0,
            rllg: teamAData.playersWithRLLG || 0,
            quad: teamAData.playersWithQuad || 0,
            pent: teamAData.playersWithPent || 0,
            ring: teamAData.playersWithRing || 0
        };
        const teamB = {
            rl: teamBData.playersWithRL || 0,
            lg: teamBData.playersWithLG || 0,
            rllg: teamBData.playersWithRLLG || 0,
            quad: teamBData.playersWithQuad || 0,
            pent: teamBData.playersWithPent || 0,
            ring: teamBData.playersWithRing || 0
        };

        // Team A goes up (above center axis)
        const topContainer = document.createElement('div');
        topContainer.className = 'diverging-bar-top';
        addGranularSegments(topContainer, teamA, maxTeamValue, barHeight);

        // Team B goes down (below center axis)
        const bottomContainer = document.createElement('div');
        bottomContainer.className = 'diverging-bar-bottom';
        addGranularSegments(bottomContainer, teamB, maxTeamValue, barHeight);

        bar.appendChild(topContainer);
        bar.appendChild(bottomContainer);
        container.appendChild(bar);
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
