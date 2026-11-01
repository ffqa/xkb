class Xkb < Formula
  desc "Spec splitter, conflict-aware kanban scheduler and two-stage verifier"
  homepage "https://github.com/ffqa/xkb"
  version "0.1.5"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.5/xkb-v0.1.5-darwin-arm64.tar.gz"
      sha256 "de1b0396e5836a8942b46db7b3b67efc378d1adc7e2e58a4b53742276be85234"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.5/xkb-v0.1.5-darwin-amd64.tar.gz"
      sha256 "7738e7401782baa6592dac40302a841d98047c4bd4f63d3cf3a4a651538f9ddf"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.5/xkb-v0.1.5-linux-arm64.tar.gz"
      sha256 "18225557fa717d88966a0f074306cb864ce6ea69bcce466288cb703d7dacdad7"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.5/xkb-v0.1.5-linux-amd64.tar.gz"
      sha256 "e05ce7447459cbfa1e9585608c4eb1b32c46c7370eafa9894775d9893e2d2d86"
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
