MeteoAPI 1.0.0 para WD My Cloud Mirror Gen2 / My Cloud OS 5

Servicio ligero escrito en Go y compilado estáticamente para ARMv7.
Puerto web/API: 8098
Datos persistentes: /mnt/HD/HD_a2/Nas_Prog/_meteoapi
Fuente meteorológica predeterminada: Open-Meteo (sin clave API).

Endpoints principales:
  GET  /api/v1/current
  GET  /api/v1/forecast
  GET  /api/v1/air
  GET  /api/v1/history?hours=24
  POST /api/v1/ingest?key=CLAVE
  POST /input/ecowitt?key=CLAVE
  GET  /api/v1/health
  GET  /metrics
