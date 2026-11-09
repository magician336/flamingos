---
id: 0001
title: "脚手架与 config 模块"
triage: ready-for-agent
date: 2026-11-02
source: 设计会话(docs/design/architecture.md §4.3/§6/§9)+ spike(docs/design/spike-go-sdk.md)
tracker-note: 本地 tracker;W4 GitHub 公开时迁为 issue 并加同名 label
---

# Spec 0001 — 脚手架与 config 模块

## Problem Statement

网关的任何功能都还没有落地形态:没有可执行入口,没有配置文件支撑。部署者(以及未来 GitHub 用户)没有办法用声明式的方式描述"我有哪些上游 MCP server";开发者在下一步写转发逻辑时,也没有可信任的配置结构可用——如果配置校验散落在各模块,每个模块都要写防御代码,错误在运行时才暴露。

## Solution

建立一个"接口只有一个函数"的深模块:配置加载。部署者写一个 YAML 文件声明网关监听地址与上游列表(HTTP/stdio 两种),程序启动时一次性加载并全量校验——任何问题(拼写、缺失、格式、歧义)在启动瞬间以人类可读的中文上下文报错,且**一次性列出全部问题**而非逐次试错。校验通过后返回的结构可被下游模块完全信任,零防御代码。同时落地仓库骨架:可执行入口(仅薄装配:解析参数→加载配置→组装→退出)。

## User Stories

1. As 网关运维, I want 用一个 YAML 文件声明全部上游, so that 不改代码就能增删上游
2. As 网关运维, I want 配置错误在启动时一次性全部列出, so that 不用"改一处、重启、再报下一个错"地试错
3. As 网关运维, I want 省略可选字段时得到合理默认值(监听 :8080、上游超时 30s), so that 最小配置即可运行
4. As 网关运维, I want 错误信息指出具体位置(如 upstreams[1] 缺 command), so that 能直接定位问题条目
5. As 网关运维, I want 命名空间名含下划线时启动被拒, so that 路由拆分歧义在启动时暴露而非运行时静默错路由
6. As 网关运维, I want 上游 name 重复时启动被拒, so that 路由表不会被静默覆盖
7. As 网关运维, I want type 只接受 http/stdio 两个值, so that 拼写错误(如 htpp)立刻暴露而非运行时走进未知分支
8. As 网关运维, I want http 上游缺 url、stdio 上游缺 command 时启动被拒, so that 配置完整性由 config 保证,下游模块零防御
9. As 网关运维, I want 至少声明一个上游, so that 不产生"网关看似正常但什么都没有"的误导状态
10. As 网关运维, I want url 必须是可解析的 http(s) 地址, so that URL 写错在启动时暴露
11. As 开发者, I want Load 返回的结构可完全信任(D8:校验后零防御), so that 后续的 gateway/upstream 构造函数不含任何校验代码
12. As 开发者, I want 超时字段支持 Go duration 字面量(10s/1m), so that 时间单位无歧义
13. As 开发者, I want 未知配置字段报错而非静默忽略, so that 键名 typo 不被吞掉(严格解析)
14. As 开发者, I want config 只被装配入口 import(D7), so that 配置格式变更的波及面恒为装配处一处
15. As 开发者, I want YAML 语法错误(缩进/类型)被包装为带文件名的友好错误, so that 不暴露解析库的内部报错
16. As 开发者, I want 单列表 + type 字段区分上游种类(D6), so that 公共字段(name/timeout)不重复两处、未来加传输类型不改结构
17. As 测试维护者, I want 全部校验规则可表驱动覆盖, so that 新增规则 = 新增一个 case,不动测试框架
18. As CI, I want `go build ./...` 与 `go test ./... -race` 全绿作为本任务完成的机械判据, so that 验收不依赖主观判断
19. As 维护者, I want 每条校验规则都能对应到设计决策与被否候选(D5-D8), so that 后续修改有据可依、取舍可追溯
20. As 网关运维, I want stdio 上游的命令行参数支持数组形式, so that 多参数表达无歧义

## Implementation Decisions

