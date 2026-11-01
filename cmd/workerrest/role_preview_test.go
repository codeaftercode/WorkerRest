package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"workerrest/internal/role"
)

// fakeWebP 造一段「魔数合法」的 WebP 字节。
//
// 只验落盘与格式嗅探（role.SniffImageExt 本来就只认魔数，不负责解码 —— 真正的像素由渲染层
// 生产），所以这里不需要一张真能解码的图；长度给够 32 字节以满足 role.DecodeImageDataURL
// 的下限。
func fakeWebP(marker byte, n int) []byte {
	if n < 32 {
		n = 32
	}
	b := make([]byte, n)
	copy(b[0:4], "RIFF")
	copy(b[8:12], "WEBP")
	copy(b[12:16], "VP8L")
	b[20] = marker // 让不同调用造出的内容可区分
	for i := 32; i < n; i++ {
		b[i] = marker
	}
	return b
}

func webpDataURL(b []byte) string {
	return "data:image/webp;base64," + base64.StdEncoding.EncodeToString(b)
}

// 磁贴缩略图缺失时，thumb 必须回退到立绘原图 —— 否则磁贴就是一片空白。
//
// 这是「preview 从必需品降级为可选优化」的核心保证：用户删掉 preview.*、
// 或把专员目录手工拷进来，界面都不能开天窗。
func TestScanRolesThumbFallsBackToFigure(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "只有立绘", map[string]any{
		"name": "只有立绘", "enabled": true, "image": "figure.webp",
	}, map[string]string{"figure.webp": "only-figure"})

	r := findRole(t, role.ScanRoles(root), "只有立绘")
	if r.Preview != "" {
		t.Fatalf("没有 preview.* 时 Preview 应为空，实际 %q", r.Preview)
	}
	want := role.ToDataURL(filepath.Join(root, "只有立绘", "figure.webp"))
	if r.Thumb != want || want == "" {
		t.Fatalf("thumb 应回退到立绘原图\n got=%q\nwant=%q", r.Thumb, want)
	}
}

// 有 preview.webp 时优先用它（这才是「磁贴省解码内存」的收益所在）。
func TestScanRolesThumbPrefersPreview(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "有预览", map[string]any{
		"name": "有预览", "enabled": true, "image": "figure.webp", "preview": "preview.webp",
	}, map[string]string{"figure.webp": "big-figure", "preview.webp": "small-preview"})

	r := findRole(t, role.ScanRoles(root), "有预览")
	if r.Thumb != role.ToDataURL(filepath.Join(root, "有预览", "preview.webp")) {
		t.Fatalf("有预览图时 thumb 应取预览图，实际 %q", r.Thumb)
	}
	if r.Preview != "preview.webp" {
		t.Fatalf("Preview 应为 preview.webp，实际 %q", r.Preview)
	}
}

// SaveRolePreview：写盘 + 回写 meta.json + 清掉旧扩展名的预览。
func TestSaveRolePreviewWritesAndUpdatesMeta(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "督休专员", map[string]any{
		"name": "督休专员", "enabled": true, "image": "figure.png", "preview": "preview.png",
	}, map[string]string{"figure.png": "fig", "preview.png": "stale-preview"})

	blob := fakeWebP(0x41, 200)
	if !role.SaveRolePreview(root, "督休专员", webpDataURL(blob)) {
		t.Fatal("保存预览图应成功")
	}
	dir := filepath.Join(root, "督休专员")
	got, err := os.ReadFile(filepath.Join(dir, "preview.webp"))
	if err != nil {
		t.Fatalf("preview.webp 应已写入：%v", err)
	}
	if string(got) != string(blob) {
		t.Fatalf("写入内容不一致（%d vs %d 字节）", len(got), len(blob))
	}
	if role.IsRegularFile(filepath.Join(dir, "preview.png")) {
		t.Fatal("旧扩展名的预览图应被清掉")
	}
	cfg, ok := role.ReadRoleFile(root, "督休专员")
	if !ok || cfg.Preview != "preview.webp" {
		t.Fatalf("meta.json 的 preview 应改为 preview.webp，实际 %q", cfg.Preview)
	}
	// 磁贴随之用上新预览图
	r := findRole(t, role.ScanRoles(root), "督休专员")
	if r.Thumb != role.ToDataURL(filepath.Join(dir, "preview.webp")) {
		t.Fatal("扫描结果的 thumb 应指向新的 preview.webp")
	}
}

