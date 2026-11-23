package actions

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
)

const sshKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGtQx2bS8ZJ4Wm1bZ7zO0Zr3wq8yXc2v1q9w8mL3h4kP owner@laptop"

// testTransfer is the transfer of the website of the tests.
var testTransfer = siteTransferIn{WebsiteID: websiteID, User: "acme_x7k2", Peer: "10.207.0.2", Token: moveToken}

func TestFramesCarryAPartAndItsEnd(t *testing.T) {
	var b bytes.Buffer
	w := frameWriter{w: &b}
	data := strings.Repeat("0123456789", maxFrame/4)
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.end(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("next part")); err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(b.Bytes())
	got, err := io.ReadAll(&frameReader{r: r})
	if err != nil || string(got) != data {
		t.Fatalf("the first part: %d bytes, %v", len(got), err)
	}
	// The second part has no end frame: it is not complete.
	got, err = io.ReadAll(&frameReader{r: r})
	if !errors.Is(err, io.ErrUnexpectedEOF) || string(got) != "next part" {
		t.Errorf("a part with no end: %q %v", got, err)
	}
	// A frame that is larger than the maximum stops the part.
	big := bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff, 'x'})
	if _, err := io.ReadAll(&frameReader{r: big}); err == nil {
		t.Error("a frame larger than the maximum was accepted")
	}
}

// sitePair gives the root settings of two servers. The old server has the files of the
// website and the known hosts of its deploys.
func sitePair(t *testing.T) (from, to *Deps, fromRun, toRun *execx.Fake) {
	t.Helper()
	fromRun, toRun = &execx.Fake{}, &execx.Fake{}
	from, to = settings(t.TempDir(), fromRun, &fakeEdge{sites: nil}), settings(t.TempDir(), toRun, &fakeEdge{sites: nil})
	if err := os.MkdirAll(from.knownHosts("acme_x7k2"), 0o700); err != nil {
		t.Fatal(err)
	}
	fromRun.Rules = []execx.Rule{
		{Prefix: "runuser -u acme_x7k2 -- tar --create --file=- --directory=" + from.Host.AppDir("acme_x7k2") + " -- .", Output: strings.Repeat("files ", 400_000)},
		{Prefix: "runuser -u acme_x7k2 -- tar --create --file=- --directory=" + filepath.Dir(from.knownHosts("acme_x7k2")) + " -- .cf-ssh", Output: "known hosts"},
	}
	return from, to, fromRun, toRun
}

// transfer sends the website from one server to the other through a pipe, and gives the
// result of the server that receives.
func transfer(t *testing.T, from, to *Deps) (sent error, ok bool, received error) {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() {
		err := from.writeSiteTransfer(context.Background(), a, testTransfer)
		a.Close()
		done <- err
	}()
	ok, received = to.receiveSiteFrom(context.Background(), b, testTransfer)
	b.Close()
	return <-done, ok, received
}

func TestAWebsiteMoveCarriesTheFilesAndTheKnownHosts(t *testing.T) {
	from, to, _, toRun := sitePair(t)
	sent, ok, received := transfer(t, from, to)
	if sent != nil || !ok || received != nil {
		t.Fatalf("send: %v, receive: %v %v", sent, ok, received)
	}
	app := to.Host.AppDir("acme_x7k2")
	// The website user empties the directory, and writes the files.
	want := []string{
		"runuser -u acme_x7k2 -- find " + app + " -mindepth 1 -delete",
		"runuser -u acme_x7k2 -- tar --extract --file=- --directory=" + app + " --no-same-owner --preserve-permissions",
		"runuser -u acme_x7k2 -- tar --extract --file=- --directory=" + filepath.Dir(to.knownHosts("acme_x7k2")) + " --no-same-owner --preserve-permissions -- .cf-ssh",
	}
	for _, w := range want {
		if !toRun.Called(w) {
			t.Errorf("not called: %s\ncalls: %q", w, toRun.Calls)
		}
	}
	for i, c := range toRun.Calls {
		switch c {
		case want[1]:
			if toRun.Inputs[i] != strings.Repeat("files ", 400_000) {
				t.Errorf("tar got %d bytes of the files", len(toRun.Inputs[i]))
			}
		case want[2]:
			if toRun.Inputs[i] != "known hosts" {
				t.Errorf("tar got the known hosts %q", toRun.Inputs[i])
			}
		}
	}
}

// A website with no deploy has no known hosts. Its second part is empty.
func TestAWebsiteWithNoKnownHostsMoves(t *testing.T) {
	from, to, _, toRun := sitePair(t)
	if err := os.RemoveAll(from.knownHosts("acme_x7k2")); err != nil {
		t.Fatal(err)
	}
	sent, ok, received := transfer(t, from, to)
	if sent != nil || !ok || received != nil {
		t.Fatalf("send: %v, receive: %v %v", sent, ok, received)
	}
	if toRun.Called("runuser -u acme_x7k2 -- tar --extract --file=- --directory=" + filepath.Dir(to.knownHosts("acme_x7k2"))) {
		t.Error("tar wrote known hosts that the website does not have")
	}
}

