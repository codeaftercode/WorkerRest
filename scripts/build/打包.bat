@echo off
chcp 936 >nul 2>nul
setlocal EnableExtensions
title 打包 WorkerRest

rem ---------------------------------------------------------------------------
rem 一键打包脚本：双击即可执行，无需在 VSCode 里选 Python 环境。
rem 硬编码系统标准 Python 3.11 解释器，绕开 VSCode 解释器选择/缓存问题。
rem ---------------------------------------------------------------------------

set "PY=D:\MyPrograms\Python\Python311\python.exe"

if not exist "%PY%" (
    echo [错误] 找不到 Python：%PY%
    echo 请确认 D:\MyPrograms\Python\Python311\python.exe 存在。
    pause
    exit /b 1
)

rem 切到脚本所在目录（scripts/ 的上一级，即项目根）
cd /d "%~dp0.."

echo [打包] 使用 Python: %PY%
echo [打包] 工作目录: %CD%
echo.

"%PY%" scripts\make_portable.py %*
set "RC=%ERRORLEVEL%"

echo.
if "%RC%"=="0" (
    echo [打包] 成功完成。
) else if "%RC%"=="2" (
    echo [打包] 未执行：当前提交没有版本 tag。
    echo        请先 git tag v主.次.修订 再重跑。
) else (
    echo [打包] 失败，退出码 %RC%。
)

echo.
pause
exit /b %RC%
