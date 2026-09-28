package notify

// AbsoluteURL resolves a dashboard path ("/projects/…") against the
// public base URL (KUSO_PUBLIC_URL, else https://$KUSO_DOMAIN), the same
// way notification links are built. "" when no base is configured.
func AbsoluteURL(path string) string { return absoluteURL(path) }
