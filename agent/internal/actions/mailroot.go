package actions

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/mailcluster"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/safefs"
	"github.com/itbm/cloudfactory/agent/internal/stalwart"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The root work for the mail server. The worker of the agent is not root: it asks the root
// helper for each of these operations. The worker sends only the host name of the mail
// server. The helper makes the user, the paths, the unit and the firewall rules from its own
// settings.

const (
	opMailPrepare     = "mail.prepare"
	opMailFirewall    = "mail.firewall"
	opMailReady       = "mail.ready"
	opMailInfo        = "mail.info"
	opMailCertificate = "mail.certificate"

	mailUnit    = "cf-mail.container"
	mailService = "cf-mail.service"
	// mailFirewallFile is the file of the agent with the firewall rules of the mail server,
	// in firewallDir. The firewall file of the installer includes it.
	mailFirewallFile = "mail.nft"
)

type (
	mailIn struct {
		Hostname string
	}
	mailPrepared struct {
		// Changed is true when the unit changed: the container must start again.
		Changed bool
		// Setup is true until the agent has applied the settings of the mail server for the
		// first time. The container then starts in the recovery mode of Stalwart, which has
		// only the management API.
		Setup bool
	}
	mailInfo struct {
		// Hostname is empty on a server with no mail server.
		Hostname string
		// Password is the password of the administrator of the management API.
		Password string
		// Cluster is set on a member of a mail cluster. It has no secret.
		Cluster *mailcluster.Cluster
	}
	mailCertificate struct {
		// Found is true when the edge proxy has a certificate for the host name.
		Found bool
		// Changed is true when the mail server must read the certificate again.
		Changed bool
	}
)

func (d *Deps) mailHome() string {
	if d.MailHome != "" {
		return d.MailHome
	}
	return "/var/lib/cf-mail"
}

func (d *Deps) mailConfigDir() string {
	if d.MailConfigDir != "" {
		return d.MailConfigDir
	}
	return "/etc/cloudfactory/mail"
}

func (d *Deps) mailCertDir() string  { return filepath.Join(d.mailConfigDir(), "certs") }
func (d *Deps) mailEnvFile() string  { return filepath.Join(d.mailConfigDir(), "admin.env") }
func (d *Deps) mailHostFile() string { return filepath.Join(d.mailConfigDir(), "hostname") }
func (d *Deps) mailDoneFile() string { return filepath.Join(d.mailConfigDir(), "configured") }

// mailRootOps are the operations of the root helper for the mail server (RootOps).
func (d *Deps) mailRootOps() map[string]gate.Handler {
	ops := d.mailNodeRootOps()
	for name, op := range d.mailServerRootOps() {
		ops[name] = op
	}
	for name, op := range d.webmailRootOps() {
		ops[name] = op
	}
	return ops
}

func (d *Deps) mailServerRootOps() map[string]gate.Handler {
	return map[string]gate.Handler{
		opMailPrepare: gate.Op(func(ctx context.Context, in mailIn) (mailPrepared, error) {
			return d.prepareMail(ctx, in.Hostname)
		}),
		opMailFirewall:    none(func(ctx context.Context, _ gate.None) error { return d.openMailPorts(ctx) }),
		opMailReady:       gate.Op(func(ctx context.Context, _ gate.None) (bool, error) { return d.finishMailSetup(ctx) }),
		opMailInfo:        gate.Op(func(ctx context.Context, _ gate.None) (mailInfo, error) { return d.mailInfo(ctx) }),
		opMailCertificate: gate.Op(func(ctx context.Context, _ gate.None) (mailCertificate, error) { return d.copyMailCertificate(ctx) }),
	}
}

