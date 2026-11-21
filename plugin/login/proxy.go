package login

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// NormalizeLoginProxy accepts the native runtime and OAuth client transports.
// An empty value inherits the next configured level.
func NormalizeLoginProxy(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if strings.EqualFold(value, "direct") || strings.EqualFold(value, "off") || strings.EqualFold(value, "none") {
		return "direct", nil
	}
	invalid := errors.New("invalid_proxy")
	if len(value) > 4096 || strings.ContainsAny(value, "\r\n\t\x00 ") {
		return "", invalid
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.Hostname() == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(value, "#") {
		return "", invalid
	}
	switch parsed.Scheme {
	case "http", "https":
	case "socks5", "socks5h":
	default:
		return "", invalid
	}
	if port := parsed.Port(); port != "" {
		number, errPort := strconv.Atoi(port)
		if errPort != nil || number < 1 || number > 65535 {
			return "", invalid
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return "", invalid
	} else if parsed.Scheme == "socks5" || parsed.Scheme == "socks5h" {
		parsed.Host = net.JoinHostPort(parsed.Hostname(), "1080")
	}
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		if parsed.User.Username() == "" || strings.IndexFunc(parsed.User.Username()+password, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			return "", invalid
		}
	}
	parsed.Path = ""
	return parsed.String(), nil
}

func (s *Service) effectiveLoginProxy(override, targetID string) (string, string, error) {
	value, _ := s.loginProxy.Load().(string)
	var bound string
	if strings.TrimSpace(override) == "" && value == "" && targetID != "" {
		if target, ok := s.opts.Manager.GetByID(targetID); ok {
			bound = target.ProxyURL
		}
	}
	return displayLoginProxy(override, value, bound, s.opts.Config.ProxyURL)
}

func displayLoginProxy(override, defaultProxy, bound, cpaProxy string) (string, string, error) {
	value, source := strings.TrimSpace(override), "account"
	if value == "" {
		value, source = defaultProxy, "login_default"
	}
	if value == "" {
		value, source = bound, "credential"
	}
	if value == "" {
		value, source = cpaProxy, "cpa"
	}
	if strings.TrimSpace(value) == "" {
		value, source = "direct", "direct"
	}
	normalized, err := NormalizeLoginProxy(value)
	if err != nil {
		return value, source, err
	}
	return normalized, source, nil
}
