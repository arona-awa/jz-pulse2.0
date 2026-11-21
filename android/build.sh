#!/bin/sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
SDK="${ANDROID_SDK_ROOT:-/opt/android-sdk}"
PLATFORM="$SDK/platforms/android-36/android.jar"
BUILD_TOOLS="${ANDROID_BUILD_TOOLS:-$(ls -d "$SDK"/build-tools/* 2>/dev/null | sort -V | tail -1)}"
AAPT2="${AAPT2:-$(command -v aapt2 || echo "$BUILD_TOOLS/aapt2")}"
D8="$BUILD_TOOLS/d8"
APKSIGNER="$BUILD_TOOLS/apksigner"
BUILD="$ROOT/build"
OUT="${OUT:-$ROOT/jz-pulse.apk}"
KEYSTORE="$ROOT/debug.keystore"

rm -rf "$BUILD"
mkdir -p "$BUILD/gen" "$BUILD/classes" "$BUILD/dex"

"$AAPT2" compile --dir "$ROOT/res" -o "$BUILD/res.zip"
"$AAPT2" link \
    -o "$BUILD/app-unsigned.apk" \
    -I "$PLATFORM" \
    --manifest "$ROOT/AndroidManifest.xml" \
    --java "$BUILD/gen" \
    --min-sdk-version 24 \
    --target-sdk-version 36 \
    "$BUILD/res.zip"

find "$ROOT/src" "$BUILD/gen" -name '*.java' -print | sort > "$BUILD/sources.txt"
javac -encoding UTF-8 -source 8 -target 8 -classpath "$PLATFORM" -d "$BUILD/classes" @"$BUILD/sources.txt"
jar cf "$BUILD/classes.jar" -C "$BUILD/classes" .
"$D8" --lib "$PLATFORM" --min-api 24 --output "$BUILD/dex" "$BUILD/classes.jar"

( cd "$BUILD/dex" && zip -q -j "$BUILD/app-unsigned.apk" classes.dex )

if [ ! -f "$KEYSTORE" ]; then
    keytool -genkeypair \
        -keystore "$KEYSTORE" \
        -storepass android \
        -keypass android \
        -alias jzpulse \
        -keyalg RSA \
        -keysize 2048 \
        -validity 10000 \
        -dname "CN=JZ Pulse, OU=App, O=JZ, L=Jiangyan, ST=Jiangsu, C=CN"
fi

"$APKSIGNER" sign \
    --ks "$KEYSTORE" \
    --ks-key-alias jzpulse \
    --ks-pass pass:android \
    --key-pass pass:android \
    --out "$OUT" \
    "$BUILD/app-unsigned.apk"

"$APKSIGNER" verify --verbose "$OUT"
echo "APK: $OUT"
