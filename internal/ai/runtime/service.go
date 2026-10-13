package runtime

// This file has been modified for the KestrelDesk project.

import (
	"context"

	applicationruntime "github.com/Swhshd/kestreldesk/internal/ai/application/runtime"
)

var Service = newService()

func newService() *service {
	return &service{
		app: applicationruntime.NewService(),
	}
}

type service struct {
	app *applicationruntime.Service
}

func (s *service) Run(ctx context.Context, req applicationruntime.RunInput) (*applicationruntime.RunResult, error) {
	return s.app.Run(ctx, req)
}

func (s *service) Resume(ctx context.Context, req applicationruntime.ResumeInput) (*applicationruntime.RunResult, error) {
	return s.app.Resume(ctx, req)
}
