package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/stalwart"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The mail server of a server. One container (Stalwart) has the mail of all customers of the
// server, because a server has one port 25. The panel keeps the customers apart: it gives
// each mail domain to one customer, and sends a job only for a domain of that customer.
//
// The worker of the agent changes the domains, the mailboxes and the addresses through the
// management API of the mail server. The API is on the private service address, which the
// firewall keeps for root: the root helper opens the connection (DialLocal) and gives the
// password of the administrator (mail.info).

const (
	// mailKey is the key of all mail jobs: they run one at a time.
	mailKey = "mail"
	// mailRoute is the ID of the route of the mail host name in the edge proxy.
	mailRoute = "mail"
	// mailPage is the answer of the edge proxy on the mail host name.
	mailPage = "This is a mail server. Use a mail program to read your email.\n"
	// mailMaxDestinations is the maximum number of destinations of one address. The panel
	// has the same limit.
	mailMaxDestinations = 50
	// mailCertificateWait is the maximum time that the job waits for the edge proxy to get
	// the certificate of the mail host name.
	mailCertificateWait = 3 * time.Minute
)

// errNoMail is the error of a mail job on a server that has no mail server.
var errNoMail = errors.New("this server has no mail server. Turn on email for the server first")

// mailURL gives the address of the management API of the mail server.
func (d *Deps) mailURL() string {
	if d.MailURL != "" {
		return d.MailURL
	}
	return "http://" + net.JoinHostPort(quadlet.ServiceAddress, strconv.Itoa(quadlet.MailAdminPort))
}

// mailClientFor gives the client of the management API for a password.
func (d *Deps) mailClientFor(password string) *stalwart.Client {
	c := &stalwart.Client{URL: d.mailURL(), Password: password, HTTP: &http.Client{Timeout: time.Minute}}
	if d.MailURL == "" {
		c.HTTP.Transport = &http.Transport{DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			return d.DialLocal(ctx, address)
		}}
	}
	return c
}

// mailClient gives the client of the management API. It fails on a server with no mail server.
func (d *Deps) mailClient(ctx context.Context) (*stalwart.Client, error) {
	info, err := d.mailInfo(ctx)
	if err != nil {
		return nil, err
	}
	if info.Hostname == "" {
		return nil, errNoMail
	}
	return d.mailClientFor(info.Password), nil
}

func mailSettings(hostname string) stalwart.Settings {
	return stalwart.Settings{
		Hostname: hostname,
		Ports: stalwart.Ports{SMTP: quadlet.MailSMTPPort, Submissions: quadlet.MailSubmissionsPort,
			Submission: quadlet.MailSubmissionPort, IMAPS: quadlet.MailIMAPSPort, HTTP: quadlet.MailHTTPPort},
		CertFile: quadlet.MailCertFile, KeyFile: quadlet.MailKeyFile,
	}
}

