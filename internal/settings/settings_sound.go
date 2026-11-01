package settings

import (
	"strings"

	"workerrest/internal/jsonx"
	"workerrest/internal/model"
)

// ---------- 声音配置视图 ----------

// SoundCfg 取出声音配置。缺字段按「与默认配置同口径」兜底：启用、音量 0.6、单曲循环。
func (s *Settings) SoundCfg() model.SoundCfg {
	return soundCfgOf(s.Map("sound"))
}

func soundCfgOf(m map[string]any) model.SoundCfg {
	cfg := model.SoundCfg{Enabled: true, Volume: 0.6, Mode: "single", Music: []model.SoundMusicItem{}}
	if m == nil {
		return cfg
	}
	cfg.Enabled = mapFlag(m, "enabled", true)
	if v, ok := jsonx.ToNumber(m["volume"]); ok {
		cfg.Volume = clamp01(v)
	}
	if mode := mapStr(m, "mode", "single"); mode == "random" || mode == "sequence" {
		cfg.Mode = mode
	}
	cfg.SingleID = mapStr(m, "singleId", "")
	for _, it := range jsonx.AnySlice(m["music"]) {
		mi, ok := it.(map[string]any)
		if !ok {
			continue
		}
		file := mapStr(mi, "file", "")
		if strings.TrimSpace(file) == "" {
			continue
		}
		cfg.Music = append(cfg.Music, model.SoundMusicItem{
			ID:      mapStr(mi, "id", ""),
			File:    file,
			Name:    mapStr(mi, "name", ""),
			Enabled: mi["enabled"] != false,
		})
	}
	return cfg
}

// SoundCfgOf 从一份 sound 对象读出类型化声音配置（跨包导出，供测试复用）。
func SoundCfgOf(m map[string]any) model.SoundCfg { return soundCfgOf(m) }

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
