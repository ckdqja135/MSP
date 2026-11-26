@echo off
rem 설치나 설정 없이 이 컴퓨터를 바로 본다. Docker 컨테이너와 열린 포트가 나온다.
rem 필요한 것: Go, Node.js. 사용법: local.cmd  (끝내려면 Ctrl+C)
chcp 65001 >nul
cd /d "%~dp0web" || exit /b 1
if not exist node_modules call npm install || exit /b 1
call npm run build || exit /b 1
cd /d "%~dp0" || exit /b 1
go run ./cmd/central --local %*
