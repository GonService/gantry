//go:build !linux

package agent

import "errors"

const watchSupported = false

func watchFiles([]string, chan<- string) (func(), error) {
	return nil, errors.New("acompanhar arquivos só funciona no Linux")
}
