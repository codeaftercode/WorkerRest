package model

// 设置层读出来的类型化结构（字段名与磁盘 JSON / types.ts 逐字一致）。零 App 依赖。

// SoundMusicItem 音乐列表的一条（内置提示音与用户导入的曲子同构：内置的 file 是
// 相对文件名，导入的是绝对路径）
type SoundMusicItem struct {
	ID      string `json:"id"`
	File    string `json:"file"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// SoundCfg 声音配置的类型化视图（字段名与 types.ts 的 SoundConfig 对齐）
type SoundCfg struct {
	Enabled  bool             `json:"enabled"`
	Volume   float64          `json:"volume"`
	Mode     string           `json:"mode"` // single | random | sequence
	SingleID string           `json:"singleId"`
	Music    []SoundMusicItem `json:"music"`
}
