// map-gl.js — WebGL/Three.js renderer for the 3D map view.
//
// Replaces the legacy Canvas-2D pseudo-3D ("3D" toggle) path in app.js with a
// real WebGL scene. app.js stays classic global-scope script; this module is
// loaded with type="module" and talks to app.js exclusively through the global
// `window.MapGL` API below. It never reaches back into app.js internals — all
// per-frame data arrives via renderFrame(state) (see the adapter in app.js).
//
// Coordinate system: Quake units, Z-up. camera.up = (0,0,1); no axis remap.
//
// Lifecycle:
//   MapGL.init(canvas)        once, lazily, on first 3D activation
//   MapGL.loadMap(name)       fetch maps3d/<name>.bin → build the shell mesh
//   MapGL.setLiquids(list)    add translucent water/slime/lava volumes
//   MapGL.show() / hide()     follow app's mapIs3D() toggle
//   MapGL.resize(w,h,dpr)     mirror resizeMapCanvas()
//   MapGL.renderFrame(state)  push per-frame analysis data (later phases)
//   MapGL.dispose()           free GPU resources on demo/tab teardown
//
// An internal rAF loop runs only while visible, so OrbitControls damping stays
// smooth independently of the demo playback clock.

import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';

const BG_COLOR = 0x0a0a15; // matches #map-canvas background in styles.css

const LIQUID_COLORS = {
  water: 0x2a6fb0,
  slime: 0x4a8f3a,
  lava:  0xd06a20,
};