// The old server sends the last frame only when its tar read all files. Before that, tar
// gave a valid archive with no file that it could not read, and the new server would accept
// it.
func TestAWebsiteMoveFailsWhenTarCannotReadAFile(t *testing.T) {
	from, to, fromRun, _ := sitePair(t)
	fromRun.Rules[0].Err = errors.New("tar: ./secret: Cannot open: Permission denied")
	sent, ok, received := transfer(t, from, to)
	if sent == nil {
		t.Error("the old server reported that it sent all files")
	}
	if !ok || !errors.Is(received, io.ErrUnexpectedEOF) {
		t.Errorf("the new server accepted a part of the files: %v %v", ok, received)
	}
}

func TestAWebsiteMoveRefusesAWrongToken(t *testing.T) {
	_, to, _, toRun := sitePair(t)
	a, b := net.Pipe()
	go func() {
		_, _ = a.Write([]byte(siteHello + strings.Repeat("f", 64) + "\n"))
		a.Close()
	}()
	ok, err := to.receiveSiteFrom(context.Background(), b, testTransfer)
	if ok || err != nil {
		t.Errorf("a wrong token: %v %v", ok, err)
	}
	if len(toRun.Calls) != 0 {
		t.Errorf("the server changed the files for a wrong token: %q", toRun.Calls)
	}
	// The hello of a database server is not the hello of a website.
	if readHello(bufio.NewReader(strings.NewReader(transferHello+moveToken+"\n")), siteHello, moveToken) {
		t.Error("the hello of a database server opened a website")
	}
}

