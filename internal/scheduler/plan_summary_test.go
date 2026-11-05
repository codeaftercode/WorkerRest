package scheduler

import (
	"strings"
	"testing"
	"time"

	"workerrest/internal/plan"
)

// 诊断面：PlanSummary 进日志、cfgByID 给状态机取配置。两者都在托盘 / 日志里被用户
// 间接看到（「下次休息在…」），错一位就是一条误导人的日志。

func TestPlanSummaryDescribesNextBreak(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	got := s.PlanSummary()
	if !strings.Contains(got, "短休息") || !strings.Contains(got, "倒计时") {
		t.Fatalf("摘要应说明类型与来源，得到 %q", got)
	}
	// 全部关掉 → 明确说「未安排休息」，不能是一句空串
	off, _ := newTestSched(t, `{"short":{"enabled":false},"long":{"enabled":false}}`)
	off.replan()
	if got := off.PlanSummary(); got != "未安排休息" {
		t.Fatalf("全部关闭时得到 %q", got)
	}
}

func TestCfgByID(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)

	if c, ok := s.cfgByID("short"); !ok || c.IntervalMs != 600_000 {
		t.Fatalf("短休息配置不符：%+v ok=%v", c, ok)
	}
	// 不存在 / 空 id：给 false 让调用方兜底，不能编出一份默认配置
	for _, id := range []string{"", "long", "nope"} {
		if id == "long" {
			continue // long 在出厂配置里是存在的，下面单独验
		}
		if _, ok := s.cfgByID(id); ok {
			t.Fatalf("cfgByID(%q) 不该命中", id)
		}
	}
	if _, ok := s.cfgByID("long"); !ok {
		t.Fatal("long 是内置类型，应该命中")
	}
}

func TestPlanSummaryAfterSkipShowsNextOccurrence(t *testing.T) {
	s, _ := newTestSched(t, shortCountdown)
	s.replan()
	first := s.StateCopy().NextAt
	// 隔开两毫秒再跳：同一毫秒内重排出来的倒计时时刻与被消费的那次相同，
	// 会被「已消费」过滤掉（下一个 tick 才恢复），那是正常的瞬态，不是本用例要验的。
	time.Sleep(2 * time.Millisecond)
	s.Skip()
	if got := s.StateCopy().NextAt; got <= first {
		t.Fatalf("跳过后摘要里的时刻应往后推，first=%d got=%d", first, got)
	}
	if got := s.PlanSummary(); got == "未安排休息" {
		t.Fatal("还有下一跳时不该说「未安排休息」")
	}
}

// 分组视图（状态卡的「下次休息提醒 / 下次其它提醒」）与全局 next 的口径差异：
// 外卖不进全局 next，但必须出现在 other 分组里。
func TestSplitKeepsTakeoutOutOfGlobalNext(t *testing.T) {
	s, host := newTestSched(t, shortCountdown)
	host.cfg = testSettings(t, `{"short":{"enabled":false},"long":{"enabled":false},"takeout":{"enabled":true,"slots":[{"id":"t","hour":11,"minute":30,"repeat":"daily","days":[],"enabled":true}]}}`)
	s.replan()

	st := s.StateCopy()
	// 全局 next 不含外卖：没有休息可排时它就是「没有下一次」，而不是拿外卖顶上
	if st.NextAt != 0 || st.NextType != "" {
		t.Fatalf("全部休息关闭后全局 next 应为空，得到 type=%q at=%d", st.NextType, st.NextAt)
	}
	if st.Rest != nil {
		t.Fatalf("内置分组不该有内容，得到 %+v", st.Rest)
	}
	// 外卖必须出现在 other 分组（状态卡要显示「下次外卖提醒还有多久」）
	if st.Other == nil || st.Other.Name != plan.TAKEOUT_NAME {
		t.Fatalf("other 分组应是外卖，得到 %+v", st.Other)
	}
	if st.Other.At == 0 {
		t.Fatal("other 分组应带时刻")
	}
}