- **模块**:装配入口(cmd/flamingos,薄装配:参数解析 → Load → 组装输出 → 退出;真实服务启动属后续任务)+ config 模块(Load、Config/Upstream 类型、全部校验规则)
- **接口**:`Load(path) → (Config, error)`,全模块唯一公开函数;错误信息汇总全部问题(非首错即停),格式形如 `flamingos.yaml: upstreams[1]: type=stdio 缺少 command`
- **类型形状**(源自设计文档 §4.3,非原型代码):`Config{Listen string(默认 :8080), Upstreams []Upstream}`;`Upstream{Name, Type, URL, Command string, Args []string, Timeout time.Duration(默认 30s)}`
- **校验清单(9 条)**:name 非空;name 仅含字母/数字/连字符(架构 §4.2-R1 字符集,含禁下划线,防路由歧义)(D5);name 不重复;type ∈ {http, stdio};type=http → url 为合法 http(s) URL;type=stdio → command 非空;upstreams ≥ 1;timeout 可解析为 duration;YAML 顶层仅已知字段
- **严格解析**:未知字段报错(解码器 KnownFields 等价能力)。代价:旧二进制读新配置会报错——本项目单二进制开发工具,可接受;收益:typo 不静默
- **YAML 库选型**:gopkg.in/yaml.v3(社区标准、支持严格模式);理由:久经考验优先于新潮(全局代码质量原则)
- **依赖纪律**:config 仅被装配入口 import(D7);下游模块通过构造参数接收值,不读配置
- **默认值常量化**:":8080"、"30s" 等定义为包级常量,无魔法值
- **模块已就位**:Go module 于探针任务阶段建立(模块路径 github.com/magician336/flamingos),本 spec 不重复建

## Testing Decisions

- **唯一接缝**:config.Load(用户已确认)。测试只测公开行为:给定 YAML 文件 → 断言返回结构或错误文案;不测内部拆分出的私有函数。**cmd 层接缝澄清(2026-11-02 review 定案)**:装配入口的进程级行为(退出码约定、摘要文案)以两个纯函数近似测之并加契约测试钉死——这属于 cmd 层自身的接缝,不违反"唯一接缝"(该原则仅约束 config)
- **测试形态**:表驱动 + 每例临时目录夹具;先写测试再实现(TDD;config 包小,目标分支覆盖接近 100%,全仓 ≥80% 目标由后续任务共同达成)
- **覆盖清单**:合法最小配置(默认值生效)、双上游混合(显式值生效)、9 条校验规则各一反例、聚合断言(单文件含两处错误 → 两条都在报错里)、未知字段报错、YAML 语法错误包装、name 含下划线反例(路由歧义防御)
- **既有先例**:本仓库尚无测试先例,本 spec 的表驱动模式将成为后续所有任务的模板(下一个任务起复用)
- 全测试带 `-race` 运行

## Out of Scope

- transport/gateway/upstream 及一切 W2+ 包(目录随模块诞生,D10)
- main 中的真实服务启动与信号处理(下一任务)
- 配置热更新、env 覆盖、多文件合并(不做清单)
- policy/ratelimit/cache 配置段(W2/W3 随模块增补)
- CI 与 lint 工具链(W4 发布清单)

## Further Notes

- 依据链:架构设计 §4.3(config 接口与校验)、§6-D5/D6/D7/D8、§9(里程碑映射)
- spike 已确认 go.mod 存在且 go-sdk v1.8.0 在位;本任务新增依赖仅 yaml.v3
- Windows/UTF-8 无特殊处理:YAML 文件一律 UTF-8,错误信息中的路径原样输出
- 完成判据(CI 可机械验证):`go build ./...` 成功;`go test ./... -race` 全绿;用 spec 内合法夹具跑装配入口能正确输出装配结果,用任一非法夹具能一次性列出全部错误

## 修订

| 日期 | 变更 |
|---|---|
| 2026-11-02 | 实现+双轴 code-review 后定稿:R1 字符集校验落地(替代初稿"仅禁下划线"表述);cmd 纯函数接缝澄清;解码层报错净化(Story 15 补强);url 非法分支补反例(Story 10 验证补全) |
