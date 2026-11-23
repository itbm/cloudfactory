package server

import (
	"context"

	"connectrpc.com/connect"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
)

// ExportWebsite starts the job that sends the parts of an account export to the panel
// (docs/account-export.md).
func (s *Service) ExportWebsite(_ context.Context, r *connect.Request[agentv1.ExportWebsiteRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.ExportWebsite(r.Msg))
}
