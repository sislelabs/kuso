package kusoApi

import "github.com/go-resty/resty/v2"

// TestNotificationEventType sends a synthetic built-in event (build.failed,
// pod.crashed, …) to every channel a real one would reach. project, when
// set, tests that project's routing.
func (k *KusoClient) TestNotificationEventType(eventType, project string) (*resty.Response, error) {
	k.client.SetBody(map[string]string{"project": project})
	return k.client.Post("/api/notifications/event-types/" + esc(eventType) + "/test")
}

// LatestNotificationDeliveries reads the newest delivery per event type,
// alert rule and channel.
func (k *KusoClient) LatestNotificationDeliveries() (*resty.Response, error) {
	return k.client.Get("/api/notifications/deliveries/latest")
}
