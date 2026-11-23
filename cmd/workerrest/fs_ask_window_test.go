package main

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 窗口名与 URL 的 ?win= 必须一致：CloseWindow / ensureWindowsOnScreen / 退出清理都按名字
// 找窗口（与 pause_notice_test.go 的同名用例同一个理由）。
func TestFSAskWindowNameMatchesURL(t *testing.T) {
	u, err := url.Parse(fsAskURL("vlc.exe", "影片 & 字幕", true, 12))
	if err != nil {
		t.Fatalf("URL 解析失败：%v", err)
	}
	if got := u.Path; got != "/fsask.html" {
		t.Fatalf("页面路径不对：%q", got)
	}
	q := u.Query()
	if got := q.Get("win"); got != fsAskWindowName {
		t.Fatalf("URL 的 win=%q 与窗口名 %q 不一致", got, fsAskWindowName)
	}
	// 名字里带 & 与中文：必须整体编码，否则会在 & 处被切成两段
	if got := q.Get("name"); got != "影片 & 字幕" {
		t.Fatalf("应用名没经 query 正确下发：%q", got)
	}
	if got := q.Get("exe"); got != "vlc.exe" {
		t.Fatalf("exe 没经 query 正确下发：%q", got)
	}
	if got := q.Get("canMark"); got != "1" {
		t.Fatalf("canMark 应为 1，实际 %q", got)
	}
	if got := q.Get("secs"); got != "12" {
		t.Fatalf("secs 应为 12，实际 %q", got)
	}
	// 不可标记时要显式下发 0（页面按 "0" 判断，空串会被当成可标记）
	if got := urlQuery(t, fsAskURL("ApplicationFrameHost.exe", "照片", false, 12), "canMark"); got != "0" {
		t.Fatalf("canMark 应为 0，实际 %q", got)
	}
}

func urlQuery(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("URL 解析失败：%v", err)
	}
	return u.Query().Get(key)
}

// 页面的按钮与 Go 侧的答复键必须一一对应。
//
// 这条是防“点了没反应”的关键：Go 的 FullScreenAskAnswer 对未知 answer 只打一行日志、
// 返回 nil（迟到的点击不该报错），所以页面写错一个 data-answer 不会有任何报错，
// 只表现为“这个按钮按了没用”。两个集合必须严格相等，多一个少一个都算漂移。
func TestFSAskPageAnswersMatchGoConstants(t *testing.T) {
	html := readDist(t, "fsask.html")
	re := regexp.MustCompile(`data-answer="([^"]+)"`)
	got := make([]string, 0, 4)
	for _, m := range re.FindAllStringSubmatch(html, -1) {
		got = append(got, m[1])
	}
	sort.Strings(got)

	want := []string{fsAnswerNever, fsAnswerPause, fsAnswerSkip, fsAnswerToday}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("页面 data-answer 与 Go 侧答复键不一致：页面 %v / Go %v", got, want)
	}
}

// 页面必须装上 shim（否则 window.api 不存在，四个按钮全哑）且**不能内联脚本**
// （CSP default-src 'self' 会静默拦掉；同一坑 2026-10-25 踩过）。
func TestFSAskPageHasNoInlineScript(t *testing.T) {
	html := readDist(t, "fsask.html")
	for _, need := range []string{"./nozoom.js", "./shim.js", "./fsask.js", "./fsask.css"} {
		if !strings.Contains(html, need) {
			t.Errorf("fsask.html 少了 %s", need)
		}
	}
	if inlineScriptTags(html) != 0 {
		t.Error("fsask.html 里有内联脚本：CSP 会静默拦掉")
	}
	// 答复只能走这一条绑定；名字变了 Go 侧就收不到点击
	if !strings.Contains(readDist(t, "fsask.js"), "window.api.fullScreenAskAnswer(") {
		t.Error("fsask.js 没调 window.api.fullScreenAskAnswer")
	}
}

// inlineScriptTags 数一数有几个不带 src 的 <script>。
// 不用正则：Go 的 regexp 是 RE2，没有 lookahead，`<script(?![^>]*src=)` 这种写法在这里
// 编译不过（panic 会让整包测试挂掉，而不是失败一条）。
func inlineScriptTags(html string) int {
	lower := strings.ToLower(html)
	n := 0
	for i := 0; ; {
		j := strings.Index(lower[i:], "<script")
		if j < 0 {
			return n
		}
		start := i + j
		end := strings.Index(lower[start:], ">")
		if end < 0 {
			return n
		}
		if !strings.Contains(lower[start:start+end], "src=") {
			n++
		}
		i = start + end
	}
}

