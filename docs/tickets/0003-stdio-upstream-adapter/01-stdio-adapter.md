# 01:stdio adapter 落地(子进程握手/调用/体面关闭)

**Spec:** [0003 stdio 上游适配器(简化版)](../../../specs/0003-stdio-upstream-adapter.md)
**What to build:** 给定一个 stdio 上游(名称、command、args、timeout),构造出可长期使用的上游客户端:拉得起(构造时启动子进程并完成 initialize 握手,协议版本固定 2025-11-25;命令不存在或握手失败即报携上游名的 TransportError)、列得出(ListTools 返回该上游原始 tool,名字未加命名空间前缀)、调得动(CallTool 以原始 tool 名 + 原始参数字节转发,结果原样返回;慢工具被 timeout 打断)、错得清(一切失败包 TransportError;上游业务失败 IsError 原样透传)、关得掉(Close 幂等,子进程经 SDK 分级终止链退出不留孤儿;Close 后再调用返回 ErrClosed)。测试用真实子进程夹具:仓库内最小 go-sdk stdio server(NewServer + 若干 tool + StdioTransport),测试入口一次性构建为临时二进制。
**Blocked by:** None (can start immediately).
**Status:** done (2026-11-04)

- [x]stdio adapter 实现 upstream.Client,契约与 HTTP adapter 同构(构造即握手、锁内取会话锁外调用、每次调用 WithTimeout、Close 幂等 + ErrClosed)
- [x]构造即拉起子进程并握手;协商结果断言 = 2025-11-25(Pin 字符串单一出处)
- [x]命令不存在/不可执行 → TransportError 点名上游与命令,启动即败
- [x]ListTools/CallTool 经真实子进程往返;参数字节级一致断言;慢工具超时生效
- [x]IsError 应答原样透传不吞不改
- [x]Close 后子进程确实退出(无孤儿)且幂等;Close 后调用返回 errors.Is 可判别的 ErrClosed
- [x]SDK 机器类型(CommandTransport/ClientSession)不出 adapter 文件;config/gateway/transport 零改动
- [x]`go test ./internal/upstream -race` 全绿;`go build ./...` 成功
