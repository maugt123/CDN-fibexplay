package rutas

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// FuenteArchivo lee un CSV "usuario,zona" y lo relee cuando cambia.
type FuenteArchivo struct {
	tabla
	ruta     string
	refresco time.Duration
	log      *slog.Logger
	modTime  time.Time
}

func NuevaArchivo(ruta string, refresco time.Duration, log *slog.Logger) *FuenteArchivo {
	return &FuenteArchivo{ruta: ruta, refresco: refresco, log: log}
}

func (f *FuenteArchivo) Zona(_ context.Context, usuario string) (string, bool) {
	return f.buscar(usuario)
}

func (f *FuenteArchivo) Estado() EstadoFuente { return f.estado("archivo") }

func (f *FuenteArchivo) Iniciar(ctx context.Context) error {
	err := f.cargarSiCambio()
	go func() {
		t := time.NewTicker(f.refresco)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				f.cargarSiCambio()
			}
		}
	}()
	return err
}

func (f *FuenteArchivo) cargarSiCambio() error {
	info, err := os.Stat(f.ruta)
	if err != nil {
		f.cargaFallida(err)
		f.log.Error("no se pudo leer el archivo de rutas", "archivo", f.ruta, "error", err)
		return err
	}
	if info.ModTime().Equal(f.modTime) {
		return nil
	}
	nuevas, err := LeerCSV(f.ruta)
	if err != nil {
		f.cargaFallida(err)
		f.log.Error("archivo de rutas inválido", "archivo", f.ruta, "error", err)
		return err
	}
	f.reemplazar(nuevas)
	f.modTime = info.ModTime()
	f.cargaOK()
	f.log.Info("rutas cargadas desde archivo", "archivo", f.ruta, "abonados", len(nuevas))
	return nil
}

// LeerCSV acepta separador coma, punto y coma o tabulador. Ignora líneas
// vacías, comentarios (#) y una cabecera que empiece por "usuario" o "username".
func LeerCSV(ruta string) (map[string]string, error) {
	a, err := os.Open(ruta)
	if err != nil {
		return nil, err
	}
	defer a.Close()

	res := map[string]string{}
	in := internador{}
	sc := bufio.NewScanner(a)
	for n := 1; sc.Scan(); n++ {
		linea := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\xef\xbb\xbf"))
		if linea == "" || strings.HasPrefix(linea, "#") {
			continue
		}
		campos := strings.FieldsFunc(linea, func(r rune) bool { return r == ',' || r == ';' || r == '\t' })
		if len(campos) < 2 {
			return nil, fmt.Errorf("línea %d: se esperaba \"usuario,zona\"", n)
		}
		usuario := strings.TrimSpace(campos[0])
		zona := strings.ToLower(strings.TrimSpace(campos[1]))
		if n == 1 && (strings.EqualFold(usuario, "usuario") || strings.EqualFold(usuario, "username")) {
			continue
		}
		res[usuario] = in.de(zona)
	}
	return res, sc.Err()
}
