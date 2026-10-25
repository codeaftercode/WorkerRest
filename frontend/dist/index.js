/*
 * 诊断控制台（index.html）的页面脚本
 * ---------------------------------------------------------------------------
 * 从 index.html 的内联 <script> 搬出来的：该页要带 CSP（default-src 'self'），
 * 内联脚本会被直接拦掉（2026-10-25 补），所以必须是同源外链。
 * 依赖 shim.js（window.api），因此引入顺序必须是 shim.js → index.js。
 */
(function () {
  "use strict";

  var log = function (t) {
    document.getElementById("log").textContent = t;
  };

  function esc(s) {
    return String(s).replace(/[&<>"]/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c];
    });
  }

  function fillEnv(state, info) {
    var rows = [
      ["引擎", (info && info.engine) || "go+wails3"],
      ["版本", (info && info.version) || "—"],
      ["督休专员数", info ? info.roles : "—"],
      ["督休专员目录", state && state.rolesRoot],
      ["配置文件", state && state.settings],
      ["运行模式", state ? (state.serverMode ? "无头服务（-tags server）" : "图形界面") : "—"],
    ];
    document.getElementById("env").innerHTML = rows
      .map(function (r) {
        return "<dt>" + esc(r[0]) + "</dt><dd>" + esc(r[1] == null ? "—" : r[1]) + "</dd>";
      })
      .join("");
  }

  function fillRoles(list) {
    var box = document.getElementById("roles");
    var pick = document.getElementById("rolePick");
    if (!list || !list.length) {
      box.innerHTML = '<div class="hint">没有扫描到督休专员目录</div>';
      return;
    }
    box.innerHTML = "";
    list.forEach(function (r) {
      var d = document.createElement("div");
      d.className = "role" + (r.enabled ? "" : " off");
      var img = document.createElement("img");
      img.alt = r.name;
      img.src = r.thumb || "";
      d.appendChild(img);
      var s = document.createElement("span");
      s.textContent = r.name;
      s.title = r.key + (r.enabled ? "" : "（已停用）");
      d.appendChild(s);
      box.appendChild(d);

      var opt = document.createElement("option");
      opt.value = r.key;
      opt.textContent = r.name + (r.enabled ? "" : "（已停用）");
      pick.appendChild(opt);
    });
  }

  (async function init() {
    var state = await window.api.getState();
    var info = await window.api.appInfo();
    fillEnv(state, info);
    if (state && state.settings && state.settings.indexOf("未找到") === 0) {
      document.getElementById("settingsHint").textContent =
        "未找到已有配置，正在使用出厂默认值（安装版/免安装版会把配置写在程序目录或 %APPDATA% 下）。";
    }
    fillRoles(await window.api.listSupervisorRoles());

    document.getElementById("short").addEventListener("click", async function () {
      log("弹出中…");
      var id = await window.api.startBreak("short");
      log(id ? "已弹出短休息：" + id : "弹出失败");
    });
    document.getElementById("long").addEventListener("click", async function () {
      log("弹出中…");
      var id = await window.api.startBreak("long");
      log(id ? "已弹出长休息：" + id : "弹出失败");
    });
    document.getElementById("preview").addEventListener("click", async function () {
      var key = document.getElementById("rolePick").value;
      if (!key) {
        log("先在左侧选一个督休专员");
        return;
      }
      var ok = await window.api.previewSupervisor(null, key);
      log(ok ? "已用「" + key + "」预览" : "预览失败：" + key);
    });
    document.getElementById("quit").addEventListener("click", function () {
      window.api.quit();
    });
  })();
})();
