// Package benchmark 是顶层压测套件(架构 §2 布局树预告行的落地):vegeta v12
// 以库形态引入(Attacker + ConstantPacer + Metrics 分位数,唯一新第三方依赖),
// 零 import internal——网关与假上游一律 go build 出真实二进制、临时 YAML 配置
// 拉起真实进程,压测进程即测试进程,三进程同机回环拓扑。黑盒纪律由编译器守门:
// 本包 import 了 internal 或 e2e 即设计错误(spec 0007 Implementation Decisions)。
//
// 自足纪律(显式裁决):本套件不 import 其他测试套件(e2e/benchmark 互
// 不依赖),harness 形状复制自 cmd 测试先例——共享需先立架构布局内共享
// 包,复制代价已知且被接受(e2e 包是同款 harness 模式的另一份独立实现)。
//
// 门控形态(spec 0007 Testing Decisions):烟测随常规回归跑,全量场景经
// 环境变量 FLAMINGOS_BENCHMARK=1 显式触发,数字沉淀 docs/benchmark.md。
package benchmark

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

// TestMain 构建出的二进制路径;空串表示构建失败,套件已在 TestMain 报错退出。
// 构建产物放在一次性临时目录:路径全局唯一,孤儿核对按 argv[0] 精确匹配不会
// 误伤无关进程(e2e 同款纪律,独立实现)。
var (
	gatewayBin     string // 仓库 cmd/flamingos 构建的网关二进制
	httpFixtureBin string // upstream 包 testdata HTTP 假上游夹具二进制
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "flamingos-benchmark-")
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

	// 无孤儿核对:套件结束进程表无网关与夹具残留。测试内全部进程句柄的
	// 清理挂靠 t.Cleanup,正常路径此处必空;发现残留先补刀再判失败。
	var orphans []int
	for _, bin := range []string{gatewayBin, httpFixtureBin} {
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
// 回退测试二进制自带的 GOROOT(本机 go 不在 PATH,e2e 同款兜底)。
func goBin() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}

// buildBin 用 go build 把仓库内 pkgDir(自本包出发的相对路径)一次性构建
// 为 dir 下名为 name 的二进制,全部场景复用;构建输出原样带回便于定位编译失败。
func buildBin(dir, name, pkgDir string) (string, error) {
	bin := filepath.Join(dir, name)
	cmd := exec.Command(goBin(), "build", "-o", bin, pkgDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s: %w\n%s", cmd.String(), err, out)
	}
	return bin, nil
}

// fixturePids 扫描 /proc 进程表,返回 argv[0] 恰为给定二进制路径的存活
// 进程 PID:/proc 不可读时返回 nil(本仓目标平台为 Linux)。
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
