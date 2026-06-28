package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var version = "dev"

const (
	appName       = "MeteoAPI"
	defaultListen = "0.0.0.0:8098"
	maxBodySize   = 128 << 10
)

type Config struct {
	LocationName      string  `json:"location_name"`
	Latitude          float64 `json:"latitude"`
	Longitude         float64 `json:"longitude"`
	Timezone          string  `json:"timezone"`
	PollMinutes       int     `json:"poll_minutes"`
	RetentionDays     int     `json:"retention_days"`
	WeatherEnabled    bool    `json:"weather_enabled"`
	AirQualityEnabled bool    `json:"air_quality_enabled"`
	APIKey            string  `json:"api_key"`
	RequireIngestKey  bool    `json:"require_ingest_key"`
}

type Cache struct {
	Forecast          map[string]any
	Air               map[string]any
	Local             map[string]any
	ForecastFetchedAt time.Time
	AirFetchedAt      time.Time
	LocalReceivedAt   time.Time
	LastError         string
	LastAttempt       time.Time
}

type App struct {
	// atomic.Uint64 carries the alignment guarantee required on 32-bit ARM.
	requests atomic.Uint64

	dataDir string
	listen  string
	logger  *log.Logger
	client  *http.Client

	mu    sync.RWMutex
	cfg   Config
	cache Cache

	refreshCh chan struct{}
	startedAt time.Time
}

