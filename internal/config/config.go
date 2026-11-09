// Package config 提供配置加载:读 YAML 文件 → 解析 → 返回 Config。
// 本包是全模块唯一负责读配置的地方,仅被装配入口(cmd/flamingos)import。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/magician336/flamingos/internal/originrule"
	"github.com/magician336/flamingos/internal/toolpattern"
)

// 默认值常量:配置省略可选字段时生效,避免魔法值散落。
const (
	// DefaultListen 是 listen 字段缺省时的监听地址。
	DefaultListen = ":8080"
	// DefaultTimeout 是上游 timeout 字段缺省时的超时时间。
	DefaultTimeout = 30 * time.Second
	// DefaultServiceName 是 telemetry.service_name 缺省时的 Jaeger 服务名。
	DefaultServiceName = "flamingos"
	// DefaultRoleHeader 是 policy.rbac.role_header 缺省时的角色声明头名
	// (spec 0005:头名可配,缺省 mcp-role)。
	DefaultRoleHeader = "mcp-role"
	// DefaultCacheMaxEntries 是 cache.max_entries 缺省时的缓存容量上限
	// (spec 0006:参数基数爆炸不打爆内存的兜底线)。
	DefaultCacheMaxEntries = 1024
)

// cache.rules[].scope 的合法取值(spec 0006:public 全局共享;private 票
// 03 升级为缓存键含角色,本票降级为不缓存)。
const (
	ScopePublic  = "public"
	ScopePrivate = "private"
)

// 上游 type 白名单(设计 D6:单 upstreams 列表 + type 判别)。
const (
	// TypeHTTP 声明 HTTP 传输上游。
	TypeHTTP = "http"
	// TypeStdio 声明 stdio 子进程上游。
	TypeStdio = "stdio"
)

// http 上游 url 允许的 scheme;与 type 判别值是两个概念,值恰好相同。
const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// Config 是配置文件的顶层结构。
type Config struct {
	Listen    string     `yaml:"listen"`    // 监听地址,缺省 DefaultListen
	Upstreams []Upstream `yaml:"upstreams"` // 上游列表(单列表 + type 判别)
	Telemetry *Telemetry `yaml:"telemetry"` // 段缺省(含显式 null)为 nil = tracing 关闭态
	Policy    *Policy    `yaml:"policy"`    // 段缺省(含显式 null)为 nil = 策略全关闭态
	Cache     *Cache     `yaml:"cache"`     // 段缺省(含显式 null)为 nil = 缓存关闭态
	Recorder  *Recorder  `yaml:"recorder"`  // 段缺省(含显式 null)为 nil = 录制关闭态(spec 0008)
}

// Recorder 是 recorder 段的领域模型(spec 0008):行为基准录制的 JSON-lines
// 落盘路径。路径的可写性归 recorder 包构造期校验(启动即败)——配置层不碰
// 文件系统。段缺省(含显式 null)= 关闭态:零挂点,网关行为与未引入录制前
// 逐字节一致。
type Recorder struct {
	Path string `yaml:"path"` // JSON-lines 录制文件路径,段存在时必填
}

// Cache 是 cache 段的领域模型(spec 0006):聚合工具名通配规则表与容量
// 上限。rules 为空(段存在但未声明规则)同样是零行为——分类永不命中,
// 等同关闭态。
type Cache struct {
	Rules      []CacheRule `yaml:"rules"`       // 按声明顺序首条命中生效(优先级=声明序)
	MaxEntries int         `yaml:"max_entries"` // 缓存容量上限,缺省 DefaultCacheMaxEntries
	ListTTLMS  int64       `yaml:"list_ttl_ms"` // tools/list 应答的 TTL hint(毫秒),缺省 0 = 不宣告;必须 ≥ 0
}

// CacheRule 是单条缓存规则:tools 内任一通配模式命中即按本条 ttl_ms/scope
// 定档。ttl_ms 保留声明单位(毫秒),换算为时长是 cache 模块知识、归 cmd
// 装配翻译(架构 D7)。
type CacheRule struct {
	Tools []string `yaml:"tools"`  // 聚合工具名通配模式表(三形态,语法判定经 toolpattern)
	TTLMS int64    `yaml:"ttl_ms"` // 缓存存活时长(毫秒),必须 > 0
	Scope string   `yaml:"scope"`  // ScopePublic | ScopePrivate
}

