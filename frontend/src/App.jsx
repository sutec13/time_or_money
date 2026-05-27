const { useEffect, useMemo, useState } = React;

const api = {
  async config() {
    const response = await fetch("/api/config");
    return parseResponse(response);
  },
  async listLocks() {
    const response = await fetch("/api/locks");
    return parseResponse(response);
  },
  async createLock(payload) {
    const response = await fetch("/api/locks", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    });
    return parseResponse(response);
  },
  async createCheckout(id) {
    const response = await fetch(`/api/locks/${id}/checkout`, { method: "POST" });
    return parseResponse(response);
  },
  async completeCheckout(sessionId) {
    const response = await fetch(`/api/stripe/checkout/complete?session_id=${encodeURIComponent(sessionId)}`, {
      method: "POST"
    });
    return parseResponse(response);
  },
  async remove(id) {
    const response = await fetch(`/api/locks/${id}`, {
      method: "DELETE",
      headers: {
        "Content-Type": "application/json",
        "X-Delete-Confirmation": "削除する"
      },
      body: JSON.stringify({ confirmation: "削除する" })
    });
    return parseResponse(response);
  }
};

async function parseResponse(response) {
  if (response.status === 204) return null;
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || "リクエストに失敗しました");
  return data;
}

function defaultUnlockTime() {
  const date = new Date();
  date.setSeconds(0, 0);
  date.setMinutes(date.getMinutes() - date.getTimezoneOffset());
  return date.toISOString().slice(0, 16);
}

function timezoneName() {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "Local";
}

function timezoneOffsetMinutes(localValue) {
  return new Date(localValue).getTimezoneOffset();
}

function localToRFC3339(localValue) {
  return new Date(localValue).toISOString();
}

function formatDate(value) {
  return new Intl.DateTimeFormat("ja-JP", {
    dateStyle: "medium",
    timeStyle: "short"
  }).format(new Date(value));
}

function formatPrice(amount, currency = "JPY") {
  return new Intl.NumberFormat("ja-JP", {
    style: "currency",
    currency,
    maximumFractionDigits: 0
  }).format(amount);
}

function formatUnlockTime(lock) {
  if (lock.unlockLocal) {
    return `${lock.unlockLocal.replace("T", " ")} (${lock.timezoneName})`;
  }
  return formatDate(lock.unlockAt);
}

function getRemaining(unlockAt) {
  return new Date(unlockAt).getTime() - Date.now();
}

