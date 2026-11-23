"use strict";
/**
 * 非独占全屏的询问小窗（见 fs_ask_window.go / fs_ask_bindings.go）。
 *
 * 职责只有两件：把主进程下发的应用名摆上去、把四个答复回传。
 * 倒计时只是**视觉**上的 —— 真正的结案由 Go 侧的 fsAskArmTimeout 兜底（WebView2 万一
 * 没把页面加载起来，用户也必须看到“按暂停处理”的结果）。
 *
 * 回复一律走 window.api.fullScreenAskAnswer(answer)：Go 侧对迟到的点击（窗口已因超时
 * 或段结束关掉）静默返回，所以这里不需要做“已经答过”的本地去重。
 */
(() => {
  const q = new URLSearchParams(location.search);

  // 应用名：主进程已按 exe → 标题 → “未知应用” 的优先级算好（ForegroundInfo.DisplayName），
  // 页面拿到的就是最终展示文本，不再做任何加工。
  const name = (q.get("name") || "").trim();
  if (name) document.getElementById("who").textContent = name;

  // canMark=0：前台是 ApplicationFrameHost / rundll32 这类宿主进程，没有可记录的进程名。
  // 两个“记住这个应用”的选项必须灰掉，并且给出原因（灰按钮不带 title 等于没解释）。
  if (q.get("canMark") === "0") {
    const host = "这个应用没有可记录的进程名（系统宿主进程），不能加入名单";
    for (const el of document.querySelectorAll('[data-answer="today"], [data-answer="never"]')) {
      el.disabled = true;
      el.title = host;
    }
  }

  // 倒计时。秒数由 Go 侧下发（fsAskTimeoutMs/1000），与真正的结案定时器同一个源，
  // 页面不再自己写死 12。
  const leftEl = document.getElementById("left");
  const barEl = document.getElementById("bar");
  let left = Number.parseInt(q.get("secs") || "", 10);
  if (!Number.isFinite(left) || left < 0) left = 0;
  const total = left;

  function render() {
    if (left > 0) {
      leftEl.textContent = left + " 秒后自动暂停";
    } else {
      leftEl.textContent = "时间到，按暂停处理";
    }
    if (total > 0) barEl.style.width = Math.max(0, (left / total) * 100).toFixed(1) + "%";
  }

  if (total > 0) {
    render();
    const timer = setInterval(() => {
      left -= 1;
      render();
      if (left <= 0) clearInterval(timer);
    }, 1000);
  } else {
    // secs=0（理论上不会发生）：不显示倒计时条，标题行也不该留一句假话
    document.getElementById("bar-wrap").hidden = true;
    leftEl.hidden = true;
  }

  // 单击委派：四个按钮共用一条路径，谁也不会漏接。
  document.addEventListener("click", (e) => {
    const btn = e.target instanceof Element ? e.target.closest("[data-answer]") : null;
    if (!btn || btn.disabled) return;
    void window.api.fullScreenAskAnswer(btn.dataset.answer);
  });

  // Esc = 本次不暂停（与“关闭提示”同一种习惯）。这扇窗带 WS_EX_NOACTIVATE，
  // 正常情况下键盘事件根本到不了这里 —— 留着是为了它能被读屏/键盘用户操作时行为一致。
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") void window.api.fullScreenAskAnswer("skip");
  });
})();
