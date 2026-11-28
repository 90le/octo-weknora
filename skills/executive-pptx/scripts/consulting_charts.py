#!/usr/bin/env python3
"""
consulting_charts.py — Parameterized consulting-grade chart & infographic helpers
for executive-pptx v1.3.0.

Zero external dependencies, 100% offline, and theme-agnostic (no locked corporate
master template). Pass any palette dictionary `theme` to generate cohesive:
  - Waterfall / Bridge ECharts specs (McKinsey-style value bridge)
  - 2x2 Magic Quadrant / Competitive Positioning Bubble ECharts specs
  - Annotated CAGR Trend + Bar Combo ECharts specs
  - Ranked Horizontal Bar / Funnel ECharts specs
  - Multi-Layer Industry Value Chain / Ecosystem SVG diagrams
  - Harvey Balls Consulting Capability Scorecard HTML
  - Executive KPI Hero Strip HTML
"""

from __future__ import annotations

import html as _html
import json
from typing import Any, Dict, List, Optional, Sequence

DEFAULT_THEME: Dict[str, str] = {
    "primary": "#0F2744",      # Deep navy / header ink
    "accent": "#0EA5E9",       # Electric cyan / primary bar
    "secondary": "#1D4ED8",    # Royal blue / secondary bar
    "highlight": "#F59E0B",    # Warm amber / callout & CAGR badge
    "positive": "#10B981",     # Emerald / growth & positive delta
    "negative": "#EF4444",     # Coral red / drag & negative delta
    "muted": "#94A3B8",        # Slate muted / reference lines
    "ink": "#0F172A",          # Primary body text
    "subtext": "#475569",      # Secondary label text
    "surface": "#FFFFFF",      # Card fill
    "canvas": "#F5F7FB",       # Slide canvas tint
    "border": "#E2E8F0",       # Subtle divider
}


def resolve_theme(theme: Optional[Dict[str, str]] = None) -> Dict[str, str]:
    merged = dict(DEFAULT_THEME)
    if theme:
        for k, v in theme.items():
            if v:
                merged[k] = str(v)
    return merged


def _esc(text: Any) -> str:
    return _html.escape(str(text if text is not None else ""), quote=True)


def to_echarts_attr(option: Dict[str, Any]) -> str:
    """Serialize an ECharts option dict safely for an HTML single-quoted data-echarts attribute."""
    raw = json.dumps(option, ensure_ascii=False, separators=(",", ":"))
    return raw.replace("&", "&amp;").replace("'", "&#39;").replace("<", "&lt;").replace(">", "&gt;")


def echarts_div(option: Dict[str, Any], width: str = "100%", height: str = "360px", extra_style: str = "") -> str:
    style = f"width:{width};height:{height};{extra_style}"
    return f"<div style='{_esc(style)}' data-echarts='{to_echarts_attr(option)}'></div>"


