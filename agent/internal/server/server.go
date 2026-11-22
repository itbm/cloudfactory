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
	"github.com/itbm/cloudfactory/agent/internal/sitefiles"
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

func (s *Service) PruneBackups(_ context.Context, r *connect.Request[agentv1.PruneBackupsRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.PruneBackups(r.Msg))
}

// The mail server of this server, and the domains, the mailboxes and the addresses on it.
func (s *Service) SetMailServer(_ context.Context, r *connect.Request[agentv1.SetMailServerRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetMailServer(r.Msg))
}

func (s *Service) PrepareMailNode(_ context.Context, r *connect.Request[agentv1.PrepareMailNodeRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.PrepareMailNode(r.Msg))
}

func (s *Service) SetMailNode(_ context.Context, r *connect.Request[agentv1.SetMailNodeRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetMailNode(r.Msg))
}

func (s *Service) SetMailDomain(_ context.Context, r *connect.Request[agentv1.MailDomainRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetMailDomain(r.Msg))
}

func (s *Service) DeleteMailDomain(_ context.Context, r *connect.Request[agentv1.MailDomainRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.DeleteMailDomain(r.Msg))
}

func (s *Service) SetMailbox(_ context.Context, r *connect.Request[agentv1.SetMailboxRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetMailbox(r.Msg))
}

func (s *Service) DeleteMailbox(_ context.Context, r *connect.Request[agentv1.MailAddressRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.DeleteMailbox(r.Msg))
}

func (s *Service) SetMailAlias(_ context.Context, r *connect.Request[agentv1.SetMailAliasRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetMailAlias(r.Msg))
}

func (s *Service) DeleteMailAlias(_ context.Context, r *connect.Request[agentv1.MailAddressRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.DeleteMailAlias(r.Msg))
}

func (s *Service) SetSftp(_ context.Context, r *connect.Request[agentv1.SetSftpRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetSftp(r.Msg))
}

func (s *Service) SetDatabaseServer(_ context.Context, r *connect.Request[agentv1.DatabaseServerRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetDatabaseServer(r.Msg))
}

func (s *Service) SetMesh(_ context.Context, r *connect.Request[agentv1.SetMeshRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetMesh(r.Msg))
}

func (s *Service) RotateMeshKey(_ context.Context, r *connect.Request[agentv1.RotateMeshKeyRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.RotateMeshKey(r.Msg))
}

func (s *Service) DeleteDatabaseServer(_ context.Context, r *connect.Request[agentv1.DeleteDatabaseServerRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.DeleteDatabaseServer(r.Msg))
}

func (s *Service) SendDatabaseServer(_ context.Context, r *connect.Request[agentv1.SendDatabaseServerRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SendDatabaseServer(r.Msg))
}

func (s *Service) ReceiveDatabaseServer(_ context.Context, r *connect.Request[agentv1.ReceiveDatabaseServerRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.ReceiveDatabaseServer(r.Msg))
}

func (s *Service) SetDnsZones(_ context.Context, r *connect.Request[agentv1.SetDnsZonesRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetDnsZones(r.Msg))
}

func (s *Service) DeleteDnsZones(_ context.Context, r *connect.Request[agentv1.DeleteDnsZonesRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.DeleteDnsZones(r.Msg))
}

func (s *Service) ConfigureHost(_ context.Context, r *connect.Request[agentv1.ConfigureHostRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.ConfigureHost(r.Msg))
}

func (s *Service) SetPanelCertificate(_ context.Context, r *connect.Request[agentv1.SetPanelCertificateRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetPanelCertificate(r.Msg))
}

func (s *Service) SetPanelBrands(_ context.Context, r *connect.Request[agentv1.SetPanelBrandsRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetPanelBrands(r.Msg))
}

func (s *Service) SetPacks(_ context.Context, r *connect.Request[agentv1.SetPacksRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetPacks(r.Msg))
}

func (s *Service) SetCronJobs(_ context.Context, r *connect.Request[agentv1.SetCronJobsRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.SetCronJobs(r.Msg))
}

func (s *Service) RunCronJob(_ context.Context, r *connect.Request[agentv1.RunCronJobRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.RunCronJob(r.Msg))
}

func (s *Service) Deploy(_ context.Context, r *connect.Request[agentv1.DeployRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.Deploy(r.Msg))
}

func (s *Service) ActivateRelease(_ context.Context, r *connect.Request[agentv1.ActivateReleaseRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.ActivateRelease(r.Msg))
}

func (s *Service) InstallDependencies(_ context.Context, r *connect.Request[agentv1.InstallDependenciesRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.InstallDependencies(r.Msg))
}

