---
name: executive-pptx
version: 1.3.0
description: 资深行业分析师与顶级咨询（McKinsey/Bain/艾瑞级）水准的原生可编辑 PowerPoint (.pptx) 生成工作流。支持先立纲（SCQA+MECE）再出页、通过 search_knowledge(asset_type="image") 精准定位知识库高清截图与 OCR 数据图、双轨素材处理（高清真图直贴 vs OCR 数据复现重绘）、内置参数化咨询图表库（瀑布图/2×2竞品象限/CAGR复合趋势/产业链架构SVG/Harvey Balls矩阵，无母版锁死）、以及纯文本与视觉模型通用的 qa_report.json + 紧凑 JPEG 双重质检。
---

# Executive PPTX v1.3 — 资深行业分析师级汇报与 PPT 生产流

当用户需要生成 `.pptx` / 行业分析报告 / 战略汇报 / 产品方案演示文稿时，**绝不做成流水账文字页或生硬的灰白表格**。严格遵循本 Skill 的 **「4 阶段资深分析师工作流（目标 12–16 轮内高质量交付）」**，同时兼容**纯文本模型**与**视觉模型**。

---

## 一、阶段 0：先立纲再出页（Storyline-First，杜绝流水账）

动手写 HTML 前，先基于检索证据完成一份紧凑的**分析简报（Storyline Brief）**（可直接作为注释/数据结构写在 `/workspace/gen_deck.py` 顶部）：

1. **SCQA 核心论点**：
   - **S（背景）**：当前行业/业务所处阶段与核心规模基数；
   - **C（冲突/痛点）**：市场或客户面临的核心矛盾与增长瓶颈；
   - **Q（核心问题）**：本份报告要回答的关键战略问题；
   - **A（核心判断）**：一句话给出贯穿全篇的中心结论与行动主线。
2. **MECE 逐页地图与 Action Title（结论句主标题）**：
   - **每页主标题必须是“带观点的完整结论句”**（例：`AI 驱动社媒洞察从“事后复盘”迈向“实时决策”，2026 年市场增速达 28%`），**严禁使用“市场现状”“产品介绍”“总结”这类空洞名词标题**！
   - 行业分析报告标准叙事弧（12–16 页为例）：
     - `P1` 深色封面 → `P2` 执行摘要（SCQA 结论横幅 + 3–4 个大号 KPI）→ `P3` 市场规模与增速（**CAGR 复合柱线图**）→ `P4` 驱动力拆解（**瀑布图/Bridge** 或结构对比）→ `P5` 产业链与生态格局（**多层价值链 SVG**）→ `P6` 竞争格局定位（**2×2 竞品象限气泡图**）→ `P7` 章节过渡页 → `P8–P11` 核心方案/产品实景截图/OCR 复现洞察（图文交替，绝不连续同构）→ `P12` 能力对标评估（**Harvey Balls 评分矩阵**）→ `P13–P14` 标杆案例与商业价值 → `P15` 落地路线图与行动建议。
3. **数据证据台账（严守真实性）**：
   - 关键数据必须来源于知识库文档 `dN`、轻量表格（`.xlsx/.csv`）或 `image_ocr`；**禁止臆造未经证实的精确市场数字**；若某维数据为示意/推算，必须在页脚显式标注口径边界。

---

## 二、阶段 1：精准多模态检索 & 双轨视觉素材策略（2–4 轮完成）

1. **并行调用文本检索 + 视觉/OCR 资产检索**：
   - 文本证据：`search_knowledge(query="...", mode="hybrid")` 获取正文观点、数据口径、文档句柄 `dN`。
   - **配图 & OCR 图一步直击**：调用 **`search_knowledge(query="...", asset_type="image", limit=12)`**！
     - 它会直接检索知识库已建索引的 `image_caption`、`image_ocr` 与 `image_vector`，**自动过滤 `<15KB` 的小 Logo/装饰图标**，一次返回候选图的 `resource://<handle>`、文件大小、图注与**完整 `image_ocr` 文字**。
   - 若某篇核心文档带有轻量表格数据源（`file_resource="resource://<handle>"`，`.xlsx/.csv` ≤ 15 MB），用 `read_file` 落盘到 `/workspace/input/kb/` 后用 Python 精确汇总。