def echarts_waterfall(
    title: str,
    labels: Sequence[str],
    values: Sequence[float],
    theme: Optional[Dict[str, str]] = None,
    unit: str = "",
    totals_indices: Optional[Sequence[int]] = None,
) -> Dict[str, Any]:
    """
    Build a McKinsey-style Waterfall (Bridge) ECharts option.
    - `totals_indices`: indices that render as full-height total bars (default: first & last).
    - Positive intermediate deltas render in `theme["positive"]`; negative in `theme["negative"]`.
    """
    th = resolve_theme(theme)
    n = len(labels)
    if totals_indices is None:
        total_set = {0, n - 1} if n >= 2 else {0}
    else:
        total_set = set(totals_indices)

    base_series: List[float] = []
    bar_series: List[Dict[str, Any]] = []
    running = 0.0
    for idx, val in enumerate(values):
        v = float(val)
        if idx in total_set:
            base_series.append(0.0)
            bar_series.append({
                "value": round(v, 2),
                "itemStyle": {"color": th["primary"], "borderRadius": [6, 6, 0, 0]},
                "label": {"formatter": f"{v:g}{unit}", "color": th["primary"], "fontWeight": "bold"},
            })
            running = v
        elif v >= 0:
            base_series.append(round(running, 2))
            bar_series.append({
                "value": round(v, 2),
                "itemStyle": {"color": th["positive"], "borderRadius": [6, 6, 0, 0]},
                "label": {"formatter": f"+{v:g}{unit}", "color": th["positive"], "fontWeight": "bold"},
            })
            running += v
        else:
            running += v
            base_series.append(round(max(0.0, running), 2))
            bar_series.append({
                "value": round(abs(v), 2),
                "itemStyle": {"color": th["negative"], "borderRadius": [6, 6, 0, 0]},
                "label": {"formatter": f"{v:g}{unit}", "color": th["negative"], "fontWeight": "bold"},
            })

    return {
        "animation": False,
        "title": {
            "text": title,
            "left": 4,
            "top": 0,
            "textStyle": {"fontSize": 15, "fontWeight": "bold", "color": th["ink"], "fontFamily": "Noto Sans CJK SC"},
        },
        "grid": {"left": 44, "right": 20, "top": 48, "bottom": 42, "containLabel": True},
        "xAxis": {
            "type": "category",
            "data": list(labels),
            "axisLine": {"lineStyle": {"color": th["border"]}},
            "axisTick": {"show": False},
            "axisLabel": {"color": th["subtext"], "fontSize": 12, "interval": 0},
        },
        "yAxis": {
            "type": "value",
            "axisLine": {"show": False},
            "axisTick": {"show": False},
            "splitLine": {"lineStyle": {"color": th["border"], "type": "dashed"}},
            "axisLabel": {"color": th["subtext"], "fontSize": 11, "formatter": f"{{value}}{unit}"},
        },
        "series": [
            {
                "name": "base",
                "type": "bar",
                "stack": "bridge",
                "silent": True,
                "itemStyle": {"borderColor": "transparent", "color": "transparent"},
                "data": base_series,
            },
            {
                "name": "value",
                "type": "bar",
                "stack": "bridge",
                "barMaxWidth": 48,
                "label": {"show": True, "position": "top", "fontSize": 12},
                "data": bar_series,
            },
        ],
    }


def _nice_ceil(val: float) -> float:
    """Round a positive upper axis bound to a clean human-readable tick ceiling."""
    if val <= 0:
        return 10.0
    import math
    exp = math.floor(math.log10(val))
    base = 10 ** exp
    norm = val / base
    for step in (1.0, 1.2, 1.5, 2.0, 2.5, 3.0, 4.0, 5.0, 6.0, 8.0, 10.0):
        if norm <= step:
            return round(step * base, 2)
    return round(10.0 * base, 2)


