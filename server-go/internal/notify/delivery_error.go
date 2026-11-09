package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"regexp"
	"strings"
)

var upstreamStatusRe = regexp.MustCompile(`\bupstream (\d{3})\b`)

// SafeDeliveryError maps a delivery error to a fixed, caller-safe
// category. Raw errors carry the channel URL (a Slack/Discord webhook URL
// is the secret itself), request headers or the receiver's response body,
// so nothing from err's text is passed through except an HTTP status code.
func SafeDeliveryError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	var dnsErr *net.DNSError
	var netErr net.Error
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var recErr tls.RecordHeaderError
	switch {
	case upstreamStatusRe.MatchString(msg):
		return "receiver returned HTTP " + upstreamStatusRe.FindStringSubmatch(msg)[1]
	case errors.As(err, &dnsErr) || strings.Contains(msg, "no such host"):
		return "DNS lookup failed"
	case errors.As(err, &certErr) || errors.As(err, &unknownAuth) || errors.As(err, &hostErr) || errors.As(err, &recErr) ||
		strings.Contains(msg, "x509:") || strings.Contains(msg, "tls:"):
		return "TLS error"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) ||
		strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "refusing to dial") || strings.Contains(msg, "no route to host") || strings.Contains(msg, "dial "):
		return "couldn't connect"
	case strings.Contains(msg, "channel needs") || strings.Contains(msg, "has no webhook url") || strings.Contains(msg, "no valid recipients") || strings.Contains(msg, "is disabled"):
		return "channel is misconfigured"
	case strings.HasPrefix(msg, "smtp"):
		return "mail server refused the message"
	default:
		return "delivery failed"
	}
}
