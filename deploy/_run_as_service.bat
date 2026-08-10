@echo off
taskkill /F /IM agent.exe >nul 2>&1
net stop LibraryAgent >nul 2>&1
timeout /t 1 /nobreak >nul
copy /y "C:\frendy-project\librawatch\deploy\agent.exe" "C:\LibraryAgent\agent.exe" >nul
echo ws://localhost:8080/ws> "C:\LibraryAgent\server.txt"
net start LibraryAgent
