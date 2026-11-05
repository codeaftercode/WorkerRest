/**
 * Wails v3 适配层：把渲染层依赖的 window.api（上一版 预加载脚本的形状）
 * 映射到 Wails 的 Go 绑定。渲染层代码（settings.js / break.js / takeout.js /
 * prebreak.js）一行不改，只在这里换实现。
 *
 * 调用约定（v3 与 v2 的关键差别）：
 *   - v2：`window.go.main.App.Xxx(...)`，由 wails 运行时全局注入；
 *   - v3：运行时是内置模块 `/wails/runtime.js`，必须用 ES module 装入，
 *         绑定调用是**按名字** `Call.ByName("main.App.<方法>", ...args)`。
 *   本文件是传统 script，所以用动态 import()（同源，CSP default-src 'self' 放行）。
 *
 * `main.App.` 前缀对应 Go 侧 App 结构体的全限定名（package.Type.Method）。
 * 它不是运行时推断出来的，而是写死的——所以 Go 侧有契约测试（contract_test.go）
 * 与启动自检：契约里有、Go 侧没有的方法名会被点名，避免这里静默失效。
 *
 * 窗口身份的判定顺序：window.__WR_WIN → URL 的 ?win= → "settings"。
 * ⚠️ 实际生效的是 URL query：Wails v3 在 Windows 上只对内嵌 HTML 注入 WebviewWindowOptions.JS、
 *   对 URL 加载的页面不注入，所以 Go 侧各窗口都靠 URL 带 ?win= 下发身份（window.__WR_WIN
 *   这条优先级仍在，但当前 beta.28 对 URL 页面不生效）。
 * 事件：Go 端 app.Event.Emit(name, data) -> Events.On(name, ev => cb(ev.data))。
 *
 * ⚠️ 参数类型：Wails 会校验 Go 方法的参数类型，所以**不能把 null/undefined 直接传
 * 过去**（Go 侧 string 收不下 null，会 422）。下面统一用 `str()` 把空值收敛成 ""。
 */
