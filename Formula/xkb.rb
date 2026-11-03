class Xkb < Formula
  desc "Spec splitter, conflict-aware kanban scheduler and two-stage verifier"
  homepage "https://github.com/ffqa/xkb"
  version "0.1.7"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.7/xkb-v0.1.7-darwin-arm64.tar.gz"
      sha256 "58b48811a32962b9e0f15b8836defcb01a563b11a6cace189925884c3127744a"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.7/xkb-v0.1.7-darwin-amd64.tar.gz"
      sha256 "77ee949f5eae2f6b78d202e6ba46033d0a130dac8320422f5d96b8fb21bf6828"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.7/xkb-v0.1.7-linux-arm64.tar.gz"
      sha256 "984efd0ce5cea8cbead5c5d45b651ca47866736c03aadb59723934440bcdb7d8"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.7/xkb-v0.1.7-linux-amd64.tar.gz"
      sha256 "e2fa4992f8e9ff0ec908349673805ec3dde44d8a5a83e0fbe27c9da512cb2b54"
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
