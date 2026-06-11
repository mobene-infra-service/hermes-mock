// Package wsagent 让 mock 坐席经 hermes-ws 工作台上线，并切换坐席工作状态。
//
// 上线用 WS `login` action 一步到位（hermes-ws 内部校验口令并签发 token，无需 REST/验证码）：
//
//	连 ws://<wsHost>/agent-workbench/api/ws
//	发 {"action":"login","params":{"username":<number>,"password":md5(ts+明文口令+nonce),"timestamp":<ts>,"nonce":<n>}}
//	收 {"action":"auth","content":{"token":...}} 即上线；每 3s 发 {"action":"ping"} 保活。
//
// 切状态走 call-center REST（其鉴权拦截器在本地被注释，直接注入身份 header 即可）：
//
//	POST <callCenterURL>/agent-workbench/sdk/agent/status/switch  body {"action":<code>}
//	header AGENT_NUMBER_KEY:<number>  ORG_CODE_KEY:<orgCode>
//
// 注意：坐席要被 call-center 预测式拨号选中外呼客户，还需 SIP 注册（onlineSipClient），
// 由 sipagent 负责；本包只管工作台 WS 在线态与工作状态。
package wsagent

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/sirupsen/logrus"

	"hermes-mock/internal/agents"
	"hermes-mock/internal/orgcfg"
	"hermes-mock/internal/tracelog"
)

// hermes AgentStatusEnum code（与 call-center AgentStatusEnum.kt 一致；@JsonValue 用 code 数字）。
const (
	StatusOffline = 1 // 离线
	StatusOnline  = 2 // 在线/空闲（预测式拨号可分配）
	StatusRinging = 3 // 响铃中
	StatusCalling = 4 // 通话中
	StatusDialing = 5 // 呼叫中
	StatusResting = 6 // 小休中
	StatusBusy    = 7 // 忙碌中
	StatusWrapUp  = 8 // 整理中(ACW)
	StatusAutoOut = 9 // 自动外呼
)

// Client mock 坐席工作台客户端（连 hermes-ws）。
// 地址/机构/口令全部从当前机构配置（orgcfg，「机构」页维护）动态取——改配置/切机构即生效，无环境变量。
type Client struct {
	reg  *agents.Registry
	bus  *tracelog.Bus
	orgs *orgcfg.Store
	http *http.Client

	mu       sync.Mutex
	sessions map[string]*session // number -> 会话
}

type session struct {
	number   string
	password string
	token    string
	stop     chan struct{}
}

// New 构造坐席工作台客户端（接入配置经 orgs 动态读取）。
func New(reg *agents.Registry, bus *tracelog.Bus, orgs *orgcfg.Store) *Client {
	return &Client{
		reg: reg, bus: bus, orgs: orgs,
		http:     &http.Client{Timeout: 10 * time.Second},
		sessions: map[string]*session{},
	}
}

// wsHost 当前机构的 hermes-ws 地址（显式 agentWsUrl 或按 direct 服务地址推导）。
func (c *Client) wsHost() string {
	if cfg, ok := c.orgs.CurrentConfig(); ok {
		return cfg.AgentWSHost()
	}
	return ""
}

// callCenterURL 当前机构的 call-center 基址（direct 模式服务地址；gateway 模式回退网关）。
func (c *Client) callCenterURL() string {
	cfg, ok := c.orgs.CurrentConfig()
	if !ok {
		return ""
	}
	if u := strings.TrimSpace(cfg.CallCenterURL); u != "" {
		return u
	}
	return strings.TrimSpace(cfg.GatewayURL)
}

// orgCode 当前机构 code。
func (c *Client) orgCode() string { return c.orgs.Current() }

// defaultPwd 当前机构的坐席默认口令（机构页 defaultAgentPassword；空则兜底）。
func (c *Client) defaultPwd() string {
	if cfg, ok := c.orgs.CurrentConfig(); ok && cfg.DefaultAgentPassword != "" {
		return cfg.DefaultAgentPassword
	}
	return "1234."
}

// Configured 当前机构是否能推导出 hermes-ws 地址。
func (c *Client) Configured() bool { return c.wsHost() != "" }