// prepareMail makes the Unix user of the mail server, its directories, the password of its
// administrator and the unit of its container. Root owns the settings and the certificate:
// the mail server can read them, but cannot change them.
func (d *Deps) prepareMail(ctx context.Context, hostname string) (out mailPrepared, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opMailPrepare, mailIn{Hostname: hostname}, &out)
		return out, err
	}
	if err := validate.Domain(hostname); err != nil {
		return out, err
	}
	if d.MailImage == "" {
		return out, errors.New("the root helper has no image of the mail server")
	}
	home := d.mailHome()
	if err := d.Host.EnsureServiceUser(ctx, host.MailUser, home); err != nil {
		return out, err
	}
	_, gid, err := d.Host.FileOwner(host.MailUser)
	if err != nil {
		return out, err
	}
	// A member of a mail cluster keeps its data in the database node of the same member.
	cluster, err := d.mailCluster()
	if err != nil {
		return out, err
	}
	store, clusterDir := quadlet.MailConfig, ""
	if cluster != nil {
		store, clusterDir = mailcluster.DataStore(cluster.Me().Address), d.mailNodeDir()
	}
	config, data := d.mailConfigDir(), filepath.Join(home, "data")
	if err := os.MkdirAll(d.mailCertDir(), 0o755); err != nil {
		return out, err
	}
	if err := d.serviceDir(ctx, host.MailUser, data); err != nil {
		return out, err
	}
	if _, err := host.WriteFile(filepath.Join(config, "config.json"), store, 0o644); err != nil {
		return out, err
	}
	if _, err := host.WriteFile(d.mailHostFile(), hostname+"\n", 0o644); err != nil {
		return out, err
	}
	if _, err := os.Stat(d.mailDoneFile()); err != nil {
		out.Setup = true
	}
	env, err := d.writeMailEnv(out.Setup, gid)
	if err != nil {
		return out, err
	}
	// The mail server needs a certificate to start. This one is in use until the edge proxy
	// has a certificate for the host name (copyMailCertificate).
	if _, err := os.Stat(filepath.Join(d.mailCertDir(), "tls.crt")); os.IsNotExist(err) {
		certificate, key, err := selfSigned(hostname)
		if err != nil {
			return out, err
		}
		if err := d.writeMailCertificate(certificate, key, gid); err != nil {
			return out, err
		}
	}
	unit := quadlet.MailUnit(quadlet.Mail{Image: d.MailImage, Gvisor: d.runtime(), ConfigDir: config, CertDir: d.mailCertDir(), DataDir: data, EnvFile: d.mailEnvFile(), ClusterDir: clusterDir})
	out.Changed, err = d.Host.WriteUnit(host.MailUser, mailUnit, unit)
	// The container reads the environment file only when it starts. On a server that becomes
	// a member of a cluster, the file has new values.
	out.Changed = out.Changed || env && !out.Setup
	return out, err
}

// writeMailEnv writes the environment file of the container. It keeps a password that
// exists. In the setup, the file also turns on the recovery mode of Stalwart. On a member of
// a mail cluster, the file also has the name of the member and the secrets of the cluster.
// It reports true when the file changed.
func (d *Deps) writeMailEnv(setup bool, gid int) (bool, error) {
	password, _ := d.mailPassword()
	if password == "" {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return false, err
		}
		password = hex.EncodeToString(b)
	}
	content := "STALWART_RECOVERY_ADMIN=" + stalwart.AdminUser + ":" + password + "\n"
	if setup {
		content += "STALWART_RECOVERY_MODE=1\n"
	}
	cluster, err := d.mailCluster()
	if err != nil {
		return false, err
	}
	if cluster != nil {
		secrets, err := d.mailSecrets()
		if err != nil {
			return false, err
		}
		// Each mail server of a cluster has its own name: the cluster knows a server by it.
		content += "STALWART_HOSTNAME=" + cluster.Me().Name + "\n" +
			mailcluster.DBPasswordVariable + "=" + secrets.DBPassword + "\n" +
			mailcluster.KeepSecretVariable + "=" + secrets.KeepSecret + "\n"
	}
	file := d.mailEnvFile()
	changed, err := host.WriteFile(file, content, 0o640)
	if err != nil {
		return false, err
	}
	return changed, chownRoot(file, gid)
}

// chownRoot gives a file to root and a group. A process that is not root, for example a
// test, keeps the owner.
func chownRoot(file string, gid int) error {
	if os.Geteuid() != 0 {
		return nil
	}
	return os.Chown(file, 0, gid)
}

