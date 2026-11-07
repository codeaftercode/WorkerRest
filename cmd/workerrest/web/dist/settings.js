"use strict";
(() => {
  // 本地唯一 id：16 字节随机数的十六进制串（32 位）。只在本列表内做定位用，
  // 不用平台自带的加密级随机 id（老内核没这个接口，这里也不需要标准格式）。
  function newId() {
    const b = new Uint8Array(16);
    for (let i = 0; i < b.length; i++) b[i] = (Math.random() * 256) | 0;
    try {
      if (window.crypto && window.crypto.getRandomValues) window.crypto.getRandomValues(b);
    } catch {
      // 退化到上面的 Math.random 已经够用
    }
    let s = "";
    for (let i = 0; i < b.length; i++) s += b[i].toString(16).padStart(2, "0");
    return s;
  }
  // 历史源码/types.ts。2026-10-28 用户要求：默认高度 80%、距左 5%、贴底，
  // 与 Go 侧 role_write.go 的 supGeomDefault 必须同值（内置 meta.json 也一律这个值）
  var SUP_GEOM_DEFAULT = { sizePct: 80, posXPct: 5, posYPct: 0, flip: false };
  var SUP_GEOM_LIMITS = {
    sizePct: [20, 100],
    // 距左上限 30%：再往右立绘会挤掉右侧台词的位置（右侧内容要摆在“督休专员右边缘 ↔ 窗口右边缘”中间）
    posXPct: [0, 30],
    posYPct: [0, 40]
  };
  function normalizeSupGeom(raw) {
    const pick = (k) => {
      const [min, max] = SUP_GEOM_LIMITS[k];
      const v = raw ? raw[k] : void 0;
      return typeof v === "number" && Number.isFinite(v) ? Math.min(max, Math.max(min, Math.round(v))) : SUP_GEOM_DEFAULT[k];
    };
    return {
      sizePct: pick("sizePct"),
      posXPct: pick("posXPct"),
      posYPct: pick("posYPct"),
      // 只认真正的布尔 true：老配置没有这个字段，一律按"不翻转"
      flip: raw?.flip === true
    };
  }
  var SUPERVISOR_SELECTIONS = [
    { value: "fixed", label: "固定显示", hint: "每次提醒都用同一个立绘；点选立绘前的单选框指定" },
    { value: "random", label: "随机显示", hint: "每次提醒在已勾选的立绘中随机选一个" },
    { value: "sequence", label: "顺序循环", hint: "在已勾选的立绘间按列表顺序轮换" }
  ];
  var WEEKDAYS = [
    { value: 1, label: "周一", short: "一" },
    { value: 2, label: "周二", short: "二" },
    { value: 3, label: "周三", short: "三" },
    { value: 4, label: "周四", short: "四" },
    { value: 5, label: "周五", short: "五" },
    { value: 6, label: "周六", short: "六" },
    { value: 7, label: "周日", short: "日" }
  ];
  var DEFAULT_WEEKDAYS = [1, 2, 3, 4, 5];
  function isoWeekday(d) {
    const w = d.getDay();
    return w === 0 ? 7 : w;
  }
  // 日期键（YYYY-MM-DD，本地日）→ “周四”。解析失败返回空串，调用方自己决定退化成什么。
  // 不用 new Date("2026-10-22")：那是按 UTC 解析的，东八区会退到前一天。
  function weekdayLabelOf(dateKey) {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(dateKey || ""));
    if (!m) return "";
    const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
    const hit = WEEKDAYS.find((w) => w.value === isoWeekday(d));
    return hit ? hit.label : "";
  }
  function nextSlotAt(slots, from = Date.now()) {
    let best = null;
    for (const slot of slots) {
      if (!slot.enabled) continue;
      for (let d = 0; d < 8; d++) {
        const day = new Date(from);
        day.setHours(0, 0, 0, 0);
        day.setDate(day.getDate() + d);
        if (slot.repeat === "weekly" && !slot.days.includes(isoWeekday(day))) continue;
        const at = new Date(day);
        at.setHours(slot.hour, slot.minute, 0, 0);
        if (at.getTime() <= from) continue;
        if (!best || at.getTime() < best.at) best = { at: at.getTime(), slot };
        break;
      }
    }
    return best;
  }
  // 外卖两项的默认值（区间不在这里：一律读输入框自己的 min/max，见 clampNum）
  var TAKEOUT_LEAD_DEFAULT = 0;
  var TAKEOUT_POSTPONE_DEFAULT = 10;
  // 只收“打开它基本就是在开会”的客户端（口径与 settings.go 的 appPausePresets 一致）。
  // 飞书 / 钉钉 / 企业微信 / WeLink 是综合办公套件：能开会，但日常是消息与审批入口，
  // 进程常驻并不等于在开会，所以不入列。
  var APP_PAUSE_PRESETS = [
    { exe: "WeMeetApp.exe", label: "腾讯会议" },
    { exe: "ms-teams.exe", label: "Microsoft Teams" },
    { exe: "Zoom.exe", label: "Zoom" },
    { exe: "CiscoCollabHost.exe", label: "Webex" }
  ];
  function appPauseLabel(exe) {
    const k = String(exe ?? "").trim().toLowerCase();
    return APP_PAUSE_PRESETS.find((p) => p.exe.toLowerCase() === k)?.label ?? "";
  }
  var APP_NAME_MAX = 30;
  function normalizeAppName(raw) {
    return String(raw ?? "").replace(/\s+/g, " ").trim().slice(0, APP_NAME_MAX);
  }
  function syncAppName(name, fromExe, toExe) {
    const cur = normalizeAppName(name);
    if (cur && cur !== appPauseLabel(fromExe)) return cur;
    return appPauseLabel(toExe);
  }
  function exeBasename(raw) {
    const s = String(raw ?? "").trim();
    if (!s) return "";
    const seg = s.split(/[\\/]/).filter(Boolean).pop() ?? "";
    return seg.trim().slice(0, 120);
  }
  var BREAK_MODES = [
    { value: "countdown", label: "倒计时", hint: "每隔一段时间提醒一次" },
    { value: "fixed", label: "固定时间", hint: "到设定的时刻提醒" }
  ];
  var CUSTOM_BREAK_PREFIX = "cb-";
  // “暂停休息时”下拉：三种处置，默认“推迟提醒”。每一项都带一个“?”，悬停看该项
  // 的具体行为；行标签上的“?”解释“暂停休息”包括哪几种情况（空闲分钟数动态取，
  // 跟着时间规划页的“空闲 N 分钟后自动暂停”走，每次保存后刷新一遍）。
  var PAUSE_ACTIONS = [
    { value: "remind", label: "正常提醒",
      hint: "暂停休息期间到了提醒时间，仍照常弹出提前提示小窗和提醒窗口，不跳过也不推迟。" },
    { value: "postpone", label: "推迟提醒",
      hint: "暂停休息期间到了提醒时间不弹窗，按本卡“可推迟（分钟）”填的时长往后推；推迟后到点若仍在暂停休息，就继续往后推，恢复后按推迟后的时刻弹出。" },
    { value: "skip", label: "跳过提醒",
      hint: "暂停休息期间到了提醒时间，不弹提前提示小窗、也不弹提醒窗口，直接按“跳过”记一次，计入统计报表的跳过。" }
  ];
  // 下拉实例按宿主元素记（WeakMap）：自定义提醒卡片每次重建都换新元素，
  // 用 id 记会指到已经脱离文档的旧节点。
  var PAUSE_DROPS = new WeakMap();
  function pauseActionOf(v) {
    return PAUSE_ACTIONS.some((a) => a.value === v) ? v : "postpone";
  }
  // “暂停休息”的范围说明（挂在“暂停休息时”标签的“?”上）。只改措词、口径不变：
  // 列的是**所有可能**导致暂停的情况，勾没勾选都照列（和以前列“勿扰”“应用占用”一致），
  // 具体哪几条在你的机器上生效，看“自动暂停”卡片里各开关的状态。
  function pauseScopeHint() {
    const n = Math.max(1, Math.round(Number(S && S.idlePauseMin) || 5));
    return "“暂停休息”指：系统空闲 " + n +
      " 分钟自动暂停，勿扰/专注、全屏/演示、无线投放、复制投影、指定应用运行时自动暂停，" +
      "以及手动暂停休息。";
  }
  function refreshPauseHints() {
    const text = pauseScopeHint();
    for (const el of document.querySelectorAll(".pause-scope-help")) el.title = text;
  }
  function mkQMark(cls, title) {
    const q = document.createElement("span");
    q.className = "q-mark " + cls;
    q.textContent = "?";
    q.title = title;
    return q;
  }
  // 自绘下拉：“暂停休息时”与“触发方式”共用这一套（外观见 settings.css 的 .cdrop）。
  // 原生 <select> 的选项由浏览器绘制，缩进改不了、也放不下每项的“?”，所以自己画。
  // items = [{value,label,hint}]，def = 取值非法时的兜底项，onPick = 用户选中后的回调。
  // name = 下拉的可访问名（兄弟 <label> 是纯视觉文字、不带 for，读屏念不到），
  //        挂在按钮的 aria-label 上；缺省空串表示没有独立名字（调用方应传入）。
  // 实例按宿主元素记（WeakMap，见 PAUSE_DROPS）：自定义提醒卡每次重建都换新元素。
  function mountDrop(host, items, value, def, onPick, name) {
    if (!host) return null;
    // 宿主宽度来自 .cdrop-host（180px，与输入框同列同宽）：静态那处写在 HTML 里，
    // JS 新建的卡片这里统一补上，免得下拉铺满整列。
    host.classList.add("cdrop-host");
    const exists = PAUSE_DROPS.get(host);
    if (exists) {
      exists.set(value);
      return exists;
    }
    const norm = (v) => (items.some((i) => i.value === v) ? v : def || items[0].value);
    host.innerHTML = "";
    const root = document.createElement("div");
    root.className = "cdrop";
    root.dataset.value = norm(value);
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "cdrop-btn";
    btn.setAttribute("aria-haspopup", "listbox");
    btn.setAttribute("aria-expanded", "false");
    if (name) btn.setAttribute("aria-label", name);
    const cur = document.createElement("span");
    cur.className = "cdrop-cur";
    const caret = document.createElement("span");
    caret.className = "cdrop-caret";
    caret.textContent = "▾";
    btn.append(cur, mkQMark("", ""), caret);
    const menu = document.createElement("div");
    menu.className = "cdrop-menu hidden";
    menu.setAttribute("role", "listbox");
    for (const opt of items) {
      const item = document.createElement("button");
      item.type = "button";
      item.className = "cdrop-item";
      item.dataset.value = opt.value;
      item.setAttribute("role", "option");
      const label = document.createElement("span");
      label.className = "cdrop-label";
      label.textContent = opt.label;
      // 选中标识由 CSS 的 ::before 竖条承担（不再插勾选列，避免把文字顶离左缘）
      item.append(label, mkQMark("", opt.hint));
      item.addEventListener("click", () => {
        api.set(opt.value);
        close();
        if (onPick) onPick(opt.value);
      });
      menu.appendChild(item);
    }
    root.append(btn, menu);
    host.appendChild(root);

    let outside = null;
    function focusItem(which) {
      // 键盘/点击都要落到同一个“当前项”，焦点随选中项走；菜单收起时给回按钮。
      const el = which && which.isConnected ? which : menu.children[0];
      if (el) el.focus();
    }
    function open(focus) {
      root.classList.add("open");
      menu.classList.remove("hidden");
      btn.setAttribute("aria-expanded", "true");
      outside = (e) => { if (!root.contains(e.target)) close(); };
      document.addEventListener("pointerdown", outside, true);
      if (focus) {
        const idx = Array.from(menu.children).findIndex(
          (it) => it.dataset.value === root.dataset.value);
        focusItem(menu.children[idx >= 0 ? idx : 0]);
      }
    }
    function close() {
      root.classList.remove("open");
      menu.classList.add("hidden");
      btn.setAttribute("aria-expanded", "false");
      if (outside) {
        document.removeEventListener("pointerdown", outside, true);
        outside = null;
      }
    }
    function render() {
      const opt = items.find((a) => a.value === root.dataset.value) || items[0];
      cur.textContent = opt.label;
      btn.querySelector(".q-mark").title = opt.hint || "";
      // 宿主上也留一份取值：探针 / 外部脚本按 dataset.value 读，代替原生 select 的 .value
      host.dataset.value = opt.value;
      for (const item of menu.children) {
        const on = item.dataset.value === opt.value;
        item.setAttribute("aria-selected", on ? "true" : "false");
      }
    }
    // 键盘导航（listbox 语义）：按钮上 Enter/Space/↑/↓ 展开，菜单内 ↑/↓/Home/End 移动焦点、
    // Enter/Space 选中、Escape 关闭回按钮、Tab 关闭让焦点自然移出。
    function moveTo(e, dir) {
      const all = Array.from(menu.children);
      const i = all.indexOf(document.activeElement);
      const next = all[(i < 0 ? -1 : i) + dir];
      if (next) { e.preventDefault(); next.focus(); }
    }
    btn.addEventListener("click", (e) => {
      e.stopPropagation();
      if (root.classList.contains("open")) close(); else open(true);
    });
    btn.addEventListener("keydown", (e) => {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        open(true);
        if (e.key === "ArrowUp") {
          const all = Array.from(menu.children);
          (all[all.length - 1] || menu.children[0]).focus();
        }
      } else if (e.key === "Escape") {
        close();
      }
    });
    menu.addEventListener("keydown", (e) => {
      if (e.key === "ArrowDown") moveTo(e, 1);
      else if (e.key === "ArrowUp") moveTo(e, -1);
      else if (e.key === "Home") { e.preventDefault(); menu.children[0].focus(); }
      else if (e.key === "End") { e.preventDefault(); menu.children[menu.children.length - 1].focus(); }
      else if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        const act = document.activeElement;
        if (act && act.dataset && act.dataset.value != null) {
          root.dataset.value = act.dataset.value;
          render();
          close();
          btn.focus();
          if (onPick) onPick(act.dataset.value);
        }
      } else if (e.key === "Escape") {
        close();
        btn.focus();
      } else if (e.key === "Tab") {
        close();
      }
    });
    const api = {
      get: () => root.dataset.value,
      set: (v) => { root.dataset.value = norm(v); render(); }
    };
    PAUSE_DROPS.set(host, api);
    render();
    return api;
  }
  // ---- “显示的屏幕”多选下拉 ----
  //
  // 与上面 mountDrop 的关键差别：单选点一下就收起菜单，这里要能连续勾选，所以点击只切换、不关闭。
  //
  // 取值口径与 Go 侧 display.Resolve 严格对齐：**勾选结果是具体 ID 的快照**。
  // 刻意不提供“全部”这种动态语义 —— 让新接入的屏自动进入显示范围，等于用户接上投影仪
  // 的那一刻提醒就弹到投影上，而投影仪恰恰是最不该弹的那块屏。想多屏显示请自己勾，
  // 插了新屏也请自己回来补勾（面板底部会写出当前检测到几块）。
  // 空数组不是“全部”，而是“还没挑过”→ Go 侧兜底主屏，这里也按主屏显示勾选态。
  var displayDrop = null;   // { get, setValue }
  var displayCache = [];    // 最近一次枚举结果

  function displayItems(list) {
    return list.map(function (d) {
      return {
        value: d.id,
        label: d.label || "显示器",
        hint: d.width + "×" + d.height + (d.isPrimary ? " · 主屏" : ""),
        isPrimary: !!d.isPrimary,
      };
    });
  }

  // 现场枚举一次显示器，返回下拉要的 items。
  //
  // **枚举时机只有三个**：设置页加载完、点下拉里的“刷新”、恢复默认设置之后。
  // 也就是说软件启动之后再插上或拔掉显示器，面板里的选项会一直是旧的，直到用户自己点刷新。
  // 不自动刷新的取舍：勾选结果要落盘，选项在用户低头看面板时自己增减，容易点到一块刚消失的屏；
  // 而“插了新屏没看见”有一个显式按钮可以补救，比猜时机更可控。
  //
  // 枚举失败（远程会话 / 无桌面 / 驱动未就绪）返回空数组 —— 调用方据此显示“未检测到显示器”，
  // 但**绝不清空配置**：清空等于把“只显示副屏”悄悄改成“所有屏”。
  async function fetchDisplays() {
    let list = [];
    try {
      list = await window.api.listDisplays();
    } catch (err) {
      console.warn("[settings] 枚举显示器失败:", err);
    }
    displayCache = Array.isArray(list) ? list : [];
    return displayItems(displayCache);
  }

  function mountMultiDrop(host, items, selected, onChange, name, reload) {
    if (!host) return null;
    host.classList.add("cdrop-host");
    host.innerHTML = "";
    var allIds = items.map(function (i) { return i.value; });
    // 配置里的快照。用户没动过时原样回写：设置页别因为改了别的项就把这块重写一遍
    var original = (selected || []).slice();
    var picked = new Set();
    var touched = false;

    var root = document.createElement("div");
    root.className = "cdrop cdrop-multi";
    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "cdrop-btn";
    btn.setAttribute("aria-haspopup", "listbox");
    btn.setAttribute("aria-expanded", "false");
    if (name) btn.setAttribute("aria-label", name);
    var cur = document.createElement("span");
    cur.className = "cdrop-cur";
    var caret = document.createElement("span");
    caret.className = "cdrop-caret";
    caret.textContent = "▾";
    btn.append(cur, caret);

    var menu = document.createElement("div");
    menu.className = "cdrop-menu hidden";
    // listbox 只包住选项本身：底部的“刷新”行不能算选项，否则读屏会把它念成一块屏，
    // 键盘上下键也会停到它身上。
    var optsBox = document.createElement("div");
    optsBox.className = "cdrop-opts";
    optsBox.setAttribute("role", "listbox");
    optsBox.setAttribute("aria-multiselectable", "true");
    var empty = document.createElement("div");
    empty.className = "cdrop-empty hidden";
    empty.textContent = "未检测到显示器";
    var foot = document.createElement("div");
    foot.className = "cdrop-foot";
    var note = document.createElement("span");
    note.className = "cdrop-foot-note";
    var refresh = document.createElement("button");
    refresh.type = "button";
    refresh.className = "cdrop-refresh";
    refresh.setAttribute("aria-label", "重新检测显示器");
    refresh.title = "重新检测显示器：新接上的屏、拔掉的屏都会在这里更新";
    var refreshIcon = document.createElement("span");
    refreshIcon.className = "cdrop-refresh-icon";
    refreshIcon.textContent = "↻";
    var refreshLabel = document.createElement("span");
    refreshLabel.textContent = "刷新";
    refresh.append(refreshIcon, refreshLabel);
    foot.append(note, refresh);
    menu.append(optsBox, empty, foot);
    root.append(btn, menu);
    host.appendChild(root);

    function optEls() {
      return Array.prototype.slice.call(optsBox.querySelectorAll("[role='option']"));
    }
    // 当前“算数”的选择：没动过就是配置原值（可能含已拔掉的屏），动过就是面板上的勾选。
    // 已经拔掉的屏保留在配置里 —— 面板上看不见的东西，不该被一次“顺手保存”悄悄抹掉；
    // 接回去时它还在，正好符合“勾选是快照”的语义。
    function effective() {
      if (!touched) return original.slice();
      var absent = original.filter(function (id) { return allIds.indexOf(id) < 0; });
      return Array.from(picked).concat(absent);
    }
    function value() { return effective(); }
    function labelOf(id) {
      var hit = items.filter(function (i) { return i.value === id; })[0];
      return hit ? hit.label : "1 块屏幕";
    }
    function isOn(id) { return picked.has(id); }
    function summary() {
      if (allIds.length === 0) return "未检测到显示器";
      if (picked.size === 1) return labelOf(picked.values().next().value);
      return picked.size + " 块屏幕";
    }
    function renderNote() {
      if (allIds.length === 0) {
        note.textContent = "点“刷新”重新检测";
        note.title = "枚举不到显示器：远程桌面、无桌面会话，或显卡驱动未就绪";
        return;
      }
      var missing = effective().filter(function (id) { return allIds.indexOf(id) < 0; });
      note.textContent = "检测到 " + allIds.length + " 块" +
        (missing.length ? "，勾选的 " + missing.length + " 块未接入" : "");
      note.title = missing.length
        ? "已勾选但当前没接上的屏：" + missing.join("、") +
          "（脚本层面会忽略它，接回来后点一次刷新即可）"
        : "列表来自最近一次检测。接了新屏或拔了屏，点右边“刷新”";
    }
    function render() {
      cur.textContent = summary();
      var opts = optEls();
      for (var i = 0; i < opts.length; i++) {
        opts[i].setAttribute("aria-selected", isOn(opts[i].dataset.value) ? "true" : "false");
      }
      // 探针按 dataset.value 读（JSON 数组），代替原生 select 的 .value
      host.dataset.value = JSON.stringify(value());
      renderNote();
    }

    function buildOption(opt) {
      var item = document.createElement("button");
      item.type = "button";
      item.className = "cdrop-item";
      item.dataset.value = opt.value;
      item.setAttribute("role", "option");
      var box = document.createElement("span");
      box.className = "cdrop-box";
      var label = document.createElement("span");
      label.className = "cdrop-label";
      label.textContent = opt.label;
      item.append(box, label);
      if (opt.hint) {
        var hint = document.createElement("span");
        hint.className = "cdrop-hint";
        hint.textContent = opt.hint;
        item.append(hint);
      }
      item.addEventListener("click", function () { toggle(opt.value); });
      return item;
    }
    function renderOptions() {
      optsBox.innerHTML = "";
      for (var n = 0; n < items.length; n++) optsBox.appendChild(buildOption(items[n]));
      empty.classList.toggle("hidden", items.length > 0);
      render();
    }

    // 按当前 items 重算勾选：认不得的 ID（换过接口 / 屏被拔了）直接忽略，一块都不剩时兜底主屏
    // —— 与 Go 的 Resolve 同口径（空 / 全失配 = 主屏，绝不是“所有屏”）。
    function syncSelection() {
      var basis = effective();
      var next = new Set();
      for (var k = 0; k < basis.length; k++) {
        if (allIds.indexOf(basis[k]) >= 0) next.add(basis[k]);
      }
      if (next.size === 0) {
        var pd = items.filter(function (i) { return i.isPrimary; })[0] || items[0];
        if (pd) next.add(pd.value);
      }
      picked = next;
    }
    function toggle(id) {
      if (picked.has(id)) {
        if (picked.size === 1) {
          // 全不勾 = 提醒窗口一块屏都不弹，用户只会以为软件坏了
          toast("至少要保留一块屏幕。想彻底不弹提醒，请去关上面的提醒开关");
          return;
        }
        picked.delete(id);
      } else {
        picked.add(id);
      }
      // 不做“勾满就归一化成全部”：勾选结果就是快照，插新屏不该被自动带上
      touched = true;
      render();
      // 落盘值就是新的“已配置”基准：刷新时靠它判断哪几块已勾选但当前没接上。
      // 不同步的话基准一直停在“刚进这一页那一刻”的配置上，第一次勾选之后
      // 新勾上的屏被拔掉就永远算不进“未接入”，也会被下一次刷新从配置里漏掉。
      original = value().slice();
      if (onChange) onChange(value());
    }

    // “刷新”：重新枚举后就地替换选项（面板不关、能保住的勾选都保住）。
    // 刷新本身**不写配置**：没动过的勾选原样回写，动过的等下一次 autoSave。
    var busy = false;
    function refreshToast(beforeIds) {
      if (allIds.length === 0) return "未检测到显示器（远程桌面或显卡未就绪？）";
      var added = 0;
      var removed = 0;
      var i;
      for (i = 0; i < allIds.length; i++) {
        if (beforeIds.indexOf(allIds[i]) < 0) added++;
      }
      for (i = 0; i < beforeIds.length; i++) {
        if (allIds.indexOf(beforeIds[i]) < 0) removed++;
      }
      var tail = [];
      if (added) tail.push("新增 " + added + " 块");
      if (removed) tail.push("移除 " + removed + " 块");
      return "已检测到 " + allIds.length + " 块屏幕" +
        (tail.length ? "（" + tail.join("，") + "）" : "");
    }
    async function doRefresh() {
      if (!reload || busy) return;
      busy = true;
      // 用 class + aria-busy 而不是 disabled：disabled 会把焦点从按钮上踢掉，
      // 面板的 focusout 逻辑随即判定“焦点离开了”，刚点的刷新反而把面板关了。
      refresh.classList.add("busy");
      refresh.setAttribute("aria-busy", "true");
      var beforeIds = allIds.slice();
      try {
        var next = await reload();
        if (Array.isArray(next)) {
          items = next;
          allIds = items.map(function (i) { return i.value; });
          syncSelection();
          renderOptions();
          toast(refreshToast(beforeIds));
        }
      } finally {
        busy = false;
        refresh.classList.remove("busy");
        refresh.removeAttribute("aria-busy");
      }
    }
    refresh.addEventListener("click", function () { void doRefresh(); });

    var outside = null;
    function open() {
      root.classList.add("open");
      menu.classList.remove("hidden");
      btn.setAttribute("aria-expanded", "true");
      outside = function (e) { if (!root.contains(e.target)) close(); };
      document.addEventListener("pointerdown", outside, true);
      var first = optEls()[0] || refresh;
      if (first) first.focus();
    }
    function close() {
      root.classList.remove("open");
      menu.classList.add("hidden");
      btn.setAttribute("aria-expanded", "false");
      if (outside) {
        document.removeEventListener("pointerdown", outside, true);
        outside = null;
      }
    }
    function moveTo(e, dir) {
      var all = optEls();
      var i = all.indexOf(document.activeElement);
      var next = all[(i < 0 ? -1 : i) + dir];
      if (next) { e.preventDefault(); next.focus(); }
    }
    btn.addEventListener("click", function (e) {
      e.stopPropagation();
      if (root.classList.contains("open")) close(); else open();
    });
    btn.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        open();
      } else if (e.key === "Escape") {
        close();
      }
    });
    menu.addEventListener("keydown", function (e) {
      var act = document.activeElement;
      if (e.key === "ArrowDown") moveTo(e, 1);
      else if (e.key === "ArrowUp") moveTo(e, -1);
      else if (e.key === "Home") {
        var head = optEls()[0];
        if (head) { e.preventDefault(); head.focus(); }
      } else if (e.key === "End") {
        var tail = optEls().pop();
        if (tail) { e.preventDefault(); tail.focus(); }
      } else if (e.key === "Enter" || e.key === " ") {
        // “刷新”是普通按钮，交给它自己处理：这里 preventDefault 会把键盘激活一起吞掉
        if (act === refresh) return;
        // 多选不关菜单：勾完一块接着勾下一块是常态
        e.preventDefault();
        if (act && act.dataset && act.dataset.value != null) toggle(act.dataset.value);
      } else if (e.key === "Escape") {
        close();
        btn.focus();
      }
    });
    // 焦点整体离开面板才收起。用 setTimeout 而不是直接看 relatedTarget：点面板空白处时
    // relatedTarget 是 null，但焦点其实还留在面板里（document.activeElement 仍是按钮）。
    // 这样 Tab 也能走到“刷新”上，真正退出面板时才关。
    root.addEventListener("focusout", function () {
      setTimeout(function () {
        if (root.contains(document.activeElement)) return;
        close();
      }, 0);
    });

    var api = {
      get: value,
      setValue: function (v) { original = (v || []).slice(); touched = false; syncSelection(); renderOptions(); },
    };
    syncSelection();
    renderOptions();
    return api;
  }

  // 拉一次显示器列表并挂上多选下拉。枚举为空时触发器显示“未检测到显示器”，
  // 但面板里的“刷新”照样能用 —— 插好屏之后不必重启软件。
  async function refreshDisplayDrop() {
    const host = $("ap-displays");
    if (!host) return;
    const items = await fetchDisplays();
    // 勾一下就落盘（同卡片里其它控件的 autoSave 节奏）；多选不关菜单，连勾几块会存几次，
    // 每次都是整份配置，开销可以忽略
    displayDrop = mountMultiDrop(host, items, S.displayIds || [], function () { void autoSave(); }, "显示的屏幕", fetchDisplays);
  }

  // “暂停休息时”：选中即存盘（原生 select 的 change 原来由 bindAutoSaveEl 兜着）。
  function mountPauseDrop(host, value) {
    return mountDrop(host, PAUSE_ACTIONS, pauseActionOf(value), "postpone", () => { void autoSave(); }, "暂停休息时");
  }
  // “触发方式”：切换后联动显示“间隔”/“提醒时间”，再重算摘要并存盘。
  function mountBreakModeDrop(id) {
    const host = $(`${id}-mode`);
    if (!host) return null;
    const c = breakCfg(id);
    return mountDrop(host, BREAK_MODES, c && c.mode, "countdown", (v) => {
      const cfg = breakCfg(id);
      if (!cfg) return;
      cfg.mode = v === "fixed" ? "fixed" : "countdown";
      applyBreakModeUI();
      updateBreakSummary(id);
      void autoSave();
    }, "触发方式");
  }
  function dropValue(host, def) {
    const api = host ? PAUSE_DROPS.get(host) : null;
    return api ? api.get() : def;
  }
  function setDropValue(host, value) {
    const api = host ? PAUSE_DROPS.get(host) : null;
    if (api) api.set(value);
  }
  function defaultCustomBreakConfig() {
    return {
      enabled: true,
      mode: "countdown",
      intervalMs: 10 * 6e4,
      durationMs: 2e4,
      notifyBeforeMs: 1e4,
      postponeMs: 2 * 6e4,
      // 暂停休息期间到点：默认推迟（按“可推迟”时长往后推，仍暂停就继续推）
      pauseAction: "postpone",
      slots: []
    };
  }
  function breakEntries(s) {
    const out = [
      { id: "short", name: "短休息", config: s.short },
      { id: "long", name: "长休息", config: s.long }
    ];
    for (const c of s.customBreaks ?? []) out.push({ id: c.id, name: c.name, config: c.config });
    return out;
  }
  function breakConfigById(s, id) {
    const e = breakEntries(s).find((x) => x.id === id);
    return e ? e.config : null;
  }
  function pauseReasonText(reason) {
    switch (reason) {
      case "idle":
        return "系统空闲";
      case "manual":
        return "手动暂停";
      default:
        return reason;
    }
  }

  // 历史源码/fixedTime.ts
  var pad2 = (n) => String(n).padStart(2, "0");
  function slotTimeText(slot) {
    return `${pad2(slot.hour)}:${pad2(slot.minute)}`;
  }
  function describeTimeSlot(slot) {
    const time = slotTimeText(slot);
    if (slot.repeat === "daily") return `每天 ${time}`;
    const names = WEEKDAYS.filter((d) => slot.days.includes(d.value)).map((d) => d.short);
    return `每周 ${names.join("、") || "（未选）"} ${time}`;
  }

  // renderer/figureTrim.ts
  var ALPHA_MIN = 8;
  var MAX_SAMPLE = 2048;
  var IGNORE = 8e-3;
  var MIN_CONTENT = 0.25;
  var MAX_SCALE = 4;
  var NONE = { left: 0, right: 0, top: 0, bottom: 0, content: 1, scale: 1, transform: "none" };
  function norm(v) {
    return v < IGNORE ? 0 : v;
  }
  function measureFigureTrim(img) {
    const w = img.naturalWidth;
    const h = img.naturalHeight;
    if (!w || !h) return NONE;
    const s = Math.min(1, MAX_SAMPLE / Math.max(w, h));
    const cw = Math.max(1, Math.round(w * s));
    const ch = Math.max(1, Math.round(h * s));
    const cv = document.createElement("canvas");
    cv.width = cw;
    cv.height = ch;
    const ctx = cv.getContext("2d", { willReadFrequently: true });
    if (!ctx) return NONE;
    ctx.clearRect(0, 0, cw, ch);
    ctx.drawImage(img, 0, 0, cw, ch);
    let px;
    try {
      px = ctx.getImageData(0, 0, cw, ch).data;
    } catch {
      return NONE;
    }
    let top = -1;
    let bottom = -1;
    let left = cw;
    let right = -1;
    for (let y = 0; y < ch; y++) {
      const row = y * cw * 4;
      let has = false;
      for (let x = 0; x < cw; x++) {
        if (px[row + x * 4 + 3] >= ALPHA_MIN) {
          has = true;
          if (x < left) left = x;
          if (x > right) right = x;
        }
      }
      if (has) {
        if (top < 0) top = y;
        bottom = y;
      }
    }
    if (bottom < 0 || right < 0) return NONE;
    const out = {
      left: norm(left / cw),
      right: norm((cw - 1 - right) / cw),
      top: norm(top / ch),
      bottom: norm((ch - 1 - bottom) / ch),
      // 未过 IGNORE 阈值的原始边距（摆位锚点专用，与 break/takeout 同一套）：
      // IGNORE 会把 <0.8% 的边距归零，对变换无害，但锚点必须指向像素真实位置。
      rawLeft: left / cw,
      rawRight: (cw - 1 - right) / cw,
      rawTop: top / ch,
      rawBottom: (ch - 1 - bottom) / ch,
      content: 1,
      scale: 1,
      transform: "none"
    };
    out.content = 1 - out.top - out.bottom;
    if (out.content < MIN_CONTENT) return NONE;
    return out;
  }
  var measured = /* @__PURE__ */ new WeakMap();
  function applyFigureTrim(host, img, flip = false) {
    host.style.transformOrigin = "left bottom";
    const run = () => {
      host.style.transform = "none";
      const t = measureFigureTrim(img);
      const left = flip ? t.right : t.left;
      const right = flip ? t.left : t.right;
      // ⚠️ fill 必须按**布局盒**算，不能读 img 的 getBoundingClientRect：img 自身
      // 可能带着 Stage 变换（scale(1.03) scaleY(1.006)，纵向 1.03618 > 横向 1.03），
      // 立绘常在变换生效之后才加载完——量到变换后尺寸会把 fill 压成 0.994（应 1.0），
      // trim 平移量整体偏小、预览摆位一起算歪。host 变换已重置为 none，其 rect 即布局盒。
      const rendered = host.getBoundingClientRect();
      const scale = t.content >= MIN_CONTENT ? Math.min(MAX_SCALE, 1 / t.content) : 1;
      const ar = img.naturalHeight > 0 ? img.naturalWidth / img.naturalHeight : 0;
      const fill = rendered.height > 0 && ar > 0 ? Math.min(1, rendered.width / ar / rendered.height) : 1;
      const needTrim = left > 0 || t.top > 0 || t.bottom > 0;
      const transform = needTrim ? `translate(${(-left * scale * 100).toFixed(3)}%, ${(t.bottom * scale * fill * 100).toFixed(3)}%) scale(${scale.toFixed(4)})` : "none";
      host.style.transform = transform;
      host.style.visibility = "visible";
      img.dataset.trim = JSON.stringify({
        left: +left.toFixed(4),
        right: +right.toFixed(4),
        top: +t.top.toFixed(4),
        bottom: +t.bottom.toFixed(4),
        // 像素真实边距（未过 IGNORE）：摆位锚点用，语义与 left/right 一致（含 flip 交换）
        rawLeft: +(flip ? t.rawRight : t.rawLeft).toFixed(4),
        rawRight: +(flip ? t.rawLeft : t.rawRight).toFixed(4),
        rawTop: +t.rawTop.toFixed(4),
        rawBottom: +t.rawBottom.toFixed(4),
        scale: +scale.toFixed(4),
        fill: +fill.toFixed(4),
        flip,
        transform
      });
    };
    const cacheKey = `${img.src}|${flip ? "F" : "-"}`;
    if (measured.get(img) === cacheKey && img.dataset.trim) {
      const cached = JSON.parse(img.dataset.trim);
      host.style.transform = cached.transform;
      host.style.visibility = "visible";
      return;
    }
    measured.set(img, cacheKey);
    if (img.complete && img.naturalWidth > 0) {
      run();
      return;
    }
    host.style.visibility = "hidden";
    img.addEventListener("load", run, { once: true });
    img.addEventListener("error", () => {
      img.dataset.trim = JSON.stringify({ transform: "none", flip });
      host.style.visibility = "visible";
    }, { once: true });
  }

  // renderer/supervisorLayout.ts（对话框版式）：预览面板与休息窗口共用同一套
  // 摆位规则——底部悬浮对话框（内缩、实测框顶定中点），台词裸排在框里。
  function clamp(v, lo, hi) {
    return Math.min(Math.max(v, lo), hi);
  }
  // 立绘“去掉透明部分后”的可见边缘（容器坐标）——与 break/takeout 同一套几何：
  // .trim 补偿变换 translate(-left*s%, bottom*s*fill%) scale(s)（origin: left bottom）
  // 把 img 盒（铺满立绘盒，盒左缘 x0、底缘 yB、盒宽 W、盒高 H）映射到屏幕，
  // 像素内容的四条可见边：
  //   · 左缘 = x0 + s*W*(rawLeft - left)
  //   · 右缘 = x0 + s*W*(1 - rawRight - left)
  //   · 顶缘 = yB - s*fill*H*(1 - rawTop - bottom)
  //   · 底缘 = yB - s*fill*H*(rawBottom - bottom)
  // left/bottom 是变换实际使用的边距（过 IGNORE，可能被归零），rawLeft/rawTop 等是
  // 像素真实边距（未归零）——二者只在边距 <0.8% 时不同。
  // trim 尚未就绪（图片未加载/量失败）时退化为立绘自身外框。
  function figureVisibleRect(figure, fallbackTop) {
    const box = figure.getBoundingClientRect();
    const img = figure.querySelector("img");
    let t = null;
    try { t = img ? JSON.parse(img.dataset.trim || "null") : null; } catch (e) { t = null; }
    if (!t || typeof t.scale !== "number" || box.width <= 0 || box.height <= 0) {
      return { left: box.left, right: box.right, top: fallbackTop, bottom: box.bottom };
    }
    const s = t.scale;
    const fill = typeof t.fill === "number" ? t.fill : 1;
    const l = typeof t.left === "number" ? t.left : 0;
    const bt = typeof t.bottom === "number" ? t.bottom : 0;
    const rl = typeof t.rawLeft === "number" ? t.rawLeft : l;
    const rr = typeof t.rawRight === "number" ? t.rawRight : (typeof t.right === "number" ? t.right : 0);
    const rtp = typeof t.rawTop === "number" ? t.rawTop : (typeof t.top === "number" ? t.top : 0);
    const rbt = typeof t.rawBottom === "number" ? t.rawBottom : bt;
    return {
      left: box.left + s * box.width * (rl - l),
      right: box.left + s * box.width * (1 - rr - l),
      top: box.bottom - s * fill * box.height * (1 - rtp - bt),
      bottom: box.bottom - s * fill * box.height * (rbt - bt)
    };
  }
  // 与休息窗口的 layoutSupDialog 同一套规则（见 break.js）：锚点中点
  //   水平 midX = (立绘可见右缘 + 容器右缘) / 2
  //   垂直 midY = (对话框上边缘 + 容器上边缘) / 2（框顶实测，内缩自动跟随）。
  // 对话框（.sup-preview-content）与预览面板边缘的偏移由 CSS 决定（当前贴边
  // 为 0），摆位用实测 rect 换算。预览里没有倒计时环（.bar 不存在），环分支
  // 保留与 break.js 同构。
  var CD_EDGE = 10;
  function layoutSupDialog(figure, content, measure) {
    const container = figure?.parentElement ?? null;
    if (!container || !figure || !content) return;
    const cRect = container.getBoundingClientRect();
    if (cRect.width <= 0) return;
    const figBox = figure.getBoundingClientRect();
    const figImg = figure.querySelector("img");
    const haveTrim = !!(figImg && figImg.dataset.trim);
    const vis = figureVisibleRect(figure, figBox.top);
    const figLeft = clamp(vis.left - cRect.left, 0, cRect.width);
    const figRight = clamp(vis.right - cRect.left, 0, cRect.width);
    const figTop = clamp(vis.top - cRect.top, 0, cRect.height);
    const bar = content.querySelector(".bar");
    const boxRect = content.getBoundingClientRect();
    const boxLeft = boxRect.left - cRect.left; // 对话框左缘相对容器（= 水平内缩量）
    const boxBottomGap = cRect.bottom - boxRect.bottom; // 对话框底缘距容器底（= 底部内缩量）
    const boxTop = boxRect.top - cRect.top; // 对话框顶缘相对容器
    const midX = (figRight + cRect.width) / 2;
    const midY = boxTop / 2;
    let cdX = midX - boxLeft;
    let cdBottom = cRect.height - midY - boxBottomGap;
    if (bar) {
      const ringWrap = bar.querySelector(".ring-wrap");
      const barW = bar.offsetWidth || 84;
      const ringH = (ringWrap && ringWrap.offsetHeight) || 84;
      const barH = bar.offsetHeight || ringH;
      const tailH = Math.max(0, barH - ringH);
      cdX = clamp(
        midX - boxLeft - barW / 2,
        CD_EDGE - boxLeft,
        Math.max(CD_EDGE - boxLeft, cRect.width - boxLeft - barW - CD_EDGE)
      );
      cdBottom = clamp(
        cRect.height - midY - ringH / 2 - tailH - boxBottomGap,
        CD_EDGE - boxBottomGap,
        Math.max(CD_EDGE - boxBottomGap, cRect.height - boxBottomGap - barH - CD_EDGE)
      );
      bar.style.left = `${Math.round(cdX)}px`;
      bar.style.bottom = `${Math.round(cdBottom)}px`;
      bar.style.right = "auto";
    }
    content.dataset.supMetrics = JSON.stringify({
      mode: "dialog",
      figLeft: Math.round(figLeft),
      figRight: Math.round(figRight),
      figTop: Math.round(figTop),
      cdX: Math.round(cdX),
      cdBottom: Math.round(cdBottom),
      midX: Math.round(midX),
      midY: Math.round(midY),
      src: haveTrim ? "trim" : "figure"
    });
  }
  function bindSupContentLayout(figure, content, measure) {
    const relayout = () => layoutSupDialog(figure, content, measure);
    if (!figure || !content) return relayout;
    relayout();
    const container = figure.parentElement;
    if (typeof ResizeObserver !== "undefined") {
      const ro = new ResizeObserver(relayout);
      ro.observe(figure);
      if (container) ro.observe(container);
      if (measure) ro.observe(measure);
      ro.observe(content);
    }
    if (measure && typeof MutationObserver !== "undefined") {
      new MutationObserver(relayout).observe(measure, {
        attributes: true,
        attributeFilter: ["style"]
      });
    }
    window.addEventListener("resize", relayout);
    return relayout;
  }

  // renderer/settings.ts
  var S;
  var ROLES = [];
  var curSupStage = 1;
  var supPreview = null;
  var supEditName = "";
  var $ = (id) => document.getElementById(id);
  function applyTitlebarMaximized(max) {
    document.getElementById("titlebar")?.classList.toggle("maximized", max);
    const btn = document.getElementById("wb-max");
    if (btn) {
      const label = max ? "还原" : "最大化";
      btn.title = label;
      btn.setAttribute("aria-label", label);
    }
  }
  // 预览请求的后端回执：真实提醒窗口（休息 / 外卖）正在显示时后端拒弹，返回 "busy"。
  // 统一在这里提示，否则点了按钮没反应，用户会以为是卡住了。
  async function previewBreak(kind, key) {
    const r = await window.api.previewBreak(kind, key);
    if (r === "busy") toast("休息提醒窗口正在显示，暂时无法预览");
    return r;
  }
  async function previewTakeout() {
    const r = await window.api.previewTakeout();
    if (r === "busy") toast("外卖提醒窗口正在显示，暂时无法预览");
    return r;
  }
  function toast(msg) {
    const t = $("toast");
    t.textContent = msg;
    t.classList.remove("hidden");
    // 视觉提示 + 语音播报两条通道：toast 显示 2.2 s 后消失，读屏靠常驻的 #sr-status。
    // 同一个 msg 连发两次时先进空串，否则内容没变、活区不会重新播报（“又超时了”这类）。
    const live = $("sr-status");
    if (live) {
      live.textContent = "";
      requestAnimationFrame(() => { live.textContent = msg; });
    }
    setTimeout(() => t.classList.add("hidden"), 2200);
  }
  // ---- 对话框焦点管理（可访问性：域8 的“role=dialog 无焦点管理证据”）----
  // 十一个 .modal 全靠 classList 加/减 hidden 开关，调用点散在二十多处。逐个改必然漏，
  // 所以用 MutationObserver 盯 class 变化，统一做三件事：
  //   · 打开 → 记住触发元素，把焦点送进对话框（[data-autofocus] 优先，其次第一个可聚焦元素）
  //   · 打开期间 → Tab / Shift+Tab 在对话框内循环（原生 <dialog> 的免费能力，这里是手写的）
  //   · 关闭 → 焦点还给触发元素；它若已被重渲染掉，退到页面里第一个可见可聚焦控件
  // 这就是“对话框有没有焦点管理”的全部判定内容：焦点进得去、跑不出、回得来。
  const MODAL_IDS = [
    "batch-modal", "confirm-modal", "prompt-modal", "app-pick-modal", "color-modal",
    "update-modal", "updated-modal", "stats-modal", "snd-scan-modal", "notices-modal",
    "license-modal",
  ];
  const FOCUSABLE_SEL = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
  let modalOpener = null;
  function modalIsOpen(el) {
    return !!el && !el.classList.contains("hidden");
  }
  function openModalEl() {
    for (const m of document.querySelectorAll(".modal")) if (modalIsOpen(m)) return m;
    return null;
  }
  function focusablesIn(root) {
    return [...root.querySelectorAll(FOCUSABLE_SEL)].filter(
      (el) => el.offsetParent !== null || el === document.activeElement,
    );
  }
  function focusIntoModal(m) {
    const box = m.querySelector(".modal-box") || m;
    // 对话框自己已经聚焦过（如鸣谢正文、命名输入框）就不抢
    if (box.contains(document.activeElement)) return;
    const target = box.querySelector("[data-autofocus]") || focusablesIn(box)[0];
    if (target) {
      target.focus();
    } else {
      // 没有任何可聚焦控件（纯说明框）：让容器自己接焦点，Tab 从这里开始循环
      box.setAttribute("tabindex", "-1");
      box.focus();
    }
  }
  // “打开者”必须在打开的那一刻就知道是谁 —— 不能等 MutationObserver 回调再看
  // document.activeElement：有些对话框在显示自己的同一段同步代码里就把焦点挪进去了
  // （调色弹窗 `$("cp-done").focus()`、鸣谢正文）,等回调跑起来时活动元素已经在框内，
  // 于是 opener 记成 null，关掉之后焦点掉到页面第一个控件上（实测掉到“开机自启动”
  // 勾选框），键盘用户当场迷路。这里用 focusin 持续记“最后一个在框外拿到焦点的元素”，
  // 打开时若活动元素已在框内就退到它 —— 与打开顺序无关。
  let lastOutsideFocus = null;
  document.addEventListener("focusin", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLElement)) return;
    if (t.closest(".modal")) return;
    lastOutsideFocus = t;
  }, true);
  function onModalOpened(m) {
    const active = document.activeElement;
    if (active instanceof HTMLElement && !m.contains(active)) {
      modalOpener = active;
    } else if (lastOutsideFocus && lastOutsideFocus.isConnected) {
      modalOpener = lastOutsideFocus;
    } else {
      modalOpener = null;
    }
    focusIntoModal(m);
    // 内容可能是异步填进去的（鸣谢正文要等 api 回来）：下一帧再补一次
    requestAnimationFrame(() => {
      if (modalIsOpen(m)) focusIntoModal(m);
    });
  }
  function onModalClosed() {
    const opener = modalOpener;
    modalOpener = null;
    // 已经有下一个对话框接上了（统计提示关掉 → 立刻弹提示音体检）：焦点交给新的那个，
    // 不要抢回旧触发元素
    if (openModalEl()) return;
    if (opener && opener.isConnected && opener.offsetParent !== null) {
      opener.focus();
      return;
    }
    // 触发元素被重渲染掉（动态列表行等）：退到页面里第一个可见可聚焦控件
    const scope = document.querySelector("main") || document.body;
    const fallback = focusablesIn(scope).find((el) => !el.closest(".modal"));
    if (fallback) fallback.focus();
  }
  const modalOpenState = new Map();
  const modalObserver = new MutationObserver((records) => {
    for (const r of records) {
      const el = r.target;
      if (!(el instanceof HTMLElement) || !el.classList.contains("modal")) continue;
      const open = modalIsOpen(el);
      if (modalOpenState.get(el) === open) continue;
      modalOpenState.set(el, open);
      if (open) onModalOpened(el);
      else onModalClosed();
    }
  });
  for (const id of MODAL_IDS) {
    const el = $(id);
    if (!el) continue;
    modalOpenState.set(el, modalIsOpen(el));
    modalObserver.observe(el, { attributes: true, attributeFilter: ["class"] });
  }
  // Tab 循环：只在有对话框打开时接管；页面本身不拦 Tab
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Tab") return;
    const m = openModalEl();
    if (!m) return;
    const box = m.querySelector(".modal-box") || m;
    const items = focusablesIn(box);
    if (!items.length) {
      e.preventDefault();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    if (!box.contains(active)) {
      e.preventDefault();
      first.focus();
      return;
    }
    if (e.shiftKey && active === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }, true);
  // Esc 关闭对话框：十个 .modal 里五个自己带 keydown（confirm/prompt/notices/color/
  // app-pick），另外五个（批量新增、统计损坏提示、提示音体检、更新提示、更新完成）
  // 只能点按钮关 —— 键盘用户按 Esc 毫无反应，与“Esc 取消”的通用预期不符。
  // 这里统一兜底，且只兜“自己没有 Esc 处理”的那五个：点一下它自己的关闭按钮，
  // 走原来那条关闭逻辑（不把关闭语义抄成第二份实现，避免两处将来走岔）。
  const ESC_DISMISS = {
    "batch-modal": "msg-batch-cancel",
    "stats-modal": "st-ok",
    "snd-scan-modal": "ss-close",
    "update-modal": "up-later",
    "updated-modal": "upd-ok",
  };
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape") return;
    const m = openModalEl();
    if (!m) return;
    const btnId = ESC_DISMISS[m.id];
    if (!btnId) return; // 自己有 Esc 处理的，交给它
    const btn = $(btnId);
    if (!btn) return;
    e.preventDefault();
    btn.click();
  }, true);
  // 统计文件损坏：Go 侧已把原文件改名成 stats_<日期_时间>.json 备份，并重建空表
  //（见 stats.go 的 Load）。必须让用户知道——否则历史统计“凭空归零”，看起来像软件
  // 悄悄丢了数据。备份文件路径一并给出，用户想抢救原始数据还能找到。
  let statsNoticeShown = false;
  async function showStatsFileNotice() {
    if (statsNoticeShown) return false;
    const n = await window.api.getStatsFileNotice();
    if (!n || statsNoticeShown) return false;
    statsNoticeShown = true;
    const body = $("st-body");
    body.innerHTML = "";
    const add = (label, val) => {
      const div = document.createElement("div");
      div.className = "up-item";
      div.textContent = `${label}：${val || "—"}`;
      body.appendChild(div);
    };
    add("原文件已备份为", n.backup || "（改名失败）");
    add("判定原因", n.reason);
    add("处理时间", n.at);
    $("stats-modal").classList.remove("hidden");
    return true;
  }
  function bindStatsNotice() {
    $("st-ok").addEventListener("click", () => {
      $("stats-modal").classList.add("hidden");
      void window.api.ackStatsFileNotice();
      // 统计弹窗关掉之后，才轮到提示音目录的体检结果（同一时刻只弹一个）
      void showSoundScanNotice();
    });
  }
  // 随包 sounds/ 的体检结果（见 sound_scan.go）：用户往 exe 旁的 sounds/ 丢了新音频，
  // 或删掉了已登记条目的文件。**只落一次**；两段各有自己的决策按钮，处理完一段若另一段
  // 还有内容就继续留着，两段都完自动关。点“暂不处理”= 本次运行不再问（不写盘，
  // 下次启动会重新扫 —— 文件还在的话那时再问一遍）。
  var sndScanShown = false;

  function fmtBytes(n) {
    const v = Number(n) || 0;
    if (v < 1024) return v + " B";
    if (v < 1048576) return (v / 1024).toFixed(1) + " KB";
    return (v / 1048576).toFixed(1) + " MB";
  }

  // scanSection 生成一段“标题 + 说明 + 列表 + 一个决策按钮”的区块
  function scanSection(opts) {
    const sec = document.createElement("div");
    sec.className = "ss-sec";
    const title = document.createElement("p");
    title.className = "ss-sec-title";
    title.textContent = opts.title;
    const hint = document.createElement("p");
    hint.className = "ss-hint";
    hint.textContent = opts.hint;
    sec.append(title, hint);
    for (const row of opts.rows) {
      const item = document.createElement("div");
      item.className = "up-item";
      item.textContent = row.sub ? row.text + " · " + row.sub : row.text;
      item.title = row.sub || row.text;
      sec.appendChild(item);
    }
    const actions = document.createElement("div");
    actions.className = "ss-sec-actions";
    const ok = document.createElement("button");
    ok.className = "btn mini" + (opts.danger ? " danger" : " primary");
    ok.textContent = opts.okText;
    ok.addEventListener("click", async () => {
      if (ok.disabled) return;
      ok.disabled = true;
      let done = false;
      try {
        done = await opts.onOk();
      } catch (err) {
        console.error("[sound-scan] 处理失败:", err);
      }
      if (!done) {
        ok.disabled = false;
        toast("处理失败，请重试");
        return;
      }
      sec.remove();
      if (!$("ss-body").children.length) closeSoundScanModal(false);
    });
    actions.appendChild(ok);
    sec.appendChild(actions);
    return sec;
  }

  // dismiss 为真才算“用户主动暂不”：全部处理完自动关闭时不该记这一笔，
  // 否则日志会写着“用户选择暂不处理”，与事实相反。
  function closeSoundScanModal(dismiss) {
    $("snd-scan-modal").classList.add("hidden");
    $("ss-body").textContent = "";
    if (dismiss) void window.api.dismissSoundScan();
  }

  async function showSoundScanNotice() {
    if (sndScanShown) return false;
    // 统计文件弹窗优先（它关系到“数据是不是丢了”），没关就不抢位置
    if (!$("stats-modal").classList.contains("hidden")) return false;
    const n = await window.api.getSoundScanNotice();
    const newList = n && Array.isArray(n.new) ? n.new : [];
    const missList = n && Array.isArray(n.missing) ? n.missing : [];
    sndScanShown = true;
    if (!newList.length && !missList.length) return false;

    $("ss-dir").textContent = n.dir ? "目录：" + n.dir : "";
    const body = $("ss-body");
    body.textContent = "";

    if (newList.length) {
      body.appendChild(
        scanSection({
          title: "发现 " + newList.length + " 个新提示音",
          hint: "sounds 目录里有尚未登记的音频。导入后可在“提示音”列表里选用（原地登记：不复制、不改名）。",
          rows: newList.map((f) => ({ text: f.file, sub: fmtBytes(f.size) })),
          okText: "全部导入",
          onOk: async () => {
            const r = await window.api.importScannedSounds(newList.map((f) => f.file));
            if (!r || !r.ok || !r.imported) return false;
            S = await window.api.getSettings();
            renderSoundMusic();
            toast("已导入 " + r.imported + " 个提示音");
            return true;
          }
        })
      );
    }

    if (missList.length) {
      body.appendChild(
        scanSection({
          title: missList.length + " 个提示音的文件已不在",
          hint: "这些条目还在配置里，但文件已经找不到（试听会失败）。从配置删除只移除条目，不删除任何文件。",
          rows: missList.map((m) => ({ text: m.name, sub: m.file })),
          okText: "从配置删除",
          danger: true,
          onOk: async () => {
            const r = await window.api.dropMissingSounds(missList.map((m) => m.id));
            if (!r || !r.ok || !r.removed) return false;
            S = await window.api.getSettings();
            renderSoundMusic();
            toast("已从配置删除 " + r.removed + " 条失效提示音");
            return true;
          }
        })
      );
    }

    $("snd-scan-modal").classList.remove("hidden");
    return true;
  }

  function bindSoundScanModal() {
    $("ss-close").addEventListener("click", () => closeSoundScanModal(true));
  }

  var cfResolve = null;
  function confirmDialog(opts) {
    $("cf-title").textContent = opts.title;
    const desc = $("cf-desc");
    desc.textContent = opts.desc ?? "";
    desc.classList.toggle("hidden", !opts.desc);
    const detail = $("cf-detail");
    detail.textContent = opts.detail ?? "";
    detail.classList.toggle("hidden", !opts.detail);
    $("cf-ok").textContent = opts.okText ?? "删除";
    $("cf-cancel").textContent = opts.cancelText ?? "取消";
    $("confirm-modal").classList.remove("hidden");
    setTimeout(() => $("cf-cancel").focus(), 0);
    return new Promise((resolve) => {
      cfResolve = resolve;
    });
  }
  function closeConfirm(ok) {
    const box = $("confirm-modal");
    if (!box || box.classList.contains("hidden")) return;
    box.classList.add("hidden");
    const resolve = cfResolve;
    cfResolve = null;
    resolve?.(ok);
  }
  var pmResolve = null;
  var pmValidate = null;
  var pmBusy = false;
  function promptDialog(opts) {
    $("pm-title").textContent = opts.title;
    const desc = $("pm-desc");
    desc.textContent = opts.desc ?? "";
    desc.classList.toggle("hidden", !opts.desc);
    const label = $("pm-label");
    label.textContent = opts.label ?? "";
    label.classList.toggle("hidden", !opts.label);
    const input = $("pm-input");
    input.value = opts.value ?? "";
    input.placeholder = opts.placeholder ?? "";
    $("pm-ok").textContent = opts.okText ?? "确定";
    // validate(value) 返回非空文案 = 拒绝保存。命名框用它做“重名检查”：
    // 拒绝时对话框**不关**，输入框里的内容原样留着，用户改个名再提交 ——
    // 而不是弹一下 toast 就把刚输入的名字丢掉（丢名字 = 用户白输一遍）。
    pmValidate = typeof opts.validate === "function" ? opts.validate : null;
    setPromptError(opts.error ?? "");
    $("prompt-modal").classList.remove("hidden");
    setTimeout(() => {
      input.focus();
      input.select();
    }, 0);
    return new Promise((resolve) => {
      pmResolve = resolve;
    });
  }
  // setPromptError 显示/清空命名框里的错误提示（重名等）
  function setPromptError(msg) {
    const el = $("pm-error");
    if (!el) return;
    el.textContent = msg ?? "";
    el.classList.toggle("hidden", !msg);
  }
  function closePrompt(v) {
    const box = $("prompt-modal");
    if (!box || box.classList.contains("hidden")) return;
    // 保存进行中不接受“取消”：validate 里可能正在往磁盘写名字，
    // 这时候把目录删掉，写完就把它复活成一个没有立绘的僵尸目录。
    // （等 validate 回来就好 —— 它成功会自己关框，失败会留在框里报错。）
    if (pmBusy) return;
    box.classList.add("hidden");
    const resolve = pmResolve;
    pmResolve = null;
    pmValidate = null;
    setPromptError("");
    resolve?.(v);
  }
  // submitPrompt 命名框的提交入口（确定按钮与回车都走这里）：
  // 先过 validate（可以是异步的：真正的保存动作就放在里面），
  // 被拒就把错误显示在框里、留在框里让用户改，不 resolve。
  async function submitPrompt() {
    if (pmBusy) return;
    const input = $("pm-input");
    const v = input.value;
    if (pmValidate) {
      pmBusy = true;
      let err = null;
      try {
        err = await pmValidate(v);
      } catch (e) {
        console.error("[promptDialog] validate 抛错", e);
        err = "校验失败，请重试";
      }
      pmBusy = false;
      if (err) {
        setPromptError(err);
        input.focus();
        input.select();
        return;
      }
    }
    closePrompt(v);
  }
  function currentGeomName() {
    return supEditName;
  }
  function currentRoleName() {
    return ROLES.find((r) => r.name === supEditName)?.name ?? "—";
  }
  function geomOf(key) {
    return ROLES.find((r) => r.name === key)?.geom ?? null;
  }
  // 督休专员显示名是否已被占用（与 Go 侧 roleNameTaken 同口径：忽略大小写）。
  // excludeName 是“自己”现在的名字 —— 改名时要排除自己，否则用户不改名直接点确定
  // 会被自己的旧名字挡住。
  //
  // 数据源是内存里的 ROLES（ListRoles 的结果），不是磁盘 —— 前端要的是
  // **用户看得见的名字**是否重复，Go 侧还会再兜一层底。
  function roleNameTaken(name, excludeName) {
    const n = (name ?? "").trim();
    if (!n) return false;
    const lower = n.toLowerCase();
    return ROLES.some(
      (r) => r.name !== excludeName && (r.name ?? "").trim().toLowerCase() === lower,
    );
  }
  // roleNameError 重名检查的错误文案（null = 通过）。
  // excludeName 同 roleNameTaken：改名时排除自己。
  function roleNameError(name, excludeName) {
    const n = (name ?? "").trim();
    if (!n) return "名称不能为空";
    if (roleNameTaken(n, excludeName)) {
      return `名称“${n}”已存在，请换一个`;
    }
    return null;
  }
  // persistRole 把内存里的某个督休专员写回 meta.json。
  // 返回 Promise<boolean>：false = Go 侧拒绝（重名 / 写失败）。
  // 调用方必须能感知这个结果 —— 否则界面显示改了名字、磁盘上却没改，
  // 下次启动又变回去，界面在骗人。
  function persistRole(key) {
    const role = ROLES.find((r) => r.name === key);
    if (!role) return Promise.resolve(false);
    try {
      return Promise.resolve(
        window.api.saveSupervisorRole(key, {
          name: role.name,
          enabled: role.enabled,
          geom: role.geom,
          messages: role.messages
        })
      )
        .then((ok) => ok === true)
        .catch((e) => {
          console.error("[persistRole] 保存失败", e);
          return false;
        });
    } catch (e) {
      console.error("[persistRole] 保存失败", e);
      return Promise.resolve(false);
    }
  }
  function ensureSupEditKey() {
    if (supEditName && ROLES.some((r) => r.name === supEditName)) return;
    if (S.supervisor.selection === "fixed") {
      // 固定模式：按 fixedRole（专员名）选（不再要求 enabled）
      supEditName = ROLES.find((r) => r.name === S.supervisor.fixedRole)?.name ?? ROLES[0]?.name ?? "";
      return;
    }
    // 随机 / 顺序：优先已勾选立绘，否则列表第一个
    const firstEnabled = ROLES.find((r) => r.enabled);
    supEditName = firstEnabled?.name ?? ROLES[0]?.name ?? "";
  }
  function commitGeomToRole(g) {
    const role = ROLES.find((r) => r.name === currentGeomName());
    if (!role) return false;
    role.geom = { ...g };
    return true;
  }
  function applySelectionUI() {
    const btn = $("sup-selection-btn");
    if (!btn) return;
    applyModeIcon(btn, S.supervisor.selection ?? "random");
  }
  function fillSupervisorImageName() {
    const name = currentRoleName();
    const msgRole = $("sup-msg-role");
    if (msgRole) msgRole.textContent = name;
    const advRole = $("sup-adv-role");
    if (advRole) advRole.textContent = name;
    for (const btn of document.querySelectorAll("#sup-gallery .role-item")) {
      btn.classList.toggle("active", btn.dataset.name === supEditName);
    }
  }
  // 勾选态的唯一入口：只有磁贴上那个单选框/复选框会调它（user 2026-11-02：点立绘不许改勾选）。
  // 改完只同步选择指示，不整块重排磁贴 —— 重排会把刚点上来的键盘焦点丢掉。
  function selectRole(key) {
    const role = ROLES.find((r) => r.name === key);
    if (!role) return;
    supEditName = key;
    if (S.supervisor.selection === "fixed") {
      // 固定显示：单选框语义 —— 选中即设为固定项；再点已选中那枚不产生变化（单选框不能取消）
      if (S.supervisor.fixedRole !== key) {
        S.supervisor.fixedRole = key;
        autoSave();
      }
    } else {
      // 随机 / 顺序显示：复选框语义
      role.enabled = !role.enabled;
      persistRole(key);
      autoSave();
    }
    fillSupervisorImageName();
    fillGeomControls(normalizeSupGeom(geomOf(supEditName)));
    void refreshSupervisorPreview();
    syncRoleSelUI();
  }
  // 点立绘磁贴：只换“正在编辑的专员”（预览面板 / 尺寸 / 台词跟着切），不动勾选态。
  // 勾选与编辑对象是两件事：我想看看某人的立绘，不等于我要把他勾上。
  function focusRole(key) {
    if (!ROLES.some((r) => r.name === key)) return;
    if (supEditName === key) return;
    supEditName = key;
    fillSupervisorImageName();
    fillGeomControls(normalizeSupGeom(geomOf(supEditName)));
    void refreshSupervisorPreview();
  }
  // 勾选态变化后只改选择指示（含 aria-checked），磁贴 DOM 保持不动
  function syncRoleSelUI() {
    const fixedMode = S.supervisor.selection === "fixed";
    for (const el of document.querySelectorAll("#sup-gallery .role-item")) {
      const role = ROLES.find((r) => r.name === el.dataset.name);
      const sel = el.querySelector(".role-sel");
      if (!role || !sel) continue;
      const on = fixedMode ? role.name === S.supervisor.fixedRole : role.enabled;
      sel.classList.toggle("on", on);
      sel.setAttribute("aria-checked", on ? "true" : "false");
    }
  }
  async function renameRole(key) {
    const role = ROLES.find((r) => r.name === key);
    if (!role) return;
    const input = await promptDialog({
      title: "重命名",
      desc: "名称会显示在立绘库磁贴、台词气泡的说话人与提醒内容面板标题。",
      value: role.name,
      placeholder: "名称",
      okText: "确定",
      // 重名 → 拒绝保存，留在框里要求重新输入（不关对话框、不丢已输入的名字）。
      // excludeName = key：排除自己，否则“不改名直接点确定”会被自己的旧名挡住。
      //
      // 校验与**保存**都在 validate 里做：Go 侧 SaveRole 才是权威（磁盘上可能已经
      // 多了一个同名专员，前端内存里的 ROLES 不一定知道），它返回 false 就是被拒 ——
      // 这时对话框不关、错误留在框里，用户改个名再提交，而不是弹个 toast 把名字丢掉。
      validate: async (v) => {
        const name = (v ?? "").trim();
        if (!name) return "名称不能为空";
        const err = roleNameError(name, key);
        if (err) return err;
        // 全字段保存（与原来的 persistRole 同一套语义：名字 / 启停 / 几何 / 台词一起落盘），
        // 失败时把内存里的名字退回去 —— 界面不能显示一个没存住的名字。
        const prev = role.name;
        role.name = name;
        const ok = await persistRole(key);
        if (!ok) {
          role.name = prev;
          return "名称已存在（或保存失败），请换一个";
        }
        return null;
      }
    });
    if (input === null) return;
    // 改的是名字本身：当前编辑对象的标识要跟着换，否则后面按旧名找不到人
    supEditName = role.name;
    fillSupervisorImageName();
    autoSave();
    await refreshSupervisorPreview();
    await renderRoleGallery();
    toast(`已重命名为“${role.name}”`);
  }
  // makeRolePreview 生成并保存磁贴缩略图（导入 / 换图之后调用）。
  //
  // 为什么在渲染层做：Go 侧不引图像库（CGO_ENABLED=0，见 role_preview.go 的取舍说明），
  // 而内核自带 canvas 与 WebP 编码器 —— 由 role_thumb.js 裁掉四边透明区、缩到磁贴高度
  // （92 CSS px × 2 倍屏 = 184）、压成 6~10 KB 的 WebP，再交回 Go 落盘。
  // 失败不影响主流程：没有预览图时磁贴会回退到立绘原图（role.go 的 scanRoles）。
  async function makeRolePreview(key) {
    try {
      if (!window.WRRoleThumb) return false;
      const src = await window.api.roleFigureDataURL(key);
      if (!src) return false;
      const dataURL = await window.WRRoleThumb.buildFromDataURL(src);
      if (!dataURL) return false;
      return await window.api.saveRolePreview(key, dataURL);
    } catch (e) {
      return false;
    }
  }
  async function replaceRoleImage(key) {
    const role = ROLES.find((r) => r.name === key);
    if (!role) return;
    const ok = await window.api.replaceSupervisorImage(key);
    if (!ok) return;
    // 换图时 Go 侧把旧预览图删了（留着就会显示上一张图），这里补一张新的
    await makeRolePreview(key);
    // 换图后缩略图要重读，否则磁贴仍显示旧图
    ROLES = await window.api.listSupervisorRoles();
    if (supEditName === key) {
      // 换的是当前编辑督休专员：预览要重读（Go 侧会按新立绘重新组装会话）
      await refreshSupervisorPreview();
    }
    await renderRoleGallery();
    fillSupervisorImageName();
    toast(`已更新“${role.name}”的立绘图片`);
  }
  // 启用/禁用菜单项已移除：随机/顺序模式改由列表复选框直接控制 role.enabled
  async function removeRole(key) {
    const role = ROLES.find((r) => r.name === key);
    if (!role) return;
    const ok = await confirmDialog({
      title: "删除",
      desc: `确定删除“${role.name}”？`,
      detail: "会连同目录（配置、立绘、预览图）一并删除，且无法恢复。",
      okText: "删除"
    });
    if (!ok) return;
    // 必须看返回值：删除失败（文件被占用 / 权限不足）时若照样从 ROLES 里摘掉并提示
    // “已删除”，界面会说谎，而重启后这个督休专员又原样冒出来。
    const done = await window.api.removeSupervisorRole(key);
    if (!done) {
      toast("删除失败：文件可能被占用，请稍后重试");
      ROLES = await window.api.listSupervisorRoles();
      await renderRoleGallery();
      return;
    }
    ROLES = ROLES.filter((r) => r.name !== key);
    if (S.supervisor.fixedRole === key) {
      S.supervisor.fixedRole = ROLES[0]?.name ?? "";
    }
    // 顺序表里也把这个 key 摘掉：留下的陈旧 key 会让配置随删随涨，
    // 而且用户重新导入同一张图时它会“回到原来的位置”。
    if (Array.isArray(S.supervisor.order)) {
      S.supervisor.order = S.supervisor.order.filter((k) => k !== key);
    }
    ensureSupEditKey();
    fillSupervisorImageName();
    autoSave();
    await refreshSupervisorPreview();
    await renderRoleGallery();
    toast("已删除");
  }
  async function addCustomRole() {
    const picked = await window.api.pickSupervisorImage();
    if (!picked) return;
    // picked.name 是**磁盘上实际落盘**的名字（Go 侧建目录时已去过重：
    // 同名 → "名字 (2)"），用它当默认输入值 —— 用户不改名直接确定才不会撞名被拒。
    const base = picked.name.trim() || "自定义立绘";
    // saved = 名字是否已经存进磁盘（validate 里保存成功才算）。
    // 取消时要清掉刚建的目录 —— 那时名字还没存，目录是个半成品。
    let saved = false;
    const input = await promptDialog({
      title: "命名",
      desc: "给这个立绘起一个名字（显示在立绘库磁贴与提醒内容面板标题），名称不能与其他督休专员重名。",
      value: base,
      placeholder: "例如：我的上司",
      okText: "添加",
      // 重名 → 拒绝保存，留在框里要求用户重新输入（不关对话框、不丢已输入的名字）。
      //
      // 校验与**保存**都在 validate 里做：Go 侧 SaveRole 才是权威（磁盘上可能已经
      // 有一个同名专员，前端内存里的 ROLES 不一定知道），它返回 false 就是被拒 ——
      // 这时对话框不关、错误留在框里，用户改个名再提交，而不是弹个 toast 把名字丢掉。
      validate: async (v) => {
        const name = (v ?? "").trim();
        if (!name) return "名称不能为空";
        const err = roleNameError(name, picked.saved);
        if (err) return err;
        const ok = await window.api.saveSupervisorRole(picked.saved, { name });
        if (!ok) return "名称已存在（或保存失败），请换一个";
        saved = true;
        return null;
      }
    });
    if (input === null) {
      // 取消：删掉刚建的目录（名字还没存 → 那个目录对用户没有意义）
      if (!saved) await window.api.removeSupervisorRole(picked.saved);
      return;
    }
    const name = input.trim() || base;
    // 建目录时 Go 侧只写了立绘（不复制副本），磁贴缩略图在这里补生成
    await makeRolePreview(name);
    ROLES = await window.api.listSupervisorRoles();
    // 改名已经把落盘名换成用户输入的名字，这里要用最终的 name，不是 picked.saved（旧落盘名）
    supEditName = name;
    if (S.supervisor.selection === "fixed" && !S.supervisor.fixedRole) S.supervisor.fixedRole = name;
    fillSupervisorImageName();
    autoSave();
    await refreshSupervisorPreview();
    await renderRoleGallery();
    toast(`已添加“${name}”`);
  }
  function closeRoleMenu() {
    document.getElementById("sup-role-menu")?.remove();
  }
  function openRoleMenu(key, x, y) {
    closeRoleMenu();
    const role = ROLES.find((r) => r.name === key);
    if (!role) return;
    const menu = document.createElement("div");
    menu.id = "sup-role-menu";
    const items = [
      { act: "preview", label: "预览" },
      { act: "rename", label: "重命名" },
      { act: "replace", label: "修改图片" },
      { act: "del", label: "删除", danger: true }
    ];
    for (const it of items) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "role-menu-item" + (it.danger ? " danger" : "");
      b.dataset.act = it.act;
      b.textContent = it.label;
      b.addEventListener("click", () => {
        closeRoleMenu();
        if (it.act === "preview") void previewRoleFromMenu(key);
        else if (it.act === "rename") void renameRole(key);
        else if (it.act === "replace") void replaceRoleImage(key);
        else void removeRole(key);
      });
      menu.appendChild(b);
    }
    document.body.appendChild(menu);
    const rect = menu.getBoundingClientRect();
    menu.style.left = `${Math.max(8, Math.min(x, window.innerWidth - rect.width - 8))}px`;
    menu.style.top = `${Math.max(8, Math.min(y, window.innerHeight - rect.height - 8))}px`;
  }
  function bindRoleMenuDismiss() {
    document.addEventListener("click", (e) => {
      const menu = document.getElementById("sup-role-menu");
      if (menu && !menu.contains(e.target)) closeRoleMenu();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") closeRoleMenu();
    });
  }
  async function previewRoleFromMenu(key) {
    autoSave();
    await previewBreak("short", key);
  }
  var GALLERY_H_KEY = "wr.galleryH";
  function galleryMinHeight() {
    const body = $("sup-gallery-body");
    const box = $("sup-gallery");
    const first = box.querySelector(".role-item") ?? box.querySelector(".role-add");
    if (!first) return 0;
    const rowH = first.getBoundingClientRect().height;
    if (rowH < 40) return 0;
    const gap = parseFloat(getComputedStyle(box).rowGap) || 0;
    const padV = (el) => {
      const s = getComputedStyle(el);
      return parseFloat(s.paddingTop) + parseFloat(s.paddingBottom);
    };
    return Math.round(rowH * 2 + gap + padV(body) + padV(box) + 1);
  }
  function applyGalleryHeight(stored) {
    const body = $("sup-gallery-body");
    const min = galleryMinHeight();
    if (min <= 0) return;
    body.style.minHeight = String(min) + "px";
    const h = Math.max(min, Math.round(stored ?? 0) || min);
    body.style.height = String(h) + "px";
  }
  function setupGalleryPanel() {
    const panel = $("sup-gallery-panel");
    const body = $("sup-gallery-body");
    panel.querySelector(".msg-toggle").addEventListener("click", () => {
      panel.classList.toggle("open");
      body.classList.toggle("hidden");
    });
    const grip = $("sup-gallery-resize");
    grip.addEventListener("mousedown", (e) => {
      e.preventDefault();
      grip.classList.add("dragging");
      const startY = e.clientY;
      const startH = body.getBoundingClientRect().height;
      const onMove = (ev) => {
        applyGalleryHeight(startH + (ev.clientY - startY));
      };
      const onUp = () => {
        grip.classList.remove("dragging");
        document.removeEventListener("mousemove", onMove);
        document.removeEventListener("mouseup", onUp);
        localStorage.setItem(GALLERY_H_KEY, String(Math.round(body.getBoundingClientRect().height)));
      };
      document.addEventListener("mousemove", onMove);
      document.addEventListener("mouseup", onUp);
    });
    const ro = new ResizeObserver(() => {
      if (grip.classList.contains("dragging")) return;
      const min = galleryMinHeight();
      if (min <= 0) return;
      body.style.minHeight = String(min) + "px";
      if (!(Number(localStorage.getItem(GALLERY_H_KEY)) || 0)) body.style.height = String(min) + "px";
    });
    ro.observe($("sup-gallery"));
    applyGalleryHeight(Number(localStorage.getItem(GALLERY_H_KEY)) || null);
  }
  /* ---------- 候选专员拖拽排序 ----------
     画廊是**多列网格**（auto-fill），落点不能照搬单列列表的“上/下半”判断：
     必须先按行分组，再在行内按左右比较，最后换算成全局下标。
     顺序落在 config/settings.json 的 supervisor.order（role key 数组），
     Go 侧 ListRoles / 顺序轮换共用这一份 —— 拖一次，画廊顺序与轮换次序同时生效。 */
  var supDragName = null;   // 正在拖动的专员 key
  var supDragFrom = -1;    // 拖动源在 ROLES 里的下标
  var supDropIndex = -1;   // 落点：插入到第 n 个磁贴之前（n == 磁贴数 = 插到末尾）
  var supDragEndAt = 0;    // 最近一次拖动结束的时刻（挡掉拖完浏览器补的那一下 click）
  function roleTiles() {
    return Array.prototype.slice.call($("sup-gallery").querySelectorAll(".role-item"));
  }
  // 按行分组：同一行的磁贴 top 基本一致（阈值取磁贴高度的 40%，够容错又不串行）
  function roleRows() {
    const rows = [];
    for (const el of roleTiles()) {
      const r = el.getBoundingClientRect();
      const row = rows.find((rr) => Math.abs(rr.top - r.top) < Math.max(4, r.height * 0.4));
      if (row) row.items.push({ el: el, r: r });
      else rows.push({ top: r.top, items: [{ el: el, r: r }] });
    }
    return rows;
  }
  function computeRoleDropIndex(mx, my) {
    const tiles = roleTiles();
    if (!tiles.length) return -1;
    const rows = roleRows();
    if (!rows.length) return -1;
    // ① 鼠标落在哪一行（行带 = 该行磁贴 top..bottom）
    let row = rows.find((rr) => {
      const top = Math.min.apply(null, rr.items.map((x) => x.r.top));
      const bottom = Math.max.apply(null, rr.items.map((x) => x.r.bottom));
      return my >= top && my <= bottom;
    });
    if (!row) {
      // ② 落在行与行的缝里：取垂直距离最近的一行
      let best = null;
      let bestD = Infinity;
      for (const rr of rows) {
        const top = Math.min.apply(null, rr.items.map((x) => x.r.top));
        const bottom = Math.max.apply(null, rr.items.map((x) => x.r.bottom));
        const d = my < top ? top - my : my > bottom ? my - bottom : 0;
        if (d < bestD) { bestD = d; best = rr; }
      }
      row = best;
    }
    if (!row) return 0;
    // ③ 行内按左右比较：落在某块磁贴左半 → 插到它前面；右半 → 插到它后面
    for (const it of row.items) {
      if (mx < it.r.left + it.r.width / 2) return tiles.indexOf(it.el);
    }
    // ④ 行内最右侧 → 插到该行最后一块之后（= 下一行第一块之前，线性下标上是同一个位置）
    const last = row.items[row.items.length - 1];
    return tiles.indexOf(last.el) + 1;
  }
  function clearRoleDropHint() {
    const mk = document.getElementById("role-drop-marker");
    if (mk) mk.remove();
    supDropIndex = -1;
  }
  function showRoleDropHint(idx) {
    const box = $("sup-gallery");
    const tiles = roleTiles();
    if (!tiles.length) return;
    supDropIndex = idx;
    const boxRect = box.getBoundingClientRect();
    let mk = document.getElementById("role-drop-marker");
    if (!mk) {
      mk = document.createElement("div");
      mk.id = "role-drop-marker";
      mk.className = "role-drop-marker";
      box.appendChild(mk);
    }
    let left;
    let top;
    let height;
    if (idx < tiles.length) {
      // 插到第 idx 块之前 → 竖条画在它左边那道缝隙里
      const r = tiles[idx].getBoundingClientRect();
      left = r.left - boxRect.left - 5;
      top = r.top - boxRect.top;
      height = r.height;
    } else {
      // 插到末尾 → 竖条画在最后一块右边那道缝隙里（末尾的“＋新增”磁贴在这道缝之后）
      const r = tiles[tiles.length - 1].getBoundingClientRect();
      left = r.right - boxRect.left + 2;
      top = r.top - boxRect.top;
      height = r.height;
    }
    mk.style.left = Math.max(0, Math.round(left)) + "px";
    mk.style.top = Math.round(top) + "px";
    mk.style.height = Math.round(height) + "px";
  }
  // 把 ROLES 按新顺序重排并落盘（supervisor.order = 全部 key，数组整体替换）
  function applyRoleMove(from, to) {
    if (from < 0 || to < 0 || from === to) return;
    const moved = ROLES.splice(from, 1)[0];
    ROLES.splice(to, 0, moved);
    if (!S.supervisor) S.supervisor = {};
    S.supervisor.order = ROLES.map((r) => r.name);
    void autoSave();
    void renderRoleGallery();
  }
  // 容器级事件（只绑一次，别放进 renderRoleGallery —— 那里每渲染一次就会多绑一遍）
  function bindRoleGalleryDrag() {
    const box = $("sup-gallery");
    box.addEventListener("dragenter", (e) => {
      if (!supDragName) return;
      e.preventDefault();
    });
    box.addEventListener("dragover", (e) => {
      if (!supDragName) return;
      e.preventDefault(); // 不拦住就放不下
      if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
      const idx = computeRoleDropIndex(e.clientX, e.clientY);
      if (idx >= 0) showRoleDropHint(idx);
    });
    box.addEventListener("drop", (e) => {
      if (!supDragName) return;
      e.preventDefault();
      const from = supDragFrom;
      let to = supDropIndex;
      // 移除拖动源后，落点下标要前移一位（to > from = 落点在源之后）
      if (to > from) to -= 1;
      // 改 DOM 推迟到下一个任务：drop 之后浏览器还要在**源元素**上派发 dragend，
      // 源元素被提前换掉会导致 dragend 不触发、拖拽态清不掉。
      setTimeout(() => applyRoleMove(from, to), 0);
    });
  }
  async function renderRoleGallery() {
    const box = $("sup-gallery");
    box.innerHTML = "";
    const fixedMode = S.supervisor.selection === "fixed";
    for (const r of ROLES) {
      // 磁贴用 div 不用 button：里面要放一个可聚焦的选择控件，交互元素不能嵌在 button 里。
      // 磁贴只负责“切换正在编辑的专员”，勾选/取消勾选一律落到下面那个 sel 上。
      const tile = document.createElement("div");
      tile.className = "role-item";
      tile.dataset.name = r.name;
      tile.draggable = true;
      const img = document.createElement("img");
      if (r.thumb) img.src = r.thumb;
      img.alt = r.name;
      img.draggable = false; // 图片默认可拖，会抢走磁贴的拖拽（拖的是图而不是整块磁贴）
      const hint = document.createElement("span");
      hint.className = "role-hint";
      hint.textContent = "右键操作";
      // 选择指示：固定模式=单选框（按 fixedRole 名字），随机/顺序=复选框（按 enabled）。
      // 它是这份列表里唯一能改勾选的地方，所以带自己的 click / keydown 与 ARIA 状态。
      const sel = document.createElement("span");
      const on = fixedMode ? r.name === S.supervisor.fixedRole : r.enabled;
      sel.className = "role-sel " + (fixedMode ? "role-radio" : "role-check");
      sel.classList.toggle("on", on);
      sel.setAttribute("role", fixedMode ? "radio" : "checkbox");
      sel.setAttribute("aria-checked", on ? "true" : "false");
      sel.setAttribute("aria-label", (fixedMode ? "固定显示：" : "参与轮换：") + r.name);
      sel.tabIndex = 0;
      sel.addEventListener("click", (e) => {
        // 别冒泡到磁贴：那一下是“切换编辑对象”，不是“再勾一次”
        e.stopPropagation();
        selectRole(r.name);
      });
      sel.addEventListener("keydown", (e) => {
        if (e.key !== " " && e.key !== "Enter") return;
        e.preventDefault();
        e.stopPropagation();
        selectRole(r.name);
      });
      const cap = document.createElement("span");
      cap.className = "role-cap";
      cap.textContent = r.name;
      const capRow = document.createElement("span");
      capRow.className = "role-cap-row";
      capRow.append(sel, cap);
      tile.append(img, hint, capRow);
      tile.addEventListener("click", () => {
        // 拖完浏览器可能补一个 click（此时磁贴已经重排），那不是用户想看的人
        if (supDragEndAt && Date.now() - supDragEndAt < 400) {
          supDragEndAt = 0;
          return;
        }
        focusRole(r.name);
      });
      tile.addEventListener("contextmenu", (e) => {
        e.preventDefault();
        e.stopPropagation();
        openRoleMenu(r.name, e.clientX, e.clientY);
      });
      tile.addEventListener("dragstart", (e) => {
        supDragName = r.name;
        supDragFrom = ROLES.findIndex((x) => x.name === r.name);
        supDropIndex = supDragFrom;
        tile.classList.add("dragging");
        box.classList.add("sorting");
        if (e.dataTransfer) {
          e.dataTransfer.setData("text/plain", r.name);
          e.dataTransfer.effectAllowed = "move";
        }
      });
      tile.addEventListener("dragend", () => {
        tile.classList.remove("dragging");
        box.classList.remove("sorting");
        supDragName = null;
        supDragFrom = -1;
        supDragEndAt = Date.now();
        clearRoleDropHint();
      });
      box.appendChild(tile);
    }
    const add = document.createElement("button");
    add.type = "button";
    add.className = "role-add";
    add.title = "新增：支持 PNG / JPG / WebP，建议透明背景的竖版人物图；图片会复制到配置目录，原文件移动或删除后仍可用";
    add.innerHTML = '<span class="role-add-box"><span class="role-add-plus">＋</span><span class="role-add-hint">PNG / JPG / WebP<br>透明底竖版图</span></span><span class="role-add-cap">新增</span>';
    add.addEventListener("click", () => void addCustomRole());
    box.appendChild(add);
    $("sup-gallery-count").textContent = String(ROLES.length);
    fillSupervisorImageName();
    applyGalleryHeight(Number(localStorage.getItem(GALLERY_H_KEY)) || null);
  }
  function updateGeomSummary(g) {
    const el = $("sup-geom-summary");
    if (!el) return;
    const custom = g.sizePct !== SUP_GEOM_DEFAULT.sizePct || g.posXPct !== SUP_GEOM_DEFAULT.posXPct || g.posYPct !== SUP_GEOM_DEFAULT.posYPct || g.flip !== SUP_GEOM_DEFAULT.flip;
    el.textContent = `${custom ? "已调整 \xB7 " : ""}高度 ${g.sizePct}% \xB7 距左 ${g.posXPct}% \xB7 距底 ${g.posYPct}%` + (g.flip ? " \xB7 已翻转" : "");
    el.classList.toggle("customized", custom);
    el.title = "点开“高级选项”可调整当前立绘的显示尺寸与位置，以及是否水平翻转";
  }
  var supRelayout = null;
  function applySupGeometry(g) {
    const box = $("sup-preview");
    box.style.setProperty("--fig-size", String(g.sizePct));
    box.style.setProperty("--fig-x", String(g.posXPct));
    box.style.setProperty("--fig-y", String(g.posYPct));
    box.classList.toggle("sup-flip", g.flip);
    const pImg = $("sup-preview-img");
    if (pImg && pImg.src) applyFigureTrim($("sup-preview-trim"), pImg, g.flip);
    $("sup-size-val").textContent = `${g.sizePct}%`;
    $("sup-posx-val").textContent = `${g.posXPct}%`;
    $("sup-posy-val").textContent = `${g.posYPct}%`;
    updateGeomSummary(g);
    supRelayout ??= bindSupContentLayout(
      document.querySelector(".sup-preview-fig"),
      document.querySelector(".sup-preview-content"),
      $("sup-preview-trim")
    );
    supRelayout();
    // 立绘尺寸/位置一变，名字牌要跟着重新贴边（拖滑条时逐帧走这里）
    placeSupNamePlate();
  }
  function fillGeomControls(g) {
    $("sup-size").value = String(g.sizePct);
    $("sup-posx").value = String(g.posXPct);
    $("sup-posy").value = String(g.posYPct);
    $("sup-flip").checked = g.flip;
    applySupGeometry(g);
  }
  function clampGeom(el, key) {
    // 区间由 range 自己的 min/max 决定（与 SUP_GEOM_LIMITS 同值），越界已回写
    return clampNum(el, SUP_GEOM_DEFAULT[key]);
  }
  function readGeomFromForm() {
    return {
      sizePct: clampGeom($("sup-size"), "sizePct"),
      posXPct: clampGeom($("sup-posx"), "posXPct"),
      posYPct: clampGeom($("sup-posy"), "posYPct"),
      flip: $("sup-flip").checked
    };
  }
  // 预览面板的名字牌与真实提醒窗口同款：每次重绘随机换一枚“章”（牌底 + 碎点同下标），
  // 并避开上一次。档数与 web/dist 里的 seal-ink-1..6.svg、两份提醒页 JS 的
  // SEAL_VARIANTS 一致 —— 改档数要一起改（探针会红）。
  var PREVIEW_SEAL_VARIANTS = 6;
  var previewLastSeal = 0;
  function pickPreviewSeal(el) {
    if (!el) return;
    var i = 1 + Math.floor(Math.random() * PREVIEW_SEAL_VARIANTS);
    if (i === previewLastSeal) i = (i % PREVIEW_SEAL_VARIANTS) + 1;
    previewLastSeal = i;
    el.style.setProperty("--rt-ink", 'url("seal-ink-' + i + '.svg")');
    el.style.setProperty("--rt-specks", 'url("seal-specks-' + i + '.svg")');
  }
  // 名字牌的摆位：真机里这块固定在“立绘头部左侧的空白窄缝”，位置是 layoutRoleTitle()
  // 按立绘 alpha 剖面量出来的，不是写死的百分比 —— 预览必须同样按实测摆，否则立绘一靠左
  // 牌子就压到人物脸上（面板比真机窗口窄，差几个像素就是压不压脸的区别）。
  // 量的是 figureVisibleRect()（去掉透明边距后的可见边），与对话框摆位同源。
  function placeSupNamePlate() {
    const box = $("sup-preview");
    const fig = document.querySelector(".sup-preview-fig");
    const n = $("sup-preview-name");
    if (!box || !fig || !n || n.classList.contains("hidden")) return;
    const br = box.getBoundingClientRect();
    if (br.width <= 0) return;                     // 页签没切过来，量不到（保持 CSS 默认值）
    const fb = fig.getBoundingClientRect();
    const vis = figureVisibleRect(fig, fb.top);
    const w = n.offsetWidth || 27;
    const h = n.offsetHeight || 60;
    // 横向：牌子右缘贴住立绘可见左缘（留 6px 缝），缝不够就贴面板左缘 2px
    const left = Math.round(Math.max(2, Math.min(10, vis.left - br.left - w - 6)));
    // 纵向：牌顶与头顶齐平（真机就是钉在头部那一行），再夹进面板
    const top = Math.round(Math.min(Math.max(vis.top - br.top + 4, 8), Math.max(8, br.height - h - 8)));
    n.style.left = `${left}px`;
    n.style.top = `${top}px`;
  }
  function renderSupervisorPreview(stage) {
    curSupStage = stage;
    const box = $("sup-preview");
    box.dataset.stage = String(stage);
    box.style.setProperty("--accent", S.appearance.color);
    const off = !S.supervisor.enabled;
    // 顶层“启用”开关：未启用时整块隐藏立绘与对话框（CSS 据 data-enabled 隐藏），
    // 只留背景 + 中央关闭提示，避免“无立绘却挂着对话框”的错位观感
    box.dataset.enabled = off ? "0" : "1";
    const badge = $("sup-preview-badge");
    badge.classList.toggle("hidden", !off);
    badge.textContent = off ? "功能已关闭 \xB7 提醒窗口不会显示督休专员" : "";
    $("sup-preview-line").textContent = supPreview ? stage === 2 ? supPreview.lines.command || supPreview.lines.urge : supPreview.lines.urge : "无法读取立绘文件";
    // 说话人由对话框左上角的名字牌承载（与休息窗口的 dialog-name 同款），不再挂在台词上
    const previewName = $("sup-preview-name");
    pickPreviewSeal(previewName);
    if (supPreview?.name) {
      previewName.textContent = supPreview.name;
      previewName.classList.remove("hidden");
    } else {
      previewName.textContent = "";
      previewName.classList.add("hidden");
    }
    if (supPreview) {
      const img = $("sup-preview-img");
      img.src = supPreview.image;
      applyFigureTrim($("sup-preview-trim"), img, supPreview.geom.flip === true);
      // 立绘解码完才会量到真实的可见左缘，落位要等这一刻（applyFigureTrim 内部
      // 也是等 load 再量，这里跟它同一条时机）
      img.addEventListener("load", placeSupNamePlate, { once: true });
      placeSupNamePlate();
    }
  }
  async function refreshSupervisorPreview(keepUrge = false) {
    const prevUrge = supPreview?.lines.urge;
    supPreview = await window.api.previewSupervisor(S.supervisor, supEditName);
    if (supPreview) {
      if (keepUrge && prevUrge) supPreview.lines.urge = prevUrge;
      fillGeomControls(supPreview.geom);
    }
    renderMsgList("sup");
    renderSupervisorPreview(curSupStage);
  }
  var previewAudio = null;
  var previewFile = "";
  function fmtPreviewTime(sec) {
    if (!isFinite(sec) || sec < 0) sec = 0;
    const m = Math.floor(sec / 60);
    const s = Math.floor(sec % 60);
    return `${m}:${String(s).padStart(2, "0")}`;
  }
  function stopSoundPreview() {
    const bar = $("snd-player");
    if (previewAudio) {
      previewAudio.pause();
      previewAudio.src = "";
      previewAudio = null;
    }
    previewFile = "";
    if (bar) bar.classList.add("hidden");
  }
  async function previewSoundFile(file, name) {
    stopSoundPreview();
    const url = await window.api.soundFileUrl(file);
    if (!url) {
      toast("找不到该音频文件");
      return;
    }
    const a = new Audio(url);
    a.volume = Math.min(1, Math.max(0, S.sound.volume));
    previewAudio = a;
    previewFile = file;
    const bar = $("snd-player");
    const seek = $("snd-player-seek");
    const time = $("snd-player-time");
    const toggle = $("snd-player-toggle");
    $("snd-player-name").textContent = name;
    seek.value = "0";
    time.textContent = "0:00 / …";
    toggle.textContent = "暂停";
    bar.classList.remove("hidden");
    a.addEventListener("timeupdate", () => {
      if (a !== previewAudio || !a.duration) return;
      seek.value = String(Math.round(a.currentTime / a.duration * 1e3));
      time.textContent = `${fmtPreviewTime(a.currentTime)} / ${fmtPreviewTime(a.duration)}`;
    });
    a.addEventListener("ended", () => {
      if (a === previewAudio) stopSoundPreview();
    });
    a.addEventListener("error", () => {
      if (a === previewAudio) {
        toast("试听失败：文件不可用");
        stopSoundPreview();
      }
    });
    void a.play().catch(() => {
      toast("试听失败：文件不可用");
      stopSoundPreview();
    });
  }
  function bindSoundPlayer() {
    const seek = $("snd-player-seek");
    const toggle = $("snd-player-toggle");
    seek.addEventListener("input", () => {
      const a = previewAudio;
      if (a && isFinite(a.duration) && a.duration > 0) a.currentTime = parseFloat(seek.value) / 1e3 * a.duration;
    });
    toggle.addEventListener("click", () => {
      const a = previewAudio;
      if (!a) return;
      if (a.paused) {
        void a.play().catch(() => void 0);
        toggle.textContent = "暂停";
      } else {
        a.pause();
        toggle.textContent = "继续";
      }
    });
    $("snd-player-stop").addEventListener("click", stopSoundPreview);
  }
  function renderSoundMusic() {
    const list = $("snd-music-list");
    if (!list) return;
    list.textContent = "";
    const music = Array.isArray(S.sound.music) ? S.sound.music : [];
    const singleMode = S.sound.mode === "single";
    if (music.length === 0) {
      const empty = document.createElement("p");
      empty.className = "hint";
      empty.textContent = "列表已空：播放时不会再有提示音，点标题行的“＋”添加。";
      list.appendChild(empty);
      return;
    }
    for (const m of music) {
      const row = document.createElement("div");
      row.className = "tk-item" + (!singleMode && !m.enabled ? " disabled" : "") + " snd-row";
      const handle = document.createElement("span");
      handle.className = "msg-handle";
      handle.textContent = "⠿";
      handle.title = "拖动调整顺序";
      let ctrl;
      if (singleMode) {
        ctrl = document.createElement("input");
        ctrl.type = "radio";
        ctrl.name = "snd-single";
        ctrl.value = m.id;
        ctrl.checked = m.id === S.sound.singleId;
        ctrl.title = "单曲循环播放这首";
        ctrl.addEventListener("change", () => {
          if (!ctrl.checked) return;
          S.sound.singleId = m.id;
          autoSave();
        });
      } else {
        ctrl = document.createElement("input");
        ctrl.type = "checkbox";
        ctrl.checked = m.enabled;
        ctrl.title = "启用 / 停用：是否参与随机 / 顺序播放";
        ctrl.addEventListener("change", () => {
          m.enabled = ctrl.checked;
          row.classList.toggle("disabled", !ctrl.checked);
          autoSave();
        });
      }
      const name = document.createElement("span");
      name.className = "snd-music-name";
      name.textContent = m.name;
      name.title = m.file;
      const test = document.createElement("button");
      test.className = "btn mini";
      test.textContent = "试听";
      test.addEventListener("click", () => {
        void previewSoundFile(m.file, m.name);
      });
      const del = document.createElement("button");
      del.className = "btn mini danger";
      del.textContent = "✕";
      del.title = "删除这首音乐";
      del.addEventListener("click", async () => {
        const ok = await confirmDialog({
          title: "删除这首音乐？",
          desc: "删除后将从音乐列表移除，导入的音频副本也会一并清除。",
          detail: m.name,
          okText: "删除"
        });
        if (!ok) return;
        if (previewFile === m.file) stopSoundPreview();
        S.sound.music = music.filter((x) => x !== m);
        if (S.sound.singleId === m.id) S.sound.singleId = "";
        void window.api.deleteMusicSound(m.file);
        renderSoundMusic();
        autoSave();
        toast("已删除");
      });
      row.append(handle, ctrl, name, test, del);
      row.addEventListener("click", (e) => {
        // 点试听 / 删除 / 拖拽手柄 / 前面的控件本身时，交给各自的监听，不触发整行勾选
        if (e.target.closest(".btn") || e.target === ctrl || e.target === handle) return;
        if (singleMode) {
          if (S.sound.singleId === m.id) return; // 已是当前单曲，无需动作
          S.sound.singleId = m.id;
          autoSave();
          renderSoundMusic(); // 重绘以正确反映单选互斥
        } else {
          m.enabled = !m.enabled;
          ctrl.checked = m.enabled;
          row.classList.toggle("disabled", !m.enabled);
          autoSave();
        }
      });
      list.appendChild(row);
      handle.addEventListener("mousedown", () => {
        row.draggable = true;
      });
      row.addEventListener("dragstart", (e) => {
        row.classList.add("dragging");
        if (e.dataTransfer) {
          e.dataTransfer.setData("text/plain", m.id);
          e.dataTransfer.effectAllowed = "move";
        }
      });
      row.addEventListener("dragend", () => {
        row.classList.remove("dragging");
        row.draggable = false;
      });
      row.addEventListener("dragover", (e) => {
        e.preventDefault();
        if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
        row.classList.add("drop-target");
      });
      row.addEventListener("dragleave", () => row.classList.remove("drop-target"));
      row.addEventListener("drop", (e) => {
        e.preventDefault();
        row.classList.remove("drop-target");
        const dragId = e.dataTransfer?.getData("text/plain");
        if (!dragId || dragId === m.id) return;
        const from = S.sound.music.findIndex((i) => i.id === dragId);
        const to = S.sound.music.findIndex((i) => i.id === m.id);
        if (from < 0 || to < 0) return;
        const [moved] = S.sound.music.splice(from, 1);
        S.sound.music.splice(to, 0, moved);
        renderSoundMusic();
        autoSave();
      });
    }
  }
  /* 播放方式：三种循环策略的图标与名称。图标随当前模式切换（单曲循环=带“1”的
     循环箭头、随机播放=交叉箭头、顺序循环=循环箭头），常驻只显示图标，悬浮由
     title 显示文字名称；点击在三者间循环切换。 */
  var SND_MODE = {
    single: {
      label: "单曲循环",
      icon: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="m17 2 4 4-4 4"/><path d="M3 11v-1a4 4 0 0 1 4-4h14"/><path d="m7 22-4-4 4-4"/><path d="M21 13v1a4 4 0 0 1-4 4H3"/><path d="M11 10h1v4"/></svg>'
    },
    random: {
      label: "随机播放",
      icon: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M2 18h1.4c1.3 0 2.5-.6 3.3-1.7l6.1-8.6c.7-1.1 1.9-1.7 3.2-1.7H22"/><path d="m18 2 4 4-4 4"/><path d="M2 6h1.9c1.5 0 2.9.9 3.6 2.2"/><path d="M22 18h-4.7c-1.3 0-2.5-.6-3.3-1.7l-.5-.7"/><path d="m18 14 4 4-4 4"/></svg>'
    },
    sequence: {
      label: "顺序循环",
      icon: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="m17 2 4 4-4 4"/><path d="M3 11v-1a4 4 0 0 1 4-4h14"/><path d="m7 22-4-4 4-4"/><path d="M21 13v1a4 4 0 0 1-4 4H3"/></svg>'
    }
  };
  var SND_MODE_NEXT = { single: "random", random: "sequence", sequence: "single" };
  function setSoundModeUI(mode) {
    const btn = $("snd-mode");
    if (!btn) return;
    if (!SND_MODE[mode]) mode = "single";
    const info = SND_MODE[mode];
    btn.dataset.mode = mode;
    btn.setAttribute("aria-label", "播放方式：" + info.label);
    btn.title = info.label;
    const ico = btn.querySelector(".snd-mode-ico");
    if (ico) ico.innerHTML = info.icon;
  }
  // 提醒内容 / 立绘的“显示方式”图标按钮：复用声音卡三态图标（固定显示↔单曲循环、
  // 随机显示↔随机播放、顺序循环↔顺序循环），但标签改为提醒语境；点击循环切换。
  var DISPLAY_MODE = {
    fixed: { label: "固定显示", icon: SND_MODE.single.icon },
    random: { label: "随机显示", icon: SND_MODE.random.icon },
    sequence: { label: "顺序循环", icon: SND_MODE.sequence.icon }
  };
  var DISPLAY_MODE_NEXT = { fixed: "random", random: "sequence", sequence: "fixed" };
  function applyModeIcon(btn, mode) {
    if (!btn) return;
    if (!DISPLAY_MODE[mode]) mode = "fixed";
    const info = DISPLAY_MODE[mode];
    btn.dataset.mode = mode;
    btn.setAttribute("aria-label", "显示方式：" + info.label);
    btn.title = info.label;
    const ico = btn.querySelector(".snd-mode-ico");
    if (ico) ico.innerHTML = info.icon;
  }
  var pad22 = (n) => String(n).padStart(2, "0");
  function dayDiffOf(at) {
    const d = new Date(at);
    const now = /* @__PURE__ */ new Date();
    return Math.round(
      (new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime() - new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()) / 864e5
    );
  }
  function nextSlotDayLabel(at) {
    const d = new Date(at);
    const dayDiff = dayDiffOf(at);
    const day = dayDiff === 0 ? "今天" : dayDiff === 1 ? "明天" : WEEKDAYS.find((w) => w.value === isoWeekday(d))?.label ?? "";
    return `${day} ${pad22(d.getHours())}:${pad22(d.getMinutes())}`;
  }
  function slotNextText(next) {
    if (!next) return "";
    if (dayDiffOf(next.at) > 0) return "今日已无提醒";
    const mins = Math.max(0, Math.round((next.at - Date.now()) / 6e4));
    const left = mins >= 60 ? `${Math.floor(mins / 60)} 小时 ${mins % 60} 分` : `${mins} 分`;
    return `下次 ${nextSlotDayLabel(next.at)}（${left}后）`;
  }
  function defaultSlotTime() {
    const d = /* @__PURE__ */ new Date();
    // 向上取到下一个 10 分钟边界：保证新建的提醒时间一定在未来，
    // 否则落在过去的默认时间会被“今日提醒时间线”过滤掉，看起来像"没刷新"。
    const total = Math.floor((d.getHours() * 60 + d.getMinutes()) / 10) * 10 + 10;
    return { hour: Math.floor(total / 60) % 24, minute: total % 60 };
  }
  function newSlot() {
    const { hour, minute } = defaultSlotTime();
    return { id: newId(), hour, minute, repeat: "daily", days: [], enabled: true };
  }
  function updateTakeoutSummary() {
    const el = $("tk-summary");
    if (!el || !S.takeout) return;
    const t = S.takeout;
    const active = t.slots.filter((s) => s.enabled);
    if (!t.enabled) {
      el.textContent = "已关闭";
      el.classList.remove("customized");
      return;
    }
    if (active.length === 0) {
      el.textContent = "固定时间 \xB7 还没有提醒时刻";
      el.classList.remove("customized");
      return;
    }
    const plannedAt = LAST_STATE?.fixedNext?.takeout;
    const cfgNext = nextSlotAt(t.slots);
    const next = plannedAt !== void 0 ? { at: plannedAt } : cfgNext && { at: cfgNext.at };
    // 标题栏右侧只报“类型 · 条数 · 下次时刻”：提前通知秒数是卡内设置项（下面就有
    // 输入框），标题行不再复述一遍。分段拼接是为了 next 为空时不留下结尾的“·”。
    const parts = ["固定时间", `${active.length} 条`];
    const nx = slotNextText(next);
    if (nx) parts.push(nx);
    el.textContent = parts.join(" \xB7 ");
    el.classList.add("customized");
  }
  function renderSlotRow(slot, opts) {
    const row = document.createElement("div");
    row.className = "tk-item" + (slot.enabled ? "" : " disabled");
    const on = document.createElement("input");
    on.type = "checkbox";
    on.checked = slot.enabled;
    on.title = "启用 / 停用这条提醒";
    on.addEventListener("change", () => {
      slot.enabled = on.checked;
      row.classList.toggle("disabled", !on.checked);
      opts.onChange();
    });
    const time = document.createElement("div");
    time.className = "tk-time";
    const hh = document.createElement("input");
    hh.type = "number";
    hh.min = "0";
    hh.max = "23";
    hh.value = String(slot.hour);
    hh.title = "小时（0~23，24 小时制）";
    const mm = document.createElement("input");
    mm.type = "number";
    mm.min = "0";
    mm.max = "59";
    mm.value = String(slot.minute);
    mm.title = "分钟（0~59）";
    const hhText = document.createElement("span");
    hhText.textContent = "时";
    const mmText = document.createElement("span");
    mmText.textContent = "分";
    hh.addEventListener("change", () => {
      slot.hour = clampNum(hh, 12); // 区间读 hh.min/max（0~23），越界已回写
      opts.onChange();
    });
    mm.addEventListener("change", () => {
      slot.minute = clampNum(mm, 0); // 区间读 mm.min/max（0~59），越界已回写
      opts.onChange();
    });
    time.append(hh, hhText, mm, mmText);
    const repeat = document.createElement("select");
    repeat.className = "tk-repeat";
    for (const opt of [{ v: "daily", t: "每天" }, { v: "weekly", t: "每周" }]) {
      const o = document.createElement("option");
      o.value = opt.v;
      o.textContent = opt.t;
      repeat.appendChild(o);
    }
    repeat.value = slot.repeat;
    const days = document.createElement("div");
    days.className = "tk-days" + (slot.repeat === "weekly" ? "" : " hidden");
    const chips = [];
    for (const w of WEEKDAYS) {
      const lab = document.createElement("label");
      lab.className = "tk-day" + (slot.days.includes(w.value) ? " on" : "");
      lab.title = w.label;
      const box = document.createElement("input");
      box.type = "checkbox";
      box.checked = slot.days.includes(w.value);
      box.addEventListener("change", () => {
        slot.days = box.checked ? [.../* @__PURE__ */ new Set([...slot.days, w.value])].sort((a, b) => a - b) : slot.days.filter((v) => v !== w.value);
        lab.classList.toggle("on", box.checked);
        opts.onChange();
      });
      const txt = document.createElement("span");
      txt.textContent = w.short;
      lab.append(box, txt);
      days.appendChild(lab);
      chips.push({ value: w.value, box, label: lab });
    }
    repeat.addEventListener("change", () => {
      slot.repeat = repeat.value === "weekly" ? "weekly" : "daily";
      if (slot.repeat === "weekly" && slot.days.length === 0) {
        slot.days = [...DEFAULT_WEEKDAYS];
        for (const c of chips) {
          c.box.checked = slot.days.includes(c.value);
          c.label.classList.toggle("on", c.box.checked);
        }
      }
      if (slot.repeat === "daily") slot.days = [];
      days.classList.toggle("hidden", slot.repeat !== "weekly");
      opts.onChange();
    });
    const del = document.createElement("button");
    del.className = "btn mini danger";
    del.textContent = "✕";
    del.title = "删除这条提醒";
    del.addEventListener("click", () => {
      void confirmDialog({
        title: `删除${opts.what}？`,
        desc: "删除后立即生效，无法撤销。",
        detail: describeTimeSlot(slot),
        okText: "删除"
      }).then((ok) => {
        if (ok) opts.onRemove(slot);
      });
    });
    row.append(on, time, repeat, days, del);
    return row;
  }
  function renderSlotList(listEl, slots, emptyText, opts) {
    if (!listEl) return;
    listEl.innerHTML = "";
    if (slots.length === 0) {
      const empty = document.createElement("div");
      empty.className = "tk-empty";
      empty.textContent = emptyText;
      listEl.appendChild(empty);
      return;
    }
    for (const slot of slots) listEl.appendChild(renderSlotRow(slot, opts));
  }
  function renderTakeoutList() {
    const t = S.takeout;
    renderSlotList($("tk-list"), t.slots, "还没有外卖提醒时间，点标题行右侧的“＋”新增一条", {
      what: "这条外卖提醒",
      onChange: () => {
        updateTakeoutSummary();
        autoSave();
        refreshStatusNow();
      },
      onRemove: (slot) => {
        t.slots = t.slots.filter((s) => s !== slot);
        renderTakeoutList();
        autoSave();
        refreshStatusNow();
      }
    });
    updateTakeoutSummary();
  }
  function shortBreakCard() {
    for (const c of document.querySelectorAll("#tab-timing > .card")) {
      const h = c.querySelector(":scope > .card-head h3");
      if (h && h.textContent === "短休息") return c;
    }
    return null;
  }
  function customCardEl(id) {
    return document.querySelector(`#tab-timing > .card[data-cb="${id}"]`);
  }
  function buildCustomBreakCard(cb) {
    const src = shortBreakCard();
    if (!src) return null;
    const card = src.cloneNode(true);
    const rewrite = (el) => {
      if (el.id && (el.id === "short" || el.id.startsWith("short-"))) {
        el.id = cb.id + el.id.slice("short".length);
      }
      for (const attr of ["data-break", "data-kind"]) {
        if (el.getAttribute(attr) === "short") el.setAttribute(attr, cb.id);
      }
      for (const child of Array.from(el.children)) rewrite(child);
    };
    rewrite(card);
    card.dataset.cb = cb.id;
    card.classList.add("card-custom");
    const head = card.querySelector(":scope > .card-head");
    head.querySelector("h3").textContent = cb.name;
    const sum = head.querySelector(".adv-summary");
    sum.textContent = "—";
    const actions = document.createElement("div");
    actions.className = "card-actions";
    sum.replaceWith(actions);
    actions.appendChild(sum);
    // 重命名 / 删除：图标按钮（✎ / ✕，与“提醒内容”行尾的编辑 / 删除同款），
    // 重命名在左、删除在右；改名后即时同步卡片标题与今日时间线。
    const rename = mkBtn("✎", "重命名", () => void renameCustomBreak(cb.id));
    rename.classList.add("cb-rename");
    actions.appendChild(rename);
    const del = mkBtn("✕", "删除", () => void removeCustomBreak(cb.id));
    del.classList.add("cb-del", "danger");
    actions.appendChild(del);
    const pvBtn = card.querySelector(`#${cb.id}-preview`);
    if (pvBtn) {
      pvBtn.addEventListener("click", async () => {
        autoSave();
        await previewBreak(cb.id);
      });
    }
    // “启用”下一行：暂停休息期间怎么处置（短 / 长休息没有这一行，保持原状）。
    // 必须跟在克隆之后插：短休息卡片本身没有这个控件，模板只作骨架。
    // 标签后的“?”说明“暂停休息”包括哪几种情况，下拉每一项自己也带一个“?”。
    const pauseRow = document.createElement("div");
    pauseRow.className = "row field";
    const pauseLabel = document.createElement("label");
    pauseLabel.append(document.createTextNode("暂停休息时"),
      mkQMark("pause-scope-help", pauseScopeHint()));
    const pauseHost = document.createElement("div");
    pauseHost.id = `${cb.id}-pause`;
    pauseRow.append(pauseLabel, pauseHost);
    card.querySelector(":scope > .row.check.has-btn")?.after(pauseRow);
    mountPauseDrop(pauseHost, cb.config?.pauseAction);
    card.querySelector(".tk-list").innerHTML = "";
    const msgList = card.querySelector(".msg-list");
    msgList.innerHTML = "";
    const trailingHint = msgList.nextElementSibling;
    if (trailingHint && trailingHint.tagName === "P") trailingHint.remove();
    const panel = card.querySelector(".msg-panel");
    panel.classList.remove("open");
    panel.querySelector(".msg-body").classList.add("hidden");
    const slotFold = card.querySelector(":scope > .slot-fixed");
    slotFold?.classList.remove("open");
    slotFold?.querySelector(".slot-body")?.classList.add("hidden");
    return card;
  }
  function setupCustomBreakCard(card, cb) {
    const id = cb.id;
    // “新增”出的卡片插在 bindAutoSave 之后，全局绑定扫不到它：卡内全部
    // input/select 在这里显式补绑。漏绑的后果是触发方式/间隔/时长等改动不落盘、
    // 调度器不重算 ——“倒计时切固定时间且无时刻”后下次提醒与今日时间线
    // 仍显示按倒计时算出的旧时刻（bug 实测来源）。
    for (const el of card.querySelectorAll("input, select")) bindAutoSaveEl(el);
    // 触发方式改成自绘下拉（与“暂停休息时”同款）：整卡是克隆来的，克隆出的
    // 下拉节点没有事件，这里按新宿主重新挂一遍实例（选中即联动 + 存盘）。
    mountBreakModeDrop(id);
    $(`${id}-add`).addEventListener("click", async () => {
      const c = breakCfg(id);
      if (!c) return;
      const slot = newSlot();
      c.slots.push(slot);
      renderBreakList(id);
      await autoSave();
      refreshStatusNow();
      toast(`已添加 ${slotTimeText(slot)} 的${cb.name}`);
    });
    for (const suffix of ["enabled", "interval"]) {
      $(`${id}-${suffix}`).addEventListener("change", () => updateBreakSummary(id));
    }
    setupMsgPanel(card.querySelector(".msg-panel"));
  }
  function renderCustomCard(cb) {
    const addCard = $("cb-add-card");
    const card = buildCustomBreakCard(cb);
    if (!addCard || !card) return;
    customCardEl(cb.id)?.remove();
    addCard.before(card);
    setupCustomBreakCard(card, cb);
    fillBreakForm(cb.id);
    renderMsgList(cb.id);
  }
  function renderCustomCards() {
    for (const el of document.querySelectorAll("#tab-timing > .card[data-cb]")) el.remove();
    const addCard = $("cb-add-card");
    if (!addCard) return;
    for (const cb of S.customBreaks ?? []) {
      const card = buildCustomBreakCard(cb);
      if (!card) continue;
      addCard.before(card);
      setupCustomBreakCard(card, cb);
    }
  }
  // 已被占用的提醒名称：内置（短休息/长休息/外卖提醒）+ 已有自定义提醒名称；新增时不得与之重名。
  function reservedReminderNames() {
    const names = ["短休息", "长休息", "外卖提醒"];
    for (const cb of S.customBreaks ?? []) names.push(cb.name);
    return names;
  }
  async function addCustomBreak() {
    const input = await promptDialog({
      title: "新建自定义提醒",
      label: "提醒名称：",
      placeholder: "例如：喝水提醒",
      okText: "创建"
    });
    if (input === null) return;
    const name = input.trim().slice(0, 24);
    if (!name) {
      toast("名称不能为空");
      return;
    }
    if (reservedReminderNames().includes(name)) {
      toast(`提醒名称“${name}”已存在，请换一个`);
      return;
    }
    const cb = {
      id: `${CUSTOM_BREAK_PREFIX}${newId()}`,
      name,
      config: defaultCustomBreakConfig(),
      // 提醒内容**默认为空**：不预置任何内置条目，用户自己加几条就是几条（默认固定显示）
      messages: { order: "fixed", fixedId: "", items: [] }
    };
    S.customBreaks = [...S.customBreaks ?? [], cb];
    renderCustomCard(cb);
    applyBreakModeUI();
    autoSave();
    refreshStatusNow();
    toast(`已新建自定义提醒“${name}”`);
  }
  // 重命名自定义提醒：复用新建时的输入弹窗（预填当前名并自动全选，直接输入即覆盖）。
  // 校验口径与新建一致：非空 / ≤24 字 / 不与内置和其他自定义提醒重名；
  // reservedReminderNames 含自身当前名，故“原名直接确认”已在上面提前返回，不会误报重名。
  async function renameCustomBreak(id) {
    const cb = (S.customBreaks ?? []).find((c) => c.id === id);
    if (!cb) return;
    const input = await promptDialog({
      title: "重命名自定义提醒",
      label: "提醒名称：",
      value: cb.name,
      okText: "保存"
    });
    if (input === null) return;
    const name = input.trim().slice(0, 24);
    if (!name) {
      toast("名称不能为空");
      return;
    }
    if (name === cb.name) return; // 没改：不落盘、不提示
    if (reservedReminderNames().includes(name)) {
      toast(`提醒名称“${name}”已存在，请换一个`);
      return;
    }
    cb.name = name;
    const h3 = customCardEl(id)?.querySelector(":scope > .card-head h3");
    if (h3) h3.textContent = name;
    autoSave();
    refreshStatusNow();
    toast(`已重命名为“${name}”`);
  }
  async function removeCustomBreak(id) {
    const cb = (S.customBreaks ?? []).find((c) => c.id === id);
    if (!cb) return;
    const ok = await confirmDialog({
      title: `删除自定义提醒“${cb.name}”？`,
      desc: "删除后立即生效，无法撤销。",
      detail: cb.config.mode === "fixed" ? `固定时间 \xB7 ${cb.config.slots.length} 条时刻` : `倒计时 \xB7 每 ${Math.round(cb.config.intervalMs / 6e4)} 分钟一次`,
      okText: "删除"
    });
    if (!ok) return;
    S.customBreaks = (S.customBreaks ?? []).filter((c) => c.id !== id);
    customCardEl(id)?.remove();
    applyBreakModeUI();
    autoSave();
    refreshStatusNow();
    toast(`已删除自定义提醒“${cb.name}”`);
  }
  function allBreakIds() {
    return ["short", "long", ...(S.customBreaks ?? []).map((c) => c.id)];
  }
  function breakCfg(id) {
    return breakConfigById(S, id);
  }
  function activeSlotCount(id) {
    return (breakCfg(id)?.slots ?? []).filter((s) => s.enabled).length;
  }
  function updateBreakSummary(id) {
    const el = $(`${id}-summary`);
    const c = breakCfg(id);
    if (!el || !c) return;
    if (!c.enabled) {
      el.textContent = "已关闭";
      el.classList.remove("customized");
      return;
    }
    if (c.mode === "countdown") {
      el.textContent = `倒计时 \xB7 每 ${Math.round(c.intervalMs / 6e4)} 分钟一次`;
      el.classList.add("customized");
      return;
    }
    const n = activeSlotCount(id);
    if (n === 0) {
      el.textContent = "固定时间 \xB7 还没有提醒时刻";
      el.classList.remove("customized");
      return;
    }
    const plannedAt = LAST_STATE?.fixedNext?.[id];
    const next = plannedAt !== void 0 ? { at: plannedAt } : nextSlotAt(c.slots);
    el.textContent = `固定时间 \xB7 ${n} 条 \xB7 ${slotNextText(next)}`;
    el.classList.add("customized");
  }
  function renderBreakList(id) {
    const c = breakCfg(id);
    if (!c) return;
    renderSlotList($(`${id}-list`), c.slots, "还没有提醒时间，点标题行右侧的“＋”新增一条", {
      what: "这条休息时刻",
      onChange: () => {
        updateBreakSummary(id);
        autoSave();
        refreshStatusNow();
      },
      onRemove: (slot) => {
        c.slots = c.slots.filter((s) => s !== slot);
        renderBreakList(id);
        autoSave();
        refreshStatusNow();
      }
    });
    updateBreakSummary(id);
  }
  function fillBreakForm(id) {
    const c = breakCfg(id);
    if (!c) return;
    $(`${id}-enabled`).checked = c.enabled;
    setDropValue($(`${id}-mode`), c.mode);
    $(`${id}-interval`).value = String(Math.round(c.intervalMs / 6e4));
    $(`${id}-duration`).value = id === "long" ? String(Math.round(c.durationMs / 6e4)) : String(Math.round(c.durationMs / 1e3));
    $(`${id}-notify`).value = String(Math.round(c.notifyBeforeMs / 1e3));
    $(`${id}-postpone`).value = String(Math.round(c.postponeMs / 6e4));
    // “暂停休息时”只有自定义提醒卡片有（短 / 长休息的模板本身没有这行）
    setDropValue($(`${id}-pause`), c.pauseAction);
    renderBreakList(id);
  }
  function applyBreakModeUI() {
    for (const id of allBreakIds()) {
      const c = breakCfg(id);
      if (!c) continue;
      const fixed = c.mode === "fixed";
      for (const el of document.querySelectorAll(`.bk-countdown[data-break="${id}"]`)) {
        el.classList.toggle("hidden", fixed);
      }
      for (const el of document.querySelectorAll(`.slot-fixed[data-break="${id}"]`)) {
        el.classList.toggle("hidden", !fixed);
      }
    }
    const bothCountdown = S.short.mode === "countdown" && S.long.mode === "countdown";
    $("longEveryNRow")?.classList.toggle("hidden", !bothCountdown);
  }
  // 分钟框跟着“系统空闲”勾选框走：没勾时它不该还能编辑（关掉的是整条空闲检测，
  // 改分钟数没有意义）。只改 disabled，不另造禁用样式。
  function syncIdlePauseUI() {
    const on = !!$("idlePause")?.checked;
    const min = $("idlePauseMin");
    if (min) min.disabled = !on;
  }
  function fillForm() {
    $("appName").value = S.appName ?? "";
    $("tb-title").textContent = S.appName || "吃饼人也敢休息？";
    const themeVal = S.theme === "light" || S.theme === "dark" ? S.theme : "light";
    document.querySelector(`input[name=theme][value="${themeVal}"]`).checked = true;
    S.theme = themeVal;
    $("idlePauseMin").value = String(Math.max(1, Math.round(S.idlePauseMin)));
    // 缺键按“开”（后端 Flag(true,…) 同口径），所以判据是 !== false，不能写成 !!
    $("idlePause").checked = S.idlePause !== false;
    syncIdlePauseUI();
    $("longEveryNShort").value = String(S.longEveryNShort);
    $("autostart").checked = S.autostart;
    $("startMinimized").checked = S.startMinimized;
    // 自动记录窗口尺寸：老配置里可能没有 window 块（deepMerge 会补，但填表这一层
    // 不能假设后端一定下发），缺了就当“开”——与后端 Flag(true, …) 的默认值一致
    $("win-remember").checked = !(S.window && S.window.rememberSize === false);
    $("dndPause").checked = S.dndPause !== false;
    // 老配置里可能没有 fullscreenPause；后端 deepMerge 会补上出厂 true，
    // 这里不额外兜底，避免与后端下发的实际值不一致
    $("fullscreenPause").checked = !!S.fullscreenPause;
    // 缺键按“开”（后端 Flag(true,…) 同口径），所以判据是 !== false，不能写成 !!
    $("castWirelessPause").checked = S.castWirelessPause !== false;
    $("castMirrorPause").checked = S.castMirrorPause !== false;
    $("chk-update").checked = S.checkUpdateOnStart;
    $("appPause-enabled").checked = S.appPause.enabled;
    renderAppList();
    for (const id of allBreakIds()) fillBreakForm(id);
    applyBreakModeUI();
    const a = S.appearance;
    setColorChip(normalizeHex(a.color) ?? DEFAULT_ACCENT);
    $("ap-opacity").value = String(a.transparency);
    $("ap-opacity-val").textContent = a.transparency.toFixed(2);
    $("ap-widthPct").value = String(a.widthPct);
    $("ap-heightPct").value = String(a.heightPct);
    $("ap-fullscreen").checked = a.fullscreen;
    $("ap-alwaysOnTop").checked = a.alwaysOnTop;
    // 显示器列表必须现场枚举（HMONITOR 每次进程启动都会变，不能缓存上次结果）。
    // 异步挂载，失败时下拉变灰、配置原样保留。
    void refreshDisplayDrop();
    // 共享屏幕时隐藏：老配置里没有这个键，缺省按“开”——与 Go 侧 Flag(true,…) 同口径，
    // 不能写成 !!a.hideOnShare（undefined → false，老用户会被静默关掉保护）
    $("ap-hideOnShare").checked = a.hideOnShare !== false;
    $("snd-enabled").checked = S.sound.enabled;
    $("snd-volume").value = String(S.sound.volume);
    $("snd-volume-val").textContent = `${Math.round(S.sound.volume * 100)}%`;
    setSoundModeUI(S.sound.mode ?? "single");
    renderSoundMusic();
    $("sup-enabled").checked = S.supervisor.enabled;
    applySelectionUI();
    ensureSupEditKey();
    fillSupervisorImageName();
    fillGeomControls(normalizeSupGeom(geomOf(currentGeomName())));
    $("tk-enabled").checked = S.takeout.enabled;
    mountPauseDrop($("tk-pause"), S.takeout.pauseAction);
    $("tk-lead").value = String(S.takeout.leadSec);
    $("tk-postpone").value = String(S.takeout.postponeMin);
    $("sc-postpone").value = S.shortcuts.postpone;
    $("sc-endEarly").value = S.shortcuts.endEarly;
    $("sc-pause").value = S.shortcuts.pause;
    $("sc-nextBreak").value = S.shortcuts.nextBreak;
    // 回填后立刻判一次：配置文件里被手改出来的“裸键 / 两个动作同键”也要显形
    markShortcuts();
    // “暂停休息时”行标签的“?”说明里带着当前空闲分钟数，首屏就按配置对齐
    refreshPauseHints();
  }
  function collectForm() {
    S.theme = document.querySelector("input[name=theme]:checked").value;
    S.appName = ($("appName").value.trim() || "吃饼人也敢休息？").slice(0, 30);
    $("tb-title").textContent = S.appName;
    S.idlePause = $("idlePause").checked;
    S.idlePauseMin = clampNum($("idlePauseMin"), 5);
    S.longEveryNShort = clampNum($("longEveryNShort"), 2);
    S.autostart = $("autostart").checked;
    S.startMinimized = $("startMinimized").checked;
    // 只回写 rememberSize：lastWidth / lastHeight 由 Go 独占写（窗口 resize 时记录），
    // 这里是打开页面那一刻的旧快照，写回去会把刚记下的尺寸顶掉（后端另有一道
    // keepWindowLastSize 兜底，两边都不许碰）。
    if (!S.window || typeof S.window !== "object") S.window = {};
    S.window.rememberSize = $("win-remember").checked;
    S.dndPause = $("dndPause").checked;
    S.fullscreenPause = $("fullscreenPause").checked;
    S.castWirelessPause = $("castWirelessPause").checked;
    S.castMirrorPause = $("castMirrorPause").checked;
    S.checkUpdateOnStart = $("chk-update").checked;
    S.appPause.enabled = $("appPause-enabled").checked;
    const readBreak = (id) => {
      const c = breakCfg(id);
      if (!c) return;
      c.enabled = $(`${id}-enabled`).checked;
      c.mode = dropValue($(`${id}-mode`), "countdown") === "fixed" ? "fixed" : "countdown";
      c.intervalMs = clampNum($(`${id}-interval`), 10) * 6e4;
      const bySec = id !== "long";
      const dv = clampNum($(`${id}-duration`), 20);
      c.durationMs = bySec ? dv * 1e3 : dv * 6e4;
      c.notifyBeforeMs = clampNum($(`${id}-notify`), 10) * 1e3;
      c.postponeMs = clampNum($(`${id}-postpone`), 2) * 6e4;
      // 暂停休息时：只有自定义提醒卡片有这一行（短 / 长休息不写这个键）
      const pauseHost = $(`${id}-pause`);
      if (pauseHost) c.pauseAction = dropValue(pauseHost, "postpone");
    };
    for (const id of allBreakIds()) readBreak(id);
    const a = S.appearance;
    a.transparency = parseFloat($("ap-opacity").value);
    a.widthPct = clampNum($("ap-widthPct"), 95);
    a.heightPct = clampNum($("ap-heightPct"), 95);
    a.fullscreen = $("ap-fullscreen").checked;
    a.alwaysOnTop = $("ap-alwaysOnTop").checked;
    a.hideOnShare = $("ap-hideOnShare").checked;
    S.displayIds = displayDrop ? displayDrop.get() : (S.displayIds || []);
    S.sound.enabled = $("snd-enabled").checked;
    S.sound.volume = parseFloat($("snd-volume").value);
    S.sound.mode = $("snd-mode").dataset.mode || "single";
    S.supervisor.enabled = $("sup-enabled").checked;
    S.takeout.enabled = $("tk-enabled").checked;
    S.takeout.pauseAction = dropValue($("tk-pause"), "postpone");
    S.takeout.leadSec = clampNum($("tk-lead"), TAKEOUT_LEAD_DEFAULT);
    S.takeout.postponeMin = clampNum($("tk-postpone"), TAKEOUT_POSTPONE_DEFAULT);
    S.shortcuts.postpone = $("sc-postpone").value.trim();
    S.shortcuts.endEarly = $("sc-endEarly").value.trim();
    S.shortcuts.pause = $("sc-pause").value.trim();
    S.shortcuts.nextBreak = $("sc-nextBreak").value.trim();
    // 空闲分钟数可能刚改：把“暂停休息时”行标签的“?”说明同步成新数字
    refreshPauseHints();
  }
  // 数值框取值：区间**只认输入框自己的 min/max**（HTML 属性是唯一真相，代码不再各写
  // 一份区间——早先 short-interval 的 DOM 是 1~240、代码钳 1~480，两边打架）。
  // 越界或留空时钳到区间并**回写**输入框：否则会出现“框里显示 999999、配置里存 240”，
  // 用户看到的值和真正生效的值不一致（P3-1）。
  // 回写发生在 change（失焦 / 回车）或别的控件触发保存时，不打断正在键入的过程。
  function clampNum(el, fallback) {
    if (!el) return fallback;
    let lo = -Infinity;
    let hi = Infinity;
    if (el.min !== "") {
      const n = Number(el.min);
      if (!Number.isNaN(n)) lo = n;
    }
    if (el.max !== "") {
      const n = Number(el.max);
      if (!Number.isNaN(n)) hi = n;
    }
    const raw = parseInt(el.value, 10);
    const v = Number.isNaN(raw) ? fallback : Math.min(hi, Math.max(lo, raw));
    if (el.value !== String(v)) el.value = String(v);
    return v;
  }
  var MSG_KINDS = ["short", "long", "takeout"];
  var MSG_KIND_LABEL = { short: "短休息", long: "长休息", takeout: "外卖提醒" };
  var msgBatchTarget = "short";
  var BUILTIN_MSGS = null;
  // 条目 id = 本列表内的序号（与 Go 侧 nextSeqID 同口径）：取现有最大整数 +1。
  // 只要求本列表内唯一且稳定（固定模式用它选条，删除/拖动用它定位），不需要全局唯一串。
  function nextMsgId(items) {
    let max = 0;
    const used = new Set();
    for (const it of items ?? []) {
      const s = it == null ? "" : String(it.id ?? "");
      used.add(s);
      if (!/^\d+$/.test(s)) continue;
      const n = Number(s);
      if (n > max) max = n;
    }
    // 跳过已被占用的号（后端收敛前的残留 id 万一是数字又撞上）
    let n = max + 1;
    while (used.has(String(n))) n += 1;
    return String(n);
  }
  function msgCfgOf(target) {
    const custom = (S.customBreaks ?? []).find((c) => c.id === target);
    if (custom) return custom.messages;
    if (target !== "sup") {
      const kind = target;
      const cfg = S.reminderMessages[kind] ?? (S.reminderMessages[kind] = { order: "fixed", fixedId: "", items: [] });
      if (!cfg.seeded) {
        cfg.seeded = true;
        if (cfg.items.length === 0 && BUILTIN_MSGS) {
          cfg.items = BUILTIN_MSGS.kinds[kind].map((i) => ({ id: i.id, text: i.text, enabled: true }));
          if (cfg.order === "fixed" && !cfg.fixedId) cfg.fixedId = cfg.items[0]?.id ?? "";
        }
      }
      return cfg;
    }
    const key = currentGeomName();
    if (!key) return null;
    const role = ROLES.find((r) => r.name === key);
    if (!role) return null;
    if (!role.messages || !Array.isArray(role.messages.items)) {
      role.messages = { order: "random", items: [] };
    }
    return role.messages;
  }
  function msgTargetLabel(target) {
    if (target === "sup") return currentRoleName();
    const custom = (S.customBreaks ?? []).find((c) => c.id === target);
    return custom ? custom.name : MSG_KIND_LABEL[target] ?? target;
  }
  function renderMsgList(target) {
    const panel = document.querySelector(`.msg-panel[data-kind="${target}"]`);
    if (!panel) return;
    const list = panel.querySelector(".msg-list");
    const cfg = msgCfgOf(target);
    const order = cfg?.order ?? "fixed";
    const fixedMode = order === "fixed";
    const modeBtn = panel.querySelector(".snd-mode-btn");
    if (modeBtn) applyModeIcon(modeBtn, order);
    list.innerHTML = "";
    if (!cfg) {
      const d = document.createElement("div");
      d.className = "msg-empty";
      d.textContent = "正在识别当前立绘，请稍候…";
      list.appendChild(d);
      return;
    }
    if (cfg.items.length === 0) {
      const d = document.createElement("div");
      d.className = "msg-empty";
      d.textContent = target === "takeout" ? "暂无提醒内容：外卖提醒窗口里将显示“外卖提醒”" : target === "sup" ? "暂无提醒内容：升级阶段将沿用第一阶段的台词" : "暂无提醒内容：提醒窗口里将显示该提醒的名称";
      list.appendChild(d);
      return;
    }
    for (const item of cfg.items) list.appendChild(buildMsgRow(target, item, fixedMode));
  }
  // 退出所有“提醒内容”列表中仍处于编辑态的行（折叠 / 切页 / 关窗时调用）
  function exitAllMsgEditing() {
    document.querySelectorAll(".msg-item.editing").forEach((row) => {
      if (typeof row._cancelEdit === "function") row._cancelEdit();
    });
  }
  function buildMsgRow(target, item, fixedMode) {
    const cfg = msgCfgOf(target);
    const row = document.createElement("div");
    row.className = "msg-item" + (!fixedMode && !item.enabled ? " disabled" : "");
    row.dataset.id = item.id;
    const handle = document.createElement("span");
    handle.className = "msg-handle";
    handle.title = "拖动调整顺序";
    handle.textContent = "⠿";
    let ctrl;
    if (fixedMode) {
      ctrl = document.createElement("input");
      ctrl.type = "radio";
      ctrl.name = `msg-single-${target}`;
      ctrl.checked = item.id === cfg?.fixedId;
      ctrl.title = "固定显示这一条";
      ctrl.addEventListener("change", () => {
        if (!ctrl.checked || !cfg) return;
        cfg.fixedId = item.id;
        autoSave();
        if (target === "sup") void refreshSupervisorPreview(true);
      });
    } else {
      ctrl = document.createElement("input");
      ctrl.type = "checkbox";
      ctrl.checked = item.enabled;
      ctrl.title = "启用/停用：是否参与随机 / 顺序播放";
      ctrl.addEventListener("change", () => {
        item.enabled = ctrl.checked;
        row.classList.toggle("disabled", !ctrl.checked);
        autoSave();
        if (target === "sup") void refreshSupervisorPreview(true);
      });
    }
    const txt = document.createElement("input");
    txt.type = "text";
    txt.className = "msg-text";
    txt.value = item.text;
    txt.placeholder = "提醒内容（纯文本）";
    txt.readOnly = true; // 默认不可编辑，点“编辑”图标才进入编辑态
    const ICON_EDIT = "✎", ICON_DEL = "✕", ICON_SAVE = "✓", ICON_CANCEL = "↺";
    let editing = false;
    function setButtonsToEdit() {
      editBtn.textContent = ICON_EDIT; editBtn.title = "编辑";
      delBtn.textContent = ICON_DEL; delBtn.title = "删除";
    }
    function setButtonsToSave() {
      editBtn.textContent = ICON_SAVE; editBtn.title = "保存";
      delBtn.textContent = ICON_CANCEL; delBtn.title = "取消";
    }
    function enterEdit() {
      if (editing) return;
      editing = true;
      row.classList.add("editing");
      txt.readOnly = false;
      txt.focus();
      txt.select();
      setButtonsToSave();
    }
    function cancelEdit() {
      editing = false;
      txt.value = item.text; // 丢弃未保存改动，恢复为原文本
      txt.readOnly = true;
      row.classList.remove("editing");
      setButtonsToEdit();
    }
    function saveEdit() {
      editing = false;
      item.text = txt.value;
      txt.readOnly = true;
      row.classList.remove("editing");
      setButtonsToEdit();
      autoSave();
      if (target === "sup") void refreshSupervisorPreview(true);
    }
    function deleteRow() {
      void confirmDialog({
        title: "删除这条提醒内容？",
        desc: "删除后立即生效，无法撤销。",
        detail: item.text || "（空）",
        okText: "删除"
      }).then((ok) => {
        if (!ok) return;
        const c = msgCfgOf(target);
        if (!c) return;
        c.items = c.items.filter((i) => i.id !== item.id);
        renderMsgList(target);
        autoSave();
        if (target === "sup") void refreshSupervisorPreview(true);
      });
    }
    const editBtn = document.createElement("button");
    editBtn.type = "button";
    editBtn.className = "btn mini";
    editBtn.textContent = ICON_EDIT;
    editBtn.title = "编辑";
    const delBtn = document.createElement("button");
    delBtn.type = "button";
    delBtn.className = "btn mini danger";
    delBtn.textContent = ICON_DEL;
    delBtn.title = "删除";
    editBtn.addEventListener("click", (e) => {
      e.stopPropagation();
      if (editing) saveEdit(); else enterEdit();
    });
    delBtn.addEventListener("click", (e) => {
      e.stopPropagation();
      if (editing) cancelEdit(); else deleteRow();
    });
    row.append(handle, ctrl, txt, editBtn, delBtn);
    handle.addEventListener("mousedown", () => {
      row.draggable = true;
    });
    row.addEventListener("dragstart", (e) => {
      row.classList.add("dragging");
      if (e.dataTransfer) {
        e.dataTransfer.setData("text/plain", item.id);
        e.dataTransfer.effectAllowed = "move";
      }
    });
    row.addEventListener("dragend", () => {
      row.classList.remove("dragging");
      row.draggable = false;
    });
    row.addEventListener("dragover", (e) => {
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
      row.classList.add("drop-target");
    });
    row.addEventListener("dragleave", () => row.classList.remove("drop-target"));
    row.addEventListener("drop", (e) => {
      e.preventDefault();
      row.classList.remove("drop-target");
      const dragId = e.dataTransfer?.getData("text/plain");
      if (!dragId || dragId === item.id) return;
      const cfg2 = msgCfgOf(target);
      if (!cfg2) return;
      const from = cfg2.items.findIndex((i) => i.id === dragId);
      const to = cfg2.items.findIndex((i) => i.id === item.id);
      if (from < 0 || to < 0) return;
      const [moved] = cfg2.items.splice(from, 1);
      cfg2.items.splice(to, 0, moved);
      renderMsgList(target);
      autoSave();
    });
    // 供“折叠 / 切页 / 关窗”统一退出编辑态调用
    row._cancelEdit = cancelEdit;
    // 整行点击：非编辑态下，点行任意处（含只读文本框）勾选 / 取消勾选本行
    row.addEventListener("click", (e) => {
      if (editing) return; // 编辑态不触发整行勾选，避免干扰输入
      if (e.target.closest(".btn") || e.target === ctrl || e.target === handle) return;
      if (fixedMode) {
        if (cfg && cfg.fixedId === item.id) return; // 已是当前固定项
        if (cfg) cfg.fixedId = item.id;
        autoSave();
        renderMsgList(target); // 重绘以正确反映单选互斥
        if (target === "sup") void refreshSupervisorPreview(true);
      } else {
        item.enabled = !item.enabled;
        ctrl.checked = item.enabled;
        row.classList.toggle("disabled", !item.enabled);
        autoSave();
        if (target === "sup") void refreshSupervisorPreview(true);
      }
    });
    return row;
  }
  function renderAllMsgLists() {
    for (const kind of MSG_KINDS) renderMsgList(kind);
    for (const cb of S.customBreaks ?? []) renderMsgList(cb.id);
    renderMsgList("sup");
  }
  var msgPanelBound = /* @__PURE__ */ new WeakSet();
  function setupMsgPanel(panel) {
    if (msgPanelBound.has(panel)) return;
    msgPanelBound.add(panel);
    const target = panel.dataset.kind ?? "short";
    panel.querySelector(".msg-toggle").addEventListener("click", () => {
      if (panel.classList.contains("open")) exitAllMsgEditing();
      panel.classList.toggle("open");
      panel.querySelector(".msg-body").classList.toggle("hidden");
    });
    const modeBtn = panel.querySelector(".snd-mode-btn");
    if (modeBtn) {
      modeBtn.addEventListener("click", () => {
        const cfg = msgCfgOf(target);
        if (!cfg) return;
        const cur = modeBtn.dataset.mode || cfg.order || "fixed";
        const next = DISPLAY_MODE_NEXT[cur] || "fixed";
        cfg.order = next;
        applyModeIcon(modeBtn, next);
        renderMsgList(target);
        autoSave();
      });
    }
    panel.querySelector(".msg-add").addEventListener("click", () => {
      msgBatchTarget = target;
      $("batch-title").textContent = `批量新增提醒内容（${msgTargetLabel(target)}）`;
      $("msg-batch-text").value = "";
      $("batch-modal").classList.remove("hidden");
      $("msg-batch-text").focus();
    });
  }
  function setupMsgPanels() {
    renderAllMsgLists();
    for (const panel of document.querySelectorAll(".msg-panel")) setupMsgPanel(panel);
  }
  function mkBtn(label, title, onClick) {
    const b = document.createElement("button");
    b.className = "btn mini";
    b.textContent = label;
    b.title = title;
    b.addEventListener("click", onClick);
    return b;
  }
  function acceleratorFromEvent(e) {
    if (["Control", "Shift", "Alt", "Meta"].includes(e.key)) return "";
    const parts = [];
    if (e.ctrlKey) parts.push("Ctrl");
    if (e.altKey) parts.push("Alt");
    if (e.shiftKey) parts.push("Shift");
    if (e.metaKey) parts.push("Super");
    const k = e.key.length === 1 ? e.key.toUpperCase() : e.key;
    parts.push(k);
    return parts.join("+");
  }
  // 快捷键四行的 id / 显示名 / 录入提示。顺序 = Go 侧 hotkeyDefs 的注册次序：
  // 撞车时先注册的那个能生效，后面的注册失败（所以“重复”只标后一行）。
  var SHORTCUT_IDS = ["sc-postpone", "sc-endEarly", "sc-pause", "sc-nextBreak"];
  var SHORTCUT_LABELS = {
    "sc-postpone": "推迟当前休息",
    "sc-endEarly": "提前结束休息",
    "sc-pause": "暂停 / 恢复休息",
    "sc-nextBreak": "跳转到下一次休息"
  };
  var SHORTCUT_MODS = ["Ctrl", "Alt", "Shift", "Super"];
  var SHORTCUT_TITLE_OK = "点击后按下组合键录入；留空表示禁用；Esc 清空";
  var SHORTCUT_NO_MOD = "需要至少一个修饰键（Ctrl / Alt / Shift / Super）";
  // 一道判断两处用：录入时挡下、回填后标红。裸键（A / F5 / 空格）注册成全局热键会把
  // 正常打字全吃掉，Go 侧 parseAccelerator 本来就拒绝 —— 这里必须当场说清楚，
  // 而不是让用户录进去一个永远不生效的键（P3-4）。
  function shortcutIssue(id) {
    const input = $(id);
    const acc = input ? input.value.trim() : "";
    if (!acc) return "";
    const parts = acc.split("+");
    if (parts.length < 2 || !SHORTCUT_MODS.includes(parts[0])) return SHORTCUT_NO_MOD;
    for (const other of SHORTCUT_IDS) {
      if (other === id) continue;
      if (SHORTCUT_IDS.indexOf(other) > SHORTCUT_IDS.indexOf(id)) continue;
      if (($(other)?.value ?? "").trim() === acc) {
        return `和“${SHORTCUT_LABELS[other]}”用了同一个组合键（只有它生效）`;
      }
    }
    return "";
  }
  // 标红 + 汇总说明。录入时、失焦改值、以及从配置回填后各跑一次：
  // 手改过 settings.json 弄出的重复 / 裸键也能被看见，而不是“配了却没反应”。
  function markShortcuts() {
    const msgs = [];
    for (const id of SHORTCUT_IDS) {
      const input = $(id);
      if (!input) continue;
      const issue = shortcutIssue(id);
      input.classList.toggle("bad", !!issue);
      input.title = issue || SHORTCUT_TITLE_OK;
      if (issue) msgs.push(`“${SHORTCUT_LABELS[id]}”${issue}`);
    }
    const warn = $("sc-warn");
    if (warn) {
      warn.textContent = msgs.join("；");
      warn.classList.toggle("hidden", msgs.length === 0);
    }
  }
  function setupShortcutCapture() {
    for (const id of SHORTCUT_IDS) {
      const input = $(id);
      input.addEventListener("keydown", (e) => {
        e.preventDefault();
        e.stopPropagation();
        if (e.key === "Escape") {
          input.value = "";
          input.blur();
          markShortcuts();
          autoSave();
          return;
        }
        const acc = acceleratorFromEvent(e);
        if (!acc) return; // 只按住了修饰键：继续等主键
        // 不合法的组合键**整条拒收**（框里保持原值）而不是写进去再标红：
        // 后者会把它顺手存进配置，等于给了用户一个永远不生效的键。
        // 拒收时用 toast 说清原因，录入框保持聚焦，直接重按即可。
        const prev = input.value;
        input.value = acc;
        const issue = shortcutIssue(id);
        if (issue) {
          input.value = prev;
          markShortcuts();
          toast(issue);
          return;
        }
        markShortcuts();
        input.blur();
        autoSave();
      });
      // 失焦 / 粘贴改值也要重新判定：否则红框和说明会停在旧状态
      input.addEventListener("change", markShortcuts);
    }
  }
  function fmtRemain(ms) {
    const s = Math.max(0, Math.ceil(ms / 1e3));
    if (s < 60) return `${s}秒`;
    const m = Math.floor(s / 60);
    const r = s % 60;
    if (m < 60) return r > 0 ? `${m}分${r}秒` : `${m}分`;
    const h = Math.floor(m / 60);
    const mm = m % 60;
    let out = `${h}小时`;
    if (mm > 0) out += `${mm}分`;
    if (r > 0) out += `${r}秒`;
    return out;
  }
  // 定时暂停的结束时刻（24 小时制）。只用于状态格的悬停提示，不参与判定。
  function fmtClockMs(ms) {
    const d = new Date(ms);
    return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
  }
  var LAST_STATE = null;
  // 侧栏状态格字号自适应：名称（左）与时间（右）都必须单行，但侧栏只有 240px，
  // “下次报警日报提醒”+“今日已无提醒”在 12/15px 下就超出卡片内宽。这里按实测宽度
  // 逐档缩字号（名称 12→8px、时间 15→10px，两者同步缩以保持层级），缩到底再收窄列间距
  // （16→6px）；8px 仍放不下就中间省略 + title 显示全名（240px 装不下 20 个以上的中文字）。
  // 全程不换行、不裁半截字、不把文字挤出卡片。
  // force=true 用于 resize（可用宽变了要重算）；MutationObserver 走默认值，每格只算一次。
  function fitStatusCells(force) {
    const host = $("data-status");
    if (!host) return;
    for (const cell of host.querySelectorAll(".status-cell")) {
      const k = cell.querySelector(".k");
      const v = cell.querySelector(".v");
      if (!k || !v) continue;
      // 每个节点只算一次：省略要改 textContent，改完会再触发一次 MutationObserver，
      // 不设这道闸就会在“还原全名 → 又溢出 → 再省略”之间无限抖动（实测直接把页面卡死）。
      // renderStatus 重建节点时标记自然消失，所以不会漏算。
      if (!force && cell.dataset.fitDone === "1") continue;
      cell.dataset.fitDone = "1";
      const cs = getComputedStyle(cell);
      const avail = Math.round(cell.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight));
      delete k.dataset.truncated;
      // 全名永远从 dataset.full 取（renderStatus 重建节点时它自然丢掉，重新开始）
      const full = k.dataset.full || k.textContent;
      k.dataset.full = full;
      k.textContent = full;
      k.removeAttribute("title");
      // 复位：resize 重入时必须从基准字号重新量
      k.style.fontSize = "";
      v.style.fontSize = "";
      cell.style.columnGap = "";
      const kBase = parseFloat(getComputedStyle(k).fontSize) || 12;
      const vBase = parseFloat(getComputedStyle(v).fontSize) || 15;
      const kMin = Math.min(kBase, 8);
      const vMin = Math.min(vBase, 10);
      let kf = kBase, vf = vBase, gap = 16, guard = 0;
      const need = () => k.scrollWidth + v.scrollWidth + gap;
      while (need() > avail && guard++ < 80) {
        if (kf > kMin || vf > vMin) {
          kf = Math.max(kMin, kf - 0.5);
          vf = Math.max(vMin, vf - 0.5);
          k.style.fontSize = kf + "px";
          v.style.fontSize = vf + "px";
        } else if (gap > 6) {
          gap -= 2;
          cell.style.columnGap = gap + "px";
        } else {
          break; // 缩到下限仍放不下 → 走下面的省略
        }
      }
      if (k.scrollWidth > k.clientWidth + 1 && full.length > 3) {
        k.title = full;
        // 中间省略：保留“下次…提醒”首尾，二分找能放下的最长长度
        let lo = 2, hi = full.length - 1, best = "…";
        while (lo <= hi) {
          const mid = (lo + hi) >> 1;
          const head = Math.ceil(mid / 2);
          const tail = mid - head;
          const cand = full.slice(0, head) + "…" + (tail > 0 ? full.slice(full.length - tail) : "");
          k.textContent = cand;
          if (k.scrollWidth <= k.clientWidth + 1) { best = cand; lo = mid + 1; } else { hi = mid - 1; }
        }
        k.textContent = best;
        k.dataset.truncated = "1";
      }
    }
  }
  window.addEventListener("resize", () => {
    clearTimeout(fitStatusCells._t);
    fitStatusCells._t = setTimeout(() => fitStatusCells(true), 120);
  });
  // 侧栏状态格的内容会随提醒名变化（renderStatus 重建节点、名称被改写），
  // 只监听 childList 会漏掉“文本在原地变长”，所以连 characterData 一起盯。
  // options 里不放 attributes：fitStatusCells 自己会改 style，否则会自激。
  (function watchStatusCells() {
    const host = $("data-status");
    if (!host || !window.MutationObserver) return;
    new MutationObserver(() => fitStatusCells()).observe(host, {
      childList: true, subtree: true, characterData: true,
    });
  })();
  function renderStatus(st) {
    LAST_STATE = st;
    // “暂停休息/恢复休息”互斥显示：暂停中只显示“恢复休息”，否则只显示“暂停休息”
    const qaPause = $("qa-pause");
    if (qaPause) qaPause.textContent = st.paused ? "恢复休息" : "暂停休息";
    // “推迟休息”按钮：当前下次提醒的“可推迟”为 0 分钟时不显示 —— 推迟 0 分钟
    // 等于立即再弹一次，没有意义。提前提示窗 / 外卖窗口的“推迟”按钮同样按此显隐。
    // 休息进行中也不显示（已经在休息了，没有“再往后推”这回事）。
    const inBreak = st.phase === "break";
    // 休息中：“立即休息”无从谈起，禁用而不是让点了没反应；
    // “跳过本次”在休息中等价于提前结束（与托盘“提前结束休息”同义，
    // 判定落在 scheduler.skip：休息相位直接结束本次休息）。
    const qaBreak = $("qa-break");
    if (qaBreak) qaBreak.disabled = inBreak;
    const qaSkip = $("qa-skip");
    if (qaSkip) qaSkip.textContent = inBreak ? "提前结束" : "跳过本次";
    const qaPostpone = $("qa-postpone");
    if (qaPostpone) {
      const cfg = st.nextType ? breakCfg(st.nextType) : null;
      qaPostpone.classList.toggle("hidden", inBreak || !(cfg && cfg.postponeMs > 0));
    }
    const phaseText = st.phase === "break" ? "休息中" : st.phase === "pre" ? "即将休息" : "努力工作中";
    // 暂停统一显示“暂停休息”：不论是手动暂停、系统空闲（人离开工位）、系统勿扰
    // 还是指定应用占用运行，状态格只给一个结论；具体原因挂到该格的 title 上悬停可看
    // （托盘 tooltip 里照旧是“已暂停：原因”）。
    const phaseVal = st.paused ? "暂停休息" : phaseText;
    // 定时暂停多一句“到 X 自动结束”：状态格只写“暂停休息”的话，过一会儿就忘了
    // 自己设过时长（托盘那边写的是剩余时间，两处口径不同，但指的是同一件事）。
    const pauseUntilText = st.paused && st.pauseDeadline > 0
      ? "；到 " + fmtClockMs(st.pauseDeadline) + " 自动结束"
      : "";
    const pauseTitle = st.paused && st.pauseReasons.length
      ? "暂停原因：" + st.pauseReasons.map(pauseReasonText).join("、") + pauseUntilText
      : "";
    const now = st.paused && !(st.phase === "break" && st.breakWhilePaused) ? st.pausedAt : Date.now();
    const otherKey = (name) => name.endsWith("提醒") ? `下次${name}` : `下次${name}提醒`;
    const todayOnly = (at) => dayDiffOf(at) > 0 ? "今日已无提醒" : fmtRemain(Math.max(0, at - now));
    // “下次提醒”取外卖/自定义里最近的一次（st.other 已合并两者）；
    // 仅当该最近提醒落在“今天”（at 在当天剩余时间）才显示，否则隐藏整格。
    const otherCell = st.other && dayDiffOf(st.other.at) === 0
      ? [otherKey(st.other.name), todayOnly(st.other.at)]
      : null;
    const cells = [["状态", phaseVal, pauseTitle]];
    if (st.phase === "break") {
      cells.push(["正在休息", fmtRemain(Math.max(0, st.breakEndsAt - now))]);
      if (otherCell) cells.push(["下次提醒", "—"]);
    } else {
      cells.push(
        st.rest ? [`下次${st.rest.name}`, todayOnly(st.rest.at)] : ["下次休息提醒", "未安排"]
      );
      if (otherCell) cells.push(otherCell);
    }
    $("data-status").innerHTML = cells.map(([k, v, t]) => `<div class="status-cell"${t ? ` title="${String(t).replace(/"/g, "&quot;")}"` : ""}><div class="k">${k}</div><div class="v">${v}</div></div>`).join("");
    fitStatusCells();
    updateTakeoutSummary();
    for (const t of allBreakIds()) updateBreakSummary(t);
    renderTimeline(st);
  }
  var tlExpanded = localStorage.getItem("wr-tl-expanded") === "1";
  function setupCardFolds() {
    for (const card of document.querySelectorAll(".tab .card.foldable[id]")) {
      card.classList.toggle("closed", localStorage.getItem(`wr-fold-${card.id}`) === "1");
    }
    const onFoldClick = (e) => {
      const t = e.target;
      const head = t.closest(".card-head");
      const card = head?.closest(".card");
      if (!head || !card || card.id === "tl-card") return;
      if (!card.classList.contains("foldable")) return;
      if (t.closest("button") && !t.closest(".card-fold")) return;
      if (t instanceof HTMLInputElement) return;
      if (!card.id) return;
      const closed = card.classList.toggle("closed");
      localStorage.setItem(`wr-fold-${card.id}`, closed ? "1" : "0");
    };
    $("tab-timing").addEventListener("click", onFoldClick);
    $("tab-stats").addEventListener("click", onFoldClick);
  }
  document.addEventListener("click", (e) => {
    const btn = e.target.closest(".slot-head > .msg-toggle");
    if (!btn) return;
    const panel = btn.closest(".slot-fixed");
    if (!panel) return;
    const open = panel.classList.toggle("open");
    panel.querySelector(".slot-body")?.classList.toggle("hidden", !open);
  });
  var TL_COLOR_FIXED = { short: "#2f9e63", long: "#4c6ef5", takeout: "#e8890c" };
  var TL_COLOR_PALETTE = ["#c2255c", "#9c36b5", "#0c8599", "#e8590c", "#5f3dc4", "#2b8a3e", "#1971c2", "#d9480f"];
  function tlColorOf(it) {
    const fixed = TL_COLOR_FIXED[it.type];
    if (fixed) return fixed;
    let h = 0;
    for (let i = 0; i < it.key.length; i++) h = h * 31 + it.key.charCodeAt(i) >>> 0;
    return TL_COLOR_PALETTE[h % TL_COLOR_PALETTE.length];
  }
  function renderTimeline(st) {
    const card = document.getElementById("tl-card");
    const count = document.getElementById("tl-count");
    const body = document.getElementById("tl-body");
    const list = document.getElementById("tl-list");
    const empty = document.getElementById("tl-empty");
    if (!card || !count || !body || !list || !empty) return;
    const items = st?.timeline ?? [];
    count.textContent = items.length > 0 ? `待触发 ${items.length} 条` : "今天没有未触发的提醒";
    body.classList.toggle("hidden", !tlExpanded);
    card.classList.toggle("open", tlExpanded);
    empty.classList.toggle("hidden", items.length > 0);
    if (!tlExpanded) return;
    list.textContent = "";
    for (const it of items) {
      const row = document.createElement("div");
      row.className = "tl-row";
      row.style.setProperty("--tl-c", tlColorOf(it));
      const time = document.createElement("span");
      time.className = "tl-time";
      time.textContent = new Date(it.at).toTimeString().slice(0, 5);
      const dot = document.createElement("span");
      dot.className = "tl-dot";
      const name = document.createElement("span");
      name.className = "tl-name";
      name.textContent = it.name;
      const src = document.createElement("span");
      src.className = "tl-src";
      src.textContent = it.source === "countdown" ? "倒计时" : "固定时刻";
      row.append(time, dot, name, src);
      if (it.paused) {
        const badge = document.createElement("span");
        badge.className = "tl-badge pause";
        badge.textContent = "已暂停";
        row.append(badge);
      }
      list.append(row);
    }
  }
  function applyTheme() {
    document.documentElement.dataset.theme = S.theme;
  }
  function fmtDur(ms) {
    const s = Math.round(ms / 1e3);
    if (s < 60) return `${s} 秒`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m} 分 ${s % 60} 秒`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h} 小时 ${m % 60} 分`;
    const d = Math.floor(h / 24);
    return `${d} 天 ${h % 24} 小时`;
  }
  // ---------- 今天：一条堆叠条 + 两组横向条形 ----------
  // 为什么不用三个饼图：“工作 / 休息 / 离开”互斥且加总就是今天的软件运行时长，属部分-整体；
  // 而“推迟”是中间动作 —— 推完这次提醒还会再弹，最终仍会计入完成或跳过（外卖同理），
  // “完成+推迟+跳过”“点餐+取消+推迟”相加都不成立，画饼会把同一件事算成两份份额。
  var COMP_TIME = [
    { k: "work", label: "工作", color: "var(--accent)" },
    { k: "break", label: "休息", color: "#34a46f" },
    { k: "away", label: "离开", color: "#9aa3b2" }
  ];
  // 四项都只统计**内置短休息 / 长休息**：自定义提醒与外卖（各有自己的报表）一律不计。
  // 完成 = 坐满整次的（shortCount + longCount）；提前结束单独一行、**不算完成**。
  var COMP_BREAK = [
    { k: "done", label: "完成", color: "#34a46f" },
    { k: "post", label: "推迟", color: "#e0a23f" },
    { k: "skip", label: "跳过", color: "#e05555" },
    { k: "early", label: "提前结束", color: "#9aa3b2" }
  ];
  // 久坐（最长一段连续工作）的判定基准：45 分钟。取健康科普里常见的“坐 45~60 分钟
  // 该起身一次”下沿 —— 不用用户设定的休息间隔，因为间隔是提醒频率（默认才 10 分钟），
  // 拿它当基准会让条形常年满格标红，反而看不出今天哪一段真的坐久了。
  var SED_REF_MS = 45 * 6e4;
  function compPct(v, total) {
    if (!total || !v) return "0%";
    const p = (v / total) * 100;
    if (p < 1) return "<1%";
    return Math.round(p) + "%";
  }
  function buildComposition(box) {
    const segs = COMP_TIME.map((s) => `<i class="seg" data-seg="${s.k}" style="background:${s.color}"><span class="seg-lb"></span></i>`).join("");
    const keys = COMP_TIME.map((s) => `<div class="ck"><i style="background:${s.color}"></i><span>${s.label}</span><b data-v="${s.k}">0 秒</b><em data-p="${s.k}">0%</em></div>`).join("");
    const bars = (group, list) => list.map((s) => `<div class="cbar" data-b="${group}-${s.k}"><span class="bl">${s.label}</span><span class="track"><i style="background:${s.color}"></i></span><b class="bv">0 次</b></div>`).join("");
    box.innerHTML = `
      <div class="comp-panel">
        <div class="comp-h">
          <span>时间构成</span><em class="comp-total" id="comp-total"></em>
        </div>
        <div class="comp-stack">${segs}</div>
        <div class="comp-keys">${keys}</div>
      </div>
      <div class="comp-panel">
        <div class="comp-h">
          <span>休息</span>
        </div>
        <div class="comp-bars br">${bars("br", COMP_BREAK)}</div>
      </div>
      <div class="comp-panel">
        <div class="comp-h">
          <span>久坐</span>
        </div>
        <div class="comp-bars sed">
          <div class="cbar" data-b="lk-max"><span class="bl">最长</span><span class="track"><i></i></span><b class="bv">0 秒</b></div>
          <div class="cbar" data-b="lk-avg"><span class="bl">平均</span><span class="track"><i></i></span><b class="bv">0 秒</b></div>
          <div class="cbar" data-b="lk-med"><span class="bl">中位数</span><span class="track"><i></i></span><b class="bv">0 秒</b></div>
        </div>
        <p class="comp-note" id="comp-sed-note"></p>
      </div>`;
  }
  function fillCompositionBars(box, group, rows) {
    const max = Math.max(1, ...rows.map((r) => r.v));
    for (const r of rows) {
      const row = box.querySelector(`[data-b="${group}-${r.k}"]`);
      if (!row) continue;
      const bar = row.querySelector(".track > i");
      // 非零值给一个下限，避免“1 次 vs 20 次”时小条细到看不见
      if (bar) bar.style.width = r.v ? `${Math.max((r.v / max) * 100, 3)}%` : "0";
      const bv = row.querySelector(".bv");
      if (bv) bv.textContent = `${r.v} 次`;
      row.title = r.tip || "";
    }
  }
  // 久坐的三个指标都来自今天的工作段分布（后端 DayStats.workSegments，末项是进行中那段）：
  //   平均 = 各段均值；中位数 = 排序后取中间值（比平均值更不受“偶尔坐了两小时”的长尾影响）。
  // 段数组还没攒起来时（升级当天 / 旧数据）平均退回“工作时长 ÷（休息次数 + 1）”，中位数只能给 0。
  function segList(t) {
    return Array.isArray(t.workSegments) ? t.workSegments.filter((v) => v > 0) : [];
  }
  function avgSegmentMs(t) {
    const segs = segList(t);
    if (segs.length) {
      return Math.round(segs.reduce((a, b) => a + b, 0) / segs.length);
    }
    const rests = (t.shortCount || 0) + (t.longCount || 0) + (t.customCount || 0);
    return t.workMs ? Math.round(t.workMs / (rests + 1)) : 0;
  }
  function medianSegmentMs(t) {
    const segs = segList(t).slice().sort((a, b) => a - b);
    if (!segs.length) return 0;
    const mid = segs.length >> 1;
    return segs.length % 2 ? segs[mid] : Math.round((segs[mid - 1] + segs[mid]) / 2);
  }
  // 久坐面板的降级变体：条长按 45 分钟基准归一，超过基准满格并变红
  function fillSedentary(box, rows) {
    for (const r of rows) {
      const row = box.querySelector(`[data-b="lk-${r.k}"]`);
      if (!row) continue;
      const bar = row.querySelector(".track > i");
      const over = r.v > SED_REF_MS;
      if (bar) {
        // 非零值给 3% 下限，避免“刚坐 1 分钟”时条形细到看不见
        bar.style.width = r.v ? `${Math.min(100, Math.max((r.v / SED_REF_MS) * 100, 3))}%` : "0";
        bar.style.background = over ? "#e05555" : "#34a46f";
      }
      const bv = row.querySelector(".bv");
      if (bv) bv.textContent = fmtDur(r.v);
      row.title =
        r.k === "max"
          ? `今天最长的一段连续工作；超过 ${Math.round(SED_REF_MS / 6e4)} 分钟标红`
          : r.k === "avg"
            ? "今天各段连续工作时长的平均值"
            : "今天各段连续工作时长排序后的中间值（比平均值更不受偶尔一次超长影响）";
    }
  }
  function renderComposition(t) {
    const box = $("stats-comp");
    if (!box || !t) return;
    // 日期挂在卡片标题行（原“今天”卡的指标格已删，日期随卡片搬到这里）
    const dateEl = $("stats-date");
    if (dateEl && t.date) {
      const wd = weekdayLabelOf(t.date);
      dateEl.textContent = wd ? `（${t.date} ${wd}）` : `（${t.date}）`;
    }
    if (box.dataset.built !== "1") {
      buildComposition(box);
      box.dataset.built = "1";
    }
    const vals = { work: t.workMs || 0, break: t.breakMs || 0, away: t.awayMs || 0 };
    const total = vals.work + vals.break + vals.away;
    const tot = $("comp-total");
    if (tot) tot.textContent = total ? `软件运行 ${fmtDur(total)}` : "暂无记录";
    for (const s of COMP_TIME) {
      const v = vals[s.k];
      const seg = box.querySelector(`[data-seg="${s.k}"]`);
      if (seg) {
        // 用 flex-grow 表达占比：零值直接隐藏，非零至少留一丝，不会被挤出可视区
        seg.style.display = v ? "" : "none";
        seg.style.flexGrow = v ? String(v) : "0";
        const lb = seg.querySelector(".seg-lb");
        const p = total ? (v / total) * 100 : 0;
        if (lb) lb.textContent = p >= 14 ? `${s.label} ${Math.round(p)}%` : "";
      }
      const kv = box.querySelector(`[data-v="${s.k}"]`);
      if (kv) kv.textContent = fmtDur(v);
      const kp = box.querySelector(`[data-p="${s.k}"]`);
      if (kp) kp.textContent = compPct(v, total);
    }
    const short = t.shortCount || 0;
    const long = t.longCount || 0;
    fillCompositionBars(box, "br", [
      { k: "done", v: short + long, tip: `短休息 ${short} 次 · 长休息 ${long} 次（坐满整次的；提前结束不算）` },
      { k: "post", v: t.postponeCount || 0, tip: `累计推迟 ${fmtDur(t.postponeMs || 0)}` },
      { k: "skip", v: t.skipCount || 0, tip: `被跳过休息的原定时长合计 ${fmtDur(t.skipMs || 0)}` },
      { k: "early", v: t.endEarlyCount || 0, tip: "短 / 长休息在窗口里点“提前结束”的次数（不计入完成）" }
    ]);
    fillSedentary(box, [
      { k: "max", v: t.maxStreakMs || 0 },
      { k: "avg", v: avgSegmentMs(t) },
      { k: "med", v: medianSegmentMs(t) }
    ]);
    // 面板底部一句人话小结：把“最长”翻译成“有没有坐过头”
    const maxStreak = t.maxStreakMs || 0;
    const refMin = Math.round(SED_REF_MS / 6e4);
    const note = $("comp-sed-note");
    if (note) {
      note.classList.toggle("over", maxStreak > SED_REF_MS);
      note.textContent = !maxStreak
        ? "今天还没有工作记录"
        : maxStreak > SED_REF_MS
          ? `今天最长连续坐了 ${fmtDur(maxStreak)}，超过 ${refMin} 分钟`
          : `今天最长连续坐了 ${fmtDur(maxStreak)}，还没超过 ${refMin} 分钟`;
    }
  }
  async function renderStats() {
    const data = await window.api.getStats();
    renderComposition(data.today);
    void renderRestChart();
    void renderTakeoutChart();
    void renderCustomReportChart();
  }
  var RC_NS = "wr-rcv2";
  function rcLoadStr(key, oldKey = "") {
    const nk = `${RC_NS}-${key}`;
    let v = localStorage.getItem(nk);
    if (v === null && oldKey) {
      v = localStorage.getItem(oldKey);
      if (v !== null) localStorage.setItem(nk, v);
    }
    return v;
  }
  function rcSaveStr(key, v) {
    localStorage.setItem(`${RC_NS}-${key}`, v);
  }
  var RC_W = 760;
  var RC_H = 300;
  var RC_M = { top: 16, right: 46, bottom: 26, left: 44 };
  var RC_WINDOWS = { day: 30, week: 26, month: 13, quarter: 5, year: 2 };
  var RC_UNITS = [
    { u: "day", label: "日" },
    { u: "week", label: "周" },
    { u: "month", label: "月" },
    { u: "quarter", label: "季" },
    { u: "year", label: "年" }
  ];
  var RC_SERIES = [
    { id: "long", label: "长休息时间", kind: "bar", cls: "rc-long" },
    { id: "short", label: "短休息时间", kind: "bar", cls: "rc-short" },
    { id: "rest", label: "休息次数", kind: "line", cls: "rc-l-rest" },
    { id: "skip", label: "跳过次数", kind: "line", cls: "rc-l-skip" },
    { id: "postpone", label: "推迟次数", kind: "line", cls: "rc-l-post" },
    // 提前结束与休息次数**互不排斥**：休息开始了就算一次休息，提前结束只是没坐满，
    // 所以这条线不会从“休息次数”里扣掉任何东西（今天卡的“完成”才是坐满整次的次数）。
    { id: "early", label: "提前结束次数", kind: "line", cls: "rc-l-early" }
  ];
  var rcUnit = rcLoadStr("unit", "wr-rc-unit") || "";
  if (!RC_WINDOWS[rcUnit]) rcUnit = "day";
  var rcShowEmpty = rcLoadStr("empty") === "1";
  function rcLegendOn() {
    const def = {
      long: true,
      short: true,
      rest: true,
      skip: true,
      postpone: true,
      early: true
    };
    try {
      const raw = rcLoadStr("on", "wr-rc-on");
      return { ...def, ...JSON.parse(raw || "{}") };
    } catch {
      return def;
    }
  }
  function fmtHms(ms) {
    const s = Math.round(ms / 1e3);
    const h = Math.floor(s / 3600);
    const m = Math.floor(s % 3600 / 60);
    if (h > 0) return `${h} 时 ${m} 分 ${s % 60} 秒`;
    if (m > 0) return `${m} 分 ${s % 60} 秒`;
    return `${s % 60} 秒`;
  }
  function rcDayStr(d) {
    const p = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  }
  function rcMonday(d) {
    const x = new Date(d.getFullYear(), d.getMonth(), d.getDate());
    const wd = x.getDay() === 0 ? 7 : x.getDay();
    x.setDate(x.getDate() - (wd - 1));
    return x;
  }
  function rcIsoWeek(d) {
    const t = new Date(d.getFullYear(), d.getMonth(), d.getDate());
    t.setDate(t.getDate() - (t.getDay() + 6) % 7 + 3);
    const firstThu = new Date(t.getFullYear(), 0, 4);
    firstThu.setDate(firstThu.getDate() - (firstThu.getDay() + 6) % 7 + 3);
    return 1 + Math.round((t.getTime() - firstThu.getTime()) / (7 * 24 * 3600 * 1e3));
  }
  function rcDefaultRange(unit) {
    const now = /* @__PURE__ */ new Date();
    const y = now.getFullYear();
    const m = now.getMonth();
    const ym = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
    if (unit === "day") {
      return { start: rcDayStr(new Date(y, m, now.getDate() - (RC_WINDOWS.day - 1))), end: rcDayStr(now) };
    }
    if (unit === "week") {
      const end = rcMonday(now);
      const start = rcMonday(now);
      start.setDate(start.getDate() - (RC_WINDOWS.week - 1) * 7);
      return { start: rcDayStr(start), end: rcDayStr(end) };
    }
    if (unit === "month") {
      return { start: ym(new Date(y, m - (RC_WINDOWS.month - 1), 1)), end: ym(now) };
    }
    if (unit === "quarter") {
      const q0 = Math.floor(m / 3);
      return {
        start: ym(new Date(y, (q0 - (RC_WINDOWS.quarter - 1)) * 3, 1)),
        end: ym(new Date(y, q0 * 3, 1))
      };
    }
    return { start: String(y - (RC_WINDOWS.year - 1)), end: String(y) };
  }
  // 只有“用户手动设过”的范围才进缓存 / 落 localStorage；**默认范围每次按当前时间现算**。
  // 否则软件连续运行跨天时，缓存里的 end 一直停在昨天，今天的桶根本不生成，
  // 今天的柱 / 折线 / 表格行永远画不出来（只有重启软件才会好）。
  var rcRangeCache = {};
  function rcGetRange(unit) {
    const cached = rcRangeCache[unit];
    if (cached) return cached;
    try {
      const raw = JSON.parse(rcLoadStr(`range-${unit}`, `wr-rc-range-${unit}`) || "null");
      if (raw && typeof raw.start === "string" && typeof raw.end === "string" && raw.start <= raw.end) {
        const r = { start: raw.start, end: raw.end };
        rcRangeCache[unit] = r;
        return r;
      }
    } catch {
    }
    return rcDefaultRange(unit);
  }
  function rcSetRange(unit, r) {
    const norm2 = r.start <= r.end ? { start: r.start, end: r.end } : { start: r.end, end: r.start };
    rcRangeCache[unit] = norm2;
    rcSaveStr(`range-${unit}`, JSON.stringify(norm2));
  }
  function rcResetRange(unit) {
    delete rcRangeCache[unit];
    localStorage.removeItem(`${RC_NS}-range-${unit}`);
  }
  function rcAllRange(unit) {
    const e = rcEarliest;
    if (!e) return null;
    const now = /* @__PURE__ */ new Date();
    const ym = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
    const [ey, em, ed] = e.split("-").map(Number);
    if (unit === "day") return { start: e, end: rcDayStr(now) };
    if (unit === "week") {
      return { start: rcDayStr(rcMonday(new Date(ey, em - 1, ed))), end: rcDayStr(rcMonday(now)) };
    }
    if (unit === "month") return { start: `${ey}-${String(em).padStart(2, "0")}`, end: ym(now) };
    if (unit === "quarter") {
      return {
        start: ym(new Date(ey, Math.floor((em - 1) / 3) * 3, 1)),
        end: ym(new Date(now.getFullYear(), Math.floor(now.getMonth() / 3) * 3, 1))
      };
    }
    return { start: String(ey), end: String(now.getFullYear()) };
  }
  function rcPrevRange(unit, range, len) {
    const pd = (s, addDays) => {
      const [y2, m, d] = s.split("-").map(Number);
      return rcDayStr(new Date(y2, m - 1, d + addDays));
    };
    if (unit === "day") return { start: pd(range.start, -len), end: pd(range.start, -1) };
    if (unit === "week") return { start: pd(range.start, -7 * len), end: pd(range.start, -7) };
    const back = (s, months) => {
      const [y2, m] = s.split("-").map(Number);
      const d = new Date(y2, m - 1 - months, 1);
      return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
    };
    if (unit === "month") return { start: back(range.start, len), end: back(range.start, 1) };
    if (unit === "quarter") return { start: back(range.start, len * 3), end: back(range.start, 3) };
    const y = Number(range.start);
    return { start: String(y - len), end: String(y - 1) };
  }
  var rcEarliest;
  async function rcGetEarliest() {
    if (rcEarliest === void 0) {
      try {
        const data = await window.api.getHistory(null, null);
        const ds = data.days ?? [];
        rcEarliest = ds.length ? ds[0].date : null;
      } catch {
        rcEarliest = null;
      }
    }
    return rcEarliest;
  }
  function rcAnchorToDay(unit, a) {
    if (unit === "day" || unit === "week") return a;
    if (unit === "year") return `${a}-01-01`;
    return `${a}-01`;
  }
  function rcBuckets(unit, range) {
    const out = [];
    const mk = (key, label, title, start, end) => ({
      key,
      label,
      title,
      start,
      end,
      workMs: 0,
      awayMs: 0,
      longMs: 0,
      shortMs: 0,
      breakMs: 0,
      restCount: 0,
      skipCount: 0,
      postponeCount: 0,
      endEarlyCount: 0,
      takeoutOrdered: 0,
      takeoutSkipped: 0,
      takeoutPostponed: 0,
      customCounts: {}
    });
    const parseDay = (s) => {
      const [yy, mm, dd] = s.split("-").map(Number);
      return new Date(yy, mm - 1, dd);
    };
    const cap = 800;
    if (unit === "day" || unit === "week") {
      const sd = parseDay(range.start);
      const stepDays = unit === "day" ? 1 : 7;
      const total = Math.round((parseDay(range.end).getTime() - sd.getTime()) / 864e5 / stepDays);
      for (let i = 0; i <= total && out.length < cap; i++) {
        const d = new Date(sd.getFullYear(), sd.getMonth(), sd.getDate() + i * stepDays);
        if (unit === "day") {
          const k = rcDayStr(d);
          out.push(mk(k, k.slice(5), k, k, k));
        } else {
          const sun = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 6);
          const wk = rcIsoWeek(d);
          const s = rcDayStr(d);
          out.push(
            mk(
              `${d.getFullYear()}-W${wk}`,
              `${d.getFullYear()}W${wk}`,
              `${d.getFullYear()} 年第 ${wk} 周（${s.slice(5)} ~ ${rcDayStr(sun).slice(5)}）`,
              s,
              rcDayStr(sun)
            )
          );
        }
      }
    } else if (unit === "month" || unit === "quarter") {
      const stepM = unit === "month" ? 1 : 3;
      const [sy, sm] = range.start.split("-").map(Number);
      const [ey, em] = range.end.split("-").map(Number);
      const totalM = (ey * 12 + em - 1 - (sy * 12 + sm - 1)) / stepM;
      for (let i = 0; i <= totalM && out.length < cap; i++) {
        const d = new Date(sy, sm - 1 + i * stepM, 1);
        const e = new Date(d.getFullYear(), d.getMonth() + stepM, 0);
        const s = rcDayStr(d);
        if (unit === "month") {
          out.push(
            mk(
              `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`,
              `${String(d.getFullYear()).slice(2)}/${d.getMonth() + 1}`,
              `${d.getFullYear()} 年 ${d.getMonth() + 1} 月`,
              s,
              rcDayStr(e)
            )
          );
        } else {
          const q = Math.floor(d.getMonth() / 3) + 1;
          out.push(
            mk(
              `${d.getFullYear()}-Q${q}`,
              `${String(d.getFullYear()).slice(2)}Q${q}`,
              `${d.getFullYear()} 年第 ${q} 季度`,
              s,
              rcDayStr(e)
            )
          );
        }
      }
    } else {
      for (let yy = Number(range.start); yy <= Number(range.end) && out.length < cap; yy++) {
        out.push(mk(String(yy), String(yy), `${yy} 年`, `${yy}-01-01`, `${yy}-12-31`));
      }
    }
    return out;
  }
  var rcSeq = 0;
  var rcLastDays = [];
  var rcPrevOf = null;
  function rcFillBuckets(buckets, days) {
    for (const d of days) {
      const b = buckets.find((x) => d.date >= x.start && d.date <= x.end);
      if (!b) continue;
      b.workMs += d.workMs;
      b.longMs += d.longMs;
      b.shortMs += d.shortMs;
      b.breakMs += d.breakMs;
      b.awayMs += d.awayMs ?? 0;
      // “休息次数”= 休息只要开始过就算一次：短 + 长 + 提前结束 + 自定义提醒。
      // 与今天卡“完成”（坐满整次、不含提前结束）是两个口径 —— 提前结束在这里**不扣次数**。
      b.restCount += d.shortCount + d.longCount + (d.endEarlyCount ?? 0) + d.customCount;
      b.endEarlyCount += d.endEarlyCount ?? 0;
      b.skipCount += d.skipCount;
      b.postponeCount += d.postponeCount;
      b.takeoutOrdered += d.takeoutOrdered;
      b.takeoutSkipped += d.takeoutSkipped;
      b.takeoutPostponed += d.takeoutPostponed;
      for (const [id, v] of Object.entries(d.customCounts ?? {})) {
        b.customCounts[id] = (b.customCounts[id] ?? 0) + v;
      }
    }
  }
  function rcEmptyBucket(b) {
    return b.workMs <= 0 && b.breakMs <= 0 && b.restCount <= 0 && b.skipCount <= 0 && b.postponeCount <= 0;
  }
  function rcRenderBadges(shown, unit, prev) {
    const box = $("rc-badges");
    if (!box) return;
    const work = shown.reduce((s, b) => s + b.workMs, 0);
    const brk = shown.reduce((s, b) => s + b.breakMs, 0);
    const avg = shown.length ? brk / shown.length : 0;
    const top = shown.reduce((m, b) => !m || b.breakMs > m.breakMs ? b : m, null);
    const unitLabel = { day: "日均", week: "周均", month: "月均", quarter: "季均", year: "年均" }[unit];
    const delta = (cur, old) => {
      if (old <= 0) return "";
      const pct = Math.round((cur - old) / old * 100);
      return pct === 0 ? "较上期持平" : `较上期 ${pct > 0 ? "+" : "−"}${Math.abs(pct)}%`;
    };
    const badge = (k, v, sub) => `<div class="rc-badge"><span class="k">${k}</span><b>${v}</b>${sub ? `<i>${sub}</i>` : ""}</div>`;
    box.innerHTML = badge("工作时间", fmtDur(work), delta(work, prev.work)) + badge("休息时间", fmtDur(brk), delta(brk, prev.brk)) + badge(`${unitLabel}休息`, shown.length ? fmtDur(avg) : "—", `${shown.length} 个周期`) + badge("最长休息", top ? fmtDur(top.breakMs) : "—", top ? top.title : "");
  }
  async function renderRestChart(animate = false) {
    const seq = ++rcSeq;
    const range = rcGetRange(rcUnit);
    rcSyncRangeLabel("rc", rcUnit, range);
    const buckets = rcBuckets(rcUnit, range);
    const prevBuckets = rcBuckets(rcUnit, rcPrevRange(rcUnit, range, buckets.length));
    const anchor = rcAnchorToDay(rcUnit, prevBuckets[0]?.start ?? range.start);
    const data = await window.api.getHistory(anchor, null);
    if (seq !== rcSeq) return;
    rcLastDays = data.days ?? [];
    rcFillBuckets(buckets, rcLastDays);
    rcFillBuckets(prevBuckets, rcLastDays);
    const shown = rcShowEmpty ? buckets : buckets.filter((b) => !rcEmptyBucket(b));
    const fullIdx = new Map(buckets.map((b, i) => [b.key, i]));
    rcPrevOf = (b) => {
      const i = fullIdx.get(b.key);
      return i !== void 0 && i > 0 ? buckets[i - 1] : null;
    };
    rcRenderBadges(shown, rcUnit, {
      work: prevBuckets.reduce((s, b) => s + b.workMs, 0),
      brk: prevBuckets.reduce((s, b) => s + b.breakMs, 0)
    });
    rcDraw(shown, animate);
    rcRenderTable(buckets.filter((b) => !rcEmptyBucket(b)));
  }
  var RC_SORT_GET = {
    period: (b) => b.start,
    work: (b) => b.workMs,
    break: (b) => b.breakMs,
    long: (b) => b.longMs,
    short: (b) => b.shortMs,
    away: (b) => b.awayMs,
    rest: (b) => b.restCount,
    skip: (b) => b.skipCount,
    post: (b) => b.postponeCount,
    early: (b) => b.endEarlyCount
  };
  var RC_TABLE_COLS = [
    { key: "period", label: "统计周期" },
    { key: "work", label: "工作时间" },
    { key: "break", label: "休息总时间" },
    { key: "long", label: "长休息" },
    { key: "short", label: "短休息" },
    { key: "away", label: "离开时间" },
    { key: "rest", label: "休息次数" },
    { key: "skip", label: "跳过次数" },
    { key: "post", label: "推迟次数" },
    { key: "early", label: "提前结束次数" }
  ];
  var rcTableSort = { key: "period", dir: "desc" };
  var rcTableData = [];
  function rcRenderTable(rows) {
    const t = $("rc-table");
    if (!t) return;
    rcTableData = rows;
    const head = "<tr>" + RC_TABLE_COLS.map((c) => {
      const arrow = c.key === rcTableSort.key ? rcTableSort.dir === "asc" ? " ▲" : " ▼" : "";
      const num = c.key === "period" ? "" : " num";
      return `<th class="sortable${num}" data-key="${c.key}" title="点按排序">${c.label}${arrow}</th>`;
    }).join("") + "</tr>";
    const dir = rcTableSort.dir === "asc" ? 1 : -1;
    const get = RC_SORT_GET[rcTableSort.key];
    const sorted = [...rows].sort((a, b) => {
      const va = get(a);
      const vb = get(b);
      return (va < vb ? -1 : va > vb ? 1 : 0) * dir;
    });
    const cells = (b) => `<td>${b.title}</td><td class="num rcv-work" data-v="${b.workMs}">${fmtDur(b.workMs)}</td><td class="num" data-v="${b.breakMs}">${fmtDur(b.breakMs)}</td><td class="num rcv-long" data-v="${b.longMs}">${fmtDur(b.longMs)}</td><td class="num rcv-short" data-v="${b.shortMs}">${fmtDur(b.shortMs)}</td><td class="num" data-v="${b.awayMs}">${fmtDur(b.awayMs)}</td><td class="num rcv-rest" data-v="${b.restCount}">${b.restCount}</td><td class="num rcv-skip" data-v="${b.skipCount}">${b.skipCount}</td><td class="num rcv-post" data-v="${b.postponeCount}">${b.postponeCount}</td><td class="num rcv-early" data-v="${b.endEarlyCount}">${b.endEarlyCount}</td>`;
    const body = rows.length ? sorted.map((b) => `<tr>${cells(b)}</tr>`).join("") : '<tr><td colspan="10" class="rc-empty">暂无数据</td></tr>';
    const sum = (f) => rows.reduce((s, b) => s + f(b), 0);
    const total = rows.length ? `<tr class="total"><td>合计</td><td class="num rcv-work">${fmtDur(sum((b) => b.workMs))}</td><td class="num">${fmtDur(sum((b) => b.breakMs))}</td><td class="num rcv-long">${fmtDur(sum((b) => b.longMs))}</td><td class="num rcv-short">${fmtDur(sum((b) => b.shortMs))}</td><td class="num">${fmtDur(sum((b) => b.awayMs))}</td><td class="num rcv-rest">${sum((b) => b.restCount)}</td><td class="num rcv-skip">${sum((b) => b.skipCount)}</td><td class="num rcv-post">${sum((b) => b.postponeCount)}</td><td class="num rcv-early">${sum((b) => b.endEarlyCount)}</td></tr>` : "";
    t.innerHTML = head + body + total;
  }
  var rcTip = null;
  function rcHideTip() {
    if (rcTip) rcTip.style.display = "none";
  }
  function rcShowTip(b, x, y) {
    if (!rcTip) {
      rcTip = document.createElement("div");
      rcTip.className = "rc-tip";
      document.body.append(rcTip);
    }
    let env = "";
    const prev = rcPrevOf?.(b) ?? null;
    if (prev) {
      const pct = (cur, old) => {
        if (old <= 0) return cur > 0 ? "—" : "";
        const p = Math.round((cur - old) / old * 100);
        return p === 0 ? "持平" : `${p > 0 ? "+" : "−"}${Math.abs(p)}%`;
      };
      const bp = pct(b.breakMs, prev.breakMs);
      const wp = pct(b.workMs, prev.workMs);
      if (bp || wp) env = `<span class="rc-tip-d">较上一期\u3000休息 ${bp || "—"} \xB7 工作 ${wp || "—"}</span>`;
    }
    rcTip.innerHTML = `<b>${b.title}</b><span class="rcv-work">工作时间 ${fmtHms(b.workMs)}</span><span>休息总时间 ${fmtHms(b.breakMs)}</span><span class="rcv-long">长休息时间 ${fmtHms(b.longMs)}</span><span class="rcv-short">短休息时间 ${fmtHms(b.shortMs)}</span><span>离开时间 ${fmtHms(b.awayMs)}</span><span class="rcv-rest">休息 ${b.restCount} 次</span><span class="rcv-skip">跳过 ${b.skipCount} 次</span><span class="rcv-post">推迟 ${b.postponeCount} 次</span><span class="rcv-early">提前结束 ${b.endEarlyCount} 次</span>` + env;
    rcTip.style.display = "block";
    const r = rcTip.getBoundingClientRect();
    rcTip.style.left = `${Math.max(8, Math.min(window.innerWidth - r.width - 8, x + 14))}px`;
    rcTip.style.top = `${Math.max(8, Math.min(window.innerHeight - r.height - 8, y - r.height - 12))}px`;
  }
  function rcSmoothPath(pts) {
    if (pts.length < 2) return "";
    let d = `M ${pts[0].x.toFixed(1)} ${pts[0].y.toFixed(1)}`;
    for (let i = 1; i < pts.length; i++) {
      const p0 = pts[i - 2] ?? pts[i - 1];
      const p1 = pts[i - 1];
      const p2 = pts[i];
      const p3 = pts[i + 1] ?? p2;
      d += ` C ${(p1.x + (p2.x - p0.x) / 6).toFixed(1)} ${(p1.y + (p2.y - p0.y) / 6).toFixed(1)}, ${(p2.x - (p3.x - p1.x) / 6).toFixed(1)} ${(p2.y - (p3.y - p1.y) / 6).toFixed(1)}, ${p2.x.toFixed(1)} ${p2.y.toFixed(1)}`;
    }
    return d;
  }
  function rcProbeAxis(dataMax) {
    const UNITS = [10, 20, 50, 100, 200, 500, 1e3, 2e3, 5e3, 1e4, 2e4, 5e4, 1e5];
    let max = 10;
    for (const u of UNITS) {
      max = Math.ceil(Math.max(0, dataMax) / u) * u;
      if (max <= dataMax) max += u;
      if (max / u <= 8) break;
    }
    const STEPS = [1, 2, 5, 10, 20, 50, 100, 200, 500, 1e3, 2e3, 5e3, 1e4];
    let step = 1;
    for (const s of STEPS) {
      if (max % s === 0 && max / s >= 3 && max / s <= 8) step = s;
    }
    return { max, step };
  }
  var rcZoom = null;
  var rcDrag = null;
  var rcDragMoved = false;
  function rcDraw(buckets, animate = false) {
    const box = $("rc-plot");
    const on = rcLegendOn();
    const n = buckets.length;
    if (rcZoom && (rcZoom.total !== n || rcZoom.o >= n)) rcZoom = null;
    const zo = rcZoom ? Math.min(rcZoom.o, Math.max(0, n - rcZoom.n)) : 0;
    const zv = rcZoom ? Math.min(rcZoom.n, n - zo) : n;
    const view = zv === n ? buckets : buckets.slice(zo, zo + zv);
    const iw = RC_W - RC_M.left - RC_M.right;
    const ih = RC_H - RC_M.top - RC_M.bottom;
    const barParts = RC_SERIES.filter((s) => s.kind === "bar" && on[s.id]);
    const lineParts = RC_SERIES.filter((s) => s.kind === "line" && on[s.id]);
    const maxRestMin = Math.ceil(
      buckets.reduce((mv, b) => Math.max(mv, b.longMs + b.shortMs), 0) / 6e4
    );
    const maxCnt = buckets.reduce(
      (mv, b) => Math.max(mv, b.restCount, b.skipCount, b.postponeCount, b.endEarlyCount),
      0
    );
    const axC = rcProbeAxis(maxCnt);
    const axM = rcProbeAxis(maxRestMin);
    const yMs = (v) => RC_M.top + ih * (1 - v / (axM.max * 6e4));
    const yC = (v) => RC_M.top + ih * (1 - v / axC.max);
    const step = iw / Math.max(1, zv);
    const bw = Math.min(34, Math.max(3, step * 0.6));
    const showDots = zv <= 60;
    const showPeaks = zv <= 40;
    const grad = (id, color, o0, o1) => `<linearGradient id="${id}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${color}" stop-opacity="${o0}"/><stop offset="1" stop-color="${color}" stop-opacity="${o1}"/></linearGradient>`;
    const defs = `<defs>` + grad("rc-g-long", "#9a6bff", 1, 0.72) + grad("rc-g-short", "#34a46f", 1, 0.72) + grad("rc-a-rest", "#2fa3b5", 0.2, 0) + grad("rc-a-skip", "#e05555", 0.2, 0) + grad("rc-a-post", "#e0a23f", 0.2, 0) + grad("rc-a-early", "#9aa3b2", 0.2, 0) + `</defs>`;
    let g = "";
    g += `<line class="rc-axis" x1="${RC_M.left}" y1="${RC_M.top}" x2="${RC_M.left}" y2="${(RC_M.top + ih).toFixed(1)}"/><line class="rc-axis" x1="${RC_W - RC_M.right}" y1="${RC_M.top}" x2="${RC_W - RC_M.right}" y2="${(RC_M.top + ih).toFixed(1)}"/>`;
    for (let v = 0; v <= axC.max; v += axC.step) {
      const yy = yC(v).toFixed(1);
      g += `<line class="grid" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/><text class="tick" x="${RC_M.left - 6}" y="${+yy + 4}" text-anchor="end">${v}</text>`;
    }
    for (let v = 0; v <= axM.max; v += axM.step) {
      g += `<text class="tick" x="${RC_W - RC_M.right + 6}" y="${(yMs(v * 6e4) + 4).toFixed(1)}">${v}</text>`;
    }
    g += `<text class="tick rc-cap" x="${RC_M.left - 6}" y="10" text-anchor="end">次数</text><text class="tick rc-cap" x="${RC_W - RC_M.right + 6}" y="10">分钟</text>`;
    const withData = buckets.filter((b) => !rcEmptyBucket(b));
    // 平均线的文字单独攒着，等柱、折线、圆点全画完再追加 —— SVG 按文档顺序绘制，
    // 早画的会被后画的盖住；这段文字贴着右轴，正好落在末几根柱子的范围里。
    let avgLabel = "";
    if (withData.length) {
      const avgMs = withData.reduce((s, b) => s + b.breakMs, 0) / withData.length;
      const avgMin = avgMs / 6e4;
      if (avgMin > 0 && avgMin < axM.max) {
        const yy = yMs(avgMs).toFixed(1);
        // 虚线本身仍画在柱后（保持原来的层次），只有文字提到最上层
        g += `<line class="rc-avgline" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/>`;
        avgLabel = `<text class="rc-avglabel" x="${RC_W - RC_M.right - 4}" y="${+yy - 4}" text-anchor="end">平均 ${fmtDur(avgMs)}</text>`;
      }
    }
    const barTop = (x, y, w, h, gid) => {
      const r = Math.min(3, w / 2, h);
      if (r < 0.5) {
        return `<rect class="bar" x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${w.toFixed(1)}" height="${h.toFixed(1)}" style="fill:url(#${gid})"/>`;
      }
      return `<path class="bar" d="M ${x.toFixed(1)} ${(y + r).toFixed(1)} Q ${x.toFixed(1)} ${y.toFixed(1)} ${(x + r).toFixed(1)} ${y.toFixed(1)} L ${(x + w - r).toFixed(1)} ${y.toFixed(1)} Q ${(x + w).toFixed(1)} ${y.toFixed(1)} ${(x + w).toFixed(1)} ${(y + r).toFixed(1)} L ${(x + w).toFixed(1)} ${(y + h).toFixed(1)} L ${x.toFixed(1)} ${(y + h).toFixed(1)} Z" style="fill:url(#${gid})"/>`;
    };
    view.forEach((b, i) => {
      if (rcEmptyBucket(b)) return;
      const cx = RC_M.left + step * i + step / 2;
      let acc = 0;
      const vis = barParts.map((s) => ({ s, v: s.id === "long" ? b.longMs : b.shortMs })).filter((x) => x.v > 0);
      vis.forEach(({ s, v }, vi) => {
        const yTop = yMs(acc + v);
        const h = yMs(acc) - yTop;
        acc += v;
        const x = cx - bw / 2;
        if (vi === vis.length - 1) g += barTop(x, yTop, bw, h, `rc-g-${s.id}`);
        else {
          g += `<rect class="bar" x="${x.toFixed(1)}" y="${yTop.toFixed(1)}" width="${bw.toFixed(1)}" height="${h.toFixed(1)}" style="fill:url(#rc-g-${s.id})"/>`;
        }
      });
    });
    for (const s of lineParts) {
      const val = (b) => s.id === "rest" ? b.restCount : s.id === "skip" ? b.skipCount : s.id === "postpone" ? b.postponeCount : b.endEarlyCount;
      const segs = [];
      let pts = [];
      view.forEach((b, i) => {
        const p = { x: RC_M.left + step * i + step / 2, y: yC(val(b)), b };
        if (rcEmptyBucket(b)) {
          if (pts.length) segs.push(pts);
          pts = [];
        } else pts.push(p);
      });
      if (pts.length) segs.push(pts);
      const base = yC(0).toFixed(1);
      for (const seg of segs) {
        const d = rcSmoothPath(seg);
        if (d) {
          if (seg.length >= 2) {
            const first = seg[0];
            const last = seg[seg.length - 1];
            g += `<path class="rc-area ${s.cls}" d="${d} L ${last.x.toFixed(1)} ${base} L ${first.x.toFixed(1)} ${base} Z" style="fill:url(#rc-a-${s.id})"/>`;
          }
          g += `<path class="rc-line ${s.cls}" d="${d}"${animate ? ' pathLength="1"' : ""}/>`;
          if (showPeaks && seg.length >= 2) {
            const pk = seg.reduce((m, p) => val(p.b) > val(m.b) ? p : m, seg[0]);
            if (val(pk.b) > 0) {
              g += `<text class="rc-peak ${s.cls}" x="${pk.x.toFixed(1)}" y="${(pk.y - 7).toFixed(1)}" text-anchor="middle">${val(pk.b)}</text>`;
            }
          }
        }
        if (showDots) {
          seg.forEach((p) => {
            g += `<circle class="rc-dot ${s.cls}" cx="${p.x.toFixed(1)}" cy="${p.y.toFixed(1)}" r="2.6"/>`;
          });
        }
      }
    }
    const labelEvery = zv <= 10 ? 1 : Math.ceil(zv / 10);
    view.forEach((b, i) => {
      if (i % labelEvery === 0) {
        g += `<text class="tick" x="${(RC_M.left + step * i + step / 2).toFixed(1)}" y="${RC_H - 8}" text-anchor="middle">${b.label}</text>`;
      }
    });
    if (view.length === 0 || withData.length === 0) {
      g += `<text class="tick" x="${(RC_M.left + iw / 2).toFixed(1)}" y="${(RC_M.top + ih / 2).toFixed(1)}" text-anchor="middle">暂无数据</text>`;
    }
    // “平均 x 分 x 秒”最后追加，压在所有图形之上（见上面 avgLabel 的说明）
    g += avgLabel;
    g += `<line class="rc-cursor" x1="0" y1="${RC_M.top}" x2="0" y2="${(RC_M.top + ih).toFixed(1)}" style="display:none"/>`;
    g += `<rect class="rc-hit" x="${RC_M.left}" y="${RC_M.top}" width="${iw.toFixed(1)}" height="${ih.toFixed(1)}"/>`;
    box.innerHTML = `<svg viewBox="0 0 ${RC_W} ${RC_H}" role="img" tabindex="0" aria-label="休息报表图"${animate ? ' class="anim"' : ""}>${defs}${g}</svg>`;
    const svg = box.querySelector("svg");
    if (!svg) return;
    svg.style.cursor = rcZoom ? "grab" : "";
    const cursor = svg.querySelector(".rc-cursor");
    const centers = view.map((b, i) => ({ b, cx: RC_M.left + step * i + step / 2 }));
    let kbX = null;
    const nearest = (xSvg) => {
      if (!centers.length) return null;
      let best = centers[0];
      for (const c of centers) if (Math.abs(c.cx - xSvg) < Math.abs(best.cx - xSvg)) best = c;
      return xSvg < RC_M.left - 6 || xSvg > RC_W - RC_M.right + 6 ? null : best;
    };
    const placeCursor = (c, clientY) => {
      if (!cursor) return;
      const rect = svg.getBoundingClientRect();
      cursor.setAttribute("x1", c.cx.toFixed(1));
      cursor.setAttribute("x2", c.cx.toFixed(1));
      cursor.style.display = "";
      rcShowTip(c.b, rect.left + c.cx / RC_W * rect.width, clientY);
    };
    const xOf = (e) => {
      const rect = svg.getBoundingClientRect();
      return (e.clientX - rect.left) / rect.width * RC_W;
    };
    svg.addEventListener("mousemove", (e) => {
      const best = nearest(xOf(e));
      if (!best || !cursor) {
        if (cursor) cursor.style.display = "none";
        rcHideTip();
        return;
      }
      kbX = best.cx;
      placeCursor(best, e.clientY);
    });
    svg.addEventListener("mouseleave", () => {
      if (cursor) cursor.style.display = "none";
      rcHideTip();
      rcDrag = null;
    });
    svg.addEventListener("wheel", (e) => {
      if (!n) return;
      e.preventDefault();
      const horiz = Math.abs(e.deltaX) > Math.abs(e.deltaY) || e.shiftKey;
      if (horiz && rcZoom) {
        const per = iw / rcZoom.n;
        const o = Math.round(rcZoom.o + (e.deltaX || e.deltaY) / per);
        rcZoom = { ...rcZoom, o: Math.max(0, Math.min(n - rcZoom.n, o)) };
        rcDraw(buckets);
        return;
      }
      const rect = svg.getBoundingClientRect();
      const xSvg = (e.clientX - rect.left) / rect.width * RC_W;
      const anchor = Math.max(0, Math.min(n - 1, Math.floor((xSvg - RC_M.left) / (iw / n))));
      const cur = rcZoom ?? { o: 0, n, total: n };
      let wn = Math.round(cur.n * (e.deltaY < 0 ? 1 / 1.35 : 1.35));
      wn = Math.min(n, Math.max(3, wn));
      if (wn >= n) {
        rcZoom = null;
      } else {
        const o = Math.round(anchor - (anchor - cur.o) * wn / cur.n);
        rcZoom = { o: Math.max(0, Math.min(n - wn, o)), n: wn, total: n };
      }
      rcDraw(buckets);
    });
    svg.addEventListener("mousedown", (e) => {
      rcDrag = rcZoom ? { startX: e.clientX, o0: rcZoom.o } : null;
      rcDragMoved = false;
      if (rcDrag) svg.style.cursor = "grabbing";
    });
    svg.addEventListener("mousemove", (e) => {
      if (!rcDrag || !rcZoom) return;
      if (e.buttons === 0) {
        rcDrag = null;
        svg.style.cursor = "grab";
        return;
      }
      const dx = e.clientX - rcDrag.startX;
      if (!rcDragMoved && Math.abs(dx) < 3) return;
      rcDragMoved = true;
      const o = Math.round(rcDrag.o0 - dx / (iw / rcZoom.n));
      const o2 = Math.max(0, Math.min(n - rcZoom.n, o));
      if (o2 !== rcZoom.o) {
        rcZoom = { ...rcZoom, o: o2 };
        rcDraw(buckets);
      }
    });
    svg.addEventListener("mouseup", () => {
      rcDrag = null;
      svg.style.cursor = rcZoom ? "grab" : "";
    });
    svg.addEventListener("click", (e) => {
      if (rcDragMoved) {
        rcDragMoved = false;
        return;
      }
      const best = nearest(xOf(e));
      if (!best || rcUnit === "day") return;
      e.stopPropagation();
      const rect = svg.getBoundingClientRect();
      rcOpenDetail(best.b, rect.left + best.cx / RC_W * rect.width);
    });
    svg.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        if (cursor) cursor.style.display = "none";
        rcHideTip();
        return;
      }
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      e.preventDefault();
      if (!centers.length || !cursor) return;
      let idx = 0;
      if (kbX !== null) {
        idx = centers.reduce((bi, c, i) => Math.abs(c.cx - kbX) < Math.abs(centers[bi].cx - kbX) ? i : bi, 0);
        idx = e.key === "ArrowRight" ? Math.min(centers.length - 1, idx + 1) : Math.max(0, idx - 1);
      }
      kbX = centers[idx].cx;
      const rect = svg.getBoundingClientRect();
      placeCursor(centers[idx], rect.top + (RC_M.top + ih / 2) * (rect.height / RC_H));
    });
  }
  var rcDetailEl = null;
  function rcCloseDetail() {
    if (rcDetailEl) rcDetailEl.classList.remove("open");
  }
  function rcOpenDetail(b, clientX) {
    const host = document.getElementById("rest-chart");
    if (!host) return;
    if (!rcDetailEl) {
      rcDetailEl = document.createElement("div");
      rcDetailEl.className = "rc-detail";
      host.append(rcDetailEl);
    }
    const days = rcLastDays.filter((d) => d.date >= b.start && d.date <= b.end).sort((x, y) => x.date < y.date ? -1 : 1);
    const rows = days.map(
      (d) => `<tr><td>${d.date}</td><td>${fmtDur(d.workMs)}</td><td>${fmtDur(d.breakMs)}</td><td>${fmtDur(d.longMs)}</td><td>${fmtDur(d.shortMs)}</td><td>${fmtDur(d.awayMs ?? 0)}</td><td>${d.shortCount + d.longCount}/${d.skipCount}/${d.postponeCount}</td></tr>`
    ).join("");
    rcDetailEl.innerHTML = `<div class="rc-detail-head"><b>${b.title}</b><button type="button" class="rc-detail-x" title="关闭">✕</button></div><div class="rc-detail-scroll"><table class="stats-table"><tr><th>日期</th><th>工作</th><th>休息总</th><th>长休</th><th>短休</th><th>离开</th><th>休/跳/推</th></tr>` + (rows || '<tr><td colspan="7" class="rc-empty">该周期没有逐日数据</td></tr>') + "</table></div>";
    rcDetailEl.classList.add("open");
    rcDetailEl.querySelector(".rc-detail-x")?.addEventListener("click", rcCloseDetail);
    const hr = host.getBoundingClientRect();
    rcDetailEl.style.left = `${Math.max(8, Math.min(hr.width - 292, clientX - hr.left - 140))}px`;
    rcDetailEl.style.top = "44px";
    rcHideTip();
  }
  function rcRenderControls() {
    $("rc-units").innerHTML = RC_UNITS.map(
      (x) => `<button type="button" class="rc-unit${x.u === rcUnit ? " on" : ""}" data-u="${x.u}">${x.label}</button>`
    ).join("");
    rcRenderRange();
    const on = rcLegendOn();
    $("rc-legend").innerHTML = RC_SERIES.map(
      (s) => `<button type="button" class="rc-legend-item${on[s.id] ? "" : " off"}" data-s="${s.id}" title="点击显示 / 隐藏"><i class="${s.cls} ${s.kind}"></i>${s.label}</button>`
    ).join("");
  }
  function rcRangeLabel(unit, r) {
    if (unit === "day") {
      return r.start.slice(0, 4) === r.end.slice(0, 4) ? `${r.start.slice(5)} ~ ${r.end.slice(5)}` : `${r.start} ~ ${r.end}`;
    }
    if (unit === "week") {
      const wk = (day) => {
        const [y, m, d] = day.split("-").map(Number);
        return rcIsoWeek(new Date(y, m - 1, d));
      };
      const y1 = r.start.slice(0, 4);
      const y2 = r.end.slice(0, 4);
      return y1 === y2 ? `${y1} 年第 ${wk(r.start)} 周 ~ 第 ${wk(r.end)} 周` : `${y1} 年第 ${wk(r.start)} 周 ~ ${y2} 年第 ${wk(r.end)} 周`;
    }
    if (unit === "quarter") {
      const q = (a) => `${a.slice(0, 4)}Q${Math.floor(Number(a.slice(5, 7)) / 3) + 1}`;
      return `${q(r.start)} ~ ${q(r.end)}`;
    }
    return `${r.start} ~ ${r.end}`;
  }
  function rcOpts(list, sel) {
    if (sel && !list.some((x) => x.v === sel)) list.unshift({ v: sel, t: `选定：${sel}` });
    return list.map((x) => `<option value="${x.v}"${x.v === sel ? " selected" : ""}>${x.t}</option>`).join("");
  }
  function rcWeekOpts(sel) {
    const list = [];
    const mon = rcMonday(/* @__PURE__ */ new Date());
    let weeks = 110;
    if (rcEarliest) {
      const [y, m, d] = rcEarliest.split("-").map(Number);
      const e0 = rcMonday(new Date(y, m - 1, d));
      weeks = Math.round((mon.getTime() - e0.getTime()) / 864e5 / 7) + 1;
    }
    for (let i = 0; i < weeks; i++) {
      const d = new Date(mon.getFullYear(), mon.getMonth(), mon.getDate() - i * 7);
      const v = rcDayStr(d);
      const sun = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 6);
      list.push({
        v,
        t: `${d.getFullYear()} 年第 ${rcIsoWeek(d)} 周（${v.slice(5)} ~ ${rcDayStr(sun).slice(5)}）`
      });
    }
    return rcOpts(list, sel);
  }
  function rcMonthOpts(sel) {
    const list = [];
    const now = /* @__PURE__ */ new Date();
    let months = 36;
    if (rcEarliest) {
      const [y, m] = rcEarliest.split("-").map(Number);
      months = now.getFullYear() * 12 + now.getMonth() - (y * 12 + m - 1) + 1;
    }
    for (let i = 0; i < months; i++) {
      const d = new Date(now.getFullYear(), now.getMonth() - i, 1);
      list.push({
        v: `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`,
        t: `${d.getFullYear()} 年 ${d.getMonth() + 1} 月`
      });
    }
    return rcOpts(list, sel);
  }
  function rcQuarterOpts(sel) {
    const list = [];
    const now = /* @__PURE__ */ new Date();
    const q0 = Math.floor(now.getMonth() / 3);
    let qs = 16;
    if (rcEarliest) {
      const [y, m] = rcEarliest.split("-").map(Number);
      qs = now.getFullYear() * 4 + q0 - (y * 4 + Math.floor((m - 1) / 3)) + 1;
    }
    for (let i = 0; i < qs; i++) {
      const d = new Date(now.getFullYear(), (q0 - i) * 3, 1);
      list.push({
        v: `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`,
        t: `${d.getFullYear()} 年第 ${Math.floor(d.getMonth() / 3) + 1} 季度`
      });
    }
    return rcOpts(list, sel);
  }
  function rcYearOpts(sel) {
    const list = [];
    const y = (/* @__PURE__ */ new Date()).getFullYear();
    let ys = 6;
    if (rcEarliest) ys = y - Number(rcEarliest.slice(0, 4)) + 1;
    for (let i = 0; i < ys; i++) list.push({ v: String(y - i), t: `${y - i} 年` });
    return rcOpts(list, sel);
  }
  function rcPickerHtml(unit, r) {
    const row = (span, ctl) => `<div class="rc-pop-row"><span>${span}</span>${ctl}</div>`;
    if (unit === "day") {
      const min = rcEarliest ? ` min="${rcEarliest}"` : "";
      return row("从", `<input type="date" class="rc-in" id="rc-rs" value="${r.start}"${min}>`) + row("到", `<input type="date" class="rc-in" id="rc-re" value="${r.end}"${min}>`);
    }
    const opts = unit === "week" ? rcWeekOpts : unit === "month" ? rcMonthOpts : unit === "quarter" ? rcQuarterOpts : unit === "year" ? rcYearOpts : null;
    if (!opts) return "";
    return row("从", `<select class="rc-in" id="rc-rs">${opts(r.start)}</select>`) + row("到", `<select class="rc-in" id="rc-re">${opts(r.end)}</select>`);
  }
  function rcRenderRange() {
    const box = $("rc-range");
    const r = rcGetRange(rcUnit);
    box.innerHTML = `<button type="button" class="rc-range-btn" id="rc-range-btn" title="选择时间范围"><span id="rc-range-label">${rcRangeLabel(rcUnit, r)}</span><span class="rc-caret">▾</span></button><div class="rc-pop" id="rc-pop">${rcPickerHtml(rcUnit, r)}<div class="rc-pop-foot"><button type="button" class="btn ghost" id="rc-range-all" title="从最早记录一直看到当前周期">全部数据</button><button type="button" class="btn ghost" id="rc-range-reset">恢复默认</button></div></div>`;
    $("rc-range-btn").addEventListener("click", () => box.classList.toggle("open"));
    $("rc-range-all").addEventListener("click", () => {
      const all = rcAllRange(rcUnit);
      if (!all) {
        toast("还没有统计数据");
        return;
      }
      rcSetRange(rcUnit, all);
      box.classList.remove("open");
      rcRenderControls();
      void renderRestChart(true);
    });
    $("rc-range-reset").addEventListener("click", () => {
      rcResetRange(rcUnit);
      rcRenderControls();
      void renderRestChart(true);
    });
    const startCtl = box.querySelector("#rc-rs");
    const endCtl = box.querySelector("#rc-re");
    const apply = () => {
      if (!startCtl || !endCtl) return;
      rcSetRange(rcUnit, { start: startCtl.value, end: endCtl.value });
      const label = $("rc-range-label");
      if (label) label.textContent = rcRangeLabel(rcUnit, rcGetRange(rcUnit));
      void renderRestChart(true);
    };
    startCtl?.addEventListener("change", apply);
    endCtl?.addEventListener("change", apply);
  }
  // 重画报表时把范围标签与日期控件同步到当前范围。默认范围是“现算”的：
  // 连续运行跨天后 end 会往前推，标签不同步就会出现“表里有今天、标签还写着昨天”。
  function rcSyncRangeLabel(prefix, unit, r) {
    const lab = $(`${prefix}-range-label`);
    if (lab) {
      const txt = rcRangeLabel(unit, r);
      if (lab.textContent !== txt) lab.textContent = txt;
    }
    const s = $(`${prefix}-rs`);
    const e = $(`${prefix}-re`);
    if (s && s.value !== r.start) s.value = r.start;
    if (e && e.value !== r.end) e.value = r.end;
  }
  function rcRangeFilePart(unit, r) {
    if (unit === "quarter") {
      const q = (a) => {
        const [y, m] = a.split("-").map(Number);
        return `${y}Q${Math.floor((m - 1) / 3) + 1}`;
      };
      return { s: q(r.start), e: q(r.end) };
    }
    return { s: r.start.replace(/-/g, ""), e: r.end.replace(/-/g, "") };
  }
  function rcExportName(ext) {
    const ul = RC_UNITS.find((x) => x.u === rcUnit)?.label ?? rcUnit;
    const fp = rcRangeFilePart(rcUnit, rcGetRange(rcUnit));
    return `休息报表_${ul}_${fp.s}_${fp.e}.${ext}`;
  }
  // 报表导出统一走“另存为”对话框：blob 转 base64 交给 Go 侧 ExportReport，
  // 由用户选择存储路径（上一版时代的 a.download 落点不可控，Wails 下也不可靠）。
  // 取消静默、失败 toast、成功 toast 落盘路径。
  async function saveExportFile(name, blob) {
    const b64 = await new Promise((res) => {
      const fr = new FileReader();
      fr.onload = () => res(String(fr.result ?? "").split(",")[1] || "");
      fr.onerror = () => res("");
      fr.readAsDataURL(blob);
    });
    if (!b64) {
      toast("导出失败");
      return;
    }
    const r = await window.api.exportReport(name, b64);
    if (!r || r.canceled) return;
    if (r.error) {
      toast(r.error);
      return;
    }
    toast(`已导出到 ${r.path}`);
  }
  function setupRestChart() {
    $("rc-units").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-unit");
      if (!b) return;
      rcUnit = b.dataset.u;
      rcSaveStr("unit", rcUnit);
      rcRenderControls();
      void renderRestChart(true);
    });
    $("rc-legend").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const on = rcLegendOn();
      const id = b.dataset.s;
      on[id] = !on[id];
      rcSaveStr("on", JSON.stringify(on));
      rcRenderControls();
      void renderRestChart();
    });
    $("rc-legend").addEventListener("dblclick", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const id = b.dataset.s;
      const on = rcLegendOn();
      const only = Object.keys(on).every((k) => on[k] === (k === id));
      const all = { long: true, short: true, rest: true, skip: true, postpone: true };
      const none = { long: false, short: false, rest: false, skip: false, postpone: false };
      rcSaveStr("on", JSON.stringify(only ? all : { ...none, [id]: true }));
      rcRenderControls();
      void renderRestChart();
    });
    const emptyBtn = $("rc-empty-btn");
    emptyBtn.classList.toggle("on", rcShowEmpty);
    emptyBtn.addEventListener("click", () => {
      rcShowEmpty = !rcShowEmpty;
      rcSaveStr("empty", rcShowEmpty ? "1" : "0");
      emptyBtn.classList.toggle("on", rcShowEmpty);
      void renderRestChart();
    });
    $("rc-exp-csv").addEventListener("click", () => {
      const rows = [...rcTableData].sort((a2, b) => a2.start < b.start ? -1 : 1);
      const q = (v) => `"${String(v).replace(/"/g, '""')}"`;
      const lines = ["统计周期,开始,结束,工作时间,休息总时间,长休息,短休息,离开时间,休息次数,跳过次数,推迟次数,提前结束次数"];
      for (const b of rows) {
        lines.push(
          [
            b.title,
            b.start,
            b.end,
            fmtDur(b.workMs),
            fmtDur(b.breakMs),
            fmtDur(b.longMs),
            fmtDur(b.shortMs),
            fmtDur(b.awayMs),
            b.restCount,
            b.skipCount,
            b.postponeCount,
            b.endEarlyCount
          ].map(q).join(",")
        );
      }
      if (rows.length) {
        const sum = (f) => rows.reduce((s, b) => s + f(b), 0);
        lines.push(
          [
            "合计",
            "",
            "",
            fmtDur(sum((b) => b.workMs)),
            fmtDur(sum((b) => b.breakMs)),
            fmtDur(sum((b) => b.longMs)),
            fmtDur(sum((b) => b.shortMs)),
            fmtDur(sum((b) => b.awayMs)),
            sum((b) => b.restCount),
            sum((b) => b.skipCount),
            sum((b) => b.postponeCount),
            sum((b) => b.endEarlyCount)
          ].map(q).join(",")
        );
      }
      void saveExportFile(rcExportName("csv"), new Blob(["\uFEFF" + lines.join("\r\n")], { type: "text/csv;charset=utf-8" }));
    });
    $("rc-exp-png").addEventListener("click", () => {
      const svg = document.querySelector("#rc-plot svg");
      if (!svg) return;
      try {
        const clone = svg.cloneNode(true);
        clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
        const cs = getComputedStyle(document.querySelector(".rest-chart") ?? document.body);
        const v = (name, fb) => cs.getPropertyValue(name).trim() || fb;
        const st = document.createElementNS("http://www.w3.org/2000/svg", "style");
        st.textContent = `text{fill:${v("--text-2", "#888888")};font-size:11px;font-variant-numeric:tabular-nums;font-family:${cs.fontFamily};}.rc-cap{font-weight:600;}.grid{stroke:${v("--border", "#dddddd")};stroke-width:1;}.rc-axis{stroke:${v("--text-2", "#888888")};stroke-opacity:0.5;}.rc-avgline{stroke:${v("--text-2", "#888888")};stroke-opacity:0.6;stroke-dasharray:5 4;}.rc-avglabel{fill:${v("--text", "#333333")};font-size:11px;font-weight:600;}.rc-peak{font-size:10px;font-weight:600;}.rc-peak.rc-l-rest{fill:#2fa3b5;}.rc-peak.rc-l-skip{fill:#e05555;}.rc-peak.rc-l-post{fill:#e0a23f;}.rc-peak.rc-l-early{fill:#9aa3b2;}path.rc-line{fill:none;stroke-width:2;}.rc-l-rest{stroke:#2fa3b5;}.rc-l-skip{stroke:#e05555;}.rc-l-post{stroke:#e0a23f;}.rc-l-early{stroke:#9aa3b2;}circle.rc-dot.rc-l-rest{fill:#2fa3b5;}circle.rc-dot.rc-l-skip{fill:#e05555;}circle.rc-dot.rc-l-post{fill:#e0a23f;}circle.rc-dot.rc-l-early{fill:#9aa3b2;}.rc-cursor{display:none;}`;
        clone.insertBefore(st, clone.firstChild);
        const xml = new XMLSerializer().serializeToString(clone);
        const img = new Image();
        img.onload = () => {
          try {
            const cv = document.createElement("canvas");
            cv.width = RC_W * 2;
            cv.height = RC_H * 2;
            const ctx = cv.getContext("2d");
            if (!ctx) return;
            ctx.fillStyle = v("--card", "#ffffff");
            ctx.fillRect(0, 0, cv.width, cv.height);
            ctx.drawImage(img, 0, 0, cv.width, cv.height);
            cv.toBlob((blob) => {
              if (!blob) return;
              void saveExportFile(rcExportName("png"), blob);
            }, "image/png");
          } catch {
            toast("导出图片失败");
          }
        };
        img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(xml);
      } catch {
        toast("导出图片失败");
      }
    });
    document.addEventListener("click", (e) => {
      const open = document.querySelector(".rc-range.open");
      if (open && !open.contains(e.target)) open.classList.remove("open");
      const det = document.querySelector(".rc-detail.open");
      if (det && !det.contains(e.target)) det.classList.remove("open");
    });
    $("rc-table").addEventListener("click", (e) => {
      const th = e.target.closest("th.sortable");
      if (!th) return;
      const key = th.dataset.key;
      if (rcTableSort.key === key) rcTableSort.dir = rcTableSort.dir === "asc" ? "desc" : "asc";
      else rcTableSort = { key, dir: "desc" };
      rcRenderTable(rcTableData);
    });
    rcRenderControls();
    void rcGetEarliest().then((e) => {
      if (e) rcRenderControls();
    });
  }
  var TK_NS = "wr-tkv2";
  function tkLoadStr(key) {
    return localStorage.getItem(`${TK_NS}-${key}`);
  }
  function tkSaveStr(key, v) {
    localStorage.setItem(`${TK_NS}-${key}`, v);
  }
  var TK_SERIES = [
    { id: "ord", label: "点外卖次数", cls: "tk-l-ord" },
    { id: "can", label: "取消次数", cls: "tk-l-can" },
    { id: "post", label: "推迟次数", cls: "tk-l-post" }
  ];
  var tkUnit = tkLoadStr("unit") || "";
  if (!RC_WINDOWS[tkUnit]) tkUnit = "day";
  var tkShowEmpty = tkLoadStr("empty") === "1";
  function tkLegendOn() {
    const def = { ord: true, can: true, post: true };
    try {
      const raw = tkLoadStr("on");
      return { ...def, ...JSON.parse(raw || "{}") };
    } catch {
      return def;
    }
  }
  // 同 rcGetRange：默认范围现算，只有手动范围才缓存（否则跨天后停在昨天）
  var tkRangeCache = {};
  function tkGetRange(unit) {
    const cached = tkRangeCache[unit];
    if (cached) return cached;
    try {
      const raw = JSON.parse(tkLoadStr(`range-${unit}`) || "null");
      if (raw && typeof raw.start === "string" && typeof raw.end === "string" && raw.start <= raw.end) {
        const r = { start: raw.start, end: raw.end };
        tkRangeCache[unit] = r;
        return r;
      }
    } catch {
    }
    return rcDefaultRange(unit);
  }
  function tkSetRange(unit, r) {
    const norm2 = r.start <= r.end ? { start: r.start, end: r.end } : { start: r.end, end: r.start };
    tkRangeCache[unit] = norm2;
    tkSaveStr(`range-${unit}`, JSON.stringify(norm2));
  }
  function tkResetRange(unit) {
    delete tkRangeCache[unit];
    localStorage.removeItem(`${TK_NS}-range-${unit}`);
  }
  function tkEmptyBucket(b) {
    return b.takeoutOrdered <= 0 && b.takeoutSkipped <= 0 && b.takeoutPostponed <= 0;
  }
  var tkSeq = 0;
  var tkLastDays = [];
  var tkPrevOf = null;
  function tkRenderBadges(shown, unit, prev) {
    const box = $("tk-badges");
    if (!box) return;
    const ord = shown.reduce((s, b) => s + b.takeoutOrdered, 0);
    const can = shown.reduce((s, b) => s + b.takeoutSkipped, 0);
    const post = shown.reduce((s, b) => s + b.takeoutPostponed, 0);
    const unitLabel = { day: "日均", week: "周均", month: "月均", quarter: "季均", year: "年均" }[unit];
    const delta = (cur, old) => {
      if (old <= 0) return "";
      const pct = Math.round((cur - old) / old * 100);
      return pct === 0 ? "较上期持平" : `较上期 ${pct > 0 ? "+" : "−"}${Math.abs(pct)}%`;
    };
    const badge = (k, v, sub) => `<div class="rc-badge"><span class="k">${k}</span><b>${v}</b>${sub ? `<i>${sub}</i>` : ""}</div>`;
    box.innerHTML = badge("点外卖次数", String(ord), delta(ord, prev.ord)) + badge("取消次数", String(can), delta(can, prev.can)) + badge("推迟次数", String(post), delta(post, prev.post)) + badge(
      `${unitLabel}点外卖`,
      shown.length ? String(Math.round(ord / shown.length * 10) / 10) : "—",
      `${shown.length} 个周期`
    );
  }
  async function renderTakeoutChart(animate = false) {
    const seq = ++tkSeq;
    const range = tkGetRange(tkUnit);
    rcSyncRangeLabel("tk", tkUnit, range);
    const buckets = rcBuckets(tkUnit, range);
    const prevBuckets = rcBuckets(tkUnit, rcPrevRange(tkUnit, range, buckets.length));
    const anchor = rcAnchorToDay(tkUnit, prevBuckets[0]?.start ?? range.start);
    const data = await window.api.getHistory(anchor, null);
    if (seq !== tkSeq) return;
    tkLastDays = data.days ?? [];
    rcFillBuckets(buckets, tkLastDays);
    rcFillBuckets(prevBuckets, tkLastDays);
    const shown = tkShowEmpty ? buckets : buckets.filter((b) => !tkEmptyBucket(b));
    const fullIdx = new Map(buckets.map((b, i) => [b.key, i]));
    tkPrevOf = (b) => {
      const i = fullIdx.get(b.key);
      return i !== void 0 && i > 0 ? buckets[i - 1] : null;
    };
    tkRenderBadges(shown, tkUnit, {
      ord: prevBuckets.reduce((s, b) => s + b.takeoutOrdered, 0),
      can: prevBuckets.reduce((s, b) => s + b.takeoutSkipped, 0),
      post: prevBuckets.reduce((s, b) => s + b.takeoutPostponed, 0)
    });
    tkDraw(shown, animate);
    tkRenderTable(buckets.filter((b) => !tkEmptyBucket(b)));
  }
  var TK_SORT_GET = {
    period: (b) => b.start,
    ord: (b) => b.takeoutOrdered,
    can: (b) => b.takeoutSkipped,
    post: (b) => b.takeoutPostponed
  };
  var TK_TABLE_COLS = [
    { key: "period", label: "统计周期" },
    { key: "ord", label: "点外卖次数" },
    { key: "can", label: "取消次数" },
    { key: "post", label: "推迟次数" }
  ];
  var tkTableSort = { key: "period", dir: "desc" };
  var tkTableData = [];
  function tkRenderTable(rows) {
    const t = $("tk-table");
    if (!t) return;
    tkTableData = rows;
    const head = "<tr>" + TK_TABLE_COLS.map((c) => {
      const arrow = c.key === tkTableSort.key ? tkTableSort.dir === "asc" ? " ▲" : " ▼" : "";
      const num = c.key === "period" ? "" : " num";
      return `<th class="sortable${num}" data-key="${c.key}" title="点按排序">${c.label}${arrow}</th>`;
    }).join("") + "</tr>";
    const dir = tkTableSort.dir === "asc" ? 1 : -1;
    const get = TK_SORT_GET[tkTableSort.key];
    const sorted = [...rows].sort((a, b) => {
      const va = get(a);
      const vb = get(b);
      return (va < vb ? -1 : va > vb ? 1 : 0) * dir;
    });
    const cells = (b) => `<td>${b.title}</td><td class="num tkv-ord" data-v="${b.takeoutOrdered}">${b.takeoutOrdered}</td><td class="num tkv-can" data-v="${b.takeoutSkipped}">${b.takeoutSkipped}</td><td class="num tkv-post" data-v="${b.takeoutPostponed}">${b.takeoutPostponed}</td>`;
    const body = rows.length ? sorted.map((b) => `<tr>${cells(b)}</tr>`).join("") : '<tr><td colspan="4" class="rc-empty">暂无数据</td></tr>';
    const sum = (f) => rows.reduce((s, b) => s + f(b), 0);
    const total = rows.length ? `<tr class="total"><td>合计</td><td class="num tkv-ord">${sum((b) => b.takeoutOrdered)}</td><td class="num tkv-can">${sum((b) => b.takeoutSkipped)}</td><td class="num tkv-post">${sum((b) => b.takeoutPostponed)}</td></tr>` : "";
    t.innerHTML = head + body + total;
  }
  var tkTip = null;
  function tkHideTip() {
    if (tkTip) tkTip.style.display = "none";
  }
  function tkShowTip(b, x, y) {
    if (!tkTip) {
      tkTip = document.createElement("div");
      tkTip.className = "rc-tip";
      document.body.append(tkTip);
    }
    let env = "";
    const prev = tkPrevOf?.(b) ?? null;
    if (prev) {
      const pct = (cur, old) => {
        if (old <= 0) return cur > 0 ? "—" : "";
        const p = Math.round((cur - old) / old * 100);
        return p === 0 ? "持平" : `${p > 0 ? "+" : "−"}${Math.abs(p)}%`;
      };
      const op = pct(b.takeoutOrdered, prev.takeoutOrdered);
      const cp = pct(b.takeoutSkipped, prev.takeoutSkipped);
      const pp = pct(b.takeoutPostponed, prev.takeoutPostponed);
      if (op || cp || pp) {
        env = `<span class="rc-tip-d">较上一期\u3000点外卖 ${op || "—"} \xB7 取消 ${cp || "—"} \xB7 推迟 ${pp || "—"}</span>`;
      }
    }
    tkTip.innerHTML = `<b>${b.title}</b><span class="tkv-ord">点外卖 ${b.takeoutOrdered} 次</span><span class="tkv-can">取消 ${b.takeoutSkipped} 次</span><span class="tkv-post">推迟 ${b.takeoutPostponed} 次</span>` + env;
    tkTip.style.display = "block";
    const r = tkTip.getBoundingClientRect();
    tkTip.style.left = `${Math.max(8, Math.min(window.innerWidth - r.width - 8, x + 14))}px`;
    tkTip.style.top = `${Math.max(8, Math.min(window.innerHeight - r.height - 8, y - r.height - 12))}px`;
  }
  var tkZoom = null;
  var tkDrag = null;
  var tkDragMoved = false;
  function tkDraw(buckets, animate = false) {
    const box = $("tk-plot");
    const on = tkLegendOn();
    const n = buckets.length;
    if (tkZoom && (tkZoom.total !== n || tkZoom.o >= n)) tkZoom = null;
    const zo = tkZoom ? Math.min(tkZoom.o, Math.max(0, n - tkZoom.n)) : 0;
    const zv = tkZoom ? Math.min(tkZoom.n, n - zo) : n;
    const view = zv === n ? buckets : buckets.slice(zo, zo + zv);
    const iw = RC_W - RC_M.left - RC_M.right;
    const ih = RC_H - RC_M.top - RC_M.bottom;
    const lineParts = TK_SERIES.filter((s) => on[s.id]);
    const maxCnt = buckets.reduce(
      (mv, b) => Math.max(mv, b.takeoutOrdered, b.takeoutSkipped, b.takeoutPostponed),
      0
    );
    const ax = rcProbeAxis(maxCnt);
    const yC = (v) => RC_M.top + ih * (1 - v / ax.max);
    const step = iw / Math.max(1, zv);
    const showDots = zv <= 60;
    const showPeaks = zv <= 40;
    const grad = (id, color, o0, o1) => `<linearGradient id="${id}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${color}" stop-opacity="${o0}"/><stop offset="1" stop-color="${color}" stop-opacity="${o1}"/></linearGradient>`;
    const defs = `<defs>` + grad("tk-a-ord", "#2fa3b5", 0.2, 0) + grad("tk-a-can", "#e05555", 0.2, 0) + grad("tk-a-post", "#e0a23f", 0.2, 0) + `</defs>`;
    let g = "";
    g += `<line class="rc-axis" x1="${RC_M.left}" y1="${RC_M.top}" x2="${RC_M.left}" y2="${(RC_M.top + ih).toFixed(1)}"/>`;
    for (let v = 0; v <= ax.max; v += ax.step) {
      const yy = yC(v).toFixed(1);
      g += `<line class="grid" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/><text class="tick" x="${RC_M.left - 6}" y="${+yy + 4}" text-anchor="end">${v}</text>`;
    }
    g += `<text class="tick rc-cap" x="${RC_M.left - 6}" y="10" text-anchor="end">次数</text>`;
    const withData = buckets.filter((b) => !tkEmptyBucket(b));
    if (withData.length) {
      const avg = withData.reduce((s, b) => s + b.takeoutOrdered, 0) / withData.length;
      if (avg > 0 && avg < ax.max) {
        const yy = yC(avg).toFixed(1);
        const txt = avg < 10 ? String(Math.round(avg * 10) / 10) : String(Math.round(avg));
        g += `<line class="rc-avgline" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/><text class="rc-avglabel" x="${RC_W - RC_M.right - 4}" y="${+yy - 4}" text-anchor="end">平均 ${txt} 次</text>`;
      }
    }
    for (const s of lineParts) {
      const val = (b) => s.id === "ord" ? b.takeoutOrdered : s.id === "can" ? b.takeoutSkipped : b.takeoutPostponed;
      const segs = [];
      let pts = [];
      view.forEach((b, i) => {
        const p = { x: RC_M.left + step * i + step / 2, y: yC(val(b)), b };
        if (tkEmptyBucket(b)) {
          if (pts.length) segs.push(pts);
          pts = [];
        } else pts.push(p);
      });
      if (pts.length) segs.push(pts);
      const base = yC(0).toFixed(1);
      for (const seg of segs) {
        const d = rcSmoothPath(seg);
        if (d) {
          if (seg.length >= 2) {
            const first = seg[0];
            const last = seg[seg.length - 1];
            g += `<path class="rc-area ${s.cls}" d="${d} L ${last.x.toFixed(1)} ${base} L ${first.x.toFixed(1)} ${base} Z" style="fill:url(#tk-a-${s.id})"/>`;
          }
          g += `<path class="rc-line ${s.cls}" d="${d}"${animate ? ' pathLength="1"' : ""}/>`;
          if (showPeaks && seg.length >= 2) {
            const pk = seg.reduce((m, p) => val(p.b) > val(m.b) ? p : m, seg[0]);
            if (val(pk.b) > 0) {
              g += `<text class="rc-peak ${s.cls}" x="${pk.x.toFixed(1)}" y="${(pk.y - 7).toFixed(1)}" text-anchor="middle">${val(pk.b)}</text>`;
            }
          }
        }
        if (showDots) {
          seg.forEach((p) => {
            g += `<circle class="rc-dot ${s.cls}" cx="${p.x.toFixed(1)}" cy="${p.y.toFixed(1)}" r="2.6"/>`;
          });
        }
      }
    }
    const labelEvery = zv <= 10 ? 1 : Math.ceil(zv / 10);
    view.forEach((b, i) => {
      if (i % labelEvery === 0) {
        g += `<text class="tick" x="${(RC_M.left + step * i + step / 2).toFixed(1)}" y="${RC_H - 8}" text-anchor="middle">${b.label}</text>`;
      }
    });
    if (view.length === 0 || withData.length === 0) {
      g += `<text class="tick" x="${(RC_M.left + iw / 2).toFixed(1)}" y="${(RC_M.top + ih / 2).toFixed(1)}" text-anchor="middle">暂无数据</text>`;
    }
    g += `<line class="rc-cursor" x1="0" y1="${RC_M.top}" x2="0" y2="${(RC_M.top + ih).toFixed(1)}" style="display:none"/>`;
    g += `<rect class="rc-hit" x="${RC_M.left}" y="${RC_M.top}" width="${iw.toFixed(1)}" height="${ih.toFixed(1)}"/>`;
    box.innerHTML = `<svg viewBox="0 0 ${RC_W} ${RC_H}" role="img" tabindex="0" aria-label="外卖报表图"${animate ? ' class="anim"' : ""}>${defs}${g}</svg>`;
    const svg = box.querySelector("svg");
    if (!svg) return;
    svg.style.cursor = tkZoom ? "grab" : "";
    const cursor = svg.querySelector(".rc-cursor");
    const centers = view.map((b, i) => ({ b, cx: RC_M.left + step * i + step / 2 }));
    let kbX = null;
    const nearest = (xSvg) => {
      if (!centers.length) return null;
      let best = centers[0];
      for (const c of centers) if (Math.abs(c.cx - xSvg) < Math.abs(best.cx - xSvg)) best = c;
      return xSvg < RC_M.left - 6 || xSvg > RC_W - RC_M.right + 6 ? null : best;
    };
    const placeCursor = (c, clientY) => {
      if (!cursor) return;
      const rect = svg.getBoundingClientRect();
      cursor.setAttribute("x1", c.cx.toFixed(1));
      cursor.setAttribute("x2", c.cx.toFixed(1));
      cursor.style.display = "";
      tkShowTip(c.b, rect.left + c.cx / RC_W * rect.width, clientY);
    };
    const xOf = (e) => {
      const rect = svg.getBoundingClientRect();
      return (e.clientX - rect.left) / rect.width * RC_W;
    };
    svg.addEventListener("mousemove", (e) => {
      const best = nearest(xOf(e));
      if (!best || !cursor) {
        if (cursor) cursor.style.display = "none";
        tkHideTip();
        return;
      }
      kbX = best.cx;
      placeCursor(best, e.clientY);
    });
    svg.addEventListener("mouseleave", () => {
      if (cursor) cursor.style.display = "none";
      tkHideTip();
      tkDrag = null;
    });
    svg.addEventListener("wheel", (e) => {
      if (!n) return;
      e.preventDefault();
      const horiz = Math.abs(e.deltaX) > Math.abs(e.deltaY) || e.shiftKey;
      if (horiz && tkZoom) {
        const per = iw / tkZoom.n;
        const o = Math.round(tkZoom.o + (e.deltaX || e.deltaY) / per);
        tkZoom = { ...tkZoom, o: Math.max(0, Math.min(n - tkZoom.n, o)) };
        tkDraw(buckets);
        return;
      }
      const rect = svg.getBoundingClientRect();
      const xSvg = (e.clientX - rect.left) / rect.width * RC_W;
      const anchor = Math.max(0, Math.min(n - 1, Math.floor((xSvg - RC_M.left) / (iw / n))));
      const cur = tkZoom ?? { o: 0, n, total: n };
      let wn = Math.round(cur.n * (e.deltaY < 0 ? 1 / 1.35 : 1.35));
      wn = Math.min(n, Math.max(3, wn));
      if (wn >= n) {
        tkZoom = null;
      } else {
        const o = Math.round(anchor - (anchor - cur.o) * wn / cur.n);
        tkZoom = { o: Math.max(0, Math.min(n - wn, o)), n: wn, total: n };
      }
      tkDraw(buckets);
    });
    svg.addEventListener("mousedown", (e) => {
      tkDrag = tkZoom ? { startX: e.clientX, o0: tkZoom.o } : null;
      tkDragMoved = false;
      if (tkDrag) svg.style.cursor = "grabbing";
    });
    svg.addEventListener("mousemove", (e) => {
      if (!tkDrag || !tkZoom) return;
      if (e.buttons === 0) {
        tkDrag = null;
        svg.style.cursor = "grab";
        return;
      }
      const dx = e.clientX - tkDrag.startX;
      if (!tkDragMoved && Math.abs(dx) < 3) return;
      tkDragMoved = true;
      const o = Math.round(tkDrag.o0 - dx / (iw / tkZoom.n));
      const o2 = Math.max(0, Math.min(n - tkZoom.n, o));
      if (o2 !== tkZoom.o) {
        tkZoom = { ...tkZoom, o: o2 };
        tkDraw(buckets);
      }
    });
    svg.addEventListener("mouseup", () => {
      tkDrag = null;
      svg.style.cursor = tkZoom ? "grab" : "";
    });
    svg.addEventListener("click", (e) => {
      if (tkDragMoved) {
        tkDragMoved = false;
        return;
      }
      const best = nearest(xOf(e));
      if (!best || tkUnit === "day") return;
      e.stopPropagation();
      const rect = svg.getBoundingClientRect();
      tkOpenDetail(best.b, rect.left + best.cx / RC_W * rect.width);
    });
    svg.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        if (cursor) cursor.style.display = "none";
        tkHideTip();
        return;
      }
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      e.preventDefault();
      if (!centers.length || !cursor) return;
      let idx = 0;
      if (kbX !== null) {
        idx = centers.reduce((bi, c, i) => Math.abs(c.cx - kbX) < Math.abs(centers[bi].cx - kbX) ? i : bi, 0);
        idx = e.key === "ArrowRight" ? Math.min(centers.length - 1, idx + 1) : Math.max(0, idx - 1);
      }
      kbX = centers[idx].cx;
      const rect = svg.getBoundingClientRect();
      placeCursor(centers[idx], rect.top + (RC_M.top + ih / 2) * (rect.height / RC_H));
    });
  }
  var tkDetailEl = null;
  function tkCloseDetail() {
    if (tkDetailEl) tkDetailEl.classList.remove("open");
  }
  function tkOpenDetail(b, clientX) {
    const host = document.getElementById("takeout-chart");
    if (!host) return;
    if (!tkDetailEl) {
      tkDetailEl = document.createElement("div");
      tkDetailEl.className = "rc-detail";
      host.append(tkDetailEl);
    }
    const days = tkLastDays.filter((d) => d.date >= b.start && d.date <= b.end).sort((x, y) => x.date < y.date ? -1 : 1);
    const rows = days.map(
      (d) => `<tr><td>${d.date}</td><td>${d.takeoutOrdered}</td><td>${d.takeoutSkipped}</td><td>${d.takeoutPostponed}</td></tr>`
    ).join("");
    tkDetailEl.innerHTML = `<div class="rc-detail-head"><b>${b.title}</b><button type="button" class="rc-detail-x" title="关闭">✕</button></div><div class="rc-detail-scroll"><table class="stats-table"><tr><th>日期</th><th>点外卖</th><th>取消</th><th>推迟</th></tr>` + (rows || '<tr><td colspan="4" class="rc-empty">该周期没有逐日数据</td></tr>') + "</table></div>";
    tkDetailEl.classList.add("open");
    tkDetailEl.querySelector(".rc-detail-x")?.addEventListener("click", tkCloseDetail);
    const hr = host.getBoundingClientRect();
    tkDetailEl.style.left = `${Math.max(8, Math.min(hr.width - 292, clientX - hr.left - 140))}px`;
    tkDetailEl.style.top = "44px";
    tkHideTip();
  }
  function tkRenderControls() {
    $("tk-units").innerHTML = RC_UNITS.map(
      (x) => `<button type="button" class="rc-unit${x.u === tkUnit ? " on" : ""}" data-u="${x.u}">${x.label}</button>`
    ).join("");
    tkRenderRange();
    const on = tkLegendOn();
    $("tk-legend").innerHTML = TK_SERIES.map(
      (s) => `<button type="button" class="rc-legend-item${on[s.id] ? "" : " off"}" data-s="${s.id}" title="点击显示 / 隐藏"><i class="${s.cls} line"></i>${s.label}</button>`
    ).join("");
  }
  function tkRenderRange() {
    const box = $("tk-range");
    const r = tkGetRange(tkUnit);
    box.innerHTML = `<button type="button" class="rc-range-btn" id="tk-range-btn" title="选择时间范围"><span id="tk-range-label">${rcRangeLabel(tkUnit, r)}</span><span class="rc-caret">▾</span></button><div class="rc-pop">${rcPickerHtml(tkUnit, r).replaceAll('id="rc-rs"', 'id="tk-rs"').replaceAll('id="rc-re"', 'id="tk-re"')}<div class="rc-pop-foot"><button type="button" class="btn ghost" id="tk-range-all" title="从最早记录一直看到当前周期">全部数据</button><button type="button" class="btn ghost" id="tk-range-reset">恢复默认</button></div></div>`;
    $("tk-range-btn").addEventListener("click", () => box.classList.toggle("open"));
    $("tk-range-all").addEventListener("click", () => {
      const all = rcAllRange(tkUnit);
      if (!all) {
        toast("还没有统计数据");
        return;
      }
      tkSetRange(tkUnit, all);
      box.classList.remove("open");
      tkRenderControls();
      void renderTakeoutChart(true);
    });
    $("tk-range-reset").addEventListener("click", () => {
      tkResetRange(tkUnit);
      tkRenderControls();
      void renderTakeoutChart(true);
    });
    const startCtl = box.querySelector("#tk-rs");
    const endCtl = box.querySelector("#tk-re");
    const apply = () => {
      if (!startCtl || !endCtl) return;
      tkSetRange(tkUnit, { start: startCtl.value, end: endCtl.value });
      const label = $("tk-range-label");
      if (label) label.textContent = rcRangeLabel(tkUnit, tkGetRange(tkUnit));
      void renderTakeoutChart(true);
    };
    startCtl?.addEventListener("change", apply);
    endCtl?.addEventListener("change", apply);
  }
  function tkExportName(ext) {
    const ul = RC_UNITS.find((x) => x.u === tkUnit)?.label ?? tkUnit;
    const fp = rcRangeFilePart(tkUnit, tkGetRange(tkUnit));
    return `外卖报表_${ul}_${fp.s}_${fp.e}.${ext}`;
  }
  function setupTakeoutChart() {
    $("tk-units").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-unit");
      if (!b) return;
      tkUnit = b.dataset.u;
      tkSaveStr("unit", tkUnit);
      tkRenderControls();
      void renderTakeoutChart(true);
    });
    $("tk-legend").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const on = tkLegendOn();
      const id = b.dataset.s;
      on[id] = !on[id];
      tkSaveStr("on", JSON.stringify(on));
      tkRenderControls();
      void renderTakeoutChart();
    });
    $("tk-legend").addEventListener("dblclick", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const id = b.dataset.s;
      const on = tkLegendOn();
      const only = Object.keys(on).every((k) => on[k] === (k === id));
      const all = { ord: true, can: true, post: true };
      const none = { ord: false, can: false, post: false };
      tkSaveStr("on", JSON.stringify(only ? all : { ...none, [id]: true }));
      tkRenderControls();
      void renderTakeoutChart();
    });
    const emptyBtn = $("tk-empty-btn");
    emptyBtn.classList.toggle("on", tkShowEmpty);
    emptyBtn.addEventListener("click", () => {
      tkShowEmpty = !tkShowEmpty;
      tkSaveStr("empty", tkShowEmpty ? "1" : "0");
      emptyBtn.classList.toggle("on", tkShowEmpty);
      void renderTakeoutChart();
    });
    $("tk-exp-csv").addEventListener("click", () => {
      const rows = [...tkTableData].sort((a2, b) => a2.start < b.start ? -1 : 1);
      const q = (v) => `"${String(v).replace(/"/g, '""')}"`;
      const lines = ["统计周期,开始,结束,点外卖次数,取消次数,推迟次数"];
      for (const b of rows) {
        lines.push(
          [b.title, b.start, b.end, b.takeoutOrdered, b.takeoutSkipped, b.takeoutPostponed].map(q).join(",")
        );
      }
      if (rows.length) {
        const sum = (f) => rows.reduce((s, b) => s + f(b), 0);
        lines.push(
          ["合计", "", "", sum((b) => b.takeoutOrdered), sum((b) => b.takeoutSkipped), sum((b) => b.takeoutPostponed)].map(q).join(",")
        );
      }
      void saveExportFile(tkExportName("csv"), new Blob(["\uFEFF" + lines.join("\r\n")], { type: "text/csv;charset=utf-8" }));
    });
    $("tk-exp-png").addEventListener("click", () => {
      const svg = document.querySelector("#tk-plot svg");
      if (!svg) return;
      try {
        const clone = svg.cloneNode(true);
        clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
        const cs = getComputedStyle(document.querySelector(".rest-chart") ?? document.body);
        const v = (name, fb) => cs.getPropertyValue(name).trim() || fb;
        const st = document.createElementNS("http://www.w3.org/2000/svg", "style");
        st.textContent = `text{fill:${v("--text-2", "#888888")};font-size:11px;font-variant-numeric:tabular-nums;font-family:${cs.fontFamily};}.rc-cap{font-weight:600;}.grid{stroke:${v("--border", "#dddddd")};stroke-width:1;}.rc-axis{stroke:${v("--text-2", "#888888")};stroke-opacity:0.5;}.rc-avgline{stroke:${v("--text-2", "#888888")};stroke-opacity:0.6;stroke-dasharray:5 4;}.rc-avglabel{fill:${v("--text", "#333333")};font-size:11px;font-weight:600;}.rc-peak{font-size:10px;font-weight:600;}.rc-peak.tk-l-ord{fill:#2fa3b5;}.rc-peak.tk-l-can{fill:#e05555;}.rc-peak.tk-l-post{fill:#e0a23f;}path.rc-line{fill:none;stroke-width:2;}.tk-l-ord{stroke:#2fa3b5;}.tk-l-can{stroke:#e05555;}.tk-l-post{stroke:#e0a23f;}circle.rc-dot.tk-l-ord{fill:#2fa3b5;}circle.rc-dot.tk-l-can{fill:#e05555;}circle.rc-dot.tk-l-post{fill:#e0a23f;}.rc-cursor{display:none;}`;
        clone.insertBefore(st, clone.firstChild);
        const xml = new XMLSerializer().serializeToString(clone);
        const img = new Image();
        img.onload = () => {
          try {
            const cv = document.createElement("canvas");
            cv.width = RC_W * 2;
            cv.height = RC_H * 2;
            const ctx = cv.getContext("2d");
            if (!ctx) return;
            ctx.fillStyle = v("--card", "#ffffff");
            ctx.fillRect(0, 0, cv.width, cv.height);
            ctx.drawImage(img, 0, 0, cv.width, cv.height);
            cv.toBlob((blob) => {
              if (!blob) return;
              void saveExportFile(tkExportName("png"), blob);
            }, "image/png");
          } catch {
            toast("导出图片失败");
          }
        };
        img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(xml);
      } catch {
        toast("导出图片失败");
      }
    });
    $("tk-table").addEventListener("click", (e) => {
      const th = e.target.closest("th.sortable");
      if (!th) return;
      const key = th.dataset.key;
      if (tkTableSort.key === key) tkTableSort.dir = tkTableSort.dir === "asc" ? "desc" : "asc";
      else tkTableSort = { key, dir: "desc" };
      tkRenderTable(tkTableData);
    });
    tkRenderControls();
    void rcGetEarliest().then((e) => {
      if (e) tkRenderControls();
    });
  }
  var CK_NS = "wr-ckv2";
  function ckLoadStr(key) {
    return localStorage.getItem(`${CK_NS}-${key}`);
  }
  function ckSaveStr(key, v) {
    localStorage.setItem(`${CK_NS}-${key}`, v);
  }
  function ckEsc(s) {
    return s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
  }
  var CK_PALETTE = ["#2fa3b5", "#e05555", "#e0a23f", "#7b68d9", "#4caf7d", "#d96aa8", "#5b8def", "#9a7b4f"];
  var ckSeries = [];
  var ckUnit = ckLoadStr("unit") || "";
  if (!RC_WINDOWS[ckUnit]) ckUnit = "day";
  var ckShowEmpty = ckLoadStr("empty") === "1";
  function ckLegendOn() {
    const raw = ckLoadStr("on");
    try {
      return JSON.parse(raw || "{}");
    } catch {
      return {};
    }
  }
  // 同 rcGetRange：默认范围现算，只有手动范围才缓存（否则跨天后停在昨天）
  var ckRangeCache = {};
  function ckGetRange(unit) {
    const cached = ckRangeCache[unit];
    if (cached) return cached;
    try {
      const raw = JSON.parse(ckLoadStr(`range-${unit}`) || "null");
      if (raw && typeof raw.start === "string" && typeof raw.end === "string" && raw.start <= raw.end) {
        const r = { start: raw.start, end: raw.end };
        ckRangeCache[unit] = r;
        return r;
      }
    } catch {
    }
    return rcDefaultRange(unit);
  }
  function ckSetRange(unit, r) {
    const norm2 = r.start <= r.end ? { start: r.start, end: r.end } : { start: r.end, end: r.start };
    ckRangeCache[unit] = norm2;
    ckSaveStr(`range-${unit}`, JSON.stringify(norm2));
  }
  function ckResetRange(unit) {
    delete ckRangeCache[unit];
    localStorage.removeItem(`${CK_NS}-range-${unit}`);
  }
  function ckVal(b, id) {
    return b.customCounts[id] ?? 0;
  }
  function ckBucketTotal(b) {
    return Object.values(b.customCounts).reduce((s, v) => s + v, 0);
  }
  function ckEmptyBucket(b) {
    return ckBucketTotal(b) <= 0;
  }
  var ckSeq = 0;
  var ckLastDays = [];
  var ckPrevOf = null;
  function ckRenderBadges(shown, unit, prevTotal, top, topN, seriesCount) {
    const box = $("ck-badges");
    if (!box) return;
    const total = shown.reduce((s, b) => s + ckBucketTotal(b), 0);
    const unitLabel = { day: "日均", week: "周均", month: "月均", quarter: "季均", year: "年均" }[unit];
    let env = "";
    if (prevTotal > 0) {
      const pct = Math.round((total - prevTotal) / prevTotal * 100);
      env = pct === 0 ? "较上期持平" : `较上期 ${pct > 0 ? "+" : "−"}${Math.abs(pct)}%`;
    }
    const badge = (k, v, sub) => `<div class="rc-badge"><span class="k">${k}</span><b>${v}</b>${sub ? `<i>${sub}</i>` : ""}</div>`;
    box.innerHTML = badge("触发次数", String(total), env) + badge(
      `${unitLabel}触发`,
      shown.length ? String(Math.round(total / shown.length * 10) / 10) : "—",
      `${shown.length} 个周期`
    ) + badge("最活跃提醒", top ? `${topN} 次` : "—", top ? ckEsc(top.label) : "") + badge("提醒条数", String(seriesCount), seriesCount ? "含已删除提醒的历史" : "");
  }
  async function renderCustomReportChart(animate = false) {
    const seq = ++ckSeq;
    const range = ckGetRange(ckUnit);
    rcSyncRangeLabel("ck", ckUnit, range);
    const buckets = rcBuckets(ckUnit, range);
    const prevBuckets = rcBuckets(ckUnit, rcPrevRange(ckUnit, range, buckets.length));
    const anchor = rcAnchorToDay(ckUnit, prevBuckets[0]?.start ?? range.start);
    const data = await window.api.getHistory(anchor, null);
    if (seq !== ckSeq) return;
    ckLastDays = data.days ?? [];
    rcFillBuckets(buckets, ckLastDays);
    rcFillBuckets(prevBuckets, ckLastDays);
    const seen = /* @__PURE__ */ new Set();
    for (const b of [...buckets, ...prevBuckets]) for (const id of Object.keys(b.customCounts)) seen.add(id);
    const names = data.customNames ?? {};
    const live = (S.customBreaks ?? []).map((cb) => ({ id: cb.id, label: cb.name, color: "" }));
    const hist = [...seen].filter((id) => !live.some((s) => s.id === id)).sort((a, b) => a.localeCompare(b)).map((id) => ({ id, label: names[id] || "已删除的提醒", color: "" }));
    const series = [...live, ...hist];
    const order = series.map((s) => s.id).sort();
    for (const s of series) s.color = CK_PALETTE[order.indexOf(s.id) % CK_PALETTE.length];
    ckSeries = series;
    const shown = ckShowEmpty ? buckets : buckets.filter((b) => !ckEmptyBucket(b));
    const fullIdx = new Map(buckets.map((b, i) => [b.key, i]));
    ckPrevOf = (b) => {
      const i = fullIdx.get(b.key);
      return i !== void 0 && i > 0 ? buckets[i - 1] : null;
    };
    const perId = /* @__PURE__ */ new Map();
    for (const b of shown) for (const [id, v] of Object.entries(b.customCounts)) perId.set(id, (perId.get(id) ?? 0) + v);
    let top = null;
    let topN = 0;
    for (const s of series) {
      const n = perId.get(s.id) ?? 0;
      if (n > topN) {
        top = s;
        topN = n;
      }
    }
    ckRenderBadges(shown, ckUnit, prevBuckets.reduce((s, b) => s + ckBucketTotal(b), 0), top, topN, series.length);
    ckDraw(shown, animate);
    ckRenderLegend();
    ckRenderTable(buckets.filter((b) => !ckEmptyBucket(b)));
  }
  function ckRenderLegend() {
    const box = $("ck-legend");
    if (!box) return;
    const on = ckLegendOn();
    box.innerHTML = ckSeries.map(
      (s) => `<button type="button" class="rc-legend-item${on[s.id] === false ? " off" : ""}" data-s="${s.id}" title="点击显示 / 隐藏"><i class="line" style="background:${s.color}"></i>${ckEsc(s.label)}</button>`
    ).join("");
  }
  var ckTableSort = { key: "period", dir: "desc" };
  var ckTableData = [];
  function ckRenderTable(rows) {
    const t = $("ck-table");
    if (!t) return;
    ckTableData = rows;
    const th = (key, label) => {
      const arrow = key === ckTableSort.key ? ckTableSort.dir === "asc" ? " ▲" : " ▼" : "";
      const num = key === "period" ? "" : " num";
      return `<th class="sortable${num}" data-key="${key}" title="点按排序">${ckEsc(label)}${arrow}</th>`;
    };
    const head = "<tr>" + th("period", "统计周期") + ckSeries.map((s) => th(s.id, s.label)).join("") + "</tr>";
    const dir = ckTableSort.dir === "asc" ? 1 : -1;
    const sorted = [...rows].sort((a, b) => {
      const va = ckTableSort.key === "period" ? a.start : ckVal(a, ckTableSort.key);
      const vb = ckTableSort.key === "period" ? b.start : ckVal(b, ckTableSort.key);
      return (va < vb ? -1 : va > vb ? 1 : 0) * dir;
    });
    const cells = (b) => `<td>${b.title}</td>` + ckSeries.map((s) => `<td class="num" data-v="${ckVal(b, s.id)}">${ckVal(b, s.id)}</td>`).join("");
    const body = rows.length ? sorted.map((b) => `<tr>${cells(b)}</tr>`).join("") : '<tr><td colspan="2" class="rc-empty">暂无数据</td></tr>';
    const total = rows.length ? '<tr class="total"><td>合计</td>' + ckSeries.map((s) => `<td class="num">${rows.reduce((sum, b) => sum + ckVal(b, s.id), 0)}</td>`).join("") + "</tr>" : "";
    t.innerHTML = head + body + total;
  }
  var ckTip = null;
  function ckHideTip() {
    if (ckTip) ckTip.style.display = "none";
  }
  function ckShowTip(b, x, y) {
    if (!ckTip) {
      ckTip = document.createElement("div");
      ckTip.className = "rc-tip";
      document.body.append(ckTip);
    }
    let env = "";
    const prev = ckPrevOf?.(b) ?? null;
    if (prev) {
      const cur = ckBucketTotal(b);
      const old = ckBucketTotal(prev);
      if (old > 0 || cur > 0) {
        const p = old > 0 ? Math.round((cur - old) / old * 100) : null;
        env = `<span class="rc-tip-d">较上一期 ${p === null ? "—" : p === 0 ? "持平" : `${p > 0 ? "+" : "−"}${Math.abs(p)}%`}</span>`;
      }
    }
    // 与图例、表格列对齐：**所有提醒都列**（本期 0 次的用暗色显示）。
    // 只列 >0 的话，“这一期哪条提醒没触发”在浮层里看不出来，行数还随期数上下跳。
    const rows = ckSeries.map((s) => {
      const v = ckVal(b, s.id);
      return v > 0
        ? `<span style="color:${s.color}">${ckEsc(s.label)} ${v} 次</span>`
        : `<span class="ckv-0">${ckEsc(s.label)} 0 次</span>`;
    }).join("");
    ckTip.innerHTML = `<b>${b.title}</b><span>触发 ${ckBucketTotal(b)} 次</span>` + (rows || "<span>该周期没有可统计的提醒</span>") + env;
    ckTip.style.display = "block";
    const r = ckTip.getBoundingClientRect();
    ckTip.style.left = `${Math.max(8, Math.min(window.innerWidth - r.width - 8, x + 14))}px`;
    ckTip.style.top = `${Math.max(8, Math.min(window.innerHeight - r.height - 8, y - r.height - 12))}px`;
  }
  var ckZoom = null;
  var ckDrag = null;
  var ckDragMoved = false;
  function ckDraw(buckets, animate = false) {
    const box = $("ck-plot");
    const on = ckLegendOn();
    const n = buckets.length;
    if (ckZoom && (ckZoom.total !== n || ckZoom.o >= n)) ckZoom = null;
    const zo = ckZoom ? Math.min(ckZoom.o, Math.max(0, n - ckZoom.n)) : 0;
    const zv = ckZoom ? Math.min(ckZoom.n, n - zo) : n;
    const view = zv === n ? buckets : buckets.slice(zo, zo + zv);
    const iw = RC_W - RC_M.left - RC_M.right;
    const ih = RC_H - RC_M.top - RC_M.bottom;
    const lineParts = ckSeries.filter((s) => on[s.id] !== false);
    const maxCnt = buckets.reduce((mv, b) => Math.max(mv, ckBucketTotal(b)), 0);
    const ax = rcProbeAxis(maxCnt);
    const yC = (v) => RC_M.top + ih * (1 - v / ax.max);
    const step = iw / Math.max(1, zv);
    const showDots = zv <= 60;
    const showPeaks = zv <= 40;
    const defs = `<defs>` + ckSeries.map(
      (s) => `<linearGradient id="ck-a-${s.id}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${s.color}" stop-opacity="0.2"/><stop offset="1" stop-color="${s.color}" stop-opacity="0"/></linearGradient>`
    ).join("") + `</defs>`;
    let g = "";
    g += `<line class="rc-axis" x1="${RC_M.left}" y1="${RC_M.top}" x2="${RC_M.left}" y2="${(RC_M.top + ih).toFixed(1)}"/>`;
    for (let v = 0; v <= ax.max; v += ax.step) {
      const yy = yC(v).toFixed(1);
      g += `<line class="grid" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/><text class="tick" x="${RC_M.left - 6}" y="${+yy + 4}" text-anchor="end">${v}</text>`;
    }
    g += `<text class="tick rc-cap" x="${RC_M.left - 6}" y="10" text-anchor="end">次数</text>`;
    const withData = buckets.filter((b) => !ckEmptyBucket(b));
    if (withData.length) {
      const avg = withData.reduce((s, b) => s + ckBucketTotal(b), 0) / withData.length;
      if (avg > 0 && avg < ax.max) {
        const yy = yC(avg).toFixed(1);
        const txt = avg < 10 ? String(Math.round(avg * 10) / 10) : String(Math.round(avg));
        g += `<line class="rc-avgline" x1="${RC_M.left}" y1="${yy}" x2="${RC_W - RC_M.right}" y2="${yy}"/><text class="rc-avglabel" x="${RC_W - RC_M.right - 4}" y="${+yy - 4}" text-anchor="end">平均 ${txt} 次</text>`;
      }
    }
    for (const s of lineParts) {
      const segs = [];
      let pts = [];
      view.forEach((b, i) => {
        const p = { x: RC_M.left + step * i + step / 2, y: yC(ckVal(b, s.id)), b };
        if (ckEmptyBucket(b)) {
          if (pts.length) segs.push(pts);
          pts = [];
        } else pts.push(p);
      });
      if (pts.length) segs.push(pts);
      const base = yC(0).toFixed(1);
      for (const seg of segs) {
        const d = rcSmoothPath(seg);
        if (d) {
          if (seg.length >= 2) {
            const first = seg[0];
            const last = seg[seg.length - 1];
            g += `<path class="rc-area" d="${d} L ${last.x.toFixed(1)} ${base} L ${first.x.toFixed(1)} ${base} Z" style="fill:url(#ck-a-${s.id})"/>`;
          }
          g += `<path class="rc-line" d="${d}" stroke="${s.color}"${animate ? ' pathLength="1"' : ""}/>`;
          if (showPeaks && seg.length >= 2) {
            const pk = seg.reduce((m, p) => ckVal(p.b, s.id) > ckVal(m.b, s.id) ? p : m, seg[0]);
            if (ckVal(pk.b, s.id) > 0) {
              g += `<text class="rc-peak" x="${pk.x.toFixed(1)}" y="${(pk.y - 7).toFixed(1)}" text-anchor="middle" fill="${s.color}">${ckVal(pk.b, s.id)}</text>`;
            }
          }
        }
        if (showDots) {
          seg.forEach((p) => {
            g += `<circle class="rc-dot" cx="${p.x.toFixed(1)}" cy="${p.y.toFixed(1)}" r="2.6" fill="${s.color}"/>`;
          });
        }
      }
    }
    const labelEvery = zv <= 10 ? 1 : Math.ceil(zv / 10);
    view.forEach((b, i) => {
      if (i % labelEvery === 0) {
        g += `<text class="tick" x="${(RC_M.left + step * i + step / 2).toFixed(1)}" y="${RC_H - 8}" text-anchor="middle">${b.label}</text>`;
      }
    });
    if (view.length === 0 || withData.length === 0) {
      g += `<text class="tick" x="${(RC_M.left + iw / 2).toFixed(1)}" y="${(RC_M.top + ih / 2).toFixed(1)}" text-anchor="middle">暂无数据</text>`;
    }
    g += `<line class="rc-cursor" x1="0" y1="${RC_M.top}" x2="0" y2="${(RC_M.top + ih).toFixed(1)}" style="display:none"/>`;
    g += `<rect class="rc-hit" x="${RC_M.left}" y="${RC_M.top}" width="${iw.toFixed(1)}" height="${ih.toFixed(1)}"/>`;
    box.innerHTML = `<svg viewBox="0 0 ${RC_W} ${RC_H}" role="img" tabindex="0" aria-label="自定义提醒报表图"${animate ? ' class="anim"' : ""}>${defs}${g}</svg>`;
    const svg = box.querySelector("svg");
    if (!svg) return;
    svg.style.cursor = ckZoom ? "grab" : "";
    const cursor = svg.querySelector(".rc-cursor");
    const centers = view.map((b, i) => ({ b, cx: RC_M.left + step * i + step / 2 }));
    let kbX = null;
    const nearest = (xSvg) => {
      if (!centers.length) return null;
      let best = centers[0];
      for (const c of centers) if (Math.abs(c.cx - xSvg) < Math.abs(best.cx - xSvg)) best = c;
      return xSvg < RC_M.left - 6 || xSvg > RC_W - RC_M.right + 6 ? null : best;
    };
    const placeCursor = (c, clientY) => {
      if (!cursor) return;
      const rect = svg.getBoundingClientRect();
      cursor.setAttribute("x1", c.cx.toFixed(1));
      cursor.setAttribute("x2", c.cx.toFixed(1));
      cursor.style.display = "";
      ckShowTip(c.b, rect.left + c.cx / RC_W * rect.width, clientY);
    };
    const xOf = (e) => {
      const rect = svg.getBoundingClientRect();
      return (e.clientX - rect.left) / rect.width * RC_W;
    };
    svg.addEventListener("mousemove", (e) => {
      const best = nearest(xOf(e));
      if (!best || !cursor) {
        if (cursor) cursor.style.display = "none";
        ckHideTip();
        return;
      }
      kbX = best.cx;
      placeCursor(best, e.clientY);
    });
    svg.addEventListener("mouseleave", () => {
      if (cursor) cursor.style.display = "none";
      ckHideTip();
      ckDrag = null;
    });
    svg.addEventListener("wheel", (e) => {
      if (!n) return;
      e.preventDefault();
      const horiz = Math.abs(e.deltaX) > Math.abs(e.deltaY) || e.shiftKey;
      if (horiz && ckZoom) {
        const per = iw / ckZoom.n;
        const o = Math.round(ckZoom.o + (e.deltaX || e.deltaY) / per);
        ckZoom = { ...ckZoom, o: Math.max(0, Math.min(n - ckZoom.n, o)) };
        ckDraw(buckets);
        return;
      }
      const rect = svg.getBoundingClientRect();
      const xSvg = (e.clientX - rect.left) / rect.width * RC_W;
      const anchor = Math.max(0, Math.min(n - 1, Math.floor((xSvg - RC_M.left) / (iw / n))));
      const cur = ckZoom ?? { o: 0, n, total: n };
      let wn = Math.round(cur.n * (e.deltaY < 0 ? 1 / 1.35 : 1.35));
      wn = Math.min(n, Math.max(3, wn));
      if (wn >= n) {
        ckZoom = null;
      } else {
        const o = Math.round(anchor - (anchor - cur.o) * wn / cur.n);
        ckZoom = { o: Math.max(0, Math.min(n - wn, o)), n: wn, total: n };
      }
      ckDraw(buckets);
    });
    svg.addEventListener("mousedown", (e) => {
      ckDrag = ckZoom ? { startX: e.clientX, o0: ckZoom.o } : null;
      ckDragMoved = false;
      if (ckDrag) svg.style.cursor = "grabbing";
    });
    svg.addEventListener("mousemove", (e) => {
      if (!ckDrag || !ckZoom) return;
      if (e.buttons === 0) {
        ckDrag = null;
        svg.style.cursor = "grab";
        return;
      }
      const dx = e.clientX - ckDrag.startX;
      if (!ckDragMoved && Math.abs(dx) < 3) return;
      ckDragMoved = true;
      const o = Math.round(ckDrag.o0 - dx / (iw / ckZoom.n));
      const o2 = Math.max(0, Math.min(n - ckZoom.n, o));
      if (o2 !== ckZoom.o) {
        ckZoom = { ...ckZoom, o: o2 };
        ckDraw(buckets);
      }
    });
    svg.addEventListener("mouseup", () => {
      ckDrag = null;
      svg.style.cursor = ckZoom ? "grab" : "";
    });
    svg.addEventListener("click", (e) => {
      if (ckDragMoved) {
        ckDragMoved = false;
        return;
      }
      const best = nearest(xOf(e));
      if (!best || ckUnit === "day") return;
      e.stopPropagation();
      const rect = svg.getBoundingClientRect();
      ckOpenDetail(best.b, rect.left + best.cx / RC_W * rect.width);
    });
    svg.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        if (cursor) cursor.style.display = "none";
        ckHideTip();
        return;
      }
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      e.preventDefault();
      if (!centers.length || !cursor) return;
      let idx = 0;
      if (kbX !== null) {
        idx = centers.reduce((bi, c, i) => Math.abs(c.cx - kbX) < Math.abs(centers[bi].cx - kbX) ? i : bi, 0);
        idx = e.key === "ArrowRight" ? Math.min(centers.length - 1, idx + 1) : Math.max(0, idx - 1);
      }
      kbX = centers[idx].cx;
      const rect = svg.getBoundingClientRect();
      placeCursor(centers[idx], rect.top + (RC_M.top + ih / 2) * (rect.height / RC_H));
    });
  }
  var ckDetailEl = null;
  function ckCloseDetail() {
    if (ckDetailEl) ckDetailEl.classList.remove("open");
  }
  function ckOpenDetail(b, clientX) {
    const host = document.getElementById("custom-chart");
    if (!host) return;
    if (!ckDetailEl) {
      ckDetailEl = document.createElement("div");
      ckDetailEl.className = "rc-detail";
      host.append(ckDetailEl);
    }
    const days = ckLastDays.filter((d) => d.date >= b.start && d.date <= b.end).sort((x, y) => x.date < y.date ? -1 : 1);
    const span = Math.max(1, ckSeries.length + 1);
    const rows = days.map(
      (d) => `<tr><td>${d.date}</td>` + ckSeries.map((s) => `<td>${d.customCounts?.[s.id] ?? 0}</td>`).join("") + "</tr>"
    ).join("");
    ckDetailEl.innerHTML = `<div class="rc-detail-head"><b>${b.title}</b><button type="button" class="rc-detail-x" title="关闭">✕</button></div><div class="rc-detail-scroll"><table class="stats-table"><tr><th>日期</th>${ckSeries.map((s) => `<th>${ckEsc(s.label)}</th>`).join("")}</tr>` + (rows || `<tr><td colspan="${span}" class="rc-empty">该周期没有逐日数据</td></tr>`) + "</table></div>";
    ckDetailEl.classList.add("open");
    ckDetailEl.querySelector(".rc-detail-x")?.addEventListener("click", ckCloseDetail);
    const hr = host.getBoundingClientRect();
    ckDetailEl.style.left = `${Math.max(8, Math.min(hr.width - 292, clientX - hr.left - 140))}px`;
    ckDetailEl.style.top = "44px";
    ckHideTip();
  }
  function ckRenderControls() {
    $("ck-units").innerHTML = RC_UNITS.map(
      (x) => `<button type="button" class="rc-unit${x.u === ckUnit ? " on" : ""}" data-u="${x.u}">${x.label}</button>`
    ).join("");
    ckRenderRange();
  }
  function ckRenderRange() {
    const box = $("ck-range");
    const r = ckGetRange(ckUnit);
    box.innerHTML = `<button type="button" class="rc-range-btn" id="ck-range-btn" title="选择时间范围"><span id="ck-range-label">${rcRangeLabel(ckUnit, r)}</span><span class="rc-caret">▾</span></button><div class="rc-pop">${rcPickerHtml(ckUnit, r).replaceAll('id="rc-rs"', 'id="ck-rs"').replaceAll('id="rc-re"', 'id="ck-re"')}<div class="rc-pop-foot"><button type="button" class="btn ghost" id="ck-range-all" title="从最早记录一直看到当前周期">全部数据</button><button type="button" class="btn ghost" id="ck-range-reset">恢复默认</button></div></div>`;
    $("ck-range-btn").addEventListener("click", () => box.classList.toggle("open"));
    $("ck-range-all").addEventListener("click", () => {
      const all = rcAllRange(ckUnit);
      if (!all) {
        toast("还没有统计数据");
        return;
      }
      ckSetRange(ckUnit, all);
      box.classList.remove("open");
      ckRenderControls();
      void renderCustomReportChart(true);
    });
    $("ck-range-reset").addEventListener("click", () => {
      ckResetRange(ckUnit);
      ckRenderControls();
      void renderCustomReportChart(true);
    });
    const startCtl = box.querySelector("#ck-rs");
    const endCtl = box.querySelector("#ck-re");
    const apply = () => {
      if (!startCtl || !endCtl) return;
      ckSetRange(ckUnit, { start: startCtl.value, end: endCtl.value });
      const label = $("ck-range-label");
      if (label) label.textContent = rcRangeLabel(ckUnit, ckGetRange(ckUnit));
      void renderCustomReportChart(true);
    };
    startCtl?.addEventListener("change", apply);
    endCtl?.addEventListener("change", apply);
  }
  function ckExportName(ext) {
    const ul = RC_UNITS.find((x) => x.u === ckUnit)?.label ?? ckUnit;
    const fp = rcRangeFilePart(ckUnit, ckGetRange(ckUnit));
    return `自定义提醒报表_${ul}_${fp.s}_${fp.e}.${ext}`;
  }
  function setupCustomReportChart() {
    $("ck-units").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-unit");
      if (!b) return;
      ckUnit = b.dataset.u;
      ckSaveStr("unit", ckUnit);
      ckRenderControls();
      void renderCustomReportChart(true);
    });
    $("ck-legend").addEventListener("click", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const on = ckLegendOn();
      const id = b.dataset.s ?? "";
      on[id] = on[id] === false;
      ckSaveStr("on", JSON.stringify(on));
      ckRenderLegend();
      void renderCustomReportChart();
    });
    $("ck-legend").addEventListener("dblclick", (e) => {
      const b = e.target.closest(".rc-legend-item");
      if (!b) return;
      const id = b.dataset.s ?? "";
      const on = ckLegendOn();
      const only = ckSeries.every((s) => on[s.id] !== false === (s.id === id));
      const next = {};
      if (only) for (const s of ckSeries) next[s.id] = true;
      else {
        for (const s of ckSeries) next[s.id] = false;
        next[id] = true;
      }
      ckSaveStr("on", JSON.stringify(next));
      ckRenderLegend();
      void renderCustomReportChart();
    });
    const emptyBtn = $("ck-empty-btn");
    emptyBtn.classList.toggle("on", ckShowEmpty);
    emptyBtn.addEventListener("click", () => {
      ckShowEmpty = !ckShowEmpty;
      ckSaveStr("empty", ckShowEmpty ? "1" : "0");
      emptyBtn.classList.toggle("on", ckShowEmpty);
      void renderCustomReportChart();
    });
    $("ck-exp-csv").addEventListener("click", () => {
      const rows = [...ckTableData].sort((a2, b) => a2.start < b.start ? -1 : 1);
      const q = (v) => `"${String(v).replace(/"/g, '""')}"`;
      const lines = [["统计周期", "开始", "结束", ...ckSeries.map((s) => s.label)].map(q).join(",")];
      for (const b of rows) {
        lines.push([b.title, b.start, b.end, ...ckSeries.map((s) => ckVal(b, s.id))].map(q).join(","));
      }
      if (rows.length) {
        lines.push(
          ["合计", "", "", ...ckSeries.map((s) => rows.reduce((sum, b) => sum + ckVal(b, s.id), 0))].map(q).join(",")
        );
      }
      void saveExportFile(ckExportName("csv"), new Blob(["\uFEFF" + lines.join("\r\n")], { type: "text/csv;charset=utf-8" }));
    });
    $("ck-exp-png").addEventListener("click", () => {
      const svg = document.querySelector("#ck-plot svg");
      if (!svg) return;
      try {
        const clone = svg.cloneNode(true);
        clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
        const cs = getComputedStyle(document.querySelector(".rest-chart") ?? document.body);
        const v = (name, fb) => cs.getPropertyValue(name).trim() || fb;
        const st = document.createElementNS("http://www.w3.org/2000/svg", "style");
        st.textContent = `text{fill:${v("--text-2", "#888888")};font-size:11px;font-variant-numeric:tabular-nums;font-family:${cs.fontFamily};}.rc-cap{font-weight:600;}.grid{stroke:${v("--border", "#dddddd")};stroke-width:1;}.rc-axis{stroke:${v("--text-2", "#888888")};stroke-opacity:0.5;}.rc-avgline{stroke:${v("--text-2", "#888888")};stroke-opacity:0.6;stroke-dasharray:5 4;}.rc-avglabel{fill:${v("--text", "#333333")};font-size:11px;font-weight:600;}.rc-peak{font-size:10px;font-weight:600;}path.rc-line{fill:none;stroke-width:2;}.rc-cursor{display:none;}`;
        clone.insertBefore(st, clone.firstChild);
        const xml = new XMLSerializer().serializeToString(clone);
        const img = new Image();
        img.onload = () => {
          try {
            const cv = document.createElement("canvas");
            cv.width = RC_W * 2;
            cv.height = RC_H * 2;
            const ctx = cv.getContext("2d");
            if (!ctx) return;
            ctx.fillStyle = v("--card", "#ffffff");
            ctx.fillRect(0, 0, cv.width, cv.height);
            ctx.drawImage(img, 0, 0, cv.width, cv.height);
            cv.toBlob((blob) => {
              if (!blob) return;
              void saveExportFile(ckExportName("png"), blob);
            }, "image/png");
          } catch {
            toast("导出图片失败");
          }
        };
        img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(xml);
      } catch {
        toast("导出图片失败");
      }
    });
    $("ck-table").addEventListener("click", (e) => {
      const th = e.target.closest("th.sortable");
      if (!th) return;
      const key = th.dataset.key ?? "period";
      if (ckTableSort.key === key) ckTableSort.dir = ckTableSort.dir === "asc" ? "desc" : "asc";
      else ckTableSort = { key, dir: "desc" };
      ckRenderTable(ckTableData);
    });
    ckRenderControls();
    void rcGetEarliest().then((e) => {
      if (e) ckRenderControls();
    });
  }
  var runningApps = [];
  var APP_NAME_MIN_W = 72;
  var APP_NAME_MAX_W = 190;
  var nameMeasure = document.createElement("canvas").getContext("2d");
  function fitNameInput(input) {
    const cs = getComputedStyle(input);
    const pad = parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight) + parseFloat(cs.borderLeftWidth) + parseFloat(cs.borderRightWidth);
    const ref = input.value || input.placeholder || "";
    let w = ref.length * 8;
    if (nameMeasure) {
      nameMeasure.font = `${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
      w = nameMeasure.measureText(ref).width;
    }
    const px = Math.ceil(w + pad + 8);
    input.style.width = `${Math.min(APP_NAME_MAX_W, Math.max(APP_NAME_MIN_W, px))}px`;
  }
  function buildAppRow(item) {
    const row = document.createElement("div");
    row.className = "tk-item" + (item.enabled ? "" : " disabled");
    row.dataset.appId = item.id;
    const on = document.createElement("input");
    on.type = "checkbox";
    on.checked = item.enabled;
    on.title = "启用 / 停用这条规则";
    on.addEventListener("change", () => {
      item.enabled = on.checked;
      row.classList.toggle("disabled", !on.checked);
      autoSave();
    });
    const exe = document.createElement("input");
    exe.type = "text";
    exe.className = "app-exe";
    exe.value = item.exe;
    exe.placeholder = "可执行文件名，如 Code.exe";
    exe.title = "可执行文件名：忽略路径、不区分大小写，用它跟正在运行的程序比对";
    const name = document.createElement("input");
    name.type = "text";
    name.className = "app-name";
    name.value = item.name;
    name.maxLength = APP_NAME_MAX;
    name.placeholder = appPauseLabel(item.exe) || "软件名（可选）";
    name.title = "软件名：只影响显示，可留空；自己改过之后就不会再被自动改名覆盖";
    name.addEventListener("input", () => fitNameInput(name));
    name.addEventListener("change", () => {
      item.name = normalizeAppName(name.value);
      name.value = item.name;
      fitNameInput(name);
      autoSave();
    });
    exe.addEventListener("change", () => {
      const v = exeBasename(exe.value);
      const clash = !v || S.appPause.items.some((i) => i !== item && i.exe.toLowerCase() === v.toLowerCase());
      if (clash) {
        toast(v ? `${v} 已在列表中` : "可执行文件名不能为空");
        exe.value = item.exe;
        return;
      }
      const from = item.exe;
      item.exe = v;
      exe.value = v;
      item.name = syncAppName(item.name, from, v);
      name.value = item.name;
      name.placeholder = appPauseLabel(v) || "软件名（可选）";
      fitNameInput(name);
      autoSave();
    });
    const del = mkBtn("✕", "删除这条规则", () => {
      void confirmDialog({
        title: "删除这条应用规则？",
        desc: "删除后立即生效，无法撤销。",
        detail: item.name ? `${item.name}（${item.exe}）` : item.exe,
        okText: "删除"
      }).then((ok) => {
        if (!ok) return;
        S.appPause.items = S.appPause.items.filter((i) => i.id !== item.id);
        renderAppList();
        autoSave();
      });
    });
    del.classList.add("danger");
    row.append(on, exe, name, del);
    return row;
  }
  function renderAppList() {
    const list = $("app-pause-list");
    if (!list || !S.appPause) return;
    const sum = document.getElementById("app-advanced-summary");
    if (sum) sum.textContent = S.appPause.items.length > 0 ? `${S.appPause.items.length} 项` : "空";
    list.innerHTML = "";
    if (S.appPause.items.length === 0) {
      const empty = document.createElement("div");
      empty.className = "tk-empty";
      empty.textContent = "还没有指定应用，点上方按钮添加";
      list.appendChild(empty);
      return;
    }
    for (const item of S.appPause.items) list.appendChild(buildAppRow(item));
    for (const el of list.querySelectorAll(".app-name")) fitNameInput(el);
  }
  function focusAppName(id) {
    const row = Array.from(document.querySelectorAll("#app-pause-list .tk-item")).find(
      (r) => r.dataset.appId === id
    );
    const input = row ? row.querySelector(".app-name") : null;
    if (!input) return;
    input.focus();
    input.select();
  }
  function addApps(names) {
    if (!S.appPause) return [];
    const have = new Set(S.appPause.items.map((i) => i.exe.toLowerCase()));
    const added = [];
    for (const raw of names) {
      const exe = exeBasename(raw);
      const key = exe.toLowerCase();
      if (!exe || have.has(key)) continue;
      have.add(key);
      const item = { id: newId(), exe, name: appPauseLabel(exe), enabled: true };
      S.appPause.items.push(item);
      added.push(item);
    }
    if (added.length > 0) {
      renderAppList();
      autoSave();
    }
    return added;
  }
  function renderAppPickItems(filter) {
    const list = $("app-pick-list");
    if (!list) return;
    const kw = filter.trim().toLowerCase();
    const items = S.appPause ? S.appPause.items : [];
    const have = new Set(items.map((i) => i.exe.toLowerCase()));
    const names = runningApps.filter((n) => !kw || n.toLowerCase().includes(kw));
    list.innerHTML = "";
    if (names.length === 0) {
      const d = document.createElement("div");
      d.className = "tk-empty";
      d.textContent = runningApps.length === 0 ? "没有读到正在运行的程序" : "没有匹配的程序";
      list.appendChild(d);
      return;
    }
    for (const name of names) {
      const dup = have.has(name.toLowerCase());
      const row = document.createElement("div");
      row.className = "pk-item" + (dup ? " disabled" : "");
      const box = document.createElement("input");
      box.type = "checkbox";
      box.value = name;
      box.disabled = dup;
      const txt = document.createElement("span");
      txt.textContent = name;
      row.append(box, txt);
      const presetLabel = appPauseLabel(name);
      if (presetLabel) {
        const tag = document.createElement("span");
        tag.className = "app-alias";
        tag.textContent = presetLabel;
        row.appendChild(tag);
      }
      if (dup) {
        const tag = document.createElement("em");
        tag.textContent = "已在列表";
        row.appendChild(tag);
      }
      row.addEventListener("click", (e) => {
        if (box.disabled || e.target === box) return;
        box.checked = !box.checked;
      });
      list.appendChild(row);
    }
  }
  function openAppPickModal() {
    const modal = $("app-pick-modal");
    const list = $("app-pick-list");
    const note = $("app-pick-note");
    const filter = $("app-pick-filter");
    if (!modal || !list || !note || !filter) return;
    filter.value = "";
    list.innerHTML = "";
    note.textContent = "正在读取正在运行的程序…";
    modal.classList.remove("hidden");
    void window.api.listRunningApps().then((r) => {
      if (modal.classList.contains("hidden")) return;
      if (!r || !r.supported) {
        note.textContent = r && r.detail || "当前平台不支持读取程序列表，请改用“从应用安装路径选择”或“手动输入应用名称”。";
        return;
      }
      runningApps = r.names;
      note.textContent = `共 ${runningApps.length} 个正在运行的程序，勾选后点“添加”`;
      renderAppPickItems("");
      filter.focus();
    });
  }
  function setupAppPause() {
    const modal = $("app-pick-modal");
    $("app-pause-add").addEventListener("click", () => {
      void promptDialog({
        title: "手动输入应用名称",
        desc: "请输入可执行文件名，例如 WeMeetApp.exe。",
        okText: "添加"
      }).then((v) => {
        if (v === null) return;
        const added = addApps([v]);
        if (added.length === 0) {
          toast("没有新增（名称为空或已在列表中）");
          return;
        }
        toast(`已添加 ${added[0].exe}`);
        focusAppName(added[0].id);
      });
    });
    $("app-pause-file").addEventListener("click", async () => {
      const r = await window.api.pickAppExe();
      if (!r) return;
      const added = addApps([r.exe]);
      if (added.length === 0) {
        toast(`${r.exe} 已在列表中`);
        return;
      }
      toast(`已添加 ${added[0].exe}`);
      focusAppName(added[0].id);
    });
    $("app-pause-pick").addEventListener("click", () => openAppPickModal());
    $("app-pick-cancel").addEventListener("click", () => modal.classList.add("hidden"));
    modal.addEventListener("click", (e) => {
      if (e.target === modal) modal.classList.add("hidden");
    });
    $("app-pick-filter").addEventListener("input", (e) => {
      renderAppPickItems(e.target.value);
    });
    $("app-pick-ok").addEventListener("click", () => {
      const names = Array.from(
        $("app-pick-list").querySelectorAll("input[type=checkbox]:checked")
      ).map((b) => b.value);
      modal.classList.add("hidden");
      const added = addApps(names);
      toast(added.length > 0 ? `已添加 ${added.length} 个应用` : "没有新增（未勾选或都已在列表中）");
    });
    document.addEventListener("keydown", (e) => {
      if (modal.classList.contains("hidden")) return;
      if (e.key === "Escape") {
        e.preventDefault();
        modal.classList.add("hidden");
      }
    });
  }
  var ACCENT_PRESETS = [
    { hex: "#4f7cff", name: "经典蓝" },
    { hex: "#2d9cdb", name: "天青" },
    { hex: "#12a594", name: "松石绿" },
    { hex: "#2e9e5b", name: "森林绿" },
    { hex: "#77a82c", name: "橄榄绿" },
    { hex: "#e0a020", name: "琥珀" },
    { hex: "#ed7a35", name: "落日橘" },
    { hex: "#e2544c", name: "朱砂红" },
    { hex: "#d8478f", name: "玫红" },
    { hex: "#8b5cf6", name: "紫罗兰" },
    { hex: "#9a6a4f", name: "摩卡棕" },
    { hex: "#5a6474", name: "石墨灰" }
  ];
  var DEFAULT_ACCENT = "#4f7cff";
  function normalizeHex(raw) {
    const s = String(raw ?? "").trim().replace(/^#/, "");
    if (/^[0-9a-f]{3}$/i.test(s)) return `#${s[0]}${s[0]}${s[1]}${s[1]}${s[2]}${s[2]}`.toLowerCase();
    if (/^[0-9a-f]{6}$/i.test(s)) return `#${s}`.toLowerCase();
    return null;
  }
  function hexToRgb(hex) {
    const h = hex.replace("#", "");
    return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
  }
  function rgbToHex(r, g, b) {
    const c = (n) => Math.round(Math.min(255, Math.max(0, n))).toString(16).padStart(2, "0");
    return `#${c(r)}${c(g)}${c(b)}`;
  }
  function rgbToHsv(r, g, b) {
    const rr = r / 255;
    const gg = g / 255;
    const bb = b / 255;
    const max = Math.max(rr, gg, bb);
    const min = Math.min(rr, gg, bb);
    const d = max - min;
    let h = 0;
    if (d > 0) {
      if (max === rr) h = (gg - bb) / d % 6;
      else if (max === gg) h = (bb - rr) / d + 2;
      else h = (rr - gg) / d + 4;
      h *= 60;
      if (h < 0) h += 360;
    }
    return { h, s: max === 0 ? 0 : d / max, v: max };
  }
  function hsvToRgb(h, s, v) {
    const hh = (h % 360 + 360) % 360 / 60;
    const c = v * s;
    const x = c * (1 - Math.abs(hh % 2 - 1));
    const m = v - c;
    let rgb;
    if (hh < 1) rgb = [c, x, 0];
    else if (hh < 2) rgb = [x, c, 0];
    else if (hh < 3) rgb = [0, c, x];
    else if (hh < 4) rgb = [0, x, c];
    else if (hh < 5) rgb = [x, 0, c];
    else rgb = [c, 0, x];
    return [(rgb[0] + m) * 255, (rgb[1] + m) * 255, (rgb[2] + m) * 255];
  }
  var cpHex = DEFAULT_ACCENT;
  var cpH = 220;
  var cpS = 1;
  var cpV = 1;
  function setColorChip(hex) {
    $("ap-color-open").style.background = hex;
  }
  function cpRender(writeHex = true) {
    $("cp-pad").style.background = `linear-gradient(to top, #000000, rgba(0, 0, 0, 0)), linear-gradient(to right, #ffffff, rgba(255, 255, 255, 0)), hsl(${Math.round(cpH)}, 100%, 50%)`;
    $("cp-pad-thumb").style.left = `${cpS * 100}%`;
    $("cp-pad-thumb").style.top = `${(1 - cpV) * 100}%`;
    $("cp-hue-thumb").style.left = `${cpH / 360 * 100}%`;
    $("cp-preview").style.background = cpHex;
    if (writeHex) $("cp-hex").value = cpHex;
    for (const sw of document.querySelectorAll("#color-presets .cp-sw")) {
      sw.classList.toggle("on", sw.dataset.hex === cpHex);
    }
  }
  function cpSetColor(hex, syncHsv, writeHex = true) {
    const n = normalizeHex(hex);
    if (!n) return false;
    cpHex = n;
    if (syncHsv) {
      const [r, g, b] = hexToRgb(n);
      const hsv = rgbToHsv(r, g, b);
      cpH = hsv.h;
      cpS = hsv.s;
      cpV = hsv.v;
    }
    cpRender(writeHex);
    return true;
  }
  function cpPreviewOnly() {
    S.appearance.color = cpHex;
    setColorChip(cpHex);
    if (document.getElementById("sup-preview")) renderSupervisorPreview(curSupStage);
  }
  function cpCommit() {
    S.appearance.color = cpHex;
    setColorChip(cpHex);
    autoSave();
  }
  function cpFromHsv(live) {
    const [r, g, b] = hsvToRgb(cpH, cpS, cpV);
    if (!cpSetColor(rgbToHex(r, g, b), false)) return;
    if (live) cpPreviewOnly();
    else cpCommit();
  }
  function cpFlushHex() {
    const el = $("cp-hex");
    const n = normalizeHex(el.value);
    if (!n) {
      el.value = cpHex;
      return;
    }
    if (n !== cpHex) {
      cpSetColor(n, true);
      cpCommit();
      return;
    }
    el.value = cpHex;
  }
  function resetAccentColor() {
    cpSetColor(DEFAULT_ACCENT, true);
    cpCommit();
    toast("已恢复默认颜色");
  }
  function closeColorPicker() {
    cpFlushHex();
    $("color-modal").classList.add("hidden");
  }
  function bindDragArea(el, onMove, onEnd) {
    const at = (e) => {
      const r = el.getBoundingClientRect();
      if (r.width <= 0 || r.height <= 0) return [0, 0];
      return [
        Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)),
        Math.min(1, Math.max(0, (e.clientY - r.top) / r.height))
      ];
    };
    el.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      const [nx, ny] = at(e);
      onMove(nx, ny);
      const move = (ev) => {
        const [mx, my] = at(ev);
        onMove(mx, my);
      };
      const up = () => {
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
        if (onEnd) onEnd();
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
    });
  }
  function buildPresetSwatches() {
    const box = $("color-presets");
    if (box.childElementCount > 0) return;
    for (const p of ACCENT_PRESETS) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "cp-sw";
      b.dataset.hex = p.hex;
      b.title = `${p.name} ${p.hex}`;
      b.setAttribute("aria-label", p.name);
      b.style.background = p.hex;
      box.appendChild(b);
    }
  }
  function setupColorPicker() {
    buildPresetSwatches();
    setColorChip(normalizeHex(S.appearance.color) ?? DEFAULT_ACCENT);
    $("ap-color-open").addEventListener("click", () => {
      cpSetColor(normalizeHex(S.appearance.color) ?? DEFAULT_ACCENT, true);
      $("color-modal").classList.remove("hidden");
      $("cp-done").focus();
    });
    $("cp-done").addEventListener("click", closeColorPicker);
    $("color-modal").addEventListener("click", (e) => {
      if (e.target === $("color-modal")) closeColorPicker();
    });
    document.addEventListener("keydown", (e) => {
      if ($("color-modal").classList.contains("hidden")) return;
      if (e.key === "Escape") {
        e.preventDefault();
        closeColorPicker();
      }
    });
    $("color-presets").addEventListener("click", (e) => {
      const sw = e.target.closest(".cp-sw");
      if (sw && sw.dataset.hex && cpSetColor(sw.dataset.hex, true)) cpCommit();
    });
    bindDragArea(
      $("cp-pad"),
      (nx, ny) => {
        cpS = nx;
        cpV = 1 - ny;
        cpFromHsv(true);
      },
      () => cpFromHsv(false)
    );
    bindDragArea(
      $("cp-hue"),
      (nx) => {
        cpH = nx * 360;
        cpFromHsv(true);
      },
      () => cpFromHsv(false)
    );
    const hexIn = $("cp-hex");
    hexIn.addEventListener("input", () => {
      const n = normalizeHex(hexIn.value);
      if (n && cpSetColor(n, true, false)) cpCommit();
    });
    hexIn.addEventListener("change", cpFlushHex);
    hexIn.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        cpFlushHex();
      }
    });
    $("cp-reset").addEventListener("click", resetAccentColor);
  }
  async function autoSave() {
    collectForm();
    await window.api.saveSettings(S);
    if (supEditName) persistRole(supEditName);
    applyTheme();
    if (supPreview !== null || document.getElementById("sup-preview")) renderSupervisorPreview(curSupStage);
  }
  // 新增 / 删除提醒时间后，立即向设置页重拉一次状态：刷新“今日提醒时间线”与
  // 侧栏“下次外卖提醒 / 自定义提醒”迷你卡。后端 saveSettings 已会重算并推送，
  // 这里再拉一次是为消除保存往返带来的可见延迟，确保“立即”反映。
  async function refreshStatusNow() {
    try {
      const raw = await window.api.getState();
      renderStatus(raw);
    } catch (e) {
      console.error("[refreshStatusNow] 拉取状态失败", e);
    }
  }
  function bindAutoSave() {
    for (const el of document.querySelectorAll(".tab input, .tab select")) {
      if (el.closest("#tab-stats")) continue;
      bindAutoSaveEl(el);
    }
    // “系统空闲”勾选框额外管一件事：分钟框的可用性。落盘仍由上面的 autoSave 负责
    //（它排在前面，collectForm 已把新值写进 S，这边只动 DOM）。
    const idleCb = $("idlePause");
    if (idleCb && !idleCb.dataset.wrIdleSync) {
      idleCb.dataset.wrIdleSync = "1";
      idleCb.addEventListener("change", syncIdlePauseUI);
    }
  }
  // 单个控件补绑自动保存；dataset 标记防重复绑定（动态创建的卡片会和这里的
  // 全量扫描先后碰到同一批控件）。change 而非 input：与设置页其余保存口径一致。
  function bindAutoSaveEl(el) {
    if (el.dataset.wrAutosave) return;
    el.dataset.wrAutosave = "1";
    el.addEventListener("change", autoSave);
  }
  // 程序化切到指定 tab（与点击导航项同效），供自更新重启后恢复页面用。
  function goTab(name) {
    if (!name) return;
    exitAllMsgEditing();
    document.querySelectorAll(".nav-item").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
    document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
    const tab = $(`tab-${name}`);
    if (tab) tab.classList.add("active");
    if (name === "stats") void renderStats();
    void window.api.setActiveTab(name);
  }
  async function init() {
    S = await window.api.getSettings();
    applyTheme();
    for (const btn of document.querySelectorAll(".nav-item")) {
      btn.addEventListener("click", () => {
        exitAllMsgEditing();
        document.querySelectorAll(".nav-item").forEach((b) => b.classList.remove("active"));
        document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
        btn.classList.add("active");
        $(`tab-${btn.dataset.tab}`).classList.add("active");
        if (btn.dataset.tab === "stats") {
          void renderStats();
        }
        // 上报当前 tab 给 Go，供自更新重启后恢复“更新前所在的页面”
        void window.api.setActiveTab(btn.dataset.tab);
      });
    }
    renderCustomCards();
    setupCardFolds();
    setupRestChart();
    setupTakeoutChart();
    setupCustomReportChart();
    fillForm();
    ROLES = await window.api.listSupervisorRoles();
    ensureSupEditKey();
    fillSupervisorImageName();
    fillGeomControls(normalizeSupGeom(geomOf(currentGeomName())));
    setupShortcutCapture();
    BUILTIN_MSGS = await window.api.builtinMsgDefaults();
    setupMsgPanels();
    renderTakeoutList();
    setupAppPause();
    setupColorPicker();
    bindAutoSave();
    window.addEventListener("focus", () => {
      if (document.querySelector("#tab-stats.active")) void renderStats();
    });
    bindSoundPlayer();
    $("snd-music-toggle").addEventListener("click", () => {
      const body = $("snd-music-body");
      const open = body.classList.toggle("hidden") === false;
      $("snd-music-panel").classList.toggle("open", open);
    });
    $("snd-mode").addEventListener("click", () => {
      const cur = $("snd-mode").dataset.mode || "single";
      S.sound.mode = SND_MODE_NEXT[cur] || "single";
      setSoundModeUI(S.sound.mode);
      renderSoundMusic();
      autoSave();
    });
    $("snd-music-add").addEventListener("click", async () => {
      const r = await window.api.addMusicSound();
      if (r.canceled) return;
      if (!r.items || r.items.length === 0) {
        toast(r.error ?? "添加失败");
        return;
      }
      for (const it of r.items) {
        S.sound.music.push({
          id: `sm-${newId()}`,
          file: it.file,
          name: it.name,
          enabled: true
        });
      }
      renderSoundMusic();
      autoSave();
      toast(r.items.length > 1 ? `已添加 ${r.items.length} 首音乐` : "已添加到音乐列表");
    });
    setupGalleryPanel();
    bindRoleGalleryDrag();
    void renderRoleGallery();
    const supSelBtn = $("sup-selection-btn");
    supSelBtn.addEventListener("click", () => {
      const cur = supSelBtn.dataset.mode || S.supervisor.selection || "random";
      const next = DISPLAY_MODE_NEXT[cur] || "fixed";
      S.supervisor.selection = next;
      applyModeIcon(supSelBtn, next);
      autoSave();
      void renderRoleGallery();
    });
    $("sup-gallery-add").addEventListener("click", () => void addCustomRole());
    bindRoleMenuDismiss();
    setInterval(() => {
      renderSupervisorPreview(curSupStage === 1 ? 2 : 1);
    }, 5e3);
    $("sup-enabled").addEventListener("change", () => {
      // 实时同步表单态，避免依赖 saveSettings 的落盘时机——勾选/取消立刻反映到预览
      S.supervisor.enabled = $("sup-enabled").checked;
      renderSupervisorPreview(curSupStage);
    });
    // 督休专员卡的“预览”：先等配置落盘（含“启用”勾选），再弹真实预览窗——
    // 这样预览窗能如实反映当前“启用”状态（Go 侧总开关现在始终生效）
    $("sup-break-preview").addEventListener("click", async () => {
      await autoSave();
      await previewBreak("short", supEditName);
    });
    for (const id of ["sup-size", "sup-posx", "sup-posy"]) {
      $(id).addEventListener("input", () => {
        const g = readGeomFromForm();
        applySupGeometry(g);
        commitGeomToRole(g);
      });
    }
    $("sup-flip").addEventListener("change", () => {
      const g = readGeomFromForm();
      applySupGeometry(g);
      commitGeomToRole(g);
      autoSave();
    });
    $("sup-geom-reset").addEventListener("click", () => {
      const g = { ...SUP_GEOM_DEFAULT };
      commitGeomToRole(g);
      fillGeomControls(g);
      autoSave();
      toast("已恢复默认尺寸与位置");
    });
    $("tk-add").addEventListener("click", async () => {
      const slot = newSlot();
      S.takeout.slots.push(slot);
      renderTakeoutList();
      await autoSave();
      refreshStatusNow();
      toast(`已添加 ${slotTimeText(slot)} 的外卖提醒`);
    });
    $("cb-add").addEventListener("click", () => void addCustomBreak());
    for (const t of ["short", "long"]) {
      mountBreakModeDrop(t);
      $(`${t}-add`).addEventListener("click", async () => {
        const cfg = breakCfg(t);
        if (!cfg) return;
        const slot = newSlot();
        cfg.slots.push(slot);
        renderBreakList(t);
        await autoSave();
        refreshStatusNow();
        toast(`已添加 ${slotTimeText(slot)} 的${t === "short" ? "短" : "长"}休息`);
      });
      for (const id of [`${t}-enabled`, `${t}-interval`]) {
        $(id).addEventListener("change", () => updateBreakSummary(t));
      }
      $(`${t}-preview`).addEventListener("click", async () => {
        autoSave();
        await previewBreak(t);
      });
    }
    $("tl-card").classList.toggle("open", tlExpanded);
    $("tl-toggle").addEventListener("click", () => {
      tlExpanded = !tlExpanded;
      localStorage.setItem("wr-tl-expanded", tlExpanded ? "1" : "0");
      renderTimeline(LAST_STATE);
    });
    $("tk-preview").addEventListener("click", async () => {
      autoSave();
      await previewTakeout();
    });
    $("tk-enabled").addEventListener("change", () => updateTakeoutSummary());
    const batchModal = $("batch-modal");
    $("msg-batch-cancel").addEventListener("click", () => batchModal.classList.add("hidden"));
    batchModal.addEventListener("click", (e) => {
      if (e.target === batchModal) batchModal.classList.add("hidden");
    });
    $("msg-batch-ok").addEventListener("click", () => {
      const lines = $("msg-batch-text").value.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
      if (lines.length === 0) {
        batchModal.classList.add("hidden");
        return;
      }
      const cfg = msgCfgOf(msgBatchTarget);
      if (!cfg) {
        batchModal.classList.add("hidden");
        return;
      }
      const beforeLen = cfg.items.length;
      const newIds = [];
      for (const text of lines) {
        const id = nextMsgId(cfg.items);
        cfg.items.push({ id, text, enabled: true });
        newIds.push(id);
      }
      // 新建第 1 条时自动勾选：新增前列表为空（即这是首条），让第一条处于选中态。
      // 固定模式靠 fixedId 选中单条；随机/顺序模式下新增条目默认 enabled:true 已勾选。
      if (beforeLen === 0 && newIds.length > 0 && cfg.order === "fixed") {
        cfg.fixedId = newIds[0];
      }
      batchModal.classList.add("hidden");
      renderMsgList(msgBatchTarget);
      autoSave();
      toast(`已添加 ${lines.length} 条提醒内容`);
    });
    $("cf-cancel").addEventListener("click", () => closeConfirm(false));
    $("cf-ok").addEventListener("click", () => closeConfirm(true));
    $("confirm-modal").addEventListener("click", (e) => {
      if (e.target === $("confirm-modal")) closeConfirm(false);
    });
    document.addEventListener("keydown", (e) => {
      if ($("confirm-modal").classList.contains("hidden")) return;
      if (e.key === "Escape") {
        e.preventDefault();
        closeConfirm(false);
        return;
      }
      if (e.key === "Enter") {
        if (document.activeElement instanceof HTMLButtonElement) return;
        e.preventDefault();
        closeConfirm(true);
      }
    });
    $("pm-cancel").addEventListener("click", () => closePrompt(null));
    $("pm-ok").addEventListener("click", () => submitPrompt());
    $("prompt-modal").addEventListener("click", (e) => {
      if (e.target === $("prompt-modal")) closePrompt(null);
    });
    $("pm-input").addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        submitPrompt();
      }
    });
    // 边改字边清掉错误提示：重名被拒后，用户一动手就看到旧的红字消失
    $("pm-input").addEventListener("input", () => setPromptError(""));
    document.addEventListener("keydown", (e) => {
      if ($("prompt-modal").classList.contains("hidden")) return;
      if (e.key === "Escape") {
        e.preventDefault();
        closePrompt(null);
      }
    });
    $("ap-preview-short").addEventListener("click", async () => {
      autoSave();
      await previewBreak("short");
    });
    $("ap-opacity").addEventListener("input", () => {
      $("ap-opacity-val").textContent = parseFloat($("ap-opacity").value).toFixed(2);
    });
    $("snd-volume").addEventListener("input", () => {
      $("snd-volume-val").textContent = `${Math.round(parseFloat($("snd-volume").value) * 100)}%`;
    });
    $("qa-break").addEventListener("click", () => void window.api.action("breakNow"));
    $("qa-pause").addEventListener("click", () => void window.api.action("pauseToggle"));
    $("qa-postpone").addEventListener("click", () => void window.api.action("postpone"));
    $("qa-skip").addEventListener("click", () => void window.api.action("skip"));
    $("qa-reset").addEventListener("click", () => void window.api.action("resetCycle"));
    $("wb-min").addEventListener("click", () => void window.api.action("winMin"));
    $("wb-max").addEventListener("click", () => void window.api.action("winMaxToggle"));
    $("wb-close").addEventListener("click", () => void window.api.action("winClose"));
    document.querySelector("#titlebar .tb-drag")?.addEventListener("dblclick", () => {
      void window.api.action("winMaxToggle");
    });
    applyTitlebarMaximized(await window.api.windowMaximized());
    window.api.onWindowMaximized((max) => applyTitlebarMaximized(max));
    $("d-clearstats").addEventListener("click", () => {
      void (async () => {
        const ok = await confirmDialog({
          title: "清空统计数据",
          desc: "确定清空全部使用统计数据？",
          detail: "将删除本地 stats.json 里的全部工作/休息/外卖统计，且无法恢复。",
          okText: "清空"
        });
        if (!ok) return;
        await window.api.clearStats();
        toast("统计数据已清空");
        void renderStats();
      })();
    });
    // 关于页的两个纯文本弹窗（许可条款 / 开源鸣谢）走同一套逻辑：整份文本内嵌在 exe 里
    // （license.go / notices.go），首次点开才取 —— 不必占用首帧，之后复用同一份字符串
    // （同一版本内不会变）。抽成一个函数而不是复制两遍：两边只有 id 与取数函数不同，
    // 复制出来的第二份最容易在“焦点落哪儿”“Esc 走哪条路”这类细节上悄悄跑偏。
    function mountTextModal(cfg) {
      let loaded = false;
      const body = $(cfg.body);
      const open = async () => {
        $(cfg.modal).classList.remove("hidden");
        // 焦点移进弹窗（可访问性），但落在正文容器上而不是“关闭”按钮：
        // 脚本聚焦会触发 :focus-visible，落在蓝色主按钮上会给它描一圈显眼的环，
        // 看起来像“关闭”是必须点的默认动作。正文容器只需键盘可滚，视觉上最安静。
        body.focus();
        if (loaded) return;
        const text = await cfg.load();
        // textContent 而不是 innerHTML：正文是纯文本，且清单里本来就有 <adrg@epistack.com>
        // 这种尖括号，走 innerHTML 会被当成标签吞掉。
        body.textContent = text || cfg.fallback;
        body.scrollTop = 0;
        loaded = !!text;
      };
      const close = () => $(cfg.modal).classList.add("hidden");
      $(cfg.trigger).addEventListener("click", () => void open());
      $(cfg.close).addEventListener("click", close);
      $(cfg.modal).addEventListener("click", (e) => {
        if (e.target === $(cfg.modal)) close();
      });
      document.addEventListener("keydown", (e) => {
        if ($(cfg.modal).classList.contains("hidden")) return;
        if (e.key === "Escape") {
          e.preventDefault();
          close();
        }
      });
      $(cfg.export).addEventListener("click", () => {
        void (async () => {
          const r = await cfg.exportFn();
          if (!r || r.canceled) return;
          if (r.error) return toast(r.error);
          toast("已导出到 " + r.path);
        })();
      });
    }
    mountTextModal({
      modal: "license-modal", body: "lic-body", trigger: "d-license",
      close: "lic-close", export: "lic-export",
      fallback: "许可条款读取失败。",
      load: () => window.api.getProjectLicense(),
      exportFn: () => window.api.exportProjectLicense(),
    });
    mountTextModal({
      modal: "notices-modal", body: "nt-body", trigger: "d-notices",
      close: "nt-close", export: "nt-export",
      fallback: "许可清单读取失败。",
      load: () => window.api.getThirdPartyNotices(),
      exportFn: () => window.api.exportThirdPartyNotices(),
    });
    $("d-quit").addEventListener("click", () => void window.api.quit());
    // 版本号规范化：确保恰好一个 v 前缀（version.json 已是 v1.2.3，不要再拼一个 v）
    // 版本号规范成“恰好一个 v 前缀”；非数字开头的（源码占位的 dev）原样显示，
    // 不拼成 vdev
    const normVer = (v) => {
      if (v == null) return "";
      const s = String(v).trim();
      const core = s.replace(/^v+/, "");
      if (!/^\d/.test(core)) return s;
      return "v" + core;
    };
    const info = await window.api.appInfo();
    $("d-version-val").textContent = normVer(info.version);
    // 发布时间：打包时由 ldflags 注入 RFC3339；源码直跑没有这个时间戳，显示“本地构建”
    const rawBuild = info.buildTime || "";
    $("d-build-time").textContent = rawBuild ? rawBuild.replace("T", " ").slice(0, 16) : "本地构建";
    // 行尾运行环境：Go 版本是“这份二进制由谁编译”的指纹，Wails 版本决定运行时行为差异，
    // 用户报 bug 时这两项能省一轮来回。缺失时补“—”而不是留空 —— 否则会渲染成
    // “Go  · Wails ”这种断层，看着像页面加载坏了。
    $("d-runtime").textContent =
      "Go " + (info.goVersion || "—") + " · Wails " + (info.wailsVersion || "v3");

    const updateGo = $("d-update-go");
    const updatePill = $("d-update-pill");
    const updatePillText = $("d-update-pill-text");
    let updateModalShownFor = ""; // 防止同一版本重复自动弹窗
    let checking = false; // 手动“检查更新”进行中：这段时间保持加载态，忽略事件推送
    const setPill = (kind, text) => {
      updatePill.className = `pill ${kind}`;
      updatePillText.textContent = text;
    };
    const fillChanges = (el, changes) => {
      el.innerHTML = "";
      const list = Array.isArray(changes) ? changes : [];
      for (const c of list) {
        const div = document.createElement("div");
        div.className = "up-item";
        div.textContent = c;
        el.appendChild(div);
      }
    };
    const showUpdateModal = (st) => {
      $("up-version").textContent = normVer(st.latest);
      $("up-date").textContent = st.date || "—";
      fillChanges($("up-changes"), st.changes);
      $("update-modal").classList.remove("hidden");
    };
    const showUpdatedModal = (info) => {
      if (!info) return;
      $("upd-version").textContent = normVer(info.version);
      $("upd-date").textContent = info.date || "—";
      fillChanges($("upd-changes"), info.changes);
      // 更新成功：隐藏“立即更新”按钮并复位状态条（直到下次发现新版本）
      updateGo.classList.add("hidden");
      setPill("ok", "已是最新版本");
      updateModalShownFor = "";
      $("updated-modal").classList.remove("hidden");
    };
    // autoModal=false 时只显示“有更新”状态（按钮+状态条），不自动弹窗——
    // 用于“未勾选自动检查更新”时打开设置页，避免把上次残留的更新提示又弹出来。
    // force=true 只给“手动点检查更新”用：用户主动问了，只要发现有新版本就必须弹窗给出
    // 结果，不受“同一版本本次已弹过”“该版本被忽略过”两条限制（那两条只为自动检查去重）。
    const renderUpdate = (st, autoModal = true, force = false) => {
      if (st.status === "update-available") {
        updateGo.classList.remove("hidden");
        setPill("warn", `发现新版本 ${normVer(st.latest)}`);
        // 被用户忽略的版本：仍显示“立即更新”按钮，但不再自动弹窗
        // （直到发现比被忽略版本更大的版本号才恢复弹窗）
        if (force || (!st.ignored && autoModal && updateModalShownFor !== st.latest)) {
          updateModalShownFor = st.latest;
          showUpdateModal(st);
        }
      } else if (st.status === "up-to-date") {
        updateGo.classList.add("hidden");
        setPill("ok", "已是最新版本");
      } else if (st.status === "error") {
        updateGo.classList.add("hidden");
        setPill("err", "更新检查失败");
      } else {
        updateGo.classList.add("hidden");
        setPill("ok", "已是最新版本");
      }
    };
    // 设置页打开时的初始渲染：仅当勾选“自动检查更新”时才自动弹窗，
    // 否则只显示“有更新”状态（按钮+状态条），不弹提示——避免把上次
    // 残留的“发现新版本”又弹出来打扰用户。
    renderUpdate(await window.api.updateStatus(), S.checkUpdateOnStart);
    window.api.onUpdateStatus((st) => {
      // 手动检查进行中：保持“正在检查更新…”，结果以 checkUpdate 的返回值为准，
      // 避免后台推送的旧结果把加载态盖掉。
      if (checking) return;
      renderUpdate(st);
    });
    window.api.onSettingsHidden(() => exitAllMsgEditing());
    // “检查更新”：点下去必须立刻有反馈（状态条变“正在检查更新…”+ 按钮置灰改文案），
    // 否则联网要等几秒甚至十几秒，用户以为没点上会反复点。
    const checkBtn = $("d-checkupdate");
    const checkBtnLabel = checkBtn.textContent;
    const setChecking = (on) => {
      checking = on;
      checkBtn.disabled = on;
      checkBtn.textContent = on ? "检查中…" : checkBtnLabel;
      if (on) setPill("loading", "正在检查更新…");
    };
    checkBtn.addEventListener("click", () => {
      if (checking) return; // 双保险：disabled 之外的重复点击一律吞掉
      setChecking(true);
      void (async () => {
        try {
          const st = await window.api.checkUpdate();
          // 手动检查：发现新版本必须弹窗（force），不受忽略/去重限制；
          // 其余两种结果没有弹窗，用 toast 明确告知。
          renderUpdate(st, true, true);
          if (st.status === "up-to-date") toast("已是最新版本");
          else if (st.status !== "update-available")
            toast(`检查更新失败（${st.message ?? "更新源不可达"}）`);
        } catch (e) {
          // 绑定调用本身出错（非 status=error）：也要收敛成一个明确的结果，别停在“检查中”
          setPill("err", "更新检查失败");
          toast(`检查更新失败（${e?.message ?? "未知错误"}）`);
        } finally {
          setChecking(false);
        }
      })();
    });
    // “立即更新”（在“检查更新”左侧）：直接执行更新
    // 暂停期间不拦这一下：暂停约束的是软件自己发起的自动检查，用户在机器前亲手点的
    // “立即更新”照常执行 —— 重启是他自己的选择，不是软件越过他做的决定。
    updateGo.addEventListener("click", () => {
      void (async () => {
        toast("正在更新，软件将重启…");
        try {
          const r = await window.api.startUpdate();
          if (r && r.status === "error") {
            toast(`更新未能启动：${r.message ?? "未知错误"}`);
          }
        } catch (e) {
          // 进程即将退出时绑定调用会被中断，属正常现象
        }
      })();
    });
    // 更新确认弹窗按钮
    // 下次一定：仅关闭弹窗，什么都不做（该版本下次仍会提示）
    $("up-later").addEventListener("click", () => $("update-modal").classList.add("hidden"));
    // 忽略本版本：记录忽略并关闭弹窗；之后同一版本不再自动弹窗，直到更大版本号出现
    $("up-ignore").addEventListener("click", () => {
      void window.api.updateStatus().then((st) => {
        if (st && st.latest) void window.api.ignoreVersion(st.latest);
        $("update-modal").classList.add("hidden");
      });
    });
    // 立即更新：执行更新
    $("up-now").addEventListener("click", () => {
      $("update-modal").classList.add("hidden");
      void (async () => {
        toast("正在更新，软件将重启…");
        try {
          const r = await window.api.startUpdate();
          if (r && r.status === "error") {
            toast(`更新未能启动：${r.message ?? "未知错误"}`);
          }
        } catch (e) {
          // 进程即将退出时绑定调用会被中断，属正常现象
        }
      })();
    });
    $("upd-ok").addEventListener("click", () => $("updated-modal").classList.add("hidden"));
    // 软件刚完成更新：拉取摘要并展示；若更新前设置窗口可见，则恢复显示并切回原页面
    void window.api.pendingUpdate().then((info) => {
      if (!info) return;
      showUpdatedModal(info);
      if (info.settingsVisible) {
        goTab(info.activeTab || "general");
        void window.api.showSettingsWindow();
      }
    });
    $("d-reset").addEventListener("click", () => {
      void (async () => {
        const ok = await confirmDialog({
          title: "恢复默认设置",
          desc: "确定把全部设置恢复为出厂默认？",
          detail: "当前配置会先备份成 settings.backup.json（每次覆盖）。督休专员目录与已导入的音乐文件不受影响；开机自启动会一并关闭。",
          okText: "恢复默认"
        });
        if (!ok) return;
        const r = await window.api.resetSettings();
        if (!r.ok) {
          toast(`恢复失败：${r.message ?? "未知错误"}`);
          return;
        }
        toast("已恢复默认设置（原配置已备份）");
        S = await window.api.getSettings();
        fillForm();
        renderAllMsgLists();
        renderTakeoutList();
        await refreshSupervisorPreview();
      })();
    });
    for (const a of document.querySelectorAll("#tab-about .ext-link")) {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        const url = a.dataset.url;
        if (url) void window.api.openExternal(url);
      });
    }
    await refreshSupervisorPreview();
    // 统计文件损坏重建过就弹窗说明（Go 侧启动时处置，这里取一次告知结果）
    bindStatsNotice();
    bindSoundScanModal();
    // 统计弹窗优先；它没弹（或没这道坎）才轮到提示音目录的体检结果
    void (async () => {
      if (!(await showStatsFileNotice())) await showSoundScanNotice();
    })();
    window.api.onState((raw) => renderStatus(raw));
    // 统计入账（工作/休息/离开/休息次数/外卖动作）后 Go 侧会推今天的快照：
    // “今天”卡立刻跟着变，不用等切页、切窗口或手动刷新。
    window.api.onStatsChanged((t) => renderComposition(t));
    window.api.onAutostart((v) => {
      $("autostart").checked = v;
    });
    renderStatus(await window.api.getState());
    // 连续运行跨天：页面不会自己重画，这里每秒比一次本地日期，变了就重拉统计页。
    // 默认范围已改成“现算”（见 rcGetRange），所以只要重画，日 / 周 / 月 / 季 / 年的
    // 最后一个桶就自然推到新的一天；“今天”卡本身靠 stats:changed 每秒推送，不受影响。
    let lastDayStr = rcDayStr(new Date());
    setInterval(async () => {
      renderStatus(await window.api.getState());
      updateTakeoutSummary();
      for (const t of allBreakIds()) updateBreakSummary(t);
      const dayStr = rcDayStr(new Date());
      if (dayStr !== lastDayStr) {
        lastDayStr = dayStr;
        if ($("tab-stats")?.classList.contains("active")) void renderStats();
      }
    }, 1e3);
  }
  init();
})();
