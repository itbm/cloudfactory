package actions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/safefs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
	"google.golang.org/protobuf/proto"
)

// The protection of a website in the edge proxy: the firewall, the IP address rules, the
// password-protected paths and the sign-in limit (caddy/security.go). The panel adds the
// current value to each job with the website. SetWebsiteSecurity changes only the edge route.

// opEdgeFirewallEvents reads the firewall log of the edge proxy.
const opEdgeFirewallEvents = "edge.firewallEvents"

// The limits of GetFirewallEvents.
const (
	defaultFirewallEvents = 50
	maxFirewallEvents     = 200
	// firewallLogTail is the part of the end of the firewall log that the agent reads.
	firewallLogTail = 8 << 20
)

// checkSecurity checks the protection of a website. No value is no protection.
func checkSecurity(s *agentv1.WebsiteSecurity) error {
	if s == nil {
		return nil
	}
	var errs []error
	switch s.GetFirewall() {
	case "", caddy.FirewallOff, caddy.FirewallDetect, caddy.FirewallBlock:
	default:
		errs = append(errs, fmt.Errorf("the firewall mode %q is not valid", s.GetFirewall()))
	}
	if len(s.GetFirewallExclusions()) > validate.MaxFirewallExclusions {
		errs = append(errs, fmt.Errorf("a website can turn off %d firewall rules", validate.MaxFirewallExclusions))
	}
	for _, id := range s.GetFirewallExclusions() {
		if id < 1 || id > validate.MaxFirewallRuleId {
			errs = append(errs, fmt.Errorf("the firewall rule %d is not valid", id))
		}
	}
	paths := func(list []string, max int, what string) {
		if len(list) > max {
			errs = append(errs, fmt.Errorf("a website can have %d %s", max, what))
		}
		for _, p := range list {
			errs = append(errs, validate.SecurityPath(p))
		}
	}
	paths(s.GetFirewallSkipPaths(), validate.MaxFirewallSkipPaths, "paths that the firewall does not examine")
	paths(s.GetSignInPaths(), validate.MaxSignInPaths, "sign-in paths")
	for _, list := range [][]string{s.GetAllow(), s.GetBlock()} {
		if len(list) > validate.MaxIpRules {
			errs = append(errs, fmt.Errorf("a list of IP addresses can have %d values", validate.MaxIpRules))
		}
		for _, v := range list {
			if _, err := ipRange(v); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(s.GetProtectedPaths()) > validate.MaxProtectedPaths {
		errs = append(errs, fmt.Errorf("a website can have %d password-protected paths", validate.MaxProtectedPaths))
	}
	seen := map[string]bool{}
	for _, p := range s.GetProtectedPaths() {
		errs = append(errs, validate.SecurityPath(p.GetPath()))
		if seen[p.GetPath()] {
			errs = append(errs, fmt.Errorf("the path %q is in the list two times", p.GetPath()))
		}
		seen[p.GetPath()] = true
		if len(p.GetUsers()) == 0 || len(p.GetUsers()) > validate.MaxPathUsers {
			errs = append(errs, fmt.Errorf("a password-protected path must have 1 to %d users", validate.MaxPathUsers))
		}
		for _, u := range p.GetUsers() {
			errs = append(errs, validate.PathUser(u.GetName()), validate.PathPasswordHash(u.GetPasswordHash()))
		}
	}
	if n := s.GetSignInLimit(); n != 0 && (n < validate.MinLowSignInLimit || n > validate.MaxSignInLimit) {
		errs = append(errs, fmt.Errorf("the sign-in limit must be 0 or from %d to %d", validate.MinLowSignInLimit, validate.MaxSignInLimit))
	}
	return errors.Join(errs...)
}

// ipRange reads an IP address or a range in CIDR form, and gives the range. The edge proxy
// gets the masked form, for example "203.0.113.0/24" for "203.0.113.7/24".
func ipRange(v string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(v); err == nil && p.Addr().Zone() == "" {
		return p.Masked(), nil
	}
	if a, err := netip.ParseAddr(v); err == nil && a.Zone() == "" {
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	return netip.Prefix{}, fmt.Errorf("the IP address or range %q is not valid", v)
}

// edgeSecurity gives the protection of a website for the edge proxy. The values are checked.
func edgeSecurity(s *agentv1.WebsiteSecurity) *caddy.Security {
	if s == nil {
		return nil
	}
	ranges := func(list []string) []string {
		var out []string
		for _, v := range list {
			if p, err := ipRange(v); err == nil {
				out = append(out, p.String())
			}
		}
		return out
	}
	out := &caddy.Security{
		Firewall:          s.GetFirewall(),
		FirewallSkipPaths: s.GetFirewallSkipPaths(),
		Allow:             ranges(s.GetAllow()),
		Block:             ranges(s.GetBlock()),
		SignInLimit:       int(s.GetSignInLimit()),
		SignInPaths:       s.GetSignInPaths(),
	}
	for _, id := range s.GetFirewallExclusions() {
		out.FirewallExclusions = append(out.FirewallExclusions, int(id))
	}
	for _, p := range s.GetProtectedPaths() {
		pp := caddy.ProtectedPath{Path: p.GetPath()}
		for _, u := range p.GetUsers() {
			pp.Users = append(pp.Users, caddy.PathUser{Name: u.GetName(), PasswordHash: u.GetPasswordHash()})
		}
		out.ProtectedPaths = append(out.ProtectedPaths, pp)
	}
	return out
}

// SetWebsiteSecurity changes the protection of a website in the edge proxy. The container of
// the website does not restart.
func (d *Deps) SetWebsiteSecurity(req *agentv1.SetWebsiteSecurityRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetWebsiteId()), validate.UnixUser(req.GetUnixUser()), checkSecurity(req.GetSecurity())); err != nil {
		return jobs.Job{}, err
	}
	if err := d.checkOwner(req.GetWebsiteId(), req.GetUnixUser()); err != nil {
		return jobs.Job{}, err
	}
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Set edge proxy rules", Do: func(ctx context.Context) error {
			site, err := d.State.Get(req.GetWebsiteId())
			if err != nil {
				return err
			}
			if site == nil {
				return errors.New("the agent does not know this website")
			}
			w := proto.Clone(site.Website).(*agentv1.Website)
			w.Security = req.GetSecurity()
			// A worker and a private website have no route in the edge proxy. The agent keeps
			// the value for a later change of the kind.
			if !w.GetWorker() && site.Port != 0 {
				if err := d.Edge.Apply(ctx, d.edgeSite(site, w)); err != nil {
					return err
				}
			}
			site.Website = w
			return d.State.Put(site)
		}},
	}}, nil
}

