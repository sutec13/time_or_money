# Time or Money

React + Go + SQLite/Postgres app for creating locked text entries that open after time passes or after Stripe Checkout in test mode.

## Requirements

- Go 1.22+
- Internet access for the React CDN files used by the frontend

## Run

Edit `backend/.env` if you want Supabase or Stripe settings. Empty database settings keep local SQLite.

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

Do not put `sb_secret_...` or `anon` keys in `DATABASE_URL`; those are Supabase API keys, not database connection strings. This app currently only needs the database connection string.

Useful files:

- `backend/.env.example`: environment variable template
- `backend/schema.supabase.sql`: schema you can paste into the Supabase SQL Editor if you want to create tables manually

## Stripe test payments

Set your Stripe test secret key in `backend/.env` before starting the Go server:

```env
STRIPE_SECRET_KEY="sk_test_..."
STRIPE_WEBHOOK_SECRET="whsec_..."
PUBLIC_BASE_URL="http://localhost:5173"
```

Locked items use Stripe Checkout. After Checkout redirects back with `checkout_session_id`, the server verifies the Checkout Session with Stripe before unlocking and writing `purchase_events`.

For production or Render, also create a Stripe webhook endpoint:

- Endpoint URL: `https://your-render-url/api/stripe/webhook`
- Event: `checkout.session.completed`
- Secret: copy the endpoint signing secret into `STRIPE_WEBHOOK_SECRET`

The webhook is the durable payment path. The redirect verification remains as a convenience for updating the browser immediately after payment.

## Notes

- If `STRIPE_SECRET_KEY` is not set, paid early unlocks are disabled.
- Purchase history is persisted in `purchase_events` with `provider`, `provider_payment_id`, and `status`.
- Purchase history is not exposed through a public API.
- Locks require typing `delete` before deletion.
- `/dev/reload` is only registered in development mode. Set `APP_ENV=production` on production hosts.
- Lock creation stores the selected local time, browser timezone name, and timezone offset. Unlock checks use the server-side UTC instant saved at creation time, so changing region or IP later does not make a lock open earlier.