def echarts_cagr_combo(
    title: str,
    years: Sequence[str],
    bars: Sequence[float],
    line_values: Sequence[float],
    bar_name: str,
    line_name: str,
    cagr_text: str = "",
    theme: Optional[Dict[str, str]] = None,
    bar_unit: str = "",
    line_unit: str = "%",
) -> Dict[str, Any]:
    """
    Build a consulting Market-Size Bar + YoY Growth Line combo chart with an
    optional highlighted CAGR callout graphic badge.
    """
    th = resolve_theme(theme)
    max_bar = max((float(v) for v in bars), default=100.0)
    max_line = max((float(v) for v in line_values), default=30.0)
    bar_axis_max = _nice_ceil(max_bar * 1.25) if max_bar > 0 else None
    line_axis_max = _nice_ceil(max_line * 1.45) if max_line > 0 else None
    bar_data = []
    n = len(bars)
    for i, v in enumerate(bars):
        is_forecast = i == n - 1 and n >= 3
        bar_data.append({
            "value": v,
            "itemStyle": {
                "color": th["accent"] if is_forecast else th["secondary"],
                "borderRadius": [6, 6, 0, 0],
            },
        })

    opt: Dict[str, Any] = {
        "animation": False,
        "title": {
            "text": title,
            "left": 4,
            "top": 0,
            "textStyle": {"fontSize": 15, "fontWeight": "bold", "color": th["ink"], "fontFamily": "Noto Sans CJK SC"},
        },
        "legend": {
            "top": 26,
            "left": 4,
            "itemWidth": 12,
            "itemHeight": 10,
            "textStyle": {"color": th["subtext"], "fontSize": 12},
        },
        "grid": {"left": 44, "right": 48, "top": 66, "bottom": 36, "containLabel": True},
        "xAxis": {
            "type": "category",
            "data": list(years),
            "axisLine": {"lineStyle": {"color": th["border"]}},
            "axisTick": {"show": False},
            "axisLabel": {"color": th["subtext"], "fontSize": 12},
        },
        "yAxis": [
            {
                "type": "value",
                "name": bar_unit,
                "max": bar_axis_max,
                "nameTextStyle": {"color": th["subtext"], "fontSize": 11},
                "splitLine": {"lineStyle": {"color": th["border"], "type": "dashed"}},
                "axisLabel": {"color": th["subtext"], "fontSize": 11},
            },
            {
                "type": "value",
                "min": 0,
                "max": line_axis_max,
                "splitLine": {"show": False},
                "axisLabel": {"color": th["subtext"], "fontSize": 11, "formatter": f"{{value}}{line_unit}"},
            },
        ],
        "series": [
            {
                "name": bar_name,
                "type": "bar",
                "barMaxWidth": 42,
                "data": bar_data,
                "label": {
                    "show": True,
                    "position": "top",
                    "color": th["ink"],
                    "fontSize": 12,
                    "fontWeight": "bold",
                    "formatter": f"{{c}}{bar_unit}",
                },
            },
            {
                "name": line_name,
                "type": "line",
                "yAxisIndex": 1,
                "smooth": 0.25,
                "symbol": "circle",
                "symbolSize": 8,
                "lineStyle": {"width": 3, "color": th["highlight"]},
                "itemStyle": {"color": th["highlight"], "borderColor": "#FFFFFF", "borderWidth": 2},
                "label": {
                    "show": True,
                    "position": "top",
                    "color": th["highlight"],
                    "fontSize": 11,
                    "fontWeight": "bold",
                    "formatter": f"{{c}}{line_unit}",
                },
                "data": list(line_values),
            },
        ],
    }
    if cagr_text:
        opt["graphic"] = [
            {
                "type": "group",
                "right": 14,
                "top": 6,
                "children": [
                    {
                        "type": "rect",
                        "shape": {"width": 156, "height": 30, "r": 6},
                        "style": {"fill": "#FEF3C7", "stroke": th["highlight"], "lineWidth": 1},
                    },
                    {
                        "type": "text",
                        "left": "center",
                        "top": 8,
                        "style": {
                            "text": cagr_text,
                            "fill": "#92400E",
                            "font": "bold 12px 'Noto Sans CJK SC', sans-serif",
                        },
                    },
                ],
            }
        ]
    return opt


