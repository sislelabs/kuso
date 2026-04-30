// Package types holds shared response shapes for the kuso server REST API.
//
// We deliberately keep these narrow — only the fields kuso-mcp tools need
// to expose to agents. Adding a field here is cheap; carrying every nested
// struct from the server's IApp interface is not.
package types

// App is a slim view of the kuso server's IApp.
type App struct {
	Name               string `json:"name"`
	Pipeline           string `json:"pipeline"`
	Phase              string `json:"phase"`
	Sleep              string `json:"sleep"` // "enabled" | "disabled" | empty
	Branch             string `json:"branch"`
	Buildpack          string `json:"buildpack,omitempty"`
	BuildStrategy      string `json:"buildstrategy,omitempty"`
	DeploymentStrategy string `json:"deploymentstrategy,omitempty"`
	Autodeploy         bool   `json:"autodeploy,omitempty"`
	Autoscale          bool   `json:"autoscale,omitempty"`

	Image AppImage `json:"image"`
	Web   AppScale `json:"web"`

	Domain  string   `json:"domain,omitempty"`
	Domains []string `json:"-"` // populated by tools when ingress hosts are split
}

// AppImage is the slim image block.
type AppImage struct {
	Repository    string `json:"repository,omitempty"`
	Tag           string `json:"tag,omitempty"`
	ContainerPort int    `json:"containerPort,omitempty"`
}

// AppScale is the slim web/worker scale block.
type AppScale struct {
	ReplicaCount int        `json:"replicaCount"`
	Autoscaling  Autoscaler `json:"autoscaling"`
}

// Autoscaler mirrors the HPA-shaped subobject inside AppScale.
type Autoscaler struct {
	MinReplicas                    int `json:"minReplicas,omitempty"`
	MaxReplicas                    int `json:"maxReplicas,omitempty"`
	TargetCPUUtilizationPercentage int `json:"targetCPUUtilizationPercentage,omitempty"`
}
