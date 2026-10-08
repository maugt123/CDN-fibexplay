# fibex-cdn

CDN geográfico para los servidores XUI de Fibexplay. Cada abonado ve los canales
desde el XUI de su ciudad (**Valencia** o **Maracay**) y, si ese servidor se cae,
pasa automáticamente a **Caracas**.

Es **un solo archivo ejecutable**: no necesita Node, Docker, OpenResty ni nada
instalado. Es independiente del orquestador (Backend_orquestador_test).

```
                 1. pide el canal                      2. "ve a Valencia" (302)
 App / Web ───────────────────────────►  fibex-cdn  ─────────────────────────────┐
     ▲                                     │   │                                   │
     │                                     │   └─ vigila cada 5 s:                 │
     │                                     │      Caracas · Valencia · Maracay      │
     │                                     └─ sabe la ciudad de cada abonado        │
     │                                        (tabla lines del XUI de Caracas)      │
     │  3. el video viene DIRECTO del nodo, no pasa por el CDN                      │
     └──────────────────────────────  XUI Valencia / Maracay  (o Caracas si cayó) ◄┘
```

- El CDN solo responde "a dónde ir" (redirección HTTP 302). El video **no** pasa
  por este servidor, así que gasta muy poco ancho de banda.
- Las demás peticiones (`player_api.php`, EPG, `get.php`...) se reenvían a Caracas.
- Si un nodo falla 3 chequeos seguidos (~15 s) sus abonados van a Caracas.
- Cuando vuelve, sus abonados regresan por tramos (25 → 50 → 75 → 100 %) en 9 min,
  para no saturarlo de golpe.

---

## Instalación en Debian (3 pasos)

Se necesita: Debian 11/12 (o Ubuntu) con systemd, usuario con `sudo`, puerto 80
libre y salida a internet hacia los XUI (puerto 80) y al MySQL de Caracas (3306).

**1. Bajar el ejecutable** en el servidor (sección *Releases* de este repositorio):

```bash
cd ~
wget -O fibex-cdn https://github.com/maugt123/CDN-fibexplay/releases/latest/download/fibex-cdn-linux-amd64
chmod +x fibex-cdn
```

