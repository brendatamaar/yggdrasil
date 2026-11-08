//go:build !windows && !linux && !darwin

package platform

import "errors"

// processStart is not supported here, so orphans cannot be tracked safely
// (without a start time a reused pid could be killed by mistake).
func processStart(int) (string, error) {
	return "", errors.New("platform: orphan tracking is not supported on this OS")
}
