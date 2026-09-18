@echo off
setlocal enabledelayedexpansion

title QuantBot Build Script
color 0A

set "PROJECT=QuantBot AI量化机器人"
set "OUTPUT=QuantBot.exe"
set "OUTPUT_DIR=build\bin"

cd /d "%~dp0"

echo.
echo ============================================================
echo   %PROJECT% - Build Script
echo ============================================================
echo.

rem NO -clean flag: wails build without -clean keeps all existing
rem databases, configs and logs under build\bin fully preserved.

rem Check dependencies (explicit paths, independent of terminal PATH)
set "GO_EXE=D:\go\bin\go.exe"
set "WAILS_EXE=C:\Users\JokerZ\go\bin\wails.exe"

if not exist "%GO_EXE%" (
    echo [ERROR] Go not found: %GO_EXE%
    goto error
)

if not exist "%WAILS_EXE%" (
    echo [ERROR] Wails CLI not found: %WAILS_EXE%
    goto error
)

echo [OK] All dependencies verified.
echo.

rem Ensure output directory exists
if not exist "%OUTPUT_DIR%" mkdir "%OUTPUT_DIR%"

rem Set Go environment variables (consistent with dev environment)
set "GOROOT=D:\go"
set "GOPATH=C:\Users\JokerZ\go"
set "PATH=D:\go\bin;%GOPATH%\bin;%PATH%"
echo [Env] GOROOT=%GOROOT%
echo [Env] GOPATH=%GOPATH%
echo.

rem Execute build (no -clean, preserve build\bin data/config/log)
echo [Build] Starting wails build (wails build -platform windows/amd64)...
echo.

"%WAILS_EXE%" build -platform windows/amd64
if %errorlevel% neq 0 (
    echo.
    echo [ERROR] Build failed!
    goto error
)

rem Verify output
echo.
echo [Verify] Verifying output...
if not exist "%OUTPUT_DIR%\%OUTPUT%" (
    echo [ERROR] Output file not found: %OUTPUT_DIR%\%OUTPUT%
    goto error
)

for %%A in ("%OUTPUT_DIR%\%OUTPUT%") do set "SIZE_BYTES=%%~zA"
set /a "SIZE_MB=%SIZE_BYTES%/1048576"
for %%A in ("%OUTPUT_DIR%\%OUTPUT%") do (
    set "FILE_DATE=%%~tA"
)

echo   Output: %cd%\%OUTPUT_DIR%\%OUTPUT%
echo   Size:   %SIZE_MB% MB
echo   Date:   %FILE_DATE%
echo.
echo ============================================================
echo   [OK] BUILD SUCCESSFUL (build\bin data fully preserved)
echo ============================================================
echo.

goto end

:error
echo.
echo ============================================================
echo   [FAIL] BUILD FAILED
echo ============================================================
echo.
pause
exit /b 1

:end
endlocal
exit /b 0