Si el servidor es ARM, usar `fibex-cdn-linux-arm64`. Si no hay Release
publicado, se compila con [`compilar.sh`](#desarrollo).

**2. Poner la configuración** en `~/fibex-cdn.env`. Fibex la entrega por un canal
privado con las IPs reales de los nodos (no están en este repositorio). Solo falta
completar:

| Clave | Qué poner |
|---|---|
| `RUTAS_MYSQL_USUARIO` / `RUTAS_MYSQL_CLAVE` | usuario MySQL con solo `SELECT` sobre `xui.lines` |
| `CDN_HOST_PUBLICO` | nombre o IP pública de este servidor, ej. `cdn.fibexplay.tv` |

Para armarla desde cero: `./fibex-cdn plantilla > fibex-cdn.env` y reemplazar los
`IP-...`. Ver [Configuración](#configuración) para el resto.

**3. Revisar e instalar:**

```bash
./fibex-cdn diagnostico
sudo ./fibex-cdn instalar
```

`diagnostico` comprueba que se llega a los 3 nodos y a MySQL. `instalar` deja el
CDN como servicio: arranca solo al encender el servidor y se reinicia solo si falla.
No hace falta reiniciar el servidor ni los XUI.

Comprobar:

```bash
fibex-cdn estado
journalctl -u fibex-cdn -f
```

Desde otra máquina: `curl -I http://IP-DEL-SERVIDOR/live/USUARIO/CLAVE/ID.m3u8`
debe responder `302` con `Location:` apuntando al nodo de la ciudad del abonado.

Para cambiar la configuración: editar `/etc/fibex-cdn/fibex-cdn.env` y
`sudo systemctl restart fibex-cdn`.

Usuario MySQL de solo lectura (lo crea un administrador del XUI de Caracas):

```sql
CREATE USER 'fibex_cdn'@'IP-DEL-SERVIDOR-CDN' IDENTIFIED BY 'una-clave-larga';
GRANT SELECT ON xui.`lines` TO 'fibex_cdn'@'IP-DEL-SERVIDOR-CDN';
```

### Probar en Windows

Copiar `fibex-cdn-windows-amd64.exe` a una carpeta y darle doble clic. La primera
vez crea `fibex-cdn.env` al lado; se edita y se vuelve a abrir.

---

## ¿Hay que hacer algo en las apps?

**Durante la prueba (sin tocar producción):** la app debe pedir los canales al CDN
en lugar de a `ccs.fibexplay.tv`. En Android: una versión de prueba con el
servidor apuntando a la IP/nombre del CDN. Con `CDN_HOST_PUBLICO` lleno, el CDN
corrige `server_info` de `player_api.php` para que la app siga pidiendo los
canales al CDN.

**Paso a producción:** basta con apuntar el DNS de `ccs.fibexplay.tv` a la IP del
servidor del CDN. Las apps **no se actualizan ni se reinstalan**. Quien esté viendo
un canal en ese momento sigue en Caracas hasta que cambie de canal o reabra la app.
Antes de hacerlo:

1. Bajar el TTL del registro DNS (p. ej. a 300 s) un día antes.
2. Confirmar que `NODO_CARACAS_URL` usa la **IP** de Caracas, no `ccs.fibexplay.tv`
   (si no, el CDN se redirigiría a sí mismo; lo detecta y responde 508).
3. Para volver atrás: apuntar el DNS de nuevo a la IP de Caracas.

---

## Comandos

| Comando | Qué hace |
|---|---|
| `fibex-cdn` | arranca el CDN (doble clic en Windows) |
| `fibex-cdn diagnostico` | revisa configuración, nodos, fuente de rutas y puerto |
| `fibex-cdn estado` | estado del CDN en marcha (nodos, redirecciones, rutas) |
| `sudo fibex-cdn instalar` | instala o actualiza el servicio systemd |
| `sudo fibex-cdn desinstalar` | quita servicio y binario, conserva `/etc/fibex-cdn` |
| `fibex-cdn plantilla` | imprime una configuración de ejemplo |

Para **actualizar**: copiar el ejecutable nuevo y volver a correr
`sudo ./fibex-cdn instalar` (conserva la configuración).

### Direcciones HTTP del CDN

| Ruta | Para qué |
|---|---|
| `/cdn/salud` | responde `ok` (para balanceadores o monitoreo) |
| `/cdn/estado` | JSON con estado de nodos, redirecciones y rutas cargadas |
| `/cdn/ruta/<usuario>` | a qué nodo va un abonado; solo con `CDN_TOKEN_ADMIN` y cabecera `X-Token-Admin` |

---

## Configuración

Todas las claves están explicadas en [`fibex-cdn.env.ejemplo`](fibex-cdn.env.ejemplo).
Cualquier clave también puede pasarse como variable de entorno.

**Fuente de rutas** (`RUTAS_FUENTE`), es decir, de dónde sale la ciudad de cada abonado:

| Valor | Cómo funciona | Necesita |
|---|---|---|
| `mysql` | carga la tabla `lines` de XUI Caracas en memoria (~720 mil abonados en ~1 s), busca nuevos cada minuto y relee todo cada hora | usuario MySQL de solo lectura y acceso al puerto 3306 |
| `redis` | consulta `proxy:route:<usuario>` en el Redis del orquestador | acceso al Redis del orquestador |
| `archivo` | CSV `usuario,zona`, se relee al cambiar | un archivo exportado |
| `ninguna` | todos van a `ZONA_POR_DEFECTO` | nada (solo para pruebas) |

La ciudad se decide con la **misma regla que el orquestador**: primera letra de
`admin_notes` (`C` Caracas, `V` Valencia, `A`/`M` Maracay) y, si no hay, el texto
de `reseller_notes` (CARACAS, VALENCIA, ARAGUA, MARACAY, MARGARITA). Por defecto, Caracas.

Si la fuente de rutas no responde, el CDN **sigue funcionando** con lo que tenía en
memoria; si arranca sin rutas, manda a todos a la zona por defecto hasta que la
fuente vuelva.

**Salud de los nodos**: `SALUD_MODO=tcp` (por defecto) solo comprueba que el puerto
acepte conexiones. `SALUD_MODO=http` pide `SALUD_RUTA` y exige respuesta < 500,
lo que detecta un XUI colgado; conviene usarlo cuando se conozca una URL que los
XUI respondan rápido.

---

## Desarrollo

Compilar (no hace falta Go instalado, usa Docker):

```powershell
.\compilar.ps1 0.1.0
```

Deja en `dist/` los ejecutables para Linux amd64/arm64 y Windows, más `SHA256SUMS`.
Con Go 1.26+ instalado también sirve `VERSION=0.1.0 ./compilar.sh`.

Laboratorio local completo (MySQL con 720 mil abonados, 3 nodos simulados y el CDN
en Debian):

```powershell
.\compilar.ps1
docker compose -f pruebas/docker-compose.yml -p fibexcdn-lab up -d
curl.exe -sI http://localhost:8088/live/20000001/clave/586.m3u8
docker stop fibexcdn-lab-valencia-1     # a los ~6 s Valencia pasa a Caracas
docker compose -f pruebas/docker-compose.yml -p fibexcdn-lab down
```

Estructura:

```
cmd/fibex-cdn/       punto de entrada y comandos
internal/config/     lectura y validación de fibex-cdn.env
internal/zonas/      regla de ciudad (copia de getServerByNotes del orquestador)
internal/rutas/      fuentes de rutas: mysql, redis, archivo
internal/nodos/      vigilancia de nodos, caída, rampa de retorno
internal/servidor/   redirección 302, reenvío a Caracas, /cdn/*
internal/instalar/   servicio systemd
pruebas/             laboratorio con docker compose
```
