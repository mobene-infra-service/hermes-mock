// Package sipguard 是 SIP 入口的统一守卫：在「白名单」之外提供不依赖 IP 清单的纵深防御，
// 应对公网 SIP 扫描器（sipvicious 类）刷请求/刷爆 trace 表：
//
//  1. 来源白名单（可选，SIP_ALLOWED_SOURCES）：配置了才限制，没配置不限制 IP；
//  2. 扫描器 UA 指纹（SIP_DENY_USER_AGENTS）：User-Agent 命中指纹的 INVITE 丢弃；
//  3. 违规计数自动临时封禁（fail2ban 思路）：同一 IP 在窗口内累计 N 次违规
//     （UA 指纹命中 / 严格模式下被叫号不在集群配置内）→ 封禁 M 分钟，期间其报文
//     在最早入口丢弃且不落库。**按违规计数而非原始速率**——批量压测时 FS 单 IP
//     高速率发大量合法 INVITE 是常态，按速率限流会误伤自己人。
//
// Guard 同时供 sipagent（应答决策）与 siptrace（落库过滤）使用，保证「丢弃的请求也不进 DB」。
package sipguard

import (
	"net"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"hermes-mock/internal/config"
)

// 内存自保护上限：strikes/bans 表超过此规模直接清空重来（源地址可伪造，
// 不设上限会被随机源 IP 的扫描撑爆内存；清空的代价只是计数重新累计）。
const maxTracked = 8192

// strikeWindow 违规计数窗口：窗口过后计数清零重算。
const strikeWindow = 10 * time.Minute

// Guard SIP 入口守卫。所有方法并发安全。零值不可用，用 New 构建。
type Guard struct {
	allow        func(addr string) bool // 来源白名单判定（未配置时恒 true）
	trusted      func(addr string) bool // 白名单已配置时=allow：可信来源永不被封禁；未配置为 nil
	denyUA       []string               // 扫描器 UA 指纹（小写子串匹配）；空=不启用
	banThreshold int                    // 窗口内违规次数阈值，<=0 禁用自动封禁
	banDuration  time.Duration

	mu      sync.Mutex
	strikes map[string]*strikeState // ip → 窗口内违规计数
	bans    map[string]time.Time    // ip → 封禁到期时间
}

type strikeState struct {
	count       int
	windowStart time.Time
}

// New 由配置构建 Guard。白名单解析失败返回错误（配置写错要在启动期暴露）。
func New(cfg *config.Config) (*Guard, error) {
	allow, restricted, err := cfg.AllowedSourceMatcher()
	if err != nil {
		return nil, err
	}
	g := &Guard{
		allow:        allow,
		banThreshold: cfg.SIPBanThreshold,
		banDuration:  time.Duration(cfg.SIPBanMinutes) * time.Minute,
		strikes:      map[string]*strikeState{},
		bans:         map[string]time.Time{},
	}
	if restricted {
		g.trusted = allow // 白名单内的来源（自己的 FS/Kamailio）永不被违规计数封禁
	}
	for _, tok := range strings.Split(cfg.SIPDenyUserAgents, ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			g.denyUA = append(g.denyUA, strings.ToLower(tok))
		}
	}
	if g.banDuration <= 0 {
		g.banDuration = 30 * time.Minute
	}
	return g, nil
}

// Restricted 报告是否配置了来源白名单（启动日志用）。
func (g *Guard) Restricted() bool { return g.trusted != nil }

// BanEnabled 报告自动封禁是否启用（启动日志用）。
func (g *Guard) BanEnabled() bool { return g.banThreshold > 0 }

// SourceAllowed 判定来源（"ip:port" 或纯 IP）是否放行：须同时通过白名单（若配置）与封禁表。
// sipagent 入口与 siptrace 落库都以此为准，保证被丢弃的来源不产生任何 DB 写入。
func (g *Guard) SourceAllowed(addr string) bool {
	if !g.allow(addr) {
		return false
	}
	return !g.banned(hostOf(addr), time.Now())
}

// DeniedUA 报告 User-Agent 是否命中扫描器指纹（大小写不敏感子串）。空 UA 不算命中。
func (g *Guard) DeniedUA(ua string) bool {
	if ua == "" || len(g.denyUA) == 0 {
		return false
	}
	lua := strings.ToLower(ua)
	for _, pat := range g.denyUA {
		if strings.Contains(lua, pat) {
			return true
		}
	}
	return false
}

// Strike 记一次违规（UA 指纹命中 / 严格模式被叫未命中集群配置）。
// 同一 IP 在 strikeWindow 内累计到 SIP_BAN_THRESHOLD 次 → 封禁 SIP_BAN_MINUTES 分钟（WARN 日志）。
// 白名单内的可信来源（自己的 FS）只记日志、永不封禁——防集群配置一时缺号段把自己人封了。
func (g *Guard) Strike(addr, reason string) {
	if g.banThreshold <= 0 {
		return
	}
	if g.trusted != nil && g.trusted(addr) {
		return
	}
	ip := hostOf(addr)
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.strikes) > maxTracked {
		g.strikes = map[string]*strikeState{}
	}
	st := g.strikes[ip]
	if st == nil || now.Sub(st.windowStart) > strikeWindow {
		st = &strikeState{windowStart: now}
		g.strikes[ip] = st
	}
	st.count++
	if st.count < g.banThreshold {
		return
	}
	delete(g.strikes, ip)
	if len(g.bans) > maxTracked {
		g.bans = map[string]time.Time{}
	}
	g.bans[ip] = now.Add(g.banDuration)
	logrus.WithFields(logrus.Fields{"ip": ip, "reason": reason, "strikes": st.count, "banMinutes": int(g.banDuration.Minutes())}).
		Warn("SIP 来源触发自动临时封禁（疑似扫描器）：期间该 IP 的报文丢弃且不落库")
}

// banned 报告 ip 当前是否在封禁期；顺带惰性清掉已到期的封禁项。
func (g *Guard) banned(ip string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.bans[ip]
	if !ok {
		return false
	}
	if now.After(until) {
		delete(g.bans, ip)
		return false
	}
	return true
}

// hostOf 去掉 "ip:port" 的端口部分（无端口/解析失败原样返回，IPv6 字面量安全）。
func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