// Policy 是 policy 段的领域模型:一段四子段(spec 0005),各子段缺省 =
// 该能力关闭;rbac/audit 随票 01/02 落地,origin/rate_limit 随票 03 增补。
type Policy struct {
	RBAC      *RBAC      `yaml:"rbac"`       // 子段缺省(含显式 null)为 nil = RBAC 关闭态
	Audit     *Audit     `yaml:"audit"`      // 子段缺省(含显式 null)为 nil = 审计关闭态
	Origin    *Origin    `yaml:"origin"`     // 子段缺省(含显式 null)为 nil = Origin 校验关闭态(放行)
	RateLimit *RateLimit `yaml:"rate_limit"` // 子段缺省(含显式 null)为 nil = 限流关闭态
}

// Origin 是 policy.origin 子段:Origin 允许列表(MCP Streamable HTTP 规范
// 要求校验 Origin 头防 DNS rebinding)。条目形如 scheme://host[:port],
// 端口可为 `*` 通配(本地开发常用);匹配语义归 transport 中间件,配置层
// 只管语法(启动即败)。
type Origin struct {
	Allowed []string `yaml:"allowed"` // 允许的 Origin 列表,段存在时至少一条
}

// RateLimit 是 policy.rate_limit 子段:全局单桶令牌桶的两个参数。RPS 支持
// 小数(低速上游);Burst 是桶容量(瞬时突发上限),两者都必填——段存在
// 就要能用,rps≤0 / burst<1 是桶永远放行或永远拒绝的误导状态。
type RateLimit struct {
	RPS   float64 `yaml:"rps"`   // 每秒补充令牌数,必须 > 0
	Burst int     `yaml:"burst"` // 桶容量,必须 ≥ 1
}

// Audit 是 policy.audit 子段:工具级审计的 JSON-lines 落盘路径。路径的
// 可写性归 audit 包构造期校验(启动即败)——配置层不碰文件系统。
type Audit struct {
	Path string `yaml:"path"` // JSON-lines 审计文件路径,段存在时必填
}

// RBAC 是 policy.rbac 子段:角色声明头名、默认角色与按角色声明的通配规则表。
type RBAC struct {
	RoleHeader  string               `yaml:"role_header"`  // 角色声明 HTTP 头名,缺省 DefaultRoleHeader
	DefaultRole string               `yaml:"default_role"` // 未带头时的默认角色,段存在时必填
	Roles       map[string]RoleRules `yaml:"roles"`        // 角色 → 规则表,段存在时至少一条
}

// RoleRules 是单个角色的放行/拒绝通配规则表;模式三形态:`*` 全量、
// `ns_*` 前缀、精确名(语法校验见 checkPattern)。
type RoleRules struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

// Telemetry 是 telemetry 段的领域模型(spec 0004 配置段定稿:仅两字段,
// 不为未建功能预埋)。OTLPEndpoint 保留配置原样——裸 host:port 或
// http(s)://host:port 两种形式均通过校验,剥 scheme、定明文/TLS 等
// 规范化是 telemetry 包构造导出器时的内部知识,不进领域模型。
type Telemetry struct {
	OTLPEndpoint string `yaml:"otlp_endpoint"` // OTLP gRPC 接收端,段存在时必填
	ServiceName  string `yaml:"service_name"`  // Jaeger 服务名,缺省 DefaultServiceName
}

// Upstream 是单个上游的声明;公共字段不因传输类型不同而拆两处。
type Upstream struct {
	Name    string        `yaml:"name"`    // 命名空间名(禁下划线、不重复)
	Type    string        `yaml:"type"`    // TypeHTTP | TypeStdio
	URL     string        `yaml:"url"`     // type=http 时的目标地址
	Command string        `yaml:"command"` // type=stdio 时的启动命令
	Args    []string      `yaml:"args"`    // type=stdio 时的命令行参数
	Timeout time.Duration `yaml:"timeout"` // 超时,缺省 DefaultTimeout
}

// yamlNullTag 是 yaml.v3 的 null 标量标签;库未导出该常量,此处定义避免裸字符串。
const yamlNullTag = "!!null"

// rawConfig/rawUpstream 是解码中间结构:与 Config/Upstream 字段一一对应,
// 仅 timeout 保留原始字面量(yaml.Node),使非法值能以 upstreams[i] 定位进聚合报错,
// 而非抛出 yaml.v3 按行计数的英文类型错误。
type rawConfig struct {
	Listen    string        `yaml:"listen"`
	Upstreams []rawUpstream `yaml:"upstreams"`
	Telemetry *rawTelemetry `yaml:"telemetry"`
	Policy    *rawPolicy    `yaml:"policy"`
	Cache     *rawCache     `yaml:"cache"`
	Recorder  *rawRecorder  `yaml:"recorder"`
}

