package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/caarlos0/env/v10"
)

// Config 集中声明所有环境变量配置。
// mock 只演被叫客户线路：无主叫/坐席/SIP REGISTER/录音相关配置。
type Config struct {
	// ---- HTTP / Web 配置后台 ----
	HTTPPort int `env:"HTTP_PORT" envDefault:"18080"`

	// ---- SIP agent（diago，被叫 UAS）----
	// FS 把 INVITE 发到这里；mock 作被叫按客户集群行为应答。
	SIPListenIP    string `env:"SIP_LISTEN_IP" envDefault:"0.0.0.0"`
	SIPListenPort  int    `env:"SIP_LISTEN_PORT" envDefault:"15060"`
	SIPListenPorts string `env:"SIP_LISTEN_PORTS" envDefault:"15060,15061,15062,15063,15064,15065,15066,15067,15068,15069"`
	SIPTransport   string `env:"SIP_TRANSPORT" envDefault:"udp"` // udp/tcp/tls
	// 开启后给 UDP 入站请求顶层 Via 补 received/rport，使 sipgo/diago 的响应回到实际包源地址。
	SIPResponseToSource bool `env:"SIP_RESPONSE_TO_SOURCE" envDefault:"false"`
	// SIP 来源白名单：逗号分隔的 CIDR 或裸 IP（如 "172.16.0.0/12,10.0.0.0/8,127.0.0.1"）。
	// 只处理来自这些来源的 SIP 报文（应答 + 落库）；其余（公网 SIP 扫描器等）直接丢弃、不应答、不落库。
	// 空=不限制（向后兼容）；**公网可达部署务必配置**，否则会被扫描器刷爆 mock_trace_event。
	SIPAllowedSources string `env:"SIP_ALLOWED_SOURCES" envDefault:""`
	// 严格被叫校验：true 时，被叫号码不在客户集群配置内（端口绑定组的号段/个例、或任一号段组/个例）
	// 的 INVITE 直接丢弃（不应答、不落库）并对来源记一次违规。扫描器拨的随机分机号几乎不可能命中
	// 配置的测试号段——这是不需要维护 IP 清单的「配置即白名单」。false=保持默认兜底应答（向后兼容）。
	SIPStrictCallee bool `env:"SIP_STRICT_CALLEE" envDefault:"false"`
	// 扫描器 User-Agent 指纹（逗号分隔、大小写不敏感子串匹配）：INVITE 的 UA 命中即丢弃并记违规。空=不启用。
	SIPDenyUserAgents string `env:"SIP_DENY_USER_AGENTS" envDefault:"friendly-scanner,sipvicious,sipcli,sipsak,sundayddr,VaxSIPUserAgent,pplsip"`
	// 自动临时封禁（fail2ban 思路）：同一 IP 在 10 分钟窗口内累计违规达阈值 → 封禁 SIP_BAN_MINUTES 分钟，
	// 期间其 SIP 报文在最早入口丢弃且不落库。<=0 禁用。白名单内来源永不被封禁。
	SIPBanThreshold int `env:"SIP_BAN_THRESHOLD" envDefault:"10"`
	SIPBanMinutes   int `env:"SIP_BAN_MINUTES" envDefault:"30"`
	// 提供给 SDP 协商的音频编解码列表（逗号分隔，按优先级）：PCMU,PCMA,opus。
	Codecs string `env:"CODECS" envDefault:"PCMU,PCMA"`
	// agent 对 FreeSWITCH 暴露的可达 IP（写入 SDP / Contact）。为空时由 diago 尝试按网卡自动探测。
	ExternalIP string `env:"EXTERNAL_IP" envDefault:""`
	// RTP 端口段（按并发开）
	RTPPortStart int `env:"RTP_PORT_START" envDefault:"10000"`
	RTPPortEnd   int `env:"RTP_PORT_END" envDefault:"10999"`

	// ---- 媒体 ----
	AudioDir        string `env:"AUDIO_DIR" envDefault:"assets/audio"`     // 预置 G.711 WAV 目录
	DefaultPlayback string `env:"DEFAULT_PLAYBACK" envDefault:"hello.wav"` // 默认应答后放音

	// ---- 行为默认值（客户集群未命中时的兜底应答）----
	DefaultRingMs int `env:"DEFAULT_RING_MS" envDefault:"2000"`
	DefaultTalkMs int `env:"DEFAULT_TALK_MS" envDefault:"8000"`

	// ---- hermes_mock 库（mock 自身持久化：客户集群/机构配置/呼叫记录/链路/回调；独立库，不碰业务表）----
	// 必配（无纯内存模式）。
	//   DBType=mysql（默认）：DSN_URL 整串优先，否则按组件拼（密码经 ${PROJECT}-secret 注 MYSQL_MASTER_PASSWORD，
	//     地址/库名等经 ${APP}-config 注 DBAddr/DBPort/DBName/DBUser）。
	//   DBType=sqlite：本地零依赖跑（DBPath 文件路径）。
	// Hermes 服务地址 / OpenAPI 凭据 / hermes-ws 等业务接入配置一律在「机构」页（mock_org_config）维护，
	// 不走环境变量。
	DBType     string `env:"DBType" envDefault:"mysql"`
	DSNURL     string `env:"DSN_URL" envDefault:""`
	DBUser     string `env:"DBUser" envDefault:"root"`
	DBPassword string `env:"MYSQL_MASTER_PASSWORD" envDefault:"jQGmXTqEIYZqrClN"`
	// 默认指向测试环境 MySQL（与 hermes 各服务 application-local.yml 一致）。
	DBAddr string `env:"DBAddr" envDefault:"172.16.4.141"`
	DBPort string `env:"DBPort" envDefault:"31509"`
	DBName string `env:"DBName" envDefault:"hermes_mock"`
	DBPath string `env:"DBPath" envDefault:"datas/hermes-mock.db"`

	// 观测数据（呼叫记录 / 链路 / 回调）保留天数：后台周期清理早于此的行，防长期膨胀。
	// <=0 表示不清理（永久保留）。
	ObserveTTLDays int `env:"OBSERVE_TTL_DAYS" envDefault:"7"`

	// 通话链路是否落库（mock_trace_leg / mock_trace_event）。false=只在内存观测、不写 DB（重启即失），
	// 适合不需要持久 SIP 报文、且要彻底杜绝该表膨胀的部署。
	TracePersist bool `env:"TRACE_PERSIST" envDefault:"true"`

	// ---- 日志 ----
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
	Mode     string `env:"MODE" envDefault:"DEV"` // DEV / TEST / PROD
}