// mailPassword reads the password of the administrator from the environment file.
func (d *Deps) mailPassword() (string, error) {
	b, err := os.ReadFile(d.mailEnvFile())
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if value, ok := strings.CutPrefix(line, "STALWART_RECOVERY_ADMIN="+stalwart.AdminUser+":"); ok {
			return value, nil
		}
	}
	return "", errors.New("the environment file of the mail server has no password")
}

// finishMailSetup ends the setup: the container starts with all its listeners the next time.
// It reports true when the environment file changed.
func (d *Deps) finishMailSetup(ctx context.Context) (changed bool, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opMailReady, nil, &changed)
		return changed, err
	}
	if _, err := os.Stat(d.mailDoneFile()); err == nil {
		return false, nil
	}
	_, gid, err := d.Host.FileOwner(host.MailUser)
	if err != nil {
		return false, err
	}
	if _, err := host.WriteFile(d.mailDoneFile(), time.Now().UTC().Format(time.RFC3339)+"\n", 0o644); err != nil {
		return false, err
	}
	_, err = d.writeMailEnv(false, gid)
	return true, err
}

// mailInfo gives the host name of the mail server and the password of its administrator. The
// worker needs the password for the management API, as it needs the password of the
// administrator of a Valkey database server. A server with no mail server gives no name.
func (d *Deps) mailInfo(ctx context.Context) (info mailInfo, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opMailInfo, nil, &info)
		return info, err
	}
	b, err := os.ReadFile(d.mailHostFile())
	if os.IsNotExist(err) {
		return info, nil
	}
	if err != nil {
		return info, err
	}
	info.Hostname = strings.TrimSpace(string(b))
	if info.Password, err = d.mailPassword(); err != nil {
		return info, err
	}
	info.Cluster, err = d.mailCluster()
	return info, err
}

// openMailPorts writes the firewall rules of the mail server and loads the firewall again:
//
//   - the public ports 25, 465, 587 and 993 go to the ports of the container;
//   - the mail server can open connections only to the DNS, to port 25 of other mail servers
//     and to the ports of an outbound relay (465, 587 and 2525). It cannot connect to a
//     service of this server or to a private network.
//
// The rules are in a file that the firewall file of the installer includes. A server with no
// firewall file of the installer has a firewall that the administrator manages.
func (d *Deps) openMailPorts(ctx context.Context) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opMailFirewall, nil, nil)
	}
	d.firewallMu.Lock()
	defer d.firewallMu.Unlock()
	main := d.firewallFile()
	if _, err := os.Stat(main); os.IsNotExist(err) {
		return nil
	}
	uid, err := d.Host.LookupUID(host.MailUser)
	if err != nil {
		return err
	}
	d.accessMu.Lock()
	defer d.accessMu.Unlock()
	cluster, err := d.mailCluster()
	if err != nil {
		return err
	}
	webmailUID := 0
	if d.WebmailImage != "" {
		// The webmail starts before the mail ports open (SetMailServer): its user exists.
		if webmailUID, err = d.Host.LookupUID(host.WebmailUser); err != nil {
			return err
		}
	}
	changed, err := host.WriteFile(filepath.Join(d.firewallDir(), mailFirewallFile), MailFirewall(uid, webmailUID, cluster), 0o644)
	if err != nil {
		return err
	}
	if !changed {
		// The kernel has the rules, unless a person removed them.
		if rules, err := d.Run.Run(ctx, "nft", "list", "table", "inet", "cloudfactory"); err == nil && strings.Contains(rules, "jump mail") {
			return nil
		}
	}
	// The file of the installer removes its table and makes it again with all included
	// files, in one transaction. A load of only the new file would add its rules a second time.
	if _, err := d.Run.Run(ctx, "nft", "-c", "-f", main); err != nil {
		return err
	}
	_, err = d.Run.Run(ctx, "nft", "-f", main)
	d.accessLoaded = ""
	return err
}

