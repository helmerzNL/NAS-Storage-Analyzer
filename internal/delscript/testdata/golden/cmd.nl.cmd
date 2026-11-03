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
  echo(Dit script is onvolledig of aangepast: verwacht %N% onderdelen, gevonden %COUNT%. Er is niets gewijzigd.
  exit /b 2
)
pushd "%ROOT%" >nul 2>&1
if errorlevel 1 (
  echo(De padbasis is niet te openen. Klopt hij, en is de share bereikbaar? Er is niets gewijzigd.
  exit /b 2
)
set /a FOUND=0
for /f "usebackq skip=64 tokens=1,2,3* delims=|" %%T in ("%~f0") do if "%%T"=="F" if exist "%%W" set /a FOUND+=1
echo(Gevonden onder de padbasis: %FOUND% van %N%.
if "%FOUND%"=="0" (
  echo(Geen enkel bestand gevonden. Klopt de padbasis? Er is niets gewijzigd.
  popd
  exit /b 2
)
set "ANS="
set /p "ANS=Typ DELETE %N% om te bevestigen: "
if not "%ANS%"=="DELETE %N%" (
  echo(Geannuleerd. Er is niets gewijzigd.
  popd
  exit /b 2
)
set /a DELETED=0
set /a KEPT=0
set /a GONE=0
set /a FAILED=0
for /f "usebackq skip=64 tokens=1,2,3* delims=|" %%T in ("%~f0") do if "%%T"=="F" (
  if not exist "%%W" (
    echo(bestond al niet meer: %%W
    set /a GONE+=1
  ) else if not "%%~zW"=="%%U" (
    echo(gewijzigd sinds de scan, bewaard: %%W
    set /a KEPT+=1
  ) else (
    del /q "%%W" >nul 2>&1
    if exist "%%W" (
      echo(MISLUKT: %%W
      set /a FAILED+=1
    ) else (
      echo(verwijderd: %%W
      set /a DELETED+=1
    )
  )
)
popd
echo(Klaar: %DELETED% verwijderd, %KEPT% bewaard, %GONE% bestond al niet meer, %FAILED% mislukt.
if not "%FAILED%"=="0" exit /b 1
exit /b 0
N|3
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
F|1500000000|1761900000|Movies\Action\a b.mkv
F|42|1761900001|Movies\it's 100%.mkv
F|7|0|Music\日本語.flac
