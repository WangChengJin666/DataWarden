package adapter

import "fmt"

// New 按数据源类型构造 Adapter。Redis 不走这里。
func New(cfg ConnConfig) (Adapter, error) {
	switch cfg.Type {
	case "mysql":
		return newMySQL(cfg)
	case "postgresql":
		return newPostgres(cfg)
	case "mongodb":
		return newMongo(cfg)
	default:
		return nil, fmt.Errorf("unsupported sql datasource type: %s", cfg.Type)
	}
}
