package settings

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// MergeConfigFromDir 遍历目录及其子目录下的所有 json 文件，把每个文件的顶层键按
// 「本地无则新增、本地有则不改」合并进 raw —— 更新包里配置补丁的语义。
//
// 只增不改是刻意的：更新包推下来的补丁不许覆盖用户已经调过的值，否则升级一次
// 就把个性化设置冲掉。单文件大小也设上限：配置补丁正常只有几 KB，超过 1 MiB 的
// 「配置」只会是畸形包。
//
// 解析失败的文件只记日志并跳过，不中断整个合并 —— 一个坏文件不该让更新失败。
func MergeConfigFromDir(dir string, raw map[string]any) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Size() > 1<<20 {
			log.Printf("[update] 跳过过大的配置文件 %s（%d 字节）", path, info.Size())
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var patch map[string]any
		if err := json.Unmarshal(data, &patch); err != nil {
			log.Printf("[update] 跳过无法解析的配置文件 %s: %v", path, err)
			return nil
		}
		for k, v := range patch {
			if _, exists := raw[k]; !exists {
				raw[k] = v
			}
		}
		return nil
	})
}
