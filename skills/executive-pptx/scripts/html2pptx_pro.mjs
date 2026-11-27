#!/usr/bin/env node
/**
 * html2pptx_pro.mjs
 *
 * Compiles a 16:9 (1280x720) multi-slide HTML/CSS presentation into a
 * natively editable PowerPoint (.pptx) file using headless Chromium for
 * pixel-accurate layout measurement, hard overflow/overlap QA auditing,
 * 3x retina rendering for ECharts/Canvas/SVG visuals, and pptxgenjs for
 * native text boxes, vector cards, editable tables, native charts, and notes.
 *
 * Usage:
 *   node html2pptx_pro.mjs <input.html> <output.pptx> [--allow-overflow]
 */

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createRequire } from 'node:module';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const runtimeDir = process.env.PRO_PPTX_RUNTIME || '/opt/pro-pptx-runtime';
const require = createRequire(path.join(runtimeDir, 'package.json'));

let playwright;
let PptxGenJS;
try {
  playwright = require('playwright');
  const pptxMod = require('pptxgenjs');
  PptxGenJS = pptxMod.default || pptxMod;
} catch (err) {
  console.error(`[html2pptx_pro] Failed to load runtime dependencies from ${runtimeDir}:`, err.message);
  process.exit(1);
}

const SLIDE_W_PX = 1280;
const SLIDE_H_PX = 720;
const SLIDE_W_IN = 10.0;
const SLIDE_H_IN = 5.625;
const PX_TO_IN = SLIDE_W_IN / SLIDE_W_PX; // 10 / 1280 = 0.0078125 in/px
// 1 inch = 72 pt -> 1 px on a 1280px = 10in canvas MUST equal (10/1280)*72 = 0.5625 pt
const PX_TO_PT = PX_TO_IN * 72;

function parseArgs(argv) {
  const positional = [];
  let allowOverflow = false;
  for (const arg of argv.slice(2)) {
    if (arg === '--allow-overflow') allowOverflow = true;
    else positional.push(arg);
  }
  if (positional.length < 2) {
    console.error('Usage: node html2pptx_pro.mjs <input.html> <output.pptx> [--allow-overflow]');
    process.exit(2);
  }
  return {
    inputHtml: path.resolve(positional[0]),
    outputPptx: path.resolve(positional[1]),
    allowOverflow,
  };
}

function rgbaToHexAndAlpha(cssColor) {
  if (!cssColor || cssColor === 'transparent' || cssColor === 'none') {
    return { hex: null, alpha: 0 };
  }
  const m = cssColor.match(/rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)/i);
  if (!m) return { hex: null, alpha: 0 };
  const r = Math.max(0, Math.min(255, Math.round(Number(m[1]))));
  const g = Math.max(0, Math.min(255, Math.round(Number(m[2]))));
  const b = Math.max(0, Math.min(255, Math.round(Number(m[3]))));
  let a = 1;
  if (m[4] !== undefined) {
    a = m[4].endsWith('%') ? parseFloat(m[4]) / 100 : parseFloat(m[4]);
  }
  const hex = [r, g, b].map((v) => v.toString(16).padStart(2, '0')).join('').toUpperCase();
  return { hex, alpha: isNaN(a) ? 1 : a };
}

function normalizeFontFace(fontFamily) {
  if (!fontFamily) return 'Noto Sans CJK SC';
  const lower = fontFamily.toLowerCase();
  if (lower.includes('serif') && !lower.includes('sans')) {
    return 'Noto Serif CJK SC';
  }
  if (lower.includes('mono') || lower.includes('consolas') || lower.includes('courier')) {
    return 'DejaVu Sans Mono';
  }
  return 'Noto Sans CJK SC';
}

function mapAlign(textAlign) {
  if (textAlign === 'center') return 'center';
  if (textAlign === 'right' || textAlign === 'end') return 'right';
  if (textAlign === 'justify') return 'justify';
  return 'left';
}

function loadIconsMap() {
  const iconPath = path.resolve(__dirname, '..', 'assets', 'lucide-icons.json');
  if (fs.existsSync(iconPath)) {
    try {
      return JSON.parse(fs.readFileSync(iconPath, 'utf8'));
    } catch {
      return {};
    }
  }
  return {};
}

