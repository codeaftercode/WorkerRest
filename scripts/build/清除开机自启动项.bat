@echo off
chcp 936 >nul 2>nul
setlocal EnableExtensions
title 清除开机自启动项 - 吃饼人也敢休息？

rem ---------------------------------------------------------------------------
rem 本软件是绿色便携版：删掉文件夹就算卸载，没有安装器、没有系统级卸载入口。
rem 唯一的残留是注册表里的「开机自启动」项（只有用户勾过才会写入），删了文件夹
rem 之后它就指向一个不存在的 exe，任务管理器「启动」页里还会留着一条。
rem 这个脚本只清这一项，别的一概不碰 —— 所以它不叫「卸载」，它只做这一件事。
rem
rem 值名必须与 sys_autostart.go 的 runValueName 一致：打包时 make_portable.py
rem 会解析源码逐字比对，改了值名却忘了改这里，打包直接失败。
rem ---------------------------------------------------------------------------
set "APPNAME=吃饼人也敢休息？"
set "VALUE=com.goodgoodlive.workerrest"
set "RUNKEY=HKCU\Software\Microsoft\Windows\CurrentVersion\Run"
set "APVKEY=HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run"
set "HIT="

echo.
echo   清除开机自启动项 —— %APPNAME%
echo   ------------------------------------------------------------
echo   这个脚本只做一件事：删掉本软件写进注册表的「开机自启动」项。
echo   它不删除任何文件，也不碰其它软件的自启动项，更不影响正在运行的程序。
echo.
echo   提示：如果软件还在运行，请先从托盘菜单「退出」再运行本脚本，
echo         否则你以后在设置里重新勾上「开机自启动」，这一项会被写回来。
echo.

call :delone "%RUNKEY%" "开机自启动项" show
call :delone "%APVKEY%" "「启动」页记录"

if not defined HIT (
  echo   没有找到自启动项：本软件当前没有开机自启动，不需要清理。
) else (
  echo   现在可以放心删除本软件所在的文件夹了，本软件不在别处留东西。
)

echo.
pause
exit /b 0

rem ---------------------------------------------------------------------------
rem 注册表里没有这一项就静默跳过；有就删，删成功才记一笔。
rem 第三个参数为 show 时先把存的值打印出来，让用户看清删的是什么。
:delone
reg query "%~1" /v "%VALUE%" >nul 2>nul
if errorlevel 1 exit /b 0
echo   找到%~2：
if /i "%~3"=="show" reg query "%~1" /v "%VALUE%" 2>nul | findstr /i /c:"%VALUE%"
echo.
reg delete "%~1" /v "%VALUE%" /f >nul 2>nul
if errorlevel 1 (
  echo   [失败] 删除%~2失败：可能被安全软件拦住了，或当前账户无权改写该项。
) else (
  echo   [完成] 已删除%~2。
  set "HIT=1"
)
echo.
exit /b 0
