#!/usr/bin/env python3
"""
Convert PNG images to SVG by embedding them as base64 data.
Note: This creates SVG wrappers, not true vector graphics.
For best results, export from original design files (Figma/Sketch/Draw.io).
"""

import base64
import os
import sys
from pathlib import Path

def png_to_svg_wrapper(png_path: str, svg_path: str):
    """
    Convert PNG to SVG by embedding as base64 data URI.
    This allows scaling but doesn't reduce file size.
    """
    # Read PNG file
    with open(png_path, 'rb') as f:
        png_data = f.read()

    # Encode to base64
    base64_data = base64.b64encode(png_data).decode('utf-8')

    # Get PNG dimensions using basic PNG header parsing
    # PNG signature: 89 50 4E 47 0D 0A 1A 0A
    # IHDR chunk starts at byte 16
    width = int.from_bytes(png_data[16:20], 'big')
    height = int.from_bytes(png_data[20:24], 'big')

    # Create SVG wrapper
    svg_content = f'''<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"
     width="{width}" height="{height}" viewBox="0 0 {width} {height}">
  <image width="{width}" height="{height}"
         xlink:href="data:image/png;base64,{base64_data}"/>
</svg>
'''

    # Write SVG file
    with open(svg_path, 'w', encoding='utf-8') as f:
        f.write(svg_content)

    # Report file sizes
    png_size = len(png_data) / 1024 / 1024  # MB
    svg_size = len(svg_content.encode('utf-8')) / 1024 / 1024  # MB

    print(f"  ✓ {os.path.basename(png_path)}")
    print(f"    PNG: {png_size:.2f} MB → SVG: {svg_size:.2f} MB ({(svg_size/png_size*100):.0f}%)")

    return width, height

def main():
    script_dir = Path(__file__).parent
    images_dir = script_dir.parent / "images" / "architecture"

    # Files to convert
    files_to_convert = [
        "cluster-routing",
        "data-pipeline",
        "multi-flow",
        "RDB+Journal-data-flow"
    ]

    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print("🔄 Converting PNG to SVG (embedded base64)")
    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print("")
    print("⚠️  Note: These are SVG wrappers around PNG data.")
    print("   For true vector graphics, export from original design files.")
    print("")

    for filename in files_to_convert:
        png_path = images_dir / f"{filename}.png"
        svg_path = images_dir / f"{filename}.svg"

        if not png_path.exists():
            print(f"  ✗ {filename}.png not found")
            continue

        try:
            width, height = png_to_svg_wrapper(str(png_path), str(svg_path))
            print(f"    Dimensions: {width} × {height}")
            print("")
        except Exception as e:
            print(f"  ✗ Error converting {filename}: {e}")

    print("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print("✅ Conversion complete!")
    print("")
    print("⚠️  Recommendation:")
    print("   If you have original design files (Figma/Sketch/Draw.io),")
    print("   re-export them as SVG for smaller file sizes and true scaling.")
    print("")

if __name__ == "__main__":
    main()