def echarts_quadrant_bubble(
    title: str,
    items: Sequence[Dict[str, Any]],
    x_label: str,
    y_label: str,
    quadrants: Optional[Dict[str, str]] = None,
    theme: Optional[Dict[str, str]] = None,
    x_mid: float = 50,
    y_mid: float = 50,
) -> Dict[str, Any]:
    """
    Build a 2x2 Magic Quadrant / Competitive Positioning Bubble chart.
    Each item in `items`: {"name": str, "x": float(0-100), "y": float(0-100), "size": float(16-54), "highlight": bool}
    `quadrants`: {"tr": "领导者", "tl": "挑战者", "br": "潜力者", "bl": "细分玩家"}
    """
    th = resolve_theme(theme)
    q = quadrants or {"tr": "领先象限", "tl": "专精象限", "br": "增长象限", "bl": "基础象限"}
    scatter_data = []
    for it in items:
        hl = bool(it.get("highlight"))
        sz = max(18, min(52, float(it.get("size", 28))))
        px = float(it.get("x", 50))
        py = float(it.get("y", 50))
        lbl_pos = "bottom" if py >= 76 else "top"
        scatter_data.append({
            "name": str(it.get("name", "")),
            "value": [px, py, sz],
            "symbolSize": sz,
            "itemStyle": {
                "color": th["highlight"] if hl else th["secondary"],
                "opacity": 0.92 if hl else 0.78,
                "borderColor": "#FFFFFF",
                "borderWidth": 2,
            },
            "label": {
                "show": True,
                "formatter": "{b}",
                "position": lbl_pos,
                "distance": 6,
                "color": th["primary"] if hl else th["ink"],
                "fontWeight": "bold" if hl else "normal",
                "fontSize": 12 if hl else 11,
            },
        })

    return {
        "animation": False,
        "title": {
            "text": title,
            "left": 4,
            "top": 0,
            "textStyle": {"fontSize": 15, "fontWeight": "bold", "color": th["ink"], "fontFamily": "Noto Sans CJK SC"},
        },
        "grid": {"left": 56, "right": 34, "top": 46, "bottom": 48},
        "xAxis": {
            "type": "value",
            "min": 0,
            "max": 100,
            "name": x_label,
            "nameLocation": "middle",
            "nameGap": 28,
            "nameTextStyle": {"color": th["subtext"], "fontSize": 12, "fontWeight": "bold"},
            "splitLine": {"lineStyle": {"color": "#F1F5F9"}},
            "axisLine": {"lineStyle": {"color": th["border"]}},
            "axisLabel": {"color": th["muted"], "fontSize": 11},
        },
        "yAxis": {
            "type": "value",
            "min": 0,
            "max": 100,
            "name": y_label,
            "nameLocation": "middle",
            "nameGap": 36,
            "nameTextStyle": {"color": th["subtext"], "fontSize": 12, "fontWeight": "bold"},
            "splitLine": {"lineStyle": {"color": "#F1F5F9"}},
            "axisLine": {"lineStyle": {"color": th["border"]}},
            "axisLabel": {"color": th["muted"], "fontSize": 11},
        },
        "graphic": [
            {"type": "text", "right": 42, "top": 54, "style": {"text": q.get("tr", ""), "fill": th["secondary"], "font": "bold 12px 'Noto Sans CJK SC', sans-serif"}},
            {"type": "text", "left": 66, "top": 54, "style": {"text": q.get("tl", ""), "fill": th["subtext"], "font": "bold 12px 'Noto Sans CJK SC', sans-serif"}},
            {"type": "text", "right": 42, "bottom": 56, "style": {"text": q.get("br", ""), "fill": th["subtext"], "font": "bold 12px 'Noto Sans CJK SC', sans-serif"}},
            {"type": "text", "left": 66, "bottom": 56, "style": {"text": q.get("bl", ""), "fill": th["muted"], "font": "bold 12px 'Noto Sans CJK SC', sans-serif"}},
        ],
        "series": [
            {
                "type": "scatter",
                "data": scatter_data,
                "markLine": {
                    "silent": True,
                    "symbol": "none",
                    "lineStyle": {"type": "dashed", "color": th["muted"], "width": 1.2},
                    "label": {"show": False},
                    "data": [{"xAxis": x_mid}, {"yAxis": y_mid}],
                },
            }
        ],
    }


def echarts_ranked_hbar(
    title: str,
    items: Sequence[Dict[str, Any]],
    theme: Optional[Dict[str, str]] = None,
    unit: str = "",
) -> Dict[str, Any]:
    """
    Build a ranked horizontal bar chart.
    Each item: {"name": str, "value": float, "highlight": bool}
    """
    th = resolve_theme(theme)
    ordered = list(items)[::-1]
    labels = [str(it["name"]) for it in ordered]
    data = [
        {
            "value": float(it["value"]),
            "itemStyle": {
                "color": th["highlight"] if it.get("highlight") else th["secondary"],
                "borderRadius": [0, 6, 6, 0],
            },
        }
        for it in ordered
    ]
    return {
        "animation": False,
        "title": {
            "text": title,
            "left": 4,
            "top": 0,
            "textStyle": {"fontSize": 15, "fontWeight": "bold", "color": th["ink"], "fontFamily": "Noto Sans CJK SC"},
        },
        "grid": {"left": 14, "right": 56, "top": 42, "bottom": 16, "containLabel": True},
        "xAxis": {"type": "value", "show": False},
        "yAxis": {
            "type": "category",
            "data": labels,
            "axisLine": {"show": False},
            "axisTick": {"show": False},
            "axisLabel": {"color": th["ink"], "fontSize": 12, "fontWeight": "bold"},
        },
        "series": [
            {
                "type": "bar",
                "barMaxWidth": 24,
                "data": data,
                "label": {
                    "show": True,
                    "position": "right",
                    "color": th["ink"],
                    "fontSize": 12,
                    "fontWeight": "bold",
                    "formatter": f"{{c}}{unit}",
                },
            }
        ],
    }


