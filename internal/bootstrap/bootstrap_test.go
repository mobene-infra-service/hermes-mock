package bootstrap

import (
	"testing"

	"hermes-mock/internal/cluster"
	"hermes-mock/internal/preflight"
)

// 播种后：行为档/客户组/端口绑定齐全（mock 只演客户腿，无坐席组）。
func TestSeedMakesClusterReady(t *testing.T) {
	clu := cluster.NewMemory() // 单测内存座
	res, err := Seed(clu, Params{ListenPort: 5060})
	if err != nil {
		t.Fatalf("Seed 失败: %v", err)
	}
	if res.CustomerGroup == "" || res.LineBinding == "" || res.ProfileCode == "" || res.ListenPort != 5060 {
		t.Errorf("播种结果不完整: %+v", res)
	}

	// cluster 侧应有 1 客户组 + 1 绑定 + 1 行为档
	if len(clu.ListGroups()) != 1 {
		t.Errorf("应有 1 客户组, got %d", len(clu.ListGroups()))
	}
	if len(clu.ListBindings()) != 1 {
		t.Errorf("应有 1 端口绑定, got %d", len(clu.ListBindings()))
	}

	// 群呼 preflight：配齐 call-center 地址 + 线路后就绪（坐席由真实 Hermes 承担，不卡 mock 坐席）。
	rep := preflight.CallCenterTask(preflight.Inputs{
		CallCenterBaseURL: "http://cc:8080",
		LineDBConnected:   true, LineCount: 1, LineBindings: len(clu.ListBindings()),
	})
	if !rep.Ready {
		t.Errorf("播种+地址后群呼应就绪, checks=%+v", rep.Checks)
	}
}

// 客户组展开的号码应落在号段内（供后续群呼/外呼取号）。
func TestSeedCustomerNumbers(t *testing.T) {
	clu := cluster.NewMemory()
	if _, err := Seed(clu, Params{CustomerPrefix: "861", CustomerStart: 100, CustomerCount: 3, ListenPort: 5061}); err != nil {
		t.Fatal(err)
	}
	g := clu.ListGroups()[0]
	nums := g.Numbers(0)
	if len(nums) != 3 || nums[0] != "861100" || nums[2] != "861102" {
		t.Errorf("客户号展开错: %v", nums)
	}
}

// 幂等：重复播种不应报错也不应翻倍（Upsert 语义）。
func TestSeedIdempotent(t *testing.T) {
	clu := cluster.NewMemory()
	_, _ = Seed(clu, Params{ListenPort: 5060})
	_, err := Seed(clu, Params{ListenPort: 5060})
	if err != nil {
		t.Fatalf("重复播种应成功: %v", err)
	}
	if len(clu.ListGroups()) != 1 {
		t.Errorf("重复播种不应翻倍, 客户组=%d", len(clu.ListGroups()))
	}
}
