# Time or Money

React + Go + SQLite app for creating locked text entries that open after time passes, a demo payment, or Stripe Checkout in test mode.

## Requirements

- Go 1.22+
- Internet access for the React CDN files used by the frontend

## Run

Edit `backend/.env` if you want Supabase or Stripe settings. Empty values keep local SQLite and demo payments.

```powershell
cd backend
go mod tidy
go run .
```

Open http://localhost:5173.

The server automatically loads `backend/.env`. If `DATABASE_URL` is empty, the SQLite database is created at `backend/data/app.db`.

## Supabase database

The app uses SQLite by default. To use Supabase, copy the Postgres connection string from the Supabase Dashboard `Connect` panel and set it as `DATABASE_URL` before starting the server.

For a long-running local Go server, Supabase recommends a Postgres connection string or Session pooler connection string. Transaction pooler can also work; the app adds `default_query_exec_mode=simple_protocol` automatically because transaction poolers do not support prepared statements.

Put this in `backend/.env`:

```env
DATABASE_URL="postgresql://postgres.[PROJECT_REF]:[PASSWORD]@aws-0-[REGION].pooler.supabase.com:5432/postgres"
```

Do not put `sb_secret_...` or `anon` keys in `DATABASE_URL`; those are Supabase API keys, not database connection strings. Keep them in `SUPABASE_SERVICE_ROLE_KEY` later if the app adds Supabase REST/Auth features.

Useful files:

- `backend/.env.example`: environment variable template
- `backend/schema.supabase.sql`: schema you can paste into the Supabase SQL Editor if you want to create tables manually

## Stripe test payments

Set your Stripe test secret key in `backend/.env` before starting the Go server:

```env
STRIPE_SECRET_KEY="sk_test_..."
PUBLIC_BASE_URL="http://localhost:5173"
```

When `STRIPE_SECRET_KEY` is set, locked items use Stripe Checkout. After Checkout redirects back with `checkout_session_id`, the server verifies the Checkout Session with Stripe before unlocking and writing `purchase_events`.

## Notes

- If `STRIPE_SECRET_KEY` is not set, the app falls back to demo payment unlocks.
- Purchase history is persisted in `purchase_events` with `provider`, `provider_payment_id`, and `status`.
- Locks and purchase history require typing `削除する` before deletion.
- Lock creation stores the selected local time, browser timezone name, and timezone offset. Unlock checks use the server-side UTC instant saved at creation time, so changing region or IP later does not make a lock open earlier.
