// Web Worker for MVD analysis via WASM
// Loads Go WASM binary and exposes analyzeMVD() off the main thread

importScripts('wasm_exec.js');

// Per-analyze buffers recording the cost of the synchronous loc/bsp
// fetches the WASM module pulls during analyzeMVD. Reset at the start of
// each analyze so the reported numbers belong to the current demo only.
let locFetches = [];
let bspFetches = [];

// Synchronous loc fetcher exposed to the WASM module. The Go side calls
// this from internal/loc/loader_wasm.go during analysis to pull the
// per-map .loc file on demand instead of bundling all locs into the
// WASM binary. Sync XHR is deprecated on the main thread but is still
// allowed inside Web Workers, which is exactly where we run.
//
// We return a Uint8Array (binary) rather than responseText. Loc files
// from quakeworld.nu often encode item-name shorthands as high-bit
// ASCII (e.g. "ssg" → 0xf3 0xf3 0xe7) which is not valid UTF-8;
// responseText would silently mangle those bytes into U+FFFD, breaking
// substituteVariables() on the Go side and producing garbled location
// names like "o?=o?=o?=.RL.low" instead of "SSG.RL.low".
self.fetchLocSync = function(mapName) {
    if (!mapName) return null;
    const t0 = performance.now();
    try {
        const xhr = new XMLHttpRequest();
        xhr.open('GET', 'locs/' + mapName + '.loc', false); // false = synchronous
        xhr.responseType = 'arraybuffer';
        xhr.send(null);
        if (xhr.status === 200 && xhr.response) {
            const bytes = new Uint8Array(xhr.response);
            locFetches.push({ map: mapName, ms: performance.now() - t0, bytes: bytes.length });
            return bytes;
        }
    } catch (e) {
        // Network errors / CORS / 404 — fall through to null so Go reports
        // "no loc file for map ...".
    }
    locFetches.push({ map: mapName, ms: performance.now() - t0, bytes: 0 });
    return null;
};

// Synchronous BSP fetcher exposed to the WASM module. The Go side calls
// this from mvd-analytics/locvis/loader_wasm.go during analysis to pull
// the per-map .bsp file used by the visibility-aware loc attribution
// filter (V6 / V6a). Returns null on 404 / missing BSP, which causes
// locvis to degrade to V1 (pure Euclidean nearest-neighbour) for that
// map — never a hard error. BSPs are deployed to dist/bsps/ by `make
// build` when `make bsps` has been run; deployments without the BSP
// directory simply get V1 everywhere.
self.fetchBspSync = function(mapName) {
    if (!mapName) return null;
    const t0 = performance.now();
    try {
        const xhr = new XMLHttpRequest();
        xhr.open('GET', 'bsps/' + mapName + '.bsp', false);
        xhr.responseType = 'arraybuffer';
        xhr.send(null);
        if (xhr.status === 200 && xhr.response) {
            const bytes = new Uint8Array(xhr.response);
            bspFetches.push({ map: mapName, ms: performance.now() - t0, bytes: bytes.length });
            return bytes;
        }
    } catch (e) {
        // 404 / network / CORS — null falls back to V1 on the Go side.
    }
    bspFetches.push({ map: mapName, ms: performance.now() - t0, bytes: 0 });
    return null;
};

// logWorkerTimings prints the worker-side breakdown: the total WASM
// analyze wall time, the Go per-phase split, and the synchronous loc/bsp
// fetches (which are included in the wall time). Subtract loc+bsp fetch
// from the "finalize:timelineAnalysis" phase to get pure loc-resolution compute.
function logWorkerTimings(t) {
    try {
        const locMs = t.locFetch.reduce((s, f) => s + f.ms, 0);
        const bspMs = t.bspFetch.reduce((s, f) => s + f.ms, 0);
        console.groupCollapsed(
            `[mvd-timing] WASM analyze ${t.wasmAnalyzeMs.toFixed(1)} ms ` +
            `(loc fetch ${locMs.toFixed(1)} ms, bsp fetch ${bspMs.toFixed(1)} ms)`
        );
        const rows = (t.goPhases || []).map(p => ({ phase: p.name, ms: +p.ms.toFixed(2) }));
        rows.push({ phase: 'marshal', ms: +(t.marshalMs || 0).toFixed(2) });
        console.table(rows);
        if (t.locFetch.length) console.table(t.locFetch);
        if (t.bspFetch.length) console.table(t.bspFetch);
        console.groupEnd();
    } catch (e) {
        console.warn('[mvd-timing] worker log failed:', e);
    }
}