const MapGL = {
  _inited: false,
  _visible: false,
  _rafId: 0,
  renderer: null,
  scene: null,
  camera: null,
  controls: null,
  canvas: null,
  _scaffold: null,   // shown only until a real map mesh loads
  mapGroup: null,    // static shell + wireframe + liquids for the current map
  _decoded: null,    // CPU-side decoded mesh awaiting a scene to build into
  _pendingMap: null, // map name requested before init
  _liquids: null,    // liquid list awaiting a scene
  _mapName: null,    // currently loaded map (guards redundant reloads)

  isReady() { return this._inited; },

  init(canvas) {
    if (this._inited) return;
    this.canvas = canvas;

    this.renderer = new THREE.WebGLRenderer({ canvas, antialias: true });
    this.renderer.setPixelRatio(window.devicePixelRatio || 1);
    const w = canvas.clientWidth || canvas.width || 850;
    const h = canvas.clientHeight || canvas.height || 850;

    this.scene = new THREE.Scene();
    this.scene.background = new THREE.Color(BG_COLOR);

    this.camera = new THREE.PerspectiveCamera(60, w / h, 1, 40000);
    this.camera.up.set(0, 0, 1); // Quake Z-up
    this.camera.position.set(800, 800, 1400);

    this.controls = new OrbitControls(this.camera, this.renderer.domElement);
    this.controls.enableDamping = true;
    this.controls.dampingFactor = 0.1;
    this.controls.target.set(0, 0, 0);

    // Lights (ported from demopasha's setup).
    this.scene.add(new THREE.AmbientLight(0x707070, 2));
    const d1 = new THREE.DirectionalLight(0xffffff, 1.4);
    d1.position.set(1000, 1000, 2000);
    this.scene.add(d1);
    const d2 = new THREE.DirectionalLight(0x6688cc, 0.7);
    d2.position.set(-1000, -500, 500);
    this.scene.add(d2);

    // Entity layer (players/items/deaths) drawn over the static map.
    this.entities = new THREE.Group();
    this.scene.add(this.entities);
    this._players = new Map();

    this._buildScaffold();

    this.resize(w, h, window.devicePixelRatio || 1);
    this._inited = true;

    // Apply anything requested before the scene existed.
    if (this._decoded) this._buildMapMesh();
    else if (this._pendingMap) { const n = this._pendingMap; this._pendingMap = null; this.loadMap(n); }
    if (this._liquids) this._buildLiquids(this._liquids);
    if (this._floorData) this._buildFloors();
  },

  // Faint reference grid + axes, visible only until a map mesh loads.
  _buildScaffold() {
    const g = new THREE.Group();
    g.add(new THREE.AxesHelper(256));
    const grid = new THREE.GridHelper(4096, 16, 0x224466, 0x14233a);
    grid.rotation.x = Math.PI / 2; // GridHelper is XZ; lay it on Quake XY
    g.add(grid);
    this._scaffold = g;
    this.scene.add(g);
  },

  _removeScaffold() {
    if (this._scaffold) { this.scene.remove(this._scaffold); disposeTree(this._scaffold); this._scaffold = null; }
  },

  // Fetch + decode maps3d/<name>.bin and build the shell mesh. Safe to call
  // before init() (the decode is cached and built when the scene appears) and
  // idempotent for the same map.
  async loadMap(mapName) {
    if (!mapName || mapName === this._mapName) return;
    this._mapName = mapName;
    if (!this._inited) { this._pendingMap = mapName; }
    try {
      const resp = await fetch(`maps3d/${mapName}.bin`);
      if (this._mapName !== mapName) return; // a newer loadMap superseded us
      if (!resp.ok) { this._decoded = null; this._disposeMapGroup(); return; }
      const buf = await resp.arrayBuffer();
      if (this._mapName !== mapName) return; // superseded during the read
      this._decoded = decodeM3D1(buf);
      if (this._inited) this._buildMapMesh();
    } catch (e) {
      if (this._mapName === mapName) { this._decoded = null; this._disposeMapGroup(); }
    }
  },

  _buildMapMesh() {
    if (!this._decoded || !this.scene) return;
    this._disposeMapGroup();
    const { positions, bounds } = this._decoded;

    const group = new THREE.Group();
    const geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.BufferAttribute(positions, 3));
    geo.computeVertexNormals();

    // Translucent solid shell so the orbit camera can see inside the map.
    const solid = new THREE.MeshPhongMaterial({
      color: 0x6a7886, side: THREE.DoubleSide,
      transparent: true, opacity: 0.55, shininess: 6,
    });
    group.add(new THREE.Mesh(geo, solid));

    // Wireframe overlay picks out structure the translucent fill washes out.
    const wire = new THREE.MeshBasicMaterial({
      color: 0x88aacc, wireframe: true, transparent: true, opacity: 0.12,
    });
    group.add(new THREE.Mesh(geo, wire));

    this.mapGroup = group;
    this.scene.add(group);
    this._removeScaffold();

    if (this._liquids) this._buildLiquids(this._liquids);
    this._frameCamera(bounds);
  },

  // Per-loc coloured floor plan + loc labels (the static base of the 3D map
  // overlays). `data` = { locs:[{name,tris,color,alpha,textColor,centroid}], backdrop }.
  // Safe before init (deferred) and rebuilt on each demo's geometry load.
  setFloors(data) {
    this._floorData = data;
    if (this._inited) this._buildFloors();
  },

  _buildFloors() {
    if (!this.scene) return;
    // Dispose any previous floors first — covers demo switch and setFloors(null)
    // (clear-on-reset), so stale loc floors/labels never linger.
    if (this._floorGroup) {
      this.scene.remove(this._floorGroup);
      disposeTree(this._floorGroup);
      this._floorGroup = null;
      this._locMeshes = null;
    }
    if (!this._floorData) return; // cleared
    this._floorGroup = new THREE.Group();
    this._locMeshes = new Map();
    for (const loc of this._floorData.locs) {
      if (!loc.tris || loc.tris.length < 9) continue;
      const geo = new THREE.BufferGeometry();
      geo.setAttribute('position', new THREE.BufferAttribute(new Float32Array(loc.tris), 3));
      const mat = new THREE.MeshBasicMaterial({
        color: loc.color, transparent: true, opacity: loc.alpha,
        side: THREE.DoubleSide, depthWrite: false,
      });
      const mesh = new THREE.Mesh(geo, mat);
      mesh.userData.baseColor = loc.color;
      mesh.userData.baseAlpha = loc.alpha;
      this._floorGroup.add(mesh);
      this._locMeshes.set(loc.name, mesh);
      if (loc.centroid) {
        const lbl = makeLabelSprite(loc.name, loc.textColor);
        lbl.position.set(loc.centroid.x, loc.centroid.y, (loc.centroid.z || 0) + 10);
        this._floorGroup.add(lbl);
      }
    }
    const bd = this._floorData.backdrop;
    if (bd && bd.length >= 9) {
      const geo = new THREE.BufferGeometry();
      geo.setAttribute('position', new THREE.BufferAttribute(new Float32Array(bd), 3));
      this._floorGroup.add(new THREE.Mesh(geo, new THREE.MeshBasicMaterial({
        color: 0x46506e, transparent: true, opacity: 0.22, side: THREE.DoubleSide, depthWrite: false,
      })));
    }
    this.scene.add(this._floorGroup);
  },

  // Add translucent liquid volumes. `liquids` is the geometry JSON's
  // liquids array: [{kind, tris:[x,y,z, ... 9 floats/tri]}].
  setLiquids(liquids) {
    this._liquids = Array.isArray(liquids) ? liquids : null;
    if (this._inited && this.mapGroup) this._buildLiquids(this._liquids);
  },

  _buildLiquids(liquids) {
    if (!liquids || !this.mapGroup) return;
    // Drop any previously built liquid meshes (tagged) before rebuilding.
    for (const child of [...this.mapGroup.children]) {
      if (child.userData.isLiquid) { this.mapGroup.remove(child); disposeTree(child); }
    }
    for (const lq of liquids) {
      if (!lq || !Array.isArray(lq.tris) || lq.tris.length < 9) continue;
      const geo = new THREE.BufferGeometry();
      geo.setAttribute('position', new THREE.BufferAttribute(new Float32Array(lq.tris), 3));
      geo.computeVertexNormals();
      const mat = new THREE.MeshPhongMaterial({
        color: LIQUID_COLORS[lq.kind] || LIQUID_COLORS.water,
        side: THREE.DoubleSide, transparent: true, opacity: 0.4,
      });
      const mesh = new THREE.Mesh(geo, mat);
      mesh.userData.isLiquid = true;
      this.mapGroup.add(mesh);
    }
  },

  // Aim the orbit camera at the map centre from an isometric 3/4 angle, far
  // enough out to frame the whole bounding box.
  _frameCamera(b) {
    this._mapBounds = b;
    const cx = (b.minX + b.maxX) / 2, cy = (b.minY + b.maxY) / 2, cz = (b.minZ + b.maxZ) / 2;
    const dx = b.maxX - b.minX, dy = b.maxY - b.minY, dz = b.maxZ - b.minZ;
    const radius = Math.max(dx, dy, dz, 256);
    this.controls.target.set(cx, cy, cz);
    this.camera.position.set(cx + radius * 0.9, cy - radius * 0.9, cz + radius * 0.8);
    this.camera.near = Math.max(1, radius / 200);
    this.camera.far = radius * 20;
    this.camera.updateProjectionMatrix();
    this.controls.update();
  },

  show() {
    if (!this.canvas) return;
    this.canvas.style.display = 'block';
    this._visible = true;
    if (!this._rafId) this._loop();
  },

  hide() {
    if (this.canvas) this.canvas.style.display = 'none';
    this._visible = false;
    if (this._rafId) { cancelAnimationFrame(this._rafId); this._rafId = 0; }
  },

  resize(w, h, dpr) {
    if (!this.renderer) return;
    this.renderer.setPixelRatio(dpr || window.devicePixelRatio || 1);
    this.renderer.setSize(w, h, false); // false: don't touch CSS size (app owns layout)
    this.camera.aspect = w / h;
    this.camera.updateProjectionMatrix();
  },

  // Per-frame push from app.js's adapter: update the entity layer to match the
  // current timeline instant. The internal rAF loop does the actual drawing.
  renderFrame(state) {
    if (!this._inited || !state) return;
    this._syncPlayers(state.players || [], { showVel: state.showVel, showView: state.showView });
    this._syncItems(state.items || []);
    this._syncMarkers(state.deaths || [], state.drops || []);
    this._syncTrails(state.trails || []);
    this._syncFloorOverlay(state.occupancy || []);
    this._syncMovers(state.movers || []);
    this._syncEntities(state.entities || [], state.teleArrows || []);
    this._applyFollow(state.follow);
  },

  // Follow mode: keep the orbit pivot on the tracked player, shifting the
  // camera by the same delta so the user's chosen view angle is preserved.
  _applyFollow(follow) {
    if (!follow || !this.controls) return;
    const t = this.controls.target;
    const dx = follow.x - t.x, dy = follow.y - t.y, dz = follow.z - t.z;
    t.set(follow.x, follow.y, follow.z);
    this.camera.position.set(this.camera.position.x + dx, this.camera.position.y + dy, this.camera.position.z + dz);
  },

  // Re-frame the camera to the loaded map (the Reset view button).
  resetCamera() {
    if (this._mapBounds) this._frameCamera(this._mapBounds);
  },

  // Submodel rest meshes (brush-model lifts/doors) keyed by id — the mover
  // renderer offsets these by the per-frame pose origin.
  setSubmodels(map) { this._submodels = map || {}; },

  _syncMovers(list) {
    if (!this._moverGroup) { this._moverGroup = new THREE.Group(); this.entities.add(this._moverGroup); this._movers = new Map(); }
    const seen = new Set();
    for (const mv of list) {
      seen.add(mv.i);
      let obj = this._movers.get(mv.i);
      if (!obj) {
        const tris = this._submodels && this._submodels[mv.sub];
        if (!tris || tris.length < 9) continue;
        const geo = new THREE.BufferGeometry();
        geo.setAttribute('position', new THREE.BufferAttribute(new Float32Array(tris), 3));
        geo.computeVertexNormals();
        const mesh = new THREE.Mesh(geo, new THREE.MeshPhongMaterial({
          color: 0x8a93b8, transparent: true, opacity: 0.55, side: THREE.DoubleSide,
        }));
        this._moverGroup.add(mesh);
        obj = { mesh };
        this._movers.set(mv.i, obj);
      }
      obj.mesh.position.set(mv.x, mv.y, mv.z);
      obj.mesh.visible = mv.vis;
    }
    for (const [i, obj] of this._movers) if (!seen.has(i)) obj.mesh.visible = false;
  },

  // Recolour loc floor meshes for the current frame's occupancy/region-control
  // tint, resetting un-tinted locs to their static base colour.
  _syncFloorOverlay(list) {
    if (!this._locMeshes) return;
    for (const [, mesh] of this._locMeshes) {
      if (mesh.userData.tinted) {
        mesh.material.color.setHex(mesh.userData.baseColor);
        mesh.material.opacity = mesh.userData.baseAlpha;
        mesh.userData.tinted = false;
      }
    }
    for (const o of list) {
      const mesh = this._locMeshes.get(o.name);
      if (!mesh) continue;
      mesh.material.color.setHex(o.color);
      mesh.material.opacity = Math.max(mesh.userData.baseAlpha, o.alpha);
      mesh.userData.tinted = true;
    }
  },

  // Drop all entity meshes (called on demo switch so a new roster starts clean).
  resetEntities() {
    if (!this._players) return;
    for (const [, obj] of this._players) { this.entities.remove(obj.group); disposeTree(obj.group); }
    this._players.clear();
    if (this._itemGroup) { this.entities.remove(this._itemGroup); disposeTree(this._itemGroup); this._itemGroup = null; }
    this._items = null;
    if (this._markerGroup) { this.entities.remove(this._markerGroup); disposeTree(this._markerGroup); this._markerGroup = null; }
    if (this._trailGroup) { this.entities.remove(this._trailGroup); disposeTree(this._trailGroup); this._trailGroup = null; }
    if (this._moverGroup) { this.entities.remove(this._moverGroup); disposeTree(this._moverGroup); this._moverGroup = null; this._movers = null; }
    if (this._entGroup) { this.entities.remove(this._entGroup); disposeTree(this._entGroup); this._entGroup = null; this._entKey = null; }
    // Floors are per-demo too: clear them so a demo whose geometry fails to
    // load doesn't keep the previous map's loc floors/labels. New geometry
    // rebuilds via setFloors().
    this._floorData = null;
    this._buildFloors();
  },

  _syncPlayers(list, flags) {
    const seen = new Set();
    for (const p of list) {
      if (!p || !p.name) continue;
      seen.add(p.name);
      let obj = this._players.get(p.name);
      if (!obj) { obj = makePlayer(p); this._players.set(p.name, obj); this.entities.add(obj.group); }
      updatePlayer(obj, p, flags);
      obj.group.visible = true;
    }
    for (const [name, obj] of this._players) if (!seen.has(name)) obj.group.visible = false;
  },

  // Learn-mode static entities (item layout / spawns / teleporters / buttons /
  // doors) + teleport arrows. Rebuilt when the set changes (toggle/filter).
  _syncEntities(entities, teleArrows) {
    if (!this._entGroup) { this._entGroup = new THREE.Group(); this.entities.add(this._entGroup); }
    // Content signature, not just counts: filter toggles can swap which
    // entities show without changing the total, so fold position+shape in.
    let sig = 0;
    for (const e of entities) sig = (sig * 31 + (e.x | 0) + (e.y | 0) * 7 + e.shape.charCodeAt(0)) | 0;
    const key = entities.length + '|' + teleArrows.length + '|' + sig;
    if (key === this._entKey) return; // unchanged (static per learn-mode config)
    this._entKey = key;
    for (const c of this._entGroup.children) { if (c.geometry) c.geometry.dispose(); if (c.material) c.material.dispose(); }
    this._entGroup.clear();
    for (const e of entities) {
      let mesh;
      if (e.shape === 'tdst') {
        mesh = new THREE.Mesh(new THREE.SphereGeometry(10, 10, 8), new THREE.MeshBasicMaterial({ color: e.color, transparent: true, opacity: 0.7 }));
      } else {
        mesh = new THREE.Mesh(new THREE.BoxGeometry(16, 16, 16), new THREE.MeshBasicMaterial({ color: e.color, transparent: true, opacity: 0.55 }));
      }
      mesh.position.set(e.x, e.y, e.z + 10);
      this._entGroup.add(mesh);
    }
    for (const a of teleArrows) {
      const geo = new THREE.BufferGeometry();
      geo.setAttribute('position', new THREE.BufferAttribute(new Float32Array([a.sx, a.sy, a.sz, a.dx, a.dy, a.dz]), 3));
      this._entGroup.add(new THREE.Line(geo, new THREE.LineBasicMaterial({ color: 0xb388ff, transparent: true, opacity: 0.55 })));
    }
  },

  // Items have static positions; only their up/taken status changes. Build the
  // mesh pool once per demo, then just tweak opacity each frame.
  _syncItems(list) {
    if (!this._itemGroup) {
      this._itemGroup = new THREE.Group();
      this.entities.add(this._itemGroup);
      this._items = [];
    }
    if (this._items.length !== list.length) {
      // Roster of items changed (new demo) — rebuild.
      for (const it of this._items) disposeTree(it.mesh);
      this._itemGroup.clear();
      this._items = list.map((it) => {
        const mesh = new THREE.Mesh(
          new THREE.OctahedronGeometry(13),
          new THREE.MeshPhongMaterial({ color: it.color, emissive: it.color, emissiveIntensity: 0.25, transparent: true }),
        );
        mesh.position.set(it.x, it.y, it.z + 14);
        this._itemGroup.add(mesh);
        return { mesh };
      });
    }
    for (let i = 0; i < list.length; i++) {
      this._items[i].mesh.material.opacity = list[i].up ? 0.95 : 0.18;
    }
  },

  // Player movement trails. Rebuilt each frame from the windowed point lists;
  // a point flagged `cut` starts a new segment (death→spawn gaps stay open).
  _syncTrails(trails) {
    if (!this._trailGroup) { this._trailGroup = new THREE.Group(); this.entities.add(this._trailGroup); }
    for (const c of this._trailGroup.children) { c.geometry.dispose(); c.material.dispose(); }
    this._trailGroup.clear();
    for (const tr of trails) {
      const pts = tr.pts;
      const solid = [], tele = [];
      for (let i = 1; i < pts.length; i++) {
        if (pts[i].cut) continue;
        const a = pts[i - 1], b = pts[i];
        (pts[i].tp ? tele : solid).push(a.x, a.y, a.z, b.x, b.y, b.z);
      }
      this._addTrailSeg(solid, tr.color, 0.5);  // normal movement
      this._addTrailSeg(tele, tr.color, 0.18);  // teleport passages (fainter)
      // Spawn / death dots along the trail history.
      for (const p of pts) {
        if (!p.spawn && !p.death) continue;
        const dot = new THREE.Mesh(
          new THREE.SphereGeometry(p.death ? 6 : 5, 8, 6),
          new THREE.MeshBasicMaterial({ color: tr.color, transparent: true, opacity: 0.85 }),
        );
        dot.position.set(p.x, p.y, p.z);
        this._trailGroup.add(dot);
      }
    }
  },

  _addTrailSeg(arr, color, opacity) {
    if (arr.length === 0) return;
    const geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.BufferAttribute(new Float32Array(arr), 3));
    this._trailGroup.add(new THREE.LineSegments(geo, new THREE.LineBasicMaterial({ color, transparent: true, opacity })));
  },

  // Death (X) and drop (backpack) markers are transient; rebuild the small set
  // each frame from the fade-windowed list.
  _syncMarkers(deaths, drops) {
    if (!this._markerGroup) { this._markerGroup = new THREE.Group(); this.entities.add(this._markerGroup); }
    // Dispose last frame's transient markers before rebuilding (the shared X
    // texture is cached separately, so material.dispose() won't free it).
    for (const c of this._markerGroup.children) {
      if (c.geometry) c.geometry.dispose();
      if (c.material) c.material.dispose();
    }
    this._markerGroup.clear();
    for (const d of deaths) {
      const spr = new THREE.Sprite(new THREE.SpriteMaterial({
        map: xMarkerTexture(), color: d.color, transparent: true, opacity: d.alpha, depthTest: false,
      }));
      spr.scale.set(38, 38, 1);
      spr.position.set(d.x, d.y, d.z);
      this._markerGroup.add(spr);
    }
    for (const d of drops) {
      const m = new THREE.Mesh(
        new THREE.BoxGeometry(16, 16, 16),
        new THREE.MeshBasicMaterial({ color: 0xc9b46a, transparent: true, opacity: 0.5 * d.alpha }),
      );
      m.position.set(d.x, d.y, d.z + 8);
      this._markerGroup.add(m);
    }
  },

  _loop() {
    this._rafId = requestAnimationFrame(() => this._loop());
    this._render();
  },

  _render() {
    if (!this._inited) return;
    if (this.controls) this.controls.update();
    this.renderer.render(this.scene, this.camera);
  },

  _disposeMapGroup() {
    if (this.mapGroup) { this.scene.remove(this.mapGroup); disposeTree(this.mapGroup); this.mapGroup = null; }
  },

  dispose() {
    this.hide();
    this.resetEntities();            // players/items/markers/trails/movers/entities/floors
    this._disposeMapGroup();
    this._removeScaffold();
    if (this.controls) this.controls.dispose();
    if (_xTex) { _xTex.dispose(); _xTex = null; } // shared death-marker texture
    if (this.renderer) {
      this.renderer.dispose();
      this.renderer.forceContextLoss?.();
    }
    this._inited = false;
    this.scene = null;
    this.camera = null;
    this.controls = null;
    this.renderer = null;
    this._decoded = null;
    this._mapName = null;
  },
};

