# NAS Storage Analyzer: delete script. It deletes exactly the files and folders in the data block below, after you confirm.
# The data block is plain data: nothing in it is ever run as a command.
# Run it with: powershell -NoProfile -ExecutionPolicy Bypass -File <this file>
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$data = @'
N|3
T|3
R|\\ZBNAS\Media
I|NAS Storage Analyzer: delete script
I|Share:          Media
I|Base path:      \\ZBNAS\Media
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
F|1500000000|1761900000|\Movies\Action\a b.mkv
F|42|1761900001|\Movies\it's 100%.mkv
F|7|0|\Music\日本語.flac
'@
$n = 0
$t = 0
$root = ''
$info = @()
$files = @()
foreach ($line in ($data -split "\r?\n")) {
    if ($line.Length -lt 2) { continue }
    $kind = $line.Substring(0, 1)
    if ($kind -eq 'N') { $n = [int]$line.Substring(2) }
    elseif ($kind -eq 'T') { $t = [int]$line.Substring(2) }
    elseif ($kind -eq 'R') { $root = $line.Substring(2).TrimEnd('\') }
    elseif ($kind -eq 'I') { $info += $line.Substring(2) }
    elseif ($kind -eq 'F' -or $kind -eq 'D') { $files += ,($line.Split([char[]]'|', 4)) }
}
if ($files.Count -ne $n) {
    Write-Host ('This script is incomplete or was changed: expected ' + $n + ' entries, found ' + $files.Count + '. Nothing was changed.')
    exit 2
}
foreach ($l in $info) { Write-Host $l }
if (-not [System.IO.Directory]::Exists($root + '\')) {
    Write-Host 'Cannot open the base path. Is it right, and is the share reachable? Nothing was changed.'
    exit 2
}
$found = 0
foreach ($f in $files) {
    if ($f[0] -eq 'D') { if ([System.IO.Directory]::Exists($root + $f[3])) { $found++ } }
    elseif ([System.IO.File]::Exists($root + $f[3])) { $found++ }
}
Write-Host ('Found under the base path: ' + $found + ' of ' + $n + '.')
if ($found -eq 0) {
    Write-Host 'None of the files were found. Is the base path right? Nothing was changed.'
    exit 2
}
$answer = Read-Host ('Type DELETE ' + $t + ' to confirm')
if ($answer -cne ('DELETE ' + $t)) {
    Write-Host 'Cancelled. Nothing was changed.'
    exit 2
}
$epoch = New-Object DateTime 1970, 1, 1, 0, 0, 0, ([DateTimeKind]::Utc)
$deleted = 0; $kept = 0; $gone = 0; $failed = 0
foreach ($f in $files) {
    if ($f[0] -eq 'D') {
        # A folder goes with everything in it, but only when nothing but plain files and folders is in it now and not more of them
        # than at the scan: what was added since was never seen. A link is never followed: it makes the folder stay.
        # Each outcome counts what the scan knew of the folder, so the numbers add up to the one that was typed.
        $path = $root + $f[3]
        $w = [int64]$f[1] + [int64]$f[2] + 1
        try {
            $di = New-Object System.IO.DirectoryInfo $path
            if (-not $di.Exists) {
                if ([System.IO.File]::Exists($path)) { $kept += $w; Write-Host ('not a plain folder, kept: ' + $path) }
                else { $gone += $w; Write-Host ('already gone: ' + $path) }
                continue
            }
            if (($di.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                $kept += $w; Write-Host ('not a plain folder, kept: ' + $path); continue
            }
            $nf = 0; $nd = 0; $odd = $false
            $order = New-Object System.Collections.ArrayList
            $stack = New-Object System.Collections.Stack
            $stack.Push($di)
            while ($stack.Count -gt 0 -and -not $odd) {
                $d = $stack.Pop()
                [void]$order.Add($d)
                foreach ($e in $d.GetFileSystemInfos()) {
                    if (($e.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { $odd = $true; break }
                    if (($e.Attributes -band [System.IO.FileAttributes]::Directory) -ne 0) { $nd++; $stack.Push($e) } else { $nf++ }
                }
            }
            if ($odd) { $kept += $w; Write-Host ('holds links or special files, kept: ' + $path); continue }
            if ($nf -gt [int64]$f[1] -or $nd -gt [int64]$f[2]) { $kept += $w; Write-Host ('holds more than at the scan, kept: ' + $path); continue }
            foreach ($d in $order) {
                foreach ($e in $d.GetFileSystemInfos()) {
                    if (($e.Attributes -band [System.IO.FileAttributes]::Directory) -eq 0) {
                        if (($e.Attributes -band [System.IO.FileAttributes]::ReadOnly) -ne 0) { $e.Attributes = [System.IO.FileAttributes]::Normal }
                        $e.Delete()
                    }
                }
            }
            for ($i = $order.Count - 1; $i -ge 0; $i--) {
                $d = $order[$i]
                if (($d.Attributes -band [System.IO.FileAttributes]::ReadOnly) -ne 0) { $d.Attributes = [System.IO.FileAttributes]::Normal }
                $d.Delete()
            }
            $di.Refresh()
            if ($di.Exists) { $failed += $w; Write-Host ('FAILED: ' + $path) }
            else { $deleted += $w; Write-Host ('deleted: ' + $path) }
        } catch {
            $failed += $w; Write-Host ('FAILED: ' + $path + ' ' + $_.Exception.Message)
        }
        continue
    }
    $size = [int64]$f[1]
    $mtime = [int64]$f[2]
    $path = $root + $f[3]
    try {
        if ([System.IO.Directory]::Exists($path)) {
            $kept++; Write-Host ('not a regular file, kept: ' + $path); continue
        }
        $fi = New-Object System.IO.FileInfo $path
        if (-not $fi.Exists) {
            $gone++; Write-Host ('already gone: ' + $path); continue
        }
        if (($fi.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            $kept++; Write-Host ('not a regular file, kept: ' + $path); continue
        }
        if ($fi.Length -ne $size) {
            $kept++; Write-Host ('changed since the scan, kept: ' + $path); continue
        }
        if ($mtime -ne 0) {
            $secs = [int64](($fi.LastWriteTimeUtc - $epoch).TotalSeconds)
            if ([Math]::Abs($secs - $mtime) -gt 2) {
                $kept++; Write-Host ('changed since the scan, kept: ' + $path); continue
            }
        }
        $fi.Delete()
        $fi.Refresh()
        if ($fi.Exists) {
            $failed++; Write-Host ('FAILED: ' + $path)
        } else {
            $deleted++; Write-Host ('deleted: ' + $path)
        }
    } catch {
        $failed++; Write-Host ('FAILED: ' + $path + ' ' + $_.Exception.Message)
    }
}
Write-Host ('Done: ' + $deleted + ' deleted, ' + $kept + ' kept, ' + $gone + ' already gone, ' + $failed + ' failed.')
if ($failed -gt 0) { exit 1 }
exit 0
