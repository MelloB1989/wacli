#!/usr/bin/env bash
#
# Builds the gomobile bindings the Expo module links against.
#
#   ./scripts/build-bindings.sh            # whatever this machine can build
#   ./scripts/build-bindings.sh android    # AAR only
#   ./scripts/build-bindings.sh ios        # XCFramework only
#
# Outputs, both gitignored:
#   expo-wacli/android/libs/wacli.aar
#   expo-wacli/ios/Frameworks/Mobile.xcframework
#
# Requirements. Android needs the NDK, found via ANDROID_NDK_HOME or ANDROID_HOME/ndk/<version>.
# iOS needs Xcode and only builds on macOS. gomobile itself always compiles through cgo — it has to,
# since it generates the JNI and Objective-C bridges — but wacli's own dependency tree is pure Go,
# which is what keeps this to "have a toolchain" rather than "cross-compile SQLite for four
# architectures".

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
module_dir="$repo_root/expo-wacli"
package="github.com/MelloB1989/wacli/mobile"

# The Java package the Kotlin module imports. WacliModule.kt refers to com.wacli.mobile.Mobile, so
# this and that must change together.
java_package="com.wacli"

version="$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || echo dev)"
ldflags="-X ${package}.version=${version}"

target="${1:-auto}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

ensure_gomobile() {
  if ! command -v gomobile >/dev/null 2>&1; then
    log "installing gomobile"
    go install golang.org/x/mobile/cmd/gomobile@latest
    go install golang.org/x/mobile/cmd/gobind@latest
  fi
  # gomobile needs gobind on PATH at bind time, and `go install` puts it in GOBIN.
  export PATH="$PATH:$(go env GOPATH)/bin"
}

resolve_ndk() {
  if [[ -n "${ANDROID_NDK_HOME:-}" ]]; then
    echo "$ANDROID_NDK_HOME"
    return
  fi
  local sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
  # Highest installed version wins; gomobile only needs a recent-enough one.
  local candidate
  candidate="$(ls -d "$sdk"/ndk/* 2>/dev/null | sort -V | tail -1 || true)"
  echo "$candidate"
}

build_android() {
  local ndk
  ndk="$(resolve_ndk)"
  [[ -n "$ndk" && -d "$ndk" ]] || die "Android NDK not found. Set ANDROID_NDK_HOME."
  log "building AAR with NDK at $ndk"
  mkdir -p "$module_dir/android/libs"
  ANDROID_NDK_HOME="$ndk" gomobile bind \
    -target=android \
    -androidapi 24 \
    -javapkg "$java_package" \
    -ldflags "$ldflags" \
    -o "$module_dir/android/libs/wacli.aar" \
    "$package"
  log "wrote $module_dir/android/libs/wacli.aar"
}

build_ios() {
  [[ "$(uname -s)" == "Darwin" ]] || die "iOS bindings only build on macOS with Xcode."
  command -v xcrun >/dev/null 2>&1 || die "Xcode command line tools not found."
  log "building XCFramework"
  mkdir -p "$module_dir/ios/Frameworks"
  # The output name sets the Swift module name: WacliModule.swift does `import Mobile`.
  rm -rf "$module_dir/ios/Frameworks/Mobile.xcframework"
  gomobile bind \
    -target=ios,iossimulator \
    -iosversion 15.1 \
    -ldflags "$ldflags" \
    -o "$module_dir/ios/Frameworks/Mobile.xcframework" \
    "$package"
  log "wrote $module_dir/ios/Frameworks/Mobile.xcframework"
}

cd "$repo_root"
ensure_gomobile
log "wacli version $version"

case "$target" in
  android) build_android ;;
  ios) build_ios ;;
  auto)
    built_any=false
    if [[ -n "$(resolve_ndk)" ]]; then
      build_android
      built_any=true
    else
      warn "skipping Android: no NDK found (set ANDROID_NDK_HOME)"
    fi
    if [[ "$(uname -s)" == "Darwin" ]]; then
      build_ios
      built_any=true
    else
      warn "skipping iOS: not macOS"
    fi
    [[ "$built_any" == true ]] || die "nothing could be built on this machine"
    ;;
  *) die "unknown target '$target' (expected android, ios, or no argument)" ;;
esac

log "done"
