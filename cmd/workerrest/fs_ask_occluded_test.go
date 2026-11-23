package main

import "testing"

// fsAskOccluded 的**逻辑**测试：取中心点、取顶层祖先、与自己比对。
//
// 为什么注入而不是造两扇真窗：开发机上永远有别人的 TOPMOST 窗口压在最上面（这里是
// WorkBuddy 自己的 WebView2 窗，实测 hit 是 Chrome_RenderWidgetHostHWND），测试窗根本
// 抢不到那一层 —— 试过，稳定地红，而且红的是环境不是产品。真实 Win32 行为由
// probe_fs_ask.py 的「按钮坐标上确实是询问窗」兜：它问系统同一个问题，而且那一刻画面里
// 真的有一扇盖满整屏的窗。
//
// 这里能抓住的是真正容易写错的三处：中心点算错（取四角会落在圆角的透明区域里，
// WindowFromPoint 会穿透到下面的全屏内容）、忘记取顶层祖先（WebView2 渲染子是询问窗的
// 子窗，只比自身句柄永远不等）、以及把“问不到”当成“被盖住”。

type occlStub struct {
	oldRect func(uintptr) (wrRect, bool)
	oldHit  func(int, int) uintptr
	oldRoot func(uintptr) uintptr

	rect   wrRect
	rectOK bool
	hit    uintptr
	root   uintptr

	lastRectFor   uintptr
	askedX        int
	askedY        int
	asks          int
	rootAskedWith uintptr
}

// stubOccl 换掉三个 Win32 注入点。返回的 stub 用 defer restore() 还原。
// 这些变量没有并行测试在用（本包不用 t.Parallel），所以直接换是安全的。
func stubOccl(rect wrRect, rectOK bool, hit, root uintptr) *occlStub {
	s := &occlStub{
		oldRect: occlRectOf,
		oldHit:  occlPointHit,
		oldRoot: occlRootOf,
		rect:    rect,
		rectOK:  rectOK,
		hit:     hit,
		root:    root,
	}
	occlRectOf = func(h uintptr) (wrRect, bool) {
		s.lastRectFor = h
		return s.rect, s.rectOK
	}
	occlPointHit = func(x, y int) uintptr {
		s.askedX, s.askedY = x, y
		s.asks++
		return s.hit
	}
	occlRootOf = func(h uintptr) uintptr {
		s.rootAskedWith = h
		return s.root
	}
	return s
}

func (s *occlStub) restore() {
	occlRectOf = s.oldRect
	occlPointHit = s.oldHit
	occlRootOf = s.oldRoot
}

func TestFSAskOccludedLogic(t *testing.T) {
	const self = uintptr(0x1000)
	const child = uintptr(0x2000) // WebView2 渲染子窗
	const other = uintptr(0x3000) // 别人的顶层窗

	t.Run("窗的矩形中心才是采样点", func(t *testing.T) {
		st := stubOccl(wrRect{Left: 100, Top: 50, Right: 500, Bottom: 250}, true, child, self)
		defer st.restore()
		if fsAskOccluded(self) {
			t.Fatal("顶层祖先就是自己，不该判成被盖住")
		}
		// 中心点 = ((100+500)/2, (50+250)/2) = (300,150)
		if st.askedX != 300 || st.askedY != 150 {
			t.Fatalf("采样点 (%d,%d)，应为窗中心 (300,150)", st.askedX, st.askedY)
		}
		if st.lastRectFor != self {
			t.Fatalf("应该量询问窗自己的矩形，实际量了 %#x", st.lastRectFor)
		}
	})

	t.Run("命中别人的窗就是被盖住", func(t *testing.T) {
		st := stubOccl(wrRect{Left: 0, Top: 0, Right: 100, Bottom: 100}, true, other, other)
		defer st.restore()
		if !fsAskOccluded(self) {
			t.Fatal("中心点上是别人的窗，却没判成被盖住 —— 守卫不会抬，用户看不见也点不到")
		}
		if st.rootAskedWith != other {
			t.Fatalf("应对命中的窗口取顶层祖先，实际传了 %#x", st.rootAskedWith)
		}
	})

	t.Run("命中自己的子窗不算被盖住", func(t *testing.T) {
		// WindowFromPoint 很可能返回 WebView2 的渲染子窗，而不是宿主窗口
		st := stubOccl(wrRect{Left: 0, Top: 0, Right: 100, Bottom: 100}, true, child, self)
		defer st.restore()
		if fsAskOccluded(self) {
			t.Fatal("命中的是自家子窗（祖先就是自己），被判成被盖住了")
		}
	})

	t.Run("这点上没有窗口：保守当成没被盖住", func(t *testing.T) {
		st := stubOccl(wrRect{Left: 0, Top: 0, Right: 100, Bottom: 100}, true, 0, 0)
		defer st.restore()
		if fsAskOccluded(self) {
			t.Fatal("问不到归属时应返回 false，否则每拍都会白发一次 SetWindowPos")
		}
		if st.asks != 1 {
			t.Fatalf("应该只问一次，实际 %d 次", st.asks)
		}
	})

	t.Run("句柄量不到矩形：不判被盖住", func(t *testing.T) {
		st := stubOccl(wrRect{}, false, other, other)
		defer st.restore()
		if fsAskOccluded(self) {
			t.Fatal("量不到矩形时不该判成被盖住")
		}
		if st.asks != 0 {
			t.Fatalf("矩形无效时不该去问归属，实际问了 %d 次", st.asks)
		}
	})

	t.Run("退化的矩形（宽或高为 0）不问归属", func(t *testing.T) {
		st := stubOccl(wrRect{Left: 10, Top: 10, Right: 10, Bottom: 10}, true, other, other)
		defer st.restore()
		if fsAskOccluded(self) {
			t.Fatal("退化矩形不该判成被盖住")
		}
		if st.asks != 0 {
			t.Fatalf("退化矩形不该去问归属，实际问了 %d 次", st.asks)
		}
	})

	t.Run("句柄为 0：直接 false，连矩形都不量", func(t *testing.T) {
		st := stubOccl(wrRect{}, true, other, other)
		defer st.restore()
		if fsAskOccluded(0) {
			t.Fatal("句柄为 0 时应保守返回 false")
		}
		if st.lastRectFor != 0 {
			t.Fatal("句柄为 0 时不该去量矩形")
		}
	})
}
