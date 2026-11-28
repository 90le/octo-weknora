---
name: executive-pptx
version: 1.2.0
description: 高速直出商业数据分析师与战略咨询级别的原生可编辑 PowerPoint (.pptx) 汇报文件。支持完全自由的视觉美学设计（无死板母版束缚）、结合知识库真实原图（resource://）与轻量源文件（.xlsx/.csv 等）、离线 ECharts 商业数据图表、渐变英雄卡、矢量 SVG 图标与架构图、1:1 中文字高精准编译及一键紧凑 JPEG 视觉终审。
---

# Executive PPTX — 高颜值、极速直出商业汇报工作流

当用户需要生成 `.pptx` / PPT / 演示文稿时，**不要裸写简陋的 `python-pptx` 纯文字页，也不要把整套 PPT 做成干巴巴的灰白表格**。严格执行本 Skill 的「**4 阶段极速直出工作流（目标 12–18 轮内交付）**」，产出兼具顶级咨询视觉美感与 100% 原生可编辑性的专业汇报。

---

## 一、极速直出与防空转纪律（必须严守，杜绝 30 分钟长拖）

1. **并行检索与选图（2–4 轮完成）**：
   - 并行调用 `list_documents` / `search_knowledge` / `read_document` 获取素材与 `resource://<handle>` 图片列表。
   - **选图黄金法则**：一次并行 `read_file(path="resource://<handle>", detail="brief", focus="确认清晰度与内容")` 查看 3–4 张候选图（调用同时会自动落盘至 `/workspace/input/kb/<handle>.<ext>`）。
   - **优先选用 PPTX 源里的纯界面截图/架构图；直接跳过整页 PDF 扫描图或大片留白的文档页**——绝不对同一张图反复手工裁切超过 1 次！
2. **单脚本一次性生成完整 `deck.html`（1–2 轮完成）**：
   - 不要用多次 `write_sandbox_file` append 拼接几万字节原始 HTML；优先写一个清晰简洁的 Python 脚本 `/workspace/gen_deck.py` 一次性生成 `/workspace/deck.html`，结构清晰、修改某页只需改对应函数。
3. **一键编译 + 排版质检 + 紧凑视觉拼图（1 轮完成）**：
   ```bash
   node "$YEWEN_SKILL_DIR/scripts/html2pptx_pro.mjs" /workspace/deck.html /workspace/output/<简洁清晰文件名>.pptx --qa /workspace/tmp/qa
   ```
   - 一条命令同时完成：Chromium 1280×720 测量 + `--strict` 溢出/重叠硬拦截 + 原生 `.pptx` 编译 + LibreOffice 渲染为 **1–2 张紧凑 JPEG 拼图**（`qa_sheet_1.jpg`、`qa_sheet_2.jpg`，每张 ≤ 320 KB，直接可读，**禁止再写脚本切图压图**）。
4. **视觉终审上限（最多 1 轮核验 + 1 轮定点修补）**：
   - 并行调用 `read_file` 查看 `qa_sheet_1.jpg` / `qa_sheet_2.jpg`（`detail="brief"` 即可看清整页版式、字号、图片与对齐）。
   - 若发现个别页面瑕疵，只定点修改该页后重新跑一次 `--qa` 验证即可直接交付；**禁止全篇推倒重写超过 1 次**。

---

## 二、高颜值咨询美学规范（自由设计主题，彻底告别生硬表格风）

1. **禁止“灰白表格疲劳”（核心红线）**：
   - **绝对禁止把 `<table>` 当作卡片容器或布局工具**！整套 10–15 页汇报中，纯数据对比 `<table>` **最多不超过 2–3 页**。
   - 其余页面必须通过 **Flexbox / Grid 现代卡片 + 阴影层次 + 色彩对比 + 图表 + 真实截图** 交替呈现！
2. **自由定义高级视觉系统（不锁死固定母版）**：
   - 按汇报主题选择现代商业配色（如：科技战略「深海蓝 `#0F2744` + 电光青 `#0EA5E9` + 暖金 `#F59E0B`」、品牌消费「墨玉绿 `#0F3D3E` + 珊瑚橙 `#F97316`」、金融咨询「炭黑 `#111827` + 宝石蓝 `#1D4ED8`」）。
   - **画布层次感**：内容页优先采用极浅质感底色（如 `#F5F7FB` 或 `#F8FAFC`），上面放置纯白悬浮圆角卡片（`background:#FFFFFF; border:1px solid #E5EAF2; border-radius:12px; box-shadow:0 4px 14px rgba(15,23,42,0.06);`）+ 顶部/左侧 4px 主题色条，视觉立体感瞬间甩开白底黑框表格。
   - **节奏呼吸感（12+ 页必备）**：穿插 **1 页深色渐变封面 + 1–2 页深色/渐变章节过渡页 + 大号梯度 KPI 英雄卡**（编译器原生支持容器 `linear-gradient` 背景并保持卡片内文字 100% 原生可编辑！）。
