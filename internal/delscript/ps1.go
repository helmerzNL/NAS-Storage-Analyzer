package delscript

import "strings"

// psBOM makes Windows PowerShell 5.1 read the file as UTF-8. Without it 5.1 reads a script as the ANSI code page,
// and every non-ASCII character in a name would turn into something else.
const psBOM = "\xef\xbb\xbf"

// renderPowerShell writes the PowerShell script, compatible with Windows PowerShell 5.1 (no ??, no ternary, no &&).
// The data is a single-quoted here-string: nothing in it is expanded or escaped, and its end is a line that starts
// with '@, which a record never does (records start with a letter and a bar).
func renderPowerShell(t *messages, data []string) string {
	q := psQuote
	var b strings.Builder
	w := func(lines ...string) {
		for _, l := range lines {
			b.WriteString(l)
			b.WriteString("\r\n")
		}
	}
	b.WriteString(psBOM)
	w(
		"# NAS Storage Analyzer: delete script. It deletes exactly the files and folders in the data block below, after you confirm.",
		"# The data block is plain data: nothing in it is ever run as a command.",
		"# Run it with: powershell -NoProfile -ExecutionPolicy Bypass -File <this file>",
		"Set-StrictMode -Version 2.0",
		"$ErrorActionPreference = 'Stop'",
		"$data = @'",
	)
	for _, l := range data {
		w(l)
	}
	w(
		"'@",
		"$n = 0",
		"$t = 0",
		"$root = ''",
		"$info = @()",
		"$files = @()",
		`foreach ($line in ($data -split "\r?\n")) {`,
		"    if ($line.Length -lt 2) { continue }",
		"    $kind = $line.Substring(0, 1)",
		"    if ($kind -eq 'N') { $n = [int]$line.Substring(2) }",
		"    elseif ($kind -eq 'T') { $t = [int]$line.Substring(2) }",
		`    elseif ($kind -eq 'R') { $root = $line.Substring(2).TrimEnd('\') }`,
		"    elseif ($kind -eq 'I') { $info += $line.Substring(2) }",
		"    elseif ($kind -eq 'F' -or $kind -eq 'D') { $files += ,($line.Split([char[]]'|', 4)) }",
		"}",
		"if ($files.Count -ne $n) {",
		"    Write-Host ("+q(t.incomplete[0])+" + $n + "+q(t.incomplete[1])+" + $files.Count + "+q(t.incomplete[2])+")",
		"    exit 2",
		"}",
		"foreach ($l in $info) { Write-Host $l }",
		"if (-not [System.IO.Directory]::Exists($root + '\\')) {",
		"    Write-Host "+q(t.notOpen),
		"    exit 2",
		"}",
		"$found = 0",
		"foreach ($f in $files) {",
		"    if ($f[0] -eq 'D') { if ([System.IO.Directory]::Exists($root + $f[3])) { $found++ } }",
		"    elseif ([System.IO.File]::Exists($root + $f[3])) { $found++ }",
		"}",
		"Write-Host ("+q(t.found[0])+" + $found + "+q(t.found[1])+" + $n + "+q(t.found[2])+")",
		"if ($found -eq 0) {",
		"    Write-Host "+q(t.wrongRoot),
		"    exit 2",
		"}",
		"$answer = Read-Host ("+q(t.confirm[0])+" + $t + "+q(strings.TrimRight(t.confirm[1], ": "))+")",
		"if ($answer -cne ('DELETE ' + $t)) {",
		"    Write-Host "+q(t.cancelled),
		"    exit 2",
		"}",
		"$epoch = New-Object DateTime 1970, 1, 1, 0, 0, 0, ([DateTimeKind]::Utc)",
		"$deleted = 0; $kept = 0; $gone = 0; $failed = 0",
		"foreach ($f in $files) {",
		"    if ($f[0] -eq 'D') {",
		"        # A folder goes with everything in it, but only when nothing but plain files and folders is in it now and not more of them",
		"        # than at the scan: what was added since was never seen. A link is never followed: it makes the folder stay.",
		"        # Each outcome counts what the scan knew of the folder, so the numbers add up to the one that was typed.",
		"        $path = $root + $f[3]",
		"        $w = [int64]$f[1] + [int64]$f[2] + 1",
		"        try {",
		"            $di = New-Object System.IO.DirectoryInfo $path",
		"            if (-not $di.Exists) {",
		"                if ([System.IO.File]::Exists($path)) { $kept += $w; Write-Host ("+q(t.notFolder+": ")+" + $path) }",
		"                else { $gone += $w; Write-Host ("+q(t.gone+": ")+" + $path) }",
		"                continue",
		"            }",
		"            if (($di.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {",
		"                $kept += $w; Write-Host ("+q(t.notFolder+": ")+" + $path); continue",
		"            }",
		"            $nf = 0; $nd = 0; $odd = $false",
		"            $order = New-Object System.Collections.ArrayList",
		"            $stack = New-Object System.Collections.Stack",
		"            $stack.Push($di)",
		"            while ($stack.Count -gt 0 -and -not $odd) {",
		"                $d = $stack.Pop()",
		"                [void]$order.Add($d)",
		"                foreach ($e in $d.GetFileSystemInfos()) {",
		"                    if (($e.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { $odd = $true; break }",
		"                    if (($e.Attributes -band [System.IO.FileAttributes]::Directory) -ne 0) { $nd++; $stack.Push($e) } else { $nf++ }",
		"                }",
		"            }",
		"            if ($odd) { $kept += $w; Write-Host ("+q(t.hasLinks+": ")+" + $path); continue }",
		"            if ($nf -gt [int64]$f[1] -or $nd -gt [int64]$f[2]) { $kept += $w; Write-Host ("+q(t.grown+": ")+" + $path); continue }",
		"            foreach ($d in $order) {",
		"                foreach ($e in $d.GetFileSystemInfos()) {",
		"                    if (($e.Attributes -band [System.IO.FileAttributes]::Directory) -eq 0) {",
		"                        if (($e.Attributes -band [System.IO.FileAttributes]::ReadOnly) -ne 0) { $e.Attributes = [System.IO.FileAttributes]::Normal }",
		"                        $e.Delete()",
		"                    }",
		"                }",
		"            }",
		"            for ($i = $order.Count - 1; $i -ge 0; $i--) {",
		"                $d = $order[$i]",
		"                if (($d.Attributes -band [System.IO.FileAttributes]::ReadOnly) -ne 0) { $d.Attributes = [System.IO.FileAttributes]::Normal }",
		"                $d.Delete()",
		"            }",
		"            $di.Refresh()",
		"            if ($di.Exists) { $failed += $w; Write-Host ("+q(t.failed+": ")+" + $path) }",
		"            else { $deleted += $w; Write-Host ("+q(t.deleted+": ")+" + $path) }",
		"        } catch {",
		"            $failed += $w; Write-Host ("+q(t.failed+": ")+" + $path + ' ' + $_.Exception.Message)",
		"        }",
		"        continue",
		"    }",
		"    $size = [int64]$f[1]",
		"    $mtime = [int64]$f[2]",
		"    $path = $root + $f[3]",
		"    try {",
		"        if ([System.IO.Directory]::Exists($path)) {",
		"            $kept++; Write-Host ("+q(t.notRegular+": ")+" + $path); continue",
		"        }",
		"        $fi = New-Object System.IO.FileInfo $path",
		"        if (-not $fi.Exists) {",
		"            $gone++; Write-Host ("+q(t.gone+": ")+" + $path); continue",
		"        }",
		"        if (($fi.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {",
		"            $kept++; Write-Host ("+q(t.notRegular+": ")+" + $path); continue",
		"        }",
		"        if ($fi.Length -ne $size) {",
		"            $kept++; Write-Host ("+q(t.changed+": ")+" + $path); continue",
		"        }",
		"        if ($mtime -ne 0) {",
		"            $secs = [int64](($fi.LastWriteTimeUtc - $epoch).TotalSeconds)",
		"            if ([Math]::Abs($secs - $mtime) -gt 2) {",
		"                $kept++; Write-Host ("+q(t.changed+": ")+" + $path); continue",
		"            }",
		"        }",
		"        $fi.Delete()",
		"        $fi.Refresh()",
		"        if ($fi.Exists) {",
		"            $failed++; Write-Host ("+q(t.failed+": ")+" + $path)",
		"        } else {",
		"            $deleted++; Write-Host ("+q(t.deleted+": ")+" + $path)",
		"        }",
		"    } catch {",
		"        $failed++; Write-Host ("+q(t.failed+": ")+" + $path + ' ' + $_.Exception.Message)",
		"    }",
		"}",
		"Write-Host ("+q(t.done[0])+" + $deleted + "+q(t.done[1])+" + $kept + "+q(t.done[2])+" + $gone + "+q(t.done[3])+" + $failed + "+q(t.done[4])+")",
		"if ($failed -gt 0) { exit 1 }",
		"exit 0",
	)
	return b.String()
}

// psQuote single-quotes s for PowerShell. PowerShell treats the typographic quotes U+2018 to U+201B like ' as well,
// so they are doubled with it. The messages contain none; this is for safety.
func psQuote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		b.WriteRune(r)
		if r == '\'' || (r >= 0x2018 && r <= 0x201b) {
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
