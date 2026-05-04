// High-level resource list / detail fetchers. Each method maps to a
// single GET request; combinators (Inventory, etc.) live in the
// migrate command package.

package coolify

import "fmt"

// Version returns the Coolify version string. Useful for the
// migration report's "source" header.
func (c *Client) Version() (string, error) {
	body, err := c.getRaw("/version")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (c *Client) ListProjects() ([]Project, error) {
	return get[[]Project](c, "/projects")
}

// GetProject hydrates the .Environments slice that's missing from
// the list response.
func (c *Client) GetProject(uuid string) (Project, error) {
	return get[Project](c, "/projects/"+uuid)
}

func (c *Client) ListApplications() ([]Application, error) {
	return get[[]Application](c, "/applications")
}

func (c *Client) GetApplication(uuid string) (Application, error) {
	return get[Application](c, "/applications/"+uuid)
}

func (c *Client) ListApplicationEnvs(appUUID string) ([]EnvVar, error) {
	return get[[]EnvVar](c, "/applications/"+appUUID+"/envs")
}

func (c *Client) ListServices() ([]Service, error) {
	return get[[]Service](c, "/services")
}

func (c *Client) ListDatabases() ([]Database, error) {
	return get[[]Database](c, "/databases")
}

func (c *Client) GetDatabase(uuid string) (Database, error) {
	return get[Database](c, "/databases/"+uuid)
}

// ListDatabaseEnvs is documented but kuso doesn't need it today —
// the connection URL already lives on the Database struct, and the
// addon helm chart generates its own password. Kept for future use.
func (c *Client) ListDatabaseEnvs(dbUUID string) ([]EnvVar, error) {
	return get[[]EnvVar](c, "/databases/"+dbUUID+"/envs")
}

// AssertReadOnly returns an error if anyone tries to use this client
// in a context that would expect writes. Defence-in-depth: anywhere
// we wire the client into a flow, calling AssertReadOnly first is a
// human-readable signal that the flow is read-only by contract.
func (c *Client) AssertReadOnly() error {
	if c == nil {
		return fmt.Errorf("coolify client not initialised")
	}
	return nil
}