// decodeM3D1 parses the little-endian M3D1 blob emitted by mapgeom.EncodeMesh3D
// into a Float32Array of triangle-soup positions plus the 3D bounds.
function decodeM3D1(buf) {
  const dv = new DataView(buf);
  const magic = String.fromCharCode(dv.getUint8(0), dv.getUint8(1), dv.getUint8(2), dv.getUint8(3));
  if (magic !== 'M3D1') throw new Error('bad mesh magic: ' + magic);
  const triCount = dv.getUint32(8, true);
  const bounds = {
    minX: dv.getFloat32(12, true), minY: dv.getFloat32(16, true), minZ: dv.getFloat32(20, true),
    maxX: dv.getFloat32(24, true), maxY: dv.getFloat32(28, true), maxZ: dv.getFloat32(32, true),
  };
  const positions = new Float32Array(buf, 36, triCount * 9);
  return { positions, bounds, triCount };
}

// ─── Player avatars ─────────────────────────────────────────────────────────
// Each player is a pooled THREE.Group: a team-coloured sphere at the origin, a
// view-direction cone, a floor-anchor stem (a vertical line down to the floor
// beneath, mirroring the 2D drawPlayerFloorStem), and a name label sprite.

const PLAYER_R = 15;   // sphere radius (player box is ~32 wide)
const CONE_LEN = 56;   // view cone length (world units)
const CONE_R = 15;     // view cone base radius
const CONE_UP = new THREE.Vector3(0, 1, 0); // ConeGeometry's default axis
const _dir = new THREE.Vector3();           // reused per update