// fileError gives the error of a file operation or of the logs its code for the panel.
func fileError(err error) error {
	var fe *actions.FileError
	switch {
	case errors.Is(err, actions.ErrUnknownWebsite):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.As(err, &fe):
		code := connect.CodeUnknown
		switch fe.Code {
		case sitefiles.CodeNotFound:
			code = connect.CodeNotFound
		case sitefiles.CodeExists:
			code = connect.CodeAlreadyExists
		case sitefiles.CodeInvalid:
			code = connect.CodeInvalidArgument
		case sitefiles.CodeTooLarge:
			code = connect.CodeOutOfRange
		case sitefiles.CodeDenied:
			code = connect.CodePermissionDenied
		case sitefiles.CodeFull:
			code = connect.CodeResourceExhausted
		}
		return connect.NewError(code, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// files does one file operation and gives the answer of the file manager.
func (s *Service) files(ctx context.Context, websiteID string, req sitefiles.Request) (sitefiles.Response, error) {
	resp, err := s.Deps.Files(ctx, websiteID, req)
	if err != nil {
		return resp, fileError(err)
	}
	return resp, nil
}

// entry does a file operation that gives one entry.
func (s *Service) entry(ctx context.Context, websiteID string, req sitefiles.Request) (*connect.Response[agentv1.FileEntry], error) {
	resp, err := s.files(ctx, websiteID, req)
	if err != nil {
		return nil, err
	}
	if resp.Entry == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the file manager gave no entry"))
	}
	return connect.NewResponse(actions.FileEntry(*resp.Entry)), nil
}

func (s *Service) ListFiles(ctx context.Context, r *connect.Request[agentv1.FileRequest]) (*connect.Response[agentv1.FileList], error) {
	resp, err := s.files(ctx, r.Msg.GetWebsiteId(), sitefiles.Request{Op: sitefiles.OpList, Path: r.Msg.GetPath()})
	if err != nil {
		return nil, err
	}
	out := &agentv1.FileList{Truncated: resp.Truncated}
	for _, e := range resp.Entries {
		out.Entries = append(out.Entries, actions.FileEntry(e))
	}
	return connect.NewResponse(out), nil
}

func (s *Service) ReadFile(ctx context.Context, r *connect.Request[agentv1.FileRequest]) (*connect.Response[agentv1.FileContent], error) {
	resp, err := s.files(ctx, r.Msg.GetWebsiteId(), sitefiles.Request{Op: sitefiles.OpRead, Path: r.Msg.GetPath()})
	if err != nil {
		return nil, err
	}
	out := &agentv1.FileContent{Content: resp.Content}
	if resp.Entry != nil {
		out.File = actions.FileEntry(*resp.Entry)
	}
	return connect.NewResponse(out), nil
}

func (s *Service) WriteFile(ctx context.Context, r *connect.Request[agentv1.WriteFileRequest]) (*connect.Response[agentv1.FileEntry], error) {
	return s.entry(ctx, r.Msg.GetWebsiteId(), sitefiles.Request{Op: sitefiles.OpWrite, Path: r.Msg.GetPath(), Content: r.Msg.GetContent(), CreateOnly: r.Msg.GetCreateOnly()})
}

func (s *Service) MakeDirectory(ctx context.Context, r *connect.Request[agentv1.FileRequest]) (*connect.Response[agentv1.FileEntry], error) {
	return s.entry(ctx, r.Msg.GetWebsiteId(), sitefiles.Request{Op: sitefiles.OpMkdir, Path: r.Msg.GetPath()})
}

func (s *Service) MoveFile(ctx context.Context, r *connect.Request[agentv1.MoveFileRequest]) (*connect.Response[agentv1.FileEntry], error) {
	return s.entry(ctx, r.Msg.GetWebsiteId(), sitefiles.Request{Op: sitefiles.OpMove, Path: r.Msg.GetFrom(), To: r.Msg.GetTo()})
}

func (s *Service) DeleteFile(ctx context.Context, r *connect.Request[agentv1.FileRequest]) (*connect.Response[agentv1.FileDeleted], error) {
	if _, err := s.files(ctx, r.Msg.GetWebsiteId(), sitefiles.Request{Op: sitefiles.OpDelete, Path: r.Msg.GetPath()}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.FileDeleted{}), nil
}

func (s *Service) SetFileMode(ctx context.Context, r *connect.Request[agentv1.SetFileModeRequest]) (*connect.Response[agentv1.FileModeSet], error) {
	m := r.Msg
	resp, err := s.files(ctx, m.GetWebsiteId(), sitefiles.Request{Op: sitefiles.OpChmod, Path: m.GetPath(), Mode: m.GetMode(), Recursive: m.GetRecursive(), FileMode: m.GetFileMode()})
	if err != nil {
		return nil, err
	}
	if resp.Entry == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the file manager gave no entry"))
	}
	return connect.NewResponse(&agentv1.FileModeSet{File: actions.FileEntry(*resp.Entry), Changed: int64(resp.Changed)}), nil
}

func (s *Service) MakeArchive(_ context.Context, r *connect.Request[agentv1.MakeArchiveRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.MakeArchive(r.Msg))
}

func (s *Service) ExtractArchive(_ context.Context, r *connect.Request[agentv1.ExtractArchiveRequest]) (*connect.Response[agentv1.JobAccepted], error) {
	return s.submit(s.Deps.ExtractArchive(r.Msg))
}

func (s *Service) GetLogs(ctx context.Context, r *connect.Request[agentv1.GetLogsRequest]) (*connect.Response[agentv1.Logs], error) {
	lines, err := s.Deps.Logs(ctx, r.Msg.GetWebsiteId(), int(r.Msg.GetLines()))
	if err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&agentv1.Logs{Lines: lines}), nil
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

// maxRequest is the largest request. A file of the file manager is in a request as base64
// text, which is larger than the file (sitefiles.MaxWrite).
const maxRequest = 8 << 20

// Handler gives the HTTP handler of the service.
func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	path, h := agentv1connect.NewAgentServiceHandler(s, connect.WithReadMaxBytes(maxRequest))
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
