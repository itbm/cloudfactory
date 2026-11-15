package actions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

const (
	mailHost  = "mail.example.net"
	mailImage = "ghcr.io/itbm/mail:0.2.0"
	// mailHash is a bcrypt hash in the form that the panel sends.
	mailHash = "$2y$12$" + "abcdefghijklmnopqrstuuJ0b1c2d3e4f5g6h7i8j9k0l1m2n3o4p"
)

// mailDeps gives a worker and a root helper with the settings of a mail server.
func mailDeps(t *testing.T) *Deps {
	t.Helper()
	d, f, _ := deps(t)
	f.MakeDirs = true
	dir := t.TempDir()
	both(d, func(d *Deps) {
		d.MailImage = mailImage
		d.MailHome = filepath.Join(dir, "home")
		d.MailConfigDir = filepath.Join(dir, "etc")
	})
	return d
}

func TestTheRootHelperPreparesTheMailServer(t *testing.T) {
	d := mailDeps(t)
	ctx := context.Background()
	if info, err := d.mailInfo(ctx); err != nil || info.Hostname != "" {
		t.Fatalf("a server with no mail server: %+v, %v", info, err)
	}
	if _, err := d.mailClient(ctx); err != errNoMail {
		t.Fatalf("mailClient: %v", err)
	}
	prepared, err := d.prepareMail(ctx, mailHost)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Changed || !prepared.Setup {
		t.Fatalf("the first time: %+v", prepared)
	}
	root := rootOf(d)
	env, _ := os.ReadFile(root.mailEnvFile())
	if !strings.Contains(string(env), "STALWART_RECOVERY_MODE=1") {
		t.Errorf("the first start is not in the recovery mode: %s", env)
	}
	if fi, err := os.Stat(root.mailEnvFile()); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("the environment file: %v, %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(root.mailCertDir(), "tls.key")); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("the key: %v, %v", fi, err)
	}
	dir, _ := root.Host.UnitDir(host.MailUser)
	unit, _ := os.ReadFile(filepath.Join(dir, mailUnit))
	for _, want := range []string{"Image=" + mailImage, "PublishPort=2025:2025", "PublishPort=" + quadlet.ServiceAddress + ":19025:8080", "ReadOnly=true", "DropCapability=all", "EnvironmentFile=" + root.mailEnvFile()} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("the unit does not have %q:\n%s", want, unit)
		}
	}
	info, err := d.mailInfo(ctx)
	if err != nil || info.Hostname != mailHost || len(info.Password) != 48 {
		t.Fatalf("mailInfo: %+v, %v", info, err)
	}
	// The end of the setup keeps the password and turns the recovery mode off.
	if changed, err := d.finishMailSetup(ctx); err != nil || !changed {
		t.Fatalf("finishMailSetup: %v, %v", changed, err)
	}
	env, _ = os.ReadFile(root.mailEnvFile())
	if strings.Contains(string(env), "RECOVERY_MODE") || !strings.Contains(string(env), info.Password) {
		t.Errorf("the environment file after the setup: %s", env)
	}
	again, err := d.prepareMail(ctx, mailHost)
	if err != nil || again.Changed || again.Setup {
		t.Fatalf("the second time: %+v, %v", again, err)
	}
	if after, _ := d.mailInfo(ctx); after.Password != info.Password {
		t.Error("the password changed")
	}
}

func TestTheRootHelperRefusesAMailHostNameThatIsNotAName(t *testing.T) {
	d := mailDeps(t)
	for _, name := range []string{"", "mail", "../etc", "mail.example.net\nSTALWART_RECOVERY_MODE=1", "mail.example.net/x", "-mail.example.net"} {
		if _, err := d.prepareMail(context.Background(), name); err == nil {
			t.Errorf("the root helper accepted the host name %q", name)
		}
	}
}