func main() {
	dataDir := flag.String("data-dir", "./data", "Directorio persistente de datos")
	listen := flag.String("listen", defaultListen, "Dirección HTTP de escucha")
	showVersion := flag.Bool("version", false, "Mostrar versión")
	flag.Parse()

	if *showVersion {
		fmt.Printf("%s %s\n", appName, version)
		return
	}

	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		log.Fatalf("no se puede crear el directorio de datos: %v", err)
	}
	logFile, err := os.OpenFile(filepath.Join(*dataDir, "meteoapi.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatalf("no se puede abrir el registro: %v", err)
	}
	defer logFile.Close()
	logger := log.New(io.MultiWriter(os.Stdout, logFile), "", log.LstdFlags|log.LUTC)

	app := &App{
		dataDir:   *dataDir,
		listen:    *listen,
		logger:    logger,
		client:    &http.Client{Timeout: 25 * time.Second},
		refreshCh: make(chan struct{}, 1),
		startedAt: time.Now().UTC(),
	}
	if err := app.init(); err != nil {
		logger.Fatalf("error de inicialización: %v", err)
	}

	mux := http.NewServeMux()
	app.routes(mux)
	srv := &http.Server{
		Addr:              app.listen,
		Handler:           app.middleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	go app.pollLoop()
	go func() {
		logger.Printf("%s %s escuchando en http://%s", appName, version, app.listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("servidor HTTP: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	logger.Printf("deteniendo %s", appName)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func defaultConfig() Config {
	return Config{
		LocationName:      "Las Palmas de Gran Canaria",
		Latitude:          28.1235,
		Longitude:         -15.4363,
		Timezone:          "Atlantic/Canary",
		PollMinutes:       10,
		RetentionDays:     30,
		WeatherEnabled:    true,
		AirQualityEnabled: true,
		APIKey:            randomToken(24),
		RequireIngestKey:  true,
	}
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("meteo-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (a *App) init() error {
	for _, d := range []string{"cache", "history", "local"} {
		if err := os.MkdirAll(filepath.Join(a.dataDir, d), 0700); err != nil {
			return err
		}
	}

	cfgPath := filepath.Join(a.dataDir, "config.json")
	cfg := defaultConfig()
	if b, err := os.ReadFile(cfgPath); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("config.json no válido: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if err := writeJSONAtomic(cfgPath, cfg, 0600); err != nil {
		return err
	}
	normalizeConfig(&cfg)
	a.cfg = cfg

	a.loadCacheFile("forecast.json", func(m map[string]any, t time.Time) {
		a.cache.Forecast, a.cache.ForecastFetchedAt = m, t
	})
	a.loadCacheFile("air.json", func(m map[string]any, t time.Time) {
		a.cache.Air, a.cache.AirFetchedAt = m, t
	})
	localPath := filepath.Join(a.dataDir, "local", "current.json")
	if b, err := os.ReadFile(localPath); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			a.cache.Local = m
			if st, err := os.Stat(localPath); err == nil {
				a.cache.LocalReceivedAt = st.ModTime().UTC()
			}
		}
	}
	return nil
}

func normalizeConfig(c *Config) {
	if c.LocationName == "" {
		c.LocationName = "Ubicación"
	}
	if c.Timezone == "" {
		c.Timezone = "auto"
	}
	if c.PollMinutes < 5 {
		c.PollMinutes = 10
	}
	if c.PollMinutes > 1440 {
		c.PollMinutes = 1440
	}
	if c.RetentionDays < 1 {
		c.RetentionDays = 30
	}
	if c.RetentionDays > 3650 {
		c.RetentionDays = 3650
	}
	if c.APIKey == "" {
		c.APIKey = randomToken(24)
	}
}

func (a *App) loadCacheFile(name string, setter func(map[string]any, time.Time)) {
	p := filepath.Join(a.dataDir, "cache", name)
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return
	}
	st, _ := os.Stat(p)
	t := time.Time{}
	if st != nil {
		t = st.ModTime().UTC()
	}
	setter(m, t)
}

func (a *App) routes(mux *http.ServeMux) {
	mux.HandleFunc("/", a.handleDashboard)
	mux.HandleFunc("/api/v1/health", a.handleHealth)
	mux.HandleFunc("/api/v1/current", a.handleCurrent)
	mux.HandleFunc("/api/v1/forecast", a.handleForecast)
	mux.HandleFunc("/api/v1/air", a.handleAir)
	mux.HandleFunc("/api/v1/local/current", a.handleLocalCurrent)
	mux.HandleFunc("/api/v1/history", a.handleHistory)
	mux.HandleFunc("/api/v1/config", a.handleConfig)
	mux.HandleFunc("/api/v1/refresh", a.handleRefresh)
	mux.HandleFunc("/api/v1/ingest", a.handleIngest)
	mux.HandleFunc("/input/ecowitt", a.handleEcowitt)
	mux.HandleFunc("/metrics", a.handleMetrics)
}

func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Printf("panic atendiendo %s %s: %v\n%s", r.Method, r.URL.Path, recovered, debug.Stack())
				http.Error(w, "error interno de MeteoAPI", http.StatusInternalServerError)
			}
		}()
		a.requests.Add(1)
		w.Header().Set("Server", appName+"/"+version)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) pollLoop() {
	time.Sleep(2 * time.Second)
	a.refreshAll()
	for {
		a.mu.RLock()
		minutes := a.cfg.PollMinutes
		a.mu.RUnlock()
		timer := time.NewTimer(time.Duration(minutes) * time.Minute)
		select {
		case <-timer.C:
		case <-a.refreshCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		a.refreshAll()
	}
}

func (a *App) triggerRefresh() {
	select {
	case a.refreshCh <- struct{}{}:
	default:
	}
}

func (a *App) refreshAll() {
	a.mu.Lock()
	a.cache.LastAttempt = time.Now().UTC()
	cfg := a.cfg
	a.mu.Unlock()

	var errs []string
	if cfg.WeatherEnabled {
		forecast, err := a.fetchForecast(cfg)
		if err != nil {
			errs = append(errs, "meteorología: "+err.Error())
		} else {
			now := time.Now().UTC()
			a.mu.Lock()
			a.cache.Forecast = forecast
			a.cache.ForecastFetchedAt = now
			a.mu.Unlock()
			_ = writeJSONAtomic(filepath.Join(a.dataDir, "cache", "forecast.json"), forecast, 0600)
		}
	}
	if cfg.AirQualityEnabled {
		air, err := a.fetchAir(cfg)
		if err != nil {
			errs = append(errs, "calidad del aire: "+err.Error())
		} else {
			now := time.Now().UTC()
			a.mu.Lock()
			a.cache.Air = air
			a.cache.AirFetchedAt = now
			a.mu.Unlock()
			_ = writeJSONAtomic(filepath.Join(a.dataDir, "cache", "air.json"), air, 0600)
		}
	}

	a.mu.Lock()
	a.cache.LastError = strings.Join(errs, "; ")
	a.mu.Unlock()

	if cfg.WeatherEnabled {
		_ = a.appendHistory("remote", a.currentPayload())
	}
	a.cleanupHistory(cfg.RetentionDays)
	if len(errs) > 0 {
		a.logger.Printf("actualización incompleta: %s", strings.Join(errs, "; "))
	} else {
		a.logger.Printf("datos meteorológicos actualizados para %s", cfg.LocationName)
	}
}

func (a *App) fetchForecast(cfg Config) (map[string]any, error) {
	u, _ := url.Parse("https://api.open-meteo.com/v1/forecast")
	q := u.Query()
	q.Set("latitude", strconv.FormatFloat(cfg.Latitude, 'f', 6, 64))
	q.Set("longitude", strconv.FormatFloat(cfg.Longitude, 'f', 6, 64))
	q.Set("current", "temperature_2m,relative_humidity_2m,apparent_temperature,precipitation,rain,weather_code,cloud_cover,pressure_msl,surface_pressure,wind_speed_10m,wind_direction_10m,wind_gusts_10m")
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,rain_sum,wind_speed_10m_max,wind_gusts_10m_max,wind_direction_10m_dominant,sunrise,sunset")
	q.Set("temperature_unit", "celsius")
	q.Set("wind_speed_unit", "kmh")
	q.Set("precipitation_unit", "mm")
	q.Set("timezone", cfg.Timezone)
	q.Set("forecast_days", "7")
	u.RawQuery = q.Encode()
	return a.fetchJSON(u.String())
}

func (a *App) fetchAir(cfg Config) (map[string]any, error) {
	u, _ := url.Parse("https://air-quality-api.open-meteo.com/v1/air-quality")
	q := u.Query()
	q.Set("latitude", strconv.FormatFloat(cfg.Latitude, 'f', 6, 64))
	q.Set("longitude", strconv.FormatFloat(cfg.Longitude, 'f', 6, 64))
	q.Set("current", "pm10,pm2_5,carbon_monoxide,nitrogen_dioxide,sulphur_dioxide,ozone,european_aqi,uv_index")
	q.Set("timezone", cfg.Timezone)
	q.Set("forecast_days", "3")
	u.RawQuery = q.Encode()
	return a.fetchJSON(u.String())
}

func (a *App) fetchJSON(endpoint string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", appName+"/"+version+" (WD My Cloud)")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out map[string]any
	dec := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) currentPayload() map[string]any {
	a.mu.RLock()
	cfg := a.cfg
	cache := a.cache
	a.mu.RUnlock()

	weather := childMap(cache.Forecast, "current")
	air := childMap(cache.Air, "current")
	if weather != nil {
		if code, ok := number(weather["weather_code"]); ok {
			weather = cloneMap(weather)
			weather["weather_description"] = weatherDescription(int(code))
		}
	}
	staleAfter := time.Duration(maxInt(30, cfg.PollMinutes*3)) * time.Minute
	stale := cache.ForecastFetchedAt.IsZero() || time.Since(cache.ForecastFetchedAt) > staleAfter

	return map[string]any{
		"service": appName,
		"version": version,
		"location": map[string]any{
			"name":      cfg.LocationName,
			"latitude":  cfg.Latitude,
			"longitude": cfg.Longitude,
			"timezone":  cfg.Timezone,
		},
		"source":            "Open-Meteo",
		"updated_at":        timeOrNil(cache.ForecastFetchedAt),
		"air_updated_at":    timeOrNil(cache.AirFetchedAt),
		"stale":             stale,
		"weather":           weather,
		"air_quality":       air,
		"local_sensor":      cache.Local,
		"local_received_at": timeOrNil(cache.LocalReceivedAt),
		"last_error":        cache.LastError,
	}
}

func childMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok {
		return nil
	}
	x, _ := v.(map[string]any)
	return x
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func timeOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Format(time.RFC3339)
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, dashboardHTML)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	cache := a.cache
	cfg := a.cfg
	a.mu.RUnlock()
	status := "ok"
	code := http.StatusOK
	if cfg.WeatherEnabled && cache.Forecast == nil {
		status, code = "degraded", http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{
		"status":         status,
		"service":        appName,
		"version":        version,
		"uptime_seconds": int64(time.Since(a.startedAt).Seconds()),
		"last_attempt":   timeOrNil(cache.LastAttempt),
		"last_success":   timeOrNil(cache.ForecastFetchedAt),
		"last_error":     cache.LastError,
	})
}

