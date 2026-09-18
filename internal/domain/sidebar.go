package domain

// SidebarLayout stores presentation only; agent settings and conversations are
// independent of section membership and order.
type SidebarLayout struct {
	Revision            int64            `json:"revision"`
	Sections            []SidebarSection `json:"sections"`
	UnsectionedAgentIDs []string         `json:"unsectionedAgentIds"`
}

type SidebarSection struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	AgentIDs []string `json:"agentIds"`
}
