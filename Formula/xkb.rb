class Xkb < Formula
  desc "Spec splitter, conflict-aware kanban scheduler and two-stage verifier"
  homepage "https://github.com/ffqa/xkb"
  version "0.1.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.0/xkb-v0.1.0-darwin-arm64.tar.gz"
      sha256 "REPLACE_DARWIN_ARM64"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.0/xkb-v0.1.0-darwin-amd64.tar.gz"
      sha256 "REPLACE_DARWIN_AMD64"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.0/xkb-v0.1.0-linux-arm64.tar.gz"
      sha256 "REPLACE_LINUX_ARM64"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.0/xkb-v0.1.0-linux-amd64.tar.gz"
      sha256 "REPLACE_LINUX_AMD64"
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
