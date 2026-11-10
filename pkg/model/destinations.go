package model

// DestinationServer contains only display metadata, never cluster credentials.
type DestinationServer struct {
	ContextName     string `json:"contextName,omitempty"`
	Name            string `json:"name"`
	Server          string `json:"server"`
	Project         string `json:"project,omitempty"`
	ConnectionState struct {
		Status string `json:"status"`
	} `json:"connectionState"`
}

func (s DestinationServer) Key() string {
	if s.Project != "" {
		return ContextKey(s.ContextName, s.Project+"@"+s.Server)
	}
	return ContextKey(s.ContextName, s.Server)
}

// ResolveDestination refuses ambiguous names instead of assigning an app to a
// different project's cluster. An explicit destination URL remains authoritative.
func ResolveDestination(app App, servers []DestinationServer) (DestinationServer, bool) {
	var found DestinationServer
	count := 0
	for _, server := range servers {
		if app.ContextName != server.ContextName {
			continue
		}
		if server.Project != "" && (app.Project == nil || server.Project != *app.Project) {
			continue
		}
		matches := app.DestinationServer != "" && server.Server == app.DestinationServer
		if app.DestinationServer == "" {
			matches = app.DestinationName != "" && app.DestinationName == server.Name
		}
		if matches {
			found = server
			count++
		}
	}
	return found, count == 1
}
