# MeteoAPI 1.0.1 for WD My Cloud Mirror Gen2

A lightweight, self-contained application for My Cloud OS 5. It is written in Go,
statically compiled for ARMv7, and packaged with `mksapkg-OS5` for the
`WDMyCloudMirror` model.

## Build

```bash
cd src
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -trimpath -ldflags "-s -w -X main.version=1.0.1" \
  -o ../meteoapi/bin/meteoapi

cd ../meteoapi
OPENSSL_CONF=../openssl-legacy.cnf \
  ../mksapkg-OS5 -E -s -m WDMyCloudMirror
```

## Operation

* Web panel and API: `http://NAS_IP:8098/`
* Persistent data: `/mnt/HD/HD_a2/Nas_Prog/_meteoapi`
* Configuration: `_meteoapi/config.json`
* History files: `_meteoapi/history/*.jsonl`
* Logs: `_meteoapi/meteoapi.log` and `_meteoapi/launcher.log`
* Default external data source: Open-Meteo.
* Default location: Las Palmas de Gran Canaria; it can be changed from the web panel.

## API

* `GET /api/v1/current`
* `GET /api/v1/forecast`
* `GET /api/v1/air`
* `GET /api/v1/local/current`
* `GET /api/v1/history?hours=24&kind=remote|local`
* `POST /api/v1/refresh`
* `POST /api/v1/ingest?key=KEY`
* `POST /input/ecowitt?key=KEY`
* `GET /api/v1/health`
* `GET /metrics`

Example JSON input:

```bash
curl -X POST 'http://NAS_IP:8098/api/v1/ingest?key=KEY' \
  -H 'Content-Type: application/json' \
  -d '{"station":"terrace","temperature_c":23.4,"humidity_pct":61}'
```

The key is generated on first startup and displayed in the Configuration section.

## Changes in 1.0.1

Fixes the alignment of the HTTP atomic counter on 32-bit ARM systems. Version
1.0.0 could accept the TCP connection and then close it while processing the
first request, which appeared in the browser as `ERR_EMPTY_RESPONSE`.
::: 
