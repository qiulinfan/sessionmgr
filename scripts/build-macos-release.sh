#!/bin/bash
set -euo pipefail

version="${1:-}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Usage: scripts/build-macos-release.sh MAJOR.MINOR.PATCH" >&2
  exit 1
fi
if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS app packaging requires macOS and Xcode command-line tools." >&2
  exit 1
fi

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
distribution_root="$project_root/dist"
app_directory="$distribution_root/macos-v$version"
app_path="$app_directory/Session Manager.app"
asset_name="sessionmgr-v$version-macos-universal.zip"
go_executable="${GO:-go}"
macos_sdk="$(xcrun --show-sdk-path)"
temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/sessionmgr-macos.XXXXXX")"
trap 'rm -rf "$temporary_root"' EXIT

staged_app="$temporary_root/Session Manager.app"
mkdir -p "$staged_app/Contents/MacOS" "$staged_app/Contents/Resources/bin"
linker_flags="-s -w -X github.com/sessionmgr/sessionmgr/internal/app.version=$version"
for architecture in arm64 amd64; do
  (cd "$project_root" && CGO_ENABLED=0 GOOS=darwin GOARCH="$architecture" \
    "$go_executable" build -trimpath -ldflags "$linker_flags" \
    -o "$temporary_root/backend-$architecture" ./cmd/sessionmgr)
done
lipo -create "$temporary_root/backend-arm64" "$temporary_root/backend-amd64" \
  -output "$staged_app/Contents/Resources/bin/sessionmgr"

for architecture in arm64 x86_64; do
  xcrun swiftc -O -swift-version 5 -parse-as-library \
    -target "$architecture-apple-macosx14.0" -sdk "$macos_sdk" \
    -framework AppKit -framework WebKit "$project_root/macos/SessionManagerApp.swift" \
    -o "$temporary_root/launcher-$architecture"
done
lipo -create "$temporary_root/launcher-arm64" "$temporary_root/launcher-x86_64" \
  -output "$staged_app/Contents/MacOS/SessionManager"

iconset="$temporary_root/SessionManager.iconset"
mkdir -p "$iconset"
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" "$project_root/assets/sessionmgr.png" \
    --out "$iconset/icon_${size}x${size}.png" >/dev/null
  double_size=$((size * 2))
  sips -z "$double_size" "$double_size" "$project_root/assets/sessionmgr.png" \
    --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o "$staged_app/Contents/Resources/SessionManager.icns"

cat > "$staged_app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleIdentifier</key><string>com.qiulinfan.sessionmgr</string>
  <key>CFBundleName</key><string>Session Manager</string>
  <key>CFBundleDisplayName</key><string>Session Manager</string>
  <key>CFBundleExecutable</key><string>SessionManager</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$version</string>
  <key>CFBundleVersion</key><string>$version</string>
  <key>CFBundleIconFile</key><string>SessionManager.icns</string>
  <key>LSMinimumSystemVersion</key><string>14.0</string>
  <key>LSMultipleInstancesProhibited</key><true/>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict></plist>
PLIST
printf 'APPL????' > "$staged_app/Contents/PkgInfo"
plutil -lint "$staged_app/Contents/Info.plist"

# Ad-hoc signing ensures local bundle integrity. This is not Developer ID
# signing or Apple notarization; release notes state that distinction.
codesign --force --sign - --identifier com.qiulinfan.sessionmgr.backend \
  "$staged_app/Contents/Resources/bin/sessionmgr"
codesign --force --sign - "$staged_app"
codesign --verify --deep --strict "$staged_app"
for executable in "$staged_app/Contents/MacOS/SessionManager" "$staged_app/Contents/Resources/bin/sessionmgr"; do
  architectures="$(lipo "$executable" -archs)"
  if [[ "$architectures" != "arm64 x86_64" && "$architectures" != "x86_64 arm64" ]]; then
    echo "The app executable is not universal arm64/x86_64: $architectures" >&2
    exit 1
  fi
done
reported_version="$("$staged_app/Contents/Resources/bin/sessionmgr" version)"
if [[ "$reported_version" != "sessionmgr $version" ]]; then
  echo "The bundled backend does not report the release version." >&2
  exit 1
fi

if [[ -e "$app_path" || -L "$app_path" ]]; then
  existing_identifier="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$app_path/Contents/Info.plist" 2>/dev/null || true)"
  if [[ -L "$app_path" || "$existing_identifier" != "com.qiulinfan.sessionmgr" ]]; then
    echo "Refusing to replace an unrecognized app build: $app_path" >&2
    exit 1
  fi
  rm -rf "$app_path"
fi
mkdir -p "$app_directory"
mv "$staged_app" "$app_path"
ditto -c -k --sequesterRsrc --keepParent "$app_path" "$distribution_root/$asset_name"
(cd "$distribution_root" && shasum -a 256 "$asset_name") > "$distribution_root/$asset_name.sha256"
echo "Built $app_path"
echo "Release asset: $distribution_root/$asset_name"
