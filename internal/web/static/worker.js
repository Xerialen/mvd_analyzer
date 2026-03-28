// Web Worker for MVD analysis via WASM
// Loads Go WASM binary and exposes analyzeMVD() off the main thread

importScripts('wasm_exec.js');

let wasmReady = false;

async function initWasm() {
    const go = new Go();
    const result = await WebAssembly.instantiateStreaming(
        fetch('analyzer.wasm'), go.importObject
    );
    go.run(result.instance);
    wasmReady = true;
    postMessage({ type: 'ready' });
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
