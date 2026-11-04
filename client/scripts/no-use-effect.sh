#!/bin/sh
cd "$(dirname "$0")/.." || exit 2
if grep -rnE 'useEffect|useLayoutEffect' --include='*.ts' --include='*.tsx' core/src core/test react/src react/test; then
  echo 'useEffect and useLayoutEffect are forbidden under client/' >&2
  exit 1
fi
