class Xkb < Formula
  desc "Spec splitter, conflict-aware kanban scheduler and two-stage verifier"
  homepage "https://github.com/ffqa/xkb"
  version "0.1.6"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.6/xkb-v0.1.6-darwin-arm64.tar.gz"
      sha256 "4bdb327ae675461b0ab3c35a269e949306d6fa8baa925dacb10d29d64bac296d"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.6/xkb-v0.1.6-darwin-amd64.tar.gz"
      sha256 "6a4b51bdf6dd3d67e7258b630976af4eac5551637a9ea642541ef8ee50f5c9c4"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.6/xkb-v0.1.6-linux-arm64.tar.gz"
      sha256 "7c09c92341c9bcd184fbc4e6bb4783e890b60998a8e08e9e74c5a311f093245b"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.6/xkb-v0.1.6-linux-amd64.tar.gz"
      sha256 "5efae380506a4fdda17bdc1f55447d6821e982e8dafc26ec5527aaeef0c16c55"
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
