package cellnauthority

import (
	"fmt"
	"strings"
	"testing"
)

func TestPlatformDetailIsBoundedAndPathFree(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"not a platform refusal", fmt.Errorf("open /etc/celln/registration.json"), ""},
		{"authored sentence", Refuse(ReasonPolicyContracted, "run persona differs from the runtime profile's bound persona; send the profile's systemPrompt verbatim"), "run persona differs from the runtime profile's bound persona; send the profile's systemPrompt verbatim"},
		{"wrapped refusal", fmt.Errorf("admission: %w", Refuse(ReasonToolUnknown, "cluster tool %q revision changed", "web-fetch")), `cluster tool "web-fetch" revision changed`},
		{"wrapped error cut off", Refuse(ReasonPolicyWithdrawn, "namespace unavailable: %v", fmt.Errorf("Get https://10.0.0.1:6443/api: refused")), "namespace unavailable"},
		{"path withheld", Refuse(ReasonPolicyWithdrawn, "no policy in /var/lib/secret-path"), ""},
		{"backslash withheld", Refuse(ReasonPolicyWithdrawn, `no policy in C:\secret`), ""},
		{"control character withheld", Refuse(ReasonPolicyWithdrawn, "line\nbreak"), ""},
		{"non-ASCII withheld", Refuse(ReasonPolicyWithdrawn, "pol\u00edcy"), ""},
	} {
		if got := PlatformDetail(test.err); got != test.want {
			t.Errorf("%s: PlatformDetail = %q, want %q", test.name, got, test.want)
		}
	}
	long := PlatformDetail(Refuse(ReasonLimitRange, "%s", strings.Repeat("x", 500)))
	if len(long) != platformDetailLimit || !strings.HasSuffix(long, "...") {
		t.Errorf("long detail not bounded: %d bytes", len(long))
	}
}