func readDist(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("web", "dist", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", p, err)
	}
	return string(b)
}

// wailsDefaultExStyle 复刻 Wails 建窗时构造的默认扩展样式
// （webview_window_windows.go 的 run()）。
//
// 刻意照抄数值而不是引用上游常量：这份对照表的意义就是"与上游逐位比对"。上游哪天
// 加了新的默认位，这里不会自动跟上 —— 而这正是我要的，它会红，逼我回去重新核对
// 那份"整份替换"的清单。整份替换的语义下，漏一位 = 静默关掉一个行为。
func wailsDefaultExStyle(o application.WebviewWindowOptions) uint32 {
	const (
		exControlParent    = 0x00010000
		exNoRedirectionBmp = 0x00200000
		exTopmost          = 0x00000008
		exToolWindow       = 0x00000080
		exAppWindow        = 0x00040000
	)
	s := uint32(exControlParent)
	// 询问窗不设 IgnoreMouseEvents（点击必须落在窗上），所以走 NOREDIRECTIONBITMAP 那支
	if o.BackgroundType != application.BackgroundTypeSolid {
		s |= exNoRedirectionBmp
	}
	if o.AlwaysOnTop {
		s |= exTopmost
	}
	if o.Windows.HiddenOnTaskbar {
		s |= exToolWindow
	} else {
		s |= exAppWindow
	}
	return s
}

// 窗口声明逐项钉住。
//
// 这些错的后果全在真机上极难复现（要正好赶上无边框游戏全屏那一刻），而且 Wails 升级
// 会悄悄改默认值。ExStyle 那条最要紧：它是整份替换，漏一位就是静默少一个行为。
func TestFSAskWindowOptions(t *testing.T) {
	o := fsAskWindowOptions(1476, 816, fsAskWidth, fsAskHeight, "vlc.exe", "影片 & 字幕", true, false)

	// —— 几何：原样透传，否则窗会跑到别处 ——
	if o.X != 1476 || o.Y != 816 || o.Width != fsAskWidth || o.Height != fsAskHeight {
		t.Errorf("几何没透传：(%d,%d) %d×%d", o.X, o.Y, o.Width, o.Height)
	}
	// InitialPosition 为零值时 Wails 会忽略 X/Y 改成居中（踩过）
	if o.InitialPosition != application.WindowXY {
		t.Error("InitialPosition 必须是 WindowXY，否则 X/Y 被无视、窗会居中")
	}
	if !o.Frameless || !o.AlwaysOnTop || !o.DisableResize {
		t.Errorf("窗属性不对：Frameless=%v AlwaysOnTop=%v DisableResize=%v",
			o.Frameless, o.AlwaysOnTop, o.DisableResize)
	}
	if o.Name != fsAskWindowName {
		t.Errorf("窗口名 %q 与 fsAskWindowName 不一致（清理按名字找窗）", o.Name)
	}

	// —— 透明背景：切到 Solid 就变成一块实底，盖住全屏画面 ——
	if o.BackgroundType != application.BackgroundTypeTranslucent {
		t.Errorf("背景类型 %v，应为 Translucent", o.BackgroundType)
	}
	if a := o.BackgroundColour.Alpha; a != 0 {
		t.Errorf("背景色 alpha=%d，应为 0（完全透明）", a)
	}

	// —— 共享屏幕时隐藏：跟随配置，不是写死 ——
	if o.ContentProtectionEnabled {
		t.Error("hideOnShare=false 时不该开启内容保护")
	}
	if !fsAskWindowOptions(0, 0, 420, 200, "a.exe", "a", true, true).ContentProtectionEnabled {
		t.Error("hideOnShare=true 时询问窗必须被内容保护罩住（窗里带着应用名）")
	}

	// —— URL 参数 ——
	for key, want := range map[string]string{
		"win": fsAskWindowName, "exe": "vlc.exe", "name": "影片 & 字幕",
		"canMark": "1", "secs": strconv.Itoa(fsAskTimeoutMs / 1000),
	} {
		if got := urlQuery(t, o.URL, key); got != want {
			t.Errorf("URL 的 %s=%q，期望 %q", key, got, want)
		}
	}

	// —— ExStyle：整份替换，必须覆盖 Wails 的全部默认位 ——
	def := wailsDefaultExStyle(o)
	mine := uint32(o.Windows.ExStyle)
	if missing := def & ^mine; missing != 0 {
		t.Fatalf("ExStyle 漏掉 Wails 默认位 0x%X —— 整份替换会把这个行为静默关掉"+
			"（0x200000 = NOREDIRECTIONBITMAP：漏了透明合成失效，"+
			" 圆角外那圈变成实底）", missing)
	}
	for name, bit := range map[string]uint32{
		"WS_EX_TOPMOST（压在其它窗之上）":                      wsExTopmost,
		"WS_EX_TOOLWINDOW（不占任务栏 / Alt-Tab）":           wsExToolWindow,
		"WS_EX_CONTROLPARENT（Tab 在窗内控件间走）":            wsExControlParent,
		"WS_EX_NOACTIVATE（点击不抢前台，全屏不被踢出）":             wsExNoActivate,
		"WS_EX_NOREDIRECTIONBITMAP（Translucent 透明合成）": wsExNoRedirectionBmp,
	} {
		if mine&bit == 0 {
			t.Errorf("ExStyle 少了 %s（0x%X）", name, bit)
		}
	}
	// 多出来的位只允许是 NOACTIVATE：这是询问窗唯一刻意偏离 Wails 默认的地方
	if extra := mine & ^def; extra != wsExNoActivate {
		t.Errorf("ExStyle 多出 0x%X，只允许 NOACTIVATE(0x%X)", extra, uint32(wsExNoActivate))
	}
	// HiddenOnTaskbar 与 TOOLWINDOW 必须同时为真：只设其中一个会留下进任务栏的组合
	if !o.Windows.HiddenOnTaskbar {
		t.Error("HiddenOnTaskbar 必须为 true")
	}
	if mine&0x00040000 != 0 {
		t.Error("ExStyle 含 WS_EX_APPWINDOW：询问窗会出现在任务栏上")
	}
	// 装饰必须关掉，否则无边框窗顶上会多一条标题栏
	if !o.Windows.DisableFramelessWindowDecorations {
		t.Error("DisableFramelessWindowDecorations 必须为 true")
	}
	// 页面要知道自己在哪个窗（shim 按它分流）
	if !strings.Contains(o.JS, `__WR_WIN = "fsask"`) {
		t.Errorf("注入脚本不对：%q", o.JS)
	}
}