function makePlayer(p) {
  const group = new THREE.Group();

  const sphere = new THREE.Mesh(
    new THREE.SphereGeometry(PLAYER_R, 16, 12),
    new THREE.MeshPhongMaterial({ color: p.color, shininess: 20 }),
  );
  group.add(sphere);

  const cone = new THREE.Mesh(
    new THREE.ConeGeometry(CONE_R, CONE_LEN, 16, 1, true),
    new THREE.MeshBasicMaterial({ color: p.color, transparent: true, opacity: 0.4, side: THREE.DoubleSide }),
  );
  group.add(cone);

  // Floor stem: a 2-vertex line in group-local coords (origin → straight down).
  const stemPos = new Float32Array(6);
  const stemGeo = new THREE.BufferGeometry();
  stemGeo.setAttribute('position', new THREE.BufferAttribute(stemPos, 3));
  const stem = new THREE.Line(stemGeo, new THREE.LineBasicMaterial({ color: p.color, transparent: true, opacity: 0.55 }));
  group.add(stem);

  const label = makeLabelSprite(p.name, p.color);
  label.position.set(0, 0, PLAYER_R + 20);
  group.add(label);

  // Velocity arrow (opt-in via the Vel toggle): a line from the origin whose
  // length encodes speed. Own geometry so disposeTree is safe.
  const velPos = new Float32Array(6);
  const velGeo = new THREE.BufferGeometry();
  velGeo.setAttribute('position', new THREE.BufferAttribute(velPos, 3));
  const velArrow = new THREE.Line(velGeo, new THREE.LineBasicMaterial({ color: p.color }));
  velArrow.visible = false;
  group.add(velArrow);

  // Held-item badge dots — up to 8 in a ring around the sphere, toggled per
  // frame. Each gets its own geometry (shared geo would double-dispose).
  const badges = [];
  for (let i = 0; i < 8; i++) {
    const dot = new THREE.Mesh(new THREE.SphereGeometry(4, 8, 6), new THREE.MeshBasicMaterial({ color: 0xffffff }));
    dot.visible = false;
    group.add(dot);
    badges.push(dot);
  }

  return { group, sphere, cone, stem, stemPos, label, velArrow, velPos, badges, color: p.color };
}

