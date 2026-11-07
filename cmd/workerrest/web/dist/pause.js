"use strict";
(() => {
  // 自动暂停期间的“已暂停多久”小窗（见 pause_notice.go）。
  // 文案与时长都由主进程算好经 URL 的 text 下发，页面不自己拼 ——
  // 措辞约束（见 pause_notice.go 文件头）只能有一处真源。
  const q = new URLSearchParams(location.search);
  const text = q.get("text");
  if (text) document.getElementById("text").textContent = text;

  document.getElementById("resume").addEventListener("click", () => {
    void window.api.pauseNoticeResume();
  });
  document.getElementById("dismiss").addEventListener("click", () => {
    void window.api.pauseNoticeDismiss();
  });
  // Esc = 只关提示、不恢复（与系统浮层一致的习惯）
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") void window.api.pauseNoticeDismiss();
  });
})();
