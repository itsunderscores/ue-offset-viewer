@echo off
cd /d "%~dp0"
go build -o sdkviewer.exe . || (echo build failed & pause & exit /b 1)
sdkviewer.exe -open %*
