package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xpier/xkb/internal/updater"
	"github.com/xpier/xkb/internal/version"
)

func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	checkOnly := fs.Bool("check", false, "仅检查是否有新版本，不执行升级")
	force := fs.Bool("force", false, "跳过 SHA256 校验强制升级")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Printf("xkb 当前版本: %s (%s/%s)\n", version.Version, runtime.GOOS, runtime.GOARCH)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, err := updateSelf(ctx, *checkOnly, *force, true)
	return err
}

// updateSelf upgrades the current installation. It returns true when the
// binary/formula was actually changed.
func updateSelf(ctx context.Context, checkOnly, force, verbose bool) (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	if isBrewInstall(exe) {
		return updateBrew(ctx, checkOnly, verbose)
	}
	return updateBinary(ctx, exe, checkOnly, force, verbose)
}

func updateBrew(ctx context.Context, checkOnly, verbose bool) (bool, error) {
	if _, err := exec.LookPath("brew"); err != nil {
		return false, fmt.Errorf("未找到 brew 命令")
	}
	_ = exec.CommandContext(ctx, "brew", "update").Run()
	out, _ := exec.CommandContext(ctx, "brew", "outdated", "ffqa/tap/xkb").Output()
	if strings.TrimSpace(string(out)) == "" {
		out, _ = exec.CommandContext(ctx, "brew", "outdated", "xkb").Output()
	}
	if strings.TrimSpace(string(out)) == "" {
		if verbose {
			fmt.Println("已经是最新版本，无需更新。")
		}
		return false, nil
	}
	if verbose {
		fmt.Printf("发现可升级版本:\n%s\n", strings.TrimSpace(string(out)))
	}
	if checkOnly {
		if verbose {
			fmt.Println("运行 'xkb update' 即可完成升级。")
		}
		return false, nil
	}
	if verbose {
		fmt.Println("正在执行 brew upgrade...")
	}
	cmd := exec.CommandContext(ctx, "brew", "upgrade", "ffqa/tap/xkb")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fallback := exec.CommandContext(ctx, "brew", "upgrade", "xkb")
		fallback.Stdout, fallback.Stderr = os.Stdout, os.Stderr
		if ferr := fallback.Run(); ferr != nil {
			return false, err
		}
	}
	if verbose {
		fmt.Println("升级完成。若服务正在运行，请重启进程。")
	}
	return true, nil
}

func updateBinary(ctx context.Context, exe string, checkOnly, force, verbose bool) (bool, error) {
	if verbose {
		fmt.Println("正在检查 GitHub Releases 最新版本...")
	}
	rel, err := updater.FetchLatest(ctx)
	if err != nil {
		return false, err
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	if !updater.IsNewer(version.Version, latest) {
		if verbose {
			fmt.Printf("已经是最新版本 (%s)。\n", version.Version)
		}
		return false, nil
	}
	if verbose {
		fmt.Printf("发现新版本: %s (当前: %s)\n", rel.TagName, version.Version)
	}
	if checkOnly {
		if verbose {
			fmt.Println("运行 'xkb update' 即可自动下载并升级。")
		}
		return false, nil
	}
	assetName, assetURL := updater.FindMatchingAsset(rel, runtime.GOOS, runtime.GOARCH)
	if assetURL == "" {
		return false, fmt.Errorf("未找到 %s-%s 的发布包", runtime.GOOS, runtime.GOARCH)
	}
	if verbose {
		fmt.Printf("正在下载 %s ...\n", assetName)
	}
	archive, err := downloadFile(ctx, assetURL)
	if err != nil {
		return false, err
	}
	defer os.Remove(archive)

	sumsURL := updater.FindSumsURL(rel)
	if sumsURL == "" && !force {
		return false, fmt.Errorf("发布包缺少 SHA256SUMS.txt，已终止升级（可用 --force 跳过）")
	}
	if sumsURL != "" {
		sumsPath, err := downloadFile(ctx, sumsURL)
		if err != nil && !force {
			return false, fmt.Errorf("下载校验和失败: %w", err)
		}
		if err == nil {
			defer os.Remove(sumsPath)
			content, _ := os.ReadFile(sumsPath)
			expected := parseChecksum(string(content), assetName)
			if expected == "" && !force {
				return false, fmt.Errorf("SHA256SUMS.txt 中缺少 %s", assetName)
			}
			if expected != "" {
				actual, err := sha256File(archive)
				if err != nil {
					return false, err
				}
				if !strings.EqualFold(actual, expected) {
					return false, fmt.Errorf("校验和不匹配: 预期 %s 实际 %s", expected, actual)
				}
				if verbose {
					fmt.Printf("✓ 完整性校验通过 (%s)\n", actual[:12])
				}
			}
		}
	}
	target := exe
	if strings.Contains(filepath.ToSlash(exe), "/tmp/") || strings.Contains(filepath.ToSlash(exe), "/Downloads/") {
		target = localBinPath()
	}
	if verbose {
		fmt.Printf("正在安装到 %s ...\n", target)
	}
	if err := extractAndInstall(archive, target); err != nil {
		return false, err
	}
	if verbose {
		fmt.Printf("🎉 已升级至 %s\n", rel.TagName)
	}
	return true, nil
}

func localBinPath() string {
	home, _ := os.UserHomeDir()
	name := "xkb"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(home, ".local", "bin", name)
}

func isBrewInstall(exe string) bool {
	clean := filepath.ToSlash(exe)
	if strings.Contains(clean, "/Cellar/xkb/") || strings.Contains(clean, "/opt/homebrew/opt/xkb/") {
		return true
	}
	if _, err := exec.LookPath("brew"); err == nil {
		for _, formula := range []string{"ffqa/tap/xkb", "xkb"} {
			if out, err := exec.Command("brew", "list", "--formula", formula).CombinedOutput(); err == nil && strings.Contains(string(out), "xkb") {
				return true
			}
		}
	}
	return false
}

func downloadFile(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载返回 %d", resp.StatusCode)
	}
	f, err := os.CreateTemp("", "xkb-update-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func parseChecksum(content, fileName string) string {
	for _, line := range strings.Split(content, "\n") {
		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) >= 2 && parts[1] == fileName {
			return parts[0]
		}
	}
	return ""
}

func extractAndInstall(archive, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".xkb-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if strings.HasSuffix(archive, ".zip") {
		err = extractFromZip(archive, tmp)
	} else {
		err = extractFromTarGz(archive, tmp)
	}
	if err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

func extractFromTarGz(archive string, dst io.Writer) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == "xkb" {
			_, err := io.Copy(dst, tr)
			return err
		}
	}
	return fmt.Errorf("发布包中未找到 xkb 可执行文件")
}

func extractFromZip(archive string, dst io.Writer) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if filepath.Base(f.Name) != "xkb" && filepath.Base(f.Name) != "xkb.exe" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		_, err = io.Copy(dst, rc)
		rc.Close()
		return err
	}
	return fmt.Errorf("发布包中未找到 xkb 可执行文件")
}
