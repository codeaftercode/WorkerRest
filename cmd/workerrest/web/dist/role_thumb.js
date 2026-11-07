// 立绘缩略图生成器：把“立绘原图”压成“立绘库磁贴”正好需要的尺寸。
//
// 为什么放在渲染层：软件本体是纯 Go（CGO_ENABLED=0，机器上没有 gcc），
// 引 golang.org/x/image + 缩放器会让 exe 涨 1.16 MB、还要自己写编解码；
// 而 WebView2 内核本来就带 canvas 与 WebP 编码器 —— 零依赖、零体积，
// 产出的 WebP 还比纯 Go 的 PNG 小 4~5 倍（实测 6~10 KB vs 25~53 KB）。
//
// 谁在用：
//   · settings.js —— 导入 / 换图之后生成 preview.webp（PC 端唯一入口）；
//   · scripts/rebuild_role_previews.py —— 批量重做磁盘上已有的预览图（注入本文件，
//     调同一个函数，保证“运行时生成的”与“脚本批量生成的”逐像素同源）。
//
// 尺寸口径：磁贴 <img> 是 height: 92px（settings.css 的 .role-item img），
// 按 2 倍屏（Windows 150%~200% 缩放）取样 → 92 × 2 = 184。再大就是纯浪费：
// 磁贴按 contain 显示，多余像素只增加解码内存（17 张原图解码后 114 MB，
// 184 高的缩略图只有 ~4 MB）。
//
// 裁切口径：立绘四边（尤其顶部）常有大片全透明区域，直接等比缩放会让画面在磁贴上
// 显得又小又偏下。这里先求 alpha 包围盒，裁掉空白，再向外留 PAD_RATIO 的透明边
// （“只保留一点点透明区域”），最后按高度缩到 184。
(function (global) {
  "use strict";

  var THUMB_HEIGHT = 184;
  var PAD_RATIO = 0.02; // 裁剪后保留的透明边（相对内容高度）
  var ALPHA_MIN = 8; // 判定“有内容”的 alpha 阈值（0~255）
  var QUALITY = 0.82;
  var SCAN_MAX = 512; // 求包围盒时的降采样上限边长（全尺寸扫描没必要）

  function makeCanvas(w, h) {
    var c = document.createElement("canvas");
    c.width = Math.max(1, Math.round(w));
    c.height = Math.max(1, Math.round(h));
    return c;
  }

  function ctx2d(c) {
    var g = c.getContext("2d", { willReadFrequently: false });
    g.imageSmoothingEnabled = true;
    g.imageSmoothingQuality = "high";
    return g;
  }

  // contentBox 求“非透明内容”的包围盒（原图坐标系）。整张全透明返回 null。
  //
  // 先降到 SCAN_MAX 以内再扫：全尺寸 1536×2304 要读 14 MB 像素，而降采样后
  // 只差 1~2 个原图像素 —— 外面还要留 2% 的透明边，这点误差看不出来。
  function contentBox(img) {
    var nw = img.naturalWidth || img.width;
    var nh = img.naturalHeight || img.height;
    var s = Math.min(1, SCAN_MAX / Math.max(nw, nh));
    var sw = Math.max(1, Math.round(nw * s));
    var sh = Math.max(1, Math.round(nh * s));
    var c = makeCanvas(sw, sh);
    var g = ctx2d(c);
    g.drawImage(img, 0, 0, sw, sh);
    var data;
    try {
      data = g.getImageData(0, 0, sw, sh).data;
    } catch (e) {
      return null; // 画布被污染（本地文件不应发生）：交给调用方退回整图
    }
    var minX = sw, minY = sh, maxX = -1, maxY = -1;
    for (var y = 0; y < sh; y++) {
      var row = y * sw * 4;
      for (var x = 0; x < sw; x++) {
        if (data[row + x * 4 + 3] > ALPHA_MIN) {
          if (x < minX) minX = x;
          if (x > maxX) maxX = x;
          if (y < minY) minY = y;
          if (y > maxY) maxY = y;
        }
      }
    }
    if (maxX < 0) return null;
    // 向外补 1 个采样像素，避免把抗锯齿的半透明边缘切掉；再映射回原图坐标。
    var pad = 1 / s;
    var x0 = Math.max(0, minX / s - pad);
    var y0 = Math.max(0, minY / s - pad);
    var x1 = Math.min(nw, (maxX + 1) / s + pad);
    var y1 = Math.min(nh, (maxY + 1) / s + pad);
    return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
  }

  // scaleRegion 把原图的 [sx,sy,sw,sh] 逐级减半缩到 dw×dh。
  // 逐级减半是必须的：1024 → 147 一步到位会有明显锯齿（浏览器对超大比例只做粗略采样）。
  function scaleRegion(img, sx, sy, sw, sh, dw, dh) {
    var sizes = [];
    var w = sw, h = sh;
    while (w > dw * 2 && h > dh * 2) {
      w = Math.round(w / 2);
      h = Math.round(h / 2);
      sizes.push([w, h]);
    }
    sizes.push([dw, dh]);
    var src = img;
    for (var i = 0; i < sizes.length; i++) {
      var c = makeCanvas(sizes[i][0], sizes[i][1]);
      var g = ctx2d(c);
      if (i === 0) g.drawImage(src, sx, sy, sw, sh, 0, 0, c.width, c.height);
      else g.drawImage(src, 0, 0, src.width, src.height, 0, 0, c.width, c.height);
      src = c;
    }
    return src;
  }

  // buildFromImage 生成缩略图，返回 data URL（失败返回空串）。
  function buildFromImage(img) {
    var nw = img.naturalWidth || img.width;
    var nh = img.naturalHeight || img.height;
    if (!nw || !nh) return "";
    var box = contentBox(img) || { x: 0, y: 0, w: nw, h: nh };
    // 裁完再向外留一点点透明边（上下左右同为 PAD_RATIO × 内容高度）
    var pad = Math.max(1, Math.round(box.h * PAD_RATIO));
    var sx = Math.max(0, box.x - pad);
    var sy = Math.max(0, box.y - pad);
    var sw = Math.min(nw - sx, box.w + pad * 2);
    var sh = Math.min(nh - sy, box.h + pad * 2);
    if (sw < 1 || sh < 1) return "";
    var dh = THUMB_HEIGHT;
    var dw = Math.max(1, Math.round((sw * dh) / sh));
    var c = scaleRegion(img, sx, sy, sw, sh, dw, dh);
    var url = c.toDataURL("image/webp", QUALITY);
    // 内核不支持 WebP 编码时 toDataURL 会退成 PNG（甚至 "data:,"）—— 也要能用
    if (!url || url === "data:,") return "";
    return url;
  }

  // buildFromDataURL 同上，入参是 data URL（立绘由 Go 侧读盘转好送过来）。
  function buildFromDataURL(url) {
    return new Promise(function (resolve) {
      if (!url) {
        resolve("");
        return;
      }
      var img = new Image();
      img.onload = function () {
        var out = "";
        try {
          out = buildFromImage(img);
        } catch (e) {
          out = "";
        }
        resolve(out);
      };
      img.onerror = function () {
        resolve("");
      };
      img.src = url;
    });
  }

  global.WRRoleThumb = {
    HEIGHT: THUMB_HEIGHT,
    buildFromImage: buildFromImage,
    buildFromDataURL: buildFromDataURL,
  };
})(window);
