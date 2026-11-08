# Log generator for shop/worker: an INFO line every 10s, a WARN every 40s,
# and an ERROR with a multi-line stack every 60s (alternating Go-style
# "at" frames and a Python traceback, so the Errors tab gets two groups).
ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
n=0
while true; do
  n=$((n + 1))
  echo "$(ts) INFO processed $((RANDOM % 20 + 1)) orders from queue=orders in $((RANDOM % 200 + 20))ms"
  if [ $((n % 4)) -eq 0 ]; then
    echo "$(ts) WARN redis: slow reply from cache ($((RANDOM % 400 + 250))ms) retrying"
  fi
  if [ $((n % 6)) -eq 0 ]; then
    if [ $((n % 12)) -eq 0 ]; then
      echo "$(ts) ERROR failed to send order receipt order_id=$((1000 + n))"
      echo "Traceback (most recent call last):"
      echo '  File "/app/jobs/emails.py", line 42, in send_receipt'
      echo "    smtp.send(msg)"
      echo "smtplib.SMTPServerDisconnected: Connection unexpectedly closed"
    else
      echo "$(ts) ERROR payment webhook failed: stripe: read: connection reset by peer order_id=$((1000 + n))"
      echo "    at processWebhook (/app/jobs/payments.go:88)"
      echo "    at runJob (/app/queue/worker.go:141)"
      echo "    at main.loop (/app/main.go:52)"
    fi
  fi
  sleep 10
done
