@echo off
rem Double-click this file to start code_judge on Windows (it runs inside WSL).
rem When it says "Ready", open http://localhost:8080 in your browser.
rem Keep this window open while you use the page; press Ctrl+C or close it to stop.
wsl -d Ubuntu --cd "%~dp0." -- bash scripts/web.sh
pause
