---
name: executive-pptx
version: 1.0.0
description: 设计并生成商业数据分析师与战略咨询级别的原生可编辑 PowerPoint (.pptx) 汇报文件。支持完全自由的视觉美学设计（无死板母版束缚）、离线 ECharts 商业数据图表、矢量 SVG 图标与架构图、自动排版溢出质检以及 LibreOffice 缩略图视觉终审。适用于产品全景汇报、商业洞察分析、客户提案、项目复盘与多维对比展示。
---

# Executive PPTX — 专业商业汇报生成工作流

当用户需要生成 `.pptx` / PPT / 演示文稿 / 幻灯片时，**不要裸写简陋的 `python-pptx` 纯文字页**。使用本 Skill 的「**自由 HTML/CSS 幻灯片设计 → Chromium 真实排版测量与硬质检 → 原生可编辑 `.pptx` 编译 → 视觉终审**」流水线，产出兼具顶级视觉美感与 100% 原生可编辑性（文本框、矢量卡片、原生表格、高清商业图表、演讲者备注）的专业汇报。

---

## 一、专业商业设计原则（自由发挥美学，严守专业底线）

1. **按主题自由定义视觉系统（不套死板固定母版）**：
   - 根据汇报主题与受众自主选择最契合的视觉风格（如：管理层战略汇报用「高留白麦肯锡浅灰+深海蓝」、科技产品发布用「深空暗黑+电光青」、品牌洞察报告用「暖白杂志风+琥珀强调色」）。
   - 在 `<style>:root { ... }</style>` 中统一定义 4 组颜色变量：背景色、主色、1 个强调色、中性文字色阶（正文字色、次要说明色、浅分割线），全套不超过 3 个主色相。
   - 中文字体统一使用 `'Noto Sans CJK SC', sans-serif`（或衬线标题 `'Noto Serif CJK SC', serif`），确保与离线沙箱字体 100% 对齐不偏移。

2. **咨询分析师叙事结构（SCQA + 金字塔原则）**：
   - **Action Title（结论句标题）**：每页主标题必须是“一句话核心洞察/结论”（例如：`Media Studio 覆盖全链路监测与跨屏归因，支撑预算分配提效`），拒绝空洞的词组标题（如“产品介绍”）。
   - **依据与口径诚实透明**：凡引用知识库数据、报价、案例或指标口径，在页脚小字（11–12px）标明来源与边界；查不到的数据绝不编造。
   - **版式节奏交替**：严禁连续两页使用相同版式！灵活组合以下高信息价值版式：
     - 封面页（大标题 + 副标题 + 场景标签 + 视觉几何装饰，不堆字）
     - 执行摘要页（Executive Summary：顶部总结论 + 3–4 个大号 KPI 指标卡 + 关键行动建议）
     - 数据洞察页（左侧 ECharts 真实数据图表 60% + 右侧 3 条结论解读卡 40%）
     - 产品/方案全景架构页（分层架构图或流程闭环 SVG + 核心价值点）
     - 多维对比矩阵 / 表格页（原生 `<table>`，高亮推荐项或关键差异列）
     - 2×2 策略象限 / 案例复盘页（背景 → 动作 → 量化结果）
     - 总结与下一步行动页（Next Steps + 清晰分工/时间轴）

3. **防溢出与信息密度红线**：
   - 固定画布尺寸：每页必须是一个 `<section class="slide">`（`width: 1280px; height: 720px; overflow: hidden;`），四边安全边距 ≥ `48px`（顶部标题区 + 中部主内容区 + 底部页脚/来源栏）。
   - 单页文字保持精炼（≤ 120–150 字），保证 ≥ 30% 留白；正文最小 `13px`，标题 `24–30px`，大号 KPI 数字 `36–48px`。

---

## 二、HTML 幻灯片编写规范（供编译器 1:1 转为原生 PPTX）

在 `/workspace/deck.html` 中编写完整的多页 HTML（若页数 > 10 页，可先用 Python 脚本生成 `/workspace/deck.html`，或分片 `write_sandbox_file` `overwrite` + `append`）：

