package rutas

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"

	"fibex-cdn/internal/config"
	"fibex-cdn/internal/zonas"
)

const loteMySQL = 10000

// FuenteMySQL copia en memoria usuario→zona desde la tabla lines del XUI.
// Cada RUTAS_REFRESCO trae solo los ids nuevos y cada RUTAS_RECARGA_COMPLETA
// relee todo (así se notan cambios de notas en abonados existentes).
type FuenteMySQL struct {
	tabla
	db              *sql.DB
	consulta        string
	refresco        time.Duration
	recargaCompleta time.Duration
	log             *slog.Logger
	maxID           int64
}

func NuevaMySQL(c config.MySQL, refresco, recargaCompleta time.Duration, log *slog.Logger) (*FuenteMySQL, error) {
	mc := mysql.NewConfig()
	mc.User = c.Usuario
	mc.Passwd = c.Clave
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(c.Host, strconv.Itoa(c.Puerto))
	mc.DBName = c.BD
	mc.Timeout = 5 * time.Second
	mc.ReadTimeout = 2 * time.Minute
	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return &FuenteMySQL{
		db: db,
		// El nombre de la tabla ya se validó (solo letras, números y _).
		consulta: fmt.Sprintf("SELECT id, username, LEFT(COALESCE(admin_notes,''),1), COALESCE(reseller_notes,'') "+
			"FROM `%s` WHERE id > ? ORDER BY id LIMIT ?", c.Tabla),
		refresco:        refresco,
		recargaCompleta: recargaCompleta,
		log:             log,
	}, nil
}

func (f *FuenteMySQL) Zona(_ context.Context, usuario string) (string, bool) {
	return f.buscar(usuario)
}

func (f *FuenteMySQL) Estado() EstadoFuente { return f.estado("mysql") }

func (f *FuenteMySQL) Iniciar(ctx context.Context) error {
	err := f.recargaTotal(ctx)
	go f.bucle(ctx, err != nil)
	return err
}

func (f *FuenteMySQL) bucle(ctx context.Context, pendienteTotal bool) {
	t := time.NewTicker(f.refresco)
	defer t.Stop()
	ultimaTotal := time.Now()
	for {
		select {
		case <-ctx.Done():
			f.db.Close()
			return
		case <-t.C:
		}
		if pendienteTotal || time.Since(ultimaTotal) >= f.recargaCompleta {
			if err := f.recargaTotal(ctx); err != nil {
				pendienteTotal = true
				continue
			}
			pendienteTotal = false
			ultimaTotal = time.Now()
			continue
		}
		f.recargaNuevos(ctx)
	}
}

func (f *FuenteMySQL) recargaTotal(ctx context.Context) error {
	inicio := time.Now()
	nuevas := make(map[string]string, f.total()+1024)
	maxID, err := f.leerDesde(ctx, 0, nuevas)
	if err != nil {
		f.cargaFallida(err)
		f.log.Error("no se pudieron leer las rutas de MySQL", "error", err)
		return err
	}
	f.reemplazar(nuevas)
	f.maxID = maxID
	f.cargaOK()
	f.log.Info("rutas cargadas desde MySQL", "abonados", len(nuevas), "duracion", time.Since(inicio).Round(time.Millisecond))
	return nil
}

func (f *FuenteMySQL) recargaNuevos(ctx context.Context) {
	nuevas := map[string]string{}
	maxID, err := f.leerDesde(ctx, f.maxID, nuevas)
	if err != nil {
		f.cargaFallida(err)
		f.log.Warn("no se pudieron leer abonados nuevos de MySQL", "error", err)
		return
	}
	f.agregar(nuevas)
	f.maxID = maxID
	f.cargaOK()
	if len(nuevas) > 0 {
		f.log.Info("abonados nuevos agregados", "cantidad", len(nuevas))
	}
}

func (f *FuenteMySQL) leerDesde(ctx context.Context, desde int64, destino map[string]string) (int64, error) {
	maxID := desde
	for {
		filas, err := f.db.QueryContext(ctx, f.consulta, maxID, loteMySQL)
		if err != nil {
			return desde, err
		}
		leidas := 0
		for filas.Next() {
			var id int64
			var usuario, admin, revendedor sql.NullString
			if err := filas.Scan(&id, &usuario, &admin, &revendedor); err != nil {
				filas.Close()
				return desde, err
			}
			leidas++
			maxID = id
			if usuario.Valid && usuario.String != "" {
				destino[usuario.String] = zonas.Clasificar(admin.String, revendedor.String)
			}
		}
		err = filas.Err()
		filas.Close()
		if err != nil {
			return desde, err
		}
		if leidas < loteMySQL {
			return maxID, nil
		}
	}
}