// mailReady waits until the management API of the mail server answers.
func (d *Deps) mailReady(ctx context.Context, c *stalwart.Client) error {
	deadline := time.Now().Add(orDefault(d.ReadyTimeout, time.Minute))
	for {
		err := c.Ready(ctx)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the mail server does not answer: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// EnsureMail makes the mail server with a host name and starts it, or gives a mail server
// that exists a new host name or the image of this agent version.
//
// The first start is in the recovery mode of Stalwart: the server then has only its
// management API. The agent applies the settings, ends the setup, and starts the server
// again with its mail listeners. Thus the server never listens with the default settings of
// its image.
func (d *Deps) EnsureMail(ctx context.Context, hostname string) error {
	if err := validate.Domain(hostname); err != nil {
		return err
	}
	if d.MailImage == "" {
		return errors.New("the agent has no image of the mail server")
	}
	// The root helper makes the Unix user before the first Podman command of that user.
	prepared, err := d.prepareMail(ctx, hostname)
	if err != nil {
		return err
	}
	if _, err := d.Host.Podman(ctx, host.MailUser, "image", "exists", d.MailImage); err != nil {
		if _, err := d.Host.Podman(ctx, host.MailUser, "pull", "--quiet", d.MailImage); err != nil {
			return err
		}
	}
	info, err := d.mailInfo(ctx)
	if err != nil {
		return err
	}
	client := d.mailClientFor(info.Password)
	start := func(verb string) error {
		if _, err := d.Host.Systemctl(ctx, host.MailUser, verb, mailService); err != nil {
			return err
		}
		return d.mailReady(ctx, client)
	}
	verb := "start"
	if prepared.Changed {
		if _, err := d.Host.Systemctl(ctx, host.MailUser, "daemon-reload"); err != nil {
			return err
		}
		verb = "restart"
	}
	if err := start(verb); err != nil {
		return err
	}
	restart, err := client.Configure(ctx, mailSettings(hostname))
	if err != nil {
		return err
	}
	if prepared.Setup {
		// The environment file changes: the next start is not in the recovery mode.
		if _, err := d.finishMailSetup(ctx); err != nil {
			return err
		}
		restart = true
	}
	if restart {
		return start("restart")
	}
	return nil
}

// mailCertificateNow gives the mail server the certificate of the edge proxy, when the edge
// proxy has one. It reports true when the mail server has a certificate of the edge proxy.
func (d *Deps) mailCertificateNow(ctx context.Context) (bool, error) {
	out, err := d.copyMailCertificate(ctx)
	if err != nil || !out.Found {
		return false, err
	}
	if out.Changed {
		client, err := d.mailClient(ctx)
		if err != nil {
			return true, err
		}
		if err := client.ReloadCertificate(ctx); err != nil {
			return true, err
		}
	}
	return true, nil
}

// mailRouteFor tells the edge proxy to get a certificate for the mail host name.
func (d *Deps) mailRouteFor(ctx context.Context, hostname string) error {
	return d.Edge.Apply(ctx, caddy.Site{WebsiteID: mailRoute, Hosts: []string{hostname}, Primary: hostname, Page: mailPage})
}

// SetMailServer turns on email for this server, or changes the host name of the mail server.
func (d *Deps) SetMailServer(req *agentv1.SetMailServerRequest) (jobs.Job, error) {
	hostname := req.GetHostname()
	if err := validate.Domain(hostname); err != nil {
		return jobs.Job{}, fmt.Errorf("host name: %w", err)
	}
	return jobs.Job{ID: req.GetJobId(), Key: mailKey, Steps: []jobs.Step{
		{Name: "Start mail server", Do: func(ctx context.Context) error { return d.EnsureMail(ctx, hostname) }},
		{Name: "Open mail ports", Do: d.openMailPorts},
		{Name: "Get TLS certificate", Do: func(ctx context.Context) error {
			if err := d.mailRouteFor(ctx, hostname); err != nil {
				return err
			}
			deadline := time.Now().Add(orDefault(d.MailCertificateWait, mailCertificateWait))
			for {
				found, err := d.mailCertificateNow(ctx)
				if err != nil {
					return err
				}
				if found {
					return nil
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("the server has no TLS certificate for %s. Make sure that the name has an A or AAAA record with the address of this server, then do the step again", hostname)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Second):
				}
			}
		}},
	}}, nil
}

// RefreshMail starts the mail server of this server with the image and the unit of this
// agent version, and gives it the current certificate. The agent calls it when it starts.
// On a server with no mail server, it does nothing.
func (d *Deps) RefreshMail(ctx context.Context) error {
	info, err := d.mailInfo(ctx)
	if err != nil || info.Hostname == "" {
		return err
	}
	if err := d.EnsureMail(ctx, info.Hostname); err != nil {
		return err
	}
	if err := d.openMailPorts(ctx); err != nil {
		return err
	}
	if err := d.mailRouteFor(ctx, info.Hostname); err != nil {
		return err
	}
	_, err = d.mailCertificateNow(ctx)
	return err
}

// RenewMailCertificate gives the mail server the certificate of the edge proxy after a
// renewal. The agent calls it one time each day.
func (d *Deps) RenewMailCertificate(ctx context.Context) {
	info, err := d.mailInfo(ctx)
	if err != nil || info.Hostname == "" {
		return
	}
	if _, err := d.mailCertificateNow(ctx); err != nil {
		slog.Error("give the mail server its certificate", "err", err)
	}
}

// checkMailAddress checks the two parts of an address on a mail domain.
func checkMailAddress(domain, localPart string) error {
	if err := validate.Domain(domain); err != nil {
		return fmt.Errorf("domain: %w", err)
	}
	if err := validate.MailLocalPart(localPart); err != nil {
		return fmt.Errorf("address: %w", err)
	}
	return nil
}

// mailStep gives a job with one step that uses the management API.
func (d *Deps) mailStep(jobID, name string, do func(ctx context.Context, c *stalwart.Client) error) jobs.Job {
	return jobs.Job{ID: jobID, Key: mailKey, Steps: []jobs.Step{{Name: name, Do: func(ctx context.Context) error {
		c, err := d.mailClient(ctx)
		if err != nil {
			return err
		}
		return do(ctx, c)
	}}}}
}

// SetMailDomain adds a mail domain and its DKIM keys. The result has the DNS records that
// the owner of the domain adds.
func (d *Deps) SetMailDomain(req *agentv1.MailDomainRequest) (jobs.Job, error) {
	domain := req.GetDomain()
	if err := validate.Domain(domain); err != nil {
		return jobs.Job{}, fmt.Errorf("domain: %w", err)
	}
	var records []stalwart.Record
	return jobs.Job{ID: req.GetJobId(), Key: mailKey,
		Steps: []jobs.Step{
			{Name: "Add domain", Do: func(ctx context.Context) error {
				// A check only: the next step adds the domain and waits for its keys.
				_, err := d.mailClient(ctx)
				return err
			}},
			{Name: "Make DKIM keys", Do: func(ctx context.Context) error {
				c, err := d.mailClient(ctx)
				if err != nil {
					return err
				}
				records, err = c.SetDomain(ctx, domain)
				return err
			}},
		},
		Result: func() map[string]any { return map[string]any{"records": records} },
	}, nil
}

// DeleteMailDomain removes a mail domain. The panel sends it only for a domain with no
// mailboxes and no addresses.
func (d *Deps) DeleteMailDomain(req *agentv1.MailDomainRequest) (jobs.Job, error) {
	domain := req.GetDomain()
	if err := validate.Domain(domain); err != nil {
		return jobs.Job{}, fmt.Errorf("domain: %w", err)
	}
	return d.mailStep(req.GetJobId(), "Remove domain", func(ctx context.Context, c *stalwart.Client) error {
		return c.DeleteDomain(ctx, domain)
	}), nil
}

// SetMailbox makes a mailbox, or changes its size or its password. The panel sends the hash
// of the password: the agent never has the password.
func (d *Deps) SetMailbox(req *agentv1.SetMailboxRequest) (jobs.Job, error) {
	domain, localPart, quota, hash := req.GetDomain(), req.GetLocalPart(), req.GetQuotaBytes(), req.GetPasswordHash()
	if err := checkMailAddress(domain, localPart); err != nil {
		return jobs.Job{}, err
	}
	if quota <= 0 {
		return jobs.Job{}, errors.New("the mailbox needs a size")
	}
	if hash != "" {
		if err := validate.MailPasswordHash(hash); err != nil {
			return jobs.Job{}, fmt.Errorf("password hash: %w", err)
		}
	}
	publicKey := req.GetPublicKey()
	if !req.GetSetEncryption() && publicKey != "" {
		return jobs.Job{}, errors.New("the job has a public key, but it does not set the encryption")
	}
	if err := checkPublicKey(publicKey); err != nil {
		return jobs.Job{}, err
	}
	job := d.mailStep(req.GetJobId(), "Set mailbox", func(ctx context.Context, c *stalwart.Client) error {
		return c.SetMailbox(ctx, domain, localPart, quota, hash)
	})
	if !req.GetSetEncryption() {
		return job, nil
	}
	var key *stalwart.Key
	name := "Turn on encryption"
	if publicKey == "" {
		name = "Turn off encryption"
	}
	job.Steps = append(job.Steps, jobs.Step{Name: name, Do: func(ctx context.Context) error {
		c, err := d.mailClient(ctx)
		if err != nil {
			return err
		}
		key, err = c.SetEncryption(ctx, domain, localPart, publicKey)
		return err
	}})
	// The panel shows the fingerprint, so that the owner can compare it with the mail program.
	job.Result = func() map[string]any { return map[string]any{"encryption": key} }
	return job, nil
}

// mailMaxPublicKey is the largest public key block. A block can have more than one key, and a
// post-quantum key is some kilobytes.
const mailMaxPublicKey = 64 << 10

const (
	pgpBegin = "-----BEGIN PGP PUBLIC KEY BLOCK-----"
	pgpEnd   = "-----END PGP PUBLIC KEY BLOCK-----"
)

// checkPublicKey accepts one OpenPGP public key block in ASCII armour. The mail server reads
// the key and refuses a key that cannot encrypt: this check only keeps other data out.
func checkPublicKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > mailMaxPublicKey {
		return fmt.Errorf("the public key is larger than %d KB", mailMaxPublicKey>>10)
	}
	body, ok := strings.CutPrefix(strings.TrimRight(key, "\r\n"), pgpBegin)
	if ok {
		body, ok = strings.CutSuffix(body, pgpEnd)
	}
	if !ok || strings.Contains(body, "-----") {
		return errors.New("the public key is not one OpenPGP public key block in ASCII armour")
	}
	for _, r := range body {
		if r != '\n' && r != '\r' && (r < 0x20 || r > 0x7e) {
			return errors.New("the public key has characters that ASCII armour does not have")
		}
	}
	return nil
}

