@echo off
:: Library Monitor — Agent Installer
:: Run as Administrator on target PC

setlocal
set AGENT_DIR=C:\LibraryAgent
set AGENT_EXE=%AGENT_DIR%\agent.exe
set SERVICE_NAME=LibraryAgent

:: Require Administrator
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo ERROR: Run as Administrator.
    pause
    exit /b 1
)

:: Create agent directory
if not exist "%AGENT_DIR%" mkdir "%AGENT_DIR%"

:: Copy agent binary from same folder as this script
copy /Y "%~dp0agent.exe" "%AGENT_EXE%" >nul
if %errorlevel% neq 0 (
    echo ERROR: Could not copy agent.exe
    pause
    exit /b 1
)

:: Copy server URL config if present next to this script
if exist "%~dp0server.txt" (
    copy /Y "%~dp0server.txt" "%AGENT_DIR%\server.txt" >nul
)

:: Copy auth token if present
if exist "%~dp0token.txt" (
    copy /Y "%~dp0token.txt" "%AGENT_DIR%\token.txt" >nul
)

:: Stop and remove any existing service or old scheduled task
sc stop "%SERVICE_NAME%" >nul 2>&1
"%AGENT_EXE%" uninstall >nul 2>&1
schtasks /Delete /TN "%SERVICE_NAME%" /F >nul 2>&1

:: Install as Windows Service (auto-start, restart on failure)
"%AGENT_EXE%" install
if %errorlevel% neq 0 (
    echo ERROR: Could not install Windows Service.
    pause
    exit /b 1
)

:: Start the service immediately
net start "%SERVICE_NAME%"
if %errorlevel% neq 0 (
    echo WARNING: Service installed but could not be started right now.
    echo It will start automatically on next boot.
)

echo.
echo Agent installed as Windows Service.
echo Directory : %AGENT_DIR%
echo Service   : %SERVICE_NAME% (auto-start, restart on failure)
endlocal
