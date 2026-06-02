@echo off
:: Library Monitor — Agent Uninstaller
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

:: Stop the service
net stop "%SERVICE_NAME%" >nul 2>&1
sc stop "%SERVICE_NAME%" >nul 2>&1

:: Uninstall the service via the agent binary
if exist "%AGENT_EXE%" (
    "%AGENT_EXE%" uninstall >nul 2>&1
)

:: Also remove any leftover scheduled task from old install
schtasks /Delete /TN "%SERVICE_NAME%" /F >nul 2>&1

:: Kill any remaining agent.exe processes
taskkill /F /IM agent.exe >nul 2>&1

:: Remove agent binary (keep id.txt so re-install reuses same agent ID)
if exist "%AGENT_EXE%" del /F /Q "%AGENT_EXE%"

echo Agent uninstalled.
echo Service removed : %SERVICE_NAME%
echo Agent ID preserved at: %AGENT_DIR%\id.txt
endlocal
