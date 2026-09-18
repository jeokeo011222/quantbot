@echo off
setlocal enabledelayedexpansion

title QuantBot Dev Mode
color 0B

set "VERSION=1.5.0"
set "PROJECT=QuantBot AI量化机器人"

rem Parse arguments
set "PORT=3456"
set "OPEN_BROWSER=1"
set "SKIP_DEPENDENCIES=0"
set "VERBOSE=0"
set "DEVICE=auto"

:parse_args
if "%~1"=="" goto end_parse
if /i "%~1"=="-port" (
    shift
    set "PORT=%~1"
)
if /i "%~1"=="-no-browser" set "OPEN_BROWSER=0"
if /i "%~1"=="-skip-deps" set "SKIP_DEPENDENCIES=1"
if /i "%~1"=="-verbose" set "VERBOSE=1"
if /i "%~1"=="-device" (
    shift
    set "DEVICE=%~1"
)
if /i "%~1"=="-h" goto show_help
if /i "%~1"=="--help" goto show_help
if /i "%~1"=="/?" goto show_help
shift
goto parse_args

:end_parse

echo.
echo ============================================================
echo   %PROJECT% - Dev Mode v%VERSION%
echo   Hot reload enabled - code changes auto-reload
echo ============================================================
echo.

cd /d "%~dp0"

rem Check dependencies
if "%SKIP_DEPENDENCIES%"=="0" (
    echo [Check] Verifying dependencies...
    
    where wails >nul 2>&1
    if %errorlevel% neq 0 (
        echo [ERROR] Wails CLI not found. Run: go install github.com/wailsapp/wails/v2/cmd/wails@latest
        goto error
    )
    
    where npm >nul 2>&1
    if %errorlevel% neq 0 (
        echo [ERROR] npm not found. Please install Node.js 18+.
        goto error
    )
    
    rem Check if node_modules exists
    if not exist "ui\node_modules" (
        echo [Setup] Installing frontend dependencies...
        cd ui
        call npm install
        if %errorlevel% neq 0 (
            echo [ERROR] npm install failed.
            cd ..
            goto error
        )
        cd ..
    )
    
    echo [OK] Dependencies verified.
    echo.
)

rem Show dev configuration
echo [Config] Dev configuration:
echo   Port: %PORT%
echo   Auto-open browser: %OPEN_BROWSER%
echo   Device target: %DEVICE%
echo   Verbose: %VERBOSE%
echo   Skip dependencies: %SKIP_DEPENDENCIES%
echo.

rem Check if port is available
echo [Check] Verifying port %PORT% availability...
netstat -ano | findstr ":%PORT%" | findstr "LISTENING" >nul 2>&1
if %errorlevel% equ 0 (
    echo [WARN] Port %PORT% is already in use!
    echo        Another application may be using this port.
    echo        The dev server may fail to start.
    echo.
    choice /c YN /m "Continue anyway"
    if errorlevel 2 (
        echo [Info] Please choose a different port or stop the conflicting process.
        goto end
    )
)

rem Build dev command
set "DEV_CMD=wails dev"

if not "%PORT%"=="3456" set "DEV_CMD=%DEV_CMD% -port %PORT%"
if "%OPEN_BROWSER%"=="0" set "DEV_CMD=%DEV_CMD% -browser=false"
if "%VERBOSE%"=="1" set "DEV_CMD=%DEV_CMD% -v"
if not "%DEVICE%"=="auto" set "DEV_CMD=%DEV_CMD% -device %DEVICE%"

rem Start dev server
echo.
echo [Start] Starting dev server...
echo [Cmd] %DEV_CMD%
echo.
echo Press Ctrl+C to stop the dev server.
echo ============================================================
echo.

%DEV_CMD%

if %errorlevel% neq 0 (
    echo.
    echo [ERROR] Dev server exited with error code %errorlevel%
    echo.
    echo Troubleshooting:
    echo   1. Check if port %PORT% is available
    echo   2. Run 'npm install' in ui/ directory
    echo   3. Run 'go mod tidy' to update Go dependencies
    echo   4. Check the console output above for detailed errors
    goto error
)

goto end

:show_help
echo.
echo Usage: dev.bat [options]
echo.
echo Options:
echo   -port PORT          Set dev server port (default: 3456)
echo   -no-browser         Don't auto-open browser
echo   -skip-deps          Skip dependency checks/install
echo   -verbose            Enable verbose output
echo   -device DEVICE      Target device (auto, desktop, mobile)
echo   -h, --help          Show this help message
echo.
echo Examples:
echo   dev.bat                          Start dev server on default port
echo   dev.bat -port 8080               Use custom port
echo   dev.bat -no-browser -skip-deps   Fast dev startup
echo   dev.bat -port 3456 -verbose      Debug mode
echo.
goto end

:error
echo.
echo ============================================================
echo   [FAIL] DEV SERVER FAILED
echo ============================================================
echo.
echo Please check the error messages above.
pause
exit /b 1

:end
endlocal
exit /b 0