1. **页面结构**：
   ```html
   <section class="slide">
     <!-- 你的自由 Flexbox/Grid 排版内容 -->
     <aside class="notes">本页演讲者备注与口径补充...</aside>
   </section>
   ```
2. **离线商业数据图表（零外链，自动注入 `echarts`）**：
   - 方式 A（推荐，最简洁）：给容器加 `data-echarts` JSON 属性，编译器自动以 `3×` 高清透明底渲染并嵌入：
     ```html
     <div style="width:680px;height:400px;" data-echarts='{"grid":{"top":36,"right":24,"bottom":36,"left":48},"xAxis":{"type":"category","data":["Q1","Q2","Q3","Q4"]},"yAxis":{"type":"value"},"series":[{"type":"bar","data":[120,165,210,268],"label":{"show":true,"position":"top"}}]}'></div>
     ```
   - 方式 B（原生可编辑 Excel 数据图表）：给容器加 `data-pptx-chart='{"type":"bar","title":"季度趋势","data":[{"name":"增速","labels":["Q1","Q2","Q3","Q4"],"values":[12,18,27,35]}]}'`，在 PowerPoint 中可双击改数据！
   - 方式 C（复杂统计图）：用 `matplotlib` / `seaborn`（300 DPI 透明底 PNG）保存至 `/workspace/tmp/chart1.png` 后用 `<img src="/workspace/tmp/chart1.png">` 引入。
3. **离线矢量商业图标**：
   - 直接使用 `<i data-icon="trending-up" style="width:22px;height:22px;color:#2563EB;"></i>`（内置 `trending-up`、`bar-chart`、`pie-chart`、`target`、`users`、`layers`、`cpu`、`database`、`shield-check`、`zap`、`globe`、`search`、`check-circle`、`alert-triangle`、`lightbulb`、`rocket`、`briefcase`、`compass`、`git-branch`、`clock`、`award`、`file-text`、`sparkles`、`arrow-right`、`activity`、`sliders`、`lock`、`message-square`、`filter`）。
4. **复杂视觉块保真**：
   - 复杂手绘 SVG 流程图/架构图、带多重 CSS 渐变的视觉徽章可直接加 `data-rasterize` 属性让编译器以 3× 高清无损嵌入；普通卡片、文字、表格**不要**加 `data-rasterize`，以保持 100% 原生可编辑！

---

## 三、标准执行与质量闭环（必须按步完成）

1. **生成 `/workspace/deck.html`**（草稿放在 `/workspace/`，不要放 `/workspace/output/`）。
2. **一键编译 + 硬规则溢出/重叠质检**：
   ```bash
   node "$YEWEN_SKILL_DIR/scripts/html2pptx_pro.mjs" /workspace/deck.html /workspace/output/<简洁清晰文件名>.pptx
   ```
   - **若返回退出码 3（Layout QA 报错）**：仔细阅读报错的页码、溢出像素或重叠元素，调整该页 CSS 间距/字号或精简文案，重新编译直至 `qa_issues=0`！
3. **终稿真实渲染视觉核验（Visual QA）**：
   ```bash
   python3 "$YEWEN_SKILL_DIR/scripts/render_pptx_qa.py" /workspace/output/<简洁清晰文件名>.pptx /workspace/tmp/pptx_qa_grid.png
   ```
   - 用 `read_file` 查看 `/workspace/tmp/pptx_qa_grid.png`（确认中文字体清晰、无截断重叠、版式丰富、商业美感达标）；如发现某页视觉失衡，针对性微调 `deck.html` 并重新编译。
4. **交付收尾**：
   - 确保成品 `.pptx` 在 `/workspace/output/` 下且非空，所有中间文件（`deck.html`、`pptx_qa_grid.png`）保留在 `/workspace/` 或 `/workspace/tmp/`（切勿放入 `/workspace/output/`）。
   - 最终答复中清晰列出文件名称、页数与核心内容结构。
