@echo off
setlocal

:: ------------------------------------------------------------
:: Start script  -  i-Tracker
:: Usage:  start.cmd [rebuild]
::
::   (no argument)  starts _release\i-tracker.exe, building it first
::                  if it does not exist yet
::   rebuild        rebuilds the binary before starting (after code changes)
::
:: The web UI opens in the browser at http://localhost:8000.
:: Stop with Ctrl+C or by closing the console window.
:: ------------------------------------------------------------

cd /d "%~dp0"

if /I "%~1"=="rebuild" goto build
if exist "_release\i-tracker.exe" goto run

:build
call "%~dp0build.cmd" release
if errorlevel 1 exit /b 1

:run
:: Is an instance already listening on port 8000? Then just open the browser.
powershell -NoProfile -Command "if (Get-NetTCPConnection -State Listen -LocalPort 8000 -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }"
if not errorlevel 1 (
    echo [INFO ] i-Tracker is already running - opening the browser only.
    start "" http://localhost:8000
    exit /b 0
)

:: Open the browser slightly delayed so the server is already listening.
start "" /min powershell -NoProfile -WindowStyle Hidden -Command "Start-Sleep -Seconds 2; Start-Process 'http://localhost:8000'"

echo [INFO ] Starting i-Tracker - web UI at http://localhost:8000
echo [INFO ] Stop with Ctrl+C
title i-Tracker
cd /d "%~dp0_release"
i-tracker.exe

endlocal