// Login 让坐席经 hermes-ws 上线（WS login）。password 为空则用机构默认口令。幂等：已在线则重连。
func (c *Client) Login(number, password string) error {
	if c.wsHost() == "" {
		return fmt.Errorf("当前机构未配置 hermes-ws 地址（机构页 agentWsUrl，或 direct 服务地址可推导）")
	}
	if password == "" {
		password = c.defaultPwd()
	}
	c.mu.Lock()
	if old := c.sessions[number]; old != nil {
		close(old.stop)
	}
	s := &session{number: number, password: password, stop: make(chan struct{})}
	c.sessions[number] = s
	c.mu.Unlock()
	c.reg.Ensure(number)
	c.reg.SetWs(number, agents.WsConnecting, "")
	go c.maintain(s)
	go c.sipKeepalive(s)
	return nil
}

// Logout 让坐席下线（断 WS）。
func (c *Client) Logout(number string) {
	c.mu.Lock()
	s := c.sessions[number]
	delete(c.sessions, number)
	c.mu.Unlock()
	if s != nil {
		close(s.stop)
	}
	c.reg.SetWs(number, agents.WsOffline, "")
}

// maintain 维持某坐席的 WS 连接（断线 5s 重连，直到 Logout）。
func (c *Client) maintain(s *session) {
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		err := c.connectOnce(s)
		if err != nil {
			c.reg.SetWs(s.number, agents.WsFailed, err.Error())
			logrus.Warnf("坐席 %s 工作台 WS 断开/失败: %v，5s 后重连", s.number, err)
		}
		select {
		case <-s.stop:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *Client) connectOnce(s *session) error {
	host := c.wsHost()
	if host == "" {
		return fmt.Errorf("当前机构未配置 hermes-ws 地址")
	}
	url := "ws://" + host + "/agent-workbench/api/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, _, _, err := ws.Dial(ctx, url)
	cancel()
	if err != nil {
		return fmt.Errorf("dial %s: %w", url, err)
	}
	// 本次连接结束或 Logout 时关闭连接，解除阻塞读。
	connClosed := make(chan struct{})
	go func() {
		select {
		case <-s.stop:
		case <-connClosed:
		}
		_ = conn.Close()
	}()
	defer close(connClosed)

	// 发 login
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := randHex(8)
	sign := md5hex(ts + s.password + nonce)
	loginMsg, _ := json.Marshal(map[string]any{
		"action": "login",
		"params": map[string]any{"username": s.number, "password": sign, "timestamp": ts, "nonce": nonce},
	})
	if err := wsutil.WriteClientText(conn, loginMsg); err != nil {
		return fmt.Errorf("send login: %w", err)
	}

	// 心跳：每 3s 发 ping
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		ping, _ := json.Marshal(map[string]any{"action": "ping"})
		for {
			select {
			case <-connClosed:
				return
			case <-s.stop:
				return
			case <-t.C:
				if wsutil.WriteClientText(conn, ping) != nil {
					return
				}
			}
		}
	}()

	// 读循环
	for {
		data, err := wsutil.ReadServerText(conn)
		if err != nil {
			return err
		}
		var msg struct {
			Action  string          `json:"action"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Action {
		case "auth":
			var ct struct {
				Token string `json:"token"`
			}
			_ = json.Unmarshal(msg.Content, &ct)
			c.mu.Lock()
			s.token = ct.Token
			c.mu.Unlock()
			c.reg.SetWs(s.number, agents.WsOnline, "")
			c.reg.SetStatus(s.number, agents.StatusOnline) // call-center 在 WS 登录时已把坐席置 ONLINE
			logrus.Infof("坐席 %s 工作台 WS 已上线", s.number)
			sess := c.bus.OpenSession("agent-ws", "坐席 "+s.number+" 工作台上线")
			c.bus.Emit(sess, "agent:"+s.number, tracelog.ChanWS, tracelog.DirIn, "上线",
				"坐席工作台 WS 登录成功", map[string]string{"agent": s.number})
		case "ping":
			pong, _ := json.Marshal(map[string]any{"action": "pong"})
			_ = wsutil.WriteClientText(conn, pong)
		case "kick":
			return fmt.Errorf("被服务端踢出(kick)")
		case "status":
			// hermes-ws 推送的真实坐席工作状态：content 是裸整数(AgentStatusEnum code)。
			// 同步到 registry，使 mock 维护的坐席状态反映 Hermes 真实变化（被分配通话→CALLING、挂机→WRAP_UP 等）。
			var code int
			if json.Unmarshal(msg.Content, &code) == nil && code > 0 {
				c.reg.SetStatus(s.number, workStatusFromCode(code))
			}
			c.emitNotify(s.number, msg.Action, data)
		case "numberInfo", "currentCallUuid", "groupCallNotify", "callTrace":
			c.emitNotify(s.number, msg.Action, data)
		}
	}
}

// emitNotify 把坐席收到的来电/分配/状态通知记入通话链路。
func (c *Client) emitNotify(number, action string, raw []byte) {
	summary := "工作台通知 " + action
	if len(raw) < 200 {
		summary += ": " + string(raw)
	}
	sess := c.bus.OpenSession("agent-ws", "坐席 "+number+" 工作台通知")
	c.bus.Emit(sess, "agent:"+number, tracelog.ChanWS, tracelog.DirIn, action, summary,
		map[string]string{"agent": number, "action": action})
}

// SwitchStatus 切坐席工作状态（直连 call-center，注入身份 header）。statusCode 见上方常量。
func (c *Client) SwitchStatus(number string, statusCode int) error {
	ccURL := c.callCenterURL()
	if ccURL == "" {
		return fmt.Errorf("当前机构未配置 call-center 地址（机构页 callCenterUrl）")
	}
	org := c.orgCode()
	body, _ := json.Marshal(map[string]any{"action": statusCode})
	req, err := http.NewRequest(http.MethodPost, ccURL+"/agent-workbench/sdk/agent/status/switch", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AGENT_NUMBER_KEY", number)
	req.Header.Set("ORG_CODE_KEY", org)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(rb, &r)
	if r.Code != 0 {
		// 坐席登录后 call-center 已自动置 ONLINE(idle)，再切 ONLINE 会报 "...can idle"，视为成功。
		if statusCode == StatusOnline && strings.Contains(r.Msg, "idle") {
			c.reg.SetStatus(number, agents.StatusOnline)
			return nil
		}
		return fmt.Errorf("call-center status/switch code=%d: %s", r.Code, r.Msg)
	}
	c.reg.SetStatus(number, workStatusFromCode(statusCode))
	return nil
}

// sipKeepalive 周期模拟坐席 SIP 在线：调 call-center /public/auth/sip（onlineSipClient，45s TTL），
// 使 call-center 认为坐席 agentIsReady（ws && sip）。
// 注：坐席并未真注册到 FreeSWITCH——这让坐席可被预测式拨号选中、从而外呼客户腿到 mock；
// 但「客户接通后把坐席腿桥进来」需要真实 SIP 注册（属后续"真机"阶段）。
func (c *Client) sipKeepalive(s *session) {
	c.markSipOnce(s)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			c.reg.SetReg(s.number, agents.RegUnregistered, "")
			return
		case <-t.C:
			c.markSipOnce(s)
		}
	}
}

// markSipOnce 自造 SIP digest 调 /public/auth/sip：用坐席明文口令算 response，服务端用同口令校验通过。
func (c *Client) markSipOnce(s *session) {
	ccURL := c.callCenterURL()
	if ccURL == "" {
		return
	}
	realm := "hermes"
	nonce := randHex(8)
	uri := "sip:" + s.number + "@hermes"
	ha1 := md5hex(s.number + ":" + realm + ":" + s.password)
	ha2 := md5hex("REGISTER:" + uri)
	resp := md5hex(ha1 + ":" + nonce + ":" + ha2)
	body, _ := json.Marshal(map[string]any{
		"authorization": "", "algorithm": "MD5",
		"username": s.number, "realm": realm, "nonce": nonce, "uri": uri, "response": resp,
	})
	req, err := http.NewRequest(http.MethodPost, ccURL+"/public/auth/sip", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	r, err := c.http.Do(req)
	if err != nil {
		c.reg.SetReg(s.number, agents.RegFailed, err.Error())
		return
	}
	defer r.Body.Close()
	rb, _ := io.ReadAll(r.Body)
	if strings.TrimSpace(string(rb)) == "1" {
		c.reg.SetReg(s.number, agents.RegRegistered, "")
	} else {
		c.reg.SetReg(s.number, agents.RegFailed, "sip auth rejected")
	}
}

func workStatusFromCode(code int) agents.WorkStatus {
	switch code {
	case StatusOnline:
		return agents.StatusOnline
	case StatusRinging:
		return agents.StatusRinging
	case StatusCalling:
		return agents.StatusCalling
	case StatusDialing:
		return agents.StatusDialing
	case StatusResting:
		return agents.StatusResting
	case StatusBusy:
		return agents.StatusBusy
	case StatusWrapUp:
		return agents.StatusWrapUp
	case StatusAutoOut:
		return agents.StatusAutoOut
	default:
		return agents.StatusOffline
	}
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}
