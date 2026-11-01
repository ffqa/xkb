#!/usr/bin/env bash
# Publish the xkb Homebrew formula to ffqa/homebrew-tap.
# Usage: bash scripts/brew-publish.sh [v0.1.0] [--formula-only]
set -euo pipefail

GH_USER="ffqa"
MAIN_REPO="xkb"
TAP_REPO="homebrew-tap"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION=""
FORMULA_ONLY=0
for arg in "$@"; do
  case "$arg" in
    --formula-only) FORMULA_ONLY=1 ;;
    v*) VERSION="${arg#v}" ;;
    [0-9]*) VERSION="$arg" ;;
    *) echo "unknown argument: $arg" >&2; exit 1 ;;
  esac
done
if [[ -z "$VERSION" ]]; then
  VERSION="$(git describe --tags --abbrev=0 2>/dev/null || echo "")"
  VERSION="${VERSION#v}"
fi
if [[ -z "$VERSION" ]]; then
  echo "cannot determine version; pass it explicitly" >&2
  exit 1
fi

TAG="v${VERSION}"
BASE_URL="https://github.com/${GH_USER}/${MAIN_REPO}/releases/download/${TAG}"
SUMS_URL="${BASE_URL}/SHA256SUMS.txt"
SUMS_CONTENT="$(curl -fsSL "$SUMS_URL" || true)"

get_sha() {
  local asset="$1" sha=""
  if [[ -n "$SUMS_CONTENT" ]]; then
    sha="$(echo "$SUMS_CONTENT" | grep "$asset" | awk '{print $1}' || true)"
  fi
  if [[ -z "$sha" && -f "dist/release/$asset" ]]; then
    sha="$(shasum -a 256 "dist/release/$asset" | awk '{print $1}')"
  fi
  if [[ -z "$sha" ]]; then
    sha="$(curl -fsSL "${BASE_URL}/${asset}" | shasum -a 256 | awk '{print $1}')"
  fi
  echo "$sha"
}

DARWIN_ARM64_SHA="$(get_sha "xkb-${TAG}-darwin-arm64.tar.gz")"
DARWIN_AMD64_SHA="$(get_sha "xkb-${TAG}-darwin-amd64.tar.gz")"
LINUX_ARM64_SHA="$(get_sha "xkb-${TAG}-linux-arm64.tar.gz")"
LINUX_AMD64_SHA="$(get_sha "xkb-${TAG}-linux-amd64.tar.gz")"

echo "version: $VERSION"
echo "  darwin-arm64: $DARWIN_ARM64_SHA"
echo "  darwin-amd64: $DARWIN_AMD64_SHA"
echo "  linux-arm64:  $LINUX_ARM64_SHA"
echo "  linux-amd64:  $LINUX_AMD64_SHA"

mkdir -p Formula
cat <<FORMULA > Formula/xkb.rb
class Xkb < Formula
  desc "Spec splitter, conflict-aware kanban scheduler and two-stage verifier"
  homepage "https://github.com/${GH_USER}/${MAIN_REPO}"
  version "${VERSION}"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "${BASE_URL}/xkb-${TAG}-darwin-arm64.tar.gz"
      sha256 "${DARWIN_ARM64_SHA}"
    else
      url "${BASE_URL}/xkb-${TAG}-darwin-amd64.tar.gz"
      sha256 "${DARWIN_AMD64_SHA}"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "${BASE_URL}/xkb-${TAG}-linux-arm64.tar.gz"
      sha256 "${LINUX_ARM64_SHA}"
    else
      url "${BASE_URL}/xkb-${TAG}-linux-amd64.tar.gz"
      sha256 "${LINUX_AMD64_SHA}"
    end
  end

  def install
    bin.install "xkb"
  end

  def caveats
    <<~EOS
      启动常驻调度与 HTTP/SSE 服务:
        xkb server --port 7790

      终端看板:
        xkb
    EOS
  end

  test do
    assert_match "xkb", shell_output("#{bin}/xkb version")
  end
end
FORMULA

echo "formula written: Formula/xkb.rb"
if [[ "$FORMULA_ONLY" == "1" ]]; then
  exit 0
fi

TMP_TAP="$(mktemp -d)"
trap 'rm -rf "$TMP_TAP"' EXIT
gh repo clone "${GH_USER}/${TAP_REPO}" "$TMP_TAP" -- --depth=1
mkdir -p "$TMP_TAP/Formula"
cp Formula/xkb.rb "$TMP_TAP/Formula/xkb.rb"
cd "$TMP_TAP"
git config user.name "xkb-release"
git config user.email "xkb-release@localhost"
git add Formula/xkb.rb
git commit -m "xkb ${VERSION}" || true
git push
echo "published xkb ${VERSION} to ${GH_USER}/${TAP_REPO}"
