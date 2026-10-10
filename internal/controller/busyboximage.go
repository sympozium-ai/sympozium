package controller

import "os"

// DefaultBusyboxImage is the image used for the small utility containers the
// controller injects into AgentRun pods: the wait-for-memory and
// wait-for-shared-memory init containers and the postRun done container. It
// is a Docker Hub short name, so clusters that mirror short names need a way
// to point these containers at a private registry.
const DefaultBusyboxImage = "busybox:1.36"

// busyboxImage returns the image for injected utility containers. It honors
// SYMPOZIUM_BUSYBOX_IMAGE (set by the Helm chart) so air-gapped clusters can
// override the Docker Hub short name, mirroring the SYMPOZIUM_IMAGE_REGISTRY
// pattern for the Sympozium images.
func busyboxImage() string {
	if img := os.Getenv("SYMPOZIUM_BUSYBOX_IMAGE"); img != "" {
		return img
	}
	return DefaultBusyboxImage
}
