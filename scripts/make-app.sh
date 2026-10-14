#!/bin/sh
# Builds build/Reel.app around build/reel and signs it, so the Screen
# Recording and Microphone permissions belong to the app and survive
# rebuilds.
#
# macOS keeps a permission against the app's designated requirement. An
# ad-hoc signature (the linker's default) makes that requirement the cdhash,
# which changes with every build, so every build would ask again; a
# certificate signature makes it "identifier + certificate leaf", which
# doesn't. SIGN is the certificate's SHA-1 (two certificates can share a name).
#
# Copied from an earlier project of the author's,
# plus the usage strings and signing.
set -eu
bin=${1:-build/reel}
app=${2:-build/Reel.app}
sign=${SIGN:?set SIGN to a codesigning identity (its SHA-1)}
bundle_id=${BUNDLE_ID:-com.anthonyrego.reel}
version=$(git describe --always --dirty 2>/dev/null || echo dev)
icon=$(dirname "$0")/../assets/icon/AppIcon.png

rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$bin" "$app/Contents/MacOS/reel"

icon_key=""
if [ -f "$icon" ]; then
	# AppIcon.icns from the 1024 px PNG.
	set_dir=$(mktemp -d)/AppIcon.iconset
	mkdir -p "$set_dir"
	for s in 16 32 128 256 512; do
		sips -z $s $s "$icon" --out "$set_dir/icon_${s}x${s}.png" >/dev/null
		sips -z $((s * 2)) $((s * 2)) "$icon" --out "$set_dir/icon_${s}x${s}@2x.png" >/dev/null
	done
	iconutil -c icns "$set_dir" -o "$app/Contents/Resources/AppIcon.icns"
	rm -rf "$(dirname "$set_dir")"
	icon_key="<key>CFBundleIconFile</key><string>AppIcon</string>"
fi

cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>Reel</string>
	<key>CFBundleDisplayName</key><string>Reel</string>
	<key>CFBundleIdentifier</key><string>$bundle_id</string>
	<key>CFBundleExecutable</key><string>reel</string>
	$icon_key
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$version</string>
	<key>CFBundleVersion</key><string>$version</string>
	<key>LSMinimumSystemVersion</key><string>15.0</string>
	<key>LSApplicationCategoryType</key><string>public.app-category.productivity</string>
	<key>NSHighResolutionCapable</key><true/>
	<key>NSSupportsAutomaticGraphicsSwitching</key><true/>
	<key>NSMicrophoneUsageDescription</key><string>Reel records your microphone so you can talk over a recording.</string>
	<key>NSCameraUsageDescription</key><string>Reel shows your camera in a bubble on screen, so you're in what you record.</string>
	<key>NSCameraUseContinuityCameraDeviceType</key><true/>
</dict>
</plist>
PLIST

# No hardened runtime for dev builds: with it the mic needs an entitlement,
# and the embedded SDL dylib needs library validation off.
codesign --force --sign "$sign" --identifier "$bundle_id" "$app"
# Finder and the Dock cache icons by bundle; a newer mtime makes them look again.
touch "$app"
echo "$app"
