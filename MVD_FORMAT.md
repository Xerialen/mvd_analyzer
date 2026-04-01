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

QuakeWorld uses a custom character encoding:
- Characters 0-127: Standard ASCII
- Characters 128-255: "Gold" (alternate color) versions of 0-127
- To convert gold text: `char = char - 128` if `char >= 128`

Player names and messages may contain these high-bit characters for colored text.

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

**Conversion**:
```go
func convertQuakeChar(c byte) (displayChar byte, isGold bool) {
    if c >= 128 {
        return c - 128, true  // Gold/brown character
    }
    return c, false  // Normal white character
}
```

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

### Example Obituary Messages from Real Demo

```
[12.8s] "nexus was gibbed by splif's rocket"
[14.3s] "ToT_fix was lead poisoned by paniagua"
[59.9s] "nexus ate 2 loads of rusti FU's buckshot"
[77.1s] "paniagua was straw-cuttered by rghst"
[128.9s] "rusti FU gets a frag for the other team"
[195.6s] "paniagua mows down a teammate"
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
| 0xFFFF | `mvdhidden_extended` | Extended type (read next short) | - |

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

Sets the initial state for an entity. Fixed-size format:

```
Offset  Size     Field
------  ----     -----
0       1        svc_spawnbaseline (22)
1       1        modelindex
2       1        frame
3       1        colormap
4       1        skinnum
5       2 or 4   origin[0] (coord or float if FLOATCOORDS)
+       1        angles[0] (angle byte)
+       2 or 4   origin[1]
+       1        angles[1]
+       2 or 4   origin[2]
+       1        angles[2]
```

**Total size**: 13 bytes (standard coords) or 19 bytes (float coords)

### svc_fte_spawnbaseline2 (66) — FTE Extended Baseline

Uses entity delta format with a 2-byte flag header:

```
0       1        svc_fte_spawnbaseline2 (66)
1       2        flag_word (entity delta flags)
+       variable  entity delta fields (see Entity Delta Format below)
```

### svc_spawnstatic (20) / svc_fte_spawnstatic2 (21)

- `svc_spawnstatic (20)`: Same format as `svc_spawnbaseline` (13 or 19 bytes)
- `svc_fte_spawnstatic2 (21)`: Same format as `svc_fte_spawnbaseline2` (flag word + entity delta)

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
- Potential parser crashes

Each payload has its own independent byte buffer, so misalignment does not propagate across demo messages.

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

### svc_stufftext (9)

```
Offset  Size  Field
------  ----  -----
0       1     svc_stufftext (9)
1       var   command (null-terminated string)
```

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

### svc_updateping (36)

```
Offset  Size  Field
------  ----  -----
0       1     svc_updateping (36)
1       1     player_number
2       2     ping (short, milliseconds)
```

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

### svc_serverinfo (52)

```
Offset  Size  Field
------  ----  -----
0       1     svc_serverinfo (52)
1       var   key (null-terminated string)
?       var   value (null-terminated string)
```

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

        # ... handle other svc_* commands
        else:
            # Unknown command - skip rest of this payload
            break
```

---

## Practical Implementation Notes

### Robustness Tips

1. **Validate player numbers**: Always check `player_num < MAX_CLIENTS (32)` before array access.

2. **Handle unknown commands gracefully**: When encountering an unknown svc_* command, skip the rest of the current payload rather than aborting.

3. **Time tracking**: Accumulate time_delta values to track demo time. A typical 20-minute demo will have cumulative time around 1200 seconds.

4. **Character encoding**: Player names may contain high-bit characters (128-255) for gold/colored text. Subtract 128 to get ASCII equivalent.

5. **Protocol extensions**: Always check for extensions before parsing fields that may have different sizes (e.g., coordinates may be 16-bit or 32-bit float).

### Common Parsing Issues

| Issue | Solution |
|-------|----------|
| Demo stops parsing early | Check for unknown svc_* commands causing early exit |
| Invalid player numbers (>31) | Skip messages with out-of-range player numbers |
| Garbled player names | Handle high-bit character encoding |
| Missing frags | Check PRINT_MEDIUM (level 1) for obituaries, not PRINT_HIGH |
| Zero duration | Ensure time_delta accumulation is correct (milliseconds) |
| Player name mismatches | Normalize spaces and color codes (see below) |
| Damage values too high | Cap damage at victim's health (MVD has unbound damage) |

### Player Name Normalization

Player names in MVD demos may require normalization:

1. **Color codes**: Characters 128-255 are "gold" versions. Strip high bit: `char & 0x7F`
2. **Multiple spaces**: Some names contain multiple consecutive spaces (e.g., `"rusti  FU"`). Normalize to single space if comparing names across sources.
3. **Trailing/leading spaces**: Trim whitespace when comparing.
4. **Case sensitivity**: QuakeWorld names are case-sensitive.

```go
func normalizeName(name string) string {
    // Strip color codes (high-bit characters)
    var result strings.Builder
    for _, c := range name {
        if c >= 128 {
            result.WriteRune(c - 128)
        } else {
            result.WriteRune(c)
        }
    }
    // Collapse multiple spaces
    return strings.Join(strings.Fields(result.String()), " ")
}
```

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

---

## Version History

| Version | Changes |
|---------|---------|
| Original (MVDSV) | Basic MVD format - dem_* message types, svc_* commands |
| FTE Extensions | Model index > 255, entity count > 512, float coordinates |
| MVD_PEXT1 (KTX) | Hidden messages (damage tracking, antilag, demoinfo, commentary) |

### MVD_PEXT1 Hidden Message History

The hidden message system (`MVD_PEXT1_HIDDEN_MESSAGES`, bit 5 = 0x20) was added by the KTX mod to embed metadata not visible to players during playback:

- **0x0000 - antilag_position**: Antilag position data for hit detection replay
- **0x0001 - usercmd**: Player input commands
- **0x0003 - demoinfo**: Embedded JSON metadata (match info, player stats)
- **0x0007 - dmgdone**: Damage events with attacker, victim, weapon, and amount

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