// 12 秒这个值本身也要钉住：页面倒计时、Go 兜底结案、URL 的 secs 三处都从它派生，
// 改一处忘一处就是“页面数到 0 了但 Go 还没结案”。
func TestFSAskTimeoutIsTwelveSeconds(t *testing.T) {
	if fsAskTimeoutMs != 12_000 {
		t.Fatalf("fsAskTimeoutMs=%d，约定是 12 秒", fsAskTimeoutMs)
	}
	o := fsAskWindowOptions(0, 0, 420, 200, "a.exe", "a", true, false)
	if got := urlQuery(t, o.URL, "secs"); got != "12" {
		t.Fatalf("下发给页面的 secs=%q，应为 12", got)
	}
}

// 真机 12 秒没法进常规回归，但"调用之后不会立刻结案"必须能验：
// 常量一旦被改成 0（或误传 Duration 单位），这条会立刻红。
func TestFSAskArmTimeoutDoesNotConcludeImmediately(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.fsAskMu.Lock()
	a.fsAsk.pending = true
	a.fsAsk.seq = 11
	a.fsAskMu.Unlock()

	a.fsAskArmTimeout(11)
	time.Sleep(50 * time.Millisecond)

	if s.HasPauseReason(pauseReasonFullScrBusy) {
		t.Fatal("刚挂上定时器就结案了：单位算错（毫秒当成纳秒）或常量成了 0")
	}
	a.fsAskMu.Lock()
	pending := a.fsAsk.pending
	a.fsAskMu.Unlock()
	if !pending {
		t.Fatal("12 秒还没到，pending 不该被清掉")
	}
}