func (a *App) handleCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, a.currentPayload())
}

func (a *App) handleForecast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	m, t := a.cache.Forecast, a.cache.ForecastFetchedAt
	a.mu.RUnlock()
	if m == nil {
		writeError(w, http.StatusServiceUnavailable, "todavía no hay pronóstico en caché")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated_at": t.Format(time.RFC3339), "data": m})
}

func (a *App) handleAir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	m, t := a.cache.Air, a.cache.AirFetchedAt
	a.mu.RUnlock()
	if m == nil {
		writeError(w, http.StatusServiceUnavailable, "todavía no hay datos de calidad del aire")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated_at": t.Format(time.RFC3339), "data": m})
}

func (a *App) handleLocalCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	m, t := a.cache.Local, a.cache.LocalReceivedAt
	a.mu.RUnlock()
	if m == nil {
		writeError(w, http.StatusNotFound, "no se han recibido datos de sensores locales")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received_at": t.Format(time.RFC3339), "data": m})
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.mu.RLock()
		cfg := a.cfg
		a.mu.RUnlock()
		writeJSON(w, http.StatusOK, cfg)
	case http.MethodPost, http.MethodPut:
		var cfg Config
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			writeError(w, http.StatusBadRequest, "JSON no válido: "+err.Error())
			return
		}
		normalizeConfig(&cfg)
		if cfg.Latitude < -90 || cfg.Latitude > 90 || cfg.Longitude < -180 || cfg.Longitude > 180 {
			writeError(w, http.StatusBadRequest, "coordenadas fuera de rango")
			return
		}
		if strings.ContainsAny(cfg.Timezone, "\r\n") {
			writeError(w, http.StatusBadRequest, "zona horaria no válida")
			return
		}
		if err := writeJSONAtomic(filepath.Join(a.dataDir, "config.json"), cfg, 0600); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.mu.Lock()
		a.cfg = cfg
		a.mu.Unlock()
		a.triggerRefresh()
		writeJSON(w, http.StatusOK, map[string]any{"saved": true, "config": cfg})
	default:
		methodNotAllowed(w)
	}
}

