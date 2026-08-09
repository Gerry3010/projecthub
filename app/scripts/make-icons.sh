#!/usr/bin/env bash
# Generate the macOS app icon (build-resources/icon.icns) and refresh icon.png
# from the single source of truth, web/icon.svg. Uses only tooling available on
# macOS (sips, iconutil) plus a Homebrew rasterizer (rsvg-convert or magick).
#
# Run after changing web/icon.svg:  make icons
set -euo pipefail

app_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"   # app/
repo_dir="$(cd "$app_dir/.." && pwd)"
svg="$repo_dir/web/icon.svg"
res_dir="$app_dir/build-resources"

master="$(mktemp -t ph-icon).png"
iconset_root="$(mktemp -d -t ph-iconset)"
iconset="$iconset_root/icon.iconset"
mkdir -p "$iconset" "$res_dir"
trap 'rm -rf "$master" "$iconset_root"' EXIT

# 1. SVG → 1024×1024 master PNG
if command -v rsvg-convert >/dev/null 2>&1; then
  rsvg-convert -w 1024 -h 1024 "$svg" -o "$master"
elif command -v magick >/dev/null 2>&1; then
  magick -background none -density 384 "$svg" -resize 1024x1024 "$master"
else
  echo "make-icons: need 'rsvg-convert' or 'magick' to rasterize $svg" >&2
  exit 1
fi

# 2. Build the .iconset with every required size (base + @2x, up to 1024)
for sz in 16 32 128 256 512; do
  sips -z "$sz" "$sz" "$master" --out "$iconset/icon_${sz}x${sz}.png" >/dev/null
  sips -z "$((sz * 2))" "$((sz * 2))" "$master" --out "$iconset/icon_${sz}x${sz}@2x.png" >/dev/null
done

# 3. .iconset → .icns
iconutil -c icns "$iconset" -o "$res_dir/icon.icns"

# 4. Refresh the 1024 PNG (crisper Linux icon + electron-builder fallback)
cp "$master" "$res_dir/icon.png"

echo "make-icons: wrote $res_dir/icon.icns and refreshed $res_dir/icon.png"