// DeleteMailbox removes a mailbox and its messages.
func (d *Deps) DeleteMailbox(req *agentv1.MailAddressRequest) (jobs.Job, error) {
	domain, localPart := req.GetDomain(), req.GetLocalPart()
	if err := checkMailAddress(domain, localPart); err != nil {
		return jobs.Job{}, err
	}
	return d.mailStep(req.GetJobId(), "Remove mailbox and its messages", func(ctx context.Context, c *stalwart.Client) error {
		return c.DeleteMailbox(ctx, domain, localPart)
	}), nil
}

// SetMailAlias makes an address that sends each message to its destinations: an alias for
// mailboxes of the customer, or a forwarder to other addresses.
func (d *Deps) SetMailAlias(req *agentv1.SetMailAliasRequest) (jobs.Job, error) {
	domain, localPart, destinations := req.GetDomain(), req.GetLocalPart(), req.GetDestinations()
	if err := checkMailAddress(domain, localPart); err != nil {
		return jobs.Job{}, err
	}
	if len(destinations) == 0 || len(destinations) > mailMaxDestinations {
		return jobs.Job{}, fmt.Errorf("an address needs 1 to %d destinations", mailMaxDestinations)
	}
	for _, destination := range destinations {
		if err := validate.MailAddress(destination); err != nil {
			return jobs.Job{}, fmt.Errorf("destination: %w", err)
		}
	}
	return d.mailStep(req.GetJobId(), "Set address", func(ctx context.Context, c *stalwart.Client) error {
		return c.SetAddress(ctx, domain, localPart, destinations)
	}), nil
}

