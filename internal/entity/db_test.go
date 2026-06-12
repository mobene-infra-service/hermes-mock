package entity

import (
	"reflect"
	"strings"
	"testing"
)

// 所有需要 AutoMigrate 的 GORM 实体。新增实体请登记到这里。
func allEntities() []any {
	return []any{
		BehaviorProfile{}, CustomerGroup{}, CustomerOverride{}, LineBinding{},
		MockCall{}, TestRun{}, TraceLeg{}, TraceEvent{}, Callback{}, OrgConfig{},
	}
}

// TestIndexedStringColumnsHaveSize 守卫 MySQL Error 1170：
// 带 index/uniqueIndex 的 string 列若无 size，MySQL 上会建成 longtext，
// 而 longtext 不能直接建索引（需 key length）→ AutoMigrate 启动即 1170。
// sqlite 不暴露此问题，故用本静态测试在不连 MySQL 时拦住回归。
func TestIndexedStringColumnsHaveSize(t *testing.T) {
	for _, e := range allEntities() {
		typ := reflect.TypeOf(e)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Type.Kind() != reflect.String {
				continue
			}
			tag := f.Tag.Get("gorm")
			if tag == "" || tag == "-" {
				continue
			}
			indexed := strings.Contains(tag, "index") // 覆盖 index 与 uniqueIndex
			if !indexed {
				continue
			}
			hasSize := strings.Contains(tag, "size:")
			hasShortType := strings.Contains(tag, "type:varchar") || strings.Contains(tag, "type:char")
			if !hasSize && !hasShortType {
				t.Errorf("%s.%s 带索引但无 size（MySQL 会建 longtext → Error 1170）: gorm:%q",
					typ.Name(), f.Name, tag)
			}
		}
	}
}
