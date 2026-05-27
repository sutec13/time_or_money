# AGENTS.md

## Project Snapshot

Time or Money is a small single-user app for creating text locks that open after a saved unlock time or after Stripe Checkout payment.

- Frontend: React from CDN, source in `frontend/`
- Backend: Go HTTP server in `backend/`
- Database: SQLite locally by default, Supabase/Postgres in production through `DATABASE_URL`
- Production URL: `https://time-or-money.onrender.com`
- GitHub repo: `sutec13/time_or_money`

## Run Locally

```powershell
cd backend
go mod tidy
go run .
```

Open `http://localhost:5173`.

The server loads `backend/.env` automatically. Do not commit real `.env` values.

Useful local env template:

```env
DATABASE_URL=""
PUBLIC_BASE_URL="http://localhost:5173"
APP_ENV="development"
STRIPE_SECRET_KEY=""
STRIPE_WEBHOOK_SECRET=""
```

If `DATABASE_URL` is empty, the app uses `backend/data/app.db`. Both `backend/.env` and `backend/data/` are ignored by git.

## Production Configuration

Render runs the Go service from the `backend` directory.

- Root Directory: `backend`
- Build Command: `go build -tags netgo -ldflags '-s -w' -o app`
- Start Command: `./app`

Render environment variables:

```env
DATABASE_URL="Supabase Postgres connection string"
PUBLIC_BASE_URL="https://time-or-money.onrender.com"
APP_ENV="production"
STRIPE_SECRET_KEY="sk_test_or_sk_live..."
STRIPE_WEBHOOK_SECRET="whsec_..."
```

`PUBLIC_BASE_URL` is used to build Stripe Checkout `success_url` and `cancel_url`. If it is accidentally set to `localhost`, Checkout will return users to the wrong place.

## Stripe Notes

- Checkout route: `POST /api/locks/{id}/checkout`
- Redirect completion route: `POST /api/stripe/checkout/complete?session_id=...`
- Webhook route: `POST /api/stripe/webhook`
- Required webhook event: `checkout.session.completed`
- Webhook endpoint URL: `https://time-or-money.onrender.com/api/stripe/webhook`

The webhook is the durable payment path. The browser redirect is only a convenience so the UI updates immediately.

Never expose Stripe secret keys or webhook secrets in responses, logs, docs, or frontend code. If the UI needs to show test/live mode, derive it from the key prefix on the server and return only `"test"` or `"live"`.

## Database Notes

The backend migrations live in `backend/main.go`; `backend/schema.supabase.sql` is a manual Supabase reference schema.

Important tables:

- `locks`: lock content, display name, unlock time, price, Stripe/open state
- `purchase_events`: Stripe payment audit rows; no public API currently exposes this table

Existing lock names can be empty in the DB. The API returns `Lock #id` as the display fallback.

## Current Public API

- `GET /api/config`
- `GET /api/locks`
- `POST /api/locks`
- `GET /api/locks/{id}`
- `DELETE /api/locks/{id}`
- `POST /api/locks/{id}/checkout`
- `POST /api/stripe/checkout/complete`
- `POST /api/stripe/webhook`

There should be no public paymentless unlock endpoint and no public purchase-history endpoint.

Deletion requires the ASCII confirmation text `delete`.

## GitHub Actions

`.github/workflows/supabase-keepalive.yml` pings production every Sunday at 12:07 JST (`7 3 * * 0` UTC). It calls `GET /api/locks`, which reads Supabase through the app without storing DB credentials in GitHub.

Manual run:

1. GitHub repo -> Actions
2. Select `Supabase Keepalive`
3. Click `Run workflow` on branch `main`

## Verification Before Push

Run backend tests:

```powershell
cd backend
go test ./...
```

For frontend-only changes, there is no build step yet because the app uses browser-loaded React/Babel. Still inspect `frontend/src/App.jsx` for syntax carefully; a JSX syntax error breaks the page at runtime.

## Style And Safety Preferences

- Keep secrets out of repo files and final answers.
- Use `apply_patch` for manual edits.
- Avoid unrelated refactors.
- Keep UI copy concise; the user has been removing extra explanatory text.
- Prefer server-side checks for payment/security behavior.
- If touching time behavior, keep unlock decisions based on the UTC instant stored at creation time, not the viewer's current timezone or IP.
