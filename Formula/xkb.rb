class Xkb < Formula
  desc "Spec splitter, conflict-aware kanban scheduler and two-stage verifier"
  homepage "https://github.com/ffqa/xkb"
  version "0.1.3"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.3/xkb-v0.1.3-darwin-arm64.tar.gz"
      sha256 "00c80a6519a22b6872840f19ff32bbcbf52715de9c21547c95cd105a1ea52435"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.3/xkb-v0.1.3-darwin-amd64.tar.gz"
      sha256 "962a058d3ef2b7df0f6071b56f306cc896c660d64d0fcf679813d910115446f1"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.3/xkb-v0.1.3-linux-arm64.tar.gz"
      sha256 "c57d314611ed7ebe03c3373589f1c042c9415a62d2bbd46a9b0be3d02798982f"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.3/xkb-v0.1.3-linux-amd64.tar.gz"
      sha256 "999139d61796e63cada0aa3f891a0379f39ffa8837589eabb1b8a3847084e04b"
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