// MailFirewall gives the firewall rules of the mail server. uid is the UID of its Unix user:
// pasta opens each connection of the container as that user. webmailUID is the UID of the
// Unix user of the webmail, or 0 with no webmail. cluster is nil on a server that is not a
// member of a mail cluster.
func MailFirewall(uid, webmailUID int, cluster *mailcluster.Cluster) string {
	var redirects, local, ports []string
	var clusterInput, clusterOutput, clusterMail string
	if cluster != nil {
		indent := func(lines []string) string { return "    " + strings.Join(lines, "\n    ") + "\n" }
		input, output, mail := cluster.Firewall(uid)
		clusterInput, clusterOutput, clusterMail = indent(input), indent(output), indent(mail)
	}
	for _, p := range quadlet.MailPublicPorts() {
		redirects = append(redirects, fmt.Sprintf("    tcp dport %d redirect to :%d", p[0], p[1]))
		local = append(local, fmt.Sprintf("    fib daddr type local tcp dport %d redirect to :%d", p[0], p[1]))
		ports = append(ports, strconv.Itoa(p[1]))
	}
	list := strings.Join(ports, ", ")
	return strings.Join([]string{
		"# The mail server of this server. The Cloudfactory agent writes this file. Do not change it.",
		"table inet cloudfactory {",
		"  chain input {",
		"    tcp dport { " + list + " } accept",
		clusterInput + "  }",
		"  chain prerouting {",
		strings.Join(redirects, "\n"),
		"  }",
		"  chain output_nat {",
		strings.Join(local, "\n"),
		"  }",
		"  chain postrouting_nat {",
		"    ip saddr 127.0.0.0/8 ct status dnat tcp dport { " + list + " } snat ip to " + quadlet.ServiceAddress,
		"  }",
		"  chain mail {",
		"    # DNS. The resolver of the host can have a private or a link-local address.",
		"    meta l4proto { tcp, udp } th dport 53 accept",
		clusterMail + "    # No service of this server, no private network, and no metadata service of a cloud provider.",
		"    fib daddr type { local, broadcast, multicast } jump refuse",
		"    ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 168.63.129.16, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/3 } jump refuse",
		"    ip6 daddr { ::1, fc00::/7, fe80::/10, ff00::/8 } jump refuse",
		"    # Other mail servers, and the ports of an outbound relay.",
		"    tcp dport { 25, 465, 587, 2525 } accept",
		"    jump refuse",
		"  }",
		webmailChain(webmailUID) + "  chain output {",
		clusterOutput + "    ct state new meta skuid " + strconv.Itoa(uid) + " jump mail",
		webmailJump(webmailUID) + "  }",
		"}",
		"",
	}, "\n")
}

// webmailChain gives the rules for the connections of the webmail:
//
//   - the DNS;
//   - the edge proxy of this server: the webmail examines each sign-in on the mail server
//     through the public host name. The firewall sends port 443 of a local address to 8443;
//   - port 443 on the internet, for the extensions of the webmail and for public OpenPGP keys.
//
// The webmail cannot connect to a different service of this server or to a private network.
func webmailChain(uid int) string {
	if uid <= 0 {
		return ""
	}
	return strings.Join([]string{
		"  chain webmail {",
		"    meta l4proto { tcp, udp } th dport 53 accept",
		"    fib daddr type local tcp dport 8443 accept",
		"    fib daddr type { local, broadcast, multicast } jump refuse",
		"    ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 168.63.129.16, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/3 } jump refuse",
		"    ip6 daddr { ::1, fc00::/7, fe80::/10, ff00::/8 } jump refuse",
		"    tcp dport 443 accept",
		"    jump refuse",
		"  }",
		"",
	}, "\n")
}

func webmailJump(uid int) string {
	if uid <= 0 {
		return ""
	}
	return "    ct state new meta skuid " + strconv.Itoa(uid) + " jump webmail\n"
}

