#!/usr/bin/env python3
"""
render_pptx_qa.py — Render a real .pptx via LibreOffice Impress + pdftoppm into
compact, VLM-ready JPEG contact sheet(s) (<= ~240 KB per sheet, up to 8 slides
per sheet) for instant visual QA without manual slicing.

Usage:
    python3 render_pptx_qa.py /workspace/output/report.pptx /workspace/tmp/qa
    python3 render_pptx_qa.py /workspace/output/report.pptx /workspace/tmp/pptx_qa_grid.jpg
"""

import math
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont


def load_label_font(size: int = 18):
    candidates = [
        "/usr/share/fonts/opentype/noto/NotoSansCJK-Bold.ttc",
        "/usr/share/fonts/truetype/noto/NotoSansCJK-Bold.ttc",
        "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
    ]
    for p in candidates:
        if os.path.exists(p):
            try:
                return ImageFont.truetype(p, size)
            except Exception:
                continue
    return ImageFont.load_default()


def save_compact_image(canvas: Image.Image, out_path: Path, max_kb: int = 320):
    ext = out_path.suffix.lower()
    if ext == ".png":
        canvas.save(out_path, format="PNG", optimize=True)
        return
    # JPEG with adaptive quality so every sheet stays compact and super fast for VLM
    for q in (82, 76, 70, 64):
        canvas.save(out_path, format="JPEG", quality=q, optimize=True, progressive=True)
        if out_path.stat().st_size <= max_kb * 1024:
            return


def build_contact_sheet(slide_pngs, start_slide_num: int, cols: int = 2, target_w: int = 680) -> Image.Image:
    thumbs = []
    for png in slide_pngs:
        with Image.open(png) as im:
            im = im.convert("RGB")
            scale = target_w / im.width
            target_h = int(round(im.height * scale))
            thumbs.append(im.resize((target_w, target_h), Image.Resampling.LANCZOS))

    cols = max(1, min(cols, len(thumbs)))
    rows = math.ceil(len(thumbs) / cols)
    pad = 20
    header_h = 30
    cell_w = target_w
    cell_h = max(t.height for t in thumbs) + header_h

    grid_w = pad + cols * (cell_w + pad)
    grid_h = pad + rows * (cell_h + pad)
    canvas = Image.new("RGB", (grid_w, grid_h), (241, 245, 249))
    draw = ImageDraw.Draw(canvas)
    font = load_label_font(17)

    for idx, thumb in enumerate(thumbs):
        r = idx // cols
        c = idx % cols
        x = pad + c * (cell_w + pad)
        y = pad + r * (cell_h + pad)
        draw.text((x + 4, y + 5), f"Slide {start_slide_num + idx}", fill=(30, 41, 59), font=font)
        draw.rectangle(
            [x - 1, y + header_h - 1, x + thumb.width + 1, y + header_h + thumb.height + 1],
            outline=(203, 213, 225),
            width=1,
        )
        canvas.paste(thumb, (x, y + header_h))
    return canvas


def render_pptx_to_sheets(
    pptx_path: Path, out_target: Path, dpi: int = 120, cols: int = 2, per_sheet: int = 8
):
    if not pptx_path.is_file():
        raise FileNotFoundError(f"PPTX not found: {pptx_path}")
    if pptx_path.stat().st_size == 0:
        raise ValueError(f"PPTX is empty (0 bytes): {pptx_path}")

    with tempfile.TemporaryDirectory(prefix="pptx_qa_") as tmpdir:
        tmp = Path(tmpdir)
        profile_dir = tmp / "lo_profile"
        profile_dir.mkdir(exist_ok=True)

        cmd_lo = [
            "soffice",
            f"-env:UserInstallation=file://{profile_dir}",
            "--headless",
            "--norestore",
            "--nolockcheck",
            "--convert-to",
            "pdf",
            "--outdir",
            str(tmp),
            str(pptx_path),
        ]
        res_lo = subprocess.run(cmd_lo, capture_output=True, text=True, timeout=180)
        if res_lo.returncode != 0:
            raise RuntimeError(f"LibreOffice conversion failed: {res_lo.stderr or res_lo.stdout}")

        pdf_files = sorted(tmp.glob("*.pdf"))
        if not pdf_files:
            raise RuntimeError("LibreOffice produced no PDF output")
        pdf_path = pdf_files[0]

        prefix = tmp / "slide"
        cmd_ppm = ["pdftoppm", "-png", "-r", str(dpi), str(pdf_path), str(prefix)]
        res_ppm = subprocess.run(cmd_ppm, capture_output=True, text=True, timeout=180)
        if res_ppm.returncode != 0:
            raise RuntimeError(f"pdftoppm failed: {res_ppm.stderr or res_ppm.stdout}")

        slide_pngs = sorted(tmp.glob("slide-*.png"))
        if not slide_pngs:
            raise RuntimeError("pdftoppm produced no PNG pages")

        # Determine output directory & naming mode:
        # - If out_target has .png/.jpg/.jpeg suffix and slides <= per_sheet, write single file at out_target.
        # - Otherwise write compact JPEG sheets (qa_sheet_1.jpg, qa_sheet_2.jpg, ...) so every sheet is fast for VLM.
        has_img_ext = out_target.suffix.lower() in (".png", ".jpg", ".jpeg")
        sheets_written = []

        if has_img_ext and len(slide_pngs) <= per_sheet:
            out_target.parent.mkdir(parents=True, exist_ok=True)
            canvas = build_contact_sheet(slide_pngs, start_slide_num=1, cols=cols)
            save_compact_image(canvas, out_target)
            sheets_written.append((out_target, 1, len(slide_pngs)))
            pages_dir = out_target.parent / (out_target.stem + "_pages")
        else:
            out_dir = out_target.parent if has_img_ext else out_target
            stem = out_target.stem if has_img_ext else "qa_sheet"
            out_dir.mkdir(parents=True, exist_ok=True)
            for s_idx, offset in enumerate(range(0, len(slide_pngs), per_sheet), start=1):
                batch = slide_pngs[offset : offset + per_sheet]
                sheet_path = out_dir / f"{stem}_{s_idx}.jpg"
                canvas = build_contact_sheet(batch, start_slide_num=offset + 1, cols=cols)
                save_compact_image(canvas, sheet_path)
                sheets_written.append((sheet_path, offset + 1, offset + len(batch)))
            pages_dir = out_dir / f"{stem}_pages"

        if pages_dir.exists():
            shutil.rmtree(pages_dir)
        pages_dir.mkdir(parents=True, exist_ok=True)
        for idx, png in enumerate(slide_pngs, start=1):
            # Save compact per-slide JPEGs in case a single slide needs zoom inspection
            with Image.open(png) as im:
                im.convert("RGB").save(pages_dir / f"slide_{idx:02d}.jpg", format="JPEG", quality=85, optimize=True)

        return len(slide_pngs), sheets_written


def main():
    if len(sys.argv) < 2:
        print("Usage: python3 render_pptx_qa.py <input.pptx> [output_dir_or_file]", file=sys.stderr)
        sys.exit(2)
    pptx_path = Path(sys.argv[1]).resolve()
    out_target = Path(sys.argv[2]).resolve() if len(sys.argv) >= 3 else Path("/workspace/tmp/qa")
    total, sheets = render_pptx_to_sheets(pptx_path, out_target)
    parts = [f"{p} (P{a}-P{b}, {p.stat().st_size // 1024}KB)" for p, a, b in sheets]
    print(f"QA_RENDER_OK slides={total} sheets={len(sheets)} files=[{', '.join(parts)}]")


if __name__ == "__main__":
    main()
