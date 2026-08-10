@echo off
taskkill /F /IM agent.exe >nul 2>&1
timeout /t 1 /nobreak >nul
set "LIBRARY_SERVER_URL=ws://localhost:8080/ws"
cd /d C:\frendy-project\librawatch\deploy
start "" agent.exe
