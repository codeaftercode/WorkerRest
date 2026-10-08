/**
 * 探针专用 preload：模拟 Go 侧创建窗口时注入的 window.__WR_WIN。
 *
 * 为什么需要它：shim.js 的窗口身份优先级是
 *   window.__WR_WIN → URL 的 ?win= → "settings"
 * 真实运行时由 Go 的 WebviewWindowOptions.JS 注入。探针是用 Playwright 驱动的
 * 系统 Edge 打开同一个页面的，不走 Wails 的建窗路径，所以得自己把这一句补上——
 * 而且必须**在页面脚本之前**执行（renderer_probe.js 用 addInitScript 加载本文件，
 * 正好是这个时机），否则 shim 已经按默认值定下了身份。
 *
 * 只有这一个目的：让「窗口身份注入 → shim 认到身份」这条链路可被断言。
 */
window.__WR_WIN = "settings";

