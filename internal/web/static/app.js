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
        if (result.weaponStats.timelineStats) {
            displayAccuracyGraphs(result.weaponStats.timelineStats);
        }
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

function displayAccuracyGraphs(timelineStats) {
    const container = document.getElementById('sg-graphs');
    container.innerHTML = '';

    for (const [playerName, stats] of Object.entries(timelineStats)) {
        if (!stats.windows || stats.windows.length === 0) continue;

        const hasSgData = stats.windows.some(w => w.sg && w.sg.shots > 0);
        if (!hasSgData) continue;

        const playerDiv = document.createElement('div');
        playerDiv.className = 'player-graph';

        const graphBars = document.createElement('div');
        graphBars.className = 'graph-bars';

        const maxAcc = Math.max(...stats.windows.filter(w => w.sg).map(w => w.sg.accuracy || 0), 100);

        stats.windows.forEach(window => {
            if (!window.sg || window.sg.shots === 0) return;

            const bar = document.createElement('div');
            bar.className = 'graph-bar';
            const height = (window.sg.accuracy / maxAcc) * 100;
            bar.style.height = `${height}%`;
            bar.dataset.tooltip = `${formatTime(window.startTime)}: ${window.sg.accuracy.toFixed(1)}% (${window.sg.hits}/${window.sg.shots})`;
            graphBars.appendChild(bar);
        });

        const firstWindow = stats.windows[0];
        const lastWindow = stats.windows[stats.windows.length - 1];

        playerDiv.innerHTML = `
            <h4>${escapeHtml(playerName)}</h4>
            <div class="graph-container"></div>
            <div class="graph-labels">
                <span>${formatTime(firstWindow.startTime)}</span>
                <span>${formatTime(lastWindow.startTime + 60)}</span>
            </div>
        `;

        playerDiv.querySelector('.graph-container').appendChild(graphBars);
        container.appendChild(playerDiv);
    }
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

// Timeline Analysis State
let timelineState = {
    buckets: [],
    duration: 0,
    teams: [],
    selection: { start: 0, end: 60 },
    brushing: false,
    brushMode: null,
    dragStartX: 0,
    dragStartSelection: null
};

function displayTimelineAnalysis(result) {
    const timeline = result.timelineAnalysis;
    const events = result.messages?.events || [];
    const demoInfo = result.demoInfo;

    // Get teams
    let teams = [];
    if (demoInfo?.teams) {
        teams = demoInfo.teams;
    } else if (result.match?.teams) {
        teams = result.match.teams.map(t => t.name);
    }

    timelineState.buckets = timeline?.buckets || [];
    timelineState.duration = result.duration || 600;
    timelineState.teams = teams;
    timelineState.selection = { start: 0, end: Math.min(60, timelineState.duration) };

    // Update legend team names
    if (teams.length >= 2) {
        document.getElementById('legend-team-a').textContent = teams[0] + ' ↑';
        document.getElementById('legend-team-b').textContent = teams[1] + ' ↓';
        document.getElementById('team-a-health-title').textContent = `${teams[0]} Health/Armor`;
        document.getElementById('team-b-health-title').textContent = `${teams[1]} Health/Armor`;
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

    if (!buckets || buckets.length === 0 || teams.length < 2) return;

    // Find max value for scaling (max per team, not total)
    let maxTeamValue = 1;
    for (const bucket of buckets) {
        const td = bucket.teamData || {};
        const teamA = td[teams[0]] || {};
        const teamB = td[teams[1]] || {};
        const teamATotal = (teamA.playersWithWeapons || 0) + (teamA.playersWithPowerups || 0);
        const teamBTotal = (teamB.playersWithWeapons || 0) + (teamB.playersWithPowerups || 0);
        maxTeamValue = Math.max(maxTeamValue, teamATotal, teamBTotal);
    }

    // Create diverging bars (Team A up, Team B down)
    for (const bucket of buckets) {
        const bar = document.createElement('div');
        bar.className = 'diverging-bar';

        const td = bucket.teamData || {};
        const teamA = td[teams[0]] || {};
        const teamB = td[teams[1]] || {};

        // Team A goes up (above center axis)
        const topContainer = document.createElement('div');
        topContainer.className = 'diverging-bar-top';

        // Weapons first (closer to axis), powerups on top
        if ((teamA.playersWithWeapons || 0) > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment weapons';
            seg.style.height = `${(teamA.playersWithWeapons / maxTeamValue) * 40}px`;
            topContainer.appendChild(seg);
        }
        if ((teamA.playersWithPowerups || 0) > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment powerups';
            seg.style.height = `${(teamA.playersWithPowerups / maxTeamValue) * 40}px`;
            topContainer.appendChild(seg);
        }

        // Team B goes down (below center axis)
        const bottomContainer = document.createElement('div');
        bottomContainer.className = 'diverging-bar-bottom';

        if ((teamB.playersWithWeapons || 0) > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment weapons';
            seg.style.height = `${(teamB.playersWithWeapons / maxTeamValue) * 40}px`;
            bottomContainer.appendChild(seg);
        }
        if ((teamB.playersWithPowerups || 0) > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment powerups';
            seg.style.height = `${(teamB.playersWithPowerups / maxTeamValue) * 40}px`;
            bottomContainer.appendChild(seg);
        }

        bar.appendChild(topContainer);
        bar.appendChild(bottomContainer);
        container.appendChild(bar);
    }
}

function renderOverviewAxis() {
    const container = document.getElementById('overview-axis');
    container.innerHTML = '';

    const duration = timelineState.duration;
    const tickCount = 5;

    for (let i = 0; i <= tickCount; i++) {
        const time = (duration / tickCount) * i;
        const span = document.createElement('span');
        span.textContent = formatTime(time);
        container.appendChild(span);
    }
}

function setupBrush() {
    const container = document.getElementById('timeline-overview-container');
    const selection = document.getElementById('brush-selection');
    const leftHandle = document.getElementById('brush-handle-left');
    const rightHandle = document.getElementById('brush-handle-right');

    updateBrushPosition();

    // Mouse event handlers
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

    // Update time range label
    document.getElementById('time-range-label').textContent =
        `(${formatTime(start)} - ${formatTime(end)})`;

    // Update all detail panels
    updateDetailMessages(start, end);
    updateDetailGraph(start, end);
    updateHealthArmorGraphs(start, end);
}

function updateDetailMessages(startTime, endTime) {
    const container = document.getElementById('detail-messages');
    container.innerHTML = '';

    if (!currentResult?.messages?.events) return;

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

    // Render messages (limit to 200)
    deduped.slice(0, 200).forEach(event => {
        const item = document.createElement('div');
        item.className = 'timeline-message-item';
        item.innerHTML = `
            <span class="timeline-message-time">${formatTime(event.time)}</span>
            <span class="timeline-message-content ${event.type}">${escapeHtml(event.message)}</span>
        `;
        container.appendChild(item);
    });

    if (deduped.length === 0) {
        container.innerHTML = '<div style="color: #888; padding: 20px; text-align: center;">No events in selected time range</div>';
    }
}

function updateDetailGraph(startTime, endTime) {
    const container = document.getElementById('detail-graph');
    container.innerHTML = '';

    const buckets = timelineState.buckets;
    const teams = timelineState.teams;

    if (!buckets || buckets.length === 0 || teams.length < 2) return;

    // Filter buckets within range
    const filteredBuckets = buckets.filter(b =>
        b.startTime >= startTime && b.endTime <= endTime
    );

    if (filteredBuckets.length === 0) return;

    // Find max value for scaling (max per team)
    let maxTeamValue = 1;
    for (const bucket of filteredBuckets) {
        const td = bucket.teamData || {};
        const teamA = td[teams[0]] || {};
        const teamB = td[teams[1]] || {};
        const teamATotal = (teamA.playersWithWeapons || 0) + (teamA.playersWithPowerups || 0);
        const teamBTotal = (teamB.playersWithWeapons || 0) + (teamB.playersWithPowerups || 0);
        maxTeamValue = Math.max(maxTeamValue, teamATotal, teamBTotal);
    }

    // Create diverging bars (Team A up, Team B down)
    for (const bucket of filteredBuckets) {
        const bar = document.createElement('div');
        bar.className = 'diverging-bar';

        const td = bucket.teamData || {};
        const teamA = td[teams[0]] || {};
        const teamB = td[teams[1]] || {};

        // Team A goes up (above center axis)
        const topContainer = document.createElement('div');
        topContainer.className = 'diverging-bar-top';

        if ((teamA.playersWithWeapons || 0) > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment weapons';
            seg.style.height = `${(teamA.playersWithWeapons / maxTeamValue) * 90}px`;
            topContainer.appendChild(seg);
        }
        if ((teamA.playersWithPowerups || 0) > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment powerups';
            seg.style.height = `${(teamA.playersWithPowerups / maxTeamValue) * 90}px`;
            topContainer.appendChild(seg);
        }

        // Team B goes down (below center axis)
        const bottomContainer = document.createElement('div');
        bottomContainer.className = 'diverging-bar-bottom';

        if ((teamB.playersWithWeapons || 0) > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment weapons';
            seg.style.height = `${(teamB.playersWithWeapons / maxTeamValue) * 90}px`;
            bottomContainer.appendChild(seg);
        }
        if ((teamB.playersWithPowerups || 0) > 0) {
            const seg = document.createElement('div');
            seg.className = 'bar-segment powerups';
            seg.style.height = `${(teamB.playersWithPowerups / maxTeamValue) * 90}px`;
            bottomContainer.appendChild(seg);
        }

        bar.appendChild(topContainer);
        bar.appendChild(bottomContainer);
        container.appendChild(bar);
    }
}

function updateHealthArmorGraphs(startTime, endTime) {
    const teams = timelineState.teams;
    if (teams.length < 2) return;

    renderHealthArmorGraph('team-a-health-graph', teams[0], startTime, endTime);
    renderHealthArmorGraph('team-b-health-graph', teams[1], startTime, endTime);
}

function renderHealthArmorGraph(containerId, teamName, startTime, endTime) {
    const container = document.getElementById(containerId);
    container.innerHTML = '';

    const buckets = timelineState.buckets;

    // Filter buckets within range
    const filteredBuckets = buckets.filter(b =>
        b.startTime >= startTime && b.endTime <= endTime
    );

    if (filteredBuckets.length === 0) return;

    // Max values for scaling
    const maxHealth = 250; // Max health with mega
    const maxArmor = 200;  // Max armor (RA)
    const maxTotal = maxHealth + maxArmor;

    // Create bars
    for (const bucket of filteredBuckets) {
        const teamData = bucket.teamData?.[teamName] || {};
        const avgHealth = teamData.avgHealth || 0;
        const avgArmor = teamData.avgArmor || 0;

        const bar = document.createElement('div');
        bar.className = 'health-bar';

        // Health segment (green)
        if (avgHealth > 0) {
            const healthSeg = document.createElement('div');
            healthSeg.className = 'health-segment';
            healthSeg.style.height = `${(avgHealth / maxTotal) * 100}%`;
            healthSeg.title = `Health: ${Math.round(avgHealth)}`;
            bar.appendChild(healthSeg);
        }

        // Armor segment (yellow) - stacked on top
        if (avgArmor > 0) {
            const armorSeg = document.createElement('div');
            armorSeg.className = 'armor-segment';
            armorSeg.style.height = `${(avgArmor / maxTotal) * 100}%`;
            armorSeg.title = `Armor: ${Math.round(avgArmor)}`;
            bar.appendChild(armorSeg);
        }

        container.appendChild(bar);
    }
}
