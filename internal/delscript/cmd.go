package delscript

import (
	"strconv"
	"strings"
)

// renderCmd writes the cmd.exe batch file. cmd is the weakest of the three, so it is also the most careful:
//
//   - The code is plain ASCII and has no blank lines. The data comes after a final "exit /b", which cmd never
//     parses, and is read back with "for /f skip=N" from the batch file itself, N being the number of lines of code. A for variable is substituted after
//     the line has been parsed, so an & ^ ( ) or % in a file name stays text.
//   - There is no "call", no "goto" and no delayed expansion, because each of them parses the value again.
//   - Files are addressed relative to the base path, after pushd, which also makes a \\server\share path work.
//   - cmd can only compare the size of a file reliably (its time stamp depends on the locale), and it cannot
//     tell an empty file from a folder, so files of size 0 are left out when the script is generated: a folder
//     has size 0 in %~z, which is what protects a folder that took the place of a file from "del".
//   - The file is UTF-8 without a BOM and begins with "chcp 65001", which is what makes for /f read the names.
func renderCmd(t *messages, data []string) string {
	var code []string
	w := func(lines ...string) { code = append(code, lines...) }
	w(
		"@echo off",
		"setlocal DisableDelayedExpansion",
		"chcp 65001 >nul",
		"rem NAS Storage Analyzer: delete script. It deletes exactly the files in the data block at the end, after you confirm.",
		"rem The data block comes after the last exit and is never run as a command.",
		`set "N="`,
		`set "ROOT="`,
		`for /f "usebackq skip=SKIPN tokens=1,2* delims=|" %%T in ("%~f0") do (`,
		`  if "%%T"=="N" set "N=%%U"`,
		`  if "%%T"=="R" set "ROOT=%%U"`,
		")",
		`for /f "usebackq skip=SKIPN tokens=1* delims=|" %%T in ("%~f0") do if "%%T"=="I" echo(%%U`,
		"set /a COUNT=0",
		`for /f "usebackq skip=SKIPN tokens=1 delims=|" %%T in ("%~f0") do if "%%T"=="F" set /a COUNT+=1`,
		`if not "%COUNT%"=="%N%" (`,
		"  echo("+t.incomplete[0]+"%N%"+t.incomplete[1]+"%COUNT%"+t.incomplete[2],
		"  exit /b 2",
		")",
		`pushd "%ROOT%" >nul 2>&1`,
		"if errorlevel 1 (",
		"  echo("+t.notOpen,
		"  exit /b 2",
		")",
		"set /a FOUND=0",
		`for /f "usebackq skip=SKIPN tokens=1,2,3* delims=|" %%T in ("%~f0") do if "%%T"=="F" if exist "%%W" set /a FOUND+=1`,
		"echo("+t.found[0]+"%FOUND%"+t.found[1]+"%N%"+t.found[2],
		`if "%FOUND%"=="0" (`,
		"  echo("+t.wrongRoot,
		"  popd",
		"  exit /b 2",
		")",
		`set "ANS="`,
		`set /p "ANS=`+t.confirm[0]+`%N%`+t.confirm[1]+`"`,
		`if not "%ANS%"=="DELETE %N%" (`,
		"  echo("+t.cancelled,
		"  popd",
		"  exit /b 2",
		")",
		"set /a DELETED=0",
		"set /a KEPT=0",
		"set /a GONE=0",
		"set /a FAILED=0",
		`for /f "usebackq skip=SKIPN tokens=1,2,3* delims=|" %%T in ("%~f0") do if "%%T"=="F" (`,
		`  if not exist "%%W" (`,
		"    echo("+t.gone+": %%W",
		"    set /a GONE+=1",
		`  ) else if not "%%~zW"=="%%U" (`,
		"    echo("+t.changed+": %%W",
		"    set /a KEPT+=1",
		"  ) else (",
		`    del /q "%%W" >nul 2>&1`,
		`    if exist "%%W" (`,
		"      echo("+t.failed+": %%W",
		"      set /a FAILED+=1",
		"    ) else (",
		"      echo("+t.deleted+": %%W",
		"      set /a DELETED+=1",
		"    )",
		"  )",
		")",
		"popd",
		"echo("+t.done[0]+"%DELETED%"+t.done[1]+"%KEPT%"+t.done[2]+"%GONE%"+t.done[3]+"%FAILED%"+t.done[4],
		`if not "%FAILED%"=="0" exit /b 1`,
		"exit /b 0",
	)
	// The loops skip the code and see only the records, so a quote or a bar in a line of code can never reach an
	// "if". For that the number of lines of code must be exact: there is no blank line and no line starting with
	// a semicolon in it, which for /f would treat differently, and a test checks the number.
	skip := strconv.Itoa(len(code))
	var b strings.Builder
	for _, l := range code {
		b.WriteString(strings.ReplaceAll(l, "SKIPN", skip))
		b.WriteString("\r\n")
	}
	for _, l := range data {
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	return b.String()
}
