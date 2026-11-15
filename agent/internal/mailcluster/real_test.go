package mailcluster_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/mailcluster"
	"github.com/itbm/cloudfactory/agent/internal/stalwart"
)

// TestRealCluster makes a mail cluster of three members with Docker, from the settings that
// this package writes, and stops one member while mail arrives. It needs the three images
// and some minutes:
//
//	CF_TEST_MAIL_CLUSTER=1 CF_TEST_MAIL_IMAGE=cf-mail:test \
//	CF_TEST_MAIL_DB_IMAGE=mariadb:11.4 CF_TEST_MAIL_KEEP_IMAGE=ghcr.io/stalwartlabs/keep:latest \
//	go test ./internal/mailcluster/ -run TestRealCluster -v -timeout 20m
//
// Each member is three containers that share one network address: the database node, the
// member of the cluster store and the mail server. On a server, the three containers use the
// address of the server in the private network.
func TestRealCluster(t *testing.T) {
	if os.Getenv("CF_TEST_MAIL_CLUSTER") == "" {
		t.Skip("CF_TEST_MAIL_CLUSTER is not set")
	}
	mailImage, dbImage, keepImage := os.Getenv("CF_TEST_MAIL_IMAGE"), os.Getenv("CF_TEST_MAIL_DB_IMAGE"), os.Getenv("CF_TEST_MAIL_KEEP_IMAGE")
	const (
		network       = "cf-mail-cluster-test"
		admin         = "secret123"
		dbPassword    = "0123456789abcdef0123456789abcdef"
		clusterSecret = "00112233445566778899aabbccddeeff"
		peerSecret    = "ffeeddccbbaa99887766554433221100"
		domain        = "cluster-test.example.com"
	)
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remove := func() {
		for i := 1; i <= 3; i++ {
			for _, kind := range []string{"mail", "keep", "db"} {
				_ = exec.Command("docker", "rm", "-f", "-v", fmt.Sprintf("cfct-%s%d", kind, i)).Run()
			}
			_ = exec.Command("docker", "volume", "rm", fmt.Sprintf("cfct-data%d", i)).Run()
		}
		_ = exec.Command("docker", "network", "rm", network).Run()
	}
	remove()
	if os.Getenv("CF_TEST_MAIL_CLUSTER_KEEP") == "" {
		t.Cleanup(remove)
	}
	docker("network", "create", "--subnet", "10.77.25.0/24", network)

	// The keys and the settings of each member.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cluster := mailcluster.Cluster{Name: "cf-test", Hostname: "mail.example.net"}
	keepKeys, nodeKeys := map[int]string{}, map[int]string{}
	for i := 1; i <= 3; i++ {
		_, wireguard, err := mailcluster.NewWireGuardKey()
		if err != nil {
			t.Fatal(err)
		}
		m := mailcluster.Member{ID: i, Name: fmt.Sprintf("mail%d.example.net", i), Endpoint: fmt.Sprintf("203.0.113.%d", i), Address: fmt.Sprintf("10.77.25.%d", 10+i), Region: i - 1, WireGuardKey: wireguard}
		if keepKeys[i], m.KeepKey, err = mailcluster.NewKey(); err != nil {
			t.Fatal(err)
		}
		if nodeKeys[i], m.NodeKey, err = mailcluster.NewKey(); err != nil {
			t.Fatal(err)
		}
		cluster.Members = append(cluster.Members, m)
	}
	write := func(name, content string) {
		t.Helper()
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	certificate, key := selfSigned(t)
	write("certs/tls.crt", certificate)
	write("certs/tls.key", key)
	for i := 1; i <= 3; i++ {
		c := cluster
		c.Self = i
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		member := fmt.Sprintf("m%d/", i)
		write(member+"db/galera.cnf", c.Galera())
		write(member+"keep/keep.toml", c.Keep(1))
		write(member+"keep/keep.key", keepKeys[i])
		write(member+"keep/node.key", nodeKeys[i])
		write(member+"keep/servers.pem", c.ServerKeys())
		write(member+"keep/clients.pem", c.ClientKeys())
		write(member+"keep/cluster.secret", clusterSecret+"\n")
		write(member+"keep/peer.secret", peerSecret+"\n")
		write(member+"mail/config.json", mailcluster.DataStore(c.Me().Address))
	}

	name := func(kind string, i int) string { return fmt.Sprintf("cfct-%s%d", kind, i) }
	startDB := func(i int, bootstrap bool) {
		// The limits are the limits of the unit of the database node on a server.
		args := []string{"run", "-d", "--name", name("db", i), "--network", network, "--ip", cluster.Members[i-1].Address,
			"--user", "999:999", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only", "--tmpfs", "/tmp", "--tmpfs", "/run/mysqld",
			"-p", fmt.Sprintf("%d:8080", 19180+i), "-p", fmt.Sprintf("%d:2025", 19120+i),
			"-v", filepath.Join(dir, fmt.Sprintf("m%d/db", i)) + ":/etc/mysql/conf.d:ro", "-v", fmt.Sprintf("cfct-data%d:/var/lib/mysql", i),
			"-e", "MARIADB_RANDOM_ROOT_PASSWORD=1", "-e", "MARIADB_DATABASE=" + mailcluster.Database, "-e", "MARIADB_USER=" + mailcluster.DatabaseUser, "-e", "MARIADB_PASSWORD=" + dbPassword,
			dbImage}
		if bootstrap {
			args = append(args, "--wsrep-new-cluster")
		}
		docker(args...)
	}
	size := func(i int) string {
		out, _ := exec.Command("docker", "exec", name("db", i), "mariadb", "-u"+mailcluster.DatabaseUser, "-p"+dbPassword, "-N", "-e", "show status like 'wsrep_cluster_size'; show status like 'wsrep_local_state_comment'").CombinedOutput()
		return strings.Join(strings.Fields(string(out)), " ")
	}
	waitFor := func(what string, limit time.Duration, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(limit)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout: %s", what)
			}
			time.Sleep(2 * time.Second)
		}
	}
	synced := func(i, members int) func() bool {
		return func() bool {
			return size(i) == fmt.Sprintf("wsrep_cluster_size %d wsrep_local_state_comment Synced", members)
		}
	}
	startKeep := func(i int, mode string) {
		docker("run", "-d", "--name", name("keep", i), "--network", "container:"+name("db", i),
			"--user", "2000:2000", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
			"-v", filepath.Join(dir, fmt.Sprintf("m%d/keep", i))+":"+mailcluster.KeepDir+":ro",
			keepImage, "--config", mailcluster.KeepDir+"/keep.toml", "--start", mode)
	}
	client := func(i int) *stalwart.Client {
		return &stalwart.Client{URL: fmt.Sprintf("http://127.0.0.1:%d", 19180+i), Password: admin}
	}
	startMail := func(i int, recovery bool) {
		_ = exec.Command("docker", "rm", "-f", name("mail", i)).Run()
		args := []string{"run", "-d", "--name", name("mail", i), "--network", "container:" + name("db", i),
			"-v", filepath.Join(dir, fmt.Sprintf("m%d/mail", i)) + ":/etc/stalwart:ro",
			"-v", filepath.Join(dir, fmt.Sprintf("m%d/keep", i)) + ":" + mailcluster.KeepDir + ":ro",
			"-v", filepath.Join(dir, "certs") + ":/etc/stalwart-certs:ro",
			"-e", "STALWART_RECOVERY_ADMIN=" + stalwart.AdminUser + ":" + admin, "-e", "STALWART_HOSTNAME=" + cluster.Members[i-1].Name,
			"-e", mailcluster.DBPasswordVariable + "=" + dbPassword, "-e", mailcluster.KeepSecretVariable + "=" + clusterSecret}
		if recovery {
			args = append(args, "-e", "STALWART_RECOVERY_MODE=1")
		}
		docker(append(args, mailImage)...)
		waitFor(fmt.Sprintf("mail server %d answers", i), 2*time.Minute, func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return client(i).Ready(ctx) == nil
		})
	}

	// The first database node starts the cluster. The other nodes get a copy of its data.
	startDB(1, true)
	waitFor("the first database node is ready", 3*time.Minute, synced(1, 1))
	startDB(2, false)
	startDB(3, false)
	waitFor("three database nodes", 5*time.Minute, synced(3, 3))
	for i := 1; i <= 3; i++ {
		startKeep(i, "bootstrap")
	}

	// The first mail server applies the settings in the recovery mode. The settings are in
	// the database, so the other mail servers have them when they start.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	startMail(1, true)
	settings := stalwart.Settings{Hostname: "mail.example.net", Ports: stalwart.Ports{SMTP: 2025, Submissions: 2465, Submission: 2587, IMAPS: 2993, HTTP: 8080}, CertFile: "/etc/stalwart-certs/tls.crt", KeyFile: "/etc/stalwart-certs/tls.key"}
	if _, err := client(1).Configure(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if restart, err := client(1).SetCluster(ctx, cluster.InMemoryStore()); err != nil || !restart {
		t.Fatalf("restart %v, err %v", restart, err)
	}
	if restart, err := client(1).SetCluster(ctx, cluster.InMemoryStore()); err != nil || restart {
		t.Fatalf("the second time: restart %v, err %v", restart, err)
	}
	for i := 1; i <= 3; i++ {
		startMail(i, false)
	}
	// A member that applies the settings again changes nothing.
	if restart, err := client(2).Configure(ctx, settings); err != nil || restart {
		t.Fatalf("restart %v, err %v", restart, err)
	}
	nodes, err := client(3).Nodes(ctx)
	if err != nil || len(nodes) != 3 || nodes[0] != (stalwart.Node{Hostname: "mail1.example.net", Status: "active"}) {
		t.Fatalf("nodes %v, err %v", nodes, err)
	}

	// A mailbox that member 1 makes is on each member.
	if _, err := client(1).SetDomain(ctx, domain); err != nil {
		t.Fatal(err)
	}
	// hash is the bcrypt hash of password, in the form that the panel sends.
	const password, hash = "Testpass-123456", "$2y$12$Wlrsi0RSciVGEgjvW9gO5Od8pb68WpSyRgIWT2idZjFTm7JeR4AW2"
	if err := client(1).SetMailbox(ctx, domain, "info", 1<<30, hash); err != nil {
		t.Fatal(err)
	}
	address := "info@" + domain
	send := func(i int, subject string) error {
		body := fmt.Sprintf("From: someone@example.org\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <%d@example.org>\r\n\r\n%s\r\n", address, subject, time.Now().UnixNano(), strings.Repeat("The text of the message. ", 400))
		c, err := smtp.Dial(fmt.Sprintf("127.0.0.1:%d", 19120+i))
		if err != nil {
			return err
		}
		defer c.Close()
		if err := c.Hello("client.example.org"); err != nil {
			return err
		}
		if err := c.Mail("someone@example.org"); err != nil {
			return err
		}
		if err := c.Rcpt(address); err != nil {
			return err
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(body)); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		// The server has the message now. An error after this point is not a reason to send
		// the message again.
		_ = c.Quit()
		return nil
	}
	// count gives the number of messages in the mailbox, as the owner of the mailbox sees it
	// through member i. It gives -1 when the member does not answer.
	count := func(i int) int {
		call := func(method, path, body string, into any) error {
			req, _ := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", 19180+i, path), strings.NewReader(body))
			req.SetBasicAuth(address, password)
			req.Header.Set("Content-Type", "application/json")
			res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if err != nil {
				return err
			}
			defer res.Body.Close()
			return json.NewDecoder(res.Body).Decode(into)
		}
		var session struct {
			PrimaryAccounts map[string]string `json:"primaryAccounts"`
		}
		if err := call("GET", "/jmap/session", "", &session); err != nil {
			return -1
		}
		account := session.PrimaryAccounts["urn:ietf:params:jmap:mail"]
		var answer struct {
			MethodResponses [][]json.RawMessage `json:"methodResponses"`
		}
		query := fmt.Sprintf(`{"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"],"methodCalls":[["Email/query",{"accountId":%q,"calculateTotal":true,"limit":1},"a"]]}`, account)
		if err := call("POST", "/jmap", query, &answer); err != nil || len(answer.MethodResponses) != 1 || len(answer.MethodResponses[0]) < 2 {
			return -1
		}
		var result struct {
			Total *int `json:"total"`
		}
		if err := json.Unmarshal(answer.MethodResponses[0][1], &result); err != nil || result.Total == nil {
			return -1
		}
		return *result.Total
	}
	arrived := func(i, messages int) func() bool { return func() bool { return count(i) >= messages } }
	// The mail servers accept a limited number of messages each second from one address, and
	// they count together in the cluster store. A refused message is not in a mailbox:
	// deliver sends it again until the limit of time, as a mail server on the internet does.
	deliver := func(i int, subject string, limit time.Duration) error {
		deadline := time.Now().Add(limit)
		err := send(i, subject)
		for err != nil && time.Now().Before(deadline) {
			time.Sleep(3 * time.Second)
			err = send(i, subject)
		}
		return err
	}
	if err := send(2, "through member 2"); err != nil {
		t.Fatal(err)
	}
	waitFor("member 3 has the message that member 2 got", time.Minute, arrived(3, 1))

	// Mail through all members at the same time: each message arrives one time.
	const each = 8
	var group sync.WaitGroup
	failures := make(chan error, 3*each)
	for i := 1; i <= 3; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for n := 0; n < each; n++ {
				err := deliver(i, fmt.Sprintf("at the same time %d-%d", i, n), time.Minute)
				if err != nil {
					failures <- err
				}
				time.Sleep(700 * time.Millisecond)
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("send: %v", err)
	}
	total := 1 + 3*each
	waitFor("all messages are in the mailbox", 2*time.Minute, arrived(1, total))
	time.Sleep(5 * time.Second)
	if got := count(2); got != total {
		t.Fatalf("the mailbox has %d messages, not %d", got, total)
	}

	// Member 1 fails: its three containers stop at once. The other members continue.
	for _, kind := range []string{"mail", "keep", "db"} {
		docker("kill", name(kind, 1))
	}
	waitFor("two database nodes continue", time.Minute, synced(2, 2))
	// The cluster store refuses new locks and counters on the keys of the failed member for
	// about ten minutes. A mail server that needs such a key refuses the message for that
	// time with a temporary error, and the sender tries again.
	if err := deliver(3, "after the failure of member 1", 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := client(2).SetMailbox(ctx, domain, "second", 1<<30, hash); err != nil {
		t.Fatalf("a change on member 2 after the failure: %v", err)
	}
	// The cluster store refuses new locks on the keys of the failed member for about ten
	// minutes, so a delivery that needs such a lock waits for that time.
	waitFor("member 2 has the message that member 3 got after the failure", 15*time.Minute, arrived(2, total+1))

	// Member 1 comes back with the data that it had: its database node gets the changes that
	// it missed. It does not start a new cluster.
	for _, kind := range []string{"mail", "keep", "db"} {
		docker("rm", "-f", name(kind, 1))
	}
	startDB(1, false)
	waitFor("three database nodes again", 5*time.Minute, synced(1, 3))
	startKeep(1, "rejoin")
	startMail(1, false)
	waitFor("member 1 has all messages", time.Minute, arrived(1, total+1))
	usage, err := client(1).Usage(ctx)
	if _, ok := usage["second@"+domain]; err != nil || !ok {
		t.Fatalf("member 1 does not have the mailbox that member 2 made: %v, err %v", usage, err)
	}
}

// selfSigned gives a certificate for the mail servers of the test, and its key.
func selfSigned(t *testing.T) (certificate, key string) {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mail.example.net"}, DNSNames: []string{"mail.example.net"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}
