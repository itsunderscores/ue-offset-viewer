@echo off
setlocal
cd /d "%~dp0"

REM Cross-compile the SDK viewer for Ubuntu/Linux (x86_64) from Windows.
REM Output goes to dist\linux\ together with the install script and, if found,
REM a copy of sdk.txt - upload that whole folder to the server.

set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0

if exist dist\linux rmdir /s /q dist\linux
mkdir dist\linux

echo Building sdkviewer for linux/amd64...
go build -trimpath -ldflags="-s -w" -o dist\linux\sdkviewer . || (echo build failed & pause & exit /b 1)

REM Copy the Linux-side files, converting CRLF -> LF so bash/systemd accept them.
powershell -NoProfile -Command "foreach ($f in 'install-ubuntu.sh','sdkviewer.service') { $t = [IO.File]::ReadAllText(\"deploy\$f\") -replace \"`r`n\", \"`n\"; [IO.File]::WriteAllText(\"dist\linux\$f\", $t, (New-Object Text.UTF8Encoding $false)) }" || (echo failed to copy deploy files & pause & exit /b 1)
if exist offsets.json (
    copy /y offsets.json dist\linux\ >nul
    echo Included offsets.json
)

if exist sdk.txt (
    copy /y sdk.txt dist\linux\sdk.txt >nul
    echo Included sdk.txt
) else (
    echo NOTE: no sdk.txt in this folder - copy your dump next to the binary on the server.
)

REM Optional: also build for ARM servers (Raspberry Pi, AWS Graviton, Oracle Ampere).
if /i "%~1"=="arm64" (
    set GOARCH=arm64
    echo Building sdkviewer for linux/arm64...
    go build -trimpath -ldflags="-s -w" -o dist\linux\sdkviewer-arm64 . || (echo arm64 build failed & pause & exit /b 1)
)

echo.
echo Done. Files in dist\linux:
dir /b dist\linux
echo.
echo Upload to the server, e.g.:
echo   scp -r dist\linux user@SERVER-IP:~/sdkviewer
echo (If you use WinSCP/FileZilla, set transfer mode to BINARY, not text/ASCII,
echo  or the .sh/.service files get Windows line endings and fail with "bash\r".)
echo then on the server:
echo   cd ~/sdkviewer ^&^& chmod +x install-ubuntu.sh ^&^& sudo ./install-ubuntu.sh
echo.
echo Open http://SERVER-IP:1336
pause
