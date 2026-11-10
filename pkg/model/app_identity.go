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

func ContextKey(contextName, localKey string) string {
	if contextName != "" {
		return contextName + "::" + localKey
	}
	return localKey
}

func SplitContextKey(key string) (contextName, localKey string) {
	// Application namespace/name cannot contain ':'. The last separator also
	// handles saved context names containing IPv6 addresses.
	if i := strings.LastIndex(key, "::"); i >= 0 {
		return key[:i], key[i+2:]
	}
	return "", key
}

func (a App) Key() string { return ContextKey(a.ContextName, AppKey(a.Name, a.AppNamespace)) }

func (a App) ApplicationSetKey() string {
	if a.ApplicationSet == nil {
		return ""
	}
	return ContextKey(a.ContextName, AppKey(*a.ApplicationSet, a.AppNamespace))
}

// SplitAppKey also accepts unqualified names for legacy/default-namespace apps.
func SplitAppKey(key string) (name, namespace string) {
	if ns, name, ok := strings.Cut(key, "/"); ok {
		return name, ns
	}
	return key, ""
}
