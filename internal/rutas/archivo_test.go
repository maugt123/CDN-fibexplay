package rutas

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFuenteArchivo(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "rutas.csv")
	os.WriteFile(ruta, []byte("\xef\xbb\xbfusuario,zona\n12345678,Valencia\n# comentario\n\n87654321;10.0.0.83\n55555555\tmaracay\n"), 0o600)

	f := NuevaArchivo(ruta, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := f.Iniciar(context.Background()); err != nil {
		t.Fatal(err)
	}
	for usuario, quiere := range map[string]string{"12345678": "valencia", "87654321": "10.0.0.83", "55555555": "maracay"} {
		if z, ok := f.Zona(context.Background(), usuario); !ok || z != quiere {
			t.Errorf("%s -> %q,%v; se esperaba %q", usuario, z, ok, quiere)
		}
	}
	if _, ok := f.Zona(context.Background(), "usuario"); ok {
		t.Error("la cabecera no debe tomarse como abonado")
	}
	if e := f.Estado(); e.Usuarios != 3 || e.UltimoError != "" {
		t.Errorf("estado = %+v", e)
	}

	// Cambia el archivo: se relee.
	os.WriteFile(ruta, []byte("12345678,maracay\n"), 0o600)
	futuro := time.Now().Add(time.Minute)
	os.Chtimes(ruta, futuro, futuro)
	if err := f.cargarSiCambio(); err != nil {
		t.Fatal(err)
	}
	if z, _ := f.Zona(context.Background(), "12345678"); z != "maracay" {
		t.Errorf("tras recargar: %q", z)
	}

	// Un archivo roto no borra las rutas que ya había.
	os.WriteFile(ruta, []byte("solo-un-campo\n"), 0o600)
	futuro = futuro.Add(time.Minute)
	os.Chtimes(ruta, futuro, futuro)
	if err := f.cargarSiCambio(); err == nil {
		t.Fatal("se esperaba error")
	}
	if z, _ := f.Zona(context.Background(), "12345678"); z != "maracay" {
		t.Error("un archivo inválido no debe borrar las rutas cargadas")
	}
}
