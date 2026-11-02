// 批量重做督休专员的磁贴预览图（supervisors/<专员>/preview.webp）。
//
// 调的是**运行时同一个函数**：把 web/dist/role_thumb.js 注入页面，再由
// window.WRRoleThumb.buildFromDataURL 生成 —— 保证「脚本批量生成的」与「用户在设置页
// 导入/换图时生成的」逐像素同源，不会出现两套裁切/尺寸口径。
//
// 用法：
//   node scripts/rebuild_role_previews.js            # 全量重做
//   node scripts/rebuild_role_previews.js --check    # 只报告，不写盘
//   node scripts/rebuild_role_previews.js 秦始皇 画家  # 指定专员
//
// 注意：会覆盖 preview.*（旧扩展名的文件一并删除），并修正 meta.json 里的 preview 字段；
// 立绘（figure.*）与其它字段一律不动。

const fs = require("fs");
const path = require("path");
const { chromium } = require("playwright");

const ROOT = path.resolve(__dirname, "..");
const SUP = path.join(ROOT, "assets", "supervisors");
const THUMB_JS = path.join(ROOT, "cmd", "workerrest", "web", "dist", "role_thumb.js");
const MIME = {
  ".webp": "image/webp",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
};

function findAsset(dir, base) {
  for (const ext of Object.keys(MIME)) {
    const p = path.join(dir, base + ext);
    if (fs.existsSync(p)) return p;
  }
  return null;
}

function roleDirs(filter) {
  const out = [];
  for (const name of fs.readdirSync(SUP)) {
    const dir = path.join(SUP, name);
    if (!fs.statSync(dir).isDirectory()) continue;
    if (!fs.existsSync(path.join(dir, "meta.json"))) continue;
    if (filter.length && !filter.includes(name)) continue;
    out.push({ name, dir });
  }
  return out;
}

(async () => {
  const args = process.argv.slice(2);
  const check = args.includes("--check");
  const filter = args.filter((a) => !a.startsWith("--"));
  if (!fs.existsSync(THUMB_JS)) {
    console.error(`找不到 ${THUMB_JS}`);
    process.exit(2);
  }

  const targets = roleDirs(filter);
  if (!targets.length) {
    console.error("没有找到可处理的督休专员目录");
    process.exit(2);
  }

  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage();
  await page.goto("about:blank");
  await page.addScriptTag({ path: THUMB_JS });
  const height = await page.evaluate(() => window.WRRoleThumb.HEIGHT);
  console.log(`磁贴缩略图目标高度 ${height}px${check ? "（--check：只报告不写盘）" : ""}`);

  let okCount = 0;
  let oldTotal = 0;
  let newTotal = 0;
  for (const { name, dir } of targets) {
    const fig = findAsset(dir, "figure");
    if (!fig) {
      console.log(`  skip  ${name}：没有 figure.*`);
      continue;
    }
    const ext = path.extname(fig).toLowerCase();
    const raw = fs.readFileSync(fig);
    const dataURL = `data:${MIME[ext]};base64,` + raw.toString("base64");
    const out = await page.evaluate((u) => window.WRRoleThumb.buildFromDataURL(u), dataURL);
    if (!out || !out.startsWith("data:image/")) {
      console.log(`  FAIL  ${name}：生成失败`);
      continue;
    }
    const b64 = out.slice(out.indexOf(",") + 1);
    const buf = Buffer.from(b64, "base64");
    const oldPrev = findAsset(dir, "preview");
    const oldSize = oldPrev ? fs.statSync(oldPrev).size : 0;
    oldTotal += oldSize;
    newTotal += buf.length;
    if (!check) {
      const target = path.join(dir, "preview.webp");
      fs.writeFileSync(target, buf);
      // 清掉旧扩展名的预览（preview.png / preview.jpg …），刚写的那份跳过
      for (const f of fs.readdirSync(dir)) {
        if (!f.startsWith("preview")) continue;
        const p = path.join(dir, f);
        if (p !== target) fs.unlinkSync(p);
      }
      // meta.json 里的 preview 字段要么不存在（内置专员，靠 roleAsset 按主名兜底），
      // 要么指向真实文件名 —— 不能让它继续指着已被删掉的 preview.png。
      const mp = path.join(dir, "meta.json");
      const meta = JSON.parse(fs.readFileSync(mp, "utf8"));
      if ("preview" in meta && meta.preview !== "preview.webp") {
        meta.preview = "preview.webp";
        fs.writeFileSync(mp, JSON.stringify(meta, null, 2) + "\n", { encoding: "utf8" });
      }
    }
    okCount++;
    console.log(
      `  ok    ${name.padEnd(6)} ${String(Math.round(oldSize / 1024)).padStart(4)}KB -> ` +
        `${String(Math.round(buf.length / 1024)).padStart(3)}KB  (${oldSize ? Math.round((buf.length / oldSize) * 100) : 0}%)`
    );
  }

  await browser.close();
  console.log(
    `\n${check ? "可处理" : "已重做"} ${okCount}/${targets.length} 个；` +
      `合计 ${Math.round(oldTotal / 1024)}KB -> ${Math.round(newTotal / 1024)}KB`
  );
})();
