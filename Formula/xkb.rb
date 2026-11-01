class Xkb < Formula
  desc "Spec splitter, conflict-aware kanban scheduler and two-stage verifier"
  homepage "https://github.com/ffqa/xkb"
  version "0.1.1"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.1/xkb-v0.1.1-darwin-arm64.tar.gz"
      sha256 "f0173604ed073e99747fd6f10e08277a63fcbf58e3817300a77e0782b9813f50"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.1/xkb-v0.1.1-darwin-amd64.tar.gz"
      sha256 "8a14a51d2548ac68f26b22489c8a70d8a43bc22a62dca3113478040a20b58c4c"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.1/xkb-v0.1.1-linux-arm64.tar.gz"
      sha256 "c97b7b5d263e730509164768c5f2420570d1380cf01b9b5051e831abd6475bb6"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.1/xkb-v0.1.1-linux-amd64.tar.gz"
      sha256 "46f01f23b80e33fbd0da1feeb2cd213baf5359fb53d36122e6fc1067716c9555"
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
