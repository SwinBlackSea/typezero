#!/usr/bin/env bash
# Static checks for the macOS client Swift files, runnable on Linux
# (no AppKit/SwiftUI modules required):
#   1. Syntax-parse every client .swift file with swiftc.
#   2. Heuristic: a file referencing AppKit-only types must import AppKit.
#   3. Heuristic: a file referencing AVFoundation-only types must import
#      AVFoundation.
#
# Usage: scripts/check_client_swift.sh
set -u

SWIFTC="${SWIFTC:-/opt/swift/usr/bin/swiftc}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLIENT_DIR="$SCRIPT_DIR/../client/TypeZero"
fail=0

appkit_types=(
  NSTextField NSView NSWindow NSPanel NSStatusBar NSHostingView NSImage
  NSScreen NSApp NSApplication NSApplicationDelegate
  NSTextFieldDelegate NSBezierPath NSColor NSAttributedString NSEvent
  NSHostingController NSImageView NSControl NSFont NSPopover NSStatusItem
  NSMenuItem NSAlert NSSound NSRunningApplication NSOpenPanel NSPasteboard
  NSWorkspace
)
av_types=(AVCaptureDevice AVAudioRecorder AVAudioSession AVAudioEngine AVAudioPlayer)

for f in "$CLIENT_DIR"/*.swift; do
  base="$(basename "$f")"

  if ! "$SWIFTC" -frontend -parse "$f" >/dev/null 2>&1; then
    echo "SYNTAX FAIL: $base"
    "$SWIFTC" -frontend -parse "$f" 2>&1 | head -8
    fail=1
  fi

  # Source with comment-only lines stripped, so doc comments mentioning
  # framework types do not produce false positives.
  clean="$(grep -vE '^\s*(//|///|/\*|\*|\*/)' "$f")"

  if ! grep -q '^import AppKit' "$f"; then
    for t in "${appkit_types[@]}"; do
      if printf '%s\n' "$clean" | grep -qE "\b${t}\b"; then
        echo "IMPORT FAIL: $base uses $t but does not import AppKit"
        fail=1
        break
      fi
    done
  fi

  if ! grep -q '^import AVFoundation' "$f"; then
    for t in "${av_types[@]}"; do
      if printf '%s\n' "$clean" | grep -qE "\b${t}\b"; then
        echo "IMPORT FAIL: $base uses $t but does not import AVFoundation"
        fail=1
        break
      fi
    done
  fi
done

if [ "$fail" -eq 0 ]; then
  echo "ALL CLIENT SWIFT CHECKS PASSED"
else
  echo "CLIENT SWIFT CHECKS FAILED"
fi
exit "$fail"