// copyMailCertificate gives the mail server the certificate that the edge proxy has for the
// host name of the mail server, with its key. The edge proxy gets and renews the certificate.
//
// The Unix user of the edge proxy owns the storage of the edge proxy, and can put a symbolic
// link in it. Thus root reads the files with no symbolic links (package safefs), and accepts
// only a certificate for the host name with its key.
func (d *Deps) copyMailCertificate(ctx context.Context) (out mailCertificate, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opMailCertificate, nil, &out)
		return out, err
	}
	b, err := os.ReadFile(d.mailHostFile())
	if err != nil {
		return out, err
	}
	hostname := strings.TrimSpace(string(b))
	if err := validate.Domain(hostname); err != nil {
		return out, err
	}
	storage, err := safefs.Open(filepath.Join(d.DataDir, "caddy"))
	if errors.Is(err, os.ErrNotExist) {
		// The edge proxy has no storage yet.
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer storage.Close()
	const certificates = "caddy/certificates"
	issuers, err := storage.ReadDir(certificates)
	if err != nil {
		// The edge proxy has no certificate yet.
		return out, nil
	}
	var best *x509.Certificate
	var bestCertificate, bestKey []byte
	for _, issuer := range issuers {
		base := filepath.Join(certificates, issuer.Name(), hostname, hostname)
		certificate, err := storage.ReadFile(base + ".crt")
		if err != nil {
			continue
		}
		key, err := storage.ReadFile(base + ".key")
		if err != nil {
			continue
		}
		leaf, err := leafFor(certificate, key, hostname)
		if err != nil || time.Now().After(leaf.NotAfter) {
			continue
		}
		if best == nil || leaf.NotAfter.After(best.NotAfter) {
			best, bestCertificate, bestKey = leaf, certificate, key
		}
	}
	if best == nil {
		return out, nil
	}
	out.Found = true
	current, _ := os.ReadFile(filepath.Join(d.mailCertDir(), "tls.crt"))
	if string(current) == string(bestCertificate) {
		return out, nil
	}
	_, gid, err := d.Host.FileOwner(host.MailUser)
	if err != nil {
		return out, err
	}
	out.Changed = true
	return out, d.writeMailCertificate(bestCertificate, bestKey, gid)
}

// writeMailCertificate writes the certificate and its key for the container. Only root and
// the group of the mail server can read the key.
func (d *Deps) writeMailCertificate(certificate, key []byte, gid int) error {
	keyFile := filepath.Join(d.mailCertDir(), "tls.key")
	if _, err := host.WriteFile(keyFile, string(key), 0o640); err != nil {
		return err
	}
	if err := chownRoot(keyFile, gid); err != nil {
		return err
	}
	_, err := host.WriteFile(filepath.Join(d.mailCertDir(), "tls.crt"), string(certificate), 0o644)
	return err
}

// leafFor gives the first certificate of a PEM chain, when it is for the host name and the
// key is its key.
func leafFor(chain, key []byte, hostname string) (*x509.Certificate, error) {
	block, _ := pem.Decode(chain)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("the file is not a certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := leaf.VerifyHostname(hostname); err != nil {
		return nil, err
	}
	keyBlock, _ := pem.Decode(key)
	if keyBlock == nil {
		return nil, errors.New("the file is not a key")
	}
	var private any
	switch keyBlock.Type {
	case "EC PRIVATE KEY":
		private, err = x509.ParseECPrivateKey(keyBlock.Bytes)
	case "RSA PRIVATE KEY":
		private, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	default:
		private, err = x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	}
	if err != nil {
		return nil, err
	}
	public, ok := private.(crypto.Signer)
	if !ok {
		return nil, errors.New("the key has an unknown type")
	}
	if same, ok := leaf.PublicKey.(interface{ Equal(x crypto.PublicKey) bool }); !ok || !same.Equal(public.Public()) {
		return nil, errors.New("the key is not the key of the certificate")
	}
	return leaf, nil
}

// selfSigned makes a certificate for the host name that no client trusts.
func selfSigned(hostname string) (certificate, key []byte, err error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: hostname}, DNSNames: []string{hostname},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(private)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

// serviceDir makes a directory for a service user in the home directory of that user, with
// mode 0700. The service user can write its home directory, so it can put a symbolic link
// there. Root refuses a path that is not a directory: "install -d" would follow the link and
// give its target to the service user (adversarial review, finding G8-05).
func (d *Deps) serviceDir(ctx context.Context, user, dir string) error {
	if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
		return fmt.Errorf("%s is not a directory; remove it", dir)
	}
	_, err := d.Run.Run(ctx, "install", "-d", "-o", user, "-g", user, "-m", "0700", dir)
	return err
}
