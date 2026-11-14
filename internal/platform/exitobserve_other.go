//go:build !windows && !linux && !darwin

package platform

import "errors"

func newExitObserver() (exitObserver, error) {
	return nil, errors.New("safe process exit observation is unsupported on this OS")
}
