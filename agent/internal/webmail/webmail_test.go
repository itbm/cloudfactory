package webmail

import (
	"encoding/base64"
	"regexp"
	"testing"
)

// Adversarial review, finding G8-01: the edge proxy refuses the sign-in of the administrator
// of the mail server, and accepts the sign-in of a mailbox.
func TestRoutesRefuseTheAdministratorOfTheMailServer(t *testing.T) {
	re := regexp.MustCompile(Routes("mail.example.net", 19027, 19025).NoAuthorization)
	basic := func(user string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":secret"))
	}
	for header, want := range map[string]bool{
		basic("admin"): true, "basic  " + basic("admin")[6:]: true,
		basic("ann@example.net"): false, basic("administrator"): false, basic("Admin"): false, "Bearer YWRtaW46": false, "": false,
	} {
		if got := re.MatchString(header); got != want {
			t.Errorf("%q: %v, want %v", header, got, want)
		}
	}
}