func (a *App) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	a.triggerRefresh()
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

func (a *App) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !a.authorizedIngest(r) {
		writeError(w, http.StatusUnauthorized, "clave de API incorrecta")
		return
	}
	var payload map[string]any
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "JSON no válido: "+err.Error())
		return
	}
	if len(payload) == 0 {
		writeError(w, http.StatusBadRequest, "el objeto JSON está vacío")
		return
	}
	payload["received_at"] = time.Now().UTC().Format(time.RFC3339)
	if _, ok := payload["station"]; !ok {
		payload["station"] = "generic"
	}
	if err := a.storeLocal(payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "data": payload})
}

func (a *App) handleEcowitt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !a.authorizedIngest(r) {
		writeError(w, http.StatusUnauthorized, "clave de API incorrecta")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f := r.Form
	payload := map[string]any{
		"station":     firstNonEmpty(f.Get("stationtype"), f.Get("PASSKEY"), "ecowitt"),
		"source":      "ecowitt",
		"received_at": time.Now().UTC().Format(time.RFC3339),
	}
	setConverted(payload, "temperature_c", f.Get("tempf"), func(v float64) float64 { return (v - 32) * 5 / 9 })
	setConverted(payload, "humidity_pct", f.Get("humidity"), identity)
	setConverted(payload, "pressure_relative_hpa", f.Get("baromrelin"), func(v float64) float64 { return v * 33.8638866667 })
	setConverted(payload, "pressure_absolute_hpa", f.Get("baromabsin"), func(v float64) float64 { return v * 33.8638866667 })
	setConverted(payload, "wind_direction_deg", f.Get("winddir"), identity)
	setConverted(payload, "wind_speed_kmh", f.Get("windspeedmph"), func(v float64) float64 { return v * 1.609344 })
	setConverted(payload, "wind_gust_kmh", f.Get("windgustmph"), func(v float64) float64 { return v * 1.609344 })
	setConverted(payload, "rain_rate_mm_h", f.Get("rainratein"), func(v float64) float64 { return v * 25.4 })
	setConverted(payload, "rain_today_mm", f.Get("dailyrainin"), func(v float64) float64 { return v * 25.4 })
	setConverted(payload, "solar_radiation_w_m2", f.Get("solarradiation"), identity)
	setConverted(payload, "uv_index", f.Get("uv"), identity)
	if dt := f.Get("dateutc"); dt != "" {
		payload["station_time"] = dt
	}
	if err := a.storeLocal(payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func identity(v float64) float64 { return v }
func setConverted(m map[string]any, key, raw string, fn func(float64) float64) {
	if raw == "" {
		return
	}
	if v, err := strconv.ParseFloat(raw, 64); err == nil {
		m[key] = math.Round(fn(v)*1000) / 1000
	}
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *App) authorizedIngest(r *http.Request) bool {
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	if !cfg.RequireIngestKey {
		return true
	}
	supplied := r.Header.Get("X-API-Key")
	if supplied == "" {
		supplied = r.URL.Query().Get("key")
	}
	if supplied == "" {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			supplied = strings.TrimSpace(auth[7:])
		}
	}
	return supplied != "" && supplied == cfg.APIKey
}

func (a *App) storeLocal(payload map[string]any) error {
	now := time.Now().UTC()
	if err := writeJSONAtomic(filepath.Join(a.dataDir, "local", "current.json"), payload, 0600); err != nil {
		return err
	}
	a.mu.Lock()
	a.cache.Local, a.cache.LocalReceivedAt = payload, now
	a.mu.Unlock()
	return a.appendHistory("local", map[string]any{"received_at": now.Format(time.RFC3339), "data": payload})
}

func (a *App) appendHistory(kind string, data any) error {
	now := time.Now().UTC()
	entry := map[string]any{"timestamp": now.Format(time.RFC3339), "kind": kind, "data": data}
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	p := filepath.Join(a.dataDir, "history", now.Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

func (a *App) cleanupHistory(retentionDays int) {
	entries, err := os.ReadDir(filepath.Join(a.dataDir, "history"))
	if err != nil {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		d, err := time.Parse("2006-01-02", strings.TrimSuffix(e.Name(), ".jsonl"))
		if err == nil && d.Before(cutoff) {
			_ = os.Remove(filepath.Join(a.dataDir, "history", e.Name()))
		}
	}
}

func (a *App) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	hours := parseBoundedInt(r.URL.Query().Get("hours"), 24, 1, 24*365)
	limit := parseBoundedInt(r.URL.Query().Get("limit"), 500, 1, 5000)
	kind := r.URL.Query().Get("kind")
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	files, _ := filepath.Glob(filepath.Join(a.dataDir, "history", "*.jsonl"))
	sort.Strings(files)
	var out []map[string]any
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 512*1024)
		for scanner.Scan() {
			var entry map[string]any
			if json.Unmarshal(scanner.Bytes(), &entry) != nil {
				continue
			}
			ts, _ := time.Parse(time.RFC3339, fmt.Sprint(entry["timestamp"]))
			if ts.Before(cutoff) {
				continue
			}
			if kind != "" && fmt.Sprint(entry["kind"]) != kind {
				continue
			}
			out = append(out, entry)
			if len(out) > limit {
				out = out[len(out)-limit:]
			}
		}
		f.Close()
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"hours": hours, "count": len(out), "items": out})
}

