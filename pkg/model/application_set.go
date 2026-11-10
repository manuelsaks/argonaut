package model

// ApplicationSet is a registry entry, independent of its generated Applications.
// Empty sets and sets with generator errors must still be visible.
type ApplicationSet struct {
	ContextName string
	Name        string
	Namespace   string
}

func (s ApplicationSet) Key() string {
	return ContextKey(s.ContextName, AppKey(s.Name, &s.Namespace))
}
