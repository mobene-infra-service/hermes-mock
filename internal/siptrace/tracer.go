// Package siptrace 在 sipgo 传输层注册 SIPTracer，捕获 mock 收发的**所有原始 SIP 报文**
// （INVITE/100/180/200/ACK/BYE/REGISTER…），解析出方法/状态、Call-ID、所有头（含 Hermes
// 注入的 X- 业务头），按 Call-ID 聚合进 tracelog 会话。
//
// 这是「真实 SIP 信令观测」的引擎——不在业务 handler 手写摘要，而是传输层自动抓真包。
package siptrace

import (
	"strings"
	"sync"

	"github.com/emiago/sipgo/sip"

	"hermes-mock/internal/tracelog"
)

// Tracer 实现 sip.SIPTracer，把收发报文喂给 tracelog。
type Tracer struct {
	bus *tracelog.Bus
	mu  sync.Mutex
	// callID → bizUUID 映射：INVITE 携带 x-call-uuid，但其响应/ACK/BYE 不回显该业务头，
	// 这里记住同一 SIP 对话(Call-ID)首次见到的 bizUUID，让后续报文聚合到同一业务会话，
	// 避免「200/ACK/BYE 因无业务头而另起一条 Call-ID 会话」的分裂（实测踩坑）。
	cid2biz map[string]string
}

// Install 注册 tracer 到 sipgo 并开启 SIPDebug（传输层据此调用 tracer）。
func Install(bus *tracelog.Bus) {
	sip.SIPDebug = true
	sip.SIPDebugTracer(&Tracer{bus: bus, cid2biz: map[string]string{}})
}

// SIPTraceRead mock 收到的报文（IN）。
func (t *Tracer) SIPTraceRead(transport, laddr, raddr string, msg []byte) {
	t.handle(tracelog.DirIn, transport, laddr, raddr, msg)
}

// SIPTraceWrite mock 发出的报文（OUT）。
func (t *Tracer) SIPTraceWrite(transport, laddr, raddr string, msg []byte) {
	t.handle(tracelog.DirOut, transport, laddr, raddr, msg)
}

func (t *Tracer) handle(dir tracelog.Dir, transport, laddr, raddr string, msg []byte) {
	p := parse(msg)
	if p == nil || p.callID == "" {
		return // 非 SIP / 无 Call-ID（如 keepalive ping）忽略
	}
	// 聚合键：优先用 Hermes 业务 callUuid（让客户腿+坐席腿合并成一通业务通话），
	// 没有业务头则退回 SIP Call-ID（单腿场景）。
	// 关键：同一 Call-ID 的报文，若 INVITE 见过 bizUUID，则其响应/ACK/BYE（不回显业务头）
	// 也复用该 bizUUID，避免同一通话分裂成「INVITE 进 biz 会话、响应另起 callID 会话」。
	aggKey := t.resolveAggKey(p.callID, p.bizUUID)
	title := p.startLine
	if len(title) > 80 {
		title = title[:80]
	}
	sess := t.bus.EnsureByCallID(aggKey, "sip-call", title)
	leg := legOf(p, dir)
	summary := p.summary(dir, raddr)
	// 端点方向：IN=对端(raddr)→本端(laddr)，OUT=本端(laddr)→对端(raddr)。供梯形图画箭头。
	src, dst := laddr, raddr
	if dir == tracelog.DirIn {
		src, dst = raddr, laddr
	}
	t.bus.EmitSIP(sess, leg, dir, p.method, summary, p.headers, string(msg), aggKey, src, dst)
}

// resolveAggKey 决定一条报文的聚合键：
//   - 报文自带 bizUUID（如 INVITE 的 x-call-uuid）：记住 callID→bizUUID，返回 bizUUID。
//   - 报文无 bizUUID 但该 callID 之前见过 bizUUID（响应/ACK/BYE）：返回记住的 bizUUID。
//   - 都没有：返回 callID（单腿/无业务头场景）。
func (t *Tracer) resolveAggKey(callID, bizUUID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if bizUUID != "" {
		t.cid2biz[callID] = bizUUID
		return bizUUID
	}
	if biz, ok := t.cid2biz[callID]; ok {
		return biz
	}
	return callID
}

