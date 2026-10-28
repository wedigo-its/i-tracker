// env.go
// Detection of runtimes in which the binary must not replace itself
// Version: 2026.10.28

package selfupdate

import "os"

// ContainerEnv is set to "1" in the official container image. A container is
// updated by pulling a new image, never by replacing the binary inside it.
const ContainerEnv = "IPERF3_CONTAINER"

// InContainer reports whether the process runs inside a container.
func InContainer() bool {
	if os.Getenv(ContainerEnv) == "1" {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	_, err := os.Stat("/run/.containerenv") // podman
	return err == nil
}