// rawTelemetry 与 Telemetry 字段一一对应;全部是纯标量,无需原始字面量
// 中转,独立成 struct 仅为与 rawConfig/rawUpstream 的两套结构纪律一致。
type rawTelemetry struct {
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	ServiceName  string `yaml:"service_name"`
}

// rawPolicy/rawRBAC/rawRoleRules 与 Policy/RBAC/RoleRules 一一对应;全部
// 是纯标量与切片,独立成 struct 仅为与两套结构纪律一致。
type rawPolicy struct {
	RBAC      *rawRBAC      `yaml:"rbac"`
	Audit     *rawAudit     `yaml:"audit"`
	Origin    *rawOrigin    `yaml:"origin"`
	RateLimit *rawRateLimit `yaml:"rate_limit"`
}

type rawRBAC struct {
	RoleHeader  string                  `yaml:"role_header"`
	DefaultRole string                  `yaml:"default_role"`
	Roles       map[string]rawRoleRules `yaml:"roles"`
}

type rawRoleRules struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

// rawAudit 与 Audit 字段一一对应(纯标量),独立成 struct 仅为与两套
// 结构纪律一致。
type rawAudit struct {
	Path string `yaml:"path"`
}

// rawRecorder 与 Recorder 字段一一对应(纯标量),独立成 struct 仅为与
// 两套结构纪律一致。
type rawRecorder struct {
	Path string `yaml:"path"`
}

// rawOrigin/rawRateLimit 与 Origin/RateLimit 一一对应(纯标量与字符串切片),
// 独立成 struct 仅为与两套结构纪律一致。
type rawOrigin struct {
	Allowed []string `yaml:"allowed"`
}

type rawRateLimit struct {
	RPS   float64 `yaml:"rps"`
	Burst int     `yaml:"burst"`
}

// rawCache/rawCacheRule 与 Cache/CacheRule 一一对应;max_entries 保留原始
// 字面量(yaml.Node),使显式 0/负数与非整数字面量能以 cache 前缀定位进
// 聚合报错,而非与"字段缺省"混同(缺省语义由默认值兜底,显式 ≤0 是配置
// 错误,两者必须可区分——timeout 同款处理)。
type rawCache struct {
	Rules      []rawCacheRule `yaml:"rules"`
	MaxEntries yaml.Node      `yaml:"max_entries"`
	ListTTLMS  int64          `yaml:"list_ttl_ms"` // 纯标量:缺省/显式 null/显式 0 同值 0,无歧义无需原始字面量
}

type rawCacheRule struct {
	Tools []string `yaml:"tools"`
	TTLMS int64    `yaml:"ttl_ms"`
	Scope string   `yaml:"scope"`
}

type rawUpstream struct {
	Name    string    `yaml:"name"`
	Type    string    `yaml:"type"`
	URL     string    `yaml:"url"`
	Command string    `yaml:"command"`
	Args    []string  `yaml:"args"`
	Timeout yaml.Node `yaml:"timeout"`
}

// Load 读取 path 指向的 YAML 配置并解析为 Config。
// 严格解析:未知字段报错而非静默忽略;全部问题一次性汇总返回(非首错即停)。
// 文件不存在或 YAML 语法错误时,返回带文件路径的友好错误。
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("配置文件不存在: %s", path)
		}
		return Config{}, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}

	var raw rawConfig
	problems, err := decodeStrict(&raw, data)
	if err != nil {
		return Config{}, fmt.Errorf("配置文件 %s 不是合法 YAML: %w", path, err)
	}

	cfg, convertProblems := raw.convert()
	problems = append(problems, convertProblems...)
	applyDefaults(&cfg)
	problems = append(problems, validate(cfg)...)
	if len(problems) > 0 {
		return Config{}, aggregateError(path, problems)
	}
	return cfg, nil
}

