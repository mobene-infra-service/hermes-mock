package smsmock

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Adapter 隔离具体厂商线协议。核心状态机永远不读取 CM/NXCloud 等字段；
// 协议升级用新 version 并存，新增厂商只注册一个新 Adapter。
type Adapter interface {
	Provider() string
	ProtocolVersion() string
	Info() ProviderInfo
	// ParseSubmission 接收完整 HTTP 请求，由 Adapter 自己决定 method、query、header、
	// content-type 和 body 语义；核心不假设厂商一定使用 POST + JSON。
	ParseSubmission(request InvokeRequest) (CanonicalSubmission, error)
	SanitizeSubmission(request InvokeRequest) string
	BuildSubmissionResponse(outcomes []MessageOutcome) (WireResponse, error)
	// BuildReceipt 返回完整 DLR transport，允许其它厂商使用不同 method/header/body。
	BuildReceipt(message CanonicalMessage, spec ReceiptSpec, receivedAt time.Time) (WireCallback, error)
	BuildProtocolError(status int, err error) WireResponse
	ValidateCase(spec CaseSpec) error
}

type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry(adapters ...Adapter) *Registry {
	r := &Registry{adapters: map[string]Adapter{}}
	for _, adapter := range adapters {
		r.Register(adapter)
	}
	return r
}

func DefaultRegistry() *Registry { return NewRegistry(NewCMV1Adapter()) }

func adapterKey(provider, version string) string {
	return strings.ToUpper(strings.TrimSpace(provider)) + "/" + strings.ToLower(strings.TrimSpace(version))
}

func (r *Registry) Register(adapter Adapter) {
	if adapter == nil {
		panic("smsmock: nil adapter")
	}
	key := adapterKey(adapter.Provider(), adapter.ProtocolVersion())
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[key]; exists {
		panic(fmt.Sprintf("smsmock: duplicate adapter %s", key))
	}
	r.adapters[key] = adapter
}

func (r *Registry) Get(provider, version string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	adapter, ok := r.adapters[adapterKey(provider, version)]
	return adapter, ok
}

func (r *Registry) Infos() []ProviderInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]ProviderInfo, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		infos = append(infos, adapter.Info())
	}
	sort.Slice(infos, func(i, j int) bool {
		return adapterKey(infos[i].Provider, infos[i].ProtocolVersion) < adapterKey(infos[j].Provider, infos[j].ProtocolVersion)
	})
	return infos
}
