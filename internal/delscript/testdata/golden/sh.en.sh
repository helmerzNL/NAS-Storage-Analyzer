#!/bin/sh
# NAS Storage Analyzer: delete script. It deletes exactly the files and folders in the data block below, after you confirm.
# The data block is plain data: nothing in it is ever run as a command. Run this file with: sh <this file>
data() {
cat <<'NASA_DELSCRIPT_DATA_END'
N|3
T|3
R|/mnt/user/Media
I|NAS Storage Analyzer: delete script
I|Share:          Media
I|Base path:      /mnt/user/Media
I|Index of scan:  2026-10-31 12:30 UTC (scan 7)
I|Generated:       2026-10-31 13:00 UTC
I|Files to delete: 3
I|Size (upper bound): 1.4 GiB
I|1 of these have other hard links: removing them may free no space.
I|Per folder (largest first):
I|      1        42 B  /Movies/
I|      1     1.4 GiB  /Movies/Action/
I|      1         7 B  /Music/
I|NOT in this script (1), see the reason per file:
I|  /Movies/gone.mkv: no longer in the index (a scan ran since you selected it)
I|--------
F|1500000000|1761900000|/Movies/Action/a b.mkv
F|42|1761900001|/Movies/it's 100%.mkv
F|7|0|/Music/日本語.flac
NASA_DELSCRIPT_DATA_END
}
N=$(data | sed -n 's/^N|//p')
T=$(data | sed -n 's/^T|//p')
ROOT=$(data | sed -n 's/^R|//p')
ROOT=${ROOT%/}
data | sed -n 's/^I|//p'
COUNT=$(data | grep -c '^[FD]|')
if [ "$COUNT" != "$N" ]; then
  printf '%s%s%s%s%s\n' 'This script is incomplete or was changed: expected ' "$N" ' entries, found ' "$COUNT" '. Nothing was changed.'
  exit 2
fi
if stat -c '%s %Y' / >/dev/null 2>&1; then
  STATFMT=gnu
elif stat -f '%z %m' / >/dev/null 2>&1; then
  STATFMT=bsd
else
  printf '%s\n' 'This script needs a stat command that prints the size and time of a file. Nothing was changed.'
  exit 2
fi
fstat() {
  if [ "$STATFMT" = gnu ]; then stat -c '%s %Y' "$1"; else stat -f '%z %m' "$1"; fi
}
if [ ! -d "${ROOT:-/}" ]; then
  printf '%s\n' 'Cannot open the base path. Is it right, and is the share reachable? Nothing was changed.'
  exit 2
fi
# A record is cut apart with parameter expansion, not with read and IFS: read would take a trailing bar off the path.
# F is a file (size, time, path) and D a folder to delete with everything in it (files, folders, path): both end in the path.
# This is a function and not a command substitution: bash 3.2 (the sh of macOS) ends a $( ) at the first ) of a case pattern.
found() {
  c=0
  while IFS= read -r L; do
    case $L in F\|*|D\|*) ;; *) continue ;; esac
    L=${L#?|}; L=${L#*|}; P=${L#*|}
    if [ -e "$ROOT$P" ] || [ -h "$ROOT$P" ]; then c=$((c + 1)); fi
  done
  printf '%s' "$c"
}
FOUND=$(data | found)
printf '%s%s%s%s%s\n' 'Found under the base path: ' "$FOUND" ' of ' "$N" '.'
if [ "$FOUND" = 0 ]; then
  printf '%s\n' 'None of the files were found. Is the base path right? Nothing was changed.'
  exit 2
fi
printf '%s%s%s' 'Type DELETE ' "$T" ' to confirm: '
ANS=
IFS= read -r ANS || ANS=
if [ "$ANS" != "DELETE $T" ]; then
  printf '\n%s\n' 'Cancelled. Nothing was changed.'
  exit 2
fi
data | {
  deleted=0; kept=0; gone=0; failed=0
  while IFS= read -r L; do
    case $L in F\|*) K=F ;; D\|*) K=D ;; *) continue ;; esac
    L=${L#?|}; S=${L%%|*}; L=${L#*|}; M=${L%%|*}; P=${L#*|}
    F=$ROOT$P
    # A folder goes with everything in it, but only when nothing but plain files and folders is in it now and not more of them
    # than at the scan (S files and M folders): what was added since was never seen. Each outcome counts what the scan knew of it.
    if [ "$K" = D ]; then
      W=$((S + M + 1))
      if [ -h "$F" ]; then
        kept=$((kept + W)); printf '%s: %s\n' 'not a plain folder, kept' "$F"; continue
      fi
      if [ ! -e "$F" ]; then
        gone=$((gone + W)); printf '%s: %s\n' 'already gone' "$F"; continue
      fi
      if [ ! -d "$F" ]; then
        kept=$((kept + W)); printf '%s: %s\n' 'not a plain folder, kept' "$F"; continue
      fi
      ODD=$(find "$F" ! -type f ! -type d -print 2>/dev/null | head -n 1)
      if [ -n "$ODD" ]; then
        kept=$((kept + W)); printf '%s: %s\n' 'holds links or special files, kept' "$F"; continue
      fi
      NF=$(find "$F" -type f 2>/dev/null | wc -l); NF=$((NF + 0))
      ND=$(find "$F" -type d 2>/dev/null | wc -l); ND=$((ND - 1))
      if [ "$NF" -gt "$S" ] || [ "$ND" -gt "$M" ]; then
        kept=$((kept + W)); printf '%s: %s\n' 'holds more than at the scan, kept' "$F"; continue
      fi
      rm -rf -- "$F" 2>/dev/null
      if [ ! -e "$F" ] && [ ! -h "$F" ]; then
        deleted=$((deleted + W)); printf '%s: %s\n' 'deleted' "$F"
      else
        failed=$((failed + W)); printf '%s: %s\n' 'FAILED' "$F"
      fi
      continue
    fi
    if [ -h "$F" ]; then
      kept=$((kept + 1)); printf '%s: %s\n' 'not a regular file, kept' "$F"; continue
    fi
    if [ ! -e "$F" ]; then
      gone=$((gone + 1)); printf '%s: %s\n' 'already gone' "$F"; continue
    fi
    if [ ! -f "$F" ]; then
      kept=$((kept + 1)); printf '%s: %s\n' 'not a regular file, kept' "$F"; continue
    fi
    set -- $(fstat "$F")
    if [ "$1" != "$S" ]; then
      kept=$((kept + 1)); printf '%s: %s\n' 'changed since the scan, kept' "$F"; continue
    fi
    if [ "$M" != 0 ]; then
      D=$(($2 - M)); if [ "$D" -lt 0 ]; then D=$((0 - D)); fi
      if [ "$D" -gt 2 ]; then
        kept=$((kept + 1)); printf '%s: %s\n' 'changed since the scan, kept' "$F"; continue
      fi
    fi
    rm -f -- "$F" 2>/dev/null
    if [ ! -e "$F" ] && [ ! -h "$F" ]; then
      deleted=$((deleted + 1)); printf '%s: %s\n' 'deleted' "$F"
    else
      failed=$((failed + 1)); printf '%s: %s\n' 'FAILED' "$F"
    fi
  done
  printf '%s%s%s%s%s%s%s%s%s\n' 'Done: ' "$deleted" ' deleted, ' "$kept" ' kept, ' "$gone" ' already gone, ' "$failed" ' failed.'
  [ "$failed" = 0 ]
}
exit $?
