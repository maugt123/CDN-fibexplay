//go:build linux

// Package instalar deja fibex-cdn como servicio systemd en Debian.
package instalar

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
)

const (
	binario     = "/usr/local/bin/fibex-cdn"
	dirConfig   = "/etc/fibex-cdn"
	config      = dirConfig + "/fibex-cdn.env"
	unidad      = "/etc/systemd/system/fibex-cdn.service"
	usuarioSist = "fibex-cdn"
)

const servicio = `[Unit]
Description=Fibex CDN (redireccion geografica a los XUI)
After=network-online.target
Wants=network-online.target

[Service]
User=fibex-cdn
Group=fibex-cdn
ExecStart=/usr/local/bin/fibex-cdn servir --config /etc/fibex-cdn/fibex-cdn.env
Restart=always
RestartSec=2
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`

// Instalar copia el binario, crea el usuario de sistema, deja la configuración
// en /etc/fibex-cdn (sin pisar una existente) y activa el servicio.
func Instalar(origenConfig, plantilla string) error {
	if os.Geteuid() != 0 {
		return errors.New("hay que ejecutarlo como root: sudo ./fibex-cdn instalar")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("no se encontró systemctl; este instalador es para Debian/Ubuntu con systemd")
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if exe != binario {
		if err := copiar(exe, binario, 0o755); err != nil {
			return fmt.Errorf("copiando el binario: %w", err)
		}
		paso("binario copiado a " + binario)
	}

	if _, err := user.Lookup(usuarioSist); err != nil {
		if err := correr("useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", usuarioSist); err != nil {
			return err
		}
		paso("usuario de sistema " + usuarioSist + " creado")
	}

	if err := os.MkdirAll(dirConfig, 0o750); err != nil {
		return err
	}
	nuevaConfig := false
	if _, err := os.Stat(config); errors.Is(err, os.ErrNotExist) {
		origen := origenConfig
		if origen == "" {
			if cwd, err := os.Getwd(); err == nil {
				if _, err := os.Stat(filepath.Join(cwd, "fibex-cdn.env")); err == nil {
					origen = filepath.Join(cwd, "fibex-cdn.env")
				}
			}
		}
		if origen != "" {
			if err := copiar(origen, config, 0o640); err != nil {
				return err
			}
			paso("configuración copiada de " + origen + " a " + config)
		} else {
			if err := os.WriteFile(config, []byte(plantilla), 0o640); err != nil {
				return err
			}
			nuevaConfig = true
			paso("configuración de ejemplo creada en " + config)
		}
	} else {
		paso("se conserva la configuración existente " + config)
	}
	if err := correr("chown", "-R", "root:"+usuarioSist, dirConfig); err != nil {
		return err
	}
	os.Chmod(config, 0o640)

	if err := os.WriteFile(unidad, []byte(servicio), 0o644); err != nil {
		return err
	}
	if err := correr("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if nuevaConfig {
		if err := correr("systemctl", "enable", "fibex-cdn"); err != nil {
			return err
		}
		fmt.Printf("\nFalta un paso: edita %s y luego ejecuta:\n  sudo systemctl start fibex-cdn\n", config)
		return nil
	}
	if err := correr("systemctl", "enable", "fibex-cdn"); err != nil {
		return err
	}
	if err := correr("systemctl", "restart", "fibex-cdn"); err != nil {
		return err
	}
	paso("servicio fibex-cdn activo (arranca solo al reiniciar el servidor)")
	fmt.Println("\nVer estado:  fibex-cdn estado")
	fmt.Println("Ver logs:    journalctl -u fibex-cdn -f")
	return nil
}

// Desinstalar detiene y quita el servicio y el binario. La configuración se queda.
func Desinstalar() error {
	if os.Geteuid() != 0 {
		return errors.New("hay que ejecutarlo como root: sudo fibex-cdn desinstalar")
	}
	correr("systemctl", "disable", "--now", "fibex-cdn")
	os.Remove(unidad)
	correr("systemctl", "daemon-reload")
	os.Remove(binario)
	paso("servicio y binario eliminados; la configuración sigue en " + dirConfig)
	return nil
}

func paso(msg string) { fmt.Println("  ✔", msg) }

func correr(nombre string, args ...string) error {
	cmd := exec.Command(nombre, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", nombre, args, err)
	}
	return nil
}

func copiar(origen, destino string, modo os.FileMode) error {
	in, err := os.Open(origen)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := destino + ".nuevo"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, modo)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, destino)
}
