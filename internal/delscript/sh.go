package delscript

import "strings"

// shDataTag ends the here-document that holds the data. A record never equals it: records start with a letter and
// a bar, and the tag has neither.
const shDataTag = "NASA_DELSCRIPT_DATA_END"

// renderSh writes the POSIX shell script. The data sits in a quoted here-document, so no character in it is
// special; the code reads it back with read -r and quotes every use of a path.
func renderSh(t *messages, data []string) string {
	q := shQuote
	var b strings.Builder
	w := func(lines ...string) {
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	w(
		"#!/bin/sh",
		"# NAS Storage Analyzer: delete script. It deletes exactly the files and folders in the data block below, after you confirm.",
		"# The data block is plain data: nothing in it is ever run as a command. Run this file with: sh <this file>",
		"data() {",
		"cat <<'"+shDataTag+"'",
	)
	for _, l := range data {
		w(l)
	}
	w(
		shDataTag,
		"}",
		"N=$(data | sed -n 's/^N|//p')",
		"T=$(data | sed -n 's/^T|//p')",
		"ROOT=$(data | sed -n 's/^R|//p')",
		"ROOT=${ROOT%/}",
		"data | sed -n 's/^I|//p'",
		"COUNT=$(data | grep -c '^[FD]|')",
		`if [ "$COUNT" != "$N" ]; then`,
		"  printf '%s%s%s%s%s\\n' "+q(t.incomplete[0])+` "$N" `+q(t.incomplete[1])+` "$COUNT" `+q(t.incomplete[2]),
		"  exit 2",
		"fi",
		"if stat -c '%s %Y' / >/dev/null 2>&1; then",
		"  STATFMT=gnu",
		"elif stat -f '%z %m' / >/dev/null 2>&1; then",
		"  STATFMT=bsd",
		"else",
		"  printf '%s\\n' "+q(t.noStat),
		"  exit 2",
		"fi",
		"fstat() {",
		`  if [ "$STATFMT" = gnu ]; then stat -c '%s %Y' "$1"; else stat -f '%z %m' "$1"; fi`,
		"}",
		"if [ ! -d \"${ROOT:-/}\" ]; then",
		"  printf '%s\\n' "+q(t.notOpen),
		"  exit 2",
		"fi",
		"# A record is cut apart with parameter expansion, not with read and IFS: read would take a trailing bar off the path.",
		"# F is a file (size, time, path) and D a folder to delete with everything in it (files, folders, path): both end in the path.",
		"# This is a function and not a command substitution: bash 3.2 (the sh of macOS) ends a $( ) at the first ) of a case pattern.",
		"found() {",
		"  c=0",
		"  while IFS= read -r L; do",
		`    case $L in F\|*|D\|*) ;; *) continue ;; esac`,
		"    L=${L#?|}; L=${L#*|}; P=${L#*|}",
		`    if [ -e "$ROOT$P" ] || [ -h "$ROOT$P" ]; then c=$((c + 1)); fi`,
		"  done",
		`  printf '%s' "$c"`,
		"}",
		"FOUND=$(data | found)",
		"printf '%s%s%s%s%s\\n' "+q(t.found[0])+` "$FOUND" `+q(t.found[1])+` "$N" `+q(t.found[2]),
		`if [ "$FOUND" = 0 ]; then`,
		"  printf '%s\\n' "+q(t.wrongRoot),
		"  exit 2",
		"fi",
		"printf '%s%s%s' "+q(t.confirm[0])+` "$T" `+q(t.confirm[1]),
		"ANS=",
		"IFS= read -r ANS || ANS=",
		`if [ "$ANS" != "DELETE $T" ]; then`,
		"  printf '\\n%s\\n' "+q(t.cancelled),
		"  exit 2",
		"fi",
		"data | {",
		"  deleted=0; kept=0; gone=0; failed=0",
		"  while IFS= read -r L; do",
		`    case $L in F\|*) K=F ;; D\|*) K=D ;; *) continue ;; esac`,
		`    L=${L#?|}; S=${L%%|*}; L=${L#*|}; M=${L%%|*}; P=${L#*|}`,
		`    F=$ROOT$P`,
		"    # A folder goes with everything in it, but only when nothing but plain files and folders is in it now and not more of them",
		"    # than at the scan (S files and M folders): what was added since was never seen. Each outcome counts what the scan knew of it.",
		`    if [ "$K" = D ]; then`,
		`      W=$((S + M + 1))`,
		`      if [ -h "$F" ]; then`,
		"        kept=$((kept + W)); printf '%s: %s\\n' "+q(t.notFolder)+` "$F"; continue`,
		"      fi",
		`      if [ ! -e "$F" ]; then`,
		"        gone=$((gone + W)); printf '%s: %s\\n' "+q(t.gone)+` "$F"; continue`,
		"      fi",
		`      if [ ! -d "$F" ]; then`,
		"        kept=$((kept + W)); printf '%s: %s\\n' "+q(t.notFolder)+` "$F"; continue`,
		"      fi",
		`      ODD=$(find "$F" ! -type f ! -type d -print 2>/dev/null | head -n 1)`,
		`      if [ -n "$ODD" ]; then`,
		"        kept=$((kept + W)); printf '%s: %s\\n' "+q(t.hasLinks)+` "$F"; continue`,
		"      fi",
		`      NF=$(find "$F" -type f 2>/dev/null | wc -l); NF=$((NF + 0))`,
		`      ND=$(find "$F" -type d 2>/dev/null | wc -l); ND=$((ND - 1))`,
		`      if [ "$NF" -gt "$S" ] || [ "$ND" -gt "$M" ]; then`,
		"        kept=$((kept + W)); printf '%s: %s\\n' "+q(t.grown)+` "$F"; continue`,
		"      fi",
		`      rm -rf -- "$F" 2>/dev/null`,
		`      if [ ! -e "$F" ] && [ ! -h "$F" ]; then`,
		"        deleted=$((deleted + W)); printf '%s: %s\\n' "+q(t.deleted)+` "$F"`,
		"      else",
		"        failed=$((failed + W)); printf '%s: %s\\n' "+q(t.failed)+` "$F"`,
		"      fi",
		"      continue",
		"    fi",
		`    if [ -h "$F" ]; then`,
		"      kept=$((kept + 1)); printf '%s: %s\\n' "+q(t.notRegular)+` "$F"; continue`,
		"    fi",
		`    if [ ! -e "$F" ]; then`,
		"      gone=$((gone + 1)); printf '%s: %s\\n' "+q(t.gone)+` "$F"; continue`,
		"    fi",
		`    if [ ! -f "$F" ]; then`,
		"      kept=$((kept + 1)); printf '%s: %s\\n' "+q(t.notRegular)+` "$F"; continue`,
		"    fi",
		`    set -- $(fstat "$F")`,
		`    if [ "$1" != "$S" ]; then`,
		"      kept=$((kept + 1)); printf '%s: %s\\n' "+q(t.changed)+` "$F"; continue`,
		"    fi",
		`    if [ "$M" != 0 ]; then`,
		`      D=$(($2 - M)); if [ "$D" -lt 0 ]; then D=$((0 - D)); fi`,
		`      if [ "$D" -gt 2 ]; then`,
		"        kept=$((kept + 1)); printf '%s: %s\\n' "+q(t.changed)+` "$F"; continue`,
		"      fi",
		"    fi",
		`    rm -f -- "$F" 2>/dev/null`,
		`    if [ ! -e "$F" ] && [ ! -h "$F" ]; then`,
		"      deleted=$((deleted + 1)); printf '%s: %s\\n' "+q(t.deleted)+` "$F"`,
		"    else",
		"      failed=$((failed + 1)); printf '%s: %s\\n' "+q(t.failed)+` "$F"`,
		"    fi",
		"  done",
		"  printf '%s%s%s%s%s%s%s%s%s\\n' "+q(t.done[0])+` "$deleted" `+q(t.done[1])+` "$kept" `+q(t.done[2])+` "$gone" `+q(t.done[3])+` "$failed" `+q(t.done[4]),
		`  [ "$failed" = 0 ]`,
		"}",
		"exit $?",
	)
	return b.String()
}

// shQuote single-quotes s for sh. The messages never contain a single quote; the replacement is for safety.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
