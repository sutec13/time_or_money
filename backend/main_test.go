package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestApp(t *testing.T) *app {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := migrate(db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	return &app{db: db, dialect: "sqlite"}
}

func TestCreateLockDefaultsPrice(t *testing.T) {
	application := newTestApp(t)
	body := bytes.NewBufferString(`{"secretText":"secret","unlockAt":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/locks", body)
	recorder := httptest.NewRecorder()

	application.createLock(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var item lockItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.PriceAmount != defaultPrice {
		t.Fatalf("expected default price %d, got %d", defaultPrice, item.PriceAmount)
	}
	if item.Name != "Lock #"+strconv.FormatInt(item.ID, 10) {
		t.Fatalf("expected default name Lock #%d, got %q", item.ID, item.Name)
	}
	if item.SecretText != "" {
		t.Fatalf("secret text should be hidden before unlock")
	}
}

func TestCreateLockStoresCustomName(t *testing.T) {
	application := newTestApp(t)
	body, _ := json.Marshal(createLockRequest{
		Name:       "Launch note",
		SecretText: "secret",
		UnlockAt:   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	request := httptest.NewRequest(http.MethodPost, "/api/locks", bytes.NewReader(body))
	recorder := httptest.NewRecorder()

	application.createLock(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var item lockItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.Name != "Launch note" {
		t.Fatalf("expected custom name, got %q", item.Name)
	}
}

func TestUnlockCreatesPurchaseEvent(t *testing.T) {
	application := newTestApp(t)
	price := 900
	body, _ := json.Marshal(createLockRequest{
		SecretText:  "paid secret",
		UnlockAt:    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		PriceAmount: &price,
	})
	createRequest := httptest.NewRequest(http.MethodPost, "/api/locks", bytes.NewReader(body))
	createRecorder := httptest.NewRecorder()
	application.createLock(createRecorder, createRequest)

	var created lockItem
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	if _, err := application.recordUnlock(created.ID, "stripe", "cs_test_record", "paid_stripe"); err != nil {
		t.Fatalf("record unlock: %v", err)
	}

	var eventCount int
	var amount int
	if err := application.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(amount), 0) FROM purchase_events WHERE lock_id = ?`, created.ID).Scan(&eventCount, &amount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("expected one purchase event, got %d", eventCount)
	}
	if amount != price {
		t.Fatalf("expected purchase amount %d, got %d", price, amount)
	}
}

func TestPublicUnlockRouteIsNotAvailable(t *testing.T) {
	application := newTestApp(t)
	request := httptest.NewRequest(http.MethodPost, "/api/locks/1/unlock", nil)
	recorder := httptest.NewRecorder()

	application.handleLockByID(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected unlock route status 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestPurchaseHistoryRouteIsNotAvailable(t *testing.T) {
	application := newTestApp(t)
	request := httptest.NewRequest(http.MethodGet, "/api/purchases", nil)
	recorder := httptest.NewRecorder()

	application.routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected purchases route status 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestDevReloadRouteIsHiddenInProduction(t *testing.T) {
	application := newTestApp(t)
	application.devMode = false
	request := httptest.NewRequest(http.MethodGet, "/dev/reload", nil)
	recorder := httptest.NewRecorder()

	application.routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected dev reload status 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestStripeSignatureVerification(t *testing.T) {
	payload := []byte(`{"id":"evt_test","type":"checkout.session.completed"}`)
	secret := "whsec_test"
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	header := "t=" + timestamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))

	if err := verifyStripeSignature(payload, header, secret); err != nil {
		t.Fatalf("expected signature to verify: %v", err)
	}
	if err := verifyStripeSignature(payload, header, "wrong_secret"); err == nil {
		t.Fatal("expected wrong signature secret to fail")
	}
}
