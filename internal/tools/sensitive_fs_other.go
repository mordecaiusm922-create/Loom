//go:build !windows && !darwin

package tools

func isCaseInsensitiveFS() bool { return false }
