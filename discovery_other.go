//go:build !linux

package main

import "errors"

func ScanPorts() ([]PortInfo, error) {
	return nil, errors.New("port discovery requires Linux")
}
