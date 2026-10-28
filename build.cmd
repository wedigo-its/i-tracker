@echo off
setlocal

:: ------------------------------------------------------------
:: Build script  -  i-Tracker (Go)
:: Usage:  build.cmd [clean] [tidy] [release] [linux]
::
::   (no argument)  Standard build (debug, windows/amd64)
::   clean          Delete binaries, then build
::   tidy           Run go mod tidy first
::   release        Optimized windows/amd64 build without debug symbols
::   linux          Static linux/amd64 and linux/arm64 builds (no CGO)
::                  into _release\linux-amd64 and _release\linux-arm64
::   Arguments can be combined; "linux" without "release" builds Linux only.
::
:: The version comes from "git describe --tags --always --dirty" ("dev"
:: without git); only a clean build of a tag updates itself.
:: ------------------------------------------------------------

cd /d "%~dp0"

set MODULE=i-tracker
set CMD_PATH=.\cmd\i-tracker
set OUTDIR=_release
set OUT=%OUTDIR%\i-tracker.exe
set CGO_ENABLED=0

:: Parse arguments
set DO_CLEAN=0
set DO_TIDY=0
set DO_RELEASE=0
set DO_LINUX=0
set DO_WINDOWS=1
for %%A in (%*) do (
    if /I "%%A"=="clean"   set DO_CLEAN=1
    if /I "%%A"=="tidy"    set DO_TIDY=1
    if /I "%%A"=="release" set DO_RELEASE=1
    if /I "%%A"=="linux"   set DO_LINUX=1
)
if %DO_LINUX%==1 if %DO_RELEASE%==0 set DO_WINDOWS=0

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
    if exist "%OUTDIR%\linux-amd64\i-tracker" del /q "%OUTDIR%\linux-amd64\i-tracker"
    if exist "%OUTDIR%\linux-arm64\i-tracker" del /q "%OUTDIR%\linux-arm64\i-tracker"
)

:: ---------- go mod tidy ----------
if %DO_TIDY%==1 (
    echo [INFO ] go mod tidy ...
    go mod tidy
    if errorlevel 1 ( echo [ERROR] go mod tidy failed. & exit /b 1 )
)

:: ---------- Version and build date ----------
:: Injected into internal/version (Version, BuildDate).
set "VERSION="
for /f "delims=" %%i in ('git describe --tags --always --dirty 2^>nul') do set "VERSION=%%i"
if "%VERSION%"=="" set "VERSION=dev"
for /f "delims=" %%i in ('powershell -NoProfile -Command "Get-Date -Format \"yyyy-MM-dd HH:mm\""') do set "BUILD_DATE=%%i"
echo [INFO ] Version: %VERSION%, build date: %BUILD_DATE%
set "VERSION_FLAGS=-X '%MODULE%/internal/version.Version=%VERSION%' -X '%MODULE%/internal/version.BuildDate=%BUILD_DATE%'"

if not exist "%OUTDIR%" mkdir "%OUTDIR%"

if %DO_WINDOWS%==1 call :build_windows || exit /b 1
if %DO_LINUX%==1 call :build_linux amd64 || exit /b 1
if %DO_LINUX%==1 call :build_linux arm64 || exit /b 1

endlocal
exit /b 0

:: ---------- windows/amd64 ----------
:build_windows
set GOOS=windows
set GOARCH=amd64
if %DO_RELEASE%==1 (
    echo [INFO ] Mode: release (no debug symbols^)
    set BUILD_FLAGS=-trimpath -ldflags "-s -w %VERSION_FLAGS%"
) else (
    echo [INFO ] Mode: debug
    set BUILD_FLAGS=-ldflags "%VERSION_FLAGS%"
)
echo [INFO ] Building %OUT% ...
go build %BUILD_FLAGS% -o "%OUT%" %CMD_PATH%
if errorlevel 1 (
    echo [ERROR] Build failed.
    exit /b 1
)
for %%F in ("%OUT%") do set SIZE=%%~zF
set /a SIZE_MB=%SIZE% / 1048576
echo [OK   ] %OUT% built  (%SIZE_MB% MB, version %VERSION%, build %BUILD_DATE%)
echo [NOTE ] Start by double-clicking %OUT% - web UI at http://localhost:8000
exit /b 0

:: ---------- linux/<arch>, static ----------
:build_linux
set GOOS=linux
set GOARCH=%1
set "LINUX_OUT=%OUTDIR%\linux-%1\i-tracker"
if not exist "%OUTDIR%\linux-%1" mkdir "%OUTDIR%\linux-%1"
echo [INFO ] Building %LINUX_OUT% (linux/%1, static) ...
go build -trimpath -ldflags "-s -w %VERSION_FLAGS%" -o "%LINUX_OUT%" %CMD_PATH%
if errorlevel 1 (
    echo [ERROR] Build for linux/%1 failed.
    exit /b 1
)
for %%F in ("%LINUX_OUT%") do set SIZE=%%~zF
set /a SIZE_MB=%SIZE% / 1048576
echo [OK   ] %LINUX_OUT% built  (%SIZE_MB% MB, version %VERSION%)
exit /b 0