// Load 解析环境变量为 Config。
func Load() (*Config, error) {
	c := &Config{}
	if err := env.Parse(c); err != nil {
		return nil, err
	}
	return c, nil
}

// ListenPorts 返回 mock 需要监听的 SIP 端口。
// SIP_LISTEN_PORTS 非空时使用逗号分隔多端口；否则兼容旧的 SIP_LISTEN_PORT。
func (c *Config) ListenPorts() ([]int, error) {
	if strings.TrimSpace(c.SIPListenPorts) == "" {
		return []int{c.SIPListenPort}, nil
	}
	seen := map[int]bool{}
	var ports []int
	for _, raw := range strings.Split(c.SIPListenPorts, ",") {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		port, err := strconv.Atoi(s)
		if err != nil || port <= 0 || port > 65535 {
			return nil, fmt.Errorf("invalid SIP_LISTEN_PORTS port %q", raw)
		}
		if seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("SIP_LISTEN_PORTS is empty")
	}
	return ports, nil
}

// AllowedSourceMatcher 把 SIP_ALLOWED_SOURCES（逗号分隔的 CIDR 或裸 IP）解析成来源判定函数。
// 返回 (matcher, restricted, err)：
//   - restricted=false：未配置白名单 → matcher 恒为 true（放行所有，向后兼容）；
//   - restricted=true：matcher 仅对落在白名单内的来源返回 true。
//
// matcher 入参可带端口（"ip:port"）也可为纯 IP——内部会先尝试 SplitHostPort，
// 这样 SIP handler（req.Source()）与 tracer（raddr）都能直接传入。
func (c *Config) AllowedSourceMatcher() (matcher func(addr string) bool, restricted bool, err error) {
	raw := strings.TrimSpace(c.SIPAllowedSources)
	if raw == "" {
		return func(string) bool { return true }, false, nil
	}
	var nets []*net.IPNet
	var ips []net.IP
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if strings.Contains(tok, "/") {
			_, n, e := net.ParseCIDR(tok)
			if e != nil {
				return nil, false, fmt.Errorf("invalid SIP_ALLOWED_SOURCES CIDR %q: %w", tok, e)
			}
			nets = append(nets, n)
			continue
		}
		ip := net.ParseIP(tok)
		if ip == nil {
			return nil, false, fmt.Errorf("invalid SIP_ALLOWED_SOURCES IP %q", tok)
		}
		ips = append(ips, ip)
	}
	if len(nets) == 0 && len(ips) == 0 {
		return func(string) bool { return true }, false, nil
	}
	matcher = func(addr string) bool {
		host := addr
		if h, _, e := net.SplitHostPort(addr); e == nil {
			host = h
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return false
		}
		for _, n := range nets {
			if n.Contains(ip) {
				return true
			}
		}
		for _, x := range ips {
			if x.Equal(ip) {
				return true
			}
		}
		return false
	}
	return matcher, true, nil
}
