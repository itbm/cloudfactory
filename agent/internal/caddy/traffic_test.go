package caddy

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleMetrics = `# HELP caddy_http_request_size_bytes Total size of the request. Includes body
# TYPE caddy_http_request_size_bytes histogram
caddy_http_request_size_bytes_bucket{code="200",handler="reverse_proxy",host="example.com",method="GET",server="edge",le="256"} 3
caddy_http_request_size_bytes_sum{code="200",handler="reverse_proxy",host="example.com",method="GET",server="edge"} 1200
caddy_http_request_size_bytes_sum{code="404",handler="reverse_proxy",host="Example.com",method="POST",server="edge"} 300
caddy_http_request_size_bytes_sum{code="200",handler="reverse_proxy",host="_other",method="GET",server="edge"} 99
caddy_http_response_size_bytes_sum{code="200",handler="reverse_proxy",host="example.com",method="GET",server="edge"} 5.5e+06
caddy_http_response_size_bytes_sum{code="308",handler="static_response",host="www.example.com:8443",method="GET",server="edge"} 40
caddy_http_response_size_bytes_sum{code="200",handler="reverse_proxy",method="GET",server="edge"} 7
caddy_http_response_size_bytes_sum{code="200",host="broken.example"}
caddy_http_requests_total{host="example.com"} 4
`

func TestParseTraffic(t *testing.T) {
	got, err := ParseTraffic(bufio.NewScanner(strings.NewReader(sampleMetrics)))
	if err != nil {
		t.Fatal(err)
	}
	if got["example.com"] != (Traffic{In: 1500, Out: 5500000}) {
		t.Errorf("example.com = %+v", got["example.com"])
	}
	if got["www.example.com"] != (Traffic{Out: 40}) {
		t.Errorf("www.example.com = %+v", got["www.example.com"])
	}
	if len(got) != 2 {
		t.Errorf("hosts = %v, want only example.com and www.example.com", got)
	}
}

func TestTrafficTurnsOnMetrics(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/config/apps/http/metrics":
			if posted {
				w.Write([]byte(`{"per_host":true}`))
			} else {
				w.Write([]byte(`null`))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/config/apps/http/metrics":
			posted = true
		case r.URL.Path == "/metrics":
			w.Write([]byte(sampleMetrics))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	got, err := c.Traffic(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !posted {
		t.Error("the edge proxy of an earlier release did not get the metrics of each host")
	}
	if got["example.com"].Out != 5500000 {
		t.Errorf("traffic = %v", got)
	}
}
