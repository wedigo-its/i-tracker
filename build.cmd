@echo off
setlocal

:: ------------------------------------------------------------
:: Build script  -  i-Tracker (Go)
:: Usage:  build.cmd [clean] [tidy] [release]
::
::   (no argument)  Standard build (debug, current OS)
::   clean          Delete binaries, then build
::   tidy           Run go mod tidy first
::   release        Optimized build without debug symbols
::   clean, tidy and release can be combined
:: ------------------------------------------------------------

set MODULE=i-tracker
set CMD_PATH=.\cmd\i-tracker
set OUTDIR=_release
set OUT=%OUTDIR%\i-tracker.exe
set GOOS=windows
set GOARCH=amd64

:: Parse arguments
set DO_CLEAN=0
set DO_TIDY=0
set DO_RELEASE=0
for %%A in (%*) do (
    if /I "%%A"=="clean"   set DO_CLEAN=1
    if /I "%%A"=="tidy"    set DO_TIDY=1
    if /I "%%A"=="release" set DO_RELEASE=1
)

:: ---------- Prerequisites ----------
where go >nul 2>&1
if errorlevel 1 if defined GOROOT if exist "%GOROOT%\bin\go.exe" set "PATH=%PATH%;%GOROOT%\bin"
if errorlevel 1 if exist "C:\Program Files\Go\bin\go.exe" set "PATH=%PATH%;C:\Program Files\Go\bin"
if errorlevel 1 if exist "C:\go\bin\go.exe"               set "PATH=%PATH%;C:\go\bin"
where go >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Go is not in PATH. Install Go: https://go.dev/dl/
    exit /b 1
)
for /f "tokens=3" %%V in ('go version') do set GO_VER=%%V
echo [INFO ] Go version: %GO_VER%

:: ---------- Clean ----------
if %DO_CLEAN%==1 (
    echo [INFO ] Cleaning ...
    if exist "%OUT%" del /q "%OUT%"
)

:: ---------- go mod tidy ----------
if %DO_TIDY%==1 (
    echo [INFO ] go mod tidy ...
    go mod tidy
    if errorlevel 1 ( echo [ERROR] go mod tidy failed. & exit /b 1 )
)

:: ---------- Build flags ----------
:: Inject the build date into the binary (internal/version.BuildDate).
for /f "delims=" %%i in ('powershell -NoProfile -Command "Get-Date -Format \"yyyy-MM-dd HH:mm\""') do set "BUILD_DATE=%%i"
echo [INFO ] Build date: %BUILD_DATE%
set "VERSION_FLAG=-X '%MODULE%/internal/version.BuildDate=%BUILD_DATE%'"

set BUILD_FLAGS=

if %DO_RELEASE%==1 (
    echo [INFO ] Mode: release (no debug symbols^)
    set BUILD_FLAGS=-trimpath -ldflags "-s -w %VERSION_FLAG%"
) else (
    echo [INFO ] Mode: debug
    set BUILD_FLAGS=-ldflags "%VERSION_FLAG%"
)

:: ---------- Compile ----------
if not exist "%OUTDIR%" mkdir "%OUTDIR%"
echo [INFO ] Building %OUT% ...
go build %BUILD_FLAGS% -o "%OUT%" %CMD_PATH%
if errorlevel 1 (
    echo [ERROR] Build failed.
    exit /b 1
)

:: ---------- Result ----------
for %%F in ("%OUT%") do set SIZE=%%~zF
set /a SIZE_MB=%SIZE% / 1048576
echo [OK   ] %OUT% built  (%SIZE_MB% MB, build %BUILD_DATE%)
echo [NOTE ] Start by double-clicking %OUT% - web UI at http://localhost:8000

endlocal