// parsed 解析后的 SIP 报文要点。
type parsed struct {
	startLine string
	isRequest bool
	method    string // 请求方法 或 响应状态行简写(如 "200")
	callID    string
	bizUUID   string // Hermes 业务 callUuid（从 X-CALL-UUID/X-SESSION-ID 等头提取，多腿聚合）
	from      string
	to        string
	headers   []tracelog.HeaderKV
}

func (p *parsed) summary(dir tracelog.Dir, raddr string) string {
	who := "对端"
	if dir == tracelog.DirIn {
		who = "收自 " + raddr
	} else {
		who = "发往 " + raddr
	}
	if p.isRequest {
		return p.method + " " + who
	}
	return "响应 " + p.startLineCode() + " " + who
}

func (p *parsed) startLineCode() string {
	// "SIP/2.0 200 OK" → "200 OK"
	parts := strings.SplitN(p.startLine, " ", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return p.startLine
}

// parse 解析原始 SIP 报文（仅头部，按 RFC3261 行格式）。
func parse(msg []byte) *parsed {
	text := string(msg)
	// 头与 body 以空行分隔
	headerPart := text
	if i := strings.Index(text, "\r\n\r\n"); i >= 0 {
		headerPart = text[:i]
	} else if i := strings.Index(text, "\n\n"); i >= 0 {
		headerPart = text[:i]
	}
	lines := splitLines(headerPart)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil
	}
	p := &parsed{startLine: strings.TrimSpace(lines[0])}
	// 起始行：请求 "INVITE sip:... SIP/2.0" 或 响应 "SIP/2.0 200 OK"
	if strings.HasPrefix(p.startLine, "SIP/2.0") {
		p.isRequest = false
		fields := strings.Fields(p.startLine)
		if len(fields) >= 2 {
			p.method = fields[1] // 状态码
		}
	} else {
		p.isRequest = true
		fields := strings.Fields(p.startLine)
		if len(fields) >= 1 {
			p.method = fields[0] // INVITE/ACK/BYE/REGISTER…
		}
	}
	if p.method == "" {
		return nil
	}
	// 头部
	for _, ln := range lines[1:] {
		idx := strings.Index(ln, ":")
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(ln[:idx])
		val := strings.TrimSpace(ln[idx+1:])
		p.headers = append(p.headers, tracelog.HeaderKV{Name: name, Value: val})
		lname := strings.ToLower(name)
		switch lname {
		case "call-id", "i":
			if p.callID == "" {
				p.callID = val
			}
		case "from", "f":
			p.from = val
		case "to", "t":
			p.to = val
		}
		// Hermes 业务 callUuid：FS 经 sip_h_X- 注入的业务头（优先级 call-uuid > session-id）。
		if p.bizUUID == "" {
			switch lname {
			case "x-call-uuid", "x-callid", "x-jcallid", "x-call-id":
				p.bizUUID = val
			case "x-session-id", "x-session_id":
				p.bizUUID = val
			}
		}
	}
	return p
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

// legOf 从 From/To 推断腿标识（被叫=分机/客户号）。
func legOf(p *parsed, dir tracelog.Dir) string {
	user := userOf(p.to)
	if user == "" {
		user = userOf(p.from)
	}
	if user == "" {
		return ""
	}
	return user
}

// userOf 从 "\"name\" <sip:5002@host>;tag=.." 取 user 部分。
func userOf(addr string) string {
	i := strings.Index(addr, "sip:")
	if i < 0 {
		i = strings.Index(addr, "sips:")
		if i < 0 {
			return ""
		}
		i += 5
	} else {
		i += 4
	}
	rest := addr[i:]
	if j := strings.IndexAny(rest, "@>;"); j >= 0 {
		return rest[:j]
	}
	return rest
}
