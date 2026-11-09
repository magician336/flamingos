# 03:CI 工作流——单 workflow 双 job

**Spec:** [0009 发布就绪:Eino 示例、README 与公开仓交付](../../../specs/0009-release-readme-eino-publish.md)
**What to build:** 回归门禁从"人记忆与自觉"转为"push 即自动执行":GitHub Actions 工作流落地,单 workflow 双 job——job A 主模块(checkout、按 go.mod 声明版本装 Go、go build ./...、go vet ./...、gofmt -l 检查[有输出即失败]、go test ./... -race ./...):e2e 套件随测试自动执行(含 spec 0008 的 evalx 门禁 family——"evalx 自举为回归门禁"自此在 CI 兑现),benchmark 全量靠既有环境变量门控缺省不触发(CI 时长与稳定性可控);job B examples module 构建校验(进 examples 目录 go build ./...,防示例腐烂)。不引入额外校验工具(无 actionlint 等新依赖),不设 badge(spec 未要求)。**验收跨票交接(票面明定):GitHub Actions 只在仓库位于 GitHub 后执行——本票验收 = 工作流 YAML 就位 + 同命令集本地全绿模拟(build/vet/gofmt/test -race 逐条本地跑绿,YAML 结构自查);工作流在 GitHub 侧的首跑绿验收由票 04 在推送后复核**,两票以同一验收条目交接,本票留痕声明交接点。
**Blocked by:** 01:Eino 示例——examples/ 独立 module(examples 构建校验 job 需要示例 module 存在)。
**Status:** done (2026-11-09)

- [x] 工作流 YAML 落地:单 workflow 双 job(主模块测试门禁 + examples 构建校验),Go 版本以 go.mod 声明为准
- [x] job A 步骤与本地判据一致:build、vet、gofmt -l 零输出检查、go test -race ./...(evalx 门禁 family 随 e2e 在内,benchmark 全量门控缺省不触发)
- [x] job B:examples module go build ./... 校验
- [x] 同命令集本地全绿模拟:build/vet/gofmt -l/test -race 逐条本地跑绿留痕(工作流命令与本地命令一致性的依据)
- [x] 主模块四件套全绿、既有测试零改动、生产代码零改动(本票只增工作流文件与可能的 .github 目录)
- [x] 验收交接留痕:GitHub 侧首跑绿复核归票 04,在票面定档留痕写明交接点

> 定档留痕(2026-11-09,分支 0009-03-ci):工作流落地为 `.github/workflows/ci.yml`(本票唯一代码改动,目录随文件诞生)——单 workflow 双 job,on push + pull_request;job A 主模块:actions/checkout@v7、actions/setup-go@v7(均为当前 stable 大版本,2026-11-09 经 GitHub releases/latest 查得 latest 为 v7.0.1 / v7.0.0;go-version-file: go.mod,Go 版本单一出处)+ `go build ./...`、`go vet ./...`、gofmt 检查(有输出即失败并打印输出)、`go test ./... -race -count=1`;job B examples:同 checkout/setup-go + `working-directory: examples` 下 `go build ./...`。e2e(含 spec 0008 evalx 门禁 family)随 job A 测试步自动执行;benchmark 全量门控变量为 `FLAMINGOS_BENCHMARK`(值 "1" 触发,benchmark/full_test.go),工作流不设该变量,缺省不触发。gofmt 检查范围裁决:仓库根 `gofmt -l .` 对跟踪 .go 文件的唯一存量命中为 `spike/sdkapi/main.go`(tip cd3847e 既有未格式化文件,非本票范围),工作流写排除形态 `gofmt -l $(git ls-files '*.go' | grep -v '^spike/')`(git ls-files 只列跟踪文件,与 CI 检出树一致;104 个跟踪 .go 路径无空格,词切分安全;examples/main.go 票 01 产物格式干净,纳入检查一并防腐烂),确保首跑不因存量即红。本地全绿模拟留痕(worktree 0009-03-ci,`/usr/local/go/bin/go` go1.27.3,与工作流命令逐字一致;本机 gofmt 不在工具 shell PATH,模拟时前置 `/usr/local/go/bin` 对齐——CI 内 setup-go 提供同版工具链):①`go build ./...` 干净;②`go vet ./...` 干净;③gofmt 检查步零输出通过(不排除时全量命中仅 spike/sdkapi/main.go 一处,排除依据实证);④`go test ./... -race -count=1` 16 包全绿 + spike/sdkapi 无测试文件;⑤examples 目录 `go build ./...` 干净。YAML 无本地 actionlint,结构经逐行核对 + YAML 解析自查(触发、双 job、步骤命令逐字核对);不引入额外校验工具,无 badge。既有测试与生产代码零改动(worktree status 仅 .github/ 新增与票面留痕)。**跨票交接:GitHub Actions 仅在仓库上 GitHub 后执行,工作流在 GitHub 侧的首跑绿复核归票 04(推送后),本票交付以工作流 YAML 就位 + 同命令集本地全绿模拟为验收。**验收框与 Status 不勾不改,归合并者。
