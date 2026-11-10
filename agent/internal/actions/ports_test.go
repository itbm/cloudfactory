package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
)

const (
	websiteID2 = "01JBQ3Z5K8N6T4W2X9Y7R5P3M9"
	image84    = "ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1"
	image85    = "ghcr.io/cloudfactory/frankenphp-php8.5:8.5.0-1"
)

func secondWebsite(image string) *agentv1.Website {
	w := website(image)
	w.Id, w.UnixUser = websiteID2, "beta_y8m3"
	w.Domains = []*agentv1.Domain{{Name: "beta.example", Kind: "primary"}}
	return w
}

var publishPort = regexp.MustCompile(`PublishPort=\S+`)

func publishedPort(t *testing.T, d *Deps, unit string) string {
	t.Helper()
	return publishPort.FindString(unitFile(t, d, "any", unit))
}

// Audit finding B6: the runner starts the jobs of different websites at the same time. Two
// changes of a runtime got the same port for their new containers, because the search for a
// free port and the record of the port were two steps.
func TestTwoRuntimeChangesGetDifferentPorts(t *testing.T) {
	d, _, _ := deps(t)
	for i, w := range []*agentv1.Website{website(image84), secondWebsite(image84)} {
		job, err := d.CreateWebsite(&agentv1.WebsiteRequest{JobId: []string{jobID, jobID2}[i], Website: w})
		if err := run(t)(job, err); err != nil {
			t.Fatal(err)
		}
	}
	jobA, err := d.SetRuntime(&agentv1.WebsiteRequest{JobId: "01JBQ3Z5K8N6T4W2X9Y7R5P3N1", Website: website(image85)})
	if err != nil {
		t.Fatal(err)
	}
	jobB, err := d.SetRuntime(&agentv1.WebsiteRequest{JobId: "01JBQ3Z5K8N6T4W2X9Y7R5P3N2", Website: secondWebsite(image85)})
	if err != nil {
		t.Fatal(err)
	}
	// both runs one step of the two jobs, as the runner does for two websites at the same time.
	both := func(steps ...int) {
		t.Helper()
		for _, i := range steps {
			for _, job := range []struct{ do func(context.Context) error }{{jobA.Steps[i].Do}, {jobB.Steps[i].Do}} {
				if err := job.do(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// The two jobs are at their health checks at the same time.
	both(0, 1)
	a, b := publishedPort(t, d, "cf-acme_x7k2-b.container"), publishedPort(t, d, "cf-beta_y8m3-b.container")
	if a == "" || a == b {
		t.Fatalf("the two new containers publish %q and %q", a, b)
	}
	// Four ports are in use now: one old and one new container for each website.
	sites, _ := d.State.All()
	ports := map[int]bool{}
	for _, s := range sites {
		ports[s.Port], ports[s.NextPort] = true, true
	}
	if len(ports) != 4 || ports[0] {
		t.Errorf("ports in use = %v, want 4 different ports", ports)
	}

	// The rest of the two jobs. Then each website has one port, and the old ports are free.
	both(2, 3, 4)
	sa, _ := d.State.Get(websiteID)
	sb, _ := d.State.Get(websiteID2)
	if sa.NextPort != 0 || sb.NextPort != 0 || sa.Port == sb.Port || sa.Port != 20002 || sb.Port != 20003 {
		t.Errorf("after the change: A = %+v, B = %+v", sa, sb)
	}
	c := website(image84)
	c.Id, c.UnixUser, c.Domains = "01JBQ3Z5K8N6T4W2X9Y7R5P3MA", "gamma_z1q9", []*agentv1.Domain{{Name: "gamma.example", Kind: "primary"}}
	job, err := d.CreateWebsite(&agentv1.WebsiteRequest{JobId: "01JBQ3Z5K8N6T4W2X9Y7R5P3N3", Website: c})
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	if sc, _ := d.State.Get(c.Id); sc.Port != 20000 {
		t.Errorf("the new website has port %d, want the free port 20000", sc.Port)
	}
}

// A change of a runtime that fails gives its second port back.
func TestAFailedRuntimeChangeGivesItsPortBack(t *testing.T) {
	d, f, _ := deps(t)
	job, err := d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website(image84)})
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	f.Rules = []execx.Rule{{Prefix: "systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2-b.service", Err: errors.New("the unit failed")}}
	job, err = d.SetRuntime(&agentv1.WebsiteRequest{JobId: jobID2, Website: website(image85)})
	if err := runSteps(t, job, err); err == nil {
		t.Fatal("the job did not fail")
	}
	site, _ := d.State.Get(websiteID)
	if site.NextPort != 0 || site.Port != 20000 || imageRef(site.Website) != image84 {
		t.Errorf("site = %+v", site)
	}
}

// Many jobs at the same time: each website gets its own port.
func TestReserveGivesEachSiteItsOwnPort(t *testing.T) {
	s := NewState(t.TempDir())
	const n = 40
	ports := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "site-" + strings.Repeat("x", i+1)
			p, err := s.Reserve(&Site{ID: id, UnixUser: "u", Website: &agentv1.Website{Id: id}})
			if err != nil {
				t.Error(err)
			}
			if i%2 == 0 {
				if p, err = s.ReserveNext(id); err != nil {
					t.Error(err)
				}
			}
			ports[i] = p
		}()
	}
	wg.Wait()
	seen := map[int]bool{}
	for _, p := range ports {
		if p < 20000 || seen[p] {
			t.Fatalf("port %d is not valid or two sites have it: %v", p, ports)
		}
		seen[p] = true
	}
	sites, _ := s.All()
	all := map[int]bool{}
	for _, site := range sites {
		for _, p := range []int{site.Port, site.NextPort} {
			if p != 0 && all[p] {
				t.Fatalf("port %d is in the state two times", p)
			}
			all[p] = true
		}
	}

	// A site that the state knows keeps its port, and a second ReserveNext gives the same port.
	first, _ := s.Reserve(&Site{ID: "site-x", UnixUser: "u", Website: &agentv1.Website{Id: "site-x"}})
	again, _ := s.Reserve(&Site{ID: "site-x", UnixUser: "u", Website: &agentv1.Website{Id: "site-x"}})
	next1, _ := s.ReserveNext("site-x")
	next2, _ := s.ReserveNext("site-x")
	if first != again || next1 != next2 || next1 == first {
		t.Errorf("first = %d, again = %d, next = %d and %d", first, again, next1, next2)
	}
	if err := s.ReleaseNext("site-x"); err != nil {
		t.Fatal(err)
	}
	if site, _ := s.Get("site-x"); site.NextPort != 0 {
		t.Errorf("the port %d is not free", site.NextPort)
	}
	if _, err := s.ReserveNext("unknown"); err == nil {
		t.Error("ReserveNext accepted a site that the state does not have")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.Path), "sites.json")); err != nil {
		t.Error(err)
	}
}
