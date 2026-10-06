@echo off
rem ============================================================
rem  QuantBot Decision-Brain DLL Build Script
rem  Output: repo-root bin\agent.dll (+ bin\agent.h emitted by Go)
rem  Usage:  dll\build.bat   (run from anywhere; paths are absolute)
rem ============================================================
setlocal

cd /d "%~dp0"

set "GO_EXE=D:\go\bin\go.exe"
if not exist "%GO_EXE%" (
    echo [ERROR] Go not found: %GO_EXE%
    exit /b 1
)

set "GOROOT=D:\go"
set "GOPATH=C:\Users\JokerZ\go"
set "PATH=D:\go\bin;%GOPATH%\bin;%PATH%"

echo [Build] agent.dll (c-shared)...
"%GO_EXE%" build -buildmode=c-shared -o "..\bin\agent.dll" .
if errorlevel 1 (
    echo [FAIL] agent.dll build failed
    exit /b 1
)

if not exist "..\bin\agent.h" (
    echo [warn] agent.h not emitted by this Go version; expected next to agent.dll
)

echo [OK] ..\bin\agent.dll + ..\bin\agent.h
endlocal
exit /b 0
