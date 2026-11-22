@echo off
setlocal
title billing-system-dev

set "ROOT=%~dp0"
set "GUI_DIR=%ROOT%code\gui"
set "LOG_DIR=%ROOT%logs"
set "LOG_FILE=%LOG_DIR%\gui-dev.log"
set "WAILS_EXE=C:\Users\lei\go\bin\wails.exe"
set "DEV_OUT=%GUI_DIR%\build\bin\billing-dev"
set "DEV_EXE=%GUI_DIR%\build\bin\billing-dev.exe"

if not exist "%LOG_DIR%" mkdir "%LOG_DIR%" >nul 2>nul
echo [%date% %time%] Start billing desktop dev launcher > "%LOG_FILE%"

if not exist "%GUI_DIR%\wails.json" (
  echo GUI project not found: "%GUI_DIR%"
  echo GUI project not found: "%GUI_DIR%" >> "%LOG_FILE%"
  pause
  exit /b 1
)

if not exist "%WAILS_EXE%" set "WAILS_EXE=wails"

echo Stopping old dev app...
powershell -NoProfile -ExecutionPolicy Bypass -Command "$root=[IO.Path]::GetFullPath('%ROOT%'); Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'billing-dev.exe' -or ($_.Name -eq 'wails.exe' -and $_.CommandLine -like ('*' + $root + '*')) } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }" >> "%LOG_FILE%" 2>&1

echo Clearing UI cache...
if exist "%APPDATA%\billing-dev.exe" rmdir /S /Q "%APPDATA%\billing-dev.exe" >> "%LOG_FILE%" 2>&1

cd /d "%GUI_DIR%"
echo Building desktop dev version. Please wait...
echo Log: "%LOG_FILE%"

"%WAILS_EXE%" build -debug -s -skipbindings -m -nosyncgomod -nopackage -o billing-dev >> "%LOG_FILE%" 2>&1
if errorlevel 1 (
  echo.
  echo Build failed. See log: "%LOG_FILE%"
  if not "%~1"=="--nopause" pause
  exit /b 1
)

if not exist "%DEV_OUT%" (
  echo.
  echo Build output not found: "%DEV_OUT%"
  echo Build output not found: "%DEV_OUT%" >> "%LOG_FILE%"
  if not "%~1"=="--nopause" pause
  exit /b 1
)

copy /Y "%DEV_OUT%" "%DEV_EXE%" >> "%LOG_FILE%" 2>&1
if errorlevel 1 (
  echo.
  echo Copy dev exe failed. See log: "%LOG_FILE%"
  if not "%~1"=="--nopause" pause
  exit /b 1
)

echo Starting desktop dev version...
start "" /D "%GUI_DIR%\build\bin" "%DEV_EXE%"
echo.
echo Desktop dev version started:
echo "%DEV_EXE%"
echo.
echo Log:
echo "%LOG_FILE%"
if not "%~1"=="--nopause" pause
exit /b 0
