-- Seed an instance-wide certificate alert. cert-manager failures are
-- silent otherwise: a renewal that keeps failing leaves every pod green
-- until the cert expires and browsers start refusing the site.
--
-- cert_expiry fires when any env TLS cert expires within thresholdInt
-- days or its Certificate has been not-Ready past the issuance grace.
-- 14 days: cert-manager renews Let's Encrypt certs 30 days out, so
-- this means renewal has been failing for over two weeks.
--
-- Runs once (numbered migration), so deleting the rule sticks. Skipped
-- when the operator already has a cert_expiry rule.
INSERT INTO "AlertRule"
    ("id","name","enabled","kind","project","service","env","query","thresholdInt","windowSeconds","severity","throttleSeconds")
SELECT 'default-cert-expiry', 'TLS certificates', true, 'cert_expiry', '', '', '', '', 14, 300, 'warn', 3600
WHERE NOT EXISTS (SELECT 1 FROM "AlertRule" WHERE "kind" = 'cert_expiry');
