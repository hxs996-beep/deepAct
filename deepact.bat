@echo off
rem ============================================================
rem  DeepAct launcher for Windows (double-click friendly)
rem  - Sets UTF-8 codepage so Chinese renders correctly
rem  - Keeps the window open on failure so errors stay visible
rem  - Runs the TUI from the directory this file lives in
rem  Place this file next to deepact.exe (same folder).
rem ============================================================
chcp 65001 >nul
title DeepAct

cd /d "%~dp0"

if not exist "%~dp0deepact.exe" (
    echo [ERROR] deepact.exe not found in %~dp0
    echo         Make sure deepact.bat sits next to deepact.exe.
    echo.
    pause
    exit /b 1
)

deepact.exe %*
set EXITCODE=%ERRORLEVEL%

if not "%EXITCODE%"=="0" (
    echo.
    echo [deepact] exited with code %EXITCODE%
    echo.
    pause
)
exit /b %EXITCODE%