3. **咨询级版式库（严禁连续两页雷同）**：
   - **封面页**：深色渐变背景 + 左对齐大标题（`44–52px`）+ 副标题 + 底部场景胶囊徽章 + 右侧几何 SVG 视觉装饰。
   - **执行摘要页**：顶部渐变结论横幅（Action Banner）+ 3–4 个大号 KPI 指标卡（数字 `40–48px` 加粗 + 离线 Lucide 图标）+ 底部 3 列核心判断卡。
   - **产品真图洞察页（左图右解读 / 右图左解读交替）**：将 `/workspace/input/kb/<handle>.png` 放进带微阴影与圆角白框的「产品展示画框」（`object-fit:contain` + 底部浅灰图注），另一侧配 3 条精炼价值要点卡（左色条 + 图标 + 1 句结论）。
   - **数据图表洞察页**：左侧 `data-echarts` 高清商业图表（柱状/折线/雷达/漏斗/散点/环形，占 60% 宽）+ 右侧 2–3 个大数字洞察卡（40% 宽）。
   - **全景架构 / 流程闭环页**：分层泳道卡或手绘矢量 `<svg>` 闭环架构图（不靠文字硬拼表格）。
   - **对比矩阵页（全套最多 2–3 页）**：原生 `<table>`，深色表头 + 斑马纹浅灰行 + 高亮关键差异列，每行单行或至多双行精炼词组。
   - **2×2 策略象限 / 横向阶段里程碑页**：带圆形序号徽章（`01 / 02 / 03 / 04`）的 4 栏横向卡片或 2×2 矩阵卡，绝对不用复杂 `colspan` 表格拼时间轴！
4. **1:1 清晰可读字号体系（编译器已精确校准 Chromium ↔ PowerPoint 行高）**：
   - 画布固定 `<section class="slide">`（`width:1280px; height:720px; overflow:hidden; padding:42px 52px 28px;`），单页正文精炼（≤ 130 字），保留 ≥ 28% 呼吸留白。
   - 推荐字号：页眉小标（Kicker）`13px`、结论主标题（Action Title）`28–32px` 粗体、副标题 `15–16px`、卡片标题 `18–20px` 粗体、卡片正文 `15–16px`（`line-height:1.45`）、KPI 大数字 `40–50px` 粗体、页脚来源 `12px`。
   - 中文字体统一 `'Noto Sans CJK SC', sans-serif`（标题可选 `'Noto Serif CJK SC', serif`）。

---

## 三、离线视觉组件速查（零外链，100% 离线可靠）

1. **离线 ECharts 商业图表（`data-echarts`，3× 视网膜高清透明底）**：
   - 直接在 `<div style="width:660px;height:380px;" data-echarts='{...}'></div>` 写标准 ECharts JSON（编译器已独立挂载离线 ECharts，支持柱状/条形/折线/雷达/饼环/散点/热力，自动启用商务配色与思源中文字体；不要给 `<div>` 起 `id="echarts"`）。
   - 若需用户在 PowerPoint 里双击改 Excel 数据，可用 `data-pptx-chart='{"type":"bar","title":"季度趋势","data":[{"name":"增速","labels":["Q1","Q2","Q3","Q4"],"values":[12,18,27,35]}]}'`。
2. **知识库真实素材（原图 + 轻量数据表）**：
   - `read_file(path="resource://<handle>")` 会同时返回视觉预览并将原图物化至 `/workspace/input/kb/<handle>.<ext>`，直接 `<img src="/workspace/input/kb/<handle>.png">` 嵌入。
   - `read_document` 返回的轻量 `.xlsx / .csv`（`file_resource="resource://<handle>"`，≤ 15 MB）同样可用 `read_file` 物化至 `/workspace/input/kb/`，用 `openpyxl`/`pandas` 精确汇总出真实数据图表。
3. **离线矢量商业图标**：
   - `<i data-icon="trending-up" style="width:22px;height:22px;color:#1D4ED8;"></i>`（内置 `trending-up`、`bar-chart`、`pie-chart`、`target`、`users`、`layers`、`cpu`、`database`、`shield-check`、`zap`、`globe`、`search`、`check-circle`、`alert-triangle`、`lightbulb`、`rocket`、`briefcase`、`compass`、`git-branch`、`clock`、`award`、`file-text`、`sparkles`、`arrow-right`、`activity`、`sliders`、`lock`、`message-square`、`filter`）。
4. **演讲者备注**：每页底部放 `<aside class="notes">口径来源与演讲补充...</aside>`。

---

## 四、交付自检清单

- 成品 `.pptx` 位于 `/workspace/output/<简洁清晰文件名>.pptx` 且非空；草稿 `gen_deck.py`、`deck.html`、`/workspace/tmp/qa/` 绝不放入 `/workspace/output/`。
- 最终回复清晰列出文件名、页数结构、嵌入的知识库真实截图/数据来源及口径边界。
