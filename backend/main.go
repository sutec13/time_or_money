package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const (
	defaultPort     = "5173"
	defaultCurrency = "JPY"
	defaultPrice    = 500
)

type app struct {
	db                  *sql.DB
	dialect             string
	static              http.Handler
	stripeKey           string
	stripeWebhookSecret string
	baseURL             string
	devMode             bool
}

type lockItem struct {
	ID                    int64   `json:"id"`
	Name                  string  `json:"name"`
	SecretText            string  `json:"secretText,omitempty"`
	Preview               string  `json:"preview"`
	UnlockAt              string  `json:"unlockAt"`
	UnlockLocal           string  `json:"unlockLocal"`
	TimezoneName          string  `json:"timezoneName"`
	TimezoneOffsetMinutes int     `json:"timezoneOffsetMinutes"`
	PriceAmount           int     `json:"priceAmount"`
	Currency              string  `json:"currency"`
	Unlocked              bool    `json:"unlocked"`
	UnlockReason          *string `json:"unlockReason"`
	IsOpen                bool    `json:"isOpen"`
	CreatedAt             string  `json:"createdAt"`
	UpdatedAt             string  `json:"updatedAt"`
}

type createLockRequest struct {
	Name                  string `json:"name"`
	SecretText            string `json:"secretText"`
	UnlockAt              string `json:"unlockAt"`
	UnlockLocal           string `json:"unlockLocal"`
	TimezoneName          string `json:"timezoneName"`
	TimezoneOffsetMinutes *int   `json:"timezoneOffsetMinutes"`
	PriceAmount           *int   `json:"priceAmount"`
}

