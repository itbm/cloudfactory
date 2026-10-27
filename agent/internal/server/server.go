// Package server serves the agent API. It accepts gRPC and the Connect protocol on one
// port, over mutual TLS. It accepts only the client certificate of the panel (FR-1706).
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1/agentv1connect"
	"github.com/itbm/cloudfactory/agent/internal/actions"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/pki"
	"github.com/itbm/cloudfactory/agent/internal/status"
)

// Service implements AgentService.
type Service struct {
	Deps   *actions.Deps
	Runner *jobs.Runner
	Status status.Collector
}

var _ agentv1connect.AgentServiceHandler = (*Service)(nil)

// submit runs the job of a request. A request that the agent cannot accept gets
// "invalid argument", and the agent does not change the host.
func (s *Service) submit(job jobs.Job, err error) (*connect.Response[agentv1.JobAccepted], error) {
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	dup, err := s.Runner.Submit(job)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentv1.JobAccepted{JobId: job.ID, Duplicate: dup}), nil
}

func (s *Service) CreateWebsite(_ context.Context, r *connect.Request[agentv1.WebsiteRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.CreateWebsite(r.Msg))
}

func (s *Service) DeleteWebsite(_ context.Context, r *connect.Request[agentv1.DeleteWebsiteRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.DeleteWebsite(r.Msg))
}

func (s *Service) SetWebsitePower(_ context.Context, r *connect.Request[agentv1.SetWebsitePowerRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetWebsitePower(r.Msg))
}

func (s *Service) SetLimits(_ context.Context, r *connect.Request[agentv1.SetLimitsRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetLimits(r.Msg))
}

func (s *Service) SetRuntime(_ context.Context, r *connect.Request[agentv1.WebsiteRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetRuntime(r.Msg))
}

func (s *Service) AddDomain(_ context.Context, r *connect.Request[agentv1.DomainRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.AddDomain(r.Msg))
}

func (s *Service) RemoveDomain(_ context.Context, r *connect.Request[agentv1.DomainRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.RemoveDomain(r.Msg))
}

func (s *Service) CreateDatabase(_ context.Context, r *connect.Request[agentv1.CreateDatabaseRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.CreateDatabase(r.Msg))
}

func (s *Service) DeleteDatabase(_ context.Context, r *connect.Request[agentv1.DeleteDatabaseRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.DeleteDatabase(r.Msg))
}

func (s *Service) SetDatabaseUser(_ context.Context, r *connect.Request[agentv1.SetDatabaseUserRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetDatabaseUser(r.Msg))
}

func (s *Service) DeleteDatabaseUser(_ context.Context, r *connect.Request[agentv1.DeleteDatabaseUserRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.DeleteDatabaseUser(r.Msg))
}

func (s *Service) Backup(_ context.Context, r *connect.Request[agentv1.BackupRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.Backup(r.Msg))
}

func (s *Service) Restore(_ context.Context, r *connect.Request[agentv1.RestoreRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.Restore(r.Msg))
}

func (s *Service) InstallHostUpdates(_ context.Context, r *connect.Request[agentv1.HostRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.InstallHostUpdates(r.Msg))
}

func (s *Service) Reboot(_ context.Context, r *connect.Request[agentv1.HostRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.RebootJob(r.Msg))
}

func (s *Service) UpdateAgent(_ context.Context, r *connect.Request[agentv1.UpdateAgentRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.UpdateAgent(r.Msg))
}

func (s *Service) GetStatus(ctx context.Context, _ *connect.Request[agentv1.GetStatusRequest]) (*connect.Response[agentv1.HostStatus], error) {
	r := s.Status.Report(ctx)
	out := &agentv1.HostStatus{AgentVersion: r.AgentVersion, Kernel: r.Kernel, PodmanVersion: r.PodmanVersion, GvisorVersion: r.GVisorVersion, RebootRequired: r.RebootRequired}
	for _, u := range r.PendingUpdates {
		if p, ok := u.(interface{ Proto() *agentv1.PendingUpdate }); ok {
			out.PendingUpdates = append(out.PendingUpdates, p.Proto())
		}
	}
	for _, svc := range r.Services {
		out.Services = append(out.Services, &agentv1.Service{Name: svc.Name, State: svc.State})
	}
	return connect.NewResponse(out), nil
}

// Handler gives the HTTP handler of the service.
func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	path, h := agentv1connect.NewAgentServiceHandler(s, connect.WithReadMaxBytes(1<<20))
	mux.Handle(path, h)
	return mux
}

// TLSConfig accepts only the panel certificate with the given fingerprint.
func TLSConfig(cert tls.Certificate, panelFingerprint string) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		// R-12: VerifyConnection runs also for a resumed session. VerifyPeerCertificate does not.
		VerifyConnection: pki.PinnedConnection(panelFingerprint),
		NextProtos:       []string{"h2", "http/1.1"},
	}
}

// Serve listens on addr until ctx ends.
func Serve(ctx context.Context, addr string, h http.Handler, cfg *tls.Config) error {
	srv := &http.Server{Addr: addr, Handler: h, TLSConfig: cfg, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
