// Package etcdcompat defines the bounded etcd versions accepted by backup and
// read-only checks. Server and storage versions are independent during upgrades.
package etcdcompat

import "regexp"

var serverVersion = regexp.MustCompile(`^3\.(6|7)\.(0|[1-9][0-9]*)$`)

// Server accepts stable 3.6.x and 3.7.x releases, not prereleases or unknown lines.
func Server(version string) bool { return serverVersion.MatchString(version) }

// Storage accepts only the known snapshot storage formats, not server patches.
func Storage(version string) bool { return version == "3.6.0" || version == "3.7.0" }