type deleteRequest struct {
	Confirmation string `json:"confirmation"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type configResponse struct {
	StripeEnabled bool   `json:"stripeEnabled"`
	DBProvider    string `json:"dbProvider"`
}

type checkoutResponse struct {
	CheckoutURL string    `json:"checkoutUrl,omitempty"`
	Mode        string    `json:"mode"`
	Lock        *lockItem `json:"lock,omitempty"`
}

type stripeCheckoutSession struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	PaymentStatus string `json:"payment_status"`
	Status        string `json:"status"`
	Metadata      struct {
		LockID string `json:"lock_id"`
	} `json:"metadata"`
}

type stripeWebhookEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

func main() {
	if err := loadEnvFile(".env"); err != nil {
		log.Printf("env file: %v", err)
	}

	db, dialect, err := openDB()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := migrate(db, dialect); err != nil {
		log.Fatal(err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	application := &app{
		db:                  db,
		dialect:             dialect,
		static:              http.FileServer(http.Dir("../frontend")),
		stripeKey:           os.Getenv("STRIPE_SECRET_KEY"),
		stripeWebhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
		baseURL:             configuredBaseURL(port),
		devMode:             isDevMode(),
	}

	log.Printf("Time or Money running at http://localhost:%s using %s", port, dialect)
	log.Fatal(http.ListenAndServe(":"+port, withLogging(application.routes())))
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/locks", a.handleLocks)
	mux.HandleFunc("/api/locks/", a.handleLockByID)
	mux.HandleFunc("/api/stripe/checkout/complete", a.handleStripeCheckoutComplete)
	mux.HandleFunc("/api/stripe/webhook", a.handleStripeWebhook)
	if a.devMode {
		mux.HandleFunc("/dev/reload", a.handleReload)
	}
	mux.Handle("/", a.spaHandler())
	return mux
}

func loadEnvFile(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
	return nil
}

func openDB() (*sql.DB, string, error) {
	if databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL")); databaseURL != "" {
		if !strings.HasPrefix(databaseURL, "postgres://") && !strings.HasPrefix(databaseURL, "postgresql://") {
			log.Printf("DATABASE_URL is not a Postgres connection string; falling back to SQLite")
		} else {
			db, err := sql.Open("pgx", postgresURL(databaseURL))
			if err != nil {
				return nil, "", err
			}
			db.SetMaxOpenConns(5)
			db.SetMaxIdleConns(2)
			db.SetConnMaxLifetime(30 * time.Minute)
			return db, "postgres", db.Ping()
		}
	}

	if err := os.MkdirAll("data", 0755); err != nil {
		return nil, "", err
	}

	dbPath := filepath.Join("data", "app.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, "", err
	}
	db.SetMaxOpenConns(1)
	return db, "sqlite", db.Ping()
}

func postgresURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	query := parsed.Query()
	if query.Get("default_query_exec_mode") == "" {
		query.Set("default_query_exec_mode", "simple_protocol")
	}
	if query.Get("sslmode") == "" {
		query.Set("sslmode", "require")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func migrate(db *sql.DB, dialect string) error {
	statements := sqliteMigrations()
	if dialect == "postgres" {
		statements = postgresMigrations()
	}

	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return err
		}
	}
	return nil
}

func sqliteMigrations() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS locks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			secret_text TEXT NOT NULL,
			unlock_at TEXT NOT NULL,
			unlock_local TEXT NOT NULL DEFAULT '',
			timezone_name TEXT NOT NULL DEFAULT 'UTC',
			timezone_offset_minutes INTEGER NOT NULL DEFAULT 0,
			price_amount INTEGER NOT NULL DEFAULT 500,
			currency TEXT NOT NULL DEFAULT 'JPY',
			unlocked INTEGER NOT NULL DEFAULT 0,
			unlock_reason TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS purchase_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			lock_id INTEGER NOT NULL,
			amount INTEGER NOT NULL,
			currency TEXT NOT NULL DEFAULT 'JPY',
			provider TEXT NOT NULL,
			provider_payment_id TEXT,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`ALTER TABLE purchase_events ADD COLUMN lock_preview TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE locks ADD COLUMN name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE locks ADD COLUMN unlock_local TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE locks ADD COLUMN timezone_name TEXT NOT NULL DEFAULT 'UTC'`,
		`ALTER TABLE locks ADD COLUMN timezone_offset_minutes INTEGER NOT NULL DEFAULT 0`,
		`UPDATE locks SET unlock_local = substr(unlock_at, 1, 16) WHERE unlock_local = ''`,
		`CREATE INDEX IF NOT EXISTS idx_purchase_events_lock_id ON purchase_events(lock_id)`,
	}
}

func postgresMigrations() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS locks (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			secret_text TEXT NOT NULL,
			unlock_at TEXT NOT NULL,
			unlock_local TEXT NOT NULL DEFAULT '',
			timezone_name TEXT NOT NULL DEFAULT 'UTC',
			timezone_offset_minutes INTEGER NOT NULL DEFAULT 0,
			price_amount INTEGER NOT NULL DEFAULT 500,
			currency TEXT NOT NULL DEFAULT 'JPY',
			unlocked INTEGER NOT NULL DEFAULT 0,
			unlock_reason TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS purchase_events (
			id BIGSERIAL PRIMARY KEY,
			lock_id BIGINT NOT NULL,
			amount INTEGER NOT NULL,
			currency TEXT NOT NULL DEFAULT 'JPY',
			provider TEXT NOT NULL,
			provider_payment_id TEXT,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			lock_preview TEXT NOT NULL DEFAULT ''
		)`,
		`ALTER TABLE purchase_events ADD COLUMN IF NOT EXISTS lock_preview TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE locks ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE locks ADD COLUMN IF NOT EXISTS unlock_local TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE locks ADD COLUMN IF NOT EXISTS timezone_name TEXT NOT NULL DEFAULT 'UTC'`,
		`ALTER TABLE locks ADD COLUMN IF NOT EXISTS timezone_offset_minutes INTEGER NOT NULL DEFAULT 0`,
		`UPDATE locks SET unlock_local = substring(unlock_at from 1 for 16) WHERE unlock_local = ''`,
		`CREATE INDEX IF NOT EXISTS idx_purchase_events_lock_id ON purchase_events(lock_id)`,
	}
}

func configuredBaseURL(port string) string {
	if value := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"); value != "" {
		return value
	}
	return "http://localhost:" + port
}

func isDevMode() bool {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	if env == "development" || env == "dev" || env == "local" {
		return true
	}
	if env == "production" || env == "prod" || os.Getenv("RENDER") != "" {
		return false
	}
	return true
}

