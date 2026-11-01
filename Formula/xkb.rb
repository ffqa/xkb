class Xkb < Formula
  desc "Spec splitter, conflict-aware kanban scheduler and two-stage verifier"
  homepage "https://github.com/ffqa/xkb"
  version "0.1.2"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.2/xkb-v0.1.2-darwin-arm64.tar.gz"
      sha256 "55427a9cbe4f8037a2aa58cdb639341d3af3af323ce71c5cba49164bdc1ecfe7"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.2/xkb-v0.1.2-darwin-amd64.tar.gz"
      sha256 "18f0f2e7fccb16c8aba41cb1225a2442b36d6cbec21b150ad272cf351895b6ca"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.2/xkb-v0.1.2-linux-arm64.tar.gz"
      sha256 "74584df3df5afa0fc923301692cbe78557de953d5e1321fba415df352964ae2f"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.2/xkb-v0.1.2-linux-amd64.tar.gz"
      sha256 "7e205de9907ffeef1508518a7d858456e99ffea3ba39913c13fdd9c64ae4c40e"
    end
  end

  def install
    bin.install "xkb"
  end


  service do
    run [opt_bin/"xkb", "server", "--port", "7790"]
    keep_alive true
    working_dir Dir.home
    environment_variables XKB_SERVICE: "1"
    log_path var/"log/xkb.log"
    error_log_path var/"log/xkb.log"
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
