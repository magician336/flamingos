# 01:录制器——config 段、mcp 层录制中间件与装配接线

**Spec:** [0008 evalx 最小闭环:工具调用录制、回放与确定性 grader 门禁](../../../specs/0008-evalx-record-replay-grader.md)
**What to build:** 网关获得行为基准采集能力:配置声明 recorder 段(JSONL 路径必填,段缺省含显式 null = 关闭零挂点)后,每次**实际执行**的 tools/call 依序落 JSON-lines 文件。internal/recorder 新包落地(架构 §1 预留接缝诞生):录制中间件挂 mcp 层 Receiving 链末位(RBAC 判定内层,deny 路径不下探故不入录制——基准表征工具处理行为而非策略配置),只处理 tools/call,其余方法原样下探零改动;格式知识自有(audit 同款 U4 裁决,不抽共享 JSONL 包)。记录单行字段:`v`(格式版本)、`seq`(文件内自增)、`time`、`method`(恒 tools/call)、`tool`(聚合名)、`args`(参数原始字节)、`kind`(成功|业务失败|传输错误三态,传输错误文案与 IsError 分开记录)、`result`(成功与业务失败的应答内容,SDK 应答类型经标准 JSON 序列化形态)、`error`、`latency_ms`、`trace_id`(经 telemetry 只读访问器闭包注入,可观测关闭态留空)、`role`(沿 policy 访问器注入,策略关闭态留空)。并发落盘互斥,seq 分配与行序同锁保证一致。cmd 装配沿 audit 先例翻译 config 段并以构造注入访问器;transport 新增接线选项。可演示:录制态配置起真网关→调用工具→检查 JSONL 文件逐字段成立。
**Blocked by:** 无(可立即开工)。
**Status:** done (2026-11-09)

- [x] config 新增 recorder 段:JSONL 路径段存在时必填,段缺省(含显式 null)= 关闭态——零挂点、网关行为与现状逐字节一致
- [x] internal/recorder 落地:中间件挂 Receiving 链末位(判定内层),只录 tools/call,deny 不入录制,其余方法零改动透传
- [x] 记录格式 12 字段齐备且含 v 版本字段;三态 kind 可区分(传输错误文案/业务失败 IsError 与成功应答各自落位)
- [x] trace_id 经 telemetry 只读访问器闭包注入、role 经 policy 访问器注入,对应能力关闭时留空不报错
- [x] 并发调用下 seq 严格递增且与文件行序一致
- [x] 装配级测试沿 audit 先例(httptest 同进程)断言 JSONL 行内容:正常调用、业务失败、传输错误三形态各一例,deny 不入录制一例
- [x] `go build ./...`、`go vet ./...`、`gofmt -l` 触碰文件干净;`go test ./... -race` 全部包全绿,既有测试零改动

> 定档留痕:①实现路径——四处接线沿笔记指认的全部先例:internal/recorder 三件套与 audit 包逐文件同形(recorder.go 的 Recorder+Config 构造注入、middleware.go 的 Receiving 中间件、record.go 的 JSONL 互斥落盘,格式知识自有未抽共享包);config 段四件(rawConfig/rawRecorder/convert/checkRecorder)沿 policy.audit 段同形,但落位为**顶层 recorder 段**而非 policy 子段(录制是 eval 能力而非策略语义,独立开关);transport 的 WithRecorder 与 options.rec 沿 WithAudit 同形,receivingChain 在 RBAC 判定之后追加(链末位=判定内层);cmd 的 buildRecorder 与 buildAudit 同位,两路访问器在此注入(policy.RoleContextKey 的 ctx 键 + telemetry.TraceIDFromContext 只读访问器闭包,后者为 telemetry 包新增的包级只读函数,关闭态/无效 span 上下文返回空串,不依赖 Telemetry 实例)。②落盘口径补充(票面未规定的执行裁决):seq 分配与整行写盘同锁;序列化失败不消耗序号、整条丢弃并记日志(录制文件的 seq 无空洞优先于单条存活,回放端按 seq 对账不受半行脏数据污染);写盘失败沿 audit 口径只记日志不反噬数据面。③真二进制边界实测(可演示项,2026-11-08,httpserver 夹具 + 网关二进制 + 临时 YAML,Stateless 裸 POST 四形态):fx_echo 落 success(seq 1,args `{"msg":"demo"}` 字节级保留,latency_ms 2,role viewer);fx_fail 落 business_failure(result 含 isError:true,error 留空);杀夹具后 fx_echo 落 transport_error(error 原样保留网关错误契约文案"上游 \"fx\" 不可用:……connection refused",result 线上形态为 null);fx_pid 被 RBAC deny(-40001)**零入录制**(文件恰 3 行、grep 计数 0,判定内层装配在真二进制上复核);演示配置无 telemetry 段,trace_id 全空串(可观测关闭态留空实测);seq 1/2/3 与文件行序逐行一致。④关闭态零行为变化的判据:既有装配测试零改动(含 chain_order_test 的链长恰 4 断言——其 fixture 不注入 recorder,录制器仅注入时追加,既有断言原样全绿)+ 新增"段缺省 nil 哨兵零挂点""WithRecorder(nil) 调用照常"两例。⑤与票面字面的偏差:kind 字段取英文字面量 success/business_failure/transport_error(票面写"成功|业务失败|传输错误"未规定字面值)——选英文 token 因消费端是 evalx 引擎与机器可读报告(票 02),中文文案不进机器字段;格式版本锚点 v="1"(FormatVersion 常量)。机械判据:go build/vet 干净、gofmt -l 触碰文件零输出、go test ./... -race 全部包全绿(14 包 ok 含新包 internal/recorder,spike 无测试文件),既有测试零改动。
