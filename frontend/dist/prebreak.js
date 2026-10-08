"use strict";
(() => {
  // renderer/prebreak.ts
  var api = window.api;
  var secondsEl = document.getElementById("pb-seconds");
  var postEl = document.getElementById("pb-post");
  var titleEl = document.getElementById("pb-title");
  var tipsEl = document.getElementById("pb-tips");
  var pauseBtn = document.getElementById("pb-pause");
  var nowBtn = document.getElementById("pb-now");
  var postponeBtn = document.getElementById("pb-postpone");
  var skipBtn = document.getElementById("pb-skip");
  document.getElementById("pb-now").addEventListener("click", () => void api.preBreakAction("breakNow"));
  document.getElementById("pb-postpone").addEventListener("click", () => void api.preBreakAction("postpone"));
  document.getElementById("pb-skip").addEventListener("click", () => void api.preBreakAction("skip"));
  document.getElementById("pb-pause").addEventListener("click", () => void api.preBreakAction("pause"));
  var BTN_LABELS = {
    builtin: { now: "\u7ACB\u5373\u4F11\u606F", postpone: "\u63A8\u8FDF\u4F11\u606F", skip: "\u8DF3\u8FC7\u4F11\u606F" },
    custom: { now: "\u7ACB\u5373\u63D0\u9192", postpone: "\u63A8\u8FDF\u63D0\u9192", skip: "\u8DF3\u8FC7\u63D0\u9192" }
  };
  function applyVariant(custom, tail) {
    postEl.textContent = tail;
    tipsEl.classList.toggle("hidden", custom);
    pauseBtn.classList.toggle("hidden", custom);
    const label = custom ? BTN_LABELS.custom : BTN_LABELS.builtin;
    nowBtn.textContent = label.now;
    postponeBtn.textContent = label.postpone;
    skipBtn.textContent = label.skip;
  }
  {
    const q = new URLSearchParams(location.search);
    applyVariant(q.get("custom") === "1", q.get("tail") ?? " \u79D2\u8FDB\u5165\u4F11\u606F");
  }
  async function poll() {
    try {
      const st = await api.preBreakState();
      if (st.paused) {
        titleEl.textContent = "\u5DF2\u6682\u505C";
        secondsEl.textContent = "--";
        postEl.textContent = "";
      } else {
        titleEl.textContent = st.title || "\u60A8\u5DF2\u8FDE\u7EED\u5DE5\u4F5C\u8F83\u957F\u65F6\u95F4\uFF0C\u5EFA\u8BAE\u60A8\u77ED\u6682\u4F11\u606F";
        secondsEl.textContent = String(Math.max(0, Math.ceil(st.remainMs / 1e3)));
        applyVariant(st.custom, st.countdownTail);
      }
    } catch {
    }
  }
  poll();
  setInterval(poll, 500);
})();
