#!/usr/bin/env bash
# fetch-bsps.sh — download the curated set of Quake 1 BSP files used by
# the locvis visibility filter. Populates the directory passed as $1
# (default ./bsps). Idempotent: existing files with a matching SHA-256
# are kept; mismatches are re-fetched.
#
# These BSPs are NOT committed to the repository. They are downloaded
# from two public mirrors:
#
#   - id-stock and registered maps (dm2, dm3, e1m2) from
#     https://github.com/quakeworld/id-maps-gpl (gzipped, GPL-licensed).
#   - Community competitive maps from
#     https://maps.quakeworld.nu/core/<name>.bsp (the canonical QW maps
#     mirror; /core/ holds the bsps shipped in the nquake client
#     distribution).
#
# Local filenames use the LOC-CORPUS form (loc.NormalizeMapName) so the
# locvis loader finds them by the same name `loc.LoadForMap` resolves
# to. Notably "phantombase" → saved as "phantoma" because of the alias
# in mvd-analytics/loc/loader.go.
#
# The script HARD FAILS on any download or sha mismatch. The Netlify
# build chains `make bsps && make build` and any missing BSP should
# fail the deploy rather than silently degrade to V1 in production.
#
# Format of each entry: "<localname> <url> <sha256-or-empty>".
# When sha256 is empty the script downloads without integrity checking
# and prints the observed sha so it can be pinned next iteration.

set -euo pipefail

BSP_DIR="${1:-bsps}"
mkdir -p "$BSP_DIR"

ENTRIES=(
  # localname    url                                                                              sha256
  "dm2          https://github.com/quakeworld/id-maps-gpl/raw/refs/heads/main/dm2.bsp.gz         10af928ff914ea3ab991dcee0736b2e89c88d88cd9bdfd7b1c64e5b341974c84"
  "dm3          https://github.com/quakeworld/id-maps-gpl/raw/refs/heads/main/dm3.bsp.gz         e6df9e9fd078b6d02aaa6c0f1ba40428111cb33da66dcf00234ba9ae2500a478"
  "dm6          https://github.com/quakeworld/id-maps-gpl/raw/refs/heads/main/dm6.bsp.gz         a092b170f37965ad29560de4877adc4cd35049ce6459a9fd4e847d2544ef94f2"
  "e1m2         https://github.com/quakeworld/id-maps-gpl/raw/refs/heads/main/e1m2.bsp.gz         62d270e89514e6e492fac0a8c5dea583ccabd6515a48919d750b08dd94fcb23a"
  "dm4          https://github.com/quakeworld/id-maps-gpl/raw/refs/heads/main/dm4.bsp.gz         a9f0b90d85359182c6223fff2f3bec59d7ecf528896cc727ee07781fd4318b07"
  "aerowalk     https://maps.quakeworld.nu/core/aerowalk.bsp                                      6c297aaa5ccb8f10f6f7ee4991ba6663f887414e31e6eac8358510e14e4ec98b"
  "schloss      https://maps.quakeworld.nu/core/schloss.bsp                                       947d6a01e293d27f387080011fea0bfecda55c574cb664494c7dd21af71eb2dd"
  "phantoma     https://maps.quakeworld.nu/core/phantombase.bsp                                   14d743eb3bade9999dfddbdbf84b0859f2dc85e1294acaae041ae3c71953494e"
  "cmt4         https://maps.quakeworld.nu/core/cmt4.bsp                                          ce25f8cff54b112aea2d50455841e8c54c0b72fcfdd57f317f9ad0e237c83e3c"
  "obsidian     https://maps.quakeworld.nu/core/obsidian.bsp                                      f1183d583689d28a469326abdd63790369d3e2ff612f3f740b460a4351b561e8"
  "qobblestone  https://maps.quakeworld.nu/core/qobblestone.bsp                                   ade0a3dc26a43d1a7638f788cf5e025bc62bad9bbab4a2bd03ba45e286d005ee"
  "rocka        https://maps.quakeworld.nu/core/rocka.bsp                                         f66f77ad5767c47c27f677e500bba9a28df71ab8118577498c050d8f2e7295ef"
  "steam        https://maps.quakeworld.nu/core/steam.bsp                                         39bd0203cbff42bebfd2f5577333ca78f787023f05f8a52ba923de6b4163a11f"
  "anwalked     https://maps.quakeworld.nu/core/anwalked.bsp                                      0c808fe481290b543e293bc716bea4bff0e71e07f941f49e415ad882745ac68c"
  "stronghold   https://maps.quakeworld.nu/core/stronghold.bsp                                    640443115de7be4f99f88b8f25b3f91c6e54bd624154ac072802e63957dcb4e9"
  "defer        https://maps.quakeworld.nu/core/defer.bsp                                         c01d9ec4b634ac07f8a8beabab95640eee2113aa64a3637d182357bf1e0a9416"
  "skull        https://maps.quakeworld.nu/core/skull.bsp                                         4d7af365db557331776d52d4b9d57068be2db73a440a95f85fd0679f826fe0e7"
  "bravado      https://maps.quakeworld.nu/core/bravado.bsp                                       5f382d591ef5080322c4f8a2da07b22795c66104a7b9a2baccd4e72789d0789d"
)

check_sha() {
  local file="$1" expected="$2"
  [[ -z "$expected" ]] && return 0
  local actual
  actual="$(sha256sum "$file" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]]
}

download_one() {
  local localname="$1" url="$2" expected_sha="$3"
  local out="$BSP_DIR/$localname.bsp"

  if [[ -f "$out" ]] && [[ -n "$expected_sha" ]] && check_sha "$out" "$expected_sha"; then
    printf "  ok    %-12s %s\n" "$localname" "(cached, sha verified)"
    return 0
  fi

  local tmp
  tmp="$(mktemp)"
  if ! curl -fsSL --retry 3 --retry-delay 2 -o "$tmp" "$url"; then
    printf "  FAIL  %-12s download failed: %s\n" "$localname" "$url"
    rm -f "$tmp"
    return 1
  fi

  if [[ "$url" == *.gz ]]; then
    if ! gunzip -c "$tmp" > "$out"; then
      printf "  FAIL  %-12s gunzip failed\n" "$localname"
      rm -f "$tmp" "$out"
      return 1
    fi
    rm -f "$tmp"
  else
    mv "$tmp" "$out"
  fi

  if [[ -n "$expected_sha" ]]; then
    if check_sha "$out" "$expected_sha"; then
      printf "  ok    %-12s (verified)\n" "$localname"
    else
      local actual
      actual="$(sha256sum "$out" | awk '{print $1}')"
      printf "  FAIL  %-12s sha mismatch: got %s want %s\n" "$localname" "$actual" "$expected_sha"
      rm -f "$out"
      return 1
    fi
  else
    local actual
    actual="$(sha256sum "$out" | awk '{print $1}')"
    printf "  ok    %-12s (downloaded, no sha pin — record %s)\n" "$localname" "$actual"
  fi
}

echo "Fetching BSPs into $BSP_DIR/ ..."
for entry in "${ENTRIES[@]}"; do
  # shellcheck disable=SC2086
  set -- $entry
  localname="$1"; url="$2"; sha="${3:-}"
  download_one "$localname" "$url" "$sha"
done

echo "All BSPs ready in $BSP_DIR/"