async function main() {
  const { inputHtml, outputPptx, allowOverflow } = parseArgs(process.argv);
  if (!fs.existsSync(inputHtml)) {
    console.error(`[html2pptx_pro] Input HTML not found: ${inputHtml}`);
    process.exit(1);
  }
  fs.mkdirSync(path.dirname(outputPptx), { recursive: true });

  const echartsPath = path.join(runtimeDir, 'node_modules', 'echarts', 'dist', 'echarts.min.js');
  const hasEcharts = fs.existsSync(echartsPath);
  const iconsMap = loadIconsMap();

  const browser = await playwright.chromium.launch({
    headless: true,
    args: [
      '--no-sandbox',
      '--disable-setuid-sandbox',
      '--disable-dev-shm-usage',
      '--disable-gpu',
      '--font-render-hinting=none',
    ],
  });

  try {
    const context = await browser.newContext({
      viewport: { width: SLIDE_W_PX, height: SLIDE_H_PX },
      deviceScaleFactor: 3,
    });
    // Pre-inject offline ECharts BEFORE page load so imperative <script> echarts.init() works too
    if (hasEcharts) {
      await context.addInitScript({ path: echartsPath });
    }
    const page = await context.newPage();

    await page.goto(pathToFileURL(inputHtml).href, { waitUntil: 'load', timeout: 60000 });

    // Fallback injection if init script didn't bind window.echarts
    if (hasEcharts) {
      const hasWinEcharts = await page.evaluate(() => Boolean(window.echarts));
      if (!hasWinEcharts) {
        await page.addScriptTag({ path: echartsPath });
      }
    }

    // Hydrate offline icons (<i data-icon="name">) and declarative charts (<div data-echarts='...'>)
    const hydrateErrors = await page.evaluate((icons) => {
      const errs = [];
      // 1. Hide speaker note child elements visually (never hide a slide itself if it carries data-notes="...")
      document.querySelectorAll('aside.notes, [data-notes]:not(.slide):not(section)').forEach((el) => {
        el.style.display = 'none';
      });

      // 2. Hydrate data-icon placeholders with offline SVG icons
      document.querySelectorAll('[data-icon]').forEach((el) => {
        const key = (el.getAttribute('data-icon') || '').trim();
        const svgStr = icons[key];
        if (!svgStr) return;
        const wrapper = document.createElement('span');
        wrapper.innerHTML = svgStr.trim();
        const svg = wrapper.firstElementChild;
        if (!svg) return;
        const cs = getComputedStyle(el);
        const w = parseFloat(cs.width) || 20;
        const h = parseFloat(cs.height) || 20;
        svg.setAttribute('width', String(w));
        svg.setAttribute('height', String(h));
        svg.style.width = `${w}px`;
        svg.style.height = `${h}px`;
        svg.style.color = el.getAttribute('data-icon-color') || cs.color || 'currentColor';
        svg.style.display = 'block';
        el.innerHTML = '';
        el.appendChild(svg);
      });

      // 3. Normalize slide wrappers to exact 1280x720 BEFORE ECharts init so containers have exact dimensions
      const slides = document.querySelectorAll('section.slide, .slide');
      slides.forEach((s) => {
        s.style.width = '1280px';
        s.style.height = '720px';
        s.style.minWidth = '1280px';
        s.style.minHeight = '720px';
        s.style.maxWidth = '1280px';
        s.style.maxHeight = '720px';
        s.style.position = 'relative';
        s.style.boxSizing = 'border-box';
        s.style.flexShrink = '0';
      });

      // 4. Hydrate declarative [data-echarts] containers (with animation: false)
      if (window.echarts) {
        document.querySelectorAll('[data-echarts]').forEach((el) => {
          if (el.dataset.echartsInitialized === '1') return;
          try {
            const raw = el.getAttribute('data-echarts') || '{}';
            const opt = JSON.parse(raw);
            opt.animation = false;
            if (!opt.textStyle) opt.textStyle = {};
            if (!opt.textStyle.fontFamily) opt.textStyle.fontFamily = 'Noto Sans CJK SC, sans-serif';
            const rect = el.getBoundingClientRect();
            const w = Math.max(200, Math.round(rect.width) || parseInt(el.style.width, 10) || 600);
            const h = Math.max(150, Math.round(rect.height) || parseInt(el.style.height, 10) || 360);
            const chart = window.echarts.init(el, null, { renderer: 'canvas', width: w, height: h });
            chart.setOption(opt);
            el.dataset.echartsInitialized = '1';
          } catch (e) {
            errs.push(`ECharts init failed: ${String(e)}`);
            el.dataset.echartsError = String(e);
          }
        });
      } else if (document.querySelector('[data-echarts]')) {
        errs.push('window.echarts is unavailable');
      }
      return errs;
    }, iconsMap);
    if (hydrateErrors.length > 0) {
      for (const e of hydrateErrors) console.warn('[html2pptx_pro] Warn:', e);
    }

    // Wait for fonts & any async chart renders
    await page.evaluate(async () => {
      if (document.fonts && document.fonts.ready) {
        await document.fonts.ready;
      }
      await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    });
    await page.waitForTimeout(250);

    // Extract structured geometry + audit every slide in one browser evaluate pass
    const extracted = await page.evaluate(() => {
      const slideNodes = Array.from(document.querySelectorAll('section.slide'));
      const targets = slideNodes.length ? slideNodes : Array.from(document.querySelectorAll('.slide'));
      const results = [];

      const isVisible = (el) => {
        const cs = getComputedStyle(el);
        if (cs.display === 'none' || cs.visibility === 'hidden' || parseFloat(cs.opacity) <= 0.01) return false;
        const r = el.getBoundingClientRect();
        return r.width > 0.5 && r.height > 0.5;
      };

      const hasOwnDirectText = (el) => {
        for (const n of el.childNodes) {
          if (n.nodeType === Node.TEXT_NODE && n.textContent.trim().length > 0) return true;
        }
        return false;
      };

      const isInlineTag = (tag) =>
        ['SPAN', 'STRONG', 'B', 'EM', 'I', 'CODE', 'A', 'SMALL', 'MARK', 'SUB', 'SUP', 'BR', 'U'].includes(tag);

      const isTextLeafBlock = (el) => {
        if (!el || el.nodeType !== Node.ELEMENT_NODE) return false;
        const tag = (el.tagName || '').toUpperCase();
        if (['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'ASIDE', 'TABLE', 'SVG', 'CANVAS', 'IMG'].includes(tag)) {
          return false;
        }
        if (el.hasAttribute('data-echarts') || el.hasAttribute('data-rasterize') || el.hasAttribute('data-pptx-chart') || el.hasAttribute('data-icon')) {
          return false;
        }
        const text = (el.innerText || '').trim();
        if (!text) return false;
        const children = Array.from(el.children).filter((c) => isVisible(c));
        if (children.length === 0) return true;
        // Flex/grid containers with multiple children position each child separately (e.g. space-between footers)
        const cs = getComputedStyle(el);
        if ((cs.display === 'flex' || cs.display === 'grid') && children.length > 1) {
          return false;
        }
        // If all visible children are inline tags (and none are svg/img/canvas/icon), treat as one rich-text block
        const allInline = children.every(
          (c) =>
            isInlineTag(c.tagName) &&
            !c.hasAttribute('data-icon') &&
            !c.querySelector('svg, img, canvas')
        );
        return allInline;
      };

      const collectRuns = (node, inheritedStyle, runs) => {
        if (node.nodeType === Node.TEXT_NODE) {
          const raw = node.textContent.replace(/\s+/g, ' ');
          if (!raw.trim() && runs.length === 0) return;
          if (raw) {
            runs.push({
              text: raw,
              fontSizePx: inheritedStyle.fontSizePx,
              bold: inheritedStyle.bold,
              italic: inheritedStyle.italic,
              color: inheritedStyle.color,
              fontFamily: inheritedStyle.fontFamily,
            });
          }
          return;
        }
        if (node.nodeType !== Node.ELEMENT_NODE) return;
        if (node.tagName === 'BR') {
          runs.push({ breakLine: true });
          return;
        }
        const cs = getComputedStyle(node);
        if (cs.display === 'none' || cs.visibility === 'hidden') return;
        const fw = parseInt(cs.fontWeight, 10);
        const cur = {
          fontSizePx: parseFloat(cs.fontSize) || inheritedStyle.fontSizePx,
          bold: (!isNaN(fw) && fw >= 600) || cs.fontWeight === 'bold',
          italic: cs.fontStyle === 'italic',
          color: cs.color || inheritedStyle.color,
          fontFamily: cs.fontFamily || inheritedStyle.fontFamily,
        };
        for (const child of node.childNodes) {
          collectRuns(child, cur, runs);
        }
      };

      targets.forEach((slideEl, slideIdx) => {
        const sRect = slideEl.getBoundingClientRect();
        const sStyle = getComputedStyle(slideEl);
        const issues = [];

        // Check slide-level overflow
        if (slideEl.scrollHeight > slideEl.clientHeight + 4) {
          issues.push(
            `Slide ${slideIdx + 1}: slide overflows vertically by ${Math.round(slideEl.scrollHeight - slideEl.clientHeight)}px (height=${slideEl.scrollHeight}px > 720px)`
          );
        }
        if (slideEl.scrollWidth > slideEl.clientWidth + 4) {
          issues.push(
            `Slide ${slideIdx + 1}: slide overflows horizontally by ${Math.round(slideEl.scrollWidth - slideEl.clientWidth)}px (width=${slideEl.scrollWidth}px > 1280px)`
          );
        }

        // Speaker notes (supports both <section class="slide" data-notes="..."> and inner <aside class="notes">)
        const attrNote = (slideEl.getAttribute('data-notes') || '').trim();
        const noteEls = Array.from(slideEl.querySelectorAll('aside.notes, [data-notes]'));
        const notes = [attrNote, ...noteEls.map((n) => (n.textContent || '').trim())].filter(Boolean).join('\n\n');

        const bgImage = sStyle.backgroundImage && sStyle.backgroundImage !== 'none' ? sStyle.backgroundImage : '';
        const bgColor = sStyle.backgroundColor || 'rgb(255, 255, 255)';

        const elements = [];
        let captureCounter = 0;

        const walk = (el) => {
          if (!el || el.nodeType !== Node.ELEMENT_NODE) return;
          if (el.tagName === 'ASIDE' && el.classList.contains('notes')) return;
          if (el !== slideEl && el.hasAttribute('data-notes')) return;
          if (['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE'].includes(el.tagName)) return;
          if (!isVisible(el)) return;

          const cs = getComputedStyle(el);
          const r = el.getBoundingClientRect();
          const relX = r.left - sRect.left;
          const relY = r.top - sRect.top;
          const relW = r.width;
          const relH = r.height;

          // Overflow / out-of-bounds audit for non-root elements
          if (el !== slideEl) {
            if (relX + relW > 1286) {
              issues.push(
                `Slide ${slideIdx + 1}: element <${el.tagName.toLowerCase()}> "${(el.innerText || '').trim().slice(0, 24)}" extends past right edge (right=${Math.round(relX + relW)}px > 1280px)`
              );
            }
            if (relY + relH > 726) {
              issues.push(
                `Slide ${slideIdx + 1}: element <${el.tagName.toLowerCase()}> "${(el.innerText || '').trim().slice(0, 24)}" extends past bottom edge (bottom=${Math.round(relY + relH)}px > 720px)`
              );
            }
            if (
              (cs.overflowY === 'hidden' || cs.overflowY === 'clip') &&
              el.scrollHeight > el.clientHeight + 6 &&
              (el.innerText || '').trim().length > 0
            ) {
              issues.push(
                `Slide ${slideIdx + 1}: clipped text inside <${el.tagName.toLowerCase()}> "${(el.innerText || '').trim().slice(0, 24)}" (overflowY=${Math.round(el.scrollHeight - el.clientHeight)}px)`
              );
            }
          }

          // 1. Native PPT Chart block
          if (el.hasAttribute('data-pptx-chart')) {
            try {
              const spec = JSON.parse(el.getAttribute('data-pptx-chart') || '{}');
              elements.push({
                kind: 'chart',
                x: relX,
                y: relY,
                w: relW,
                h: relH,
                spec,
              });
              return;
            } catch {
              // Fall through to rasterize if JSON malformed
            }
          }

          // 2. Rasterized visual blocks (ECharts, Canvas, SVG, IMG, [data-rasterize])
          const upperTag = (el.tagName || '').toUpperCase();
          const isVisual =
            upperTag === 'CANVAS' ||
            upperTag === 'SVG' ||
            upperTag === 'IMG' ||
            el.hasAttribute('data-echarts') ||
            el.hasAttribute('data-rasterize');
          if (isVisual) {
            const captureId = `s${slideIdx}_c${captureCounter++}`;
            el.setAttribute('data-pptx-capture-id', captureId);
            elements.push({
              kind: 'image',
              captureId,
              x: relX,
              y: relY,
              w: relW,
              h: relH,
            });
            return;
          }

          // 3. Native Table
          if (el.tagName === 'TABLE') {
            const rows = [];
            const trs = Array.from(el.querySelectorAll('tr'));
            const colWidthsPx = [];
            if (trs.length > 0) {
              const firstCells = Array.from(trs[0].children).filter((c) => c.tagName === 'TH' || c.tagName === 'TD');
              firstCells.forEach((c) => colWidthsPx.push(c.getBoundingClientRect().width));
            }
            trs.forEach((tr) => {
              const rowCells = [];
              const cells = Array.from(tr.children).filter((c) => c.tagName === 'TH' || c.tagName === 'TD');
              cells.forEach((cell) => {
                const ccs = getComputedStyle(cell);
                const fw = parseInt(ccs.fontWeight, 10);
                rowCells.push({
                  text: (cell.innerText || '').trim(),
                  bgColor: ccs.backgroundColor,
                  color: ccs.color,
                  fontSizePx: parseFloat(ccs.fontSize) || 13,
                  bold: cell.tagName === 'TH' || (!isNaN(fw) && fw >= 600),
                  align: ccs.textAlign,
                  borderColor: ccs.borderBottomColor || ccs.borderColor,
                  colSpan: cell.colSpan || 1,
                  rowSpan: cell.rowSpan || 1,
                  fontFamily: ccs.fontFamily,
                });
              });
              if (rowCells.length > 0) rows.push(rowCells);
            });
            elements.push({
              kind: 'table',
              x: relX,
              y: relY,
              w: relW,
              h: relH,
              colWidthsPx,
              rows,
            });
            return;
          }

          // 4. Container background / card / pill / accent border / divider
          if (el !== slideEl) {
            const hasBgGrad = cs.backgroundImage && cs.backgroundImage !== 'none';
            const bgC = cs.backgroundColor;
            const bwTop = parseFloat(cs.borderTopWidth) || 0;
            const bwRight = parseFloat(cs.borderRightWidth) || 0;
            const bwBottom = parseFloat(cs.borderBottomWidth) || 0;
            const bwLeft = parseFloat(cs.borderLeftWidth) || 0;
            const hasFullBorder = bwTop > 0 && bwRight > 0 && bwBottom > 0 && bwLeft > 0;
            const hasVisibleBg = bgC && bgC !== 'rgba(0, 0, 0, 0)' && bgC !== 'transparent';
            const radiusPx = parseFloat(cs.borderTopLeftRadius) || 0;
            const hasShadow = cs.boxShadow && cs.boxShadow !== 'none';

            if (hasBgGrad && !isTextLeafBlock(el) && el.children.length === 0) {
              const captureId = `s${slideIdx}_c${captureCounter++}`;
              el.setAttribute('data-pptx-capture-id', captureId);
              elements.push({ kind: 'image', captureId, x: relX, y: relY, w: relW, h: relH });
              return;
            }

            if (hasVisibleBg || hasFullBorder) {
              // If one accent side (top or left) is thicker/differently colored than the base border,
              // draw the card with the subtle base border (right/bottom) and emit the accent bar separately.
              const hasTopAccent = hasFullBorder && (bwTop >= bwBottom + 1.5 || (bwTop >= 3 && cs.borderTopColor !== cs.borderBottomColor));
              const hasLeftAccent = hasFullBorder && (bwLeft >= bwRight + 1.5 || (bwLeft >= 3 && cs.borderLeftColor !== cs.borderRightColor));
              const baseBW = (hasTopAccent || hasLeftAccent) ? Math.min(bwRight, bwBottom) : bwTop;
              const baseBC = (hasTopAccent || hasLeftAccent) ? cs.borderRightColor : cs.borderTopColor;
              elements.push({
                kind: 'box',
                x: relX,
                y: relY,
                w: relW,
                h: relH,
                bgColor: bgC,
                borderWidthPx: hasFullBorder ? baseBW : 0,
                borderColor: baseBC,
                radiusPx,
                hasShadow,
              });
              if (hasTopAccent) {
                elements.push({
                  kind: 'box',
                  x: relX,
                  y: relY,
                  w: relW,
                  h: Math.max(bwTop, 3),
                  bgColor: cs.borderTopColor,
                  borderWidthPx: 0,
                  borderColor: null,
                  radiusPx: 0,
                  hasShadow: false,
                });
              }
              if (hasLeftAccent) {
                elements.push({
                  kind: 'box',
                  x: relX,
                  y: relY,
                  w: Math.max(bwLeft, 3),
                  h: relH,
                  bgColor: cs.borderLeftColor,
                  borderWidthPx: 0,
                  borderColor: null,
                  radiusPx: 0,
                  hasShadow: false,
                });
              }
            }

            // Left accent bar (very popular in consulting callout cards)
            if (bwLeft >= 2 && !hasFullBorder) {
              elements.push({
                kind: 'box',
                x: relX,
                y: relY,
                w: Math.max(bwLeft, 3),
                h: relH,
                bgColor: cs.borderLeftColor,
                borderWidthPx: 0,
                borderColor: null,
                radiusPx: 0,
                hasShadow: false,
              });
            }
            // Top accent bar / horizontal divider
            if (bwTop >= 1 && !hasFullBorder && bwBottom === 0) {
              elements.push({
                kind: 'box',
                x: relX,
                y: relY,
                w: relW,
                h: Math.max(bwTop, 1.5),
                bgColor: cs.borderTopColor,
                borderWidthPx: 0,
                borderColor: null,
                radiusPx: 0,
                hasShadow: false,
              });
            }
            // Bottom divider line
            if (bwBottom >= 1 && !hasFullBorder && bwTop === 0) {
              elements.push({
                kind: 'box',
                x: relX,
                y: relY + relH - Math.max(bwBottom, 1.5),
                w: relW,
                h: Math.max(bwBottom, 1.5),
                bgColor: cs.borderBottomColor,
                borderWidthPx: 0,
                borderColor: null,
                radiusPx: 0,
                hasShadow: false,
              });
            }
          }

          // 5. Leaf rich-text block
          if (el !== slideEl && isTextLeafBlock(el)) {
            const padLeft = parseFloat(cs.paddingLeft) || 0;
            const padRight = parseFloat(cs.paddingRight) || 0;
            const padTop = parseFloat(cs.paddingTop) || 0;
            const padBottom = parseFloat(cs.paddingBottom) || 0;
            const fw = parseInt(cs.fontWeight, 10);
            const baseStyle = {
              fontSizePx: parseFloat(cs.fontSize) || 14,
              bold: (!isNaN(fw) && fw >= 600) || cs.fontWeight === 'bold',
              italic: cs.fontStyle === 'italic',
              color: cs.color || 'rgb(30, 41, 59)',
              fontFamily: cs.fontFamily,
            };
            const runs = [];
            collectRuns(el, baseStyle, runs);
            const cleanRuns = runs.filter((rn) => rn.breakLine || (rn.text && rn.text.length > 0));
            if (cleanRuns.length > 0) {
              // Trim leading/trailing whitespace on first/last run
              if (cleanRuns[0].text) cleanRuns[0].text = cleanRuns[0].text.replace(/^\s+/, '');
              const last = cleanRuns[cleanRuns.length - 1];
              if (last.text) last.text = last.text.replace(/\s+$/, '');
              const lhPx = parseFloat(cs.lineHeight);
              const lineMultiple = !isNaN(lhPx) && baseStyle.fontSizePx > 0 ? lhPx / baseStyle.fontSizePx : 1.25;
              const isFlexCenter =
                (cs.display === 'flex' || cs.display === 'inline-flex') && cs.alignItems === 'center';
              elements.push({
                kind: 'text',
                x: relX + padLeft,
                y: relY + padTop,
                w: Math.max(8, relW - padLeft - padRight),
                h: Math.max(10, relH - padTop - padBottom),
                align: cs.textAlign,
                valign: isFlexCenter ? 'middle' : 'top',
                lineMultiple: Math.max(1.0, Math.min(2.0, lineMultiple)),
                runs: cleanRuns,
              });
            }
            return;
          }

          // 6. Mixed container with direct text nodes alongside block children
          if (el !== slideEl && hasOwnDirectText(el)) {
            for (const n of el.childNodes) {
              if (n.nodeType === Node.TEXT_NODE && n.textContent.trim()) {
                const range = document.createRange();
                range.selectNodeContents(n);
                const tr = range.getBoundingClientRect();
                if (tr.width > 1 && tr.height > 1) {
                  const fw = parseInt(cs.fontWeight, 10);
                  elements.push({
                    kind: 'text',
                    x: tr.left - sRect.left,
                    y: tr.top - sRect.top,
                    w: tr.width,
                    h: tr.height,
                    align: cs.textAlign,
                    valign: 'top',
                    lineMultiple: 1.25,
                    runs: [
                      {
                        text: n.textContent.trim(),
                        fontSizePx: parseFloat(cs.fontSize) || 14,
                        bold: (!isNaN(fw) && fw >= 600) || cs.fontWeight === 'bold',
                        italic: cs.fontStyle === 'italic',
                        color: cs.color || 'rgb(30, 41, 59)',
                        fontFamily: cs.fontFamily,
                      },
                    ],
                  });
                }
              }
            }
          }

          for (const child of el.children) {
            walk(child);
          }
        };

        walk(slideEl);

        // Text-block overlap audit
        const textBoxes = elements.filter((e) => e.kind === 'text');
        for (let i = 0; i < textBoxes.length; i++) {
          for (let j = i + 1; j < textBoxes.length; j++) {
            const a = textBoxes[i];
            const b = textBoxes[j];
            const ox = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x);
            const oy = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y);
            if (ox > 14 && oy > 8) {
              const ta = a.runs.map((r) => r.text || '').join('').slice(0, 18);
              const tb = b.runs.map((r) => r.text || '').join('').slice(0, 18);
              issues.push(
                `Slide ${slideIdx + 1}: text overlap (${Math.round(ox)}x${Math.round(oy)}px) between "${ta}" and "${tb}"`
              );
            }
          }
        }

        results.push({
          index: slideIdx,
          bgColor,
          hasBgImage: Boolean(bgImage),
          notes,
          elements,
          issues,
        });
      });

      return results;
    });

    if (!extracted.length) {
      console.error('[html2pptx_pro] No <section class="slide"> or .slide elements found in HTML.');
      process.exit(1);
    }

    const allIssues = extracted.flatMap((s) => s.issues);
    if (allIssues.length > 0) {
      console.error(`[html2pptx_pro] Layout QA found ${allIssues.length} issue(s):`);
      for (const issue of allIssues.slice(0, 20)) {
        console.error(`  - ${issue}`);
      }
      if (!allowOverflow) {
        console.error('[html2pptx_pro] Aborting PPTX export (--strict). Fix the CSS/copy overflow above and re-run.');
        process.exit(3);
      }
    }

    // Capture screenshots for gradient backgrounds and rasterized visuals (charts, SVGs, images)
    const capturedImages = new Map();
    const slideBgImages = new Map();

    const slideLocator = (await page.locator('section.slide').count()) > 0
      ? page.locator('section.slide')
      : page.locator('.slide');

    for (const slideData of extracted) {
      const sLoc = slideLocator.nth(slideData.index);
      await sLoc.scrollIntoViewIfNeeded();

      for (const el of slideData.elements) {
        if (el.kind === 'image' && el.captureId) {
          const loc = page.locator(`[data-pptx-capture-id="${el.captureId}"]`);
          try {
            const buf = await loc.screenshot({ omitBackground: true, animations: 'disabled' });
            capturedImages.set(el.captureId, `data:image/png;base64,${buf.toString('base64')}`);
          } catch (err) {
            console.warn(`[html2pptx_pro] Warn: screenshot failed for ${el.captureId}: ${err.message}`);
          }
        }
      }

      if (slideData.hasBgImage) {
        // Hide children temporarily to capture clean gradient/image slide background
        const bgBuf = await (async () => {
          await sLoc.evaluate((node) => {
            Array.from(node.children).forEach((c) => {
              c.dataset._prevVis = c.style.visibility || '';
              c.style.visibility = 'hidden';
            });
          });
          const buf = await sLoc.screenshot({ animations: 'disabled' });
          await sLoc.evaluate((node) => {
            Array.from(node.children).forEach((c) => {
              c.style.visibility = c.dataset._prevVis || '';
              delete c.dataset._prevVis;
            });
          });
          return buf;
        })();
        slideBgImages.set(slideData.index, `data:image/png;base64,${bgBuf.toString('base64')}`);
      }
    }

    // Compile into native editable PPTX via pptxgenjs
    const pptx = new PptxGenJS();
    pptx.layout = 'LAYOUT_16x9';
    pptx.author = 'WeKnora Executive PPTX';
    pptx.company = 'Yewen';

    for (const slideData of extracted) {
      const slide = pptx.addSlide();
      if (slideBgImages.has(slideData.index)) {
        slide.background = { data: slideBgImages.get(slideData.index) };
      } else {
        const bg = rgbaToHexAndAlpha(slideData.bgColor);
        slide.background = { color: bg.hex || 'FFFFFF' };
      }

      if (slideData.notes) {
        slide.addNotes(slideData.notes);
      }

      for (const el of slideData.elements) {
        const x = Math.max(0, el.x * PX_TO_IN);
        const y = Math.max(0, el.y * PX_TO_IN);
        const w = Math.max(0.02, el.w * PX_TO_IN);
        const h = Math.max(0.02, el.h * PX_TO_IN);

        if (el.kind === 'box') {
          const fill = rgbaToHexAndAlpha(el.bgColor);
          const border = rgbaToHexAndAlpha(el.borderColor);
          const shapeOpts = { x, y, w, h };
          if (fill.hex && fill.alpha > 0.01) {
            shapeOpts.fill = {
              color: fill.hex,
              transparency: Math.round((1 - fill.alpha) * 100),
            };
          } else {
            shapeOpts.fill = { type: 'none' };
          }
          if (el.borderWidthPx > 0 && border.hex && border.alpha > 0.01) {
            shapeOpts.line = {
              color: border.hex,
              pt: Math.max(0.5, el.borderWidthPx * PX_TO_PT),
              transparency: Math.round((1 - border.alpha) * 100),
            };
          } else {
            shapeOpts.line = { type: 'none' };
          }
          if (el.hasShadow) {
            shapeOpts.shadow = {
              type: 'outer',
              blur: 6,
              offset: 2,
              angle: 45,
              color: '000000',
              opacity: 0.08,
            };
          }
          if (el.radiusPx >= 2) {
            shapeOpts.rectRadius = Math.min(0.35, (el.radiusPx * PX_TO_IN) / Math.min(w, h));
            slide.addShape(pptx.ShapeType.roundRect, shapeOpts);
          } else {
            slide.addShape(pptx.ShapeType.rect, shapeOpts);
          }
        } else if (el.kind === 'image') {
          const dataUri = capturedImages.get(el.captureId);
          if (dataUri) {
            slide.addImage({ data: dataUri, x, y, w, h });
          }
        } else if (el.kind === 'chart') {
          const spec = el.spec || {};
          const chartTypeMap = {
            bar: pptx.ChartType.bar,
            line: pptx.ChartType.line,
            pie: pptx.ChartType.pie,
            doughnut: pptx.ChartType.doughnut,
            area: pptx.ChartType.area,
            radar: pptx.ChartType.radar,
          };
          const chartType = chartTypeMap[(spec.type || 'bar').toLowerCase()] || pptx.ChartType.bar;
          if (Array.isArray(spec.data) && spec.data.length > 0) {
            slide.addChart(chartType, spec.data, {
              x,
              y,
              w,
              h,
              showLegend: spec.showLegend !== false,
              showTitle: Boolean(spec.title),
              title: spec.title || '',
              chartColors: spec.colors || ['2563EB', '0EA5E9', '10B981', 'F59E0B', '8B5CF6'],
              ...(spec.options || {}),
            });
          }
        } else if (el.kind === 'table') {
          if (!el.rows || el.rows.length === 0) continue;
          const pptxRows = el.rows.map((row) =>
            row.map((cell) => {
              const fill = rgbaToHexAndAlpha(cell.bgColor);
              const fg = rgbaToHexAndAlpha(cell.color);
              const brd = rgbaToHexAndAlpha(cell.borderColor);
              const opts = {
                fontFace: normalizeFontFace(cell.fontFamily),
                fontSize: Math.max(9, Math.round(cell.fontSizePx * PX_TO_PT * 10) / 10),
                bold: Boolean(cell.bold),
                color: fg.hex || '1E293B',
                align: mapAlign(cell.align),
                valign: 'middle',
                margin: [4, 6, 4, 6],
                border: {
                  type: 'solid',
                  pt: 0.5,
                  color: brd.hex || 'E2E8F0',
                },
              };
              if (fill.hex && fill.alpha > 0.02) {
                opts.fill = { color: fill.hex };
              }
              if (cell.colSpan > 1) opts.colspan = cell.colSpan;
              if (cell.rowSpan > 1) opts.rowspan = cell.rowSpan;
              return { text: cell.text, options: opts };
            })
          );
          const colW = el.colWidthsPx.length
            ? el.colWidthsPx.map((cw) => Math.max(0.4, cw * PX_TO_IN))
            : undefined;
          slide.addTable(pptxRows, {
            x,
            y,
            w,
            colW,
            autoPage: false,
          });
        } else if (el.kind === 'text') {
          const textRuns = [];
          for (let ri = 0; ri < el.runs.length; ri++) {
            const rn = el.runs[ri];
            if (rn.breakLine) {
              if (textRuns.length > 0) {
                textRuns[textRuns.length - 1].options.breakLine = true;
              }
              continue;
            }
            if (!rn.text) continue;
            const fg = rgbaToHexAndAlpha(rn.color);
            textRuns.push({
              text: rn.text,
              options: {
                fontFace: normalizeFontFace(rn.fontFamily),
                fontSize: Math.max(8, Math.round(rn.fontSizePx * PX_TO_PT * 10) / 10),
                bold: Boolean(rn.bold),
                italic: Boolean(rn.italic),
                color: fg.hex || '1E293B',
              },
            });
          }
          if (textRuns.length === 0) continue;
          // Width cushion prevents PowerPoint/LibreOffice CJK font metric differences
          // from wrapping the final character onto a stray second line.
          const basePx = el.runs[0]?.fontSizePx || 14;
          const isSingleLine = el.h < basePx * 1.9;
          const wPad = isSingleLine ? 0.28 : 0.12;
          const safeW = Math.min(SLIDE_W_IN - x - 0.05, w + wPad);
          const safeH = Math.min(SLIDE_H_IN - y, h + 0.06);
          slide.addText(textRuns, {
            x,
            y,
            w: safeW,
            h: safeH,
            align: mapAlign(el.align),
            valign: el.valign || 'top',
            margin: 0,
            wrap: true,
            lineSpacingMultiple: el.lineMultiple || 1.2,
          });
        }
      }
    }

    await pptx.writeFile({ fileName: outputPptx });
    const stat = fs.statSync(outputPptx);
    console.log(
      `HTML2PPTX_PRO_OK slides=${extracted.length} output=${outputPptx} size_bytes=${stat.size} qa_issues=${allIssues.length}`
    );
  } finally {
    await browser.close();
  }
}

main().catch((err) => {
  console.error('[html2pptx_pro] Fatal error:', err);
  process.exit(1);
});
