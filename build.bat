@echo off
setlocal enabledelayedexpansion

title QuantBot Build Script
color 0A

set "PROJECT=QuantBot Build"
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

rem Copy decision-brain DLL next to the exe (bin\agent.dll -> build\bin\agent.dll)
if exist "bin\agent.dll" (
    copy /Y "bin\agent.dll" "%OUTPUT_DIR%\agent.dll" >nul
    echo [OK] Copied agent.dll
    if exist "bin\agent.h" (
        copy /Y "bin\agent.h" "%OUTPUT_DIR%\agent.h" >nul
        echo [OK] Copied agent.h
    )
) else (
    echo [warn] bin\agent.dll not found; exe will fall back to harness
)

rem Sync bridge strategy folders from third_party (ship with every build)
if exist "third_party\Ptrade" (
    if not exist "%OUTPUT_DIR%\Ptrade" mkdir "%OUTPUT_DIR%\Ptrade"
    xcopy "third_party\Ptrade\*" "%OUTPUT_DIR%\Ptrade\" /E /I /Y >nul
    echo [OK] Synced third_party\Ptrade -^> build\bin\Ptrade
)
if exist "third_party\XtQuant" (
    if not exist "%OUTPUT_DIR%\XtQuant" mkdir "%OUTPUT_DIR%\XtQuant"
    xcopy "third_party\XtQuant\*" "%OUTPUT_DIR%\XtQuant\" /E /I /Y >nul
    echo [OK] Synced third_party\XtQuant -^> build\bin\XtQuant
)

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
