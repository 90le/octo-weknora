---
name: executive-pptx
version: 1.3.1
description: 资深行业分析师与顶级咨询（McKinsey/Bain/艾瑞级）水准的原生可编辑 PowerPoint (.pptx) 极速生产工作流。内置 SCQA+MECE 先立纲、search_knowledge(asset_type="image") 精准配图与 OCR 复现双轨策略、参数化咨询图表库（完整签名已内嵌，无需探测环境或读源码）以及 qa_report.json + 紧凑 JPEG 双重质检（10–18 轮高效交付）。
---

# Executive PPTX v1.3.1 — 资深行业分析师级汇报极速生产流

当用户需要生成 `.pptx` / 行业分析报告 / 战略汇报 / 产品方案演示文稿时，严格遵循本 Skill 的 **「4 阶段高效工作流（目标 10–18 轮、3–6 分钟内交付）」**，同时兼容**纯文本模型**与**视觉模型**。

---

## 一、铁律与提速红线（必须严守，杜绝 60 轮长拖）

1. **禁止环境探测与冒烟测试**：沙箱已预装并验证全部离线运行环境（Node + Playwright Chromium + ECharts + PptxGenJS + LibreOffice + poppler + Noto Sans/Serif CJK SC）。**严禁跑 `node -v`/`ls /opt/pro-pptx-runtime`/`grep html2pptx_pro.mjs`、严禁逐段读取 `consulting_charts.py`、严禁制作 `smoke.html` 冒烟文件**——本说明已给出全部准确函数签名，直接开写 `gen_deck.py`！
2. **原样传递图片句柄（不要改写 `res://`）**：`search_knowledge(asset_type="image")` 返回的图片句柄（如 `res://0012` 或 `resource://...`）直接原样传给 `read_file(path="...")`，**不要把 `res://0012` 手动改写成 `resource://0012`**。
3. **编译与质检次数硬上限（最多 2 轮）**：
   - 初版编译 `--qa /workspace/tmp/qa`（第 1 轮）→ 若有硬问题或明显视觉瑕疵，集中一次性改完 `gen_deck.py` 重编 `--qa`（第 2 轮）→ **立即交付**！
   - **严禁手动再调用 `soffice --headless` / `pdftoppm` 逐页切图反复刷 v2/v3/v4/v5/v6**！`html2pptx_pro.mjs --qa /workspace/tmp/qa` 已一次性产出：`qa_report.json`、`qa_sheet_1.jpg`、`qa_sheet_2.jpg` 及逐页原图 `/workspace/tmp/qa/qa_sheet_pages/page-NN.png`（如需放大看某 1–2 页直接 `read_file` 对应 `page-NN.png` 即可）。
4. **一次性写完 `gen_deck.py`**：使用 1 次 `write_sandbox_file` 写入结构清晰的 `/workspace/gen_deck.py`（字符串尽量用三引号 `'''...'''` 或单双引号分明，避免语法错误返工）。

---

## 二、阶段 0 & 1：精准检索 + 双轨视觉素材 + SCQA 立纲（2–4 轮完成）

1. **并行检索文本与视觉/OCR 资产**：
   - 文本证据：`search_knowledge(query="...", mode="hybrid")` 获取核心观点、数据口径、案例与文档句柄 `dN`。
   - **配图 & OCR 图一步直击**：并行调用 **`search_knowledge(query="...", asset_type="image", limit=10)`**，直接召回知识库已索引的 `image_caption` + 完整 `image_ocr` + 文件大小（已自动过滤 `<15KB` 小 Logo）。
2. **双轨素材处理**：
   - **A 轨 —— 高清产品界面截图 / 实景图直贴**：对选中的 2–4 张高清图并行调用 `read_file(path="<原样句柄>", detail="brief")`：自动落盘至 `/workspace/input/kb/<handle>.<ext>` 并返回像素宽高 `dimensions=WxH (aspect=..., landscape/portrait)`。嵌入时严格按原图纵横比配画框 + `object-fit:contain`（必要时可写 3 行 PIL 仅裁去浏览器顶栏）。
   - **B 轨 —— 旧数据图 / 模糊图表 OCR 复现重绘**：从 `<image_ocr>` 提取准确数值与维度，用下方 `consulting_charts.py` 按本套 PPT 统一配色**高清重绘为咨询级图表**并标注来源，绝不直接贴糊图。
3. **SCQA + MECE 先立纲**：
   - 在 `gen_deck.py` 顶部确立 SCQA 主线（背景-冲突-问题-核心结论）；
   - **每页主标题必须是带观点的结论句（Action Title）**，严禁使用“市场现状”“产品介绍”等空洞名词标题；
   - 严禁编造未经知识库证实的精确数据；推算/示意须在页脚显式标注口径。

---

## 三、阶段 2：内置咨询组件库速查（零依赖，自由配色，不锁母版）

