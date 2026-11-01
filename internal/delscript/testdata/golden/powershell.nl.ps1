# NAS Storage Analyzer: delete script. It deletes exactly the files and folders in the data block below, after you confirm.
# The data block is plain data: nothing in it is ever run as a command.
# Run it with: powershell -NoProfile -ExecutionPolicy Bypass -File <this file>
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$data = @'
N|3
T|3
R|\\ZBNAS\Media
I|NAS Storage Analyzer: verwijderscript
I|Share:          Media
I|Padbasis:       \\ZBNAS\Media
I|Index van scan: 2026-10-31 12:30 UTC (scan 7)
I|Gemaakt:         2026-10-31 13:00 UTC
I|Te verwijderen: 3
I|Grootte (bovengrens): 1,4 GiB
I|Van deze bestanden hebben er 1 nog andere harde links: verwijderen geeft dan misschien geen ruimte.
I|Per map (grootste eerst):
I|      1        42 B  /Movies/
I|      1     1,4 GiB  /Movies/Action/
I|      1         7 B  /Music/
I|NIET in dit script (1), de reden staat per bestand:
I|  /Movies/gone.mkv: staat niet meer in de index (er is een scan geweest sinds je het aanvinkte)
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
    Write-Host ('Dit script is onvolledig of aangepast: verwacht ' + $n + ' onderdelen, gevonden ' + $files.Count + '. Er is niets gewijzigd.')
    exit 2
}
foreach ($l in $info) { Write-Host $l }
if (-not [System.IO.Directory]::Exists($root + '\')) {
    Write-Host 'De padbasis is niet te openen. Klopt hij, en is de share bereikbaar? Er is niets gewijzigd.'
    exit 2
}
$found = 0
foreach ($f in $files) {
    if ($f[0] -eq 'D') { if ([System.IO.Directory]::Exists($root + $f[3])) { $found++ } }
    elseif ([System.IO.File]::Exists($root + $f[3])) { $found++ }
}
Write-Host ('Gevonden onder de padbasis: ' + $found + ' van ' + $n + '.')
if ($found -eq 0) {
    Write-Host 'Geen enkel bestand gevonden. Klopt de padbasis? Er is niets gewijzigd.'
    exit 2
}
$answer = Read-Host ('Typ DELETE ' + $t + ' om te bevestigen')
if ($answer -cne ('DELETE ' + $t)) {
    Write-Host 'Geannuleerd. Er is niets gewijzigd.'
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
                if ([System.IO.File]::Exists($path)) { $kept += $w; Write-Host ('geen gewone map, bewaard: ' + $path) }
                else { $gone += $w; Write-Host ('bestond al niet meer: ' + $path) }
                continue
            }
            if (($di.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                $kept += $w; Write-Host ('geen gewone map, bewaard: ' + $path); continue
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
            if ($odd) { $kept += $w; Write-Host ('bevat koppelingen of speciale bestanden, bewaard: ' + $path); continue }
            if ($nf -gt [int64]$f[1] -or $nd -gt [int64]$f[2]) { $kept += $w; Write-Host ('bevat meer dan bij de scan, bewaard: ' + $path); continue }
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
            if ($di.Exists) { $failed += $w; Write-Host ('MISLUKT: ' + $path) }
            else { $deleted += $w; Write-Host ('verwijderd: ' + $path) }
        } catch {
            $failed += $w; Write-Host ('MISLUKT: ' + $path + ' ' + $_.Exception.Message)
        }
        continue
    }
    $size = [int64]$f[1]
    $mtime = [int64]$f[2]
    $path = $root + $f[3]
    try {
        if ([System.IO.Directory]::Exists($path)) {
            $kept++; Write-Host ('geen gewoon bestand, bewaard: ' + $path); continue
        }
        $fi = New-Object System.IO.FileInfo $path
        if (-not $fi.Exists) {
            $gone++; Write-Host ('bestond al niet meer: ' + $path); continue
        }
        if (($fi.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            $kept++; Write-Host ('geen gewoon bestand, bewaard: ' + $path); continue
        }
        if ($fi.Length -ne $size) {
            $kept++; Write-Host ('gewijzigd sinds de scan, bewaard: ' + $path); continue
        }
        if ($mtime -ne 0) {
            $secs = [int64](($fi.LastWriteTimeUtc - $epoch).TotalSeconds)
            if ([Math]::Abs($secs - $mtime) -gt 2) {
                $kept++; Write-Host ('gewijzigd sinds de scan, bewaard: ' + $path); continue
            }
        }
        $fi.Delete()
        $fi.Refresh()
        if ($fi.Exists) {
            $failed++; Write-Host ('MISLUKT: ' + $path)
        } else {
            $deleted++; Write-Host ('verwijderd: ' + $path)
        }
    } catch {
        $failed++; Write-Host ('MISLUKT: ' + $path + ' ' + $_.Exception.Message)
    }
}
Write-Host ('Klaar: ' + $deleted + ' verwijderd, ' + $kept + ' bewaard, ' + $gone + ' bestond al niet meer, ' + $failed + ' mislukt.')
if ($failed -gt 0) { exit 1 }
exit 0