// edgeCertificate puts a certificate for name in the storage of the edge proxy.
func edgeCertificate(t *testing.T, d *Deps, issuer, name string, certificate, key []byte) string {
	t.Helper()
	dir := filepath.Join(rootOf(d).DataDir, "caddy", "caddy", "certificates", issuer, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, data := range map[string][]byte{name + ".crt": certificate, name + ".key": key} {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestTheMailServerGetsTheCertificateOfTheEdgeProxy(t *testing.T) {
	d := mailDeps(t)
	ctx := context.Background()
	if _, err := d.prepareMail(ctx, mailHost); err != nil {
		t.Fatal(err)
	}
	root := rootOf(d)
	if out, err := d.copyMailCertificate(ctx); err != nil || out.Found {
		t.Fatalf("with no certificate: %+v, %v", out, err)
	}
	before, _ := os.ReadFile(filepath.Join(root.mailCertDir(), "tls.crt"))

	// A certificate for a different name, and a certificate with the key of a different
	// certificate: the root helper uses none of them.
	otherCertificate, otherKey, _ := selfSigned("other.example.net")
	certificate, key, _ := selfSigned(mailHost)
	edgeCertificate(t, d, "wrong-name", mailHost, otherCertificate, otherKey)
	edgeCertificate(t, d, "wrong-key", mailHost, certificate, otherKey)
	// A symbolic link to a file of root in the place of the key.
	link := edgeCertificate(t, d, "link", mailHost, certificate, nil)
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, key, 0o600)
	os.Remove(filepath.Join(link, mailHost+".key"))
	if err := os.Symlink(secret, filepath.Join(link, mailHost+".key")); err != nil {
		t.Fatal(err)
	}
	if out, err := d.copyMailCertificate(ctx); err != nil || out.Found {
		t.Fatalf("with certificates that are not correct: %+v, %v", out, err)
	}
	if now, _ := os.ReadFile(filepath.Join(root.mailCertDir(), "tls.crt")); string(now) != string(before) {
		t.Fatal("the certificate of the mail server changed")
	}

	edgeCertificate(t, d, "acme-v02.api.letsencrypt.org-directory", mailHost, certificate, key)
	out, err := d.copyMailCertificate(ctx)
	if err != nil || !out.Found || !out.Changed {
		t.Fatalf("with a certificate: %+v, %v", out, err)
	}
	if now, _ := os.ReadFile(filepath.Join(root.mailCertDir(), "tls.key")); string(now) != string(key) {
		t.Error("the mail server does not have the key")
	}
	if out, err := d.copyMailCertificate(ctx); err != nil || !out.Found || out.Changed {
		t.Fatalf("the second time: %+v, %v", out, err)
	}
}

// The firewall rules of the mail server must load with the firewall file of the installer.
func TestTheMailFirewallRulesAreValid(t *testing.T) {
	rules := MailFirewall(985, nil)
	for _, want := range []string{"tcp dport 25 redirect to :2025", "tcp dport 993 redirect to :2993", "meta skuid 985 jump mail", "tcp dport 25 accept"} {
		if !strings.Contains(rules, want) {
			t.Errorf("the rules do not have %q", want)
		}
	}
	// The mail server must not connect to a service of this server or to a private network.
	if strings.Index(rules, "169.254.0.0/16") > strings.Index(rules, "    tcp dport 25 accept") {
		t.Error("the rule for port 25 is before the rule for the private networks")
	}
	checkFirewall(t, rules)
}

// checkFirewall loads firewall rules of the agent with the chains of the installer, on a
// computer that has nft.
func checkFirewall(t *testing.T, rules string) {
	t.Helper()
	nft, err := exec.LookPath("nft")
	if err != nil || os.Geteuid() != 0 {
		t.Skip("no nft, or not root")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mail.nft"), []byte(rules), 0o644)
	main := `table inet cloudfactory {
  chain input { type filter hook input priority filter; policy drop; }
  chain output { type filter hook output priority filter; policy accept; }
  chain refuse { reject; }
  chain prerouting { type nat hook prerouting priority dstnat; }
  chain output_nat { type nat hook output priority dstnat; }
  chain postrouting_nat { type nat hook postrouting priority srcnat; }
}
include "` + dir + `/*.nft"
`
	file := filepath.Join(dir, "main")
	os.WriteFile(file, []byte(main), 0o644)
	if out, err := exec.Command(nft, "-c", "-f", file).CombinedOutput(); err != nil {
		if strings.Contains(string(out), "Operation not permitted") || strings.Contains(string(out), "not supported") {
			t.Skip(string(out))
		}
		t.Fatalf("nft: %v\n%s", err, out)
	}
}

func TestTheMailJobsCheckTheirValues(t *testing.T) {
	d := mailDeps(t)
	if _, err := d.SetMailServer(&agentv1.SetMailServerRequest{JobId: jobID, Hostname: "mail"}); err == nil {
		t.Error("a host name with one label")
	}
	if _, err := d.SetMailDomain(&agentv1.MailDomainRequest{JobId: jobID, Domain: "a b.example"}); err == nil {
		t.Error("a domain with a space")
	}
	for name, req := range map[string]*agentv1.SetMailboxRequest{
		"no size":          {Domain: "acme.example.com", LocalPart: "info", PasswordHash: mailHash},
		"a plain password": {Domain: "acme.example.com", LocalPart: "info", QuotaBytes: 1 << 30, PasswordHash: "secret"},
		"an address":       {Domain: "acme.example.com", LocalPart: "info@other.example", QuotaBytes: 1 << 30, PasswordHash: mailHash},
		"two dots":         {Domain: "acme.example.com", LocalPart: "a..b", QuotaBytes: 1 << 30, PasswordHash: mailHash},
	} {
		if _, err := d.SetMailbox(req); err == nil {
			t.Errorf("SetMailbox accepted %s", name)
		}
	}
	if _, err := d.SetMailbox(&agentv1.SetMailboxRequest{Domain: "acme.example.com", LocalPart: "info", QuotaBytes: 1 << 30, PasswordHash: mailHash}); err != nil {
		t.Errorf("a correct mailbox: %v", err)
	}
	const block = "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nmDMEavj2\n=x1Qq\n-----END PGP PUBLIC KEY BLOCK-----\n"
	for name, req := range map[string]*agentv1.SetMailboxRequest{
		"a private key":            {SetEncryption: true, PublicKey: strings.ReplaceAll(block, "PUBLIC", "PRIVATE")},
		"two blocks":               {SetEncryption: true, PublicKey: block + block},
		"text that is not a key":   {SetEncryption: true, PublicKey: "my key"},
		"a key with a NUL":         {SetEncryption: true, PublicKey: strings.Replace(block, "mDME", "mD\x00E", 1)},
		"a very large key":         {SetEncryption: true, PublicKey: strings.Replace(block, "mDME", strings.Repeat("A", mailMaxPublicKey), 1)},
		"a key that the job drops": {PublicKey: block},
	} {
		req.Domain, req.LocalPart, req.QuotaBytes = "acme.example.com", "info", 1<<30
		if _, err := d.SetMailbox(req); err == nil {
			t.Errorf("SetMailbox accepted %s", name)
		}
	}
	for name, key := range map[string]string{"a key": block, "a key with CRLF": strings.ReplaceAll(block, "\n", "\r\n"), "no key": ""} {
		job, err := d.SetMailbox(&agentv1.SetMailboxRequest{Domain: "acme.example.com", LocalPart: "info", QuotaBytes: 1 << 30, SetEncryption: true, PublicKey: key})
		if err != nil || len(job.Steps) != 2 {
			t.Errorf("%s: %d steps, err %v", name, len(job.Steps), err)
		}
	}
	many := make([]string, mailMaxDestinations+1)
	for i := range many {
		many[i] = "a@example.org"
	}
	for name, destinations := range map[string][]string{"no destination": nil, "too many": many, "not an address": {"example.org"}, "two lines": {"a@example.org\nb@example.org"}} {
		if _, err := d.SetMailAlias(&agentv1.SetMailAliasRequest{Domain: "acme.example.com", LocalPart: "sales", Destinations: destinations}); err == nil {
			t.Errorf("SetMailAlias accepted %s", name)
		}
	}
	// A job on a server with no mail server fails with a clear message.
	job, err := d.DeleteMailbox(&agentv1.MailAddressRequest{JobId: jobID, Domain: "acme.example.com", LocalPart: "info"})
	if err != nil || job.Key != mailKey {
		t.Fatalf("DeleteMailbox: %v", err)
	}
	if err := job.Steps[0].Do(context.Background()); err != errNoMail {
		t.Errorf("the step: %v", err)
	}
	if r := d.mailReport(context.Background()); r != nil {
		t.Errorf("the report of a server with no mail server: %+v", r)
	}
}
