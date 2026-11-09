// Package e2e 是顶层黑盒端到端套件(架构 §2 布局树预告行的落地):零
// import internal 包——网关与假上游一律 go build 出真实二进制、临时 YAML
// 配置(字符串模板写临时文件)拉起真实进程,断言只落在进程边界可观察面
// (MCP JSON-RPC 应答、进程退出码、进程表)上。黑盒纪律由编译器守门:
// 本包 import 了 internal 即设计错误(spec 0007 Solution/Testing Decisions)。
//
// 自足纪律(显式裁决):本套件不 import 其他测试套件(e2e/benchmark 互
// 不依赖),harness 形状复制自 cmd 测试先例——共享需先立架构布局内共享
// 包,复制代价已知且被接受。
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestMain 构建出的四个二进制路径;空串表示构建失败,套件已在 TestMain
// 报错退出。构建产物放在一次性临时目录:路径全局唯一,孤儿核对按 argv[0]
// 精确匹配不会误伤无关进程(fixturePids 先例)。
var (
	gatewayBin      string // 仓库 cmd/flamingos 构建的网关二进制
	httpFixtureBin  string // upstream 包 testdata HTTP 假上游夹具二进制
	stdioFixtureBin string // upstream 包 testdata stdio 假上游夹具二进制
	evalxBin        string // 仓库 cmd/evalx 构建的回放 CLI 二进制(spec 0008 门禁 family)
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "flamingos-e2e-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建临时目录失败: %v\n", err)
		os.Exit(1)
	}
	for _, b := range []struct {
		slot   *string
		name   string
		pkgDir string
	}{
		{&gatewayBin, "flamingos", "../cmd/flamingos"},
		{&httpFixtureBin, "httpserver", "../internal/upstream/testdata/httpserver"},
		{&stdioFixtureBin, "stdioserver", "../internal/upstream/testdata/stdioserver"},
		{&evalxBin, "evalx", "../cmd/evalx"},
	} {
		bin, err := buildBin(dir, b.name, b.pkgDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "构建 %s 失败: %v\n", b.name, err)
			os.RemoveAll(dir)
			os.Exit(1)
		}
		*b.slot = bin
	}
	code := m.Run()

	// 无孤儿核对(spec 0007 验收清单):套件结束进程表无网关与夹具残留。
	// 测试内全部进程句柄的清理挂靠 t.Cleanup,正常路径此处必空;发现残留
	// 先补刀再判失败——失败的套件不向机器泄漏进程。
	var orphans []int
	for _, bin := range []string{gatewayBin, httpFixtureBin, stdioFixtureBin, evalxBin} {
		orphans = append(orphans, fixturePids(bin)...)
	}
	if len(orphans) > 0 {
		fmt.Fprintf(os.Stderr, "套件结束后仍有残留进程,已补刀: %v\n", orphans)
		for _, pid := range orphans {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		code = 1
	}
	os.RemoveAll(dir)
	os.Exit(code)
}

// goBin 返回可用于 go build 的 go 可执行文件:优先取 PATH,PATH 无 go 时
// 回退测试二进制自带的 GOROOT(cmd e2e TestMain 同款兜底,本机 go 不在
// PATH)。
func goBin() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}

// buildBin 用 go build 把仓库内 pkgDir(自本包出发的相对路径)一次性构建
// 为 dir 下名为 name 的二进制,全部测试复用,避免逐例构建拖慢套件
// (cmd e2e TestMain 先例);构建输出原样带回便于定位编译失败。
func buildBin(dir, name, pkgDir string) (string, error) {
	bin := filepath.Join(dir, name)
	cmd := exec.Command(goBin(), "build", "-o", bin, pkgDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s: %w\n%s", cmd.String(), err, out)
	}
	return bin, nil
}

// fixturePids 扫描 /proc 进程表,返回 argv[0] 恰为给定二进制路径的存活
// 进程 PID:"无孤儿"核对直读真实进程表,不依赖已被撤销的客户端句柄
// (cmd e2e 同款)。/proc 不可读时返回 nil(本仓目标平台为 Linux)。
func fixturePids(binPath string) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // /proc 下的非 PID 条目(如 /proc/self)
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue // 进程恰好在此刻退出
		}
		if argv0, _, _ := strings.Cut(string(data), "\x00"); argv0 == binPath {
			pids = append(pids, pid)
		}
	}
	return pids
}
