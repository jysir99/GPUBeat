@echo off
cd /d "%~dp0"
setlocal

echo ========================================
echo   GPUBeat - Windows Build
echo ========================================
echo.

REM 1. Stop a running gpubeat.exe so the output file is not locked
tasklist /fi "imagename eq gpubeat.exe" 2>nul | find /i "gpubeat.exe" >nul
if %errorlevel% equ 0 (
    echo [1/3] gpubeat.exe is running, stopping it...
    taskkill /im gpubeat.exe /f >nul 2>&1
) else (
    echo [1/3] No running gpubeat.exe
)

REM 2. Ensure the output directory exists
if not exist dist mkdir dist
echo [2/3] Output dir: dist\

REM 3. Build (-s -w strips debug info for a smaller binary)
echo [3/3] Building...
go build -ldflags="-s -w" -o dist\gpubeat.exe .
if %errorlevel% neq 0 (
    echo.
    echo === BUILD FAILED ===
    exit /b 1
)

echo.
echo ========================================
echo   BUILD OK
echo   Output: dist\gpubeat.exe
for %%A in (dist\gpubeat.exe) do echo   Size:  %%~zA bytes
echo ========================================

endlocal
