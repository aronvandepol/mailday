//go:build !unix

package syncd

import "os"

func lockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

func withRestrictiveUmask(fn func()) { fn() }