function updatePlayer(obj, p, flags) {
  obj.group.position.set(p.x, p.y, p.z);

  if (p.color !== obj.color) {
    obj.sphere.material.color.setHex(p.color);
    obj.cone.material.color.setHex(p.color);
    obj.stem.material.color.setHex(p.color);
    obj.color = p.color;
  }

  // Dead players dim out (deaths also get a separate marker in a later phase).
  obj.sphere.material.transparent = !!p.dead;
  obj.sphere.material.opacity = p.dead ? 0.35 : 1.0;

  // View-direction cone, oriented along the Quake forward vector — shown when
  // the View toggle is on (parity with the 2D opt-in view arrows).
  if (p.yaw != null && flags && flags.showView) {
    obj.cone.visible = true;
    const cp = Math.cos(p.pitch || 0);
    _dir.set(cp * Math.cos(p.yaw), cp * Math.sin(p.yaw), -Math.sin(p.pitch || 0));
    if (_dir.lengthSq() > 1e-6) {
      _dir.normalize();
      obj.cone.quaternion.setFromUnitVectors(CONE_UP, _dir);
      obj.cone.position.copy(_dir).multiplyScalar(PLAYER_R + CONE_LEN / 2);
    }
  } else {
    obj.cone.visible = false;
  }

  // Stem reaches from the sphere centre down to the floor beneath (local z).
  const dz = (typeof p.floorZ === 'number') ? (p.floorZ - p.z) : -PLAYER_R;
  obj.stemPos[5] = dz;
  obj.stem.geometry.attributes.position.needsUpdate = true;

  // Velocity arrow (opt-in): length ∝ speed (5 u/s per world unit, min 10 u/s).
  const va = obj.velArrow;
  if (flags && flags.showVel && typeof p.vx === 'number') {
    const sp = Math.hypot(p.vx, p.vy, p.vz);
    if (sp > 10) {
      const inv = (sp / 5) / sp; // unit-vector × length, fused
      obj.velPos[3] = p.vx * inv; obj.velPos[4] = p.vy * inv; obj.velPos[5] = p.vz * inv;
      va.geometry.attributes.position.needsUpdate = true;
      va.material.color.setHex(p.color);
      va.visible = true;
    } else { va.visible = false; }
  } else { va.visible = false; }

  // Held-item badge dots, arranged in a ring around the sphere.
  const list = p.badges || [];
  for (let i = 0; i < obj.badges.length; i++) {
    const dot = obj.badges[i];
    if (i < list.length) {
      const R = PLAYER_R + 7;
      dot.position.set(Math.cos(list[i].angle) * R, Math.sin(list[i].angle) * R, PLAYER_R + 2);
      dot.material.color.setHex(list[i].color);
      dot.visible = true;
    } else {
      dot.visible = false;
    }
  }
}

