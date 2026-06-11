// Package hermesprobe 观测真实 Hermes 栈状态：各服务 HTTP 健康端点探测。
// 探测目标从「当前机构配置」推导（direct 服务地址 + /state/up），不走环境变量；
// 边界：只做 HTTP 只读探测，不直连 Hermes 业务库（DB 级取证一律走 OpenAPI 或人工核对，
// 见 docs/SCOPE.md 边界铁律与 docs/DECISIONS.md 2026-06-10 条）。
package hermesprobe

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"hermes-mock/internal/orgcfg"
)

// Prober 聚合对 Hermes 栈的只读探测能力。
type Prober struct {
	orgs    *orgcfg.Store
	httpCli *http.Client
}

// New 构建 Prober（探测目标从 orgs 当前机构配置动态推导）。
func New(orgs *orgcfg.Store) *Prober {
	return &Prober{orgs: orgs, httpCli: &http.Client{Timeout: 4 * time.Second}}
}

// ServiceHealth 单个 Hermes 服务的健康状态。
type ServiceHealth struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Up      bool   `json:"up"`
	Status  int    `json:"status"`
	Latency int64  `json:"latencyMs"`
	Err     string `json:"err,omitempty"`
}

// Health 并发探测当前机构配置的各服务健康端点（<服务地址>/state/up）。
func (p *Prober) Health() []ServiceHealth {
	eps := p.endpoints()
	out := make([]ServiceHealth, len(eps))
	var wg sync.WaitGroup
	for i, e := range eps {
		wg.Add(1)
		go func(i int, name, url string) {
			defer wg.Done()
			out[i] = p.probeOne(name, url)
		}(i, e.name, e.url)
	}
	wg.Wait()
	return out
}

type endpoint struct{ name, url string }

// endpoints 从当前机构的 direct 服务地址推导健康检查端点（Hermes 健康路径约定 /state/up）。
func (p *Prober) endpoints() []endpoint {
	if p.orgs == nil {
		return nil
	}
	cfg, ok := p.orgs.CurrentConfig()
	if !ok {
		return nil
	}
	var out []endpoint
	add := func(name, base string) {
		base = strings.TrimRight(strings.TrimSpace(base), "/")
		if base == "" {
			return
		}
		out = append(out, endpoint{name: name, url: base + "/state/up"})
	}
	add("basic", cfg.BasicURL)
	add("call-center", cfg.CallCenterURL)
	add("call-bot", cfg.CallBotURL)
	add("otp", cfg.OTPURL)
	return out
}

func (p *Prober) probeOne(name, url string) ServiceHealth {
	h := ServiceHealth{Name: name, URL: url}
	start := time.Now()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	resp, err := p.httpCli.Do(req)
	h.Latency = time.Since(start).Milliseconds()
	if err != nil {
		h.Err = err.Error()
		return h
	}
	defer resp.Body.Close()
	h.Status = resp.StatusCode
	h.Up = resp.StatusCode >= 200 && resp.StatusCode < 400
	return h
}
