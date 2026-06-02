# MVD Demo Format Specification

## Overview

MVD (Multi-View Demo) is a demo recording format for QuakeWorld that captures the complete game state from the server's perspective. Unlike QWD (QuakeWorld Demo) which records a single player's view, MVD records all players simultaneously, allowing spectators to switch between any player's point of view during playback.

### Key Characteristics

- **Server-side recording**: Captures all player states and game events
- **Multi-view support**: Viewer can switch between any player
- **Delta compression**: Only changed values are transmitted
- **Streaming support**: Can be streamed via QTV (QuakeTV) protocol
- **Time representation**: Millisecond deltas (not absolute time like QWD)
- **Server frame rate**: Typically ~77 Hz (MVDSV default `sys_maxfps`). Position updates (`svc_playerinfo`) are emitted every server frame for all players (~73 Hz observed), while stat updates (`svc_updatestat`) are event-driven and arrive at ~3 Hz per player

### File Extensions

| Extension | Description |
|-----------|-------------|
| `.mvd` | Standard MVD demo file |
| `.mvd.gz` | Gzip-compressed MVD |
| `.mvd.bz2` | Bzip2-compressed MVD |

### Real-World Example

A typical 20-minute 4on4 competitive match:
- **File size**: ~13 MB (uncompressed)
- **Messages**: ~213,000 demo messages
- **Message types breakdown**:
  - `dem_all`: Most common (~80%)
  - `dem_stats`: Per-player stat updates
  - `dem_single`: Player-specific messages
  - `dem_multiple`: Multi-player messages (including hidden messages)

---

## Binary Format Conventions

All multi-byte values are stored in **little-endian** byte order.

### Data Types

| Type | Size | Description |
|------|------|-------------|
| `byte` | 1 | Unsigned 8-bit integer (0-255) |
| `char` | 1 | Signed 8-bit integer (-128 to 127) |
| `short` | 2 | Signed 16-bit integer |
| `ushort` | 2 | Unsigned 16-bit integer |
| `long` / `int` | 4 | Signed 32-bit integer |
| `float` | 4 | IEEE 754 single-precision float |
| `coord` | 2 | Position coordinate: `value * 8` (gives 1/8 unit precision) |
| `angle` | 1 | Angle: `value * (360.0 / 256.0)` degrees |
| `angle16` | 2 | Angle: `value * (360.0 / 65536.0)` degrees |
| `string` | variable | Null-terminated ASCII string |

### Coordinate Encoding

Standard coordinates are encoded as 16-bit signed integers:
```
encoded_value = (int)(real_value * 8)
real_value = encoded_value / 8.0
```

With `FTE_PEXT_FLOATCOORDS` or `MVD_PEXT1_FLOATCOORDS`, coordinates are 32-bit floats.

### Character Encoding

QuakeWorld uses a custom 8-bit character encoding inherited from id Software's Quake font:

- **0x00–0x1F**: Special font glyphs — bracket-digits (`[0]…[9]`), brackets, dots, arrows. **Not generic control characters.** They render as visible icons in the Quake font.
- **0x20–0x7E**: Standard printable ASCII (white text).
- **0x7F**: DEL — renders as `>` in the Quake font.
- **0x80–0xFF**: "Gold" (alternate color) glyphs. Each byte `b` in this range renders identically to byte `b - 0x80`, just in gold/brown instead of white.

