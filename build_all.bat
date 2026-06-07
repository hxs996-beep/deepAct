@echo off
set CGO_ENABLED=0
set V=0.1.3
set C=HEAD
set D=2026-06-06T00:00:00Z
set LDFLAGS=-s -w -X main.version=%V% -X main.commit=%C% -X main.date=%D%

echo === Building windows/amd64 ===
mkdir build\deepact_windows_amd64 2>nul
go build -ldflags "%LDFLAGS%" -o build\deepact_windows_amd64\deepact.exe .
if %ERRORLEVEL% neq 0 exit /b %ERRORLEVEL%

echo === Building linux/amd64 ===
mkdir build\deepact_linux_amd64 2>nul
set GOOS=linux
set GOARCH=amd64
go build -ldflags "%LDFLAGS%" -o build\deepact_linux_amd64\deepact .
if %ERRORLEVEL% neq 0 exit /b %ERRORLEVEL%

echo === Building linux/arm64 ===
mkdir build\deepact_linux_arm64 2>nul
set GOOS=linux
set GOARCH=arm64
go build -ldflags "%LDFLAGS%" -o build\deepact_linux_arm64\deepact .
if %ERRORLEVEL% neq 0 exit /b %ERRORLEVEL%

echo === Building darwin/amd64 ===
mkdir build\deepact_darwin_amd64 2>nul
set GOOS=darwin
set GOARCH=amd64
go build -ldflags "%LDFLAGS%" -o build\deepact_darwin_amd64\deepact .
if %ERRORLEVEL% neq 0 exit /b %ERRORLEVEL%

echo === Building darwin/arm64 ===
mkdir build\deepact_darwin_arm64 2>nul
set GOOS=darwin
set GOARCH=arm64
go build -ldflags "%LDFLAGS%" -o build\deepact_darwin_arm64\deepact .
if %ERRORLEVEL% neq 0 exit /b %ERRORLEVEL%

echo === All builds complete ===
dir /s /b build\*.exe build\*\deepact 2>nul
