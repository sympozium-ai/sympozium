package main

import "strings"

// The default Celln starter package this build of the installer pins, set by
// the release workflow with -ldflags "-X main.cellnStarterImage=… " from the
// identity hack/build-celln-starter.sh produced. A development build carries
// none and keeps the explicit --celln-fleet-* inputs.
var (
	cellnStarterImage        string
	cellnStarterHash         string
	cellnStarterPublisher    string
	cellnStarterCellnVersion string
)

// cellnStarter identifies a published starter package exactly: the
// digest-pinned image carrying it, the package.json hash the nodes admit, and
// the publisher key the fleet trusts. A default exists only when all three do.
type cellnStarter struct {
	Image, PackageHash, Publisher, CellnVersion string
}

func (s cellnStarter) complete() bool {
	return s.Image != "" && s.PackageHash != "" && s.Publisher != "" &&
		!strings.ContainsAny(s.Image+s.PackageHash+s.Publisher, " \t\r\n,=")
}

func defaultCellnStarter() cellnStarter {
	return cellnStarter{Image: cellnStarterImage, PackageHash: cellnStarterHash, Publisher: cellnStarterPublisher, CellnVersion: cellnStarterCellnVersion}
}

// cellnStarterLDFlags renders the linker flags that pin a package into a
// build, for the release workflow and the journeys to share one spelling.
func cellnStarterLDFlags(s cellnStarter) string {
	return "-X main.cellnStarterImage=" + s.Image + " -X main.cellnStarterHash=" + s.PackageHash +
		" -X main.cellnStarterPublisher=" + s.Publisher + " -X main.cellnStarterCellnVersion=" + s.CellnVersion
}