let wasmReady = false;

async function initWasm() {
    const t0 = performance.now();
    const go = new Go();
    const result = await WebAssembly.instantiateStreaming(
        fetch('analyzer.wasm'), go.importObject
    );
    go.run(result.instance);
    wasmReady = true;
    const wasmLoadMs = performance.now() - t0;
    console.log(`[mvd-timing] wasm load+instantiate: ${wasmLoadMs.toFixed(1)} ms`);
    postMessage({ type: 'ready', version: self.wasmVersion || null, wasmLoadMs });
}

initWasm().catch(err => {
    postMessage({ type: 'error', message: 'Failed to load WASM: ' + err.message });
});

onmessage = function(e) {
    if (e.data.type === 'analyze') {
        if (!wasmReady) {
            postMessage({ type: 'error', message: 'WASM not loaded yet' });
            return;
        }
        try {
            const bytes = new Uint8Array(e.data.bytes);
            const filename = e.data.filename || 'demo.mvd';

            // Reset fetch buffers; the upcoming analyzeMVD pulls loc/bsp
            // files synchronously and these record their cost.
            locFetches = [];
            bspFetches = [];

            const tAnalyze = performance.now();
            const jsonStr = analyzeMVD(bytes, filename);
            const wasmAnalyzeMs = performance.now() - tAnalyze;

            // Per-phase pipeline timings from the Go side (init, event
            // pass, each analyzer Finalize, each post-processor + marshal).
            let goTimings = {};
            try {
                goTimings = JSON.parse(getAnalysisTimings());
            } catch (err) {
                goTimings = {};
            }

            const timings = {
                wasmAnalyzeMs,
                goPhases: goTimings.phases || [],
                marshalMs: goTimings.marshalMs || 0,
                locFetch: locFetches,
                bspFetch: bspFetches,
            };
            logWorkerTimings(timings);

            // Deliver the analysed result immediately so the main thread can
            // render the summary now. The expensive 50ms bucket / region
            // views below are only needed by the Timeline/Map tabs, so they
            // are built *after* the result is sent. The worker being busy
            // here does not block the main thread — the result message is
            // already queued for it and renders while we compute.
            postMessage({ type: 'result', json: jsonStr, timings });

            // ---- Deferred: legacy 50ms buckets + region-control states ----
            // Schema v7: highResBuckets and regionControl.bucketStates are
            // not parse-time fields; the existing panels still read them, so
            // build via the bridge and ship a second 'buckets' message.
            let bucketsJSON = '';
            const tBuckets = performance.now();
            try {
                bucketsJSON = getDefaultBuckets();
            } catch (err) {
                bucketsJSON = '';
            }
            const bucketsMs = performance.now() - tBuckets;

            let regionStatesJSON = '';
            const tRegion = performance.now();
            try {
                const parsed = JSON.parse(jsonStr);
                const rc = parsed.timelineAnalysis && parsed.timelineAnalysis.regionControl;
                if (rc && rc.regions && rc.teamA && rc.teamB) {
                    const overrideJSON = JSON.stringify({
                        regions: rc.regions.map(r => ({
                            name: r.name,
                            locs: [...new Set((r.points || []).map(p => p.name))],
                        })),
                    });
                    regionStatesJSON = recomputeRegionControl(overrideJSON);
                }
            } catch (err) {
                regionStatesJSON = '';
            }
            const regionMs = performance.now() - tRegion;

            console.log(
                `[mvd-timing] deferred bucket build (off critical path): ` +
                `getDefaultBuckets ${bucketsMs.toFixed(1)} ms, ` +
                `recomputeRegionControl ${regionMs.toFixed(1)} ms`
            );

            postMessage({ type: 'buckets', bucketsJSON, regionStatesJSON, bucketsMs, regionMs });
        } catch (err) {
            postMessage({ type: 'error', message: err.message || String(err) });
        }
    } else if (e.data.type === 'recomputeRegions') {
        // recomputeRegionControl is a Go export living on this worker's
        // self (js.Global() resolves to the worker scope). Calling it
        // from the main page would NameError — that's what the v6
        // shipping bug was — so the round-trip goes through here.
        if (!wasmReady) {
            postMessage({ type: 'recompute_error', message: 'WASM not loaded yet' });
            return;
        }
        try {
            const jsonStr = recomputeRegionControl(e.data.overrideJSON);
            postMessage({ type: 'recompute_result', json: jsonStr });
        } catch (err) {
            postMessage({ type: 'recompute_error', message: err.message || String(err) });
        }
    }
};