// fsAskBounds 右下角定位。夹紧那两行很容易把窗推到屏幕外 —— 左侧/上方有副屏时 X/Y 是负数，
// 拿 0 当“最小”就会把窗丢到主屏左上角，用户点不到「暂停」。
func TestFSAskBounds(t *testing.T) {
	cases := []struct {
		name  string
		area  application.Rect
		wantX int
		wantY int
	}{
		{
			name: "主屏 1920×1040 工作区：贴右下角、留 24",
			area: application.Rect{X: 0, Y: 0, Width: 1920, Height: 1040},
			// 1920-420-24 / 1040-200-24
			wantX: 1476, wantY: 816,
		},
		{
			name: "左侧副屏（X 为负）：落在那一块屏的右下角，而不是主屏",
			area: application.Rect{X: -1920, Y: 0, Width: 1920, Height: 1040},
			// -1920+1920-420-24 / 1040-200-24
			wantX: -444, wantY: 816,
		},
		{
			name:  "工作区比窗还窄：夹到该区域左上，不跑到外面",
			area:  application.Rect{X: 0, Y: 0, Width: 300, Height: 150},
			wantX: 0, wantY: 0,
		},
		{
			name:  "负坐标且比窗窄：夹到该屏左上（-1920），不是 0",
			area:  application.Rect{X: -1920, Y: 0, Width: 300, Height: 150},
			wantX: -1920, wantY: 0,
		},
	}
	for _, c := range cases {
		x, y, w, h := fsAskBounds(c.area)
		if w != fsAskWidth || h != fsAskHeight {
			t.Errorf("%s：尺寸 %d×%d，应为 %d×%d", c.name, w, h, fsAskWidth, fsAskHeight)
		}
		if x != c.wantX || y != c.wantY {
			t.Errorf("%s：位置 (%d,%d)，期望 (%d,%d)", c.name, x, y, c.wantX, c.wantY)
		}
		// 无论怎么夹，窗的左上角都不该跑到工作区外面
		if x < c.area.X || y < c.area.Y {
			t.Errorf("%s：夹紧后 (%d,%d) 仍超出工作区原点 (%d,%d)", c.name, x, y, c.area.X, c.area.Y)
		}
	}
}

// 超时到点：必须真的落一条「非独占全屏」暂停。
//
// 真机等 12 秒没法进常规回归，而这个回调是“不答就静默放过”的唯一防线 —— 把它拆成可注入
// 时长的版本，就能在毫秒级把三条分支都钉住。
func TestFSAskTimeoutAfterConcludesPause(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.fsAskMu.Lock()
	a.fsAsk.pending = true
	a.fsAsk.seq = 7
	a.fsAskMu.Unlock()

	a.fsAskArmTimeoutAfter(7, 5*time.Millisecond)
	waitFor(t, "超时结案要落暂停", 2*time.Second, func() bool {
		return s.HasPauseReason(pauseReasonFullScrBusy)
	})

	a.fsAskMu.Lock()
	pending, logged := a.fsAsk.pending, a.fsAsk.logged
	a.fsAskMu.Unlock()
	if pending {
		t.Error("结案后 pending 必须清掉，否则下一拍还会当成“正在问”")
	}
	if !logged {
		t.Error("结案要置 logged：段结束时靠它判断“这一段的进入日志打过没有”")
	}
}

// 迟到的定时器不许结掉**下一段**询问。seq 单调递增就是为了这个。
func TestFSAskTimeoutAfterIgnoresStaleSeq(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.fsAskMu.Lock()
	a.fsAsk.pending = true
	a.fsAsk.seq = 8 // 当前是第 8 段
	a.fsAskMu.Unlock()

	a.fsAskArmTimeoutAfter(7, 5*time.Millisecond) // 第 7 段遗留的定时器
	time.Sleep(80 * time.Millisecond)

	if s.HasPauseReason(pauseReasonFullScrBusy) {
		t.Fatal("上一段的定时器把这一段结掉了：用户还在看询问窗，却已经被暂停")
	}
	a.fsAskMu.Lock()
	pending := a.fsAsk.pending
	a.fsAskMu.Unlock()
	if !pending {
		t.Fatal("当前这段询问不该被上一段的定时器结案")
	}
}

// 用户已经答过（pending=false）：同一段的定时器到点也必须什么都不做。
func TestFSAskTimeoutAfterIgnoresAnswered(t *testing.T) {
	a, s := newWatchApp(t, shortCountdown)
	a.fsAskMu.Lock()
	a.fsAsk.pending = false // 已答复
	a.fsAsk.seq = 9
	a.fsAskMu.Unlock()

	a.fsAskArmTimeoutAfter(9, 5*time.Millisecond)
	time.Sleep(80 * time.Millisecond)

	if s.HasPauseReason(pauseReasonFullScrBusy) {
		t.Fatal("用户答了“本次不暂停”，超时却又补了一条暂停")
	}
	a.fsAskMu.Lock()
	logged := a.fsAsk.logged
	a.fsAskMu.Unlock()
	if logged {
		t.Error("不该被结案的回调置位 logged —— 那会让真正的答复路径少打一次日志")
	}
}