func (a *app) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, configResponse{StripeEnabled: a.stripeKey != "", DBProvider: a.dialect})
}

func readDeleteConfirmation(r *http.Request) bool {
	if r.Header.Get("X-Delete-Confirmation") == "delete" || r.URL.Query().Get("confirmation") == "delete" {
		return true
	}
	var input deleteRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return false
	}
	return input.Confirmation == "delete"
}

func (a *app) handleLocks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.listLocks(w)
	case http.MethodPost:
		a.createLock(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *app) query(query string) string {
	if a.dialect != "postgres" {
		return query
	}
	var builder strings.Builder
	index := 1
	for _, char := range query {
		if char == '?' {
			builder.WriteString("$")
			builder.WriteString(strconv.Itoa(index))
			index++
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

func (a *app) handleLockByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/locks/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "lock not found")
		return
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid lock id")
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			a.getLock(w, id)
		case http.MethodDelete:
			a.deleteLock(w, r, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	if len(parts) == 2 && parts[1] == "checkout" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.createStripeCheckout(w, id)
		return
	}

	writeError(w, http.StatusNotFound, "route not found")
}

func (a *app) listLocks(w http.ResponseWriter) {
	rows, err := a.db.Query(`SELECT id, name, secret_text, unlock_at, unlock_local, timezone_name, timezone_offset_minutes, price_amount, currency, unlocked, unlock_reason, created_at, updated_at FROM locks ORDER BY created_at DESC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list locks")
		return
	}
	defer rows.Close()

	locks := []lockItem{}
	for rows.Next() {
		item, err := scanLock(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read lock")
			return
		}
		locks = append(locks, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{"locks": locks})
}

func (a *app) createLock(w http.ResponseWriter, r *http.Request) {
	var input createLockRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	secretText := strings.TrimSpace(input.SecretText)
	if secretText == "" {
		writeError(w, http.StatusBadRequest, "secret text is required")
		return
	}
	name := strings.TrimSpace(input.Name)
	if len([]rune(name)) > 100 {
		writeError(w, http.StatusBadRequest, "name must be 100 characters or fewer")
		return
	}

	unlockAt, unlockLocal, timezoneName, timezoneOffsetMinutes, err := parseRequestedUnlock(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	price := defaultPrice
	if input.PriceAmount != nil {
		price = *input.PriceAmount
	}
	if price <= 0 {
		writeError(w, http.StatusBadRequest, "priceAmount must be greater than 0")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id, err := a.insertLock(name, secretText, unlockAt.UTC().Format(time.RFC3339), unlockLocal, timezoneName, timezoneOffsetMinutes, price, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create lock")
		return
	}
	item, err := a.findLock(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load created lock")
		return
	}

	writeJSON(w, http.StatusCreated, item)
}

func (a *app) insertLock(name string, secretText string, unlockAt string, unlockLocal string, timezoneName string, timezoneOffsetMinutes int, price int, now string) (int64, error) {
	query := `INSERT INTO locks (name, secret_text, unlock_at, unlock_local, timezone_name, timezone_offset_minutes, price_amount, currency, unlocked, unlock_reason, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, ?, ?)`
	args := []any{name, secretText, unlockAt, unlockLocal, timezoneName, timezoneOffsetMinutes, price, defaultCurrency, now, now}
	if a.dialect == "postgres" {
		var id int64
		err := a.db.QueryRow(a.query(query+" RETURNING id"), args...).Scan(&id)
		return id, err
	}
	result, err := a.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func parseRequestedUnlock(input createLockRequest) (time.Time, string, string, int, error) {
	timezoneName := strings.TrimSpace(input.TimezoneName)
	if timezoneName == "" {
		timezoneName = "Local"
	}

	if input.UnlockLocal != "" {
		if input.TimezoneOffsetMinutes == nil {
			return time.Time{}, "", "", 0, errors.New("timezoneOffsetMinutes is required with unlockLocal")
		}
		local, err := time.Parse("2006-01-02T15:04", input.UnlockLocal)
		if err != nil {
			return time.Time{}, "", "", 0, errors.New("unlockLocal must be YYYY-MM-DDTHH:mm")
		}
		offsetMinutes := *input.TimezoneOffsetMinutes
		location := time.FixedZone(timezoneName, -offsetMinutes*60)
		unlockAt := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, location)
		return unlockAt.UTC(), input.UnlockLocal, timezoneName, offsetMinutes, nil
	}

	unlockAt, err := time.Parse(time.RFC3339, input.UnlockAt)
	if err != nil {
		local, localErr := time.Parse("2006-01-02T15:04", input.UnlockAt)
		if localErr != nil {
			return time.Time{}, "", "", 0, errors.New("unlockAt must be RFC3339 or YYYY-MM-DDTHH:mm")
		}
		offsetMinutes := 0
		location := time.FixedZone(timezoneName, 0)
		if input.TimezoneOffsetMinutes != nil {
			offsetMinutes = *input.TimezoneOffsetMinutes
			location = time.FixedZone(timezoneName, -offsetMinutes*60)
		}
		localAt := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, location)
		return localAt.UTC(), input.UnlockAt, timezoneName, offsetMinutes, nil
	}
	return unlockAt.UTC(), unlockAt.UTC().Format("2006-01-02T15:04"), "UTC", 0, nil
}

func (a *app) getLock(w http.ResponseWriter, id int64) {
	item, err := a.findLock(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "lock not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load lock")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *app) createStripeCheckout(w http.ResponseWriter, id int64) {
	if a.stripeKey == "" {
		writeError(w, http.StatusServiceUnavailable, "stripe is not configured")
		return
	}

	item, err := a.findRawLock(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "lock not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load lock")
		return
	}
	if item.Unlocked == 1 || unlockTimePassed(item.UnlockAt) {
		openItem, err := a.findLock(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load lock")
			return
		}
		writeJSON(w, http.StatusOK, checkoutResponse{Mode: "already_open", Lock: &openItem})
		return
	}

	session, err := a.createStripeSession(item)
	if err != nil {
		log.Printf("stripe checkout: %v", err)
		writeError(w, http.StatusBadGateway, "failed to create stripe checkout session")
		return
	}

	writeJSON(w, http.StatusOK, checkoutResponse{Mode: "stripe", CheckoutURL: session.URL})
}

func (a *app) handleStripeCheckoutComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.stripeKey == "" {
		writeError(w, http.StatusBadRequest, "stripe is not configured")
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	session, err := a.retrieveStripeSession(sessionID)
	if err != nil {
		log.Printf("stripe retrieve: %v", err)
		writeError(w, http.StatusBadGateway, "failed to verify stripe checkout session")
		return
	}
	if session.PaymentStatus != "paid" {
		writeError(w, http.StatusBadRequest, "stripe payment is not paid")
		return
	}

	lockID, err := strconv.ParseInt(session.Metadata.LockID, 10, 64)
	if err != nil || lockID <= 0 {
		writeError(w, http.StatusBadRequest, "stripe session lock metadata is invalid")
		return
	}

	item, err := a.recordUnlock(lockID, "stripe", session.ID, "paid_stripe")
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "lock not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record stripe payment")
		return
	}

	writeJSON(w, http.StatusOK, item)
}

func (a *app) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.stripeWebhookSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "stripe webhook is not configured")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read webhook")
		return
	}
	if err := verifyStripeSignature(body, r.Header.Get("Stripe-Signature"), a.stripeWebhookSecret); err != nil {
		writeError(w, http.StatusBadRequest, "invalid stripe signature")
		return
	}

	var event stripeWebhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, http.StatusBadRequest, "invalid stripe webhook json")
		return
	}

	if event.Type == "checkout.session.completed" {
		var session stripeCheckoutSession
		if err := json.Unmarshal(event.Data.Object, &session); err != nil {
			writeError(w, http.StatusBadRequest, "invalid stripe checkout session")
			return
		}
		if session.PaymentStatus == "paid" {
			lockID, err := strconv.ParseInt(session.Metadata.LockID, 10, 64)
			if err != nil || lockID <= 0 {
				writeError(w, http.StatusBadRequest, "stripe session lock metadata is invalid")
				return
			}
			if _, err := a.recordUnlock(lockID, "stripe", session.ID, "paid_stripe"); err != nil {
				log.Printf("stripe webhook record unlock: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to record stripe payment")
				return
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]bool{"received": true})
}

func verifyStripeSignature(payload []byte, header string, secret string) error {
	timestamp, signatures := stripeSignatureValues(header)
	if timestamp == "" || len(signatures) == 0 {
		return errors.New("missing stripe signature")
	}

	createdAt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return err
	}
	if time.Since(time.Unix(createdAt, 0)) > 5*time.Minute || time.Until(time.Unix(createdAt, 0)) > 5*time.Minute {
		return errors.New("stripe signature timestamp outside tolerance")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := []byte(hex.EncodeToString(mac.Sum(nil)))
	for _, signature := range signatures {
		if hmac.Equal(expected, []byte(signature)) {
			return nil
		}
	}
	return errors.New("stripe signature mismatch")
}

func stripeSignatureValues(header string) (string, []string) {
	var timestamp string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			timestamp = value
		case "v1":
			signatures = append(signatures, value)
		}
	}
	return timestamp, signatures
}

type rawLock struct {
	ID          int64
	Name        string
	SecretText  string
	UnlockAt    string
	PriceAmount int
	Currency    string
	Unlocked    int
}

func (a *app) findRawLock(id int64) (rawLock, error) {
	var item rawLock
	err := a.db.QueryRow(a.query(`SELECT id, name, secret_text, unlock_at, price_amount, currency, unlocked FROM locks WHERE id = ?`), id).
		Scan(&item.ID, &item.Name, &item.SecretText, &item.UnlockAt, &item.PriceAmount, &item.Currency, &item.Unlocked)
	return item, err
}

func (a *app) recordUnlock(id int64, provider string, providerPaymentID string, unlockReason string) (lockItem, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return lockItem{}, err
	}
	defer tx.Rollback()

	var price int
	var currency string
	var secretText string
	var unlocked int
	err = tx.QueryRow(a.query(`SELECT price_amount, currency, secret_text, unlocked FROM locks WHERE id = ?`), id).Scan(&price, &currency, &secretText, &unlocked)
	if err != nil {
		return lockItem{}, err
	}
	if unlocked == 1 {
		_ = tx.Rollback()
		return a.findLock(id)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(a.query(`UPDATE locks SET unlocked = 1, unlock_reason = ?, updated_at = ? WHERE id = ?`), unlockReason, now, id); err != nil {
		return lockItem{}, err
	}

	if providerPaymentID != "" {
		var count int
		if err := tx.QueryRow(a.query(`SELECT COUNT(*) FROM purchase_events WHERE provider = ? AND provider_payment_id = ?`), provider, providerPaymentID).Scan(&count); err != nil {
			return lockItem{}, err
		}
		if count > 0 {
			if err := tx.Commit(); err != nil {
				return lockItem{}, err
			}
			return a.findLock(id)
		}
	}

	var paymentID any
	if providerPaymentID != "" {
		paymentID = providerPaymentID
	}

	if _, err := tx.Exec(
		a.query(`INSERT INTO purchase_events (lock_id, lock_preview, amount, currency, provider, provider_payment_id, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'succeeded', ?)`),
		id,
		makePreview(secretText),
		price,
		currency,
		provider,
		paymentID,
		now,
	); err != nil {
		return lockItem{}, err
	}

	if err := tx.Commit(); err != nil {
		return lockItem{}, err
	}

	item, err := a.findLock(id)
	if err != nil {
		return lockItem{}, err
	}

	return item, nil
}

func (a *app) createStripeSession(item rawLock) (stripeCheckoutSession, error) {
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", a.baseURL+"/?checkout_session_id={CHECKOUT_SESSION_ID}")
	form.Set("cancel_url", a.baseURL+"/?checkout_cancelled=1")
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", strings.ToLower(item.Currency))
	form.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(item.PriceAmount))
	form.Set("line_items[0][price_data][product_data][name]", "Time or Money "+lockDisplayName(item.ID, item.Name))
	form.Set("metadata[lock_id]", strconv.FormatInt(item.ID, 10))

	request, err := http.NewRequest(http.MethodPost, "https://api.stripe.com/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return stripeCheckoutSession{}, err
	}
	request.SetBasicAuth(a.stripeKey, "")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return stripeCheckoutSession{}, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return stripeCheckoutSession{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return stripeCheckoutSession{}, errors.New(string(body))
	}

	var session stripeCheckoutSession
	if err := json.Unmarshal(body, &session); err != nil {
		return stripeCheckoutSession{}, err
	}
	if session.URL == "" {
		return stripeCheckoutSession{}, errors.New("stripe checkout session did not include a url")
	}
	return session, nil
}

func (a *app) retrieveStripeSession(sessionID string) (stripeCheckoutSession, error) {
	endpoint := "https://api.stripe.com/v1/checkout/sessions/" + url.PathEscape(sessionID)
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return stripeCheckoutSession{}, err
	}
	request.SetBasicAuth(a.stripeKey, "")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return stripeCheckoutSession{}, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return stripeCheckoutSession{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return stripeCheckoutSession{}, errors.New(string(body))
	}

	var session stripeCheckoutSession
	if err := json.Unmarshal(body, &session); err != nil {
		return stripeCheckoutSession{}, err
	}
	return session, nil
}

func (a *app) deleteLock(w http.ResponseWriter, r *http.Request, id int64) {
	if !readDeleteConfirmation(r) {
		writeError(w, http.StatusBadRequest, "confirmation must be delete")
		return
	}

	result, err := a.db.Exec(a.query(`DELETE FROM locks WHERE id = ?`), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete lock")
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeError(w, http.StatusNotFound, "lock not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (a *app) findLock(id int64) (lockItem, error) {
	row := a.db.QueryRow(a.query(`SELECT id, name, secret_text, unlock_at, unlock_local, timezone_name, timezone_offset_minutes, price_amount, currency, unlocked, unlock_reason, created_at, updated_at FROM locks WHERE id = ?`), id)
	return scanLock(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanLock(row scanner) (lockItem, error) {
	var item lockItem
	var secretText string
	var unlocked int
	var unlockReason sql.NullString

	if err := row.Scan(
		&item.ID,
		&item.Name,
		&secretText,
		&item.UnlockAt,
		&item.UnlockLocal,
		&item.TimezoneName,
		&item.TimezoneOffsetMinutes,
		&item.PriceAmount,
		&item.Currency,
		&unlocked,
		&unlockReason,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return item, err
	}

	if unlockReason.Valid {
		item.UnlockReason = &unlockReason.String
	}
	item.Name = lockDisplayName(item.ID, item.Name)

	item.Unlocked = unlocked == 1
	item.IsOpen = item.Unlocked || unlockTimePassed(item.UnlockAt)
	if item.IsOpen {
		item.SecretText = secretText
		item.Preview = makePreview(secretText)
	} else {
		item.Preview = "locked"
	}

	return item, nil
}

func lockDisplayName(id int64, name string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	return "Lock #" + strconv.FormatInt(id, 10)
}

func unlockTimePassed(value string) bool {
	unlockAt, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return false
	}
	return !time.Now().Before(unlockAt)
}

func makePreview(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	runes := []rune(value)
	if len(runes) <= 28 {
		return value
	}
	return string(runes[:28]) + "..."
}

func (a *app) spaHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "api route not found")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/dev/") {
			writeError(w, http.StatusNotFound, "dev route not found")
			return
		}

		cleanPath := strings.TrimPrefix(filepath.Clean(r.URL.Path), string(filepath.Separator))
		localPath := filepath.Join("../frontend", cleanPath)
		if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
			a.static.ServeHTTP(w, r)
			return
		}

		http.ServeFile(w, r, "../frontend/index.html")
	}
}

func (a *app) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	lastSeen := latestFrontendModTime()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			next := latestFrontendModTime()
			if next.After(lastSeen) {
				lastSeen = next
				_, _ = w.Write([]byte("event: reload\ndata: changed\n\n"))
				flusher.Flush()
			}
		}
	}
}

func latestFrontendModTime() time.Time {
	var latest time.Time
	_ = filepath.WalkDir("../frontend", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	return latest
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