// makeLabelSprite renders text to a canvas texture and returns a camera-facing
// sprite. depthTest off so labels stay readable through the translucent shell.
function makeLabelSprite(text, colorInt) {
  const pad = 8, H = 40, font = 'bold 26px Inter, sans-serif';
  const meas = document.createElement('canvas').getContext('2d');
  meas.font = font;
  const tw = Math.ceil(meas.measureText(text).width);
  const canvas = document.createElement('canvas');
  canvas.width = tw + pad * 2;
  canvas.height = H;
  const ctx = canvas.getContext('2d');
  ctx.font = font;
  ctx.fillStyle = 'rgba(10,10,21,0.55)';
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.fillStyle = '#' + (colorInt >>> 0).toString(16).padStart(6, '0');
  ctx.textBaseline = 'middle';
  ctx.textAlign = 'left';
  ctx.fillText(text, pad, H / 2 + 1);
  const tex = new THREE.CanvasTexture(canvas);
  tex.minFilter = THREE.LinearFilter;
  tex.colorSpace = THREE.SRGBColorSpace;
  const spr = new THREE.Sprite(new THREE.SpriteMaterial({ map: tex, depthTest: false, transparent: true }));
  const worldH = 34;
  spr.scale.set(worldH * canvas.width / canvas.height, worldH, 1);
  return spr;
}

