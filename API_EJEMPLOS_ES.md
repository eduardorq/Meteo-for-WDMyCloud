# Ejemplos de uso de MeteoAPI

## Estado actual

```bash
curl http://IP_DEL_NAS:8098/api/v1/current
```

## Pronóstico bruto de Open-Meteo

```bash
curl http://IP_DEL_NAS:8098/api/v1/forecast
```

## Histórico de las últimas 48 horas

```bash
curl 'http://IP_DEL_NAS:8098/api/v1/history?hours=48&limit=1000'
```

## Enviar un sensor propio

```bash
curl -X POST 'http://IP_DEL_NAS:8098/api/v1/ingest?key=CLAVE' \
  -H 'Content-Type: application/json' \
  -d '{
    "station": "esp32-exterior",
    "temperature_c": 24.1,
    "humidity_pct": 58,
    "pressure_hpa": 1017.2
  }'
```

## Ecowitt

Configure el servidor personalizado con:

- Servidor: IP del NAS
- Puerto: 8098
- Ruta: `/input/ecowitt?key=CLAVE`
- Protocolo: HTTP

La aplicación convierte automáticamente temperatura, presión, viento y lluvia
a unidades métricas cuando recibe los campos habituales de Ecowitt.
