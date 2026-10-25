#!/usr/bin/env bash
# Takes the console's screenshots of the demo's synthetic world and writes them,
# as WebP, to docs/images (the README and the website use them). Needs what the
# browser checks need (Node, Playwright's Chromium: test/browser/README.md) and
# Python with Pillow.
#
#   scripts/screenshots.sh
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
[ -d "$root/test/browser/node_modules/playwright" ] || (cd "$root/test/browser" && npm install --silent)
(cd "$root" && go build -o "$work/demo" ./test/browser/demo)
(cd "$root/test/browser" && DEMO="$work/demo" timeout 300 node shots.js "$work/png")
python3 - "$work/png" "$root/docs/images" <<'PY'
import os, sys
from PIL import Image

src, dst = sys.argv[1:3]
os.makedirs(dst, exist_ok=True)
for name in sorted(os.listdir(src)):
    if name.endswith(".png"):
        image = Image.open(os.path.join(src, name)).convert("RGB")
        target = os.path.join(dst, name[:-4] + ".webp")
        image.save(target, "WEBP", quality=88, method=6)
        print(f"{target} {image.width}x{image.height} {os.path.getsize(target) // 1024} KB")
PY
