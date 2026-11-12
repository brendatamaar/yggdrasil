//go:build ygg_pin_publish_transport

package git

// This file exists only to keep the approved publish-transport dependency in
// go.mod until the new-repo task (N) imports it from a real implementation.
// It is excluded from every normal build (the tag is never set), so the binary
// does not grow. N deletes this file when it adds its first real import.
import _ "github.com/go-git/go-git/v5"
