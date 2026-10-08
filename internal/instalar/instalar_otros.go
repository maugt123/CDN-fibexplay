//go:build !linux

package instalar

import "errors"

var errSoloLinux = errors.New("instalar como servicio solo está disponible en Linux (Debian); en Windows basta con ejecutar fibex-cdn.exe")

func Instalar(string, string) error { return errSoloLinux }
func Desinstalar() error            { return errSoloLinux }