def svg_value_chain(
    layers: Sequence[Dict[str, Any]],
    theme: Optional[Dict[str, str]] = None,
    width: int = 1170,
    height: int = 410,
) -> str:
    """
    Render a clean multi-layer Industry Value Chain / Ecosystem SVG diagram.
    Each layer: {"title": "上游·数据源", "tag": "基础层", "nodes": ["节点1", "节点2", ...], "accent": optional hex}
    """
    th = resolve_theme(theme)
    n = max(1, len(layers))
    gap = 18
    row_h = int((height - gap * (n - 1)) / n)
    palette = [th["primary"], th["secondary"], th["accent"], th["positive"], th["highlight"]]

    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">',
        '<defs><filter id="vcShadow" x="-2%" y="-4%" width="104%" height="114%">'
        '<feDropShadow dx="0" dy="2" stdDeviation="3" flood-color="#0F172A" flood-opacity="0.06"/></filter></defs>',
    ]
    left_w = 210
    for idx, layer in enumerate(layers):
        y = idx * (row_h + gap)
        col = layer.get("accent") or palette[idx % len(palette)]
        title = _esc(layer.get("title", ""))
        tag = _esc(layer.get("tag", ""))
        nodes = list(layer.get("nodes", []))
        parts.append(
            f'<rect x="2" y="{y}" width="{width - 4}" height="{row_h}" rx="12" '
            f'fill="{th["surface"]}" stroke="{th["border"]}" stroke-width="1.2" filter="url(#vcShadow)"/>'
        )
        parts.append(
            f'<rect x="2" y="{y}" width="{left_w}" height="{row_h}" rx="12" fill="{col}"/>'
        )
        parts.append(f'<rect x="{left_w - 12}" y="{y}" width="14" height="{row_h}" fill="{col}"/>')
        cy = y + row_h // 2
        if tag:
            parts.append(
                f'<text x="20" y="{cy - 10}" fill="#E2E8F0" font-size="12" font-family="Noto Sans CJK SC">{tag}</text>'
            )
            parts.append(
                f'<text x="20" y="{cy + 14}" fill="#FFFFFF" font-size="16" font-weight="bold" font-family="Noto Sans CJK SC">{title}</text>'
            )
        else:
            parts.append(
                f'<text x="20" y="{cy + 6}" fill="#FFFFFF" font-size="16" font-weight="bold" font-family="Noto Sans CJK SC">{title}</text>'
            )

        if nodes:
            area_x = left_w + 18
            area_w = width - area_x - 18
            node_gap = 12
            node_w = int((area_w - node_gap * (len(nodes) - 1)) / len(nodes))
            node_h = min(56, row_h - 24)
            node_y = y + (row_h - node_h) // 2
            for ni, node in enumerate(nodes):
                nx = area_x + ni * (node_w + node_gap)
                parts.append(
                    f'<rect x="{nx}" y="{node_y}" width="{node_w}" height="{node_h}" rx="8" '
                    f'fill="{th["canvas"]}" stroke="{col}" stroke-opacity="0.35" stroke-width="1"/>'
                )
                parts.append(
                    f'<text x="{nx + node_w // 2}" y="{node_y + node_h // 2 + 5}" text-anchor="middle" '
                    f'fill="{th["ink"]}" font-size="13" font-weight="600" font-family="Noto Sans CJK SC">{_esc(node)}</text>'
                )
        if idx < n - 1:
            ay = y + row_h + 2
            parts.append(
                f'<polygon points="{left_w // 2 - 8},{ay} {left_w // 2 + 8},{ay} {left_w // 2},{ay + gap - 4}" fill="{th["muted"]}"/>'
            )

    parts.append("</svg>")
    return "".join(parts)