// convert 将解码中间结构翻译为对外的 Config;timeout 字面量在此统一解析,
// 非法值产出带条目位置的问题。
func (rc rawConfig) convert() (Config, []string) {
	var problems []string
	cfg := Config{Listen: rc.Listen}
	for i, ru := range rc.Upstreams {
		timeout, probs := parseTimeout(upstreamPos(i), ru.Timeout)
		problems = append(problems, probs...)
		cfg.Upstreams = append(cfg.Upstreams, Upstream{
			Name:    ru.Name,
			Type:    ru.Type,
			URL:     ru.URL,
			Command: ru.Command,
			Args:    ru.Args,
			Timeout: timeout,
		})
	}
	if rc.Telemetry != nil { // 段缺省(含显式 null)保持 nil 指针 = 关闭态
		cfg.Telemetry = &Telemetry{
			OTLPEndpoint: rc.Telemetry.OTLPEndpoint,
			ServiceName:  rc.Telemetry.ServiceName,
		}
	}
	if rc.Policy != nil { // 段缺省(含显式 null)保持 nil 指针 = 策略全关闭态
		policy := &Policy{}
		if rawRbac := rc.Policy.RBAC; rawRbac != nil { // 子段缺省 = RBAC 关闭
			roles := make(map[string]RoleRules, len(rawRbac.Roles))
			for name, rr := range rawRbac.Roles {
				roles[name] = RoleRules{Allow: rr.Allow, Deny: rr.Deny}
			}
			policy.RBAC = &RBAC{
				RoleHeader:  rawRbac.RoleHeader,
				DefaultRole: rawRbac.DefaultRole,
				Roles:       roles,
			}
		}
		if rawAudit := rc.Policy.Audit; rawAudit != nil { // 子段缺省 = 审计关闭
			policy.Audit = &Audit{Path: rawAudit.Path}
		}
		if rawOrigin := rc.Policy.Origin; rawOrigin != nil { // 子段缺省 = Origin 校验关闭
			policy.Origin = &Origin{Allowed: rawOrigin.Allowed}
		}
		if rawRL := rc.Policy.RateLimit; rawRL != nil { // 子段缺省 = 限流关闭
			policy.RateLimit = &RateLimit{RPS: rawRL.RPS, Burst: rawRL.Burst}
		}
		cfg.Policy = policy
	}
	if rc.Cache != nil { // 段缺省(含显式 null)保持 nil 指针 = 缓存关闭态
		maxEntries, probs := parseMaxEntries(rc.Cache.MaxEntries)
		problems = append(problems, probs...)
		var rules []CacheRule // 规则缺省保持 nil(零规则零行为,与关闭态同形)
		for _, rr := range rc.Cache.Rules {
			rules = append(rules, CacheRule{Tools: rr.Tools, TTLMS: rr.TTLMS, Scope: rr.Scope})
		}
		cfg.Cache = &Cache{Rules: rules, MaxEntries: maxEntries, ListTTLMS: rc.Cache.ListTTLMS}
	}
	if rc.Recorder != nil { // 段缺省(含显式 null)保持 nil 指针 = 录制关闭态
		cfg.Recorder = &Recorder{Path: rc.Recorder.Path}
	}
	return cfg, problems
}

// parseTimeout 解析 timeout 节点的原始字面量;字段缺省(零值节点)或显式 null
// 时返回 0,由默认值兜底;非法字面量产出带条目位置的问题。
func parseTimeout(pos string, node yaml.Node) (time.Duration, []string) {
	if node.Kind == 0 || node.Tag == yamlNullTag {
		return 0, nil
	}
	d, err := time.ParseDuration(node.Value)
	if err == nil {
		return d, nil
	}
	return 0, []string{fmt.Sprintf("%s: timeout %q 无法解析为时长(支持 Go duration 字面量,如 10s、1m)", pos, node.Value)}
}

// parseMaxEntries 解析 max_entries 节点的原始字面量;字段缺省(零值节点)
// 或显式 null 返回 0,由默认值兜底。显式 0/负数不做静默修正(0 让容量上限
// 形同虚设),非整数字面量同样拒绝——问题交聚合报错,启动即败(票 0006-02)。
func parseMaxEntries(node yaml.Node) (int, []string) {
	if node.Kind == 0 || node.Tag == yamlNullTag {
		return 0, nil
	}
	n, err := strconv.Atoi(node.Value)
	if err != nil {
		return 0, []string{fmt.Sprintf("cache: max_entries %q 无法解析为整数", node.Value)}
	}
	if n <= 0 {
		return 0, []string{fmt.Sprintf("cache: max_entries 必须为正整数(当前 %d:0 或负数让容量上限形同虚设)", n)}
	}
	return n, nil
}