// DeleteMailAlias removes an alias or a forwarder.
func (d *Deps) DeleteMailAlias(req *agentv1.MailAddressRequest) (jobs.Job, error) {
	domain, localPart := req.GetDomain(), req.GetLocalPart()
	if err := checkMailAddress(domain, localPart); err != nil {
		return jobs.Job{}, err
	}
	return d.mailStep(req.GetJobId(), "Remove address", func(ctx context.Context, c *stalwart.Client) error {
		return c.DeleteAddress(ctx, domain, localPart)
	}), nil
}

// mailReport gives the state of the mail server and the size of each mailbox for the status
// report. It gives nil on a server with no mail server.
func (d *Deps) mailReport(ctx context.Context) *panel.MailReport {
	info, err := d.mailInfo(ctx)
	if err != nil || info.Hostname == "" {
		return nil
	}
	report := &panel.MailReport{State: "stopped", Mailboxes: []panel.MailboxUse{}}
	if out, err := d.Host.Systemctl(ctx, host.MailUser, "is-active", mailService); err != nil || strings.TrimSpace(out) != "active" {
		return report
	}
	report.State = "running"
	usage, err := d.mailClientFor(info.Password).Usage(ctx)
	if err != nil {
		slog.Warn("read the size of the mailboxes", "err", err)
		return report
	}
	for address, used := range usage {
		report.Mailboxes = append(report.Mailboxes, panel.MailboxUse{Address: address, UsedBytes: used})
	}
	return report
}