def _harvey_svg(score: int, color: str, size: int = 18) -> str:
    """Render a crisp 0..4 Harvey Ball SVG (0=empty, 1=quarter, 2=half, 3=three-quarters, 4=full)."""
    s = max(0, min(4, int(score)))
    r = size / 2 - 1.5
    cx = cy = size / 2
    fill_path = ""
    if s == 4:
        fill_path = f'<circle cx="{cx}" cy="{cy}" r="{r}" fill="{color}"/>'
    elif s == 3:
        fill_path = f'<path d="M {cx} {cy} L {cx} {cy - r} A {r} {r} 0 1 1 {cx - r} {cy} Z" fill="{color}"/>'
    elif s == 2:
        fill_path = f'<path d="M {cx} {cy} L {cx} {cy - r} A {r} {r} 0 0 1 {cx} {cy + r} Z" fill="{color}"/>'
    elif s == 1:
        fill_path = f'<path d="M {cx} {cy} L {cx} {cy - r} A {r} {r} 0 0 1 {cx + r} {cy} Z" fill="{color}"/>'
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{size}" height="{size}" viewBox="0 0 {size} {size}" '
        f'style="display:inline-block;vertical-align:middle;">'
        f'<circle cx="{cx}" cy="{cy}" r="{r}" fill="#FFFFFF" stroke="{color}" stroke-width="1.5"/>'
        f"{fill_path}</svg>"
    )


def html_harvey_scorecard(
    headers: Sequence[str],
    rows: Sequence[Dict[str, Any]],
    theme: Optional[Dict[str, str]] = None,
) -> str:
    """
    Build a sleek consulting Harvey Balls Capability Matrix (div/grid based, not rigid gray table).
    - `headers`: ["评估维度", "方案A", "方案B", "核心依据"]
    - `rows`: [{"dim": "社媒覆盖", "scores": [4, 2], "note": "覆盖全网10+平台"}]
    """
    th = resolve_theme(theme)
    n_scores = max(1, len(headers) - 2)
    cols = f"1.35fr {' '.join(['0.75fr'] * n_scores)} 2.1fr"

    html_parts = [
        f'<div style="background:{th["surface"]};border:1px solid {th["border"]};border-radius:12px;'
        f'overflow:hidden;box-shadow:0 4px 14px rgba(15,23,42,0.05);">',
        f'<div style="display:grid;grid-template-columns:{cols};background:{th["primary"]};color:#FFFFFF;'
        f'padding:11px 16px;font-size:13px;font-weight:700;align-items:center;">',
    ]
    for hi, h in enumerate(headers):
        align = "center" if 0 < hi <= n_scores else "left"
        html_parts.append(f'<div style="text-align:{align};">{_esc(h)}</div>')
    html_parts.append("</div>")

    for ri, row in enumerate(rows):
        bg = th["surface"] if ri % 2 == 0 else th["canvas"]
        html_parts.append(
            f'<div style="display:grid;grid-template-columns:{cols};background:{bg};padding:11px 16px;'
            f'border-top:1px solid {th["border"]};align-items:center;font-size:13.5px;color:{th["ink"]};">'
        )
        html_parts.append(f'<div style="font-weight:700;">{_esc(row.get("dim", ""))}</div>')
        scores = list(row.get("scores", []))
        for si in range(n_scores):
            sc = scores[si] if si < len(scores) else 0
            ball_color = th["secondary"] if si == 0 else th["muted"]
            html_parts.append(f'<div style="text-align:center;">{_harvey_svg(sc, ball_color, 18)}</div>')
        html_parts.append(f'<div style="color:{th["subtext"]};font-size:13px;">{_esc(row.get("note", ""))}</div>')
        html_parts.append("</div>")
    html_parts.append("</div>")
    return "".join(html_parts)
