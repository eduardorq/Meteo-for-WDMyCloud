# MeteoAPI 1.0.1 para WD My Cloud Mirror Gen2

Aplicación ligera y autocontenida para My Cloud OS 5. Está escrita en Go,
compilada estáticamente para ARMv7 y empaquetada con `mksapkg-OS5` para el
modelo `WDMyCloudMirror`.

## Construcción

```bash
cd src
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -trimpath -ldflags "-s -w -X main.version=1.0.1" \
  -o ../meteoapi/bin/meteoapi

cd ../meteoapi
OPENSSL_CONF=../openssl-legacy.cnf \
  ../mksapkg-OS5 -E -s -m WDMyCloudMirror
```

## Funcionamiento

- Panel y API: `http://IP_DEL_NAS:8098/`
- Datos persistentes: `/mnt/HD/HD_a2/Nas_Prog/_meteoapi`
- Configuración: `_meteoapi/config.json`
- Históricos: `_meteoapi/history/*.jsonl`
- Registros: `_meteoapi/meteoapi.log` y `_meteoapi/launcher.log`
- Fuente externa predeterminada: Open-Meteo.
- Ubicación inicial: Las Palmas de Gran Canaria; puede cambiarse en el panel.

## API

- `GET /api/v1/current`
- `GET /api/v1/forecast`
- `GET /api/v1/air`
- `GET /api/v1/local/current`
- `GET /api/v1/history?hours=24&kind=remote|local`
- `POST /api/v1/refresh`
- `POST /api/v1/ingest?key=CLAVE`
- `POST /input/ecowitt?key=CLAVE`
- `GET /api/v1/health`
- `GET /metrics`

Ejemplo de entrada JSON:

```bash
curl -X POST 'http://IP_DEL_NAS:8098/api/v1/ingest?key=CLAVE' \
  -H 'Content-Type: application/json' \
  -d '{"station":"terraza","temperature_c":23.4,"humidity_pct":61}'
```

La clave se genera al primer arranque y se muestra en Configuración.

## Cambio 1.0.1

Corrige la alineación del contador atómico HTTP en ARM de 32 bits. La versión
1.0.0 podía aceptar la conexión TCP y cerrarla al procesar la primera petición,
lo que el navegador mostraba como `ERR_EMPTY_RESPONSE`.
