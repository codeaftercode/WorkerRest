@echo off
chcp 936 >nul 2>nul
setlocal EnableExtensions
title 打包 WorkerRest

rem ---------------------------------------------------------------------------
rem 一键打包：双击即可执行，不用在 VSCode 里挑解释器。
rem 解释器选择顺序：固定路径的 Python 3.11 → py -3 启动器 → PATH 上的 python。
rem 选完一律校验 >= 3.9：本机 PATH 上的 python 可能是别的开发工具自带的 2.7，
rem 那种解释器会在第一个类型注解处报 SyntaxError，看起来像脚本本身坏了。
rem ---------------------------------------------------------------------------

set "DEFAULT_PY=D:\MyPrograms\Python\Python311\python.exe"
set PY_CMD="%DEFAULT_PY%"
set PY_ARG=

if exist "%DEFAULT_PY%" goto have_py

set PY_CMD=py
set PY_ARG=-3
where py >nul 2>nul
if not errorlevel 1 goto have_py

set PY_CMD=python
set PY_ARG=

:have_py
%PY_CMD% %PY_ARG% -c "import sys;raise SystemExit(0 if sys.version_info>=(3,9) else 1)" >nul 2>nul
if errorlevel 1 (
    echo [错误] 需要 Python 3.9 或更高版本，当前解释器不可用：
    echo        %PY_CMD% %PY_ARG%
    %PY_CMD% %PY_ARG% -V
    echo        把 DEFAULT_PY 改成可用的 Python 路径，或把它加进 PATH。
    pause
    exit /b 1
)

for /f "delims=" %%v in ('%PY_CMD% %PY_ARG% -V 2^>^&1') do set "PYVER=%%v"

rem 切到项目根：本文件在 scripts\build\ 下，往上两级才是仓库根
cd /d "%~dp0..\.."

echo [打包] 使用 Python: %PY_CMD% %PY_ARG%  ^(%PYVER%^)
echo [打包] 工作目录: %CD%
echo.

rem 用 %~dp0 拼脚本路径：不依赖当前目录，脚本再搬家也不会失效
%PY_CMD% %PY_ARG% "%~dp0make_portable.py" %*
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
