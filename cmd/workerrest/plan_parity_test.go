package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
	"workerrest/internal/model"
	"workerrest/internal/plan"
	"workerrest/internal/settings"
)

// 计划层对拍：把 scenes 矩阵喂给 Go 侧的计划函数，导出成与
// scripts/gen_plan_ref.mjs 完全相同的 JSON 形状，交由
// scripts/verify_plan_parity.py 逐字段比对。
//
// 为什么比对放在 Python 而不在这里：那边的 walk() 已经会分辨“Go 缺键 / 多键 / 值不同”，
// 报告比 t.Errorf 拼出来的更好读，也能和配置层对拍共用同一套报告格式。
//
// 单独跑 `go test` 时本用例自动跳过（没有环境变量），所以它不会给日常回归添麻烦。

type planScenarioIn struct {
	Name     string         `json:"name"`
	Now      int64          `json:"now"`
	SS       int            `json:"ssL"`
	Skip     []string       `json:"skip"`
	Settings map[string]any `json:"settings"`
	Opts     struct {
		Paused           bool                    `json:"paused"`
		TakeoutFired     []string                `json:"takeoutFired"`
		TakeoutPostponed *model.TakeoutPostponed `json:"takeoutPostponed"`
		// Shifts / ExtraLines 是参考侧**实际用到的**输入（不是从配置现算的）：
		// 有些场景会覆盖 shifts（被推迟的那一跳），有些挂起线在配置里已被删除、
		// 只存在于调度器内存里 —— 不把它们带过来，两边输入就不同，对拍没有意义。
		Shifts     map[string]int64     `json:"shifts"`
		ExtraLines []model.PlannedBreak `json:"extraLines"`
	} `json:"opts"`
}

type planRefDoc struct {
	TZOffsetMin int              `json:"tzOffsetMin"`
	Base        int64            `json:"base"`
	Scenarios   []planScenarioIn `json:"scenarios"`
}

func setOfStrings(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, s := range list {
		out[s] = true
	}
	return out
}

func planAsAny(p model.PlannedBreak, ok bool) any {
	if !ok {
		return nil
	}
	return p
}

func describePlanAny(p model.PlannedBreak, ok bool) string {
	if !ok {
		return plan.DescribePlan(nil)
	}
	return plan.DescribePlan(&p)
}

// TestDumpPlanForParity 在 WR_PLAN_REF / WR_PLAN_DUMP 都设了的时候导出 Go 侧结果
func TestDumpPlanForParity(t *testing.T) {
	refPath := os.Getenv("WR_PLAN_REF")
	dumpPath := os.Getenv("WR_PLAN_DUMP")
	if refPath == "" || dumpPath == "" {
		t.Skip("未设置 WR_PLAN_REF / WR_PLAN_DUMP，跳过计划层对拍导出")
	}
	raw, err := os.ReadFile(refPath)
	if err != nil {
		t.Fatalf("读参考失败：%v", err)
	}
	var doc planRefDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析参考失败：%v", err)
	}
	// 时区必须一致：两边都用本地时区解释“几点几分”，跨时区跑这份参考没有意义
	if _, off := time.Now().Zone(); off/60 != doc.TZOffsetMin {
		t.Skipf("时区不一致（本地 %d 分钟 / 参考 %d 分钟），跳过", off/60, doc.TZOffsetMin)
	}

	type scenarioOut struct {
		Name   string         `json:"name"`
		Expect map[string]any `json:"expect"`
	}
	res := make([]scenarioOut, 0, len(doc.Scenarios))
	for _, sc := range doc.Scenarios {
		s := settings.New(sc.Settings, "")
		skip := setOfStrings(sc.Skip)

		lines := plan.SchedulerLines(s, sc.Now, sc.SS, skip, nil)
		next, ok := plan.PlanNextBreak(s, sc.Now, sc.SS, skip, nil)
		split := plan.PlanNextBreakSplit(s, sc.Now, sc.SS, skip, nil)
		fx := plan.FixedNextMap(s, sc.Now, skip)
		// 与 main.ts 的 stateForRenderer 同口径：shifts 用 fixedNext（参考侧覆盖过就用它的）、
		// 挂起线用 lines（参考侧额外塞进来的残留线也一并接上）。
		shifts := fx
		if sc.Opts.Shifts != nil {
			shifts = sc.Opts.Shifts
		}
		cdLines := lines
		if len(sc.Opts.ExtraLines) > 0 {
			cdLines = append(append([]model.PlannedBreak{}, lines...), sc.Opts.ExtraLines...)
		}
		tl := plan.TodayTimeline(s, sc.Now, plan.TimelineOptions{
			Skip:             skip,
			Shifts:           shifts,
			CountdownLines:   cdLines,
			Paused:           sc.Opts.Paused,
			TakeoutFired:     setOfStrings(sc.Opts.TakeoutFired),
			TakeoutPostponed: sc.Opts.TakeoutPostponed,
			ShortsSinceLong:  sc.SS,
		})

		res = append(res, scenarioOut{
			Name: sc.Name,
			Expect: map[string]any{
				"plan":              planAsAny(next, ok),
				"lines":             lines,
				"split":             split,
				"fixedNext":         fx,
				"plan.DescribePlan": describePlanAny(next, ok),
				"timeline":          tl,
			},
		})
	}

	b, err := json.MarshalIndent(map[string]any{"scenarios": res}, "", "  ")
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	if err := os.WriteFile(dumpPath, append(b, '\n'), 0o644); err != nil {
		t.Fatalf("写导出失败：%v", err)
	}
	t.Logf("已导出 %d 个场景到 %s", len(res), dumpPath)
}