在 `gen_deck.py` 顶部直接导入：
```python
import os, sys
sys.path.insert(0, os.path.join(os.environ["YEWEN_SKILL_DIR"], "scripts"))
import consulting_charts as cc
```

自由定义你的主题配色字典 `THEME`（全部键可选，缺省自动补齐）：
```python
THEME = {
    "primary": "#0F2744", "accent": "#0EA5E9", "secondary": "#1D4ED8",
    "highlight": "#F59E0B", "positive": "#10B981", "negative": "#EF4444",
    "muted": "#94A3B8", "ink": "#0F172A", "subtext": "#475569",
    "surface": "#FFFFFF", "canvas": "#F5F7FB", "border": "#E2E8F0",
}
```

**完整函数签名（直接调用，无需读源码）**：
- `cc.echarts_div(option, width="100%", height="360px", extra_style="") -> str`
- `cc.echarts_cagr_combo(title, years, bars, line_values, bar_name, line_name, cagr_text="", theme=THEME, bar_unit="", line_unit="%") -> dict`（市场规模柱 + 增速线 + CAGR 徽章）
- `cc.echarts_waterfall(title, labels, values, theme=THEME, unit="", totals_indices=None) -> dict`（McKinsey 瀑布/价值桥，`totals_indices` 默认首尾为总计柱）
- `cc.echarts_quadrant_bubble(title, items, x_label, y_label, quadrants={"tr":"领导者","tl":"挑战者","br":"潜力者","bl":"细分玩家"}, theme=THEME, x_mid=50, y_mid=50) -> dict`（`items=[{"name":..., "x":0-100, "y":0-100, "size":18-52, "highlight":bool}]`）
- `cc.echarts_ranked_hbar(title, items, theme=THEME, unit="") -> dict`（`items=[{"name":..., "value":float, "highlight":bool}]`）
- `cc.svg_value_chain(layers, theme=THEME, width=1170, height=400) -> str`（`layers=[{"title":..., "tag":..., "nodes":[...], "accent":可选}]`）
- `cc.html_harvey_scorecard(headers, rows, theme=THEME) -> str`（`headers=["维度","方案A","方案B","依据"]`, `rows=[{"dim":..., "scores":[4,2], "note":...}]`, 分值 `0..4`）
- 原生 PowerPoint 可改 Excel 图表：`<div style="width:660px;height:360px;" data-pptx-chart='{"type":"bar","title":"...","data":[{"name":"...","labels":[...],"values":[...]}]}'></div>`
- 离线矢量图标：`<i data-icon="trending-up" style="width:20px;height:20px;color:#0EA5E9;"></i>`

### 美学与版式红线
- 画布 `<section class="slide">` 固定 `width:1280px; height:720px; overflow:hidden; padding:40px 52px 26px;`；浅灰质感底色（`#F5F7FB`）+ 纯白悬浮圆角卡 + 顶/左 4px 色条；单页正文精炼（≤ 280 字），留白 ≥ 25%。
- 全套原生 `<table>` **最多 ≤ 2–3 页**；能力对标优先 `cc.html_harvey_scorecard`，流程/生态优先 `cc.svg_value_chain`；严禁连续两页版式雷同。
- 12+ 页必备节奏：深色渐变封面 + 1–2 页章节过渡页 + 大数字 KPI 英雄卡 + 至少 3 类不同咨询图表/架构图 + 知识库高清截图画框 + 演讲者备注 `<aside class="notes">...</aside>`。

---

## 四、阶段 3：一键编译、自适应自检与干净交付（1–2 轮完成）

1. **一键编译 + 硬质检 + 紧凑拼图**：
   ```bash
   node "$YEWEN_SKILL_DIR/scripts/html2pptx_pro.mjs" /workspace/deck.html /workspace/output/<简洁清晰文件名>.pptx --qa /workspace/tmp/qa
   ```
2. **自适应核验（最多 1 轮定点修补）**：
   - **所有模型必做**：读取 `/workspace/tmp/qa/qa_report.json`，核对 `overall_pass == true`、`table_slide_count <= 3` 及各页 `warnings`。
   - **视觉模型增强**：并行 `read_file` 查看 `qa_sheet_1.jpg` / `qa_sheet_2.jpg`（`detail="brief"` 即可）；个别页需放大看则直接读 `/workspace/tmp/qa/qa_sheet_pages/page-NN.png`，不要重复跑 `soffice`。
   - **纯文本模型降级**：不支持看图时以 `qa_report.json` 全部通过为准直接交付。
3. **干净交付**：成品仅放在 `/workspace/output/<简洁清晰文件名>.pptx`（不含草稿/QA 文件）；最终回复直接用简洁中文输出汇报导读（SCQA 核心结论、分页目录、嵌入的真实截图/OCR 数据来源与口径边界）。