(function () {
  var win =
    window.__WR_WIN ||
    new URLSearchParams(location.search).get("win") ||
    "settings";

  // 只加载一次运行时；加载失败时给每个调用返回空值而不是抛出，
  // 免得一个坏环境让整个页面白屏（页面里 console 仍会留下原因）。
  var runtimePromise = null;
  function runtime() {
    if (!runtimePromise) {
      runtimePromise = import("/wails/runtime.js").catch(function (err) {
        console.error("[shim] 加载 /wails/runtime.js 失败:", err);
        return null;
      });
    }
    return runtimePromise;
  }

  /** 把可能为 null/undefined 的值收敛成字符串（Wails 侧是 string 参数） */
  function str(v) {
    return v === null || v === undefined ? "" : String(v);
  }

  /** 把可能为 null/undefined 的值收敛成数组（Wails 侧是 []string 参数） */
  function arr(v) {
    return Array.isArray(v) ? v : [];
  }

  /**
   * 调一个 Go 绑定。args 是传给 Go 方法的参数（不含窗口名——窗口名由调用处补）。
   * 失败时 resolve 成 null 并打日志：渲染层对 null 都已有兜底分支。
   */
  function call(method, args) {
    var full = "main.App." + method;
    return runtime()
      .then(function (rt) {
        if (!rt) return null;
        return rt.Call.ByName.apply(rt.Call, [full].concat(args || []));
      })
      .catch(function (err) {
        console.error("[shim] 绑定调用失败 " + full + ":", err);
        return null;
      });
  }

  /** 事件订阅：回调只拿到 data，与 上一版的 ipcRenderer.on 形状一致 */
  function on(name, cb) {
    runtime().then(function (rt) {
      if (!rt) return;
      rt.Events.On(name, function (ev) {
        cb(ev && typeof ev === "object" && "data" in ev ? ev.data : ev);
      });
    });
  }

  /**
   * 尚未迁移接口的占位：返回约定的空值，保证页面加载不报错。
   * 每次首调打一条日志——避免把「占位」误当成「已实现」。
   */
  function stub(method, value) {
    var warned = false;
    return function () {
      if (!warned) {
        warned = true;
        console.warn("[shim] " + method + " 仍是占位实现（该功能尚未迁移到 Go）");
      }
      return Promise.resolve(typeof value === "object" && value !== null ? JSON.parse(JSON.stringify(value)) : value);
    };
  }

  window.api = {
    // ---- 状态 / 设置 ----
    getState: function () {
      return call("GetState");
    },
    getSettings: function () {
      return call("GetSettings");
    },
    saveSettings: function (s) {
      return call("SaveSettings", [s]);
    },
    // 枚举当前接入的显示器（设置页「显示的屏幕」多选下拉）。
    // 枚举不可用（远程会话 / 无桌面）时 Go 侧返回空数组，页面据此显示「未检测到显示器」。
    listDisplays: function () {
      return call("ListDisplays");
    },
    builtinMsgDefaults: function () {
      return call("BuiltinMsgDefaults");
    },
    // 报表导出（CSV / PNG）：name=默认文件名，b64=内容 base64；Go 侧弹另存为对话框写盘
    exportReport: function (name, b64) {
      return call("ExportReport", [str(name), str(b64)]);
    },
    resetSettings: function () {
      return call("ResetSettings");
    },

    // ---- 休息提醒窗口 ----
    breakCurrent: function () {
      return call("BreakCurrent", [win]);
    },
    action: function (name) {
      return call("Action", [win, str(name)]);
    },
    previewBreak: function (type, forceKey) {
      return call("PreviewBreak", [str(type) || "short", str(forceKey)]);
    },
    startBreak: function (type) {
      return call("StartBreak", [str(type) || "short"]);
    },
    // 设置页的「预览」：返回一次**会话对象**（不是弹窗、也不是布尔值）。
    // 设置页直接读它的 geom / lines.urge / lines.command 来铺预览面板，
    // 返回布尔值会让 fillGeomControls(undefined) 当场抛错、预览整块停摆。
    previewSupervisor: function (sup, key) {
      return call("SupervisorPreview", [sup || null, str(key)]);
    },

    // ---- 外卖窗口 ----
    takeoutCurrent: function () {
      return call("TakeoutCurrent", [win]);
    },
    previewTakeout: function () {
      return call("PreviewTakeout", [win]);
    },
    takeoutAction: function (a) {
      return call("TakeoutAction", [win, str(a)]);
    },

    // ---- 提前提示窗口（右下角小窗）：内容与版式都由 Go 侧下发 ----
    preBreakState: function () {
      return call("PreBreakState");
    },
    preBreakAction: function (a) {
      return call("PreBreakAction", [str(a)]);
    },

    // ---- 自动暂停期间的「已暂停多久」提示（右下角小窗，见 pause_notice.go）----
    // 只有两个动作：恢复（与托盘「恢复休息」同一个入口）与「只关提示、不恢复」。
    pauseNoticeResume: function () {
      return call("PauseNoticeResume");
    },
    pauseNoticeDismiss: function () {
      return call("PauseNoticeDismiss");
    },

    // ---- 督休专员目录 ----
    listSupervisorRoles: function () {
      return call("ListRoles");
    },
    pickSupervisorImage: function () {
      return call("PickSupervisorImage");
    },
    saveSupervisorRole: function (key, cfg) {
      return call("SaveRole", [str(key), cfg]);
    },
    removeSupervisorRole: function (key) {
      return call("RemoveRole", [str(key)]);
    },
    replaceSupervisorImage: function (key) {
      return call("ReplaceRoleImage", [str(key)]);
    },
    // 立绘原图（data URL）：渲染层据此生成磁贴缩略图，见 role_thumb.js
    roleFigureDataURL: function (key) {
      return call("RoleFigureDataURL", [str(key)]);
    },
    // 把生成好的缩略图（data URL）交回 Go 落盘并回写 meta.json
    saveRolePreview: function (key, dataURL) {
      return call("SaveRolePreview", [str(key), str(dataURL)]);
    },

    // ---- 声音 ----
    soundFileUrl: function (file) {
      return call("SoundFileURL", [str(file)]);
    },
    addMusicSound: function () {
      return call("AddMusicSound");
    },
    deleteMusicSound: function (file) {
      return call("DeleteMusicSound", [str(file)]);
    },
    // 随包 sounds/ 的启动体检结果（null = 没有变化，不弹窗）
    getSoundScanNotice: function () {
      return call("GetSoundScanNotice");
    },
    // 把扫到的新音频原地登记进音乐列表（不复制、不改名）
    importScannedSounds: function (files) {
      return call("ImportScannedSounds", [arr(files)]);
    },
    // 把「文件已不在」的条目从配置里删掉（只删条目，不删文件）
    dropMissingSounds: function (ids) {
      return call("DropMissingSounds", [arr(ids)]);
    },
    // 用户选了「暂不 / 保留」：本次运行不再询问
    dismissSoundScan: function () {
      return call("DismissSoundScan");
    },

    // ---- 指定应用运行时暂停 ----
    listRunningApps: function () {
      return call("ListRunningApps");
    },
    pickAppExe: function () {
      return call("PickAppExe");
    },

    // ---- 关于页 / 更新 / 手册 ----
    appInfo: function () {
      return call("AppInfo");
    },
    checkUpdate: function () {
      return call("CheckUpdate");
    },
    startUpdate: function () {
      return call("StartUpdate");
    },
    pendingUpdate: function () {
      return call("PendingUpdate");
    },
    ignoreVersion: function (version) {
      return call("IgnoreVersion", [str(version)]);
    },
    updateStatus: function () {
      return call("UpdateStatus");
    },
    // 开源鸣谢清单：整份纯文本内嵌在 exe 里（见 notices.go）。
    // 查看与另存为分开两个绑定 —— 前者只读，后者会弹另存为对话框写盘。
    getThirdPartyNotices: function () {
      return call("GetThirdPartyNotices");
    },
    exportThirdPartyNotices: function () {
      return call("ExportThirdPartyNotices");
    },
    // 项目许可证：与鸣谢清单同源同形（内嵌 + 关于页入口 + 另存为）。
    // 便携目录里不再散放 LICENSE，这两个绑定是条款唯一的出口。
    getProjectLicense: function () {
      return call("GetProjectLicense");
    },
    exportProjectLicense: function () {
      return call("ExportProjectLicense");
    },
    openExternal: function (url) {
      return call("OpenExternal", [str(url)]);
    },
    // 自更新重启恢复：前端在更新成功后调用 —— 显示设置窗口 / 上报当前 tab。
    showSettingsWindow: function () {
      return call("ShowSettingsWindow");
    },
    setActiveTab: function (tab) {
      return call("SetActiveTab", [str(tab)]);
    },

    // ---- 统计 ----
    getStats: function () {
      return call("GetStats");
    },
    getHistory: function (start, end) {
      return call("GetHistory", [str(start), str(end)]);
    },
    clearStats: function () {
      return call("ClearStats");
    },
    // 统计文件损坏已备份重建的说明（null = 没发生过，不弹窗）
    getStatsFileNotice: function () {
      return call("GetStatsFileNotice");
    },
    // 用户看过弹窗后确认，本次运行不再弹
    ackStatsFileNotice: function () {
      return call("AckStatsFileNotice");
    },

    // ---- 窗口 / 进程 ----
    closeWindow: function () {
      return call("CloseWindow", [win]);
    },
    windowMaximized: function () {
      return call("WindowMaximized");
    },
    quit: function () {
      return call("QuitApp");
    },

    // ---- 页内键鼠活动（多屏同步） ----
    supActivity: function () {
      return call("SupActivity", [win]);
    },
    onSupActivity: function (cb) {
      on("sup:active", cb);
    },

    // ---- 主进程推送 ----
    onState: function (cb) {
      on("state", cb);
    },
    onAutostart: function (cb) {
      on("autostart", cb);
    },
    onWindowMaximized: function (cb) {
      on("window:maximized-changed", cb);
    },
    onUpdateStatus: function (cb) {
      on("update:status-changed", cb);
    },
    // 统计数据变了（Go 侧每次入账后推送今天的快照）：统计报表「今天」卡据此实时刷新。
    // 回调拿到的就是 DayStats（字段与 GetStats().today 相同）。
    onStatsChanged: function (cb) {
      on("stats:changed", cb);
    },
    // 设置窗口被隐藏（关闭到托盘 / 切换）时前端退出所有提醒内容的编辑态
    onSettingsHidden: function (cb) {
      on("settings:hidden", cb);
    },
  };

  // 桌面应用：全局禁用浏览器默认右键菜单。
  // 立绘磁贴的自定义右键菜单（settings.js）已自行 stopPropagation，不会冒泡到此处，不受影响。
  document.addEventListener("contextmenu", function (e) {
    e.preventDefault();
  });

  // 冒烟/排查用标记：确认 shim 已生效（同时也是「这页跑在 Go 后端上」的判据）
  window.__WR_SHIM = "go";
})();