// SaveRolePreview 必须拒绝非图片数据与超大输入，且**不留下任何文件**。
func TestSaveRolePreviewRejectsBadData(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "督休专员", map[string]any{
		"name": "督休专员", "enabled": true, "image": "figure.png",
	}, map[string]string{"figure.png": "fig"})
	dir := filepath.Join(root, "督休专员")

	cases := []struct {
		name    string
		dataURL string
	}{
		{"空串", ""},
		{"不是 base64", "data:image/webp;base64,!!!!"},
		{"非 base64 的 data URL", "data:image/svg+xml,<svg/>"},
		{"魔数不对", "data:image/webp;base64," + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 128)))},
		{"过小", webpDataURL([]byte("RIFF0000WEBP"))},
	}
	for _, c := range cases {
		if role.SaveRolePreview(root, "督休专员", c.dataURL) {
			t.Fatalf("%s：应被拒绝", c.name)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("被拒绝后目录里只该有 meta.json + figure.png，实际 %d 项", len(entries))
	}
	if role.SaveRolePreview(root, "不存在的督休专员", webpDataURL(fakeWebP(2, 200))) {
		t.Fatal("专员不存在时应返回 false")
	}
}

// 渲染层万一送来 PNG（内核不支持 WebP 编码时会退成 PNG），也要能落盘。
func TestSaveRolePreviewAcceptsPNG(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "督休专员", map[string]any{
		"name": "督休专员", "enabled": true, "image": "figure.png",
	}, map[string]string{"figure.png": "fig"})
	b := make([]byte, 64)
	copy(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	if !role.SaveRolePreview(root, "督休专员", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(b)) {
		t.Fatal("PNG 数据应被接受")
	}
	cfg, _ := role.ReadRoleFile(root, "督休专员")
	if cfg.Preview != "preview.png" {
		t.Fatalf("PNG 应落成 preview.png，实际 %q", cfg.Preview)
	}
}

// 换图后旧预览必须清空：留着就会显示上一张图（这正是「preview 复制原图」时代的毛病）。
func TestReplaceRoleImageDropsStalePreview(t *testing.T) {
	root := t.TempDir()
	writeRoleDir(t, root, "督休专员", map[string]any{
		"name": "督休专员", "enabled": true, "image": "figure.png", "preview": "preview.png",
	}, map[string]string{"figure.png": "old-fig", "preview.png": "old-preview"})

	src := writeImgSrc(t, t.TempDir(), "新图.webp", "new-bytes")
	if !role.ReplaceRoleImage(root, "督休专员", src) {
		t.Fatal("换图应成功")
	}
	dir := filepath.Join(root, "督休专员")
	if role.IsRegularFile(filepath.Join(dir, "preview.png")) {
		t.Fatal("旧预览图应被删除（等渲染层重新生成）")
	}
	cfg, _ := role.ReadRoleFile(root, "督休专员")
	if cfg.Image != "figure.webp" {
		t.Fatalf("image 应指向新立绘，实际 %q", cfg.Image)
	}
	if cfg.Preview != "" {
		t.Fatalf("preview 应置空，实际 %q", cfg.Preview)
	}
	// 没有预览图时，磁贴回退到新立绘
	r := findRole(t, role.ScanRoles(root), "督休专员")
	if r.Thumb != role.ToDataURL(filepath.Join(dir, "figure.webp")) {
		t.Fatal("换图后 thumb 应回退到新立绘")
	}
}
