@echo off
:: Library Monitor — Agent Installer
:: Run as Administrator on target PC

setlocal
set AGENT_DIR=C:\LibraryAgent
set AGENT_EXE=%AGENT_DIR%\agent.exe
set TASK_NAME=LibraryAgent

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

:: Remove existing scheduled task
schtasks /Delete /TN "%TASK_NAME%" /F >nul 2>&1

:: Create task: run as SYSTEM at startup, highest privileges
schtasks /Create /TN "%TASK_NAME%" /TR "\"%AGENT_EXE%\"" /SC ONSTART /RU SYSTEM /RL HIGHEST /F >nul
if %errorlevel% neq 0 (
    echo ERROR: Could not create scheduled task.
    pause
    exit /b 1
)

:: Start immediately
schtasks /Run /TN "%TASK_NAME%" >nul

echo Agent installed and started.
echo Directory : %AGENT_DIR%
echo Task      : %TASK_NAME% (SYSTEM, run at startup)
endlocal
