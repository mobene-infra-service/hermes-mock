package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"hermes-mock/internal/config"
)

func TestListListenPorts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/cluster/listen-ports", nil)

	d := &Deps{Cfg: &config.Config{SIPListenPorts: "15060,15061,15060"}}
	d.listListenPorts(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	var got []int
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []int{15060, 15061}
	if len(got) != len(want) {
		t.Fatalf("ports=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ports=%v, want %v", got, want)
		}
	}
}
