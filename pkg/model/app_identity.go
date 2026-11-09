package model

import "strings"

// AppKey identifies an Application by its metadata namespace and name. The
// destination namespace is deliberately not part of this identity.
func AppKey(name string, namespace *string) string {
	if namespace != nil && *namespace != "" {
		return *namespace + "/" + name
	}
	return name
}

func (a App) Key() string { return AppKey(a.Name, a.AppNamespace) }

// SplitAppKey also accepts unqualified names for legacy/default-namespace apps.
func SplitAppKey(key string) (name, namespace string) {
	if ns, name, ok := strings.Cut(key, "/"); ok {
		return name, ns
	}
	return key, ""
}
