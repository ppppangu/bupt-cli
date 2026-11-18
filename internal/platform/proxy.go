package platform

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"

	"bupt.local/cli/internal/domain"
)

var proxyPath = regexp.MustCompile(`^/(https?|wss?)(-\d+)?/([0-9a-fA-F]{34,})(/.*)?$`)

// ProxyURL reproduces Wengine's hostname encryption and protocol/port path.
// GatewayURL is an HTTP(S) origin; target may use HTTP(S) or WS(S). Already
// proxied target URLs are returned unchanged, without double encryption.
func ProxyURL(gateway, target string) (string, error) {
	g, e := url.Parse(gateway)
	if e != nil || g.Host == "" || g.User != nil || (g.Scheme != "http" && g.Scheme != "https") {
		return "", domain.E("CONFIG", "invalid WebVPN gateway URL")
	}
	u, e := url.Parse(target)
	if e != nil || u.Host == "" || u.User != nil {
		return "", domain.E("CONFIG", "invalid WebVPN target URL")
	}
	switch u.Scheme {
	case "http", "https", "ws", "wss":
	default:
		return "", domain.E("CONFIG", "unsupported WebVPN target protocol")
	}
	if strings.EqualFold(u.Host, g.Host) && proxyPath.MatchString(u.EscapedPath()) {
		return target, nil
	}
	key := []byte("wrdvpnisthebest!")
	block, _ := aes.NewCipher(key)
	plain := []byte(strings.ToLower(u.Hostname()))
	encrypted := make([]byte, len(plain))
	cipher.NewCFBEncrypter(block, key).XORKeyStream(encrypted, plain)
	protocol := u.Scheme
	port := u.Port()
	if (protocol == "http" || protocol == "ws") && port == "80" {
		port = ""
	}
	if (protocol == "https" || protocol == "wss") && port == "443" {
		port = ""
	}
	if port != "" {
		protocol += "-" + port
	}
	prefix := "/" + protocol + "/" + hex.EncodeToString(key) + hex.EncodeToString(encrypted)
	scheme := g.Scheme
	if u.Scheme == "ws" || u.Scheme == "wss" {
		if scheme == "https" {
			scheme = "wss"
		} else {
			scheme = "ws"
		}
	}
	result := scheme + "://" + g.Host + prefix + u.EscapedPath()
	if u.RawQuery != "" {
		result += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		result += "#" + u.EscapedFragment()
	}
	return result, nil
}

func (c *Client) websocketEndpoint(path string) (*url.URL, error) {
	u, e := url.Parse(c.endpoint(path))
	if e != nil {
		return nil, e
	}
	if c.gateway != nil && u.Host == c.gateway.Host {
		matches := proxyPath.FindStringSubmatch(u.EscapedPath())
		if len(matches) > 0 {
			protocol := matches[1]
			switch protocol {
			case "https":
				protocol = "wss"
			case "http":
				protocol = "ws"
			}
			escaped := "/" + protocol + matches[2] + "/" + matches[3] + matches[4]
			decoded, e := url.PathUnescape(escaped)
			if e != nil {
				return nil, e
			}
			u.Path = decoded
			u.RawPath = escaped
		}
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else if u.Scheme == "http" {
		u.Scheme = "ws"
	}
	return u, nil
}
