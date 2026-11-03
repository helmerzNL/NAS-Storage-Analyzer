@echo off
setlocal DisableDelayedExpansion
chcp 65001 >nul
rem NAS Storage Analyzer: delete script. It deletes exactly the files in the data block at the end, after you confirm.
rem The data block comes after the last exit and is never run as a command.
set "N="
set "ROOT="
for /f "usebackq skip=64 tokens=1,2* delims=|" %%T in ("%~f0") do (
  if "%%T"=="N" set "N=%%U"
  if "%%T"=="R" set "ROOT=%%U"
)
for /f "usebackq skip=64 tokens=1* delims=|" %%T in ("%~f0") do if "%%T"=="I" echo(%%U
set /a COUNT=0
for /f "usebackq skip=64 tokens=1 delims=|" %%T in ("%~f0") do if "%%T"=="F" set /a COUNT+=1
if not "%COUNT%"=="%N%" (
  echo(This script is incomplete or was changed: expected %N% entries, found %COUNT%. Nothing was changed.
  exit /b 2
)
pushd "%ROOT%" >nul 2>&1
if errorlevel 1 (
  echo(Cannot open the base path. Is it right, and is the share reachable? Nothing was changed.
  exit /b 2
)
set /a FOUND=0
for /f "usebackq skip=64 tokens=1,2,3* delims=|" %%T in ("%~f0") do if "%%T"=="F" if exist "%%W" set /a FOUND+=1
echo(Found under the base path: %FOUND% of %N%.
if "%FOUND%"=="0" (
  echo(None of the files were found. Is the base path right? Nothing was changed.
  popd
  exit /b 2
)
set "ANS="
set /p "ANS=Type DELETE %N% to confirm: "
if not "%ANS%"=="DELETE %N%" (
  echo(Cancelled. Nothing was changed.
  popd
  exit /b 2
)
set /a DELETED=0
set /a KEPT=0
set /a GONE=0
set /a FAILED=0
for /f "usebackq skip=64 tokens=1,2,3* delims=|" %%T in ("%~f0") do if "%%T"=="F" (
  if not exist "%%W" (
    echo(already gone: %%W
    set /a GONE+=1
  ) else if not "%%~zW"=="%%U" (
    echo(changed since the scan, kept: %%W
    set /a KEPT+=1
  ) else (
    del /q "%%W" >nul 2>&1
    if exist "%%W" (
      echo(FAILED: %%W
      set /a FAILED+=1
    ) else (
      echo(deleted: %%W
      set /a DELETED+=1
    )
  )
)
popd
echo(Done: %DELETED% deleted, %KEPT% kept, %GONE% already gone, %FAILED% failed.
if not "%FAILED%"=="0" exit /b 1
exit /b 0
N|3
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
F|1500000000|1761900000|Movies\Action\a b.mkv
F|42|1761900001|Movies\it's 100%.mkv
F|7|0|Music\日本語.flac
