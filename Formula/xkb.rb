class Xkb < Formula
  desc "Spec splitter, conflict-aware kanban scheduler and two-stage verifier"
  homepage "https://github.com/ffqa/xkb"
  version "0.1.8"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.8/xkb-v0.1.8-darwin-arm64.tar.gz"
      sha256 "953111de7dcc503b7b9f127edf94b10f971565e10872c6171412db6cf56cbb7a"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.8/xkb-v0.1.8-darwin-amd64.tar.gz"
      sha256 "468e4c392600616f3b4eb98ad30f80a7c0cedf85f2a9278744a4b8aa9ec292f4"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/ffqa/xkb/releases/download/v0.1.8/xkb-v0.1.8-linux-arm64.tar.gz"
      sha256 "6304c28e80f737896543799f7350b59ef9bc6dc2c479220eb8019ccc535a571d"
    else
      url "https://github.com/ffqa/xkb/releases/download/v0.1.8/xkb-v0.1.8-linux-amd64.tar.gz"
      sha256 "cf8556aef604934da81502140e4c5077c01a5dba5687e8787fc806db06654704"
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