2. **双轨素材决策（决定画质上限的核心分水岭）**：
   - **A 轨 —— 高清真图直贴（产品 UI 截图 / 实景照片 / 官方界面）**：
     - 对选中的 3–5 张真图并行调用 `read_file(path="resource://<handle>", detail="brief")`：自动落盘至 `/workspace/input/kb/<handle>.<ext>`，并返回精确像素尺寸 `dimensions=WxH (aspect=..., landscape/portrait)` 与图注。
     - 严格按 `aspect` 选择匹配的横版/竖版画框，并始终加 `object-fit:contain` + 纯白微阴影圆角画框 + 底部图注（**严禁拉伸变形，严禁嵌入整页文字扫描图**）。
   - **B 轨 —— OCR 数据复现重绘（老旧数据图 / 模糊柱状折线图 / 旧风格架构图）**：
     - 看到知识库里的旧报表截图、低清图表或与当前 PPT 配色冲突的架构图时，**不要直接把旧图糊进 PPT**！
     - 直接从 `search_knowledge(asset_type="image")` 返回的 `<image_ocr>`（若当前模型支持视觉可辅以 `read_file(detail="full")` 复核）提取精确维度、年份与数值，使用 `consulting_charts.py` 按当前 PPT 主题色**高清重绘为咨询级图表**，页脚注明 `数据来源：<文档名> 原图数据复现`。

---

## 三、阶段 2：单脚本构建 + 参数化咨询视觉组件库（1–2 轮完成）

写一个结构清晰的 `/workspace/gen_deck.py` 一次性生成 `/workspace/deck.html`。**严禁锁死固定公司母版**，根据行业主题自由选择现代高级配色 `THEME` 字典（科技战略深海蓝 `#0F2744`+电光青 `#0EA5E9`+暖金 `#F59E0B`；消费零售墨玉绿 `#0F3D3E`+珊瑚橙 `#F97316`；金融投资炭黑 `#111827`+宝石蓝 `#1D4ED8`）。

直接在 `gen_deck.py` 导入内置零依赖组件库：
```python
import os, sys
sys.path.insert(0, os.path.join(os.environ["YEWEN_SKILL_DIR"], "scripts"))
import consulting_charts as cc
```

可用高级咨询组件（全部自动适配你自定义的 `THEME`，100% 离线 3× 视网膜清晰度）：
1. **市场规模 + 增速 + CAGR 徽章复合图**：
   `cc.echarts_div(cc.echarts_cagr_combo(title, years, bars, line_values, bar_name, line_name, cagr_text="CAGR +28.1%", theme=THEME, bar_unit="亿"), height="370px")`
2. **McKinsey 价值/增长拆解瀑布图（Bridge）**：
   `cc.echarts_div(cc.echarts_waterfall(title, labels, values, theme=THEME, unit="亿"), height="370px")`
3. **2×2 竞品定位象限气泡图（Magic Quadrant）**：
   `cc.echarts_div(cc.echarts_quadrant_bubble(title, items, x_label, y_label, quadrants={"tr":"领导者","tl":"挑战者","br":"潜力者","bl":"利基玩家"}, theme=THEME), height="380px")`
4. **排名横向条形图**：
   `cc.echarts_div(cc.echarts_ranked_hbar(title, items, theme=THEME, unit="%"), height="340px")`
5. **产业链 / 生态全景多层架构 SVG**：
   `cc.svg_value_chain(layers, theme=THEME, width=1170, height=400)`
6. **Harvey Balls 咨询能力评估矩阵（替代生硬灰白表格！）**：
   `cc.html_harvey_scorecard(headers, rows, theme=THEME)`（`scores` 取 `0..4`，生成空心到实心四分圆球）
