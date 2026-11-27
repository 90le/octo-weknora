#!/usr/bin/env python3
"""
render_pptx_qa.py — Render a real .pptx via LibreOffice Impress + pdftoppm into
a single high-clarity contact sheet PNG for visual QA inspection.

Usage:
    python3 render_pptx_qa.py /workspace/output/report.pptx /workspace/tmp/pptx_qa_grid.png
"""

import math
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont


def load_label_font(size: int = 20):
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


def render_pptx_to_grid(pptx_path: Path, out_grid_path: Path, dpi: int = 130, cols: int = 2) -> int:
    if not pptx_path.is_file():
        raise FileNotFoundError(f"PPTX not found: {pptx_path}")
    if pptx_path.stat().st_size == 0:
        raise ValueError(f"PPTX is empty (0 bytes): {pptx_path}")

    out_grid_path.parent.mkdir(parents=True, exist_ok=True)

    with tempfile.TemporaryDirectory(prefix="pptx_qa_") as tmpdir:
        tmp = Path(tmpdir)
        profile_dir = tmp / "lo_profile"
        profile_dir.mkdir(exist_ok=True)

        # Convert PPTX -> PDF via LibreOffice headless with isolated user profile
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

        # Render PDF pages -> PNGs via pdftoppm
        prefix = tmp / "slide"
        cmd_ppm = ["pdftoppm", "-png", "-r", str(dpi), str(pdf_path), str(prefix)]
        res_ppm = subprocess.run(cmd_ppm, capture_output=True, text=True, timeout=180)
        if res_ppm.returncode != 0:
            raise RuntimeError(f"pdftoppm failed: {res_ppm.stderr or res_ppm.stdout}")

        slide_pngs = sorted(tmp.glob("slide-*.png"))
        if not slide_pngs:
            raise RuntimeError("pdftoppm produced no PNG pages")

        # Also save individual slides next to grid in a subfolder if needed for zooming
        pages_dir = out_grid_path.parent / (out_grid_path.stem + "_pages")
        if pages_dir.exists():
            shutil.rmtree(pages_dir)
        pages_dir.mkdir(parents=True, exist_ok=True)
        for idx, png in enumerate(slide_pngs, start=1):
            shutil.copy2(png, pages_dir / f"slide_{idx:02d}.png")

        # Build crisp contact sheet
        thumbs = []
        target_w = 760
        for png in slide_pngs:
            with Image.open(png) as im:
                im = im.convert("RGB")
                scale = target_w / im.width
                target_h = int(round(im.height * scale))
                thumbs.append(im.resize((target_w, target_h), Image.Resampling.LANCZOS))

        cols = max(1, min(cols, len(thumbs)))
        rows = math.ceil(len(thumbs) / cols)
        pad = 24
        header_h = 34
        cell_w = target_w
        cell_h = max(t.height for t in thumbs) + header_h

        grid_w = pad + cols * (cell_w + pad)
        grid_h = pad + rows * (cell_h + pad)
        canvas = Image.new("RGB", (grid_w, grid_h), (241, 245, 249))
        draw = ImageDraw.Draw(canvas)
        font = load_label_font(18)

        for idx, thumb in enumerate(thumbs):
            r = idx // cols
            c = idx % cols
            x = pad + c * (cell_w + pad)
            y = pad + r * (cell_h + pad)
            draw.text((x + 4, y + 6), f"Slide {idx + 1}", fill=(30, 41, 59), font=font)
            # Subtle border around slide preview
            draw.rectangle(
                [x - 1, y + header_h - 1, x + thumb.width + 1, y + header_h + thumb.height + 1],
                outline=(203, 213, 225),
                width=1,
            )
            canvas.paste(thumb, (x, y + header_h))

        canvas.save(out_grid_path, format="PNG", optimize=True)
        return len(thumbs)


def main():
    if len(sys.argv) < 2:
        print("Usage: python3 render_pptx_qa.py <input.pptx> [output_grid.png]", file=sys.stderr)
        sys.exit(2)
    pptx_path = Path(sys.argv[1]).resolve()
    out_grid = Path(sys.argv[2]).resolve() if len(sys.argv) >= 3 else Path("/workspace/tmp/pptx_qa_grid.png")
    count = render_pptx_to_grid(pptx_path, out_grid)
    print(f"QA_RENDER_OK slides={count} grid={out_grid} size_bytes={out_grid.stat().st_size}")


if __name__ == "__main__":
    main()
