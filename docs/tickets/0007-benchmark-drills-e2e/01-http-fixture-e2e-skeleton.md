# 01:HTTP 假上游夹具 + 真二进制黑盒 e2e 骨架与基础端到端

**Spec:** [0007 运行质量实证:vegeta 压测、故障演练与基础 e2e](../../../specs/0007-benchmark-drills-e2e.md)
**What to build:** 网关的"交付形态"获得黑盒判据。顶层 `e2e/` 包诞生(零 import internal,黑盒纪律由编译器守门):可执行 HTTP 假上游夹具落 upstream 包 testdata(echo 回显参数字节 / fail 业务失败 IsError / slow 固定延迟 / pid 自报 PID,归属与构建沿 stdioserver 先例——夹具归 upstream 包所有);harness 沿 cmd 测试 TestMain 同款 go build 模式构建网关二进制与夹具二进制,临时 YAML 配置(字符串模板写临时文件)拉起真网关进程,go-sdk 客户端连真实监听端口。三组判据:①混合上游(http 夹具 + stdio 夹具)经真二进制全链 initialize/tools/list/tools/call,内容往返一致;②协议版本组合三例握手——2026-07-28 最新版握手+全链调用、2025-11-25 上一代版握手+混合上游链路调用(stdio Pin 2025-11-25)、2025-06-18 更老版握手成功(SDK supportedProtocolVersions 五版为声明支持面,不做 5×N 矩阵);③退出码三态契约(0 成功 / 1 配置错误 / 2 启动失败)在真二进制上复核。套件结束进程表无夹具残留(fixturePids 先例)。e2e 进常规回归。
**Blocked by:** 无(可立即开工)。
**Status:** done (2026-11-08)

- [x] upstream testdata 新增 HTTP 假上游可执行夹具,echo/fail/slow/pid 四工具可用,归属与构建方式沿 stdioserver 先例
- [x] e2e 顶层包零 import internal(可机械核对),临时 YAML 拉起真网关二进制,go-sdk 客户端连真实端口
- [x] 全链 e2e:混合上游经真二进制 initialize/tools/list/tools/call 全部成功,内容往返一致
- [x] 协议版本组合三例握手判据通过:2026-07-28 全链 / 2025-11-25 混合上游链路 / 2025-06-18 握手
- [x] 退出码三态复核:正常启停 = 0、配置错误 = 1、启动失败(上游不可达)= 2
- [x] 无孤儿:套件结束后进程表无夹具与网关残留进程
- [x] `go build ./...` + `go test ./... -race` 全部包全绿(e2e 新包进常规回归),既有测试零改动
- [x] diff 检查:新增 e2e 包 + testdata 夹具,internal/ 与 cmd/ 生产文件零改动

> 定档留痕:①协议版本三例的实现路径——SDK v1.8.0 存在公开旋钮 `ClientSessionOptions.ProtocolVersion`(mcp/client.go:250),三例全部经 go-sdk 客户端完成,票面提示的裸 HTTP POST 兜底无需启用;SDK 语义实测:2026-07-28 走 SEP-2575 server/discover(SDK 服务端支持,协商结果仍为 2026-07-28),2025-11-25/2025-06-18 走 legacy initialize"支持即回显"协商(streamable.go ServeHTTP 对 MCP-Protocol-Version 头的五版白名单校验保证后续 POST 可行)。②端口分配:net.Listen 127.0.0.1:0 占位后立即关闭、地址写死进配置模板;上游不可达例的 dead 端口同款手法(cmd deadUpstreamURL 先例,"不碰运气选未监听端口")。③退出码判据手段:exec.ExitError.ExitCode 直读,SIGTERM 经 Process.Signal,监听就绪判据 = TCP 轮询拨号,轮询窗口统一 10s 上限。④进程清理:测试内进程句柄全挂 t.Cleanup(网关 SIGTERM、夹具 Kill,且 Kill+Wait 后才读输出缓冲以避 -race 竞争);TestMain 在 m.Run() 后扫 /proc argv[0] 精确匹配三个二进制路径(fixturePids 先例),发现残留先 SIGKILL 补刀再判套件失败。⑤与票面字面的偏差:判据①超出最低要求,另断言 fail(IsError 透传)/pid(与拉起 PID 精确对照)/slow(全链一次 3s 应答)以实证"四工具可用";版本三例共享同一网关拓扑、各自独立握手(协议版本是会话属性,进程复用不削弱判据);夹具的"扩展空间"以注释钉定票 02 注入形态在既有 tool 表与 handler 装配点追加,不预建未用代码;stdio 直连参照未做——cmd 先例经 upstream.NewStdio 直连在零 import internal 纪律下不可用,改用字面量往返断言(发送字节 == 返回文本),网关透明性已由 cmd 装配级测试钉住。
> 评审收口:补齐 spec 钉定的"伪造的未支持协议版本行为实测记录"缺口(评审:Spec 缺失一项)——protocolversion_test.go 新增第四例:客户端经 `ClientSessionOptions.ProtocolVersion` 报自造版本 "1999-01-01" 握手,真网关实测应答 **2025-11-25**(握手成功、伪造版本静默降级、不报错)。语义与 SDK v1.8.0 源码一致:mcp/client.go:314 请求版生效,字符串比较 "1999-01-01" < "2026-07-28" 故不经 SEP-2575 server/discover 直接走 legacy initialize;mcp/shared.go:76 negotiatedVersion 对不在支持面的请求版回"支持面中小于 2026-07-28 的最新版"(MCP 规范:不支持时 MUST 回另一支持版,SHOULD 为最新支持版);mcp/client.go:395 校验应答版在五版支持面内,会话成立。实测语义已钉进测试注释。
