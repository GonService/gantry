//go:build !darwin || !cgo

package cvm

import "errors"

func watchPaths(paths []string, fn func(path string, flags uint32)) (func(), error) {
	return nil, errors.New("os avisos de alteração só existem no app do Mac")
}