func TestSendWebsiteStopsTheWebsiteAndStartsItAgainAfterAFailure(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		d, f, edge := deps(t)
		run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
		keys := filepath.Join(d.SftpKeysDir, "acme_x7k2.keys")
		if err := os.MkdirAll(d.SftpKeysDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keys, []byte("restrict "+sshKey+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if stopped {
			run(t)(d.SetWebsitePower(&agentv1.SetWebsitePowerRequest{JobId: jobID2, WebsiteId: websiteID, Power: "stop"}))
		}
		f.Calls = nil
		// The other server is not a peer of this server: the files cannot go.
		job, err := d.SendWebsite(&agentv1.SendWebsiteRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", To: "10.207.0.2", Token: moveToken, Stopped: stopped})
		if len(job.Steps) != 2 {
			t.Fatalf("steps = %d, want 2 (the panel shows 2)", len(job.Steps))
		}
		err = runSteps(t, job, err)
		if err == nil || !strings.Contains(err.Error(), "not a peer") {
			t.Fatalf("stopped %v: a move to a server that is not a peer: %v", stopped, err)
		}
		if !f.Called("systemctl --user --machine=acme_x7k2@ stop cf-acme_x7k2.service") {
			t.Errorf("the website did not stop: %q", f.Calls)
		}
		if b, _ := os.ReadFile(keys); strings.Contains(string(b), sshKey) {
			t.Error("SFTP stayed open while the files moved")
		}
		site, _ := d.State.Get(websiteID)
		started := f.Called("systemctl --user --machine=acme_x7k2@ start cf-acme_x7k2.service")
		if stopped == started || site.Stopped != stopped || edge.sites[websiteID].Stopped != stopped {
			t.Errorf("stopped before the move %v: started again %v, stopped now %v", stopped, started, site.Stopped)
		}
	}
}

// sendFails is the gate of a worker whose root helper gives err for the files of a website.
type sendFails struct {
	testGate
	err error
}

func (g sendFails) Call(ctx context.Context, op string, in, out any) error {
	if op == opSiteSend {
		return g.err
	}
	return g.testGate.Call(ctx, op, in, out)
}

func TestSendWebsiteKeepsAStoppedCopyWhenTheNewServerDidNotAnswer(t *testing.T) {
	d, f, edge := deps(t)
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	// The root helper sent all the files, and the new server did not answer. The new server
	// possibly has the files and starts the website.
	d.Gate = sendFails{testGate: d.Gate.(testGate), err: fmt.Errorf("this server %s of the website acme_x7k2: EOF", sentNoAnswer)}
	f.Calls = nil
	job, err := d.SendWebsite(&agentv1.SendWebsiteRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", To: "10.207.0.2", Token: moveToken})
	err = runSteps(t, job, err)
	if err == nil || !strings.Contains(err.Error(), "stays stopped") {
		t.Fatalf("a send with no answer: %v", err)
	}
	site, _ := d.State.Get(websiteID)
	if !site.Stopped || !edge.sites[websiteID].Stopped || f.Called("systemctl --user --machine=acme_x7k2@ start") {
		t.Errorf("the website started again on the old server: %q", f.Calls)
	}
}

func TestReceiveWebsiteStartsTheWebsiteWithItsAccess(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		d, f, edge := deps(t)
		w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
		req := &agentv1.ReceiveWebsiteRequest{
			JobId: jobID, Website: w, From: "10.207.0.2", Token: moveToken, Stopped: stopped,
			SftpEnabled: true, SftpKeys: []string{sshKey},
			CronJobs: []*agentv1.CronJob{{Id: jobID2, Schedule: "*/5 * * * *", Command: "php artisan schedule:run", Enabled: true}},
		}
		job, err := d.ReceiveWebsite(req)
		if err != nil {
			t.Fatal(err)
		}
		if len(job.Steps) != 7 {
			t.Fatalf("steps = %d, want 7 (the panel shows 7)", len(job.Steps))
		}
		// An earlier run of the job received the files.
		root := rootOf(d)
		if err := os.MkdirAll(filepath.Dir(root.moveMarker("acme_x7k2")), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(root.moveMarker("acme_x7k2"), []byte(moveToken+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runSteps(t, job, nil); err != nil {
			t.Fatal(err)
		}
		if job.Result()["dnsPointsHere"] != true || job.Result()["port"] != 20000 {
			t.Errorf("result = %v", job.Result())
		}
		if f.Called("runuser -u acme_x7k2 -- tar --extract") {
			t.Error("the job received the files again")
		}
		started := f.Called("systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2.service")
		if started == stopped {
			t.Errorf("stopped %v: started %v", stopped, started)
		}
		unit, err := os.ReadFile(filepath.Join(d.Host.UnitRoot, "20001", "cf-acme_x7k2.container"))
		if err != nil || strings.Contains(string(unit), "[Install]") != !stopped {
			t.Errorf("stopped %v: the unit: %v\n%s", stopped, err, unit)
		}
		if s := edge.sites[websiteID]; s.Port != 20000 || s.Stopped != stopped || len(s.Hosts) != 1 {
			t.Errorf("the edge route: %+v", s)
		}
		if b, _ := os.ReadFile(filepath.Join(d.SftpKeysDir, "acme_x7k2.keys")); !strings.Contains(string(b), "restrict "+sshKey) {
			t.Errorf("the SFTP keys: %s", b)
		}
		sites, err := d.State.cronSites()
		if err != nil {
			t.Fatal(err)
		}
		for site, entries := range sites {
			if site.ID != websiteID || len(entries) != 1 || entries[0].Command != "php artisan schedule:run" {
				t.Errorf("the cron jobs: %v %+v", site.ID, entries)
			}
		}
		if len(sites) != 1 {
			t.Errorf("the cron jobs of %d websites", len(sites))
		}
		// The removal of the website removes the token of the move.
		if err := run(t)(d.DeleteWebsite(&agentv1.DeleteWebsiteRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2"})); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(root.moveMarker("acme_x7k2")); !os.IsNotExist(err) {
			t.Errorf("the token of the move stayed: %v", err)
		}
	}
}

func TestReceiveWebsiteRefusesBadValues(t *testing.T) {
	d, _, _ := deps(t)
	w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	for name, req := range map[string]*agentv1.ReceiveWebsiteRequest{
		"a short token":        {JobId: jobID, Website: w, From: "10.207.0.2", Token: "00ff"},
		"a host name":          {JobId: jobID, Website: w, From: "example.com", Token: moveToken},
		"an SFTP key with ops": {JobId: jobID, Website: w, From: "10.207.0.2", Token: moveToken, SftpKeys: []string{`command="id" ` + sshKey}},
		"a bad cron job":       {JobId: jobID, Website: w, From: "10.207.0.2", Token: moveToken, CronJobs: []*agentv1.CronJob{{Id: jobID2, Schedule: "every day", Command: "x"}}},
		"no website":           {JobId: jobID, From: "10.207.0.2", Token: moveToken},
	} {
		if _, err := d.ReceiveWebsite(req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPruneBackupsKeepsTheDatabasesOfAMovedWebsite(t *testing.T) {
	d, f := boundary(t)
	f.Rules = []execx.Rule{{Prefix: resticHead + "snapshots --tag " + tagOne, Output: `[{"id":"aaaaaaaa"}]`}}
	job, err := d.PruneBackups(&agentv1.PruneBackupsRequest{JobId: jobID, Target: "s3:b/c", Credentials: map[string]string{"AWS_ACCESS_KEY_ID": "k"}, ForgetMovedWebsiteIds: []string{websiteID}})
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		resticHead + "snapshots --tag " + tagOne + ",kind=scheduled",
		resticHead + "snapshots --tag " + tagOne + ",kind=manual",
		resticHead + "forget aaaaaaaa",
	} {
		if !f.Called(want) {
			t.Errorf("not called: %s\ncalls: %q", want, f.Calls)
		}
	}
	for _, c := range f.Calls {
		if c == resticHead+"snapshots --tag "+tagOne || strings.Contains(c, "-databases") {
			t.Errorf("the prune touched the snapshots of the databases: %s", c)
		}
	}
}