Player names, chat messages, and obituary text may use any of these. **Naively stripping all bytes `< 0x20` is wrong** — it loses legitimate name characters. The canonical normalization is the ezquake/mvdsv `Q_normalizetext` table; see [Player Name Normalization](#player-name-normalization) below.

### Text Rendering and Color Codes

*Source: ezQuake `r_draw_charset.c`, `fonts.c`, `console.c`*

#### High-Bit "Gold/Brown" Characters

Characters with values 128-255 are rendered using the same glyphs as characters 0-127, but displayed in a "gold" or "brown" color instead of white. This is commonly used for:
- Highlighted text in the console
- Alternate styling in player names
- Special markers in messages

**ezQuake Gradient Colors** (from `fonts.c`):
| Type | Top Color | Bottom Color |
|------|-----------|--------------|
| Normal (white) | rgb(255, 255, 255) | rgb(107, 98, 86) |
| Alternate (gold/brown) | rgb(175, 120, 52) | rgb(75, 52, 22) |
| Numbers | rgb(255, 255, 150) | rgb(218, 132, 7) |

**Conversion** (for *display* — i.e. picking which glyph and which color to render):
```go
func convertQuakeChar(c byte) (displayChar byte, isGold bool) {
    if c >= 128 {
        return c - 128, true  // Gold/brown character
    }
    return c, false  // Normal white character
}
```

> ⚠️ This conversion is for **rendering** only. It does not produce a string suitable for cross-source joins or for use as a map key, because it leaves bytes `0x00–0x1F` unchanged (so a name like `[bbb]` stays as `\x10 bbb \x11` and won't match a JSON-decoded `[bbb]`). For string-comparison purposes use [`Q_normalizetext`](#player-name-normalization) instead.

#### Inline Color Codes

Modern QuakeWorld clients support inline color codes in text strings:

| Code | Format | Description |
|------|--------|-------------|
| `&cRGB` | 3 hex digits | Set text color to RGB |
| `&cfff` | Special case | Reset to white (equivalent to `&r`) |
| `&r` | Reset | Reset color to default (white) |

**Color Calculation**:
```c
// ezQuake r_draw_charset.c
rgba[0] = (r * 16);  // R hex digit (0-F) -> 0-240
rgba[1] = (g * 16);  // G hex digit (0-F) -> 0-240
rgba[2] = (b * 16);  // B hex digit (0-F) -> 0-240
```

**Examples**:
- `&cf00` = Red (rgb(240, 0, 0))
- `&c0f0` = Green (rgb(0, 240, 0))
- `&c00f` = Blue (rgb(0, 0, 240))
- `&cff0` = Yellow (rgb(240, 240, 0))
- `&c888` = Gray (rgb(128, 128, 128))

**Parsing Algorithm**:
```javascript
function parseQuakeText(text) {
    let output = '';
    let currentColor = null;
    let i = 0;

    while (i < text.length) {
        const charCode = text.charCodeAt(i);

        // Check for &c color code
        if (text.slice(i, i + 2) === '&c') {
            const colorMatch = text.slice(i + 2, i + 5).match(/^[0-9a-fA-F]{3}/);
            if (colorMatch) {
                const [r, g, b] = colorMatch[0].split('').map(h => parseInt(h, 16) * 16);
                currentColor = `rgb(${r},${g},${b})`;
                i += 5;
                continue;
            }
        }

        // Check for &r reset code
        if (text.slice(i, i + 2) === '&r') {
            currentColor = null;
            i += 2;
            continue;
        }

        // Handle high-bit gold characters
        if (charCode >= 128 && charCode <= 255) {
            const baseChar = String.fromCharCode(charCode - 128);
            output += formatWithColor(baseChar, currentColor || 'gold');
            i++;
            continue;
        }

        // Regular character
        output += formatWithColor(text[i], currentColor || 'white');
        i++;
    }

    return output;
}
```

#### Sound Triggers

Chat messages may end with sound trigger codes that cause the client to play sounds:

| Code | Sound | Description |
|------|-------|-------------|
| `!K` | Kill sound | Played on frag events |
| `!H` | Hit sound | Played on damage |
| `!G` | Generic sound | Various notifications |
| `!C` | Chat sound | Chat notification |

These should be stripped when displaying text, as they are only meaningful for audio playback.

---

## File Structure

An MVD file is a sequential stream of **demo messages**. There is no file header - parsing begins immediately with the first message.

```
[Message 1]  <- Usually dem_set (sequence initialization)
[Message 2]  <- Usually dem_all with svc_serverdata
[Message 3]
...
[End Message] <- dem_all with svc_disconnect "EndOfDemo"
```

### First Message Sequence

A typical MVD file begins with:
1. `dem_set` - Sequence numbers (first 10 bytes)
2. `dem_all` - Contains `svc_serverdata` with protocol extensions and server info

---

## Message Structure

Each message consists of a header followed by type-specific payload.

### Message Header

```
Offset  Size  Field
------  ----  -----
0       1     time_delta    - Milliseconds since previous message (0-255)
1       1     message_type  - Message type (see below)
```

The `message_type` byte encodes both the type and sometimes additional data:

```
Bits 0-2: Message type (0-7)
Bits 3-7: Additional data (for dem_single and dem_stats: player number)
```

**Important**: The first message typically has `time_delta = 0`. Subsequent messages accumulate time deltas to track demo time.

The decoder accumulates these deltas as an `int32` cumulative millisecond counter (`mvd.Decoder.timeMs`) — the canonical, exact wire-native value. A derived `float64` seconds view (`CurrentTime()`) is exposed for callers that prefer seconds arithmetic, but persistence-layer consumers should use the integer-ms value to avoid float-precision drift across boundary comparisons. The analytics layer's schema v8 stores **every** timestamped result field as `int32` ms for the same reason — the wire format's integer unit flows end-to-end through the pipeline.

### Message Types

| Value | Name | Description |
|-------|------|-------------|
| 0 | `dem_cmd` | User command (QWD only, not used in MVD) |
| 1 | `dem_read` | Network message packet |
| 2 | `dem_set` | Sequence numbers (once at demo start) |
| 3 | `dem_multiple` | Message for multiple players (MVD only) |
| 4 | `dem_single` | Message for single player (MVD only) |
| 5 | `dem_stats` | Stats update for player (MVD only) |
| 6 | `dem_all` | Message for all players (MVD only) |

---

## Message Type Details

### dem_set (type 2)

Appears **once** at the beginning of the demo. Sets initial sequence numbers.

```
Offset  Size  Field
------  ----  -----
0       1     time_delta (usually 0)
1       1     message_type (2)
2       4     incoming_sequence (little-endian long)
6       4     outgoing_sequence (little-endian long)
```

**Total size**: 10 bytes

**Note**: This is the only message type that does NOT have a payload_size field - the payload is always exactly 8 bytes (two longs).

### dem_all (type 6)

Message broadcast to all players. Most common message type.

```
Offset  Size  Field
------  ----  -----
0       1     time_delta
1       1     message_type (6)
2       4     payload_size (little-endian long)
6       N     payload (network message data)
```

**Total size**: 6 + payload_size bytes

### dem_multiple (type 3)

Message directed to specific players, identified by a bitmask.

```
Offset  Size  Field
------  ----  -----
0       1     time_delta
1       1     message_type (3)
2       4     player_mask (little-endian long, 32 bits = 32 players)
6       4     payload_size (little-endian long)
10      N     payload (network message data)
```

**Total size**: 10 + payload_size bytes

**Player mask interpretation**: Each bit represents a player slot (0-31). If bit N is set, player N should receive this message.

**Special case - Hidden messages**: When `player_mask == 0`, the message is a "hidden" message containing metadata not displayed to any player. See [Hidden Messages](#hidden-messages) section.

### dem_single (type 4)

Message for a single player. Player number encoded in header byte.

```
Header byte structure:
  Bits 0-2: message_type (4)
  Bits 3-7: player_number (0-31)

Offset  Size  Field
------  ----  -----
0       1     time_delta
1       1     message_type_and_player (type in bits 0-2, player in bits 3-7)
2       4     payload_size (little-endian long)
6       N     payload (network message data)
```

**Extracting player number**:
```c
int message_type = header_byte & 0x07;      // bits 0-2
int player_number = header_byte >> 3;        // bits 3-7
```

**Total size**: 6 + payload_size bytes

### dem_stats (type 5)

Stats update for a specific player. Same encoding as dem_single.

```
Header byte structure:
  Bits 0-2: message_type (5)
  Bits 3-7: player_number (0-31)

Offset  Size  Field
------  ----  -----
0       1     time_delta
1       1     message_type_and_player
2       4     payload_size (little-endian long)
6       N     payload (svc_updatestat messages)
```

**Total size**: 6 + payload_size bytes

---

## Network Message Payload

The payload in dem_read, dem_all, dem_multiple, dem_single, and dem_stats messages contains standard QuakeWorld network messages (svc_* commands).

### Server Command Types (svc_*)

| Value | Name | Description |
|-------|------|-------------|
| 0 | `svc_bad` | Invalid |
| 1 | `svc_nop` | No operation |
| 2 | `svc_disconnect` | Disconnect message |
| 3 | `svc_updatestat` | Update player stat (byte value) |
| 6 | `svc_sound` | Play sound |
| 8 | `svc_print` | Print message |
| 9 | `svc_stufftext` | Stuff command into console |
| 10 | `svc_setangle` | Set view angle |
| 11 | `svc_serverdata` | Server initialization data |
| 12 | `svc_lightstyle` | Set light style |
| 14 | `svc_updatefrags` | Update player frags |
| 16 | `svc_stopsound` | Stop a playing sound (rare in MVD) |
| 19 | `svc_damage` | Local damage feedback to one player |
| 20 | `svc_spawnstatic` | Spawn static entity |
| 21 | `svc_fte_spawnstatic2` | FTE extended static entity |
| 22 | `svc_spawnbaseline` | Entity baseline |
| 23 | `svc_temp_entity` | Temporary entity (explosion, etc.) |
| 24 | `svc_setpause` | Pause game |
| 26 | `svc_centerprint` | Center screen message |
| 27 | `svc_killedmonster` | Monster killed (no payload) |
| 28 | `svc_foundsecret` | Secret found (no payload) |
| 29 | `svc_spawnstaticsound` | Static sound |
| 30 | `svc_intermission` | Intermission screen |
| 31 | `svc_finale` | Finale text |
| 32 | `svc_cdtrack` | CD track number |
| 33 | `svc_sellscreen` | Sell screen |
| 34 | `svc_smallkick` | Small view kick (no payload) |
| 35 | `svc_bigkick` | Big view kick (no payload) |
| 36 | `svc_updateping` | Update player ping |
| 37 | `svc_updateentertime` | Update player enter time |
| 38 | `svc_updatestatlong` | Update player stat (long value) |
| 39 | `svc_muzzleflash` | Muzzle flash effect |
| 40 | `svc_updateuserinfo` | Update player userinfo |
| 41 | `svc_download` | File download chunk |
| 42 | `svc_playerinfo` | Player state update |
| 43 | `svc_nails` | Nail projectiles |
| 44 | `svc_chokecount` | Choked packet count |
| 45 | `svc_modellist` | Model precache list |
| 46 | `svc_soundlist` | Sound precache list |
| 47 | `svc_packetentities` | Entity updates |
| 48 | `svc_deltapacketentities` | Delta entity updates |
| 49 | `svc_maxspeed` | Max speed setting |
| 50 | `svc_entgravity` | Entity gravity setting |
| 51 | `svc_setinfo` | Set player info key |
| 52 | `svc_serverinfo` | Set server info key |
| 53 | `svc_updatepl` | Update packet loss |
| 54 | `svc_nails2` | Nail projectiles (MVD extended) |
| 60 | `svc_fte_modellistshort` | FTE short model list |
| 66 | `svc_fte_spawnbaseline2` | FTE extended baseline |

### Parsing Payloads

Each payload may contain multiple svc_* commands. Parse commands sequentially until the payload is exhausted:

```go
for !reader.EOF() {
    cmd := reader.ReadByte()
    switch cmd {
    case SVC_SERVERDATA:
        parseServerData(reader)
    case SVC_PRINT:
        parsePrint(reader)
    // ... handle other commands
    default:
        // Unknown command - must skip appropriately
    }
}
```

**Important**: If you encounter an unknown command and cannot determine its size, you must skip the rest of the current payload and continue with the next demo message.

---

## svc_serverdata (11) - Server Initialization

First message in the demo, contains complete server setup.

```
Offset  Size  Field
------  ----  -----
0       1     svc_serverdata (11)

// Optional protocol extensions (repeat until PROTOCOL_VERSION seen)
1       4     extension_id (e.g., PROTOCOL_VERSION_FTE, PROTOCOL_VERSION_MVD1)
5       4     extension_flags

// Standard header (after extensions)
N       4     PROTOCOL_VERSION (28)
N+4     4     server_count (spawn count)
N+8     var   game_directory (string, e.g., "qw")
N+?     4     server_time (float)
N+?     var   level_name (string, e.g., "Schloss Adler")

// Movement variables (10 floats)
        4     gravity (float, typically 800.0)
        4     stopspeed (float)
        4     maxspeed (float, typically 320.0)
        4     spectator_maxspeed (float)
        4     accelerate (float)
        4     airaccelerate (float)
        4     wateraccelerate (float)
        4     friction (float)
        4     waterfriction (float)
        4     entgravity (float, typically 1.0)
```

### Protocol Extension IDs

| ID | Name | Value | ASCII |
|----|------|-------|-------|
| `PROTOCOL_VERSION_FTE` | FTE extensions | 0x58455446 | "FTEX" |
| `PROTOCOL_VERSION_FTE2` | FTE extensions 2 | 0x32455446 | "FTE2" |
| `PROTOCOL_VERSION_MVD1` | MVD extensions | 0x3144564D | "MVD1" |
| `PROTOCOL_VERSION` | Standard QW | 28 | - |

### Parsing Protocol Extensions

```go
for {
    version := reader.ReadUint32()
    if version == PROTOCOL_VERSION {
        // Standard protocol - done with extensions
        break
    }
    flags := reader.ReadUint32()
    switch version {
    case PROTOCOL_VERSION_FTE:
        fteFlags = flags
    case PROTOCOL_VERSION_FTE2:
        fte2Flags = flags
    case PROTOCOL_VERSION_MVD1:
        mvd1Flags = flags
    }
}
```

### FTE Protocol Extension Flags

```c
#define FTE_PEXT_TRANS              0x00000008  // Alpha transparency
#define FTE_PEXT_HLBSP              0x00000200  // Half-Life BSP support
#define FTE_PEXT_MODELDBL           0x00001000  // Model index > 255
#define FTE_PEXT_ENTITYDBL          0x00002000  // Entity count > 512
#define FTE_PEXT_ENTITYDBL2         0x00004000  // Entity count > 1024
#define FTE_PEXT_FLOATCOORDS        0x00008000  // Float coordinates
#define FTE_PEXT_SPAWNSTATIC2       0x00400000  // Extended static entities
#define FTE_PEXT_COLOURMOD          0x00080000  // RGB color modification
#define FTE_PEXT_256PACKETENTITIES  0x01000000  // 256 packet entities
#define FTE_PEXT_CHUNKEDDOWNLOADS   0x20000000  // Chunked downloads
```

### MVD Protocol Extension Flags

```c
#define MVD_PEXT1_FLOATCOORDS       (1 << 0)  // Float coords for entities
#define MVD_PEXT1_HIGHLAGTELEPORT   (1 << 1)  // Teleport direction fix
#define MVD_PEXT1_HIDDEN_MESSAGES   (1 << 5)  // Hidden message support (0x20)
```

---

## svc_modellist (45) - Model Precache List

Contains the list of models used by the map. The **first model** is always the map BSP file, which provides the authoritative map filename.

```
Offset  Size  Field
------  ----  -----
0       1     svc_modellist (45)
1       1     start_index (usually 0)

// Model list (null-terminated strings until empty string)
?       var   model_1 (string, e.g., "maps/dm2.bsp")  <- Map BSP file
?       var   model_2 (string, e.g., "progs/player.mdl")
...
?       var   model_N (string)
?       1     empty string (0x00 terminator)
?       1     next_index (for continuation, usually 0)
```

### Map Name Sources

There are two places to get the map name, serving different purposes:

| Source | Field | Example | Use Case |
|--------|-------|---------|----------|
| `svc_serverdata` | `level_name` | `"Claustrophobopolis"` | Display name shown to players |
| `svc_modellist` | First model | `"maps/dm2.bsp"` | BSP filename for loading .loc files, etc. |

**Example parsing**:
```go
func parseModelList(r *BufferReader) string {
    r.ReadByte() // skip start_index

    for {
        model := r.ReadString()
        if model == "" {
            break
        }
        // First model is always the map BSP
        if strings.HasPrefix(model, "maps/") {
            // Extract "dm2" from "maps/dm2.bsp"
            return strings.TrimSuffix(strings.TrimPrefix(model, "maps/"), ".bsp")
        }
    }
    r.ReadByte() // skip next_index
    return ""
}
```

---

## svc_playerinfo (42) - Player State Update

This is the core message type for player positions in MVD. The format differs between MVD and standard QWD.

**Update frequency**: Emitted every server frame (~77 Hz) for all players simultaneously. In a typical 4on4 match with 8 players, each `dem_all` message contains 8 `svc_playerinfo` commands — one per player. The median inter-update gap is ~13ms with virtually all gaps under 25ms. Uses delta compression, so only changed coordinates are transmitted per update.

### MVD Format

```
Offset  Size  Field
------  ----  -----
0       1     svc_playerinfo (42)
1       1     player_number (0-31)
2       2     flags (DF_* flags, little-endian short)
4       1     frame (animation frame)

// Conditional fields based on flags:
if (flags & DF_ORIGIN)      2  origin_x (coord or float if FLOATCOORDS)
if (flags & DF_ORIGIN<<1)   2  origin_y (coord or float if FLOATCOORDS)
if (flags & DF_ORIGIN<<2)   2  origin_z (coord or float if FLOATCOORDS)

if (flags & DF_ANGLES)      2  angle_pitch (angle16)
if (flags & DF_ANGLES<<1)   2  angle_yaw (angle16)
if (flags & DF_ANGLES<<2)   2  angle_roll (angle16)

if (flags & DF_MODEL)       1  model_index
if (flags & DF_SKINNUM)     1  skin_number
if (flags & DF_EFFECTS)     1  effects
if (flags & DF_WEAPONFRAME) 1  weapon_frame
```

### DF_* Flags (Delta Flags)

```c
#define DF_ORIGIN       (1 << 0)   // 0x0001 - Origin X present
#define DF_ORIGIN_Y     (1 << 1)   // 0x0002 - Origin Y present
#define DF_ORIGIN_Z     (1 << 2)   // 0x0004 - Origin Z present
#define DF_ANGLES       (1 << 3)   // 0x0008 - Angle pitch present
#define DF_ANGLES_Y     (1 << 4)   // 0x0010 - Angle yaw present
#define DF_ANGLES_Z     (1 << 5)   // 0x0020 - Angle roll present
#define DF_EFFECTS      (1 << 6)   // 0x0040 - Effects byte present
#define DF_SKINNUM      (1 << 7)   // 0x0080 - Skin number present
#define DF_DEAD         (1 << 8)   // 0x0100 - Player is dead
#define DF_GIB          (1 << 9)   // 0x0200 - Player is gibbed
#define DF_WEAPONFRAME  (1 << 10)  // 0x0400 - Weapon frame present
#define DF_MODEL        (1 << 11)  // 0x0800 - Model index present
```

### Model Index Extended Range

When `FTE_PEXT_MODELDBL` is enabled and both `DF_MODEL` and `DF_SKINNUM` are set:
- If `skin_number` has bit 7 set, add 256 to model_index
- Clear bit 7 from skin_number

```c
if ((flags & DF_MODEL) && (flags & DF_SKINNUM)) {
    if (skin_number & 0x80) {
        model_index += 256;
        skin_number &= 0x7F;
    }
}
```

---

## svc_updatestat (3) / svc_updatestatlong (38)

Player statistics update. Sent via `dem_stats` messages directed at specific players.

**Update frequency**: Event-driven, not periodic. Only emitted when a stat value actually changes (e.g., health changes on damage/pickup, ammo changes on fire/pickup). Observed rate is ~3 Hz per player on average, but highly variable — bursts during combat, quiet when idle.

### svc_updatestat (byte value)
```
Offset  Size  Field
------  ----  -----
0       1     svc_updatestat (3)
1       1     stat_index
2       1     stat_value
```

### svc_updatestatlong (long value)
```
Offset  Size  Field
------  ----  -----
0       1     svc_updatestatlong (38)
1       1     stat_index
2       4     stat_value (little-endian long)
```

### Stat Indices

| Index | Name | Description |
|-------|------|-------------|
| 0 | `STAT_HEALTH` | Health points (0-250+) |
| 1 | `STAT_FRAGS` | Frag count |
| 2 | `STAT_WEAPON` | Current weapon model index |
| 3 | `STAT_AMMO` | Current ammo for active weapon |
| 4 | `STAT_ARMOR` | Armor points (0-200) |
| 5 | `STAT_WEAPONFRAME` | Weapon animation frame |
| 6 | `STAT_SHELLS` | Shell ammo count (max 100) |
| 7 | `STAT_NAILS` | Nail ammo count (max 200) |
| 8 | `STAT_ROCKETS` | Rocket ammo count (max 100) |
| 9 | `STAT_CELLS` | Cell ammo count (max 100) |
| 10 | `STAT_ACTIVEWEAPON` | Active weapon flags |
| 11 | `STAT_TOTALSECRETS` | Total secrets in level |
| 12 | `STAT_TOTALMONSTERS` | Total monsters in level |
| 13 | `STAT_SECRETS` | Secrets found |
| 14 | `STAT_MONSTERS` | Monsters killed |
| 15 | `STAT_ITEMS` | Item flags (weapons, armor, powerups) |
| 16 | `STAT_VIEWHEIGHT` | View height offset |
| 17 | `STAT_TIME` | Server time |

### Item Flags (STAT_ITEMS)

```c
#define IT_SHOTGUN              (1 << 0)
#define IT_SUPER_SHOTGUN        (1 << 1)
#define IT_NAILGUN              (1 << 2)
#define IT_SUPER_NAILGUN        (1 << 3)
#define IT_GRENADE_LAUNCHER     (1 << 4)
#define IT_ROCKET_LAUNCHER      (1 << 5)
#define IT_LIGHTNING            (1 << 6)
#define IT_SUPER_LIGHTNING      (1 << 7)   // Unused in standard QW
#define IT_SHELLS               (1 << 8)
#define IT_NAILS                (1 << 9)
#define IT_ROCKETS              (1 << 10)
#define IT_CELLS                (1 << 11)
#define IT_AXE                  (1 << 12)
#define IT_ARMOR1               (1 << 13)  // Green armor (100)
#define IT_ARMOR2               (1 << 14)  // Yellow armor (150)
#define IT_ARMOR3               (1 << 15)  // Red armor (200)
#define IT_SUPERHEALTH          (1 << 16)  // Megahealth
#define IT_KEY1                 (1 << 17)
#define IT_KEY2                 (1 << 18)
#define IT_INVISIBILITY         (1 << 19)  // Ring of Shadows
#define IT_INVULNERABILITY      (1 << 20)  // Pentagram
#define IT_SUIT                 (1 << 21)  // Biosuit
#define IT_QUAD                 (1 << 22)  // Quad Damage
// Bits 23-27 reserved
// Bits 28-31: Server flags (sigils)
```

---

## svc_updatefrags (14)

Updates a player's frag count. The value is the **absolute** frag count, not a delta.

```
Offset  Size  Field
------  ----  -----
0       1     svc_updatefrags (14)
1       1     player_number (0-31)
2       2     frags (signed short, little-endian)
```

### Frag Count Behavior

- The server sends this message whenever `ent->v->frags` changes (checked in `SV_SendClientMessages`)
- The value is **absolute** (total frags), not a delta — the client replaces the stored count entirely
- Frags **increase** on kills (+1 per kill)
- Frags **decrease** on suicides and teamkills (-1 per event)
- Frags can be negative (e.g., multiple suicides before any kills)

*Source: `sv_send.c` — server compares `sv_client->old_frags` to current `ent->v->frags` and broadcasts when changed*

### Usage Note: Frag Detection Methods

There are two approaches to detect frags in MVD files:

| Method | Source | Use Case |
|--------|--------|----------|
| `svc_updatefrags` | Native message | **Recommended for counting frags** — authoritative absolute count from server |
| Obituary parsing | `svc_print` messages | **For frag details** — weapon type, killer/victim names |

To track frags over time, compute deltas between consecutive `svc_updatefrags` values for each player. A delta of +1 indicates a kill, -1 indicates a suicide or teamkill. Multiple frags can occur between updates if the server batches them.

**Recommendation**: Use `svc_updatefrags` for timeline/score tracking. Use obituary parsing only when you need additional details like weapon type.

### Death / Spawn Detection — Three Signal Sources

Per-player "this player just died" / "this player just spawned" boundaries are not a single signal in MVD. Three independent sources contribute, and a complete parser uses all three because each misses cases the others catch:

| # | Signal | Mechanism | Per-player? | Broadcast? | Misses |
|---|---|---|---|---|---|
| 1 | **`STAT_HEALTH` crossings** | `svc_updatestat` health crosses 0 | yes | **directed via `dem_stats`** | Kills whose stat update is bundled in a `dem_stats` block addressed to a different POV player. The recorder must currently be POV'd on (or near) the victim. |
| 2 | **`DF_DEAD` bit in `svc_playerinfo`** | bit 8 of the flag word; set when `ent->v->health <= 0` (mvdsv/src/sv_demo.c) | yes | yes — every recorded frame | Tight respawn cycles compressed into a single inter-frame gap (no sampled frame ever shows `DF_DEAD=1` between two adjacent recorded frames). |
| 3 | **Obituary `svc_print`** | KTX fragfile lines like `"X was rocketed by Y"`, `"X suicides"`, the pent-deflection `"Satan's power deflects X's telefrag"`, etc. | yes (victim is named) | yes | Deaths emitted by mods/death-types with no print path; pre-match obits arriving before the match-start phrase (parser-side `matchStarted` gate). |

KTX's authoritative deaths counter (`logfrag(targ, targ)` and friends — see `ktx/src/client.c`) increments once per kill regardless of which of these wire-level signals fires. Validating reconstructed death counts against KTX's `demoInfo.players[].stats.deaths` is the cleanest end-to-end check.

The mvd-reader parser deduplicates across the three signals so consumers see one `DeathEvent`/`SpawnEvent` per real transition; see `mvd-reader/parser/stats.go` (`maybeEmitDeath` / `maybeEmitSpawn`) and `mvd-reader/parser/print.go` (`tryEmitObituaryDeath`, `forceEmitDeath` — obit bypasses dedup so the dtTELE2 deflection corner case is still counted).

Note that signal #3 only names a victim for *victim-prefix* obituaries. Several KTX teamkill lines name only the **attacker** — `"X mows down a teammate"`, `"X checks his glasses"`, `"X loses another friend"`, `"X gets a frag for the other team"`, `"X squished a teammate"` (`ktx/src/client.c:5288-5331`) — so the victim's death is unattributable from the print and must come from signal #1/#2. A per-player death **count** therefore can't be built from obituary parsing alone; derive it from the deduped `DeathEvent` stream (resolved to the player on that slot) and validate against `demoInfo.players[].stats.deaths`. This is what `mvd-analytics`'s frag analyser does.

---

## svc_updateuserinfo (40)

Player information update.

```
Offset  Size  Field
------  ----  -----
0       1     svc_updateuserinfo (40)
1       1     player_slot (0-31)
2       4     user_id (little-endian uint32)
6       var   userinfo_string (null-terminated)
```

### Player Slot vs User ID

These two identifiers serve different purposes:

| Field | Range | Description |
|-------|-------|-------------|
| `player_slot` | 0-31 | Client slot on the server. Fixed position in the server's client array, assigned when a player connects based on which slot is available. Used throughout the demo to identify which player entity is being updated. |
| `user_id` | arbitrary | Unique session identifier assigned by the server. Typically increments with each new connection. Used by external tools (e.g., QuakeWorld Hub viewer's `track` parameter) to identify specific players across demos. |

**Key differences:**
- **Slot** is positional (limited to 32 clients) and may be reused if a player disconnects
- **UserID** is unique per session and persists for that player's connection
- Different demos from the same server may assign different slots to the same player
- The same player on different servers will have different UserIDs

**Implementation note:** The `svc_updateuserinfo` message is sent multiple times during a demo (e.g., when player info changes). Some server mods (like KTPro) resend userinfo with `user_id=0` or corrupted values in subsequent updates. When parsing, keep the **first valid UserID** (non-zero) for each slot and ignore later updates that have invalid values.

### Userinfo String Format

The userinfo string is backslash-delimited key-value pairs:
```
\name\PlayerName\team\blue\topcolor\4\bottomcolor\4\skin\base
```

### Common Userinfo Keys

| Key | Description | Example |
|-----|-------------|---------|
| `name` | Player name | `"splif"` |
| `team` | Team name | `"blue"`, `"red"` |
| `topcolor` | Top color (0-13) | `"4"` |
| `bottomcolor` | Bottom color (0-13) | `"4"` |
| `skin` | Skin name | `"base"` |
| `spectator` | "1" if spectator | `"1"` |
| `*client` | Client software | `"ezQuake 3.6"` |

### Parsing Userinfo

```go
func parseUserinfo(s string) map[string]string {
    if s == "" || s[0] != '\\' {
        return nil
    }
    parts := strings.Split(s[1:], "\\")
    result := make(map[string]string)
    for i := 0; i+1 < len(parts); i += 2 {
        result[parts[i]] = parts[i+1]
    }
    return result
}
```

### The userinfo `name` is not always the displayed netname

On most demos `userinfo["name"]` and `ent->s.v.netname` (the string the server prints in chat lines and obituaries) are equal. **They are not required to be.** KTX with authentication enabled keeps the auth/login name in `userinfo["name"]` (often plus a `*auth\<login>` field) while the player's *displayed* netname can be a completely different string — typically the colored/decorated name they chose client-side.

Example from a real demo (`broken.mvd.gz`, slot 2):

| Source | Bytes | After Q_normalizetext |
|---|---|---|
| `svc_updateuserinfo` `\name\` value | `4e 65 6f 70 68 79 74 65` | `Neophyte` (auth name) |
| Server-emitted chat line `"X: hi"` | `2e ce 15 6f f0 68 f9 74 15 ae` | `.N3ophyt3.` (display netname) |
| KTX demoinfo JSON `players[i].name` | same bytes as the chat line | `.N3ophyt3.` |

Both strings refer to the same human. **No `svc_setinfo` and no second `svc_updateuserinfo` is sent** to bridge them — the demo simply contains the auth name in userinfo and the display name in every print/obituary that reaches the parser. A consumer that builds its `slot → display name` map purely from `svc_updateuserinfo` will be wrong about that player for the entire demo.

#### Where the displayed name actually comes from

The displayed name is `ent->s.v.netname` on the server side. From the demo's perspective you can recover it from any of:

1. **`svc_print` chat lines** (`"name: text"` for public say, `"(name): text"` for `say_team`).
2. **`svc_print` obituaries** (`"X was rocketed by Y"`, etc.).
3. **The KTX `mvdhidden_demoinfo` JSON** at end of demo, in `players[i].name`. This is the most authoritative source because KTX writes it directly from `ent->s.v.netname` without any cleanup, encoded into JSON via `\u00XX` escapes (see the `Q_normalizetext` note above about decoding those back through the same table).

#### Slot ↔ DemoInfo bridging

The demoinfo JSON does not carry a `slot` field per player, and the userinfo `name` for a slot may differ from the demoinfo `name` (auth-override case). However, both ends carry the player's **login token**, which provides a deterministic join.

##### The `*auth` userinfo key

When a player authenticates via mvdsv's web login (`central.c`), the server sets `*auth\<login>` in the client's userinfo. This key is in `shortinfotbl[]` (`sv_user.c`), so it is:

- included in `svc_updateuserinfo` (full userinfo broadcast at connect or demo start), and
- broadcast via `svc_setinfo` if authentication completes mid-demo.

The `<login>` value is byte-identical to what KTX writes into `demoInfo.players[i].login` (sourced from `ezinfokey(player, "login")` in `stats_json.c`).

##### Recommended bridging strategy

For each slot, try these steps in order:

1. **Login join (authenticated players).** If the slot's userinfo contains `*auth`, find the demoinfo player whose `login` field equals it. This is unambiguous — mvdsv enforces login uniqueness server-side, and the same string round-trips through both the userinfo and the demoinfo JSON writers.

2. **Name join (unauthenticated players).** If the slot has no `*auth`, match the slot's userinfo `name` against the demoinfo `name` (after `Q_normalizetext` normalization). For unauthenticated players, `userinfo["name"]` and `ent->s.v.netname` are equal by construction — authentication is the only mechanism that makes them diverge.

3. **Fall back to userinfo name.** If neither step matches, leave the slot's display name set to `userinfo["name"]`. Do **not** guess.

The name divergence problem only exists for authenticated players, and for those players `*auth` is always present. The two steps above are therefore exhaustive for all demos produced by mvdsv + KTX.

##### Example (`broken.mvd.gz`, slot 2)

| Source | Key | Value |
|---|---|---|
| `svc_updateuserinfo` | `\*auth\` | `Neophyte` |
| `svc_updateuserinfo` | `\name\` | `Neophyte` (auth name forced by `sv_forcenick`) |
| KTX demoinfo JSON | `players[i].login` | `"Neophyte"` |
| KTX demoinfo JSON | `players[i].name` | `.N3ophyt3.` (display netname from `ent->netname`) |

The join `slot.userinfo["*auth"] == demoInfo.players[i].login` maps slot 2 → the `.N3ophyt3.` demoinfo entry unambiguously, without needing frag counts or any other heuristic.

### Reconnect: one human spans multiple slots over time

A `slot → name` map (even a perfect one) is still **one entry per slot**, which breaks when a player disconnects and **reconnects mid-match**. On reconnect mvdsv assigns a *new* slot and a *new* userid, so the human now occupies two slots across the demo's timeline; the slot they vacated is frequently reused by a latecomer or simply stamped with a late `svc_updateuserinfo` name. A consumer that resolves each slot to its *final* occupant therefore relabels the player's pre-reconnect events (pickups, frags, position track) with whoever ended up on that slot — observed on gameId 216835, where "rusti" played the first half on one slot, reconnected onto another, and his vacated slot's final name was a non-playing connector. KTX itself treats the two occupancies as one player via its **ghost** mechanism: on reconnect it restores frags/stats by netname (`ktx/src/client.c:1513-1538`).

The reconnect is visible on the wire as KTX broadcast prints (`G_bprint`, `PRINT_HIGH`, so they arrive as broadcast `PrintEvent`s; the `\220`/`\221` team brackets fold to `[`/`]` and redtext folds to plain under `Q_normalizetext`):

| Event | KTX source | Normalised string |
|---|---|---|
| Leave | `client.c:2948`, `bot_commands.c:401` | `<name> left the game with <N> frags` |
| Rejoin (team) | `client.c:1529` | `<name> [<team>] rejoins the game with <N> frags` |
| Rejoin (non-team) | `client.c:1536` | `<name> rejoins the game with <N> frags` |
| Reconnect, ghost expired | `client.c:1550/1555` | `<name> [<team>] reenters the game without stats` |

The frag count on `left … with N` matches the `rejoins … with N` for the same netname (KTX restored exactly that count). To attribute correctly, track per-slot **occupancy sessions** (split on userid change), then fold sessions of the same human into one identity — by the `rejoins`/`reenters` netname, or by joining each session (not just the final slot) to a demoinfo entry via the `*auth`/name rules above — and resolve each event by the identity active **at that event's time**. The analyzer implements this in the `identity` analyser (`mvd-analytics/analyzer/identity.go`).

That restore also lands as the new slot's **first `svc_updatefrags`**, carrying the whole `N` (not a `+1`). A consumer that derives running score from per-slot frag *deltas* must **rebase** the slot's baseline to `N` on the handoff and emit no kill for it — otherwise the restore reads as a large delta, any corruption guard that rejects big jumps drops it, and (if that guard also declines to advance the baseline) every later real `+1` keeps reading as a huge delta and is dropped too, freezing the player's score at the pre-reconnect total. The timeline analyser keys this rebase off the userid change (`mvd-analytics/analyzer/timeline.go`, `fragResetPending`).

---

## svc_print (8)

Print message to console. **Critical for frag detection**.

```
Offset  Size  Field
------  ----  -----
0       1     svc_print (8)
1       1     print_level
2       var   message (null-terminated string)
```

### Print Levels

| Value | Name | Description | Use Case |
|-------|------|-------------|----------|
| 0 | `PRINT_LOW` | Low priority | Debug/info messages |
| 1 | `PRINT_MEDIUM` | Medium priority | **Obituaries (kill messages)** |
| 2 | `PRINT_HIGH` | High priority | Match events, server messages |
| 3 | `PRINT_CHAT` | Chat message | Player chat |

### Per-recipient chat duplication (KTX)

Vanilla `SV_Say` (`mvdsv/src/sv_user.c`) writes a chat line to the MVD **once** — `dem_all` for public say, `dem_multiple` (with a recipient bitmask) for say_team. But it first calls `PR_ClientSay`, and if the mod handles say it returns early *before* that single write (`sv_user.c:1832`/`:1837`). **KTX always handles say in QC** (`ClientSay`, `ktx/src/g_cmd.c:287`), looping over every eligible recipient and calling `G_sprint` per client (`g_cmd.c:560`). Each `G_sprint` → `SV_ClientPrintf` emits its own `MVDWrite_Begin(dem_single, …)` (`mvdsv/src/sv_send.c:220`), so on KTX demos **one chat line is written to the MVD once per recipient** and the parser faithfully emits one `PrintEvent` per copy. Public say reaches every client (most copies); say_team only teammates (fewer) — which is why public say duplicates more.

All copies of a line share an identical wire-ms. Two wrinkles:
- KTX sends the colored text (`str`) to colour-capable clients and a markup-stripped copy (`textuncolored`) to the rest (`g_cmd.c:558`), so the copies are byte-identical only when all recipients have the same colour-support capability (the common case on modern ezquake).
- Personal/private say_team without the `$\` fake marker is flagged `SPRINT_IGNOREINDEMO` (`g_cmd.c:554-557`) and never reaches the MVD.

The analytics layer collapses the identical copies — see `mvd-analytics/analyzer/messages.go` (`seenChat`) and `mvd-analytics/analyzer/messages.md`. The parser layer does **not** dedup: it reports every wire copy faithfully.

### Match Timing Detection

KTX servers send `PRINT_HIGH` messages to indicate match state transitions. These are critical for determining when actual gameplay begins and ends.

#### Warmup/Countdown Phase

Before a match starts, players are in **warmup mode** where:
- All players have all weapons and infinite ammo
- Health regenerates or is set high
- Frags don't count
- Player state data is **meaningless for analysis**

The countdown sequence typically looks like:
```
"The match begins in 10 seconds"
"The match begins in 5"
"The match begins in 4"
"The match begins in 3"
"The match begins in 2"
"The match begins in 1"
"Fight!"
```

#### Match Start Detection

The match officially starts when one of these messages appears:

| Pattern | Server Type | Notes |
|---------|-------------|-------|
| `"The match has begun!"` | KTX | Most common |
| `"match has begun"` | KTX variants | Substring match recommended |
| `"Fight!"` | KTX/MVDSV | End of countdown |
| `"Go!"` | Some servers | Alternative to "Fight!" |

**Implementation note**: Use substring matching (e.g., `contains(msg, "match has begun")`) rather than exact matching to handle variations.

**Critical**: All player state (items, health, armor, ammo) before match start should be **discarded**. Players spawn fresh with:
- 100 health
- 0 armor
- Axe + Shotgun (25 shells)
- No powerups

#### Match End Detection

The match ends when one of these messages appears:

| Pattern | Cause |
|---------|-------|
| `"The match is over"` | Normal end |
| `"match ended"` | Various |
| `"Game over"` | Generic end |
| `"Match complete"` | Some servers |
| `"Timelimit hit"` | Time ran out |
| `"Fraglimit hit"` | Frag limit reached |

#### Example Timeline

```
[0.0s]   Demo recording starts
[0.5s]   Players connecting, warmup begins
[5.2s]   "The match begins in 10 seconds"
[10.2s]  "The match begins in 5"
...
[14.2s]  "The match begins in 1"
[15.2s]  "Fight!"              <- matchStartTime
[15.3s]  First valid player state updates
...
[1215.2s] "The match is over"  <- matchEndTime (20 min match)
```

**Match duration** = `matchEndTime - matchStartTime` (not demo duration)

### Obituary Messages (Frag Detection)

Kill messages appear at `PRINT_MEDIUM` (level 1). Common patterns:

**Weapon Kills** (victim ... killer pattern):
| Pattern | Weapon |
|---------|--------|
| `"X was gibbed by Y's rocket"` | Rocket Launcher |
| `"X was smeared by Y's quad rocket"` | Rocket Launcher (with Quad) |
| `"X ate N loads of Y's buckshot"` | Super Shotgun |
| `"X was lead poisoned by Y"` | Super Shotgun |
| `"X accepts Y's shaft"` | Lightning Gun |
| `"X was perforated by Y"` | Nailgun |
| `"X was punctured by Y"` | Nailgun |
| `"X was ventilated by Y"` | Nailgun |
| `"X was straw-cuttered by Y"` | Super Nailgun |
| `"X was telefragged by Y"` | Telefrag |
| `"X was ax-murdered by Y"` | Axe |

**Suicides** (single player pattern):
| Pattern | Cause |
|---------|-------|
| `"X suicides"` | /kill command |
| `"X tries to leave"` | Disconnect during combat |
| `"X blew himself up"` | Self-rocket |
| `"X becomes bored with life"` | Various |
| `"X cratered"` | Fall damage |
| `"X sleeps with the fishes"` | Drowning |
| `"X sucks it down"` | Slime |
| `"X burst into flames"` | Lava |

**Team Kills**:
| Pattern | Description |
|---------|-------------|
| `"X mows down a teammate"` | Team kill |
| `"X gets a frag for the other team"` | Self-damage hurting team |

**KTX special telefrag variants** (`ktx/src/client.c:5141-5185`, death types `dtTELE2`–`dtTELE4`):

| Pattern | Death type | Note |
|---|---|---|
| `"Satan's power deflects X's telefrag"` | `dtTELE2` | Pent player is the survivor; X (the would-be telefragger) dies. Infix form — victim is bracketed by the prefix `"Satan's power deflects "` and the suffix `"'s telefrag"`. KTX counts this against X's deathcount even though no `DF_DEAD` or stat transition may appear on the wire — see [Death / Spawn Detection](#death--spawn-detection--three-signal-sources). |
| `"X was telefragged by Y's Satan's power"` | `dtTELE3` | Pent-vs-pent double-666 telefrag. Victim-prefix; killer suffix is `"'s Satan's power"`. |
| `"X couldn't resist the shiny spawn point"` | `dtTELE4` | Spawnicide variant (random 1-of-3 when `k_spawnicide` is enabled). |
| `"X got too close to the baby factory"` | `dtTELE4` | Spawnicide variant. |
| `"X was fragged by poor life choices"` | `dtTELE4` | Spawnicide variant. |

**CRMod-added obituary variants**: CRMod (Custom Rules Mod) added a parallel obituary table on top of KTX's fragfile; servers running the CR ruleset still emit these strings and KTX retains them for compatibility. All are victim-prefix.

| Pattern | Weapon |
|---|---|
| `"X was disembowled by Y's shotgun"` *(sic — CRMod misspelling)* | sg |
| `"X eats 2 scoops of Y's lead shot"` | ssg |
| `"X is shish-kebabed by Y's rocket"` | rl |
| `"X was blown to chunks by Y's rocket"` | rl |
| `"X was blown to chunks by Y's grenade"` | gl |
| `"X gets intimate with Y's grenade"` | gl |
| `"X gets a warm fuzzy feeling from Y"` | lg |

### Example Obituary Messages from Real Demo

```
[12.8s] "nexus was gibbed by splif's rocket"
[14.3s] "ToT_fix was lead poisoned by paniagua"
[59.9s] "nexus ate 2 loads of rusti FU's buckshot"
[77.1s] "paniagua was straw-cuttered by rghst"
[128.9s] "rusti FU gets a frag for the other team"
[195.6s] "paniagua mows down a teammate"
```

### Per-client pickup prints — and the PRINT_LOW filter

KTX's `G_sprint(other, PRINT_LOW, ...)` calls in `ktx/src/items.c` emit per-client text on every pickup: `"You got the Red Armor\n"` (armors / weapons / ammo boxes / powerups), `"You receive 25 health\n"` (health pickups including MH), and the `"You get "` opener line for backpacks (followed by per-piece continuation prints). In MVDs these land in `dem_single` with the picking player's slot in the header, giving authoritative pickup attribution — they would cover the categories `//ktx took` skips (ammo boxes and H15/H25 — `ammo_touch` at items.c:1171 has no stuffcmd, and only the megahealth branch of `health_touch` emits `//ktx took`).

**The catch** — `SV_ClientPrintf` in `mvdsv/src/sv_send.c:225` applies a per-client filter *before* the MVD write:

```c
if (level < cl->messagelevel)
    return;
```

`cl->messagelevel` tracks the `msg` cvar the client set in their config. Pickup prints are PRINT_LOW (0). Competitive QW players widely use `msg 2` to suppress pickup spam in their console, which **also strips the prints from the MVD entirely** — they never reach `MVDWrite_Begin (dem_single, ...)`. On a typical 4on4 / duel with everyone at `msg 2` you'll see **zero** PRINT_LOW events in the recording.

**Coverage is therefore per-player and per-demo.** A duel where one player set `msg 0` and the other set `msg 2` gives you pickup prints for exactly one side. The `//ktx took` and `//ktx bp` directives bypass this filter (they're STUFFCMD_DEMOONLY, not prints) — they're the correct signal when coverage matters. For the practical consequence on backpack pickups specifically (non-RL/LG packs end up with no authoritative attribution on competitive demos), see [Practical gap — non-RL/LG backpack pickups on competitive demos](#svc_stufftext-9).

To decide if print-based pickup attribution is viable on a given demo, count `PrintEvent.Level == 0` entries: zero means the filter stripped everything; a healthy count means at least some players had `msg 0` set.

```
SV_ClientPrintf (level=PRINT_LOW, target=cl)
    │
    ├── if level < cl->messagelevel: return  ← strips prints before MVD write
    │
    └── MVDWrite_Begin(dem_single, cl - svs.clients, ...) → svc_print header → message
```

---

## Hidden Messages

When `MVD_PEXT1_HIDDEN_MESSAGES` (0x20) is enabled, `dem_multiple` messages with `player_mask == 0` contain structured hidden data.

### Hidden Message Format

Hidden messages consist of sequential blocks within the payload:

```
Offset  Size  Field
------  ----  -----
0       4     block_length (little-endian long, data length AFTER type_id)
4       2     type_id (little-endian short)
6       N     block_data (block_length bytes)
```

**Important**: The `block_length` field specifies the length of the data that follows the `type_id`, NOT including the type_id itself. This is a common source of parsing errors.

If `type_id == 0xFFFF`, read another short for extended type range.

### Hidden Message Types

*Source: KTX `include/g_consts.h:323-332`*

| Type ID | Name | Description | Availability |
|---------|------|-------------|--------------|
| 0x0000 | `mvdhidden_antilag_position` | Antilag position data | Rare |
| 0x0001 | `mvdhidden_usercmd` | User command data (buttons, impulse) | **Requires server flag** |
| 0x0002 | `mvdhidden_usercmd_weapons` | Weapon selection data | **Requires server flag** |
| 0x0003 | `mvdhidden_demoinfo` | Embedded demo info (JSON) | **Common** |
| 0x0004 | `mvdhidden_commentary_track` | Commentary track info | Rare |
| 0x0005 | `mvdhidden_commentary_data` | Commentary audio data | Rare |
| 0x0006 | `mvdhidden_commentary_text_segment` | Commentary text | Rare |
| 0x0007 | `mvdhidden_dmgdone` | Damage dealt info | **Common** |
| 0x0008 | `mvdhidden_usercmd_weapons_ss` | Server-side weapon data | **Requires server flag** |
| 0x0009 | `mvdhidden_usercmd_weapon_instruction` | Weapon instruction | **Requires server flag** |
| 0x000A | `mvdhidden_paused_duration` | Paused time (QTV only) | QTV only |
| 0x000B | `mvdhidden_demo_start_timestamp_ms` | Unix timestamp (ms) at demo start (ULEB128 varint) | Newer mvdsv |
| 0xFFFF | `mvdhidden_extended` | Extended type (read next short) | - |

**Newer types**: the table above tracks qwprot as of 2026. Older parsers will warn `unknown hidden message type 0x000b` on any demo recorded by an mvdsv that includes the [PR #17 / `500bd4b`](https://github.com/QW-Group/qwprot/commit/500bd4b) addition; the payload is a Unix-millisecond timestamp captured at the moment the server opened the MVD file, **ULEB128 varint-encoded** (7 data bits per byte, high bit = continuation — *not* a fixed-width little-endian `uint64`; a ~2026 value is 6 bytes), intended for stream/voice synchronisation. It's safe to skip if you don't consume it, but recognising it explicitly cleans up the warning stream. mvdsv writes it via `Sys_TimestampMilliseconds()` in `SV_MVDEmbedStartTimestamp` (`src/sv_demo_misc.c`).

### Hidden Message Availability

**Common in standard competitive demos:**
- `0x0003` (demoinfo) - JSON metadata with match info and KTX-tracked stats
- `0x0007` (dmgdone) - Damage events, essential for weapon damage tracking

**Requires server-side configuration:**
The usercmd-related hidden messages (0x0001, 0x0002, 0x0008, 0x0009) require the server operator to enable per-player tracking:

```
sv_usercmdtrace <userid> on|off
```

*Source: MVDSV `sv_demo.c:1867-1877`, KTX `race.c:158-166`*

This command is primarily used for race mode demos. Standard 4on4/duel demos do NOT include usercmd data.

**Impact on shot tracking:**
- Without usercmd data, shot detection relies on ammo decrease tracking
- Ammo-based shot tracking cannot reliably distinguish RL vs GL (both use rockets)
- See [Shot Tracking Limitations](#shot-tracking-limitations) for details

### mvdhidden_antilag_position (0x0000)

```
// Header
Offset  Size  Field
------  ----  -----
0       1     player_num (shooting player)
1       1     num_players (number of position records)
2       4     incoming_sequence
6       4     server_time (float)
10      4     target_time (float)

// Per-player records (repeated num_players times)
0       12    client_position (3 floats: x, y, z)
12      12    antilag_position (3 floats: x, y, z)
24      1     player_num
25      1     msec
26      1     prediction_model
```

### mvdhidden_usercmd (0x0001)

```
Offset  Size  Field
------  ----  -----
0       1     player_num
1       1     drop_count
2       1     msec
3       12    angles (3 floats: pitch, yaw, roll)
15      2     forward_move (short)
17      2     side_move (short)
19      2     up_move (short)
21      1     buttons
22      1     impulse
```

### mvdhidden_demoinfo (0x0003)

Contains embedded JSON metadata about the demo.

```
Offset  Size  Field
------  ----  -----
0       2     block_number (short)
2       N     json_content (UTF-8 text)
```

**Block numbering**: JSON may be split across multiple blocks. Blocks are numbered 1, 2, 3, ..., 0 where **block 0 is the LAST block**. Concatenate blocks in order: 1, 2, ..., n, then 0.

*Source: ezQuake `sv_demo_misc.c:851-873`*

#### KTX Demoinfo JSON Schema

The JSON structure is **KTX mod specific**. Other server mods may use different schemas or omit demoinfo entirely.

**Example JSON** (abbreviated):
```json
{
  "version": 1,
  "date": "2024-01-15 20:30:00 UTC",
  "map": "dm2",
  "hostname": "QW Server",
  "ip": "192.168.1.1",
  "port": 27500,
  "mode": "4on4",
  "tl": 20,
  "fl": 0,
  "duration": 1200,
  "demo": "4on4_red_vs_blue[dm2]20240115-2030.mvd",
  "teams": ["red", "blue"],
  "players": [
    {
      "name": "PlayerName",
      "team": "red",
      "top-color": 4,
      "bottom-color": 4,
      "ping": 25,
      "login": "player_login",
      "stats": {
        "frags": 15,
        "deaths": 10,
        "tk": 0,
        "spawn-frags": 2,
        "kills": 15,
        "suicides": 0
      },
      "dmg": {
        "taken": 1500,
        "given": 2200,
        "team": 150,
        "self": 80,
        "team-weapons": 50,
        "enemy-weapons": 1800
      },
      "spree": {
        "max": 5,
        "quad": 3
      },
      "control": 45.5,
      "speed": {
        "avg": 320.5,
        "max": 580.0
      },
      "weapons": {
        "rl": {
          "acc": { "virtual": 45.2, "real": 38.5 },
          "kills": { "total": 8, "team": 0 },
          "deaths": 3,
          "pickups": { "taken": 5, "total-taken": 8, "spawn-taken": 2, "spawn-total-taken": 3 },
          "damage": { "enemy": 1200, "team": 0 }
        },
        "lg": {
          "acc": { "virtual": 32.1, "real": 28.4 },
          "kills": { "total": 4, "team": 0 },
          "deaths": 2,
          "damage": { "enemy": 650, "team": 0 }
        }
      },
      "items": {
        "ra": { "took": 3, "time": 45 },
        "ya": { "took": 5, "time": 30 },
        "mh": { "took": 2, "time": 15 },
        "quad": { "took": 1, "time": 30 }
      }
    }
  ]
}
```

**Top-level fields:**

| Field | Type | Description |
|-------|------|-------------|
| `version` | int | Demoinfo schema version |
| `date` | string | Match date/time (UTC) |
| `map` | string | Map BSP name (e.g., "dm2") - **authoritative map name** |
| `hostname` | string | Server hostname |
| `ip` | string | Server IP address |
| `port` | int | Server port |
| `mode` | string | Game mode ("4on4", "2on2", "duel", etc.) |
| `tl` | int | Timelimit in minutes |
| `fl` | int | Fraglimit |
| `duration` | int | Match duration in seconds |
| `demo` | string | Demo filename |
| `teams` | string[] | Team names |
| `players` | object[] | Player data array |

**Player fields:**

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Player name (may contain Quake color codes) |
| `team` | string | Team name |
| `top-color` | int | Top color (0-13) |
| `bottom-color` | int | Bottom color (0-13) |
| `ping` | int | Player ping in ms |
| `login` | string | Login name (if authenticated) |
| `stats` | object | Frag/death statistics |
| `dmg` | object | Damage statistics |
| `spree` | object | Kill streak info |
| `control` | float | Control percentage |
| `speed` | object | Movement speed stats |
| `weapons` | object | Per-weapon statistics (keyed by weapon name) |
| `items` | object | Item pickup statistics (keyed by item name) |

**Weapon names**: `"axe"`, `"sg"`, `"ssg"`, `"ng"`, `"sng"`, `"gl"`, `"rl"`, `"lg"`

**Item names**: `"ga"`, `"ya"`, `"ra"`, `"mh"`, `"quad"`, `"pent"`, `"ring"`

##### Weapon `pickups` counters

KTX tracks four distinct counters per `(player, weapon)` and emits whichever are non-zero:

| JSON key | KTX field | Increments when |
|----------|-----------|-----------------|
| `taken` | `tooks` | First acquisition this life — any source (world entity *or* backpack), and only if the player did not already hold the weapon. |
| `total-taken` | `ttooks` | Every touch — any source, every time, even if the player already had the weapon. |
| `spawn-taken` | `stooks` | First acquisition this life from a **world entity only** (not a backpack drop). Backpack pickups do not count. |
| `spawn-total-taken` | `sttooks` | Every touch of a **world entity** (not backpack) — every time, regardless of whether the player already had the weapon. |

The four counters fall out of one branchy increment in `TookWeaponHandler` (`ktx/src/client.c:4707`):

```c
ttooks++;                               // every call
if (!from_backpack) sttooks++;          // every world touch
if (!(player_items & new_wp)) {         // first-acquisition: didn't already hold the weapon
    if (!from_backpack) stooks++;       //   world, first-acq
    tooks++;                            //   any source, first-acq
}
```

Worked example for one player's RL counters across a single life:

1. Spawn → run over RL spawn entity (didn't have RL): `taken+1, total-taken+1, spawn-taken+1, spawn-total-taken+1`.
2. Same life, run back over the respawned RL: `total-taken+1, spawn-total-taken+1` (already held; not a first-acq).
3. Die. Respawn. Pick up an RL backpack: `taken+1, total-taken+1` (any-source first-acq, no spawn counters because it's a backpack).

**Comparison semantics for analysers.** Counting world-entity pickup phases off the wire (entity-state `Taken=true` transitions on weapon edicts) corresponds to **`spawn-total-taken`**, not `taken`. `taken` filters out repeat pickups within a life and additionally counts backpack acquisitions, neither of which is observable in entity-state alone. Items / armors / healths / powerups in `items[]` use a single `took` field with simpler semantics — every touch — because those code paths (`ktx/src/items.c:318, 332, 336, 511, 2149, 2164, 2179`) do not split the counter.

*Source: counters incremented in `ktx/src/client.c:4749-4763`; serialized in `ktx/src/stats_json.c:180-202`.*

### mvdhidden_dmgdone (0x0007)

Tracks damage dealt between players. **Critical for weapon statistics**.

Each hidden message block can contain one damage record. The format is:

```
Hidden Message Block:
Offset  Size  Field
------  ----  -----
0       4     block_length (8 for dmgdone - length of data AFTER type_id)
4       2     type_id (0x0007)

Damage Record (8 bytes):
Offset  Size  Field
------  ----  -----
0       2     flags_and_deathtype (short)
              - Bits 0-14: death type (weapon identifier)
              - Bit 15: splash damage flag (0x8000)
2       2     attacker_entity (short, 1-indexed entity number)
4       2     victim_entity (short, 1-indexed entity number)
6       2     damage_amount (signed short)
```

**Important Notes**:
- Entity numbers are 1-indexed (entity 0 is world). Convert to player number: `player_num = entity - 1`
- The `damage_amount` is **raw/unbound damage** including overkill (see [Damage Tracking](#damage-tracking-details))
- Splash damage flag indicates indirect damage (e.g., rocket splash, not direct hit)
- KTX emits the record when **either** the attacker or the victim is a player
  (`ktx/src/combat.c:810`). The parser emits a `DamageEvent` whenever the
  **victim** is a player and `damage > 0`. World/environmental damage-taken
  (lava, fall, `trigger_hurt`, drowning) arrives with a non-player attacker —
  worldspawn (entity 0 → slot −1) or a non-client entity — and is surfaced with
  **`Attacker == -1` (the "world" sentinel)** rather than dropped, so
  damage-taken totals are complete. Player→player records are unchanged.

**Example parsing**:
```go
flagsAndType := r.ReadUint16()  // e.g., 0x8007 = splash + RL
attackerEnt := r.ReadUint16()   // e.g., 3 = player slot 2
victimEnt := r.ReadUint16()     // e.g., 8 = player slot 7
damage := r.ReadInt16()         // e.g., 89

isSplash := (flagsAndType & 0x8000) != 0
deathType := flagsAndType & 0x7FFF  // 7 = DT_RL
attackerPlayer := int(attackerEnt) - 1
victimPlayer := int(victimEnt) - 1
```

---

## Death Types

The `deathtype` field in damage events and obituaries identifies the weapon or cause of death.

*Source: KTX `include/deathtype.h:1-29`*

### Death Type Constants

| Value | Name | Weapon/Cause | Obituary Keyword |
|-------|------|--------------|------------------|
| 0 | `DT_NONE` | Unknown | - |
| 1 | `DT_AXE` | Axe | "axed", "ax-murdered" |
| 2 | `DT_SG` | Shotgun | "shot" |
| 3 | `DT_SSG` | Super Shotgun | "buckshot", "lead poisoned" |
| 4 | `DT_NG` | Nailgun | "nailed", "perforated" |
| 5 | `DT_SNG` | Super Nailgun | "straw-cuttered" |
| 6 | `DT_GL` | Grenade Launcher | "grenade", "eats pineapple" |
| 7 | `DT_RL` | Rocket Launcher | "rocket", "gibbed" |
| 8 | `DT_LG_BEAM` | Lightning Gun (beam) | "shaft", "accepts shaft" |
| 9 | `DT_LG_DIS` | Lightning Gun (discharge) | "discharge" |
| 10 | `DT_LG_DIS_SELF` | LG discharge (self) | "discharges" |
| 11 | `DT_HOOK` | Grappling hook | - |
| 12 | `DT_CHANGELEVEL` | Level change | - |
| 13 | `DT_LAVA` | Lava damage | "burst into flames" |
| 14 | `DT_SLIME` | Slime damage | "sucks it down" |
| 15 | `DT_WATER` | Drowning | "sleeps with the fishes" |
| 16 | `DT_FALL` | Fall damage | "cratered" |
| 17 | `DT_STOMP` | Stomp (land on enemy) | - |
| 18-21 | `DT_TELE1-4` | Telefrag | "telefragged" |
| 22 | `DT_EXPLOBOX` | Exploding box | - |
| 23 | `DT_LASER` | Laser (mod) | - |
| 24 | `DT_FIREBALL` | Fireball (mod) | - |
| 25 | `DT_SQUISH` | Crushed by door/platform | "squished" |
| 26 | `DT_TRIGGER_HURT` | trigger_hurt brush | - |
| 27 | `DT_SUICIDE` | Suicide (/kill) | "suicides" |
| 28 | `DT_UNKNOWN` | Unknown | - |

### Pentagram-deflect telefrag (dtTELE2) — a kill that doesn't score

`dtTELE2` is the "Satan's power deflects X's telefrag" case: a player tries
to telefrag someone holding **666 health (pentagram / invulnerability)**, the
telefrag is deflected, and the *attacker* (telefragger) dies instead. This
produces a three-way divergence in KTX's own books that you must account for
when reconciling kill counts (*source: KTX `src/client.c:5124–5161`*):

```c
} else if (attacker->ct == ctPlayer) {
    attacker->kills += 1;          // the 666-holder's kills counter IS bumped
}
if (dtTELE2 == targ->deathtype) {
    G_bprint(... "Satan's power deflects %s's telefrag", victimname);
    targ->s.v.frags -= 1;          // the dead telefragger loses a frag
    logfrag(targ, targ);           // the frag LOG books it as the victim's SUICIDE
    return;                        // returns BEFORE the 666-holder's s.v.frags += 1
}
```

So one deflect leaves three inconsistent records, all from KTX:

| record | sees the deflect as the holder's kill? |
|---|---|
| `player->kills` (demoinfo `stats.kills`) | **yes** — bumped at the top |
| `s.v.frags` (demoinfo `stats.frags`, the scoreboard) | **no** — the `return` skips scoring |
| frag log / per-weapon `kills` (`logfrag`) | **no** — booked as the victim's self-telefrag |

Consequences for a parser:
- The obituary text names **only the dead telefragger** ("Satan's power
  deflects sailorman's telefrag") — there is **no way to attribute the kill to
  the 666-holder from the print alone**. Reconstructing it requires the
  powerup state (who held pentagram) at that time; the holder is co-located
  with the victim by construction (the victim spawned onto them).
- Therefore obituary-derived and frag-score-derived kill counts will sit
  **one below** demoinfo `stats.kills` for each deflect the holder absorbed.
  This is **not a parse miss** — it is KTX crediting a kill that deliberately
  does not score. `stats.frags` and the per-weapon totals agree with the
  lower (obituary-derived) count; only the headline `stats.kills` is higher.
- `dtTELE3` ("X was telefragged by Y's Satan's power", a 666-holder Y
  telefragging X) is the symmetric case and likewise returns before scoring.

### World-dealt deaths — the `suicides` stat lands on `world`

A sibling of the dtTELE2 quirk, in the *same* counter block: KTX bumps the
**`suicides` counter on `attacker`, not on the victim** (*source: KTX
`src/client.c:5130–5133`*):

```c
else if ((targ == attacker) || (attacker->ct != ctPlayer)) {
    attacker->suicides += 1;     // attacker's counter, not targ's
}
```

For a normal self-kill (`/kill`, blowing yourself up with your own rocket
or grenade) the attacker **is** the victim (`targ == attacker`), so it lands
correctly. But environmental deaths are inflicted by the **`world`** entity,
e.g. fall damage (*`src/client.c:4428`*):

```c
self->deathtype = dtFALL;
T_Damage(self, world, world, 5);   // inflictor = world, attacker = world
```

Here `targ == attacker` is false and `attacker->ct != ctPlayer` is true, so
`world->suicides += 1` runs — the victim's `suicides` stat is **never
touched**. The same applies to other world-dealt damage (`trigger_hurt`, and
any death whose `T_Damage` attacker is `world`).

What stays correct: the victim's `deaths` (`targ->deaths += 1`) and frag
score (`targ->s.v.frags -= 1` in the non-player-attacker branch,
`src/client.c:5608`). Only the **`suicides` counter** is lost to `world`.

Consequence for a parser: demoinfo `stats.suicides` undercounts a player's
self-deaths by the number of world-dealt deaths (falls, trigger_hurt, …)
they suffered. The obituary stream is the complete record — every such death
still prints its own suicide obituary ("X fell to his death", "X cratered")
and produces a `DeathEvent`. Don't treat `stats.suicides` as the count of a
player's self-inflicted deaths.

> **KTX version.** Both quirks above (dtTELE2 deflect-kill and world-dealt
> `suicides`) were verified against KTX **`MOD_VERSION` 1.47-dev** — git
> `1.46-52-g8eddb65` (commit `8eddb65`, 2026-04-07), `src/client.c`
> `ClientObituary`. The worked-example demo (gameId 211840) ran KTX
> `1.47-dev-qwc`. These are bugs in that version's counter bookkeeping and
> **may be fixed in newer KTX** — re-check `ClientObituary` (the
> `attacker->kills` / `attacker->suicides` block ~line 5130, the dtTELE2
> early return ~5141, and the fall `T_Damage(self, world, world, …)` ~4428)
> against the KTX version that recorded the demo before assuming the
> divergence still holds.

### Damage Attribution Classification

Death types are classified by who receives credit for the damage:

#### Player-Attributed Damage (counts in attacker's dmg_given)

These death types credit the attacking player:

| Death Type | Values | Track As | Description |
|------------|--------|----------|-------------|
| Weapons | 1-11 | `axe`, `sg`, `ssg`, `ng`, `sng`, `gl`, `rl`, `lg` | Standard weapon damage |
| Stomp | 17 | `stomp` | Landing on enemy's head |
| Telefrag | 18-21 | `tele` | Telefragging enemy during teleport |
| Squish | 25 | `squish` | When attacker != victim (triggered by player) |
| Exploding Box | 22 | `explobox` | Exploding box triggered by player |

**Note**: For squish damage (DT_SQUISH=25), the attribution depends on the attacker:
- If `attacker == victim` or `attacker == world` → Environmental (self-damage)
- If `attacker != victim` → Player-attributed (e.g., trapping enemy in door)

#### Environmental Damage (counts in victim's damage received)

These death types are world/self-attributed and NOT counted in dmg_given:

| Death Type | Value | Track As | Description |
|------------|-------|----------|-------------|
| Lava | 13 | `lava` | Walking in lava |
| Slime | 14 | `slime` | Walking in slime |
| Drowning | 15 | `drown` | Underwater too long |
| Fall | 16 | `fall` | Fall damage from height |
| Squish | 25 | `squish` | World-attributed crush damage |
| Trigger Hurt | 26 | `trigger` | Damage from trigger_hurt brushes |
| Suicide | 27 | `suicide` | Using /kill command |

### Mapping Death Types to Weapon Names

```go
func DeathTypeToWeapon(dt int) string {
    switch dt {
    case 1:  return "axe"
    case 2:  return "sg"
    case 3:  return "ssg"
    case 4:  return "ng"
    case 5:  return "sng"
    case 6:  return "gl"
    case 7:  return "rl"
    case 8, 9, 10: return "lg"  // beam, discharge, self-discharge
    case 17: return "stomp"     // landing on enemy
    case 18, 19, 20, 21: return "tele"  // telefrag variants
    case 22: return "explobox"  // exploding box
    case 25: return "squish"    // crush damage (when player-attributed)
    default: return "unknown"
    }
}

func IsEnvironmentalDamage(dt int) bool {
    switch dt {
    case 13, 14, 15, 16, 26, 27:  // lava, slime, water, fall, trigger_hurt, suicide
        return true
    default:
        return false
    }
}

func EnvironmentalDamageType(dt int) string {
    switch dt {
    case 13: return "lava"
    case 14: return "slime"
    case 15: return "drown"
    case 16: return "fall"
    case 25: return "squish"   // when world-attributed
    case 26: return "trigger"
    case 27: return "suicide"
    default: return ""
    }
}
```

### Example: Telefrag Damage

When a player telefrags an enemy:
1. The dmgdone hidden message contains `deathtype=18` (DT_TELE1)
2. Damage is typically 100 (instant kill)
3. This damage is credited to the telefragging player's `dmg_given`
4. It appears in weapon stats as `tele` damage

### Example: Environmental Damage

When a player walks in lava:
1. The dmgdone hidden message contains `deathtype=13` (DT_LAVA)
2. The attacker entity is typically `world` (entity 0) or self
3. This damage is NOT counted in any player's `dmg_given`
4. It's tracked separately as environmental damage received

---

## Damage Tracking Details

### MVD vs KTX Damage Values

MVD files contain **raw/unbound damage** values, which includes overkill damage. KTX mod's stats track **effective damage** capped at the victim's health.

| Type | Description | Example |
|------|-------------|---------|
| **Raw Damage** (MVD) | Actual damage dealt regardless of victim health | Rocket deals 120 damage |
| **Effective Damage** (KTX) | Damage capped at victim's current health | Victim has 50 HP → 50 counted |
| **Overkill** | Difference: raw - effective | 120 - 50 = 70 overkill |

**KTX Source Reference** (`combat.c:786-834`):
```c
// Line 786: Raw damage before health capping
unbound_dmg_dealt = dmg_dealt;

// Line 804: Effective damage capped at victim's health
dmg_dealt += bound(0, virtual_take, targ->s.v.health);

// Lines 830-834: Write unbound damage to MVD hidden message
WriteShort(MSG_MULTICAST, mvdhidden_dmgdone);
WriteShort(MSG_MULTICAST, damage_flags | targ->deathtype);
WriteShort(MSG_MULTICAST, NUM_FOR_EDICT(attacker));
WriteShort(MSG_MULTICAST, NUM_FOR_EDICT(targ));
WriteShort(MSG_MULTICAST, (short)unbound_dmg_dealt);
```

### Damage Capping Algorithm

To match KTX stats exactly, you must account for **both armor absorption AND health capping**:

```go
func calculateEffectiveDamage(rawDamage int, armorValue int, armorType float64, health int) (effective, overkill int) {
    // Step 1: Calculate armor absorption (using ceiling like KTX)
    armorAbsorbed := 0
    if armorValue > 0 && armorType > 0 {
        absorbed := int(math.Ceil(float64(rawDamage) * armorType))
        if absorbed > armorValue {
            absorbed = armorValue
        }
        armorAbsorbed = absorbed
    }

    // Step 2: Calculate health damage (what's left after armor)
    healthDamage := int(math.Ceil(float64(rawDamage) - float64(armorAbsorbed)))

    // Step 3: Cap health damage at victim's current health
    effectiveHealthDamage := healthDamage
    if health > 0 && effectiveHealthDamage > health {
        effectiveHealthDamage = health
    } else if health <= 0 {
        effectiveHealthDamage = 0
    }

    // Step 4: Total effective = armor absorbed + capped health damage
    effective = armorAbsorbed + effectiveHealthDamage
    overkill = healthDamage - effectiveHealthDamage

    return effective, overkill
}
```

**Key insight**: The MVD damage value is the "raw damage" before armor split. KTX's `dmg_dealt` includes full armor absorption but caps only the health portion. Simply capping total damage at health is WRONG and will undercount by the armor absorption amount.

### State Tracking for Accurate Damage Calculation

To accurately calculate damage, track these stats for each player:

**From `svc_updatestat`/`svc_updatestatlong`:**
- `STAT_HEALTH` (index 0): Current health points
- `STAT_ARMOR` (index 4): Current armor points
- `STAT_ITEMS` (index 15): Item flags (includes armor type)

**Armor type from STAT_ITEMS:**
```go
if items & IT_ARMOR3 != 0 {
    armorType = 0.8  // Red armor - 80% absorption
} else if items & IT_ARMOR2 != 0 {
    armorType = 0.6  // Yellow armor - 60% absorption
} else if items & IT_ARMOR1 != 0 {
    armorType = 0.3  // Green armor - 30% absorption
} else {
    armorType = 0    // No armor
}
```

**When processing damage events:**
1. Get victim's current armor, armorType, and health
2. Calculate armor_absorbed = min(ceil(armorType * rawDamage), armorValue)
3. Calculate health_damage = ceil(rawDamage - armor_absorbed)
4. Cap health_damage at victim's current health
5. effective_damage = armor_absorbed + capped_health_damage
6. Update victim's armor and health after damage

### Overkill as a Playstyle Metric

High overkill percentages can indicate:
- Aggressive finishing shots (ensuring kills)
- Quad Damage usage
- Multiple players focusing same target
- Wasteful ammunition usage

**Example analysis from real demo**:
```
Player     RL Damage   RL Overkill   Overkill%
splif      1806        658           36%
rghst      1723        557           32%
ToT_fix    875         182           21%
```

---

## Weapon Statistics Tracking

### Shot Detection via Ammo Changes

Shots are detected by monitoring ammo decreases via `svc_updatestat`:

| Weapon | Ammo Stat | Per-Shot Cost | Notes |
|--------|-----------|---------------|-------|
| Shotgun | `STAT_SHELLS` | 1 | 6 pellets per shot |
| Super Shotgun | `STAT_SHELLS` | 2 | 14 pellets per shot |
| Nailgun | `STAT_NAILS` | 1 | |
| Super Nailgun | `STAT_NAILS` | 2 | |
| Grenade Launcher | `STAT_ROCKETS` | 1 | |
| Rocket Launcher | `STAT_ROCKETS` | 1 | |
| Lightning Gun | `STAT_CELLS` | 1 | Per tick (~10 ticks/sec) |

**Reference implementation**: See mvdparser `netmsg_parser.c:255-311` for the `Stat_CalculateShotsFired()` function. This tracks ammo decreases per weapon type using `STAT_ACTIVEWEAPON` to determine which weapon fired.

**Caveat**: Ammo-based shot tracking has inherent limitations:
- Respawn resets ammo to spawn defaults (25 shells, 0 others), causing false "shots"
- Without respawn filtering, shot counts can be ~2x overcounted
- Ammo pickups reset the baseline, hiding some shots if picked up mid-decrease

### Active Weapon Tracking

The `STAT_ACTIVEWEAPON` stat contains weapon flags indicating the current weapon:

```c
// Active weapon flags (from IT_* constants)
#define IT_SHOTGUN          (1 << 0)   // 1
#define IT_SUPER_SHOTGUN    (1 << 1)   // 2
#define IT_NAILGUN          (1 << 2)   // 4
#define IT_SUPER_NAILGUN    (1 << 3)   // 8
#define IT_GRENADE_LAUNCHER (1 << 4)   // 16
#define IT_ROCKET_LAUNCHER  (1 << 5)   // 32
#define IT_LIGHTNING        (1 << 6)   // 64
```

### Shot Tracking Algorithm

```go
func handleAmmoChange(player *PlayerStats, statIndex, newValue int) {
    oldValue := player.ammo[statIndex]

    // Only count decreases (not pickups)
    if newValue >= oldValue || oldValue <= 0 {
        player.ammo[statIndex] = newValue
        return
    }

    decrease := oldValue - newValue
    weapon := getWeaponForStat(statIndex, player.activeWeapon)
    ammoPerShot := getAmmoPerShot(weapon)

    shots := decrease / ammoPerShot
    if shots > 0 {
        player.weaponStats[weapon].Shots += shots
    }

    player.ammo[statIndex] = newValue
}
```

### Shot Tracking Limitations

**Problem: Weapon switching scripts**

QuakeWorld players use weapon switching scripts that rapidly change weapons:
```
// Typical script flow:
1. Player holds SG as "resting" weapon (to avoid dropping valuable packs on death)
2. Script: switch to RL → fire → switch back to SG
3. All happens within a few milliseconds
```

The MVD stat updates cannot keep pace with these scripts, causing `STAT_ACTIVEWEAPON` to show SG when rockets are actually being fired by RL.

**Weapon accuracy by tracking reliability:**

| Weapon | Ammo Type | Tracking Reliability | Notes |
|--------|-----------|---------------------|-------|
| SG | Shells (1/shot) | **Excellent (~100%)** | Usually the "resting" weapon, active when shells decrease |
| LG | Cells (1/tick) | **Excellent (~100%)** | Only weapon using cells |
| SSG | Shells (2/shot) | Good (~85%) | 2-shell decrease distinguishes from SG |
| NG | Nails (1/shot) | Good (~85%) | |
| SNG | Nails (2/shot) | Good (~85%) | 2-nail decrease distinguishes from NG |
| RL | Rockets (1/shot) | **Poor (~30-50%)** | Often shows SG active due to scripts |
| GL | Rockets (1/shot) | **Poor (~30-50%)** | Same issue as RL |

**Recommended approach:**
1. For SG/LG: MVD-based tracking is reliable for time-windowed analysis
2. For RL/GL: Use embedded demoinfo JSON for authoritative stats, or report combined "rocket shots"
3. For damage: Use dmgdone hidden messages (death type identifies weapon accurately)

### Hit Detection

Hits are counted from damage events (non-splash):

| Weapon | Hit Criteria |
|--------|--------------|
| Hitscan (LG, SG, SSG, NG, SNG) | Any damage event, not splash |
| Projectile (RL, GL) | Direct hit only (splash flag = false) |

**Important**: Shotgun weapons fire multiple pellets, each generating a separate damage event. This means:
- SSG accuracy can exceed 100% (14 pellets per shot)
- SG accuracy can exceed 100% (6 pellets per shot)

### Accuracy Calculation

```go
func calculateAccuracy(shots, hits int) float64 {
    if shots == 0 {
        return 0
    }
    return float64(hits) / float64(shots) * 100
}
```

**Note**: For shotguns, this calculates pellet accuracy, not shot accuracy.

---

## Obituary Message Patterns (KTX)

*Source: `sv_mod_frags.h` (63 default patterns), `fragfile.dat` (extended patterns)*

Obituary messages are sent via `svc_print` at `PRINT_MEDIUM` (level 1). All patterns below use `X` for victim and `Y` for killer. Gender variants exist for some patterns (`himself`/`herself`, `his`/`her`).

### Suicide / Environmental Death Patterns (1-player)

| Pattern | Weapon ID | Category |
|---------|-----------|----------|
| `"X suicides"` | 0 (die) | /kill command |
| `"X died"` | 0 (die) | Generic death |
| `"X tried to leave"` | 0 (die) | Changelevel |
| `"X becomes bored with life"` | 7 (rl) | Self-rocket |
| `"X discovers blast radius"` | 7 (rl) | Self-rocket |
| `"X tries to put the pin back in"` | 6 (gl) | Self-grenade |
| `"X electrocutes himself"` | 13 (dschrg) | LG discharge |
| `"X electrocutes herself"` | 13 (dschrg) | LG discharge |
| `"X discharges into the water"` | 13 (dschrg) | LG discharge |
| `"X discharges into the slime"` | 13 (dschrg) | LG discharge |
| `"X discharges into the lava"` | 13 (dschrg) | LG discharge |
| `"X heats up the water"` | 13 (dschrg) | LG discharge |
| `"X sleeps with the fishes"` | 10 (drown) | Drowning |
| `"X sucks it down"` | 10 (drown) | Drowning |
| `"X gulped a load of slime"` | 10 (drown) | Slime |
| `"X can't exist on slime alone"` | 10 (drown) | Slime |
| `"X burst into flames"` | 10 (drown) | Lava |
| `"X turned into hot slag"` | 10 (drown) | Lava |
| `"X visits the Volcano God"` | 10 (drown) | Lava |
| `"X cratered"` | 15 (fall) | Fall damage |
| `"X fell to his death"` | 15 (fall) | Fall damage |
| `"X fell to her death"` | 15 (fall) | Fall damage |
| `"X blew up"` | 11 (trap) | Explosive box |
| `"X was spiked"` | 11 (trap) | Spike trap |
| `"X was zapped"` | 11 (trap) | Laser trap |
| `"X ate a lavaball"` | 11 (trap) | Fireball |
| `"X was squished"` | 14 (squish) | Crushed by world |

### Kill Patterns — Victim First (2-player: `"X <pattern> Y"`)

| Pattern | Weapon ID | Weapon |
|---------|-----------|--------|
| `"X was ax-murdered by Y"` | 1 | Axe |
| `"X was axed to pieces by Y"` | 1 | Axe (instagib) |
| `"X chewed on Y's boomstick"` | 2 | Shotgun |
| `"X was lead poisoned by Y"` | 2 | Shotgun (gib) |
| `"X was instagibbed by Y"` | 2 | Coil Gun (instagib) |
| `"X ate 2 loads of Y's buckshot"` | 3 | Super Shotgun |
| `"X ate 8 loads of Y's buckshot"` | 3 | Super Shotgun (Quad) |
| `"X was body pierced by Y"` | 4 | Nailgun |
| `"X was nailed by Y"` | 4 | Nailgun |
| `"X was perforated by Y"` | 5 | Super Nailgun |
| `"X was punctured by Y"` | 5 | Super Nailgun |
| `"X was ventilated by Y"` | 5 | Super Nailgun |
| `"X was straw-cuttered by Y"` | 5 | Super Nailgun (Quad gib) |
| `"X eats Y's pineapple"` | 6 | Grenade Launcher |
| `"X was gibbed by Y's grenade"` | 6 | GL (gib) |
| `"X rides Y's rocket"` | 7 | Rocket Launcher |
| `"X was gibbed by Y's rocket"` | 7 | RL (gib) |
| `"X was smeared by Y's quad rocket"` | 7 | RL + Quad (gib) |
| `"X was brutalized by Y's quad rocket"` | 7 | RL + Quad (gib) |
| `"X accepts Y's shaft"` | 8 | Lightning Gun |
| `"X gets a natural disaster from Y"` | 8 | LG (Quad) |
| `"X accepts Y's discharge"` | 13 | LG discharge kill |
| `"X drains Y's batteries"` | 13 | LG discharge kill |
| `"X was railed by Y"` | 9 | Rail Gun (DMM8/TF) |
| `"X was telefragged by Y"` | 12 | Telefrag |
| `"X was literally stomped into particles by Y"` | 17 | Stomp (instagib) |
| `"X softens Y's fall"` | 17 | Stomp |
| `"X tried to catch Y"` | 17 | Stomp |
| `"X was crushed by Y"` | 17 | Stomp |
| `"X was jumped by Y"` | 17 | Stomp |
| `"X was hooked by Y"` | — | Grappling Hook |
| `"X was killed by Y"` | 0 | Generic |
| `"X was fragged by Y"` | 0 | Generic |

### Kill Patterns — Killer First (2-player: `"Y <pattern> X"`, reverse=true)

| Pattern | Weapon ID | Weapon |
|---------|-----------|--------|
| `"Y rips X a new one"` | 7 | RL + Quad (gib) |
| `"Y stomps X"` | 17 | Stomp |
| `"Y squishes X"` | 14 | Squish |

### Team Kill Patterns (1-player, killer only)

| Pattern | Weapon ID | Description |
|---------|-----------|-------------|
| `"X gets a frag for the other team"` | 16 | Self-frag teamkill |
| `"X mows down a teammate"` | 16 | Killed teammate |
| `"X squished a teammate"` | 16 | Squished teammate |
| `"X checks his glasses"` | 16 | Teamkill (male) |
| `"X checks her glasses"` | 16 | Teamkill (female) |
| `"X loses another friend"` | 16 | Teamkill |

### Team Kill Patterns (1-player, victim only)

| Pattern | Weapon ID | Description |
|---------|-----------|-------------|
| `"X was telefragged by his teammate"` | 12 | Teammate telefrag |
| `"X was telefragged by her teammate"` | 12 | Teammate telefrag |
| `"X was crushed by his teammate"` | 17 | Teammate stomp |
| `"X was crushed by her teammate"` | 17 | Teammate stomp |
| `"X was jumped by his teammate"` | 17 | Teammate stomp |
| `"X was jumped by her teammate"` | 17 | Teammate stomp |

### Weapon Name Suffixes

Kill messages often include possessive weapon names after the killer. These must be stripped to extract the killer name:

| Suffix | Weapon | Notes |
|--------|--------|-------|
| `"'s shaft"` | LG | |
| `"'s rocket"` | RL | |
| `"'s quad rocket"` | RL + Quad | |
| `"'s pineapple"` | GL | |
| `"'s grenade"` | GL | Appears in gib messages |
| `"'s boomstick"` | SG | |
| `"'s buckshot"` | SSG | |
| `"'s axe"` | Axe | |
| `"'s discharge"` | LG (discharge) | |
| `"'s batteries"` | LG (discharge) | |
| `"'s fall"` | Stomp | |

For names ending in 's', KTX uses `"' rocket"` instead of `"'s rocket"` (e.g., `"James' rocket"`).

### Weapon ID Mapping

```c
char *qw_weapon[] = {
    "die",     // 0 - generic death
    "axe",     // 1
    "sg",      // 2 - shotgun
    "ssg",     // 3 - super shotgun
    "ng",      // 4 - nailgun
    "sng",     // 5 - super nailgun
    "gl",      // 6 - grenade launcher
    "rl",      // 7 - rocket launcher
    "lg",      // 8 - lightning gun
    "rail",    // 9 - rail gun
    "drown",   // 10 - environmental (also slime/lava)
    "trap",    // 11 - spike/laser/fireball/explosive box
    "tele",    // 12 - telefrag
    "dschrg",  // 13 - LG discharge
    "squish",  // 14
    "fall",    // 15
    "team",    // 16 - team kill
    "stomps"   // 17
};
```

### Distinguishing `" was gibbed by "` — Grenade vs Rocket

The pattern `"X was gibbed by Y's ..."` appears for both GL and RL gibs. The weapon depends on the suffix:
- `"X was gibbed by Y's grenade"` → GL (weapon 6)
- `"X was gibbed by Y's rocket"` → RL (weapon 7)
- `"X was gibbed by Y's quad rocket"` → RL + Quad (weapon 7)

---

## Entity Update Messages

Entity update messages are the most complex part of the MVD format. They use variable-length encoding based on flag bits, making correct parsing critical — skipping the wrong number of bytes will misalign the reader for all subsequent commands in the same payload.

### svc_spawnbaseline (22) — Entity Baseline

Sets the initial state for an entity. **The 2-byte entity number is a prefix to the baseline body — `svc_spawnstatic` (20) does *not* have this prefix.** Mixing them up costs you 2 bytes of drift per occurrence.

*Source: ezquake `cl_parse.c` — `case svc_spawnbaseline: i = MSG_ReadShort(); CL_ParseBaseline(&cl_entities[i].baseline);`*

```
Offset  Size     Field
------  ----     -----
0       1        svc_spawnbaseline (22)
1       2        entity_number (short)        ← unique to svc_spawnbaseline
3       1        modelindex
4       1        frame
5       1        colormap
6       1        skinnum
7       2 or 4   origin[0] (coord or float if FLOATCOORDS)
+       1        angles[0] (angle byte)
+       2 or 4   origin[1]
+       1        angles[1]
+       2 or 4   origin[2]
+       1        angles[2]
```

**Total size**: 15 bytes (standard coords) or 21 bytes (float coords).

### svc_fte_spawnbaseline2 (66) — FTE Extended Baseline

Uses entity delta format with a 2-byte flag header:

```
0       1        svc_fte_spawnbaseline2 (66)
1       2        flag_word (entity delta flags)
+       variable  entity delta fields (see Entity Delta Format below)
```

### svc_spawnstatic (20) / svc_fte_spawnstatic2 (21)

- `svc_spawnstatic (20)`: A bare baseline body — **no 2-byte entity-number prefix**. Total size **13 bytes** (short coords) or **19 bytes** (float coords). ezquake's `CL_ParseStatic(false)` calls `CL_ParseBaseline` directly with no leading short.
- `svc_fte_spawnstatic2 (21)`: Same wire format as `svc_fte_spawnbaseline2` (2-byte flag word + entity delta fields). Requires `FTE_PEXT_SPAWNSTATIC2` to have been negotiated in `svc_serverdata`.

### svc_packetentities (47) — Entity State Updates

Contains delta-compressed updates for multiple entities. Each entity is encoded as a 2-byte flag word followed by variable-length field data.

```
0       1        svc_packetentities (47)
// Repeating entity entries:
+       2        flag_word (ushort) — 0 = end marker, terminates the list
+       variable  entity delta fields (if flag_word != 0)
// ... more entries ...
+       2        0x0000 (end marker)
```

### svc_deltapacketentities (48) — Delta Entity Updates

Same as `svc_packetentities` but with a leading sequence byte:

```
0       1        svc_deltapacketentities (48)
1       1        from_sequence (frame number to delta from)
// Then identical to svc_packetentities format:
+       2        flag_word
+       variable  entity delta fields
// ...
+       2        0x0000 (end marker)
```

### Entity Delta Format

*Source: `CL_ParseDelta()` in ezQuake `cl_ents.c`*

Each entity update is encoded as a 2-byte flag word followed by conditional fields. The flag word contains:

**Primary flag word (2 bytes, ushort):**

| Bits | Name | Description |
|------|------|-------------|
| 0-8 | Entity number | Entity index (0-511) |
| 9 | `U_ORIGIN1` | Origin X follows |
| 10 | `U_ORIGIN2` | Origin Y follows |
| 11 | `U_ORIGIN3` | Origin Z follows |
| 12 | `U_ANGLE2` | Yaw angle follows |
| 13 | `U_FRAME` | Frame byte follows |
| 14 | `U_REMOVE` | Remove entity (no field data follows) |
| 15 | `U_MOREBITS` | Additional flag byte follows |

**If `U_MOREBITS` is set, read 1 additional byte:**

| Bit | Name | Description |
|-----|------|-------------|
| 0 | `U_ANGLE1` | Pitch angle follows |
| 1 | `U_ANGLE3` | Roll angle follows |
| 2 | `U_MODEL` | Model index byte follows |
| 3 | `U_COLORMAP` | Colormap byte follows |
| 4 | `U_SKIN` | Skin byte follows |
| 5 | `U_EFFECTS` | Effects byte follows |
| 6 | `U_SOLID` | Solid flag (no data read) |
| 7 | `U_FTE_EVENMORE` | FTE extension byte follows |

**If `U_FTE_EVENMORE` is set (and FTE extensions are active), read 1 byte:**

| Bit | Name | Description |
|-----|------|-------------|
| 1 | `U_FTE_TRANS` | Transparency byte follows (if `FTE_PEXT_TRANS`) |
| 3 | `U_FTE_MODELDBL` | Model index high bit / short model |
| 5 | `U_FTE_ENTITYDBL` | Entity number += 512 |
| 6 | `U_FTE_ENTITYDBL2` | Entity number += 1024 |
| 7 | `U_FTE_YETMORE` | Another extension byte follows |

**If `U_FTE_YETMORE` is set, read 1 more byte:**

| Bit | Name | Description |
|-----|------|-------------|
| 2 (bit 10) | `U_FTE_COLOURMOD` | 3 RGB bytes follow (if `FTE_PEXT_COLOURMOD`) |

### Field Reading Order

Fields are read in this exact order when their flag is set:

```
1. [U_MOREBITS]      → 1 byte (low flag byte)
2. [U_FTE_EVENMORE]  → 1 byte (FTE flags)
3. [U_FTE_YETMORE]   → 1 byte (more FTE flags)
4. U_MODEL           → 1 byte (model index)
   - Special: if !U_MODEL && U_FTE_MODELDBL → 2 bytes (short model index)
   - Special: if U_MODEL && U_FTE_MODELDBL → 1 byte (model index + 256)
5. U_FRAME           → 1 byte
6. U_COLORMAP        → 1 byte
7. U_SKIN            → 1 byte
8. U_EFFECTS         → 1 byte
9. U_ORIGIN1         → 2 bytes (coord) or 4 bytes (float if FLOATCOORDS)
10. U_ANGLE1         → 1 byte (angle)
11. U_ORIGIN2        → 2 bytes or 4 bytes
12. U_ANGLE2         → 1 byte
13. U_ORIGIN3        → 2 bytes or 4 bytes
14. U_ANGLE3         → 1 byte
15. U_SOLID          → (no data)
16. U_FTE_TRANS      → 1 byte (if FTE_PEXT_TRANS enabled)
17. U_FTE_COLOURMOD  → 3 bytes (if FTE_PEXT_COLOURMOD enabled)
```

### Entity Constants

```c
// Primary word flags
#define U_ORIGIN1       (1 << 9)     // 0x0200
#define U_ORIGIN2       (1 << 10)    // 0x0400
#define U_ORIGIN3       (1 << 11)    // 0x0800
#define U_ANGLE2        (1 << 12)    // 0x1000
#define U_FRAME         (1 << 13)    // 0x2000
#define U_REMOVE        (1 << 14)    // 0x4000
#define U_MOREBITS      (1 << 15)    // 0x8000

// Low byte flags (from U_MOREBITS byte)
#define U_ANGLE1        (1 << 0)
#define U_ANGLE3        (1 << 1)
#define U_MODEL         (1 << 2)
#define U_COLORMAP      (1 << 3)
#define U_SKIN          (1 << 4)
#define U_EFFECTS       (1 << 5)
#define U_SOLID         (1 << 6)
#define U_FTE_EVENMORE  (1 << 7)

// FTE extension flags (first byte)
#define U_FTE_TRANS     (1 << 1)
#define U_FTE_MODELDBL  (1 << 3)
#define U_FTE_ENTITYDBL (1 << 5)
#define U_FTE_ENTITYDBL2 (1 << 6)
#define U_FTE_YETMORE   (1 << 7)

// FTE extension flags (second byte, shifted << 8)
#define U_FTE_COLOURMOD (1 << 10)
```

### Parsing Pitfalls

**Critical**: Entity updates are variable-length. If you skip the wrong number of bytes for even one entity, all subsequent commands in the same payload will be misaligned. This causes:
- Garbage reads of `svc_updatefrags` (bogus frag counts)
- Lost `svc_print` messages (missing kill attributions)
- Phantom `svc_updatestat[long]` reads with 32-bit garbage values that look like real stats — see the [`svc_temp_entity`](#svc_temp_entity-23) note for a worked example
- Potential parser crashes

The same warning applies verbatim to [`svc_temp_entity`](#svc_temp_entity-23) (TE_GUNSHOT and TE_BLOOD carry an extra `byte count` field), [`svc_sound`](#svc_sound-6) (channel high bits gate optional volume/attenuation bytes), and [`svc_playerinfo`](#svc_playerinfo-42---player-state-update) (DF_* flags gate every coord/angle field). All four are variable-length, all four are silent on misalignment.

Each payload has its own independent byte buffer, so misalignment does not propagate across demo messages — that's why "bail on unknown" is the only safe recovery strategy.

### Item tracking via entity state

The entity-update stream is the **protocol-level ground truth** for pickup-item state — no server-mod protocol (KTX prints) or ahead-of-time BSP preprocessing is needed. Every item has a baseline (from `svc_spawnbaseline`), and every pickup / respawn shows up as a visibility transition in `svc_packetentities` / `svc_deltapacketentities`.

**Pickup / respawn on the wire** (from `ktx/src/items.c` plus the mvdsv entity encoder):

- When a player touches an item, the server-side QuakeC sets `self->model = ""` and `self->s.v.solid = SOLID_NOT`. Empty model string → modelindex 0.
- `mvdsv/src/sv_ents.c:790` filters out entities with modelindex 0 when building the packet-entities list. The delta compressor (`SV_EmitPacketEntities` at `sv_ents.c:313`) notices the entity is no longer in the "to" set and emits **`U_REMOVE`** for it.
- When the respawn think (`SUB_regen` at `ktx/src/items.c:59`) restores the model, modelindex becomes non-zero again. The entity re-enters the packet with delta-from-baseline encoding on the next frame.

**MVD-specific invariant**: MVD recording sets `pvs = NULL` (`mvdsv/src/sv_ents.c:851`), so PVS culling does *not* apply. The only filter on whether an entity appears in a packet is `modelindex != 0`. That means "entity absent from packet" is **equivalent** to "item was picked up" — it is NOT a "camera can't see it" artefact the way it would be on a live client.

**Item classification from baselines** — model paths are standard Quake 1 (id Software originals), not KTX-specific. Map from `(modelindex → model_path, skin)` to a compact kind:

| Kind  | Model path                | Skin | Notes |
|-------|---------------------------|------|-------|
| ga    | `progs/armor.mdl`         | 0    | Green Armor (100) |
| ya    | `progs/armor.mdl`         | 1    | Yellow Armor (150) |
| ra    | `progs/armor.mdl`         | 2    | Red Armor (200) |
| h15   | `maps/b_bh10.bsp`         | —    | Rotten health, 15 HP |
| h25   | `maps/b_bh25.bsp`         | —    | Health box, 25 HP |
| mh    | `maps/b_bh100.bsp`        | —    | Megahealth (+100, rots down) |
| ssg   | `progs/g_shot.mdl`        | —    | Super Shotgun |
| ng    | `progs/g_nail.mdl`        | —    | Nailgun |
| sng   | `progs/g_nail2.mdl`       | —    | Super Nailgun |
| gl    | `progs/g_rock.mdl`        | —    | Grenade Launcher |
| rl    | `progs/g_rock2.mdl`       | —    | Rocket Launcher |
| lg    | `progs/g_light.mdl`       | —    | Lightning Gun |
| shells  | `maps/b_shell{0,1}.bsp`  | —    | Shell boxes |
| nails   | `maps/b_nail{0,1}.bsp`   | —    | Nail boxes |
| rockets | `maps/b_rock{0,1}.bsp`   | —    | Rocket boxes |
| cells   | `maps/b_batt{0,1}.bsp`   | —    | Cell boxes |
| quad  | `progs/quaddama.mdl`      | —    | Quad Damage (60s / 30s practice) |
| pent  | `progs/invulner.mdl`      | —    | Pentagram of Invulnerability (300s / 60s HoonyMode) |
| ring  | `progs/invisibl.mdl`      | —    | Ring of Shadows |
| suit  | `progs/suit.mdl`          | —    | Environmental Suit |
| (drop) | `progs/backpack.mdl`    | —    | Player-dropped weapon/ammo (one-shot, no respawn) |

Resolve `modelindex` → path via the `svc_modellist` table. Index 0 is reserved for the null model (empty string). Paths are case-insensitive in practice.

**Implementation pattern** (see `mvd-reader/parser/entities.go`):

1. On `svc_modellist` / `svc_fte_modellistshort`: populate `modelList[modelindex] = path`.
2. On `svc_spawnbaseline` / `svc_fte_spawnbaseline2`: store `EntityState{modelIndex, origin, skin, frame, ...}` keyed by entity number. Classify against the model path; if recognised, emit `ItemSpawnEvent{EntNum, Kind, Origin, Time}`.
3. On `svc_packetentities` (full) / `svc_deltapacketentities` (delta): maintain a rolling `currentEntities` map. Full packets replace it; deltas copy from previous and apply per-entity updates. `U_REMOVE` deletes; other flags update fields.
4. Diff new frame vs previous frame per tracked item — emit `ItemStateEvent{EntNum, Kind, Taken: bool, Time}` on every visibility flip (present + modelindex > 0 → absent, or vice versa).

Baselines seed the "initial" state so items at match start are already "up". Non-item entities (players, projectiles, lights, triggers) pass through the classifier as empty kind and are silently filtered.

**Known limitation — insta-regrab invisibility**: If an item respawns and is immediately touched within the same server tick (player camping the spawn), the end-of-tick state is "modelindex 0, still absent from packet." The delta compressor has no new bits to emit, and the wire never shows the "respawned then retaken" transition. KTX's `//ktx took` print fires on every touch regardless, so it *does* count those pickups; the entity-state stream does not. For "when is the item practically available?" questions this is actually the more useful signal (the RA was never effectively up during a hotly-contested window), but for per-touch pickup *counts* the KTX stream is more complete for the item classes it covers (armors, MH, weapons, powerups — not small health or ammo).

**Synthesis recovery (analytics layer, not protocol-level)**: The full pickup count can be recovered using two complementary paths, both built from signals already on the wire:

1. **Hint-driven (preferred when KTX emits one).** When a `//ktx took` arrives for an entity that's still in "taken" state from the previous pickup (no wire respawn observed since), it can only be an insta-regrab — record the pickup immediately with the slot from the hint as authoritative attribution. Covers MH, GA/YA/RA, the six slot weapons, and Quad/Pent/Ring on KTX servers. Same logic applies to MH (the previous holder's `heldMHs` slot is transferred to the new picker so the existing rot tracker stamps `RespawnAt` on the right phase).

2. **Stat-delta-driven (fallback for non-hinted kinds).** For items KTX doesn't hint (small healths, ammo boxes): after every `Taken=true(ent, T)`, schedule a synthesis check at `T + respawnSec[kind]`; if a unique player has stat-delta evidence consistent with the kind near that time *and* their position was within touch radius of the entity origin, record a synthetic pickup. The classifier accepts any positive `STAT_HEALTH` delta in [1, 25] as h15-or-h25 evidence — KTX's `T_Heal` (`ktx/src/items.c:184`) caps health at 100, so a pickup at 80 HP yields a +20 delta but `tooks` still increments. The chain-forward path is disabled for MH because its predicted respawn is rot-dependent.

Both paths terminate cleanly: a wire `Taken=false` cancels any pending schedule (the entity genuinely respawned without being re-grabbed). The mvd-analytics implementation lives in [`mvd-analytics/analyzer/items.go`](../mvd-analytics/analyzer/items.go) (`recordSyntheticTakeFromHint` / `processSyntheticRespawns` / `findSyntheticPicker`); on the project's hub corpus, 9 of 10 demos match KTX's authoritative `demoInfo.players[*].items[*].took` exactly across every hinted kind (the lone residual is two same-magnitude small healths contested in a single frame).

### Derived events — death and spawn

`StatHealth` transitions are the protocol-level ground truth for player alive/dead state. The parser in this project synthesises two derived events from `svc_updatestat(long)` StatHealth payloads (`mvd-reader/parser/stats.go:114`):

- **DeathEvent** fires when StatHealth crosses from >0 to ≤0.
- **SpawnEvent** fires when StatHealth crosses from ≤0 to >0.

Both carry `{PlayerNum, Time}` at the exact stat-update tick. The value of this over "compare prevHealth vs. health at each 50 ms sample" is the **instant-respawn** case: a player gibbed deep-negative (e.g. `health = -60`) can be respawned in the same 50 ms window (KTX forcerespawn on gib), and sample-based detection would see `prevHealth = 100, health = 100` at the next boundary and miss both transitions. The parser, looking at every stat update as it arrives, catches the `100 → -60` and `-60 → 100` pair independently.

Consumers that want **killer / weapon attribution** still go to the obituary parser (see [Obituary Messages (Frag Detection)](#obituary-messages-frag-detection) and [Obituary Message Patterns (KTX)](#obituary-message-patterns-ktx)) — attribution is KTX-mod-specific text parsing, not a protocol signal.

---

## Additional svc_* Command Structures

### svc_sound (6)

```
Offset  Size  Field
------  ----  -----
0       1     svc_sound (6)
1       2     channel (short) - bits encode volume/attenuation presence
if (channel & 0x8000):
          1     volume
if (channel & 0x4000):
          1     attenuation
          1     sound_num
          6/12  origin (3 coords, size depends on FLOATCOORDS)
```

### svc_temp_entity (23)

Spawns a transient visual effect (gunshot puff, blood, lightning beam, lava splash, teleport sparkles, etc.). The wire format is **type-dependent** — the size of the payload after the type byte varies per `TE_*` constant. **This is the single most error-prone command in the protocol** because the parser can't fall back to a generic length: skipping the wrong number of bytes for one TE entry will silently misalign every command after it in the same payload.

```
Offset  Size   Field
------  ----   -----
0       1      svc_temp_entity (23)
1       1      te_type (TE_* constant)
2+      var    type-specific payload
```

#### TE Type Sizes

*Source: ezQuake `cl_tent.c::CL_ParseTEnt`, mvdsv `cl_parse.c`. The QW protocol uses short coords (2 bytes each) by default; with `FTE_PEXT_FLOATCOORDS` negotiated, every coord becomes a 4-byte IEEE float.*

| ID | Name | Wire format | Size (short coords) | Size (float coords) |
|----|------|-------------|---------------------|---------------------|
| 0 | `TE_SPIKE` | 3 coords | 6 | 12 |
| 1 | `TE_SUPERSPIKE` | 3 coords | 6 | 12 |
| **2** | **`TE_GUNSHOT`** | **byte count + 3 coords** | **7** | **13** |
| 3 | `TE_EXPLOSION` | 3 coords | 6 | 12 |
| 4 | `TE_TAREXPLOSION` | 3 coords | 6 | 12 |
| 5 | `TE_LIGHTNING1` | beam: short entity + 3 coords (start) + 3 coords (end) | 14 | 26 |
| 6 | `TE_LIGHTNING2` | beam | 14 | 26 |
| 7 | `TE_WIZSPIKE` | 3 coords | 6 | 12 |
| 8 | `TE_KNIGHTSPIKE` | 3 coords | 6 | 12 |
| 9 | `TE_LIGHTNING3` | beam | 14 | 26 |
| 10 | `TE_LAVASPLASH` | 3 coords | 6 | 12 |
| 11 | `TE_TELEPORT` | 3 coords | 6 | 12 |
| **12** | **`TE_BLOOD`** | **byte count + 3 coords** | **7** | **13** |
| 13 | `TE_LIGHTNINGBLOOD` | 3 coords (NOT a beam, despite the name) | 6 | 12 |

**Easy to get wrong:**

1. **`TE_GUNSHOT` (2) and `TE_BLOOD` (12) carry an extra `byte count` field** before the 3 coords. They are the only two QW TE types that do this. Treating them as plain "3 coord" entries causes a 1-byte under-skip per occurrence — and a frame with several rocket impacts contains a *run* of TE_BLOOD entries, so the drift compounds quickly.
2. **Beams are 14 bytes (short coords), not 16.** The wire format is `short entity + 3 short coords (start) + 3 short coords (end)` = 2 + 6 + 6 = 14 bytes. With float coords it's 2 + 12 + 12 = 26.
3. **`TE_LIGHTNINGBLOOD` (13) is NOT a beam.** Despite the "lightning" prefix it has the same 3-coord format as a regular blood splat — there is no entity number, no start/end pair.
4. **Unknown TE types must abort the message, not guess.** If you ever encounter a TE type you don't recognise (an FTE/QuakeForge extension you haven't implemented), the only safe action is to bail out of the current payload — do **not** assume "probably 3 coords" because a single byte of drift here will produce a phantom `svc_updatestatlong` later in the same payload, with a believable stat index and a 32-bit garbage value that will blow up any downstream graph autoscale.

#### Reference implementation

```go
func skipTempEntity(r *BufferReader, floatCoords bool) error {
    teType, err := r.ReadByte()
    if err != nil { return err }

    coordSize := 2
    if floatCoords {
        coordSize = 4
    }
    switch teType {
    case 0, 1, 3, 4, 7, 8, 10, 11, 13: // 3 coords
        return r.Skip(3 * coordSize)
    case 2, 12: // byte count + 3 coords
        return r.Skip(1 + 3*coordSize)
    case 5, 6, 9: // beam: short entity + 3 coords + 3 coords
        return r.Skip(2 + 6*coordSize)
    default:
        return io.EOF // bail rather than guess
    }
}
```

#### Symptom of getting this wrong

In `broken.mvd.gz` (a real demo we hit during development) a run of six `TE_BLOOD` events at the same instant — a normal multi-rocket impact frame — caused a parser that treated TE_BLOOD as 6 bytes to drift through several mis-skipped records, eventually misreading byte `0x26` as `svc_updatestatlong` and consuming the next 5 bytes (`04 e4 f8 49 0a`) as `stat=4 (armor) value=172620004`. Downstream the timeline analyzer dutifully recorded an armor of 172 million for one player; the team `avgArmor` graph autoscaled and looked empty for the entire match. The bytes were never an updatestatlong — they were the type+count+coord-Y of a perfectly legal TE_BLOOD record. The lesson: a single missed byte in `skipTempEntity` will not announce itself as a parse error, it will hand back plausible-looking corrupt data many bytes later.

---

### svc_damage (19)

Local damage feedback the server originally addressed to a single live player. In a server-recorded MVD it shows up inside `dem_single` blocks and inside `dem_all` blocks during multi-player matches. Carries no useful information for analysis (it's just the screen-tint and sbar pain-frame trigger), but you **must** skip it correctly or every command after it in the same payload is lost.

*Source: qwprot `protocol.h` — `#define svc_damage 19  // [byte] [byte] [vec3]`. Body parser: ezquake `cl_view.c::V_ParseDamage()`.*

```
Offset  Size      Field
------  ----      -----
0       1         svc_damage (19)
1       1         armor_taken
2       1         blood_taken
3       6 or 12   from (3 coords, short or float depending on FLOATCOORDS)
```

**Total size after the cmd byte**: **8 bytes** (short coords) or **14 bytes** (float coords).

**Easy to miss**: `svc_damage` is not in NetQuake — it was added in QuakeWorld — and a parser written from a NQ-era reference will treat byte `0x13` as unknown. In a busy 4-on-4 KTX demo this happens **thousands** of times per match (every hit on every player), and each occurrence abandons the rest of the payload it landed in, which routinely silently drops `svc_updateuserinfo` for late-joining players, `svc_updatefrags` for the next few seconds, and any other svc that happened to be queued behind it.

### svc_stufftext (9)

```
Offset  Size  Field
------  ----  -----
0       1     svc_stufftext (9)
1       var   command (null-terminated string)
```

The server is pushing a console command into the client. There are three classes of stufftext you'll see in MVDs and they're all worth surfacing as parser events:

1. **`fullserverinfo "\key1\value1\key2\value2\..."`** — the bulk cvar dump sent at connection time. This is the **single richest source of server metadata** in the demo: every CVAR_SERVERINFO cvar that mvdsv exposes plus every key KTX has mirrored via `localcmd "serverinfo …"`. See [Demo Metadata Sources](#demo-metadata-sources) below for the full key list.
2. **`//ktx …` directives** — KTX-specific client hints prefixed with `//` so older clients silently drop them. Emitted via `stuffcmd_flags(..., STUFFCMD_DEMOONLY, ...)` so they only appear in the recorded MVD, not in live gameplay. The common ones:

    | Directive | Source | Meaning |
    |---|---|---|
    | `//ktx matchstart` | `ktx/src/match.c:1249` | Fires once when warmup ends and the match begins |
    | `//ktx took <ent> <respawn_sec> <player_ent>` | `ktx/src/items.c:355, 541, 1048, 2074, 2083` | Item picked up. `respawn_sec` is the nominal timer (0 for MH pending rot, 20 for armors, 30 for weapons, 60/180/240/300 for powerups depending on mode). Pinning the touch to `player_ent` makes this the **authoritative pickup-attribution signal** for every competitive item type (MH, GA/YA/RA, RL/LG/GL/SSG/SNG/NG, Quad/Pent/Ring). Does *not* fire for small healths (15/25) or in `deathmatch 2`. |
    | `//ktx timer <ent> <respawn_sec>` | `ktx/src/items.c:406` | MH rot finished — the delayed 20 s respawn timer is now armed |
    | `//ktx drop <ent> <item_flags> <player_ent>` | `ktx/src/items.c:2740` | Player dropped a weapon (backpack spawned). `item_flags` is the QW items bitfield — `32 = IT_ROCKET_LAUNCHER`, `64 = IT_LIGHTNING`. Only fires for RL/LG packs. |
    | `//ktx bp <backpack_ent> <player_ent>` | `ktx/src/items.c:2471` | Backpack picked up. Symmetric to `//ktx drop` — fires only when the pack contains RL or LG, so drop/pickup pairs share `backpack_ent`. This is the **only** reliable backpack-pickup attribution signal: the entity-state stream shows visibility flutter for backpack edicts that is indistinguishable from fast regrabs. |
    | `//ktx expire <ent>` | `ktx/src/g_spawn.c:200` | Item entity is about to be removed (mode-specific despawn, e.g. powerup expires unattended) |
    | `//ktx di <dmg> <armor> <take> <attacker> <victim> <weapon> <team>` | `ktx/src/combat.c:819` | Damage-info hint, targeted at the *attacker* (client-side HUD). Emitted with `STUFFCMD_IGNOREINDEMO`, so it's not in the recorded MVD — listed here for completeness. |
    | `//wps <slot> <weapon> <hits> <shots>` | `ktx/src/stats.c` | Per-player weapon-stats ticker for the spectator HUD |

    These are *not* server config — they're per-event HUD hooks for clients that understand the KTX HUD protocol. They are also **KTX-specific**: ktpro, CustomTF, and other progs don't emit them.

    **Pickup attribution strategy** — the `//ktx took` + `//ktx bp` pair gives deterministic, same-tick pickup attribution without any heuristic. On KTX servers, analytics should prefer these over the [entity-state stream](#item-tracking-via-entity-state) for identifying *who took what*: the entity stream tells you *when* a pickup happened but not reliably by whom (nearest-player-origin is ambiguous in contests and unusable for backpack regrabs due to visibility flutter). For non-KTX servers (ktpro / CustomTF / vanilla), fall back to the entity-state stream or to per-player `svc_updatestat` deltas (STAT_ITEMS bitfield, STAT_HEALTH / STAT_ARMOR / STAT_* AMMO jumps — the MVD transports every player's stats, not just the POV's).

    **Practical gap — non-RL/LG backpack pickups on competitive demos.** Combining the two limits documented above leaves a hole in the wire signal:

    - `//ktx bp` only fires for RL/LG packs (line above), so SSG/NG/SNG/GL/ammo-only packs have no `//ktx`-driven attribution.
    - The fallback signal — `BackpackPickupPrintEvent` from KTX's `"You get "` opener — is `G_sprint(PRINT_LOW)`, which `SV_ClientPrintf` (`mvdsv/src/sv_send.c:225`) drops *before* the MVD-write step when the picking client has `messagelevel >= 1`. Competitive players overwhelmingly run `msg 2`, so on a typical 4on4 / duel **the pickup-print bytes are never written to the demo file** — see [Per-client pickup prints — and the PRINT_LOW filter](#per-client-pickup-prints--and-the-print_low-filter).

    Net result on a competitive MVD: there is **no authoritative wire signal** for non-RL/LG backpack pickups. The demo still carries indirect evidence — the picker's STAT_ITEMS bit flips on for the gained weapon, and STAT_SHELLS / STAT_NAILS / STAT_ROCKETS / STAT_CELLS all jump to reflect the absorbed ammo, both arriving as ordinary `svc_updatestat` per-slot — so a heuristic analyzer could correlate stat deltas with nearby `BackpackDropHintEvent` edicts to attribute these pickups by proximity and timing. No analyzer in this repo ships that logic today; `result.Backpacks` and `result.WeaponPickups` consequently cover only the RL/LG domain.

    The `<ent>` number is a stable server edict index for the match. `<player_ent>` is `slot + 1` (edict 0 is world, edicts 1..maxclients are the player slots).
3. **`play sound/file.wav`** style commands — countdown beeps, intermission music. Safe to ignore.

A single-pass parser should emit all three as a `StuffTextEvent` and let the consuming analyzer decide which prefix to react to.

### svc_setangle (10)

```
Offset  Size  Field
------  ----  -----
0       1     svc_setangle (10)
1       1     pitch (angle)
2       1     yaw (angle)
3       1     roll (angle)
```

### svc_lightstyle (12)

```
Offset  Size  Field
------  ----  -----
0       1     svc_lightstyle (12)
1       1     style_index
2       var   pattern (null-terminated string, e.g., "mmmaaaggg")
```

### svc_centerprint (26)

```
Offset  Size  Field
------  ----  -----
0       1     svc_centerprint (26)
1       var   text (null-terminated string)
```

KTX uses `svc_centerprint` for the **match-settings table that renders during the 10-second countdown** — see [Demo Metadata Sources](#demo-metadata-sources). The string is a multi-line column-aligned table built by `ktx/src/match.c::PrintCountdown()`, with values encoded as `redtext()` (high-bit gold characters) and digits encoded by `dig3()` as ASCII+98 (so `'1'` = `0x93`). After running the payload through [`Q_normalizetext`](#player-name-normalization) you get readable rows like:

```
Countdown:  3
Deathmatch  3
Mode      LGC
Respawns  KT2
Antilag     1
Timelimit  10
Overtime   sd
Dmgfrags   on
Noweapon   gl
matchtag draft
```

There are other centerprints throughout a match (round announcements, "Protection is almost burned out", item-pickup hints), but the countdown one is the only one that contains structured per-cvar match settings, and it's repeated once per second of countdown so a parser can latch the last sample seen before the `match has begun!` print and parse it offline.

### svc_spawnstaticsound (29)

Looping ambient sound source attached to a fixed position (lava bubbles, water hum, generator drone). Sent once per source during the initial `dem_all` setup burst.

*Source: ezquake `cl_parse.c::CL_ParseStaticSound`.*

```
Offset  Size      Field
------  ----      -----
0       1         svc_spawnstaticsound (29)
1       6 or 12   origin (3 coords, short or float)
+       1         sound_num
+       1         volume
+       1         attenuation
```

**Total size after the cmd byte**: **9 bytes** (short coords) or **15 bytes** (float coords). Note: it is *not* `3 + 3 + 3 + 2` — there is no spare byte. Earlier versions of this analyzer skipped 11/17 bytes here and accumulated 2 bytes of drift per static-sound entry, which on a busy map blew out the entity setup packet entirely.

### svc_intermission (30)

The server has entered the post-match scoreboard / camera-orbit screen. After this command the server stops sending stat updates and the recorded view freezes — but `svc_playerinfo` position updates continue for several seconds while the camera glides into place.

This is the **only reliable end-of-match signal** in some demos. KTX historically broadcasts a `bprint` like `"The match is over\n"` at the same instant, but not every server flavour or every game mode does, so a print-based detector will miss demos like the `2c8bb5e3…` one in `demos/` that never emit a matching string. `svc_intermission` is always present.

*Source: qwprot `protocol.h` — `#define svc_intermission 30  // [vec3_t] origin [vec3_t] angle`. Note that "vec3_t angle" here means three angle *bytes*, not three short angles.*

```
Offset  Size  Field
------  ----  -----
0       1     svc_intermission (30)
1       6     camera origin (3 short coords)
7       3     camera angles (3 angle bytes, 1 byte each)
```

**Total size after the cmd byte**: **9 bytes** under standard short coords. With `FTE_PEXT_FLOATCOORDS` the origin becomes 12 bytes (3 floats), so total is **15 bytes** — but in practice intermission camera coords are sent at the standard short precision even in FLOATCOORDS demos because the camera doesn't need sub-unit accuracy. Verify against your reference implementation if you support FTE FLOATCOORDS.

**Common bug**: assuming "vec3 + vec3" means 12 bytes (3 + 3 shorts). It's `6 + 3 = 9`. Skipping 12 over-runs the end-of-payload by 3 bytes on the very last `dem_all` block of the demo, causing a parse-error warning right at `EndOfDemo`.

**Suggested handling**: emit an `IntermissionEvent` (or whatever your event abstraction looks like) so downstream stat/timeline analyzers can stop sampling. KTX uses several stat fields as out-of-band HUD signalling channels (see [KTX Stat Sentinels](#ktx-stat-sentinels-out-of-band-huge-stat-values) below), and once the camera enters intermission those sentinels stop being overwritten, freezing into every subsequent sample.

### svc_updateping (36)

```
Offset  Size  Field
------  ----  -----
0       1     svc_updateping (36)
1       1     player_number
2       2     ping (short, milliseconds)
```

**Total size after the cmd byte**: 3 bytes. The ping is a `short`, **not** a byte. Treating it as a 2-byte payload (1 byte player + 1 byte ping) drifts by 1 byte per `svc_updateping` — and `svc_updateping` is sent once per player per second, so a ~20-minute 4-on-4 demo accumulates **9600 byte-drifts** that present as completely random "unknown svc command" warnings spread across the demo, with one initial setup-packet drop that loses ~5 of 8 players' `svc_updateuserinfo` (worked example: hub demo 121329 — analyzer rendered only 3 of 8 players in the timeline until this single byte was fixed).

### svc_updatepl (53)

```
Offset  Size  Field
------  ----  -----
0       1     svc_updatepl (53)
1       1     player_number
2       1     packet_loss (percentage, 0-100)
```

### svc_setinfo (51)

```
Offset  Size  Field
------  ----  -----
0       1     svc_setinfo (51)
1       1     player_number
2       var   key (null-terminated string)
?       var   value (null-terminated string)
```

**This is a single-key update**, sent any time *one* userinfo key changes mid-game (`name`, `team`, `topcolor`, `bottomcolor`, `*spectator`, etc.). It's the lightweight counterpart to `svc_updateuserinfo`, which sends the entire userinfo string. A parser that only handles `svc_updateuserinfo` will miss every mid-game name/team change and end up with a stale `Players[slot]` record.

Treat `svc_setinfo` as authoritative for the listed key only — do not clear other fields. In practice you'll see `chat=1` / `chat=` toggles (typing indicator) most often, but `name`, `team`, and skin updates also flow through this command.

### svc_serverinfo (52)

```
Offset  Size  Field
------  ----  -----
0       1     svc_serverinfo (52)
1       var   key (null-terminated string)
?       var   value (null-terminated string)
```

This is a **single-key serverinfo update** sent mid-game when the server changes one cvar that's mirrored to serverinfo. The bulk dump arrives via `fullserverinfo` stufftext at connection time — see [Demo Metadata Sources](#demo-metadata-sources). Common `svc_serverinfo` updates during a match:

- `status` → `Countdown` → `"3 min left"` → `"2 min left"` → `"1 min left"` → `Standby` (KTX cycles this every minute via `match.c::CheckTimelimit`)
- `fpd` → bitmask of "feature point disabled" flags that change when admins flip rules
- `matchtag` → set when an admin runs `/matchtag <name>` mid-match
- `mode` → set once at level start (e.g. `"duel"`, `"4on4"`, `"ffa"`)
- `serverdemo` → name of the .mvd file the server is currently recording

**Last write wins**: a metadata extractor should keep a flat `map[string]string` and apply each `svc_serverinfo` update by overwriting the entry. The final-state map is what tournament viewers want.

---

## Demo Metadata Sources

If you want to know "what server, what map, what ruleset, what timelimit, what spawn algorithm, who has handicap" for a demo, the data comes from **four separate places** in the protocol. None of them on their own is sufficient. A complete metadata extractor needs to merge all four.

### 1. `fullserverinfo` stufftext (server cvars)

The server's first svc_stufftext in the demo is always:

```
fullserverinfo "\maxfps\77\timelimit\10\teamplay\2\hostname\la.quake.world…\*version\MVDSV 1.20-dev\ktxver\1.45\…"
```

— a single `\key\value\…` blob containing every cvar with the `CVAR_SERVERINFO` flag, plus every key KTX has explicitly mirrored via `localcmd "serverinfo …"`. To extract: pull the quoted string out of the stufftext command body, split on `\`, take alternating tokens as (key, value).

**Standard mvdsv `CVAR_SERVERINFO` keys** *(source: `mvdsv/src/sv_main.c`)*:

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `maxfps` | int | 77 | Server's max packets-per-second to clients (misnamed historically) |
| `fraglimit` | int | 0 | 0 = no limit |
| `timelimit` | int | 0 | minutes |
| `teamplay` | int | 0 | 0 = FFA, 1 = no team damage, 2 = team-color rules, etc. |
| `maxclients` | int | 24 | player slots |
| `maxspectators` | int | 8 | spectator slots |
| `deathmatch` | int | 3 | 1=spawn weapons, 2=no items, 3=KTX standard, 4=LGC mode |
| `watervis` | int | 0 | water visibility flag |
| `serverdemo` | string | — | filename of the .mvd being recorded right now (CVAR_ROM) |
| `hostname` | string | "unnamed" | server display name |
| `sv_bigcoords` | string | — | 1 = FTE_PEXT_FLOATCOORDS active |
| `needpass` | int | — | 1 = password-protected |

**Standard mvdsv `*` (system) keys** — these are starkey serverinfo values the server controls directly:

| Key | Meaning |
|-----|---------|
| `*version` | `"MVDSV <version>"` |
| `*z_ext` | ZQuake extensions bitmask (decimal int) — 511 means all available |
| `*cheats` | `"ON"` if `sv_cheats 1`, otherwise unset |
| `*admin` | admin contact string from `sv.cfg` |
| `*gamedir` | gamedir name (typically `qw`) |
| `*qvm` | game module type (`so` for native, `qvm` for bytecode) |
| `*progs` | progs file in use (typically `so`) |

**KTX-added keys** *(source: `ktx/src/g_main.c`, `ktx/src/match.c`, `ktx/src/world.c`)*:

| Key | Meaning |
|-----|---------|
| `ktxver` | KTX mod version, e.g. `"1.45"` or `"1.47-dev-qwc"` (set via `localcmd "serverinfo ktxver …"`) |
| `mode` | KTX game mode label: `"duel"`, `"1on1"`, `"2on2"`, `"4on4"`, `"ffa"`, `"ctf"`, etc. |
| `status` | `"Countdown"` / `"3 min left"` / `"Standby"` / `"Forcestart"` — cycles via `svc_serverinfo` updates |
| `fpd` | "feature point disabled" bitmask (admin-controlled rules) |
| `matchtag` | tournament/event tag set via `/matchtag <name>` |
| `epoch` | unix timestamp (seconds) when the demo started |
| `pm_ktjump` | KT-style jumping enabled |
| `sv_antilag` | server antilag mode (0/1/2) |

### 2. svc_serverinfo updates (mid-game changes)

Single-key updates flow via `svc_serverinfo` (cmd 52). Apply them last-write-wins on top of the fullserverinfo map. Most updates touch `status` (timer ticks), `fpd` (admin rule changes), and `serverdemo` (when KTX renames the recording).

### 3. Countdown centerprint (match settings — best source)

KTX renders the **complete match-settings table** into an `svc_centerprint` once per second during the 10-second pre-match countdown — see [`svc_centerprint`](#svc_centerprint-26). This is the **most reliable source** for cvars that aren't exposed via serverinfo, in particular the spawn algorithm. After running it through `Q_normalizetext` you get rows like:

| Row label | Meaning | Source |
|-----------|---------|--------|
| `Mode` | Game mode (`Duel`, `Team`, `FFA`, `CA`, `CTF`, `LGC`, `BlitzTDM`, `Hoony`, `RACE`, etc.) | `match.c:1410-1447` |
| `Deathmatch` | `deathmatch` cvar value | `match.c:1384` |
| `Teamplay` | `teamplay` cvar value (only printed in team modes) | `match.c` |
| `Timelimit` | minutes | `match.c` |
| `Fraglimit` | frags | `match.c` |
| `Respawns` | spawn algorithm short name — see [Spawn Algorithm](#spawn-algorithm) below | `match.c:1475` via `respawn_model_name_short` |
| `Antilag` | `sv_antilag` cvar (0/1/2) | `match.c:1481` |
| `Overtime` | minutes (e.g. `5`) or `sd` for sudden death | `match.c` |
| `Powerups` | `on` / `off` / `QPRS` (per-powerup mask) | `match.c` |
| `Dmgfrags` | `on` if `k_dmgfrags` enabled (LGC scoring etc.) | `match.c` |
| `NoItems` | `on` if `k_noitems` enabled | `match.c:1486` |
| `Midair` | `on` if `k_midair` enabled | `match.c:1491` |
| `Instagib` | `on` if `k_instagib` enabled | `match.c:1496` |
| `Yawnmode` | `on` if `k_yawnmode` enabled | `match.c:1501` |
| `Airstep` | `on` if `pm_airstep` enabled | `match.c:1506` |
| `VWep` | `on` if vweps enabled and available | `match.c:1512` |
| `Noweapon` | space-separated list of disabled weapons (e.g. `gl axe`) | `match.c` |
| `matchtag` | tournament tag (or `no matchtag` line) | `match.c` |
| `SOCDv2` | SOCD-cleaning mode: `stats` / `warn` / `block` | `match.c` |
| `Handicap in use` | only printed when at least one player has a non-default handicap | `match.c:1356` |

#### Spawn Algorithm

The `Respawns` row is `respawn_model_name_short(k_spw)` from `ktx/src/g_utils.c:2689`:

| `k_spw` | Short name | Long name |
|---------|------------|-----------|
| 0 | `QW` | Normal QW respawns |
| 1 | `KTS` | KT SpawnSafety |
| 2 | `KT` | Kombat Teams respawns |
| 3 | `KTX` | KTX respawns |
| 4 | `KT2` | KTX2 respawns |

In practice nearly every modern competitive demo uses `KT2` (k_spw=4).

### 4. demoinfo JSON (per-player stats incl. handicap)

The `mvdhidden_demoinfo` (0x0003) JSON dump KTX writes at end-of-match contains a `players[]` array, and each entry has an optional `handicap` field which is **only emitted when the player's handicap is non-default (not 100)**:

```json
{
  "name": "alice",
  "team": "blue",
  "ping": 25,
  "login": "alice@qwbr",
  "handicap": 75,          // ← only present when != 100
  "bot": { "skill": 10, "customised": false },   // ← only present for frogbots
  "stats": { "frags": 42, ... },
  ...
}
```

If you need a definitive list of who has a handicap and what value, you must wait for the demoinfo block (which arrives only at intermission). The countdown centerprint mentions handicap in aggregate ("Handicap in use") but doesn't say which player or what value.

The `bot` field is only written when KTX was built with `BOT_SUPPORT` and the player slot is held by a frogbot. Useful for filtering bot-vs-human matches and for distinguishing replay-against-bot training demos from real games.

### Putting it all together

A complete metadata extractor needs to:

1. Listen for `svc_stufftext` and on the `fullserverinfo "..."` command, split the quoted blob into a `serverInfo` map.
2. Listen for `svc_serverinfo` and apply each update to the same map (last write wins).
3. Listen for `svc_centerprint` and for any centerprint that contains `"Countdown:"` (after Q_normalizetext), keep the *last* one observed before `"the match has begun"` arrives via `svc_print`. Parse it line-by-line into a structured `MatchSettings` view.
4. Parse the `mvdhidden_demoinfo` JSON and surface per-player `handicap` and `bot` fields.

Steps 1 and 3 are independent of the player-stat machinery and can run in parallel with the match analyzer. Step 4 is already part of any KTX-aware analyzer.

---

## Demo End Marker

The demo ends with a disconnect message:

```
Offset  Size  Field
------  ----  -----
0       1     time_delta (0)
1       1     message_type (dem_all = 6)
2       4     payload_size
6       1     svc_disconnect (2)
7       var   "EndOfDemo" (null-terminated string)
```

---

## Parsing Algorithm

### Pseudocode

```python
def parse_mvd(file):
    cumulative_time = 0.0

    while not eof(file):
        # Read message header
        time_delta = read_byte(file)
        type_byte = read_byte(file)

        # Accumulate time (delta is in milliseconds)
        cumulative_time += time_delta / 1000.0

        message_type = type_byte & 0x07

        if message_type == DEM_SET:  # 2
            incoming_seq = read_long(file)
            outgoing_seq = read_long(file)
            # No payload

        elif message_type == DEM_MULTIPLE:  # 3
            player_mask = read_long(file)
            size = read_long(file)
            payload = read_bytes(file, size)

            if player_mask == 0:
                parse_hidden_messages(payload)
            else:
                parse_network_message(payload)

        elif message_type == DEM_SINGLE:  # 4
            player_num = type_byte >> 3
            size = read_long(file)
            payload = read_bytes(file, size)
            parse_network_message(payload, target_player=player_num)

        elif message_type == DEM_STATS:  # 5
            player_num = type_byte >> 3
            size = read_long(file)
            payload = read_bytes(file, size)
            parse_stats_message(payload, player_num)

        elif message_type == DEM_ALL:  # 6
            size = read_long(file)
            payload = read_bytes(file, size)
            parse_network_message(payload)

        elif message_type == DEM_READ:  # 1
            size = read_long(file)
            payload = read_bytes(file, size)
            parse_network_message(payload)

def parse_network_message(data):
    while data.remaining():
        cmd = read_byte(data)

        if cmd == SVC_DISCONNECT:
            msg = read_string(data)
            if msg == "EndOfDemo":
                return END_OF_DEMO

        elif cmd == SVC_SERVERDATA:
            parse_serverdata(data)

        elif cmd == SVC_PLAYERINFO:
            parse_playerinfo_mvd(data)

        elif cmd == SVC_UPDATESTAT:
            stat = read_byte(data)
            value = read_byte(data)

        elif cmd == SVC_PRINT:
            level = read_byte(data)
            message = read_string(data)
            if level == PRINT_MEDIUM:
                detect_frag(message)

        elif cmd == SVC_TEMP_ENTITY:
            # Type-dependent skip — see svc_temp_entity section above.
            # Bail on unknown TE types; do NOT guess a length.
            skip_temp_entity(data, float_coords)

        elif cmd == SVC_SETINFO:
            # Single-key userinfo update; updates one field in players[slot].
            slot = read_byte(data)
            key = read_string(data)
            value = read_string(data)
            apply_setinfo(slot, key, value)

        # ... handle other svc_* commands
        else:
            # Unknown command - bail out of this payload entirely.
            # Each dem_* message has its own buffer, so the next message
            # starts fresh. Never try to "skip a guessed number of bytes".
            break
```

---

## Practical Implementation Notes

### Robustness Tips

1. **Validate player numbers**: Always check `player_num < MAX_CLIENTS (32)` before array access.

2. **Bail, don't guess, on unknown commands**: When `skipCommand` cannot determine the size of a command (unknown opcode, unknown TE type, unknown FTE extension), the only safe action is to abandon the rest of the current payload and continue with the next demo message. **Never** "skip a guessed number of bytes" — drift inside a payload silently produces plausible-looking corrupt data many bytes later (see the [`svc_temp_entity` example](#svc_temp_entity-23): one missed byte in `skipTempEntity` resurfaces as a phantom `svc_updatestatlong` with a 32-bit garbage armor value).

3. **Each payload is its own buffer**: Misalignment within one `dem_*` message does not propagate to the next. This is what makes "bail on unknown" safe — you lose at most the rest of one ~600-byte payload, not the whole demo.

4. **Time tracking**: Accumulate time_delta values to track demo time. A typical 20-minute demo will have cumulative time around 1200 seconds.

5. **Character encoding**: Player names may contain high-bit characters (`0x80–0xFF`, gold) **and** low-byte glyphs (`0x00–0x1F`, font icons including bracket-digits and dots). Subtract 128 only handles the gold half — see [Player Name Normalization](#player-name-normalization) for the full table.

6. **Protocol extensions**: Always check for extensions before parsing fields that may have different sizes. The two flags that matter for length calculations are:
   - `FTE_PEXT_FLOATCOORDS` / `MVD_PEXT1_FLOATCOORDS` — coords switch from 2-byte shorts to 4-byte IEEE floats. Affects `svc_sound`, `svc_temp_entity`, `svc_spawnbaseline`, `svc_spawnstatic`, `svc_playerinfo`, `svc_packetentities`, `svc_deltapacketentities`. Read this once from `svc_serverdata` and cache it.
   - `FTE_PEXT_ENTITYDBL`/`ENTITYDBL2`/`MODELDBL`/`COLOURMOD`/`TRANS` — these add conditional bytes to `svc_packetentities` entity deltas. Skipping them wrong corrupts every command after the entity packet in the same payload.

7. **`svc_serverdata` may not be the very first command**: cache the FTE flags in your parser the moment you see it, but be aware that the demo's first network message *usually* but not always begins with `svc_serverdata`. If you skip a command before `svc_serverdata` arrives, you may be skipping with `floatCoords=false` even though the demo turns out to use float coords.

8. **`svc_setinfo` is a real command you must handle**: don't fall back to the generic skipper. See the [`svc_setinfo`](#svc_setinfo-51) section. Mid-game name/team/skin changes flow through here, not through `svc_updateuserinfo`.

9. **Use `svc_intermission` (30) — not just bprint strings — to detect end of match**: KTX is supposed to broadcast `"The match is over"` on timelimit/fraglimit hit, but in real demos in the wild this print is missing roughly half the time. `svc_intermission` is always sent, so it's the reliable end-of-match signal. Stop sampling player state once you see it; otherwise the post-intermission camera glide keeps producing buckets full of stale (and often sentinel-poisoned) values.

### KTX stat sentinels (out-of-band huge stat values)

KTX overloads several player stat fields as out-of-band signalling channels. The values arrive via normal `svc_updatestatlong` and look like real stats, but they are HUD signalling encoded so that legitimate values can't collide with them. Filter them in your stat handler — clamping to "real" maximums is the simplest correct fix, and downstream code will not miss anything (the values are display-side annotations, not gameplay state).

| Stat | Sentinel pattern | Meaning | Source |
|------|------------------|---------|--------|
| `STAT_HEALTH` (0) | `1000 + damage` | Damage-indicator: attacker dealt this much damage on the last hit | [`ktx/src/combat.c:1001`](https://github.com/QW-Group/ktx/blob/master/src/combat.c#L1001) |
| `STAT_ACTIVEAMMO` (10) | `1000 + damage` | Damage-indicator: victim took this much damage on the last hit | [`ktx/src/combat.c:996`](https://github.com/QW-Group/ktx/blob/master/src/combat.c#L996) |
| `STAT_ARMOR` (4) | `velocity + 1000` (when `< 1000`), or `-velocity` (when `≥ 1000`) | Pre-match speed-meter (KF_SPEED keyflag): packs horizontal speed | [`ktx/src/client.c:4329`](https://github.com/QW-Group/ktx/blob/master/src/client.c#L4329) |
| `STAT_FRAGS` (1) | `velocity / 1000` | Pre-match speed-meter: thousands digit of speed | [`ktx/src/client.c:4330`](https://github.com/QW-Group/ktx/blob/master/src/client.c#L4330) |
| `STAT_SHELLS`/`NAILS`/`ROCKETS`/`CELLS` | `100 + (vertical_velocity-derived bytes)` | Pre-match speed-meter: vertical velocity packed across ammo counters | [`ktx/src/client.c:4331-4334`](https://github.com/QW-Group/ktx/blob/master/src/client.c#L4331) |

**Why you only see them after match end**: during gameplay the sentinel value is transient — KTX writes it for one server frame and overwrites it with the real value the next frame, so a 1 s aggregation window almost always picks the real value. After match end, KTX stops overwriting (`match_over` is true), and whatever sentinel a player had on their last damage hit gets frozen into every subsequent sample.

**Recommended filter**:

```go
case mvd.StatHealth:
    if e.Value <= 250 { state.health = e.Value }   // ignore 1000+dmg sentinel
case mvd.StatArmor:
    if e.Value <= 200 { state.armor = e.Value }    // ignore velocity sentinel
```

Combined with stopping all sampling at `svc_intermission`, this completely eliminates post-match `health=1000 / armor=1000` noise on the timeline.

### Common Parsing Issues

| Issue | Solution |
|-------|----------|
| Demo stops parsing early | An unknown `svc_*` command (often an unknown TE type or FTE entity flag) hit `default → io.EOF`. Acceptable; the next payload starts fresh. Investigate which command if it happens early and often. |
| Invalid player numbers (>31) | Skip messages with out-of-range player numbers |
| Garbled player names / mismatched display names | Apply `Q_normalizetext` consistently. If userinfo `name` differs from chat/obituary names, see [The userinfo `name` is not always the displayed netname](#the-userinfo-name-is-not-always-the-displayed-netname). |
| Missing frags | Check PRINT_MEDIUM (level 1) for obituaries, not PRINT_HIGH |
| One player's per-player health/armor stack is empty in the timeline view | Slot↔demoinfo bridge failed — usually the userinfo `name` differs from the demoinfo display name and your join is by string equality. Use `(team, frags)` matching, fall back to userinfo name on failure. |
| One player's frag deltas attributed to another player | Naïve `frags → demoInfoPlayer` mapping collided on a tied frag count. Key by `(team, frags)` and require strict uniqueness; do not commit ambiguous mappings. |
| Sudden 9-digit health/armor reading on one player for several seconds | Parser drifted inside a `svc_temp_entity` run (almost always TE_BLOOD or TE_GUNSHOT — both carry an extra `byte count`) and misread later bytes as `svc_updatestatlong`. Fix `skipTempEntity`, do **not** clamp the value — clamping just hides the upstream drift. |
| Only some players show up in the timeline; missing players have full stats in the demoinfo JSON and obituaries but no per-player health/armor/position | Either (a) a `svc_damage` (cmd 19) handler is missing — the QW-only command isn't in NetQuake refs and an unhandled cmd 19 abandons the rest of every payload it lands in, including bursts of `svc_updateuserinfo`; or (b) `svc_updateping` (cmd 36) is being skipped as 2 bytes instead of 3 (1 byte player + 2 byte ping `short`), causing 1-byte drift per ping update. Real-world worked example: hub demo 121329 — only 3 of 8 players appeared until both bugs were fixed. |
| Hundreds or thousands of `unknown svc_*` warnings scattered across one demo, with no obvious starting point | Almost certainly drift compounding from a small per-occurrence skip-length error somewhere upstream. Don't chase the warnings individually — instead bisect by skipping recent additions and re-running the diagnostic on a known-good demo. The likely culprits are the variable-length offenders (`svc_temp_entity`, `svc_packetentities`, `svc_playerinfo`) or off-by-one fixed lengths (`svc_updateping`, `svc_intermission`, `svc_spawnstaticsound`, `svc_spawnbaseline`'s entity prefix). |
| Player health stuck at `1000`/`1042`/`1078`/etc. for several seconds at end of match | KTX writes `health = 1000 + damage` and `armorvalue = velocity + 1000` as out-of-band HUD signalling — see [KTX Stat Sentinels](#ktx-stat-sentinels-out-of-band-huge-stat-values). The values get frozen at intermission when KTX stops overwriting them. Filter `health > 250` / `armor > 200` at the stat handler and stop sampling on `svc_intermission`. |
| `unknown_svc: svc_intermission (cmd 30), 9 bytes remaining` warning at the very end of a demo | `skipCommand` is reading 12 bytes for `svc_intermission` (assuming "vec3 origin + vec3 angles" means 6 + 6). The angles are *3 bytes* (1 byte each), not 3 shorts — total is 6 + 3 = 9. |
| Zero duration | Ensure time_delta accumulation is correct (milliseconds) |
| Damage values too high | Cap damage at victim's health (MVD has unbound damage) |

### Player Name Normalization

*Source: ezquake `common.c::Q_normalizetext` (256-byte lookup table), mvdsv ships an identical copy. Both are file-scope and used on console text, log lines, and any path that needs a "plain ASCII" rendition of a Quake-encoded string.*

**Do not** normalize names by simply doing `c & 0x7f` and dropping anything below `0x20`. That throws away `[`, `]`, dots, and bracket-digits that are legitimately part of Quake names, and produces the wrong string in two ways:

1. It collapses distinct names that differ only in low-byte glyphs. E.g. `[bbb]` (encoded as `10 62 62 62 11`) becomes `bbb` instead of `[bbb]`.
2. It silently shortens names so that downstream joins (slot↔demoinfo, frag-event attribution, frontend per-player rows) miss matches that would have worked.

Instead, run every player-facing string (userinfo `name`, userinfo `team`, `svc_print` payload, KTX demoinfo JSON `name` field) through the canonical 256-entry table:

| Byte (and its `b+0x80` "gold" twin) | Mapped to |
|-------------------------------------|-----------|
| `0x00` | `'#'` (we treat NUL as a non-printable; some implementations drop it) |
| `0x05`, `0x0E`, `0x0F`, `0x1C`, `0x2E` | `'.'` |
| `0x0A` | `'\n'` (kept) |
| `0x0D` | `'\r'` (kept) |
| `0x10` | `'['` |
| `0x11` | `']'` |
| `0x12`–`0x1B` | `'0'`–`'9'` (`'0' + (b - 0x12)`) |
| `0x1D` | `'<'` |
| `0x1E` | `'='` |
| `0x1F` | `'>'` |
| `0x20`–`0x7E` | identity (printable ASCII) |
| `0x7F` | `'>'` |
| any other `b < 0x20` | `'#'` |
| `0x80`–`0xFF` | same mapping as `b - 0x80` (high bit = gold color, irrelevant for the plain string) |
| `0x80` (override) | `'('` |
| `0x82` (override) | `')'` |
| `0x8D` (override) | `'<'` |

**Reference implementation** (matches ezquake/mvdsv byte-for-byte):

```go
var qNormalizeTable = func() [256]byte {
    var t [256]byte
    for i := 0; i < 256; i++ { t[i] = '#' }
    for i := 32; i < 127; i++ { t[i] = byte(i) }
    t[5], t[14], t[15], t[28], t[46] = '.', '.', '.', '.', '.'
    t[10], t[13] = '\n', '\r'
    t[16], t[17] = '[', ']'
    for i := 18; i <= 27; i++ { t[i] = byte('0' + (i - 18)) }
    t[29], t[30], t[31] = '(', '=', ')'
    t[127] = '>'
    for i := 0; i < 128; i++ { t[i+128] = t[i] } // gold = white
    t[128], t[129], t[130], t[141] = '(', '=', ')', '<'
    return t
}()

func NormalizeQuakeText(b []byte) string {
    out := make([]byte, 0, len(b))
    for _, c := range b {
        if c == 0 { continue }
        out = append(out, qNormalizeTable[c])
    }
    return string(out)
}
```

**Important — apply this consistently in every place a name surfaces.** A subtle gotcha: KTX writes the demoinfo JSON by escaping non-ASCII bytes as `\u00XX`, so when you decode that JSON in another language each escape comes back as a Unicode codepoint in the range U+0000–U+00FF. Run those codepoints through the *same* table (after asserting they fit in a byte) — otherwise the analyzer will hold one normalized form for chat-side names and a different normalized form for demoinfo-side names of the same player, and any join between them silently drops the player.

**Do not "tidy" trailing punctuation.** Quake names can legitimately end in `.` (e.g. `.N3ophyt3.` after Q_normalizetext folding of `2e ce 15 6f f0 68 f9 74 15 ae`). Trimming a trailing `.` from obituary parser output to "clean up punctuation" splits that player's frags off into a phantom name. Pattern-match weapon suffixes explicitly instead.

**Other things still worth doing after the table pass:**

- **Multiple spaces**: Some names contain runs of consecutive spaces (e.g. `"Hto   (FU)"`). Preserve them when displaying — the player chose them — but be aware downstream HTML rendering may collapse them.
- **Leading/trailing spaces**: Trim only if comparing names across sources where one source already trims.
- **Case sensitivity**: QuakeWorld names are case-sensitive; do not lowercase before comparing.

### Performance Considerations

- Stream parsing is recommended for large files (10MB+)
- Most messages are `dem_all` - optimize for this case
- Payload sizes are typically 4-800 bytes
- A 20-minute demo may contain 200,000+ messages

---

## Example: Reading Player Positions

```go
func parsePlayerInfoMVD(r *BufferReader, floatCoords bool) *PlayerState {
    playerNum := r.ReadByte()
    flags := r.ReadUint16()
    frame := r.ReadByte()

    state := &PlayerState{
        PlayerNum: int(playerNum),
        Frame:     frame,
        IsDead:    flags&DF_DEAD != 0,
        IsGib:     flags&DF_GIB != 0,
    }

    // Read origin components
    for i := 0; i < 3; i++ {
        if flags&(DF_ORIGIN<<i) != 0 {
            if floatCoords {
                state.Origin[i] = r.ReadFloat32()
            } else {
                state.Origin[i] = r.ReadCoord()
            }
        }
    }

    // Read angle components
    for i := 0; i < 3; i++ {
        if flags&(DF_ANGLES<<i) != 0 {
            state.Angles[i] = r.ReadAngle16()
        }
    }

    if flags&DF_MODEL != 0 {
        state.ModelIndex = int(r.ReadByte())
    }
    if flags&DF_SKINNUM != 0 {
        skin := r.ReadByte()
        // Handle extended model index
        if skin&0x80 != 0 && flags&DF_MODEL != 0 {
            state.ModelIndex += 256
            skin &= 0x7F
        }
        state.SkinNum = int(skin)
    }
    if flags&DF_EFFECTS != 0 {
        state.Effects = r.ReadByte()
    }
    if flags&DF_WEAPONFRAME != 0 {
        state.WeaponFrame = r.ReadByte()
    }

    return state
}
```

---

## Constants Reference

### Message Types
```c
#define dem_cmd         0
#define dem_read        1
#define dem_set         2
#define dem_multiple    3
#define dem_single      4
#define dem_stats       5
#define dem_all         6
```

### Protocol Constants
```c
#define PROTOCOL_VERSION        28
#define MAX_CLIENTS             32
#define MAX_MODELS              512  // With extensions: 2048
#define MAX_SOUNDS              512
#define MAX_LIGHTSTYLES         64
#define MAX_CL_STATS            32
#define UPDATE_BACKUP           64
```

---

## Source Code References

### Repositories

| Project | Description | GitHub |
|---------|-------------|--------|
| **KTX** | Server-side mod (stats, hidden messages) | [QW-Group/ktx](https://github.com/QW-Group/ktx) |
| **ezQuake** | Client with MVD recording/playback | [QW-Group/ezquake-source](https://github.com/QW-Group/ezquake-source) |
| **mvdparser** | Reference MVD parsing library | [QW-Group/mvdparser](https://github.com/QW-Group/mvdparser) |

### KTX Source Files

Server-side implementation, stats tracking, hidden message generation:

| File | Description |
|------|-------------|
| [include/g_consts.h#L323-L332](https://github.com/QW-Group/ktx/blob/master/include/g_consts.h#L323-L332) | Hidden message type IDs (`mvdhidden_*`) |
| [include/deathtype.h#L1-L29](https://github.com/QW-Group/ktx/blob/master/include/deathtype.h#L1-L29) | Death type constants (`DT_*`) |
| [src/combat.c#L786-L834](https://github.com/QW-Group/ktx/blob/master/src/combat.c#L786-L834) | Damage tracking, dmgdone hidden message |
| [src/client.c](https://github.com/QW-Group/ktx/blob/master/src/client.c) | `ClientObituary()` - obituary patterns |
| [src/weapons.c](https://github.com/QW-Group/ktx/blob/master/src/weapons.c) | Weapon damage values and mechanics |
| [src/race.c#L158-L166](https://github.com/QW-Group/ktx/blob/master/src/race.c#L158-L166) | `sv_usercmdtrace` usage for usercmd recording |

### ezQuake Source Files

Client/server MVD recording and playback:

| File | Description |
|------|-------------|
| [src/sv_demo.c](https://github.com/QW-Group/ezquake-source/blob/master/src/sv_demo.c) | Server-side MVD recording |
| [src/sv_demo.c#L1867-L1877](https://github.com/QW-Group/ezquake-source/blob/master/src/sv_demo.c#L1867-L1877) | `SV_UserCmdTrace_f` - usercmd trace command |
| [src/sv_demo_misc.c#L851-L873](https://github.com/QW-Group/ezquake-source/blob/master/src/sv_demo_misc.c#L851-L873) | Demoinfo block numbering (block 0 = last) |
| [src/cl_demo.c](https://github.com/QW-Group/ezquake-source/blob/master/src/cl_demo.c) | Client-side demo playback |
| [src/cl_parse.c](https://github.com/QW-Group/ezquake-source/blob/master/src/cl_parse.c) | Network message parsing (`svc_*`) |
| [src/cl_ents.c](https://github.com/QW-Group/ezquake-source/blob/master/src/cl_ents.c) | Entity and player state parsing |
| [src/qwprot/src/protocol.h](https://github.com/QW-Group/ezquake-source/blob/master/src/qwprot/src/protocol.h) | Protocol definitions, `svc_*` constants |

### mvdparser Source Files

Reference C implementation for MVD parsing:

| File | Description |
|------|-------------|
| [src/netmsg_parser.c#L255-L311](https://github.com/QW-Group/mvdparser/blob/master/src/netmsg_parser.c#L255-L311) | `Stat_CalculateShotsFired()` - ammo-based shot tracking |
| [src/netmsg_parser.c#L1131-L1134](https://github.com/QW-Group/mvdparser/blob/master/src/netmsg_parser.c#L1131-L1134) | `svc_muzzleflash` parsing |
| [src/qw_protocol.h](https://github.com/QW-Group/mvdparser/blob/master/src/qw_protocol.h) | Protocol constants, `svc_*` definitions |
| [src/qw_protocol.h#L404](https://github.com/QW-Group/mvdparser/blob/master/src/qw_protocol.h#L404) | `weapon_shots` array definition |

**Note**: mvdparser's shot tracking has no respawn filtering, causing ~2x overcounting.

### Key Implementation Details

**Damage tracking** ([combat.c#L786-L834](https://github.com/QW-Group/ktx/blob/master/src/combat.c#L786-L834)):
```c
// Line 786: Raw damage before health capping
unbound_dmg_dealt = dmg_dealt;

// Line 804: Effective damage capped at victim's health
dmg_dealt += bound(0, virtual_take, targ->s.v.health);

// Lines 830-834: Write to MVD hidden message
WriteShort(MSG_MULTICAST, mvdhidden_dmgdone);
```

**Usercmd recording** ([sv_demo.c#L1867](https://github.com/QW-Group/ezquake-source/blob/master/src/sv_demo.c#L1867)):
```
sv_usercmdtrace <userid> on|off
```
Enables `mvdhidden_usercmd` (0x0001) recording per player. Used primarily for race mode.

**Q_normalizetext** ([common.c#L1695-L1754](https://github.com/QW-Group/ezquake-source/blob/master/src/common.c)): the canonical 256-byte mapping table used by ezquake and mvdsv to fold any Quake-encoded string into plain ASCII. Used for log lines, console text, and SV_Logfile paths. mvdsv ships an identical copy in its own `common.c`. Note that KTX itself does **not** call `Q_normalizetext` on the player `netname` it writes into the demoinfo JSON — it just JSON-escapes the raw bytes via `\u00XX`, so the consumer is responsible for decoding the escapes back to bytes and running them through the same table.

**KTX `getname()`** ([g_utils.c#L1207-L1234](https://github.com/QW-Group/ktx/blob/master/src/g_utils.c#L1207-L1234)): copies `ent->s.v.netname` verbatim — no mapping, no stripping. This is the function whose output ends up in the demoinfo `players[i].name` JSON field, which is why that field can differ from `userinfo["name"]` when KTX's auth system has overridden the userinfo name.

**TE_BLOOD / TE_GUNSHOT count byte** (ezquake [`cl_tent.c::CL_ParseTEnt`](https://github.com/QW-Group/ezquake-source/blob/master/src/cl_tent.c)): these two TE types (and only these two in stock QW) carry a `byte count` field before their 3 coords. Worth verifying against the reference implementation if you ever extend `skipTempEntity` to handle a new TE type from an FTE/QuakeForge extension.

---

## Version History

| Version | Changes |
|---------|---------|
| Original (MVDSV) | Basic MVD format - dem_* message types, svc_* commands |
| FTE Extensions | Model index > 255, entity count > 512, float coordinates |
| MVD_PEXT1 (KTX) | Hidden messages (damage tracking, antilag, demoinfo, commentary) |

### Document Revisions

- Added [`svc_temp_entity`](#svc_temp_entity-23) section with the full per-type size table after a real-world demo (`broken.mvd.gz`) revealed that omitting the `byte count` for `TE_BLOOD` (12) and `TE_GUNSHOT` (2), and over-sizing beam types as 16 bytes, both cause silent parser drift that surfaces as plausible-looking corrupt stats.
- Replaced the simplistic "char & 0x7f" name normalization advice with the canonical [`Q_normalizetext`](#player-name-normalization) table from ezquake/mvdsv `common.c`. The old advice silently dropped legitimate name characters (`[`, `]`, dots, bracket-digits) and produced different normalized strings on different sides of cross-source joins.
- Added [The userinfo `name` is not always the displayed netname](#the-userinfo-name-is-not-always-the-displayed-netname) covering the KTX auth-override case and the recommended `(team, frags)` slot↔demoinfo bridging strategy with strict-uniqueness check.
- Documented [`svc_setinfo`](#svc_setinfo-51) as a real command consumers must handle (single-key userinfo updates flow through here, not `svc_updateuserinfo`).
- Strengthened the parsing-pitfalls and robustness tips with a "bail, don't guess" rule for unknown commands and TE types, and a cross-reference between the four variable-length offenders (`svc_temp_entity`, `svc_sound`, `svc_playerinfo`, `svc_packetentities`).
- Added [`svc_damage`](#svc_damage-19) (cmd 19, QW-only, 8/14 bytes), missing from earlier drafts. An unhandled `svc_damage` was responsible for thousands of payload aborts per demo and was the largest single cause of dropped `svc_updateuserinfo` / `svc_updatefrags` data in real KTX MVDs. Reference: hub demo 121329 — analyzer rendered only 3 of 8 players in the timeline because the initial userinfo packet kept getting truncated mid-stream.
- Corrected [`svc_spawnbaseline`](#svc_spawnbaseline-22--entity-baseline) total size: it has a **2-byte entity-number prefix** (15/21 bytes total), unlike `svc_spawnstatic` (13/19) which the doc previously claimed shared the same format. Source: ezquake `cl_parse.c case svc_spawnbaseline: i = MSG_ReadShort(); CL_ParseBaseline(...)`.
- Corrected [`svc_intermission`](#svc_intermission-30) size: it's **9 bytes** (3 short coords + 3 angle bytes), not 12. Promoted it from a pure skip target to an emitted event because it's the only reliable end-of-match signal in some demos (KTX's `"the match is over"` bprint is missing from a meaningful fraction of real demos).
- Corrected [`svc_updateping`](#svc_updateping-36): the ping is a **short** (2 bytes), not a byte. Total payload after the cmd byte is 3 bytes, not 2. A 1-byte drift here, multiplied across the per-second ping broadcasts of an 8-player match, was the actual root cause of the 121329 player-loss bug.
- Added [`svc_spawnstaticsound`](#svc_spawnstaticsound-29) (cmd 29) with the correct 9/15 byte size. Earlier versions of the analyzer were skipping 11/17 bytes here — drift in setup-packet ambient-sound entries silently corrupted the entity baseline burst that follows.
- Added [KTX stat sentinels](#ktx-stat-sentinels-out-of-band-huge-stat-values) section covering KTX's reuse of `STAT_HEALTH`/`STAT_ARMOR`/`STAT_FRAGS`/ammo as out-of-band HUD signalling channels (`health = 1000 + damage`, `armorvalue = velocity + 1000`). These are invisible during gameplay because they get overwritten next frame, but freeze into post-intermission samples — the canonical fix is filter-at-stat-handler combined with stop-sampling-at-`svc_intermission`.
- Added `mvdhidden_demo_start_timestamp_ms` (0x000B) to the [Hidden Message Types](#hidden-message-types) table. Newer mvdsv builds emit this ULEB128 varint-encoded unix-millisecond timestamp at demo start for stream/voice synchronisation; older parsers will report it as `unknown_hidden 0x000b`.
- Added [`svc_centerprint`](#svc_centerprint-26) section documenting KTX's reuse of the centerprint stream as a structured match-settings table during the 10-second countdown. Values are encoded with `redtext()` and `dig3()` so they need [`Q_normalizetext`](#player-name-normalization) before they're readable.
- Added [Demo Metadata Sources](#demo-metadata-sources) — a top-level reference for *all four* protocol-level metadata sources (`fullserverinfo` stufftext, `svc_serverinfo` updates, the countdown centerprint, and the `mvdhidden_demoinfo` JSON) with the complete set of standard mvdsv `CVAR_SERVERINFO` keys, KTX-added serverinfo keys, the spawn-algorithm short→long-name lookup table, and the merge-and-precedence rules a tournament viewer needs to combine them.
- Added [Item tracking via entity state](#item-tracking-via-entity-state) — the protocol-level way to observe pickups and respawns by diffing `modelindex` transitions in `svc_packetentities` / `svc_deltapacketentities`. Replaces any reliance on KTX's `//ktx took` prints for item-up/down status. Includes the Quake 1 item model-path table used for entity classification, the MVD-ignores-PVS invariant that lets "entity absent from packet" mean "picked up," and the same-tick insta-regrab invisibility that KTX prints uniquely catch.
- Expanded the [`//ktx` directives](#svc_stufftext-9) coverage to document `//ktx took`, `//ktx timer`, `//ktx drop`, and `//wps` with KTX source-code line references, and documented when the entity-state stream vs. KTX prints is the right signal to use.
- Added `//ktx bp <backpack_ent> <player_ent>` (`ktx/src/items.c:2471`) to the [`//ktx` directives](#svc_stufftext-9) table — the symmetric pickup companion of `//ktx drop`, fires only for RL/LG packs. Rewrote the pickup-attribution guidance: on KTX servers `//ktx took` + `//ktx bp` are the authoritative pickup-attribution signals (each pair pins the touch to a concrete player edict), superseding the nearest-origin heuristic required by entity-state alone. Non-KTX servers fall back to entity-state or to per-player `svc_updatestat` deltas. Also added `//ktx expire` and documented the demo-ignored `//ktx di` damage-info hint for completeness.
- Added [Per-client pickup prints — and the PRINT_LOW filter](#per-client-pickup-prints--and-the-print_low-filter) to the `svc_print` section. KTX's `G_sprint(PRINT_LOW)` pickup messages ("You got the Red Armor", "You receive 25 health", "You get " backpack opener) carry pickup-attribution via the `dem_single` target slot and would cover categories `//ktx took` skips (ammo boxes, H15/H25), **but** `SV_ClientPrintf` filters by the picking client's `messagelevel` cvar before the MVD write — competitive players widely set `msg 2`, which strips PRINT_LOW from the recording entirely. Coverage is per-player and per-demo; `//ktx took` / `//ktx bp` are the only signals that bypass the filter.
- Added [Derived events — death and spawn](#derived-events--death-and-spawn) — why every `StatHealth` crossing of zero should be surfaced as its own event at the parser layer rather than inferred by per-sample comparison, and the instant-respawn case that motivates it.
- Added [Practical gap — non-RL/LG backpack pickups on competitive demos](#svc_stufftext-9) under the `//ktx` directives, combining the two pre-existing limits (`//ktx bp` is RL/LG-only, and `BackpackPickupPrintEvent` is stripped by `SV_ClientPrintf` when `messagelevel >= 1`) into the explicit conclusion that **non-RL/LG backpack pickups have no authoritative wire signal on a typical 4on4 / duel MVD**. Notes the residual indirect signal (STAT_ITEMS bit + STAT_SHELLS/NAILS/ROCKETS/CELLS deltas via `svc_updatestat`) that a future heuristic analyzer could correlate with nearby `BackpackDropHintEvent`s.

### MVD_PEXT1 Hidden Message History

The hidden message system (`MVD_PEXT1_HIDDEN_MESSAGES`, bit 5 = 0x20) was added by the KTX mod to embed metadata not visible to players during playback:

- **0x0000 - antilag_position**: Antilag position data for hit detection replay
- **0x0001 - usercmd**: Player input commands
- **0x0003 - demoinfo**: Embedded JSON metadata (match info, player stats)
- **0x0007 - dmgdone**: Damage events with attacker, victim, weapon, and amount
- **0x000B - demo_start_timestamp_ms** *(2026, qwprot PR #17)*: ULEB128 varint-encoded unix-millisecond timestamp captured at demo start, for synchronising voice recordings / stream overlays with the demo timeline.

### Damage Tracking Evolution

Early MVD analysis relied on parsing obituary messages (`svc_print` at `PRINT_MEDIUM`). This provided:
- Kill/death counts
- Weapon used (from obituary text patterns)
- Team kills and suicides

The `mvdhidden_dmgdone` (0x0007) message added by KTX provides:
- Precise damage amounts per hit
- Splash vs direct hit distinction
- Continuous damage tracking (not just kills)
- Foundation for accuracy calculation

**Important caveat**: MVD damage values are **unbound** (raw damage before health capping). To match KTX match report stats, damage must be capped at victim's current health.

---

## License

This documentation is based on analysis of the ezQuake source code, which is licensed under the GNU General Public License v2.
