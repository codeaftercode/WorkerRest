package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 统计层对拍：把场景矩阵喂给 Go 侧的统计函数，导出成与 scripts/gen_stats_ref.mjs
// 完全相同的 JSON 形状，交由 scripts/verify_stats_parity.py 逐字段比对。
//
// 为什么比对放在 Python 而不在这里：那边的 walk() 已经会分辨「Go 缺键 / 多键 / 值不同」，
// 报告比 t.Errorf 拼出来的更好读，也能和配置层 / 计划层对拍共用同一套报告格式。
//
// 单独跑 `go test` 时本用例自动跳过（没有环境变量），所以它不会给日常回归添麻烦。

type statsRefDoc struct {
	TZOffsetMin int `json:"tzOffsetMin"`
	Inputs      struct {
		StatsFileText string `json:"statsFileText"`
		Now           int64  `json:"now"`
	} `json:"inputs"`
	SplitByDay []struct {
		Name   string `json:"name"`
		From   int64  `json:"from"`
		To     int64  `json:"to"`
		Expect []any  `json:"expect"`
	} `json:"splitByDay"`
	PeriodRange []struct {
		Name   string         `json:"name"`
		Unit   string         `json:"unit"`
		Offset int            `json:"offset"`
		Now    int64          `json:"now"`
		Expect map[string]any `json:"expect"`
	} `json:"periodRange"`
}

// TestDumpStatsForParity 在 WR_STATS_REF / WR_STATS_DUMP 都设了的时候导出 Go 侧结果
func TestDumpStatsForParity(t *testing.T) {
	refPath := os.Getenv("WR_STATS_REF")
	dumpPath := os.Getenv("WR_STATS_DUMP")
	if refPath == "" || dumpPath == "" {
		t.Skip("未设置 WR_STATS_REF / WR_STATS_DUMP，跳过统计层对拍导出")
	}
	raw, err := os.ReadFile(refPath)
	if err != nil {
		t.Fatalf("读参考失败：%v", err)
	}
	var doc statsRefDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析参考失败：%v", err)
	}
	// 时区必须一致：两边都用本地时区解释「几点几分」，跨时区跑这份参考没有意义
	if _, off := time.Now().Zone(); off/60 != doc.TZOffsetMin {
		t.Skipf("时区不一致（本地 %d 分钟 / 参考 %d 分钟），跳过", off/60, doc.TZOffsetMin)
	}

	// ① 跨零点切段
	splitOut := make([]map[string]any, 0, len(doc.SplitByDay))
	for _, sc := range doc.SplitByDay {
		list := make([]map[string]any, 0, 4)
		for _, s := range splitByDay(sc.From, sc.To) {
			list = append(list, map[string]any{"date": s.date, "ms": s.ms})
		}
		splitOut = append(splitOut, map[string]any{"name": sc.Name, "expect": list})
	}

	// ② 周期区间
	periodOut := make([]map[string]any, 0, len(doc.PeriodRange))
	for _, sc := range doc.PeriodRange {
		r := periodRangeOf(sc.Unit, sc.Offset, time.UnixMilli(sc.Now).Local())
		periodOut = append(periodOut, map[string]any{
			"name": sc.Name,
			"expect": map[string]any{
				"key": r.Key, "label": r.Label, "range": r.Range, "start": r.Start, "end": r.End,
			},
		})
	}

	// ③ 天记录聚合：把参考侧的 stats.json 原文写到临时文件再 Load，两边输入完全一致
	dir := t.TempDir()
	p := filepath.Join(dir, "stats.json")
	if err := os.WriteFile(p, []byte(doc.Inputs.StatsFileText), 0o644); err != nil {
		t.Fatalf("写临时统计文件失败：%v", err)
	}
	st := NewStatsStore(func() string { return p })
	st.Load()
	st.nowFn = func() int64 { return doc.Inputs.Now }
	agg := map[string]any{
		"today":       st.Today(),
		"periods":     st.Periods(),
		"daysAll":     st.DaysInRange("", ""),
		"daysWindow":  st.DaysInRange("2026-10-06", "2026-10-08"),
		"customNames": st.CustomNames(),
	}

	out := map[string]any{
		"splitByDay":  splitOut,
		"periodRange": periodOut,
		"aggregate":   agg,
	}
	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("导出失败：%v", err)
	}
	if err := os.WriteFile(dumpPath, append(buf, '\n'), 0o644); err != nil {
		t.Fatalf("写导出文件失败：%v", err)
	}
	t.Logf("已导出 %d 个切段场景 / %d 个周期场景", len(splitOut), len(periodOut))
}