function formatRemaining(ms) {
  if (ms <= 0) return "開封できます";
  const totalSeconds = Math.ceil(ms / 1000);
  const days = Math.floor(totalSeconds / 86400);
  const hours = Math.floor((totalSeconds % 86400) / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (days > 0) return `${days}日 ${hours}時間 ${minutes}分`;
  return `${String(hours).padStart(2, "0")}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
}

function statusFor(lock) {
  if (lock.unlocked) {
    if (lock.unlockReason === "paid_stripe") return "Stripeで開封";
    if (lock.unlockReason === "paid_demo") return "デモ購入で開封";
    return "開封済み";
  }
  if (lock.isOpen || getRemaining(lock.unlockAt) <= 0) return "時間で開封可能";
  return "ロック中";
}

function App() {
  const [locks, setLocks] = useState([]);
  const [stripeEnabled, setStripeEnabled] = useState(false);
  const [dbProvider, setDbProvider] = useState("sqlite");
  const [secretText, setSecretText] = useState("");
  const [unlockAt, setUnlockAt] = useState(defaultUnlockTime());
  const [unlockAtTouched, setUnlockAtTouched] = useState(false);
  const [priceAmount, setPriceAmount] = useState("500");
  const [message, setMessage] = useState("");
  const [deleteDialog, setDeleteDialog] = useState(null);
  const [deleteText, setDeleteText] = useState("");
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);

  const openCount = useMemo(
    () => locks.filter((lock) => lock.unlocked || lock.isOpen || getRemaining(lock.unlockAt) <= 0).length,
    [locks, tick]
  );

  useEffect(() => {
    bootstrap().finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    const id = setInterval(() => setTick((value) => value + 1), 1000);
    return () => clearInterval(id);
  }, []);

  useEffect(() => {
    if (!unlockAtTouched && !secretText) {
      setUnlockAt(defaultUnlockTime());
    }
  }, [tick, unlockAtTouched, secretText]);

  useEffect(() => {
    if (locks.some((lock) => !lock.unlocked && !lock.isOpen && getRemaining(lock.unlockAt) <= 0)) {
      refreshAll();
    }
  }, [tick, locks]);

  async function bootstrap() {
    const config = await api.config();
    setStripeEnabled(config.stripeEnabled);
    setDbProvider(config.dbProvider || "sqlite");

    const params = new URLSearchParams(window.location.search);
    const sessionId = params.get("checkout_session_id");
    if (sessionId) {
      setMessage("Stripe テスト決済を確認しています...");
      try {
        await api.completeCheckout(sessionId);
        setMessage("Stripe テスト決済で開封しました。");
        window.history.replaceState({}, "", window.location.pathname);
      } catch (error) {
        setMessage(error.message);
      }
    } else if (params.get("checkout_cancelled")) {
      setMessage("Stripe 決済をキャンセルしました。");
      window.history.replaceState({}, "", window.location.pathname);
    }

    await refreshAll();
  }

  async function refreshAll() {
    const lockData = await api.listLocks();
    setLocks(lockData.locks);
  }

  async function handleCreate(event) {
    event.preventDefault();
    setMessage("");
    const trimmed = secretText.trim();
    if (!trimmed) {
      setMessage("中身を入力してください。");
      return;
    }
    if (!unlockAt) {
      setMessage("開封日時を入力してください。");
      return;
    }

    const parsedPrice = priceAmount === "" ? 500 : Number(priceAmount);
    if (!Number.isInteger(parsedPrice) || parsedPrice <= 0) {
      setMessage("金額は1円以上の整数で入力してください。");
      return;
    }

    try {
      await api.createLock({
        secretText: trimmed,
        unlockAt: localToRFC3339(unlockAt),
        unlockLocal: unlockAt,
        timezoneName: timezoneName(),
        timezoneOffsetMinutes: timezoneOffsetMinutes(unlockAt),
        priceAmount: parsedPrice
      });
      setSecretText("");
      setUnlockAt(defaultUnlockTime());
      setUnlockAtTouched(false);
      setPriceAmount("500");
      setMessage("ロックを作成しました。");
      await refreshAll();
    } catch (error) {
      setMessage(error.message);
    }
  }

  async function handlePay(lock) {
    setMessage("");
    try {
      const result = await api.createCheckout(lock.id);
      if (result.mode === "stripe" && result.checkoutUrl) {
        window.location.href = result.checkoutUrl;
        return;
      }
      if (result.mode === "already_open") {
        setMessage("このロックはすでに開封できます。");
        await refreshAll();
        return;
      }
      setMessage(`${formatPrice(lock.priceAmount, lock.currency)} のデモ購入で開封しました。`);
      await refreshAll();
    } catch (error) {
      setMessage(error.message);
    }
  }

  function requestDelete(kind, item) {
    setDeleteText("");
    setDeleteDialog({ kind, item });
  }

  async function confirmDelete() {
    if (deleteText !== "削除する" || !deleteDialog) return;
    setMessage("");
    try {
      if (deleteDialog.kind === "lock") {
        await api.remove(deleteDialog.item.id);
        setMessage("ロックを削除しました。");
      }
      setDeleteDialog(null);
      setDeleteText("");
      await refreshAll();
    } catch (error) {
      setMessage(error.message);
    }
  }

  return (
    <main className="app-shell">
      <section className="hero">
        <div>
          <p className="eyebrow">Time or Money</p>
        </div>
        <div className="stats" aria-label="ロック統計">
          <span>{locks.length} 件</span>
          <span>{openCount} 開封可能</span>
          <span>{dbProvider}</span>
          <span>{stripeEnabled ? "Stripe test ON" : "Demo payment"}</span>
        </div>
      </section>

      <section className="layout">
        <form className="create-panel" onSubmit={handleCreate}>
          <div>
            <p className="eyebrow">Create</p>
            <h2>新しいロック</h2>
          </div>

          <label className="field">
            <span>中身</span>
            <textarea
              value={secretText}
              onChange={(event) => setSecretText(event.target.value)}
              rows="8"
              placeholder="未来の自分、または支払った人だけが読めるテキスト"
            />
          </label>

          <div className="field-row">
            <label className="field">
              <span>開封日時</span>
              <input
                type="datetime-local"
                value={unlockAt}
                onChange={(event) => {
                  setUnlockAtTouched(true);
                  setUnlockAt(event.target.value);
                }}
              />
            </label>
            <label className="field">
              <span>金額</span>
              <input
                type="number"
                min="1"
                step="1"
                value={priceAmount}
                onChange={(event) => setPriceAmount(event.target.value)}
                placeholder="500"
              />
            </label>
          </div>

          <button className="primary-button" type="submit">ロックを作る</button>
          {message && <p className="message">{message}</p>}
        </form>

        <section className="list-panel">
          <div className="section-heading">
            <div>
              <p className="eyebrow">Locks</p>
              <h2>ロック一覧</h2>
            </div>
            <button className="ghost-button" type="button" onClick={refreshAll}>更新</button>
          </div>

          {loading ? (
            <p className="empty">読み込み中...</p>
          ) : locks.length === 0 ? (
            <p className="empty">まだロックはありません。</p>
          ) : (
            <div className="lock-list">
              {locks.map((lock) => {
                const canOpenByTime = lock.isOpen || getRemaining(lock.unlockAt) <= 0;
                const visible = lock.unlocked || canOpenByTime;
                return (
                  <article className="lock-card" key={lock.id}>
                    <div className="lock-card-header">
                      <div>
                        <span className={`status ${visible ? "open" : "locked"}`}>{statusFor(lock)}</span>
                        <h3>Lock #{lock.id}</h3>
                      </div>
                      <strong>{formatPrice(lock.priceAmount, lock.currency)}</strong>
                    </div>

                    <dl className="meta-grid">
                      <div>
                        <dt>開封日時</dt>
                        <dd>{formatUnlockTime(lock)}</dd>
                      </div>
                      <div>
                        <dt>残り</dt>
                        <dd>{formatRemaining(getRemaining(lock.unlockAt))}</dd>
                      </div>
                    </dl>

                    {visible ? (
                      <pre className="secret-text">{lock.secretText}</pre>
                    ) : (
                      <p className="locked-copy">中身はまだ隠れています。時間まで待つか、設定金額で開けられます。</p>
                    )}

                    <div className="card-actions">
                      {!visible && (
                        <button className="pay-button" type="button" onClick={() => handlePay(lock)}>
                          {stripeEnabled ? "Stripeテスト決済へ" : `${formatPrice(lock.priceAmount, lock.currency)} でデモ開封`}
                        </button>
                      )}
                      <button className="danger-button" type="button" onClick={() => requestDelete("lock", lock)}>削除</button>
                    </div>
                  </article>
                );
              })}
            </div>
          )}
        </section>
      </section>

      {deleteDialog && (
        <div className="modal-backdrop" role="presentation">
          <div className="confirm-modal" role="dialog" aria-modal="true" aria-labelledby="delete-title">
            <p className="eyebrow">Delete</p>
            <h2 id="delete-title">本当に削除しますか</h2>
            <p className="locked-copy">
              Lock #{deleteDialog.item.id} をDBから削除します。
              続けるには「削除する」と入力してください。
            </p>
            <input value={deleteText} onChange={(event) => setDeleteText(event.target.value)} placeholder="削除する" autoFocus />
            <div className="card-actions">
              <button className="danger-button" type="button" disabled={deleteText !== "削除する"} onClick={confirmDelete}>削除を確定</button>
              <button className="ghost-button" type="button" onClick={() => setDeleteDialog(null)}>キャンセル</button>
            </div>
          </div>
        </div>
      )}
    </main>
  );
}

ReactDOM.createRoot(document.getElementById("root")).render(<App />);