func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	c := a.cache
	a.mu.RUnlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP meteoapi_up Whether the service process is running.\n# TYPE meteoapi_up gauge\nmeteoapi_up 1\n")
	fmt.Fprintf(w, "# HELP meteoapi_http_requests_total HTTP requests served.\n# TYPE meteoapi_http_requests_total counter\nmeteoapi_http_requests_total %d\n", a.requests.Load())
	fmt.Fprintf(w, "# HELP meteoapi_last_weather_success_timestamp_seconds Last successful weather refresh.\n# TYPE meteoapi_last_weather_success_timestamp_seconds gauge\nmeteoapi_last_weather_success_timestamp_seconds %d\n", c.ForecastFetchedAt.Unix())
}

func parseBoundedInt(s string, def, min, max int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func writeJSONAtomic(path string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "método no permitido")
}
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		n, e := x.Float64()
		return n, e == nil
	case int:
		return float64(x), true
	default:
		return 0, false
	}
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func weatherDescription(code int) string {
	switch code {
	case 0:
		return "Despejado"
	case 1:
		return "Principalmente despejado"
	case 2:
		return "Parcialmente nuboso"
	case 3:
		return "Cubierto"
	case 45, 48:
		return "Niebla"
	case 51, 53, 55:
		return "Llovizna"
	case 56, 57:
		return "Llovizna helada"
	case 61, 63, 65:
		return "Lluvia"
	case 66, 67:
		return "Lluvia helada"
	case 71, 73, 75, 77:
		return "Nieve"
	case 80, 81, 82:
		return "Chubascos"
	case 85, 86:
		return "Chubascos de nieve"
	case 95, 96, 99:
		return "Tormenta"
	default:
		return "Código meteorológico " + strconv.Itoa(code)
	}
}