// validate 对解码后的 Config 逐条执行结构性校验规则,返回全部问题(带条目位置)。
// 每条规则一个 check 函数,这里只做循环收集(设计 D8:启动时一次性全量校验)。
func validate(cfg Config) []string {
	var problems []string
	if len(cfg.Upstreams) == 0 {
		problems = append(problems, "upstreams: 至少声明一个上游")
	}
	seen := make(map[string]bool, len(cfg.Upstreams))
	for i, u := range cfg.Upstreams {
		pos := upstreamPos(i)
		problems = append(problems, checkNamePresent(pos, u.Name)...)
		problems = append(problems, checkNameCharset(pos, u.Name)...)
		problems = append(problems, checkNameUnique(pos, u.Name, seen)...)
		problems = append(problems, checkTypeKnown(pos, u.Type)...)
		problems = append(problems, checkTypeFields(pos, u)...)
	}
	problems = append(problems, checkTelemetry(cfg.Telemetry)...)
	problems = append(problems, checkPolicy(cfg.Policy)...)
	problems = append(problems, checkCache(cfg.Cache)...)
	problems = append(problems, checkRecorder(cfg.Recorder)...)
	return problems
}

// upstreamPos 生成上游条目的定位前缀,如 upstreams[1]。
func upstreamPos(i int) string {
	return fmt.Sprintf("upstreams[%d]", i)
}

// checkNamePresent 校验 name 非空:空名无法参与路由。
func checkNamePresent(pos, name string) []string {
	if name != "" {
		return nil
	}
	return []string{pos + ": name 不能为空"}
}

// checkNameCharset 校验命名空间名符合 R1 字符集(设计文档 §4.2:仅
// [a-zA-Z0-9-])。下划线被排除是因为路由按第一个 _ 拆分(设计 D5),
// 空格、点等其余字符一并排除,保证命名空间是安全的名字 token。
func checkNameCharset(pos, name string) []string {
	if nameCharsetOK(name) {
		return nil
	}
	return []string{fmt.Sprintf("%s: name %q 只允许字母、数字、连字符(下划线等其他字符均不允许:路由按第一个 _ 拆分,会造成歧义)", pos, name)}
}

