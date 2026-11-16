#!/usr/bin/env bash
# Mirror the Windows working copy (tracked + untracked, not ignored) into ~/ClearChain with LF endings.
# Generated crypto, ledgers and vendor folders in ~/ClearChain are left alone.
SRC=/mnt/d/GIT/ClearChain
DST=$HOME/ClearChain
cd "$SRC" || exit 1
n=0
while IFS= read -r -d '' f; do
  [ -f "$f" ] || continue
  mkdir -p "$DST/$(dirname "$f")"
  case "$f" in
    *.db|*.png|*.jpg|*.jpeg|*.pem|*_sk|*.crt|*.key|*.tar.gz) cp -p "$f" "$DST/$f" ;;
    *) tr -d '\r' < "$f" > "$DST/$f.tmp.$$" && { cmp -s "$DST/$f.tmp.$$" "$DST/$f" 2>/dev/null && rm "$DST/$f.tmp.$$" || { mv "$DST/$f.tmp.$$" "$DST/$f"; n=$((n+1)); }; } ;;
  esac
  case "$f" in *.sh) chmod +x "$DST/$f" ;; esac
done < <(git -c safe.directory='*' -c core.quotepath=off ls-files -co --exclude-standard -z)
echo "synced: $n files changed"