// xMarkerTexture lazily builds a shared white "X" sprite texture (tinted per
// marker via the SpriteMaterial colour) for death markers.
let _xTex = null;
function xMarkerTexture() {
  if (_xTex) return _xTex;
  const s = 64, canvas = document.createElement('canvas');
  canvas.width = canvas.height = s;
  const ctx = canvas.getContext('2d');
  ctx.strokeStyle = '#ffffff';
  ctx.lineWidth = 9;
  ctx.lineCap = 'round';
  const p = 14;
  ctx.beginPath();
  ctx.moveTo(p, p); ctx.lineTo(s - p, s - p);
  ctx.moveTo(s - p, p); ctx.lineTo(p, s - p);
  ctx.stroke();
  _xTex = new THREE.CanvasTexture(canvas);
  _xTex.minFilter = THREE.LinearFilter;
  return _xTex;
}

// disposeTree releases GPU resources for an Object3D subtree, including the
// CanvasTextures behind label/marker sprites (Three.js does not free a
// material's textures on material.dispose()). The shared death-marker texture
// (_xTex) is intentionally preserved — it is reused across every marker.
function disposeTree(obj) {
  obj.traverse((o) => {
    if (o.geometry) o.geometry.dispose();
    if (o.material) {
      const mats = Array.isArray(o.material) ? o.material : [o.material];
      for (const m of mats) {
        if (m.map && m.map !== _xTex) m.map.dispose();
        m.dispose();
      }
    }
  });
}

window.MapGL = MapGL;