const dashboardHTML = `<!doctype html>
<html lang="es"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>MeteoAPI</title>
<style>
:root{color-scheme:dark;--bg:#0f1720;--panel:#18232f;--panel2:#213040;--text:#eef4f8;--muted:#9fb1bf;--accent:#35b5e5;--ok:#48c78e;--warn:#ffcc66;--bad:#ff6b6b;--line:#314456}*{box-sizing:border-box}body{margin:0;background:linear-gradient(150deg,#0b1118,#142333);font:15px/1.45 system-ui,-apple-system,Segoe UI,sans-serif;color:var(--text)}header{padding:26px max(20px,5vw);display:flex;justify-content:space-between;gap:20px;align-items:center;border-bottom:1px solid var(--line)}h1{margin:0;font-size:28px}.sub{color:var(--muted)}main{max-width:1180px;margin:auto;padding:24px}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:14px}.card{background:rgba(24,35,47,.94);border:1px solid var(--line);border-radius:14px;padding:18px;box-shadow:0 10px 30px #0003}.value{font-size:32px;font-weight:700;margin-top:8px}.label{color:var(--muted);font-size:13px}.status{display:inline-block;padding:5px 10px;border-radius:999px;background:var(--panel2)}.ok{color:var(--ok)}.bad{color:var(--bad)}section{margin:22px 0}h2{font-size:19px;margin:0 0 12px}.forecast{display:grid;grid-template-columns:repeat(auto-fit,minmax(130px,1fr));gap:10px}.day{background:var(--panel2);border-radius:10px;padding:12px}.day strong{display:block;margin-bottom:8px}button{background:var(--accent);color:#07131b;border:0;border-radius:8px;padding:10px 14px;font-weight:700;cursor:pointer}button.secondary{background:#34495c;color:var(--text)}input{width:100%;background:#101922;color:var(--text);border:1px solid var(--line);border-radius:8px;padding:9px}label{display:block;color:var(--muted);font-size:13px;margin:8px 0 4px}.formgrid{display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:12px}.api code{display:block;background:#101922;border-radius:7px;padding:8px;margin:6px 0;color:#b9e7ff;overflow:auto}.small{font-size:12px;color:var(--muted)}#msg{margin-left:10px}.wide{grid-column:1/-1}@media(max-width:600px){header{align-items:flex-start;flex-direction:column}.value{font-size:26px}}
</style></head><body>
<header><div><h1>🌦️ MeteoAPI</h1><div class="sub" id="location">Cargando ubicación…</div></div><div><span class="status" id="status">Comprobando…</span></div></header>
<main>
<section><div class="grid">
<div class="card"><div class="label">Temperatura</div><div class="value" id="temp">—</div><div class="small" id="condition"></div></div>
<div class="card"><div class="label">Humedad</div><div class="value" id="hum">—</div><div class="small" id="apparent"></div></div>
<div class="card"><div class="label">Viento</div><div class="value" id="wind">—</div><div class="small" id="gust"></div></div>
<div class="card"><div class="label">Presión</div><div class="value" id="pressure">—</div><div class="small" id="updated"></div></div>
<div class="card"><div class="label">Calidad del aire (AQI UE)</div><div class="value" id="aqi">—</div><div class="small" id="particles"></div></div>
<div class="card"><div class="label">Sensor local</div><div class="value" id="localTemp">—</div><div class="small" id="localInfo">Sin datos</div></div>
</div></section>
<section class="card"><h2>Pronóstico de 7 días</h2><div class="forecast" id="forecast"><span class="sub">Esperando datos…</span></div></section>
<section class="card"><h2>Configuración</h2><div class="formgrid">
<div><label>Nombre de ubicación</label><input id="cfgName"></div><div><label>Latitud</label><input id="cfgLat" type="number" step="0.000001"></div><div><label>Longitud</label><input id="cfgLon" type="number" step="0.000001"></div><div><label>Zona horaria</label><input id="cfgTz"></div><div><label>Actualización (minutos)</label><input id="cfgPoll" type="number" min="5"></div><div><label>Retención histórica (días)</label><input id="cfgRetention" type="number" min="1"></div><div class="wide"><label>Clave de API para sensores</label><input id="cfgKey"></div></div><p><button onclick="saveConfig()">Guardar y actualizar</button> <button class="secondary" onclick="refreshNow()">Actualizar ahora</button><span id="msg"></span></p></section>
<section class="card api"><h2>API local</h2><code>GET /api/v1/current</code><code>GET /api/v1/forecast</code><code>GET /api/v1/air</code><code>GET /api/v1/history?hours=24</code><code>POST /api/v1/ingest?key=CLAVE</code><code>POST /input/ecowitt?key=CLAVE</code><code>GET /api/v1/health</code><code>GET /metrics</code><p class="small">La API funciona íntegramente en la red local. Los datos externos se obtienen de Open-Meteo y se conservan en el NAS.</p></section>
</main>
<script>
const $=id=>document.getElementById(id);const val=(x,s='')=>x===undefined||x===null?'—':x+s;
async function j(url,opt){const r=await fetch(url,opt);const d=await r.json();if(!r.ok)throw new Error(d.error||r.statusText);return d}
function fmtTime(x){return x?new Date(x).toLocaleString('es-ES'):'—'}
async function load(){try{const d=await j('/api/v1/current');$('location').textContent=d.location.name+' · '+d.location.latitude+', '+d.location.longitude;$('status').textContent=d.stale?'Datos antiguos':'Servicio activo';$('status').className='status '+(d.stale?'bad':'ok');const w=d.weather||{};$('temp').textContent=val(w.temperature_2m,' °C');$('condition').textContent=w.weather_description||'';$('hum').textContent=val(w.relative_humidity_2m,' %');$('apparent').textContent='Sensación: '+val(w.apparent_temperature,' °C');$('wind').textContent=val(w.wind_speed_10m,' km/h');$('gust').textContent='Rachas: '+val(w.wind_gusts_10m,' km/h');$('pressure').textContent=val(w.pressure_msl,' hPa');$('updated').textContent='Actualizado: '+fmtTime(d.updated_at);const a=d.air_quality||{};$('aqi').textContent=val(a.european_aqi);$('particles').textContent='PM2.5: '+val(a.pm2_5,' µg/m³')+' · PM10: '+val(a.pm10,' µg/m³');const l=d.local_sensor||{};$('localTemp').textContent=val(l.temperature_c,' °C');$('localInfo').textContent=l.station?'Estación: '+l.station:'Sin datos';}catch(e){$('status').textContent='Sin datos: '+e.message;$('status').className='status bad'}
try{const f=await j('/api/v1/forecast');renderForecast(f.data.daily||{})}catch(e){}
try{const c=await j('/api/v1/config');$('cfgName').value=c.location_name;$('cfgLat').value=c.latitude;$('cfgLon').value=c.longitude;$('cfgTz').value=c.timezone;$('cfgPoll').value=c.poll_minutes;$('cfgRetention').value=c.retention_days;$('cfgKey').value=c.api_key}catch(e){}
}
function renderForecast(d){const el=$('forecast');el.innerHTML='';const times=d.time||[];for(let i=0;i<times.length;i++){const x=document.createElement('div');x.className='day';x.innerHTML='<strong>'+new Date(times[i]+'T12:00:00').toLocaleDateString('es-ES',{weekday:'short',day:'2-digit'})+'</strong><div>🌡️ '+val(d.temperature_2m_min?.[i])+' / '+val(d.temperature_2m_max?.[i])+' °C</div><div>🌧️ '+val(d.precipitation_sum?.[i], ' mm')+'</div><div>💨 '+val(d.wind_speed_10m_max?.[i], ' km/h')+'</div>';el.appendChild(x)}}
async function saveConfig(){const old=await j('/api/v1/config');const c={...old,location_name:$('cfgName').value,latitude:+$('cfgLat').value,longitude:+$('cfgLon').value,timezone:$('cfgTz').value,poll_minutes:+$('cfgPoll').value,retention_days:+$('cfgRetention').value,api_key:$('cfgKey').value};try{await j('/api/v1/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(c)});$('msg').textContent='Guardado';setTimeout(load,1200)}catch(e){$('msg').textContent=e.message}}
async function refreshNow(){try{await j('/api/v1/refresh',{method:'POST'});$('msg').textContent='Actualización solicitada';setTimeout(load,2500)}catch(e){$('msg').textContent=e.message}}
load();setInterval(load,60000);
</script></body></html>`
