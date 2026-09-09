package controller

import (
	"bytes"
	"os/exec"
	"testing"
)

func TestExternalCellnRouterKeepsClientAuthoritySeparate(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required")
	}
	settings := "celln.enabled=true,celln.router.external=true,celln.routerUrl=https://router.example:9444,celln.tokenSecret=execution,celln.capabilityTokenSecret=discovery,celln.caConfigMap=operator-ca"
	out, err := exec.Command("helm", "template", "m0", "../../charts/sympozium", "--set", settings).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	for _, unwanted := range []string{"name: celln-installer", "name: celln-router\n", "ownership-dir", "subPath: ca-bundle"} {
		if bytes.Contains(out, []byte(unwanted)) {
			t.Fatalf("external mode rendered %s", unwanted)
		}
	}
	if bytes.Count(out, []byte("name: SSL_CERT_FILE")) != 2 || bytes.Count(out, []byte("name: \"operator-ca\"")) != 2 {
		t.Fatal("both clients must mount the public CA bundle")
	}
	for _, override := range []string{"celln.tokenSecret=discovery", "celln.installer.enabled=true", "celln.capabilityTokenSecret=", "celln.routerUrl=http://router.example:9444"} {
		if out, err := exec.Command("helm", "template", "m0", "../../charts/sympozium", "--set", settings, "--set", override).CombinedOutput(); err == nil {
			t.Fatalf("unsafe external configuration accepted: %s\n%s", override, out)
		}
	}
}
