// Web Worker for MVD analysis via WASM
// Loads Go WASM binary and exposes analyzeMVD() off the main thread

importScripts('wasm_exec.js');

// Synchronous loc fetcher exposed to the WASM module. The Go side calls
// this from internal/loc/loader_wasm.go during analysis to pull the
// per-map .loc file on demand instead of bundling all locs into the
// WASM binary. Sync XHR is deprecated on the main thread but is still
// allowed inside Web Workers, which is exactly where we run.
self.fetchLocSync = function(mapName) {
    if (!mapName) return null;
    try {
        const xhr = new XMLHttpRequest();
        xhr.open('GET', 'locs/' + mapName + '.loc', false); // false = synchronous
        xhr.send(null);
        if (xhr.status === 200) return xhr.responseText;
    } catch (e) {
        // Network errors / CORS / 404 — fall through to null so Go reports
        // "no loc file for map ...".
    }
    return null;
};

let wasmReady = false;

async function initWasm() {
    const go = new Go();
    const result = await WebAssembly.instantiateStreaming(
        fetch('analyzer.wasm'), go.importObject
    );
    go.run(result.instance);
    wasmReady = true;
    postMessage({ type: 'ready', version: self.wasmVersion || null });
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
            const jsonStr = analyzeMVD(bytes, filename);
            postMessage({ type: 'result', json: jsonStr });
        } catch (err) {
            postMessage({ type: 'error', message: err.message || String(err) });
        }
    }
};