7. **原生 PowerPoint 可改数据图表**（当需要用户在 PPT 内双击编辑 Excel 数据时）：
   `<div style="width:660px;height:360px;" data-pptx-chart='{"type":"bar","title":"...","data":[...]}'></div>`
8. **离线矢量图标**：`<i data-icon="trending-up" style="width:20px;height:20px;color:#0EA5E9;"></i>`

### 美学与版式红线（必须全部遵守）
- **画布与留白**：`<section class="slide">` 固定 `width:1280px; height:720px; overflow:hidden; padding:40px 52px 26px;`；浅灰质感底色（如 `#F5F7FB`）承载悬浮纯白圆角卡片（`border-radius:12px; border:1px solid #E2E8F0; box-shadow:0 4px 14px rgba(15,23,42,0.06)`），单页正文精炼（≤ 280 字），保留 ≥ 25–30% 留白。
- **杜绝表格疲劳**：全套 PPT 中原生 `<table>` **最多 ≤ 2–3 页**；能力对比优先用 `cc.html_harvey_scorecard`，流程/路线图优先用横向序号徽章卡或 `cc.svg_value_chain`。
- **节奏起伏**：12+ 页必须包含「深色渐变封面 + 1–2 页深色章节过渡页 + 大数字渐变 KPI 英雄卡 + 至少 3 种不同图表/视觉版式」，严禁连续两页布局雷同。
- **中文字体与层级**：统一 `'Noto Sans CJK SC', sans-serif`；主标题 `26–30px` 粗体、副标题 `14–15px`、卡片标题 `17–19px` 粗体、正文 `14–15px`（`line-height:1.45`）、KPI 大数字 `38–46px` 粗体、页脚来源 `11.5–12px`。
- **演讲者备注**：每页底部放 `<aside class="notes">来源文档、数据口径与演讲提示...</aside>`。

---

## 四、阶段 3：一键编译 + 通用自检交付（兼容纯文本 & 视觉模型）

1. **一条命令完成编译、硬拦截、结构化质检报告与紧凑拼图**：
   ```bash
   node "$YEWEN_SKILL_DIR/scripts/html2pptx_pro.mjs" /workspace/deck.html /workspace/output/<简洁清晰文件名>.pptx --qa /workspace/tmp/qa
   ```
   该命令自动产出：
   - `/workspace/output/<简洁清晰文件名>.pptx`（100% 原生可编辑 PowerPoint）
   - `/workspace/tmp/qa/qa_report.json`（每页溢界、重叠、图片拉伸失真、字数密度、留白率、表格页数体检 JSON）
   - `/workspace/tmp/qa/qa_sheet_1.jpg`（及 `qa_sheet_2.jpg`，每张 ≤ 320 KB 紧凑拼图）
2. **按模型能力自适应终审（最多 1 轮修补，禁止推倒重写）**：
   - **所有模型必做**：用 `read_file` 读取 `/workspace/tmp/qa/qa_report.json`，核对 `overall_pass == true`、`table_slide_count <= 3` 及各页 `warnings`（如有溢界/失真/字数过载，定点调整对应页函数后重跑一次）。
   - **视觉能力模型增强**：如果当前模型支持视觉（或配置了 VLM），并行 `read_file` 查看 `qa_sheet_1.jpg` / `qa_sheet_2.jpg`（`detail="brief"`）做最终视觉把关。
   - **纯文本模型优雅降级**：若当前模型不支持看图，**无需调用视觉验收**，以 `qa_report.json` 全部通过为准直接交付，不报错、不反复试探。
3. **干净交付**：
   - 成品只保留在 `/workspace/output/<简洁清晰文件名>.pptx`；严禁将 `gen_deck.py`、`deck.html`、`qa_report.json` 或 QA 图片放入 `/workspace/output/`。
   - 最终回复清晰给出：核心洞察摘要（SCQA）、分页目录、嵌入的知识库真实截图/OCR 复现来源与口径边界。
