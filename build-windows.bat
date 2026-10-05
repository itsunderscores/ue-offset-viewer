@echo off
setlocal
cd /d "%~dp0"

echo Building sdkviewer for windows/amd64...
go build -trimpath -ldflags="-s -w" -o releases\windows-amd64\sdkviewer.exe . || (echo build failed & pause & exit /b 1)

echo.
echo Binary: releases\windows-amd64\sdkviewer.exe
echo Run with sample-sdk.txt:  sdkviewer.exe -file sample-sdk.txt -open
pause
