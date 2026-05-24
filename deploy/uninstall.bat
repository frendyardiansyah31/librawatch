@echo off
:: Library Monitor — Agent Uninstaller
:: Run as Administrator on target PC

setlocal
set AGENT_DIR=C:\LibraryAgent
set TASK_NAME=LibraryAgent

:: Require Administrator
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo ERROR: Run as Administrator.
    pause
    exit /b 1
)

:: Stop scheduled task
schtasks /End /TN "%TASK_NAME%" >nul 2>&1

:: Delete scheduled task
schtasks /Delete /TN "%TASK_NAME%" /F >nul 2>&1

:: Kill process if still running
taskkill /F /IM agent.exe >nul 2>&1

:: Remove agent binary (keep id.txt so re-install reuses same agent ID)
if exist "%AGENT_DIR%\agent.exe" del /F /Q "%AGENT_DIR%\agent.exe"

echo Agent uninstalled.
echo Task Scheduler entry removed: %TASK_NAME%
echo Agent ID preserved at: %AGENT_DIR%\id.txt
endlocal
