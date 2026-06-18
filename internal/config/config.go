package config

import (
	"fmt"
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
