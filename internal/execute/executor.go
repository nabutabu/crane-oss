package execute

import (
	"context"
	"github.com/nabutabu/crane-oss/internal/hostcatalog/service"
)

type DefaultExecutor struct {
	catalog *service.HostCatalogService
}

type Executor interface {
	Execute(ctx context.Context, action *Action) error
}