// nameCharsetOK 判断 name 是否全部由 R1 字符集 [a-zA-Z0-9-] 组成;
// 空串返回 true,空名由 checkNamePresent 单独负责,避免重复计错。
func nameCharsetOK(name string) bool {
	if name == "" {
		return true
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

// checkNameUnique 校验 name 不重复:重复条目会静默覆盖路由表。
// 空名由 checkNamePresent 负责,此处跳过以免重复计错。
func checkNameUnique(pos, name string, seen map[string]bool) []string {
	if name == "" {
		return nil
	}
	if seen[name] {
		return []string{fmt.Sprintf("%s: name %q 重复(会静默覆盖已有路由)", pos, name)}
	}
	seen[name] = true
	return nil
}

// checkTypeKnown 校验 type 属于白名单:拼写错误立刻暴露而非运行时走进未知分支。
func checkTypeKnown(pos, typ string) []string {
	switch typ {
	case TypeHTTP, TypeStdio:
		return nil
	}
	return []string{fmt.Sprintf("%s: type %q 不受支持,只接受 %s 或 %s", pos, typ, TypeHTTP, TypeStdio)}
}

// checkTypeFields 校验按 type 判别的必填字段:http 需合法 http(s) url,stdio 需非空 command。
// type 非法时此处不重复计错,由 checkTypeKnown 负责。
func checkTypeFields(pos string, u Upstream) []string {
	switch u.Type {
	case TypeHTTP:
		return checkHTTPURL(pos, u.URL)
	case TypeStdio:
		if u.Command != "" {
			return nil
		}
		return []string{pos + ": type=stdio 缺少 command"}
	}
	return nil
}

// checkHTTPURL 校验 http 上游的 url 可解析、scheme 为 http(s) 且带主机名。
func checkHTTPURL(pos, rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != schemeHTTP && u.Scheme != schemeHTTPS) || u.Host == "" {
		return []string{fmt.Sprintf("%s: type=http 的 url 必须是合法 http(s) 地址,实际 %q", pos, rawURL)}
	}
	return nil
}

// checkTelemetry 校验 telemetry 段(缺省 nil 时跳过——关闭态是合法形态,
// 校验的只是"段存在就要能用"):endpoint 缺失或非法即配置错误,让部署
// 在启动瞬间失败而非等到 span 全部悄然丢失才发现(spec 0004 Story 14)。
func checkTelemetry(t *Telemetry) []string {
	if t == nil {
		return nil
	}
	if t.OTLPEndpoint == "" {
		return []string{"telemetry: 段存在但缺少 otlp_endpoint(不需要 tracing 时请整段删除,需要时填 OTLP 接收端,如 localhost:4317)"}
	}
	if !otlpEndpointOK(t.OTLPEndpoint) {
		return []string{fmt.Sprintf("telemetry: otlp_endpoint %q 不合法(应为 host:port 或 http(s)://host:port)", t.OTLPEndpoint)}
	}
	return nil
}

// otlpEndpointOK 判断 OTLP endpoint 是否为两种受支持形式之一:完整
// http(s):// URL(带主机),或裸 host:port(端口为数字;IPv6 走方括号
// 形式)。与 telemetry 包构造导出器时的解析对形式的认定保持一致。
func otlpEndpointOK(endpoint string) bool {
	if u, err := url.Parse(endpoint); err == nil &&
		(u.Scheme == schemeHTTP || u.Scheme == schemeHTTPS) && u.Host != "" {
		return true
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" || port == "" {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 0 && n <= 65535
}

// checkPolicy 校验 policy 段:段缺省 nil 时跳过(全关闭是合法形态);rbac
// 与 audit 子段各自独立开关,子段缺省时各自跳过。子段存在就要能用:
// default_role/roles 缺失与通配模式语法错误均启动即败,中文点名位置
// (spec 0005 Story 12)。
func checkPolicy(p *Policy) []string {
	if p == nil {
		return nil
	}
	var problems []string
	if rbac := p.RBAC; rbac != nil {
		problems = append(problems, checkRBAC(rbac)...)
	}
	if audit := p.Audit; audit != nil {
		problems = append(problems, checkAudit(audit)...)
	}
	if origin := p.Origin; origin != nil {
		problems = append(problems, checkOrigin(origin)...)
	}
	if rl := p.RateLimit; rl != nil {
		problems = append(problems, checkRateLimit(rl)...)
	}
	return problems
}

// checkRBAC 校验 policy.rbac 子段(default_role/roles 必填与通配语法)。
func checkRBAC(rbac *RBAC) []string {
	var problems []string
	if rbac.DefaultRole == "" {
		problems = append(problems, "policy.rbac: 缺少 default_role(rbac 段存在时必须显式声明未带头请求的默认角色;不需要 RBAC 时请整段删除)")
	}
	if len(rbac.Roles) == 0 {
		problems = append(problems, "policy.rbac: 缺少 roles(rbac 段存在时至少声明一个角色的规则表,空规则表等于全量拒绝的误导状态)")
	}
	// 角色名按字典序遍历,保证聚合报错顺序确定、可读。
	for _, name := range slices.Sorted(maps.Keys(rbac.Roles)) {
		rules := rbac.Roles[name]
		pos := rbacPos(name)
		if name == "" {
			problems = append(problems, fmt.Sprintf("%s: 角色名不能为空(空角色名无法被任何请求命中)", pos))
		}
		for i, pat := range rules.Allow {
			problems = append(problems, checkPattern(fmt.Sprintf("%s.allow[%d]", pos, i), pat)...)
		}
		for i, pat := range rules.Deny {
			problems = append(problems, checkPattern(fmt.Sprintf("%s.deny[%d]", pos, i), pat)...)
		}
	}
	return problems
}

// checkAudit 校验 policy.audit 子段:path 必填。路径的可写性不在配置层
// 校验(配置层不碰文件系统),由 audit 包构造期启动即败(Story 9)。
func checkAudit(a *Audit) []string {
	if a.Path != "" {
		return nil
	}
	return []string{"policy.audit: 缺少 path(audit 段存在时必须声明 JSON-lines 审计文件的落盘路径;不需要审计时请整段删除)"}
}

// checkRecorder 校验 recorder 段(spec 0008 票 01):段缺省 nil 时跳过
// (关闭态是合法形态);段存在则 path 必填——路径的可写性不在配置层校验
// (配置层不碰文件系统),由 recorder 包构造期启动即败。
func checkRecorder(r *Recorder) []string {
	if r == nil {
		return nil
	}
	if r.Path != "" {
		return nil
	}
	return []string{"recorder: 缺少 path(recorder 段存在时必须声明 JSON-lines 录制文件的落盘路径;不需要录制时请整段删除)"}
}

// rbacPos 生成角色条目的定位前缀,如 policy.rbac.roles.viewer。
func rbacPos(role string) string {
	return fmt.Sprintf("policy.rbac.roles.%s", role)
}

// checkOrigin 校验 policy.origin 子段:allowed 至少一条(空列表等于全量
// 403 的误导状态),逐条做格式校验(spec 0005 Story 14;条目的匹配语义归
// transport 中间件,配置层只管语法)。
func checkOrigin(o *Origin) []string {
	if len(o.Allowed) == 0 {
		return []string{"policy.origin: 缺少 allowed(origin 段存在时至少声明一个允许的 Origin;不需要 Origin 校验时请整段删除)"}
	}
	var problems []string
	for i, entry := range o.Allowed {
		problems = append(problems, checkOriginEntry(fmt.Sprintf("policy.origin.allowed[%d]", i), entry)...)
	}
	return problems
}

// checkOriginEntry 校验单个 Origin 允许条目:形如 scheme://host[:port]——
// scheme 限 http(s);host 非空且不含路径;端口为缺省、`*` 通配或 1-65535
// 的数字(Origin 序列化不含路径与查询,带即拒绝)。
// checkOriginEntry 校验单个 Origin 允许条目:语法知识的单一出处是
// originrule 叶包(与 transport 的匹配共用同一 Parse——2026-11-05 架构评审
// C1,原双份实现与跨包等价测试由此退役),本函数只补配置位置前缀,保持
// "启动即败、中文点名到具体条目"的话语权纪律(spec 0001)。
func checkOriginEntry(pos, entry string) []string {
	if _, err := originrule.Parse(entry); err != nil {
		return []string{fmt.Sprintf("%s: %v", pos, err)}
	}
	return nil
}

// checkRateLimit 校验 policy.rate_limit 子段:rps>0 与 burst≥1,两者都必填
// ——0/负值是桶永远放行(rps 无穷大错觉)或永远拒绝的误导状态,启动即败
// 让配置错误活不到被流量发现(spec 0005 Story 13)。NaN 同样落入 rps 检查
// (NaN>0 为 false)。
func checkRateLimit(rl *RateLimit) []string {
	var problems []string
	if !(rl.RPS > 0) {
		problems = append(problems, fmt.Sprintf("policy.rate_limit: 缺少 rps 或不大于 0(rate_limit 段存在时 rps 必须为正数,当前 %v;不需要限流时请整段删除)", rl.RPS))
	}
	if rl.Burst < 1 {
		problems = append(problems, fmt.Sprintf("policy.rate_limit: 缺少 burst 或小于 1(burst 是桶容量,至少为 1,当前 %d)", rl.Burst))
	}
	return problems
}

// checkCache 校验 cache 段(票 0006-02;list_ttl_ms 随票 03 增补):段缺省
// nil 时跳过(关闭态是合法形态),rules 为空(含显式空表)同样零行为合法;
// 规则存在就要能用——tools 死规则、模式语法(判定单一出处 toolpattern)、
// ttl_ms≤0、scope 白名单外均启动即败,中文点名位置(spec 0006 Story 10)。
// 段级 list_ttl_ms 必须 ≥ 0(负数无法宣告;缺省含显式 null/显式 0 同值 0
// = 不宣告,零行为)。max_entries 的 ≤0/非整数校验在 convert 站
// (parseMaxEntries,显式值与缺省可区分)。
func checkCache(c *Cache) []string {
	if c == nil {
		return nil
	}
	var problems []string
	if c.ListTTLMS < 0 {
		problems = append(problems, fmt.Sprintf("cache: list_ttl_ms 必须为非负整数(当前 %d:负的 TTL 无法宣告,立即过期应留空或显式 0)", c.ListTTLMS))
	}
	for i, r := range c.Rules {
		pos := fmt.Sprintf("cache.rules[%d]", i)
		if len(r.Tools) == 0 {
			problems = append(problems, fmt.Sprintf("%s: 缺少 tools(规则至少声明一个聚合工具名通配模式,空表是永不命中的死规则)", pos))
		}
		for j, pat := range r.Tools {
			problems = append(problems, checkPattern(fmt.Sprintf("%s.tools[%d]", pos, j), pat)...)
		}
		if r.TTLMS <= 0 {
			problems = append(problems, fmt.Sprintf("%s: ttl_ms 必须为正整数(当前 %d:没有正 TTL 的规则永不生效,是误导状态)", pos, r.TTLMS))
		}
		switch r.Scope {
		case ScopePublic, ScopePrivate:
		default:
			problems = append(problems, fmt.Sprintf("%s: scope %q 不受支持,只接受 %s 或 %s", pos, r.Scope, ScopePublic, ScopePrivate))
		}
	}
	return problems
}

// checkPattern 校验单个通配模式的语法:语法知识的单一出处是 toolpattern
// 叶包(票 0006-01 移交项、originrule C1 先例——原双份实现退役,Parse 的
// 错误文案已按本函数冒号后段逐字节对齐,上报文案零变化),本函数只补配置
// 位置前缀,保持"启动即败、中文点名到具体条目"的话语权纪律(spec 0001)。
// policy 与 cache 两个消费方共享同一份语法。
func checkPattern(pos, pat string) []string {
	if _, err := toolpattern.Parse(pat); err != nil {
		return []string{fmt.Sprintf("%s: %v", pos, err)}
	}
	return nil
}

// decodeStrict 严格解析 YAML:开启 KnownFields 后,未知字段等解码层问题
// 净化为中文问题列表返回(Story 15:不泄漏解析库英文原文与内部类型名);
// 语法级错误(无法继续解码)原样返回,由 Load 包装为"不是合法 YAML"。
// 空文件(io.EOF)是合法 YAML,返回零值交由校验层报"至少一个上游"。
func decodeStrict(raw *rawConfig, data []byte) ([]string, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var problems []string
	if err := dec.Decode(raw); err != nil && !errors.Is(err, io.EOF) {
		var typeErr *yaml.TypeError
		if !errors.As(err, &typeErr) {
			return nil, err
		}
		problems = append(problems, sanitizeTypeError(typeErr)...)
	}
	return problems, nil
}

// yaml.v3 解码错误条目的可识别模式;净化只认这两类,其余一律降级为通用
// 中文描述,保证英文原文与内部类型名(rawConfig/rawUpstream)零泄漏。
var (
	// reUnknownField 匹配未知字段条目:"field X not found in type ..."。
	reUnknownField = regexp.MustCompile(`field (\S+) not found in type`)
	// reLinePrefix 匹配条目的 "line N: " 行号前缀。
	reLinePrefix = regexp.MustCompile(`^line (\d+): `)
)

// sanitizeTypeError 把 yaml.v3 的 TypeError 逐条净化为中文问题:只保留
// 行号、字段名等部署者定位所需的信息(Story 15)。
func sanitizeTypeError(typeErr *yaml.TypeError) []string {
	problems := make([]string, 0, len(typeErr.Errors))
	for _, entry := range typeErr.Errors {
		problems = append(problems, sanitizeDecodeProblem(entry))
	}
	return problems
}

// sanitizeDecodeProblem 净化单条解码错误:未知字段与类型不匹配两类有专属
// 中文表述;未识别模式降级为通用描述,绝不透传库的英文原文。
func sanitizeDecodeProblem(entry string) string {
	line, rest := splitLineInfo(entry)
	if m := reUnknownField.FindStringSubmatch(rest); m != nil {
		return withLine(line, fmt.Sprintf("字段 %q 无法识别", m[1]))
	}
	if strings.Contains(rest, "cannot unmarshal") {
		return withLine(line, "字段的值类型不正确")
	}
	return withLine(line, "配置内容不符合结构要求")
}

// splitLineInfo 从解码错误条目拆出 "line N: " 行号前缀与正文;
// 无前缀时 line 返回空串。
func splitLineInfo(entry string) (line, rest string) {
	if m := reLinePrefix.FindStringSubmatch(entry); m != nil {
		return m[1], entry[len(m[0]):]
	}
	return "", entry
}

// withLine 拼装带行号的中文问题;原文无行号前缀时不输出行号段。
func withLine(line, msg string) string {
	if line == "" {
		return msg
	}
	return fmt.Sprintf("第 %s 行: %s", line, msg)
}

// aggregateError 将全部问题汇成单个错误:单条问题时输出"文件: 位置: 原因"单行格式,
// 多条问题时逐行列出,保证部署者一次看到全部问题而非逐次试错。
func aggregateError(path string, problems []string) error {
	if len(problems) == 1 {
		return fmt.Errorf("%s: %s", path, problems[0])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: 配置校验失败,共 %d 处问题:", path, len(problems))
	for _, p := range problems {
		fmt.Fprintf(&b, "\n  - %s", p)
	}
	return errors.New(b.String())
}

// applyDefaults 为省略的可选字段填充默认值。
func applyDefaults(cfg *Config) {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	for i := range cfg.Upstreams {
		if cfg.Upstreams[i].Timeout == 0 {
			cfg.Upstreams[i].Timeout = DefaultTimeout
		}
	}
	if cfg.Telemetry != nil && cfg.Telemetry.ServiceName == "" {
		cfg.Telemetry.ServiceName = DefaultServiceName
	}
	if cfg.Policy != nil && cfg.Policy.RBAC != nil && cfg.Policy.RBAC.RoleHeader == "" {
		cfg.Policy.RBAC.RoleHeader = DefaultRoleHeader
	}
	if cfg.Cache != nil && cfg.Cache.MaxEntries == 0 {
		cfg.Cache.MaxEntries = DefaultCacheMaxEntries
	}
}