// FirewallEvents gives the last requests of a website that the firewall blocked or detected,
// the newest first.
func (d *Deps) FirewallEvents(ctx context.Context, websiteID string, limit int) ([]*agentv1.FirewallEvent, error) {
	if _, err := d.knownSite(websiteID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultFirewallEvents
	}
	limit = min(limit, maxFirewallEvents)
	events, err := d.firewallEvents(ctx, websiteID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*agentv1.FirewallEvent, 0, len(events))
	for _, e := range events {
		pe := &agentv1.FirewallEvent{At: e.At.Format(time.RFC3339), ClientIp: e.ClientIP, Host: e.Host, Uri: e.URI, Action: e.Action, Score: int32(e.Score)}
		for _, r := range e.Rules {
			pe.Rules = append(pe.Rules, &agentv1.FirewallRule{Id: int32(r.ID), Message: r.Message, Data: r.Data})
		}
		out = append(out, pe)
	}
	return out, nil
}

type firewallEventsIn struct {
	Website string `json:"website"`
	Limit   int    `json:"limit"`
}

// firewallEvents reads the firewall log of the edge proxy as root. The edge user can change the
// data directory of the edge proxy, so the helper opens the log with no symbolic links, and
// reads it only when it is a regular file of the edge user.
func (d *Deps) firewallEvents(ctx context.Context, websiteID string, limit int) ([]caddy.FirewallEvent, error) {
	if d.Gate != nil {
		var out []caddy.FirewallEvent
		err := d.Gate.Call(ctx, opEdgeFirewallEvents, firewallEventsIn{Website: websiteID, Limit: limit}, &out)
		return out, err
	}
	if err := validate.ID(websiteID); err != nil {
		return nil, err
	}
	dir, err := safefs.Open(filepath.Join(d.DataDir, "caddy"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := dir.OpenFile(strings.TrimPrefix(caddy.FirewallLog, "/data/"), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		// No website had the firewall on yet.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	uid, err := d.Host.LookupUID(host.EdgeUser)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || int(st.Uid) != uid {
		return nil, errors.New("the firewall log is not a file of the edge proxy")
	}
	var r io.Reader = f
	if size := info.Size(); size > firewallLogTail {
		r = io.NewSectionReader(f, size-firewallLogTail, firewallLogTail)
	}
	return caddy.FirewallEvents(r, websiteID, limit), nil
}
