package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if item.SecretText != "" {
		t.Fatalf("secret text should be hidden before unlock")
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

	unlockRecorder := httptest.NewRecorder()
	application.unlockLock(unlockRecorder, created.ID)
	if unlockRecorder.Code != http.StatusOK {
		t.Fatalf("expected unlock status 200, got %d: %s", unlockRecorder.Code, unlockRecorder.Body.String())
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
