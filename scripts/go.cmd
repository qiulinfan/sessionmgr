@echo off
setlocal

where go.exe >nul 2>nul
if errorlevel 1 goto bootstrap

:check_system_go
set "SESSIONMGR_SYSTEM_GO_VERSION="
for /f "delims=" %%V in ('go.exe env GOVERSION 2^>nul') do set "SESSIONMGR_SYSTEM_GO_VERSION=%%V"
echo %SESSIONMGR_SYSTEM_GO_VERSION%| %SystemRoot%\System32\findstr.exe /r /c:"^go1\.2[4-9]\." /c:"^go1\.[3-9][0-9]\." /c:"^go[2-9]\." >nul
if errorlevel 1 goto bootstrap

go.exe %*
exit /b %errorlevel%

:bootstrap

set "SESSIONMGR_GO_ARCH="
if /I "%PROCESSOR_ARCHITECTURE%"=="AMD64" set "SESSIONMGR_GO_ARCH=amd64"
if /I "%PROCESSOR_ARCHITECTURE%"=="ARM64" set "SESSIONMGR_GO_ARCH=arm64"
if /I "%PROCESSOR_ARCHITEW6432%"=="AMD64" set "SESSIONMGR_GO_ARCH=amd64"
if /I "%PROCESSOR_ARCHITEW6432%"=="ARM64" set "SESSIONMGR_GO_ARCH=arm64"

if not defined SESSIONMGR_GO_ARCH (
    echo sessionmgr: automatic Go bootstrap does not support this Windows architecture; install Go 1.24 or newer. 1>&2
    exit /b 1
)

set "SESSIONMGR_POWERSHELL="
where pwsh.exe >nul 2>nul
if not errorlevel 1 set "SESSIONMGR_POWERSHELL=pwsh.exe"
if not defined SESSIONMGR_POWERSHELL (
    where powershell.exe >nul 2>nul
    if not errorlevel 1 set "SESSIONMGR_POWERSHELL=powershell.exe"
)

if not defined SESSIONMGR_POWERSHELL (
    echo sessionmgr: Go is missing and PowerShell is unavailable; install Go 1.24 or newer. 1>&2
    exit /b 1
)

"%SESSIONMGR_POWERSHELL%" -NoProfile -ExecutionPolicy Bypass -File "%~dp0bootstrap-go.ps1"
if errorlevel 1 exit /b %errorlevel%

"%~dp0..\.tools\go\windows-%SESSIONMGR_GO_ARCH%\bin\go.exe" %*
exit /b %errorlevel%
